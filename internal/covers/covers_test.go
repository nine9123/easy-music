package covers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nine9123/easy-music/internal/library"
)

func pngBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func setup(t *testing.T) (*library.Library, string, string) {
	music, cache := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(music, "A", "B"), 0o755)
	os.WriteFile(filepath.Join(music, "A", "B", "folder.png"), pngBytes(1200, 900), 0o644)
	lib, err := library.Open([]library.Root{{Name: "m", Dir: music}}, cache)
	if err != nil {
		t.Fatal(err)
	}
	return lib, music, cache
}

func TestLocalCoverIsDownscaledAndCached(t *testing.T) {
	lib, music, cache := setup(t)
	c, _ := New(filepath.Join(cache, "covers"), lib, false)
	tr := &library.Track{ID: "x", Root: "m", Path: "A/B/1.mp3", Album: "B", Artist: "A", Cover: "A/B/folder.png"}
	p, ok := c.Get(tr)
	if !ok {
		t.Fatal("no cover")
	}
	f, _ := os.Open(p)
	cfg, err := jpeg.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != MaxSize || cfg.Height != 450 {
		t.Errorf("cached cover %dx%d, err %v", cfg.Width, cfg.Height, err)
	}
	// Served from the cache even once the source is gone (HDD asleep).
	os.RemoveAll(filepath.Join(music, "A"))
	if _, ok := c.Get(tr); !ok {
		t.Errorf("cached cover not reused")
	}
}

func TestRemoteLookup(t *testing.T) {
	var searches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ws/2/release/":
			searches.Add(1)
			if !strings.Contains(r.URL.Query().Get("query"), `release:"Known"`) {
				w.Write([]byte(`{"releases":[]}`))
				return
			}
			if r.Header.Get("User-Agent") == "" {
				t.Errorf("no User-Agent sent to MusicBrainz")
			}
			w.Write([]byte(`{"releases":[{"id":"rel1","score":100,"release-group":{"id":"rg1"}}]}`))
		case r.URL.Path == "/release/rel1/front-500":
			http.NotFound(w, r) // fall back to the release group
		case r.URL.Path == "/release-group/rg1/front-500":
			w.Write(pngBytes(10, 10))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	lib, _, cache := setup(t)
	c, _ := New(filepath.Join(cache, "covers"), lib, true)
	c.mb.searchURL, c.mb.caaURL = srv.URL+"/ws/2/release/", srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	known := &library.Track{ID: "k", Root: "m", Path: "k.mp3", Album: "Known", Artist: "Someone"}
	unknown := &library.Track{ID: "u", Root: "m", Path: "u.mp3", Album: "Nope", Artist: "Someone"}
	if _, ok := c.Get(known); ok {
		t.Fatal("remote lookup should not block the request")
	}
	c.Get(unknown)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := c.Get(known); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("remote cover never arrived")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The miss is remembered: asking again doesn't hit MusicBrainz.
	for time.Now().Before(deadline) {
		c.mu.Lock()
		_, missed := c.misses[key(unknown)]
		c.mu.Unlock()
		if missed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	before := searches.Load()
	c.Get(unknown)
	time.Sleep(50 * time.Millisecond)
	if searches.Load() != before {
		t.Errorf("a remembered miss was looked up again")
	}
}

func TestDownscale(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for x := 0; x < 4; x++ {
		for y := 0; y < 2; y++ {
			img.Set(x, y, color.RGBA{uint8(x * 60), 0, 0, 255})
		}
	}
	out := downscale(img, 2)
	if b := out.Bounds(); b.Dx() != 2 || b.Dy() != 1 {
		t.Fatalf("size %v", b)
	}
	if r, _, _, _ := out.At(0, 0).RGBA(); r>>8 != 30 {
		t.Errorf("box filter average = %d, want 30", r>>8)
	}
}
