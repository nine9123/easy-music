// Package server is easy-music's HTTP API and embedded web UI.
package server

import (
	"compress/gzip"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nine9123/easy-music/internal/covers"
	"github.com/nine9123/easy-music/internal/hotcache"
	"github.com/nine9123/easy-music/internal/library"
	"github.com/nine9123/easy-music/internal/mix"
	"github.com/nine9123/easy-music/internal/radio"
	"github.com/nine9123/easy-music/internal/state"
	"github.com/nine9123/easy-music/internal/youtube"
)

//go:embed web
var webFS embed.FS

// Server wires the components to HTTP.
type Server struct {
	lib    *library.Library
	st     *state.Store
	covers *covers.Covers
	hot    *hotcache.Cache
	yt     *youtube.Importer // nil without a download dir
	radio  *http.Client

	mixMu    sync.Mutex
	mixer    *mix.Mixer
	mixLib   int64
	mixState int64
	mixAt    time.Time

	rescans atomic.Int32 // rescan requests running or waiting for scanMu

	mux *http.ServeMux
}

// New creates the server. yt may be nil.
func New(lib *library.Library, st *state.Store, cv *covers.Covers, hot *hotcache.Cache, yt *youtube.Importer) *Server {
	s := &Server{lib: lib, st: st, covers: cv, hot: hot, yt: yt, mux: http.NewServeMux()}
	// Streams are endless: no overall timeout, but give up on a station that
	// doesn't answer. SHOUTcast servers reply "ICY 200 OK", which net/http
	// rejects unless the status line is rewritten.
	s.radio = &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           radio.DialICY((&net.Dialer{Timeout: 10 * time.Second}).DialContext),
		ResponseHeaderTimeout: 15 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
	}}
	s.routes()
	return s
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	m := s.mux
	// For idlegate's health= option and for anyone else who asks.
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })

	m.HandleFunc("GET /api/status", s.status)
	m.HandleFunc("POST /api/scan", s.scan)
	m.HandleFunc("POST /api/now-playing", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	m.HandleFunc("GET /api/tracks", s.listTracks)
	m.HandleFunc("GET /api/tracks/{id}", s.getTrack)
	m.HandleFunc("GET /api/tracks/{id}/stream", s.streamTrack)
	m.HandleFunc("GET /api/tracks/{id}/video", s.streamVideo)
	m.HandleFunc("GET /api/tracks/{id}/cover", s.cover)
	m.HandleFunc("POST /api/tracks/{id}/played", s.played)
	m.HandleFunc("POST /api/tracks/{id}/skipped", s.skipped)
	m.HandleFunc("PUT /api/tracks/{id}/favorite", s.favorite(true))
	m.HandleFunc("DELETE /api/tracks/{id}/favorite", s.favorite(false))
	m.HandleFunc("GET /api/albums", s.listAlbums)
	m.HandleFunc("GET /api/artists", s.listArtists)
	m.HandleFunc("GET /api/stats", s.stats)
	m.HandleFunc("POST /api/mix", s.mix)

	m.HandleFunc("GET /api/playlists", s.listPlaylists)
	m.HandleFunc("POST /api/playlists", s.createPlaylist)
	m.HandleFunc("GET /api/playlists/{id}", s.getPlaylist)
	m.HandleFunc("PATCH /api/playlists/{id}", s.updatePlaylist)
	m.HandleFunc("DELETE /api/playlists/{id}", s.deletePlaylist)
	m.HandleFunc("POST /api/playlists/{id}/tracks", s.addToPlaylist)

	m.HandleFunc("GET /api/stations", s.listStations)
	m.HandleFunc("POST /api/stations", s.addStation)
	m.HandleFunc("PATCH /api/stations/{id}", s.updateStation)
	m.HandleFunc("DELETE /api/stations/{id}", s.deleteStation)
	m.HandleFunc("GET /api/stations/{id}/stream", s.streamStation)
	m.HandleFunc("POST /api/stations/{id}/played", s.stationPlayed)

	m.HandleFunc("GET /api/downloads", s.listDownloads)
	m.HandleFunc("POST /api/downloads", s.addDownload)
	m.HandleFunc("POST /api/downloads/{id}/retry", s.retryDownload)
	m.HandleFunc("DELETE /api/downloads/{id}", s.deleteDownload)

	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	m.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
}

// OnScanned is called after each library scan: drop hot-cache copies of
// files that are gone or changed.
func (s *Server) OnScanned() {
	s.hot.Prune(func(id string, size, mtime int64) bool {
		t, ok := s.lib.Get(id)
		return ok && t.Size == size && t.ModTime == mtime
	})
}

// WarmHotCache queues every track played often enough, hottest first. Run at
// start so the cache catches up on whatever changed while we were stopped.
func (s *Server) WarmHotCache() {
	stats := s.st.Stats()
	type c struct {
		t     *library.Track
		plays int
		score float64
	}
	var cands []c
	for id, st := range stats {
		if t, ok := s.lib.Get(id); ok {
			cands = append(cands, c{t, st.Plays, s.HotScore(id)})
		}
	}
	sortBy(cands, func(a, b c) bool { return a.score > b.score })
	for _, x := range cands {
		s.considerHot(x.t, x.plays)
	}
}

// HotScore ranks tracks for the hot cache: plays, decaying with time since
// the last one (a month without listening halves it).
func (s *Server) HotScore(id string) float64 {
	st := s.st.StatsFor(id)
	days := time.Since(time.Unix(st.LastPlayed, 0)).Hours() / 24
	return float64(st.Plays) / (1 + math.Max(days, 0)/30)
}

func (s *Server) considerHot(t *library.Track, plays int) {
	abs, ok := s.lib.Abs(t.Root, t.Path)
	if !ok {
		return
	}
	s.hot.Consider(hotcache.Source{ID: t.ID, Path: abs, Size: t.Size, ModTime: t.ModTime}, plays)
}

// ImportDone is the youtube.DoneFunc: index the new files, then add the
// track to the job's playlist.
func (s *Server) ImportDone(ctx context.Context, job state.Job, audioRel string) (string, error) {
	if err := s.lib.ScanRoots(ctx, library.DownloadsRoot); err != nil {
		return "", err
	}
	s.OnScanned()
	id := s.lib.TrackID(library.DownloadsRoot, audioRel)
	if _, ok := s.lib.Get(id); !ok {
		return "", errors.New("downloaded file was not picked up by the library scan")
	}
	if job.PlaylistID != "" {
		_, err := s.st.UpdatePlaylist(job.PlaylistID, func(p *state.Playlist) {
			if !containsStr(p.Tracks, id) {
				p.Tracks = append(p.Tracks, id)
			}
		})
		if err != nil {
			log.Printf("import: playlist %s: %v", job.PlaylistID, err)
		}
	}
	return id, nil
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, r *http.Request, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if len(b) > 512 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		w.WriteHeader(code)
		gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		gz.Write(b)
		gz.Close()
		return
	}
	w.WriteHeader(code)
	w.Write(b)
}

func writeError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	writeJSON(w, r, code, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func notFoundOr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not found")
		return
	}
	writeError(w, r, http.StatusInternalServerError, err.Error())
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
