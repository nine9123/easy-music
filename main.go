// Command easy-music is a small self-hosted music server: a library scanned
// from read-only directories, playlists, favorites and stats, internet radio,
// music videos, YouTube imports and a radio-like "more like this" mode.
//
// It is built to sit behind idlegate (https://github.com/nine9123/idlegate):
// it listens only once its index is loaded, catches up on start (rescan,
// resume imports, refill the hot cache), saves state atomically, and exits
// well within idlegate's stop grace period.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/nine9123/easy-music/internal/config"
	"github.com/nine9123/easy-music/internal/covers"
	"github.com/nine9123/easy-music/internal/hotcache"
	"github.com/nine9123/easy-music/internal/library"
	"github.com/nine9123/easy-music/internal/server"
	"github.com/nine9123/easy-music/internal/state"
	"github.com/nine9123/easy-music/internal/youtube"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	lib, err := library.Open(library.NewRoots(cfg.MusicDirs, cfg.DownloadDir), cfg.CacheDir)
	if err != nil {
		return err
	}
	st, err := state.Open(cfg.StateDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Printf("saving state: %v", err)
		}
	}()
	cv, err := covers.New(filepath.Join(cfg.CacheDir, "covers"), lib, cfg.CoverFetch)
	if err != nil {
		return err
	}
	var srv *server.Server
	hot, err := hotcache.Open(filepath.Join(cfg.CacheDir, "hot"), cfg.HotCacheSize, cfg.HotCacheMinPlays,
		func(id string) float64 { return srv.HotScore(id) })
	if err != nil {
		return err
	}
	var yt *youtube.Importer
	if cfg.DownloadDir != "" {
		yt = youtube.New(cfg.YTDLP, cfg.YTDLPArgs, cfg.DownloadDir, st,
			func(ctx context.Context, j state.Job, rel string) (string, error) { return srv.ImportDone(ctx, j, rel) })
		if !yt.Available() {
			log.Printf("imports disabled: %q not found", cfg.YTDLP)
		}
	}
	srv = server.New(lib, st, cv, hot, yt)

	// Listen as soon as the cached index is loaded: the UI is usable at once
	// and idlegate's wake completes, while the rescan runs in the background.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("easy-music: listening on %s, %d tracks indexed", ln.Addr(), len(lib.All()))

	var wg sync.WaitGroup
	bg := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	bg(func() { cv.Run(ctx) })
	bg(func() { hot.Run(ctx) })
	if yt != nil && yt.Available() {
		bg(func() { yt.Run(ctx) })
	}
	bg(func() { background(ctx, cfg, lib, srv) })

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	// idlegate's default stop grace is 10 s: finish well inside it.
	log.Printf("easy-music: shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(sctx); err != nil {
		httpSrv.Close() // open streams (radio, a long video) don't get to hold us up
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("easy-music: background work did not stop in time")
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// background runs the start-up catch-up and the optional periodic rescan.
func background(ctx context.Context, cfg config.Config, lib *library.Library, srv *server.Server) {
	scan := func() {
		if err := lib.Scan(ctx); err != nil {
			if ctx.Err() == nil {
				log.Printf("scan: %v", err)
			}
			return
		}
		srv.OnScanned()
	}
	if cfg.ScanOnStart || len(lib.All()) == 0 {
		scan()
	}
	srv.WarmHotCache()
	if cfg.ScanInterval <= 0 {
		return
	}
	t := time.NewTicker(cfg.ScanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			scan()
		}
	}
}

// healthcheck is for Docker's HEALTHCHECK: the image has no curl/wget.
func healthcheck() int {
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
