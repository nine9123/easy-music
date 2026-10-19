// Package covers finds album art and keeps a downscaled copy on the cache SSD.
//
// Sources, in order: a cover file next to the music (cover.jpg, folder.jpg,
// a yt-dlp thumbnail), the picture embedded in the file, and — if enabled —
// MusicBrainz + Cover Art Archive. Remote lookups never block a request: a
// miss is queued, fetched in the background at MusicBrainz's 1 req/s, and the
// next request for that cover finds it in the cache.
package covers

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/jpeg"
	_ "image/png" // decoder registration for downscaling
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nine9123/easy-music/internal/library"
	"github.com/nine9123/easy-music/internal/tags"
)

// MaxSize is the longest edge of a cached cover.
const MaxSize = 600

// retryAfter is how long a remote miss is remembered.
const retryAfter = 30 * 24 * time.Hour

// Covers is safe for concurrent use.
type Covers struct {
	dir   string
	lib   *library.Library
	fetch bool
	mb    *musicBrainz

	mu     sync.Mutex
	misses map[string]int64 // cover key -> unix time of the last failed remote lookup
	queued map[string]bool
	queue  chan *library.Track
}

// New creates the cover cache in dir. fetch enables remote lookups.
func New(dir string, lib *library.Library, fetch bool) (*Covers, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	c := &Covers{dir: dir, lib: lib, fetch: fetch, mb: newMusicBrainz(), misses: map[string]int64{}, queued: map[string]bool{}, queue: make(chan *library.Track, 512)}
	if b, err := os.ReadFile(c.missesPath()); err == nil {
		json.Unmarshal(b, &c.misses)
	}
	return c, nil
}

func (c *Covers) missesPath() string { return filepath.Join(c.dir, "misses.json") }

// key identifies a cover: the cover file if there is one (shared by an
// album's tracks, unique for a per-track thumbnail), otherwise the album.
func key(t *library.Track) string {
	var k string
	switch {
	case t.Cover != "":
		k = "file:" + t.Root + "/" + t.Cover
	case t.Album != "":
		k = "album:" + t.AlbumKey()
	default:
		k = "track:" + t.ID
	}
	h := sha1.Sum([]byte(k))
	return hex.EncodeToString(h[:10])
}

// Get returns the path of a cached cover for t, creating it from local
// sources if needed. If there is none it queues a remote lookup and returns
// false.
func (c *Covers) Get(t *library.Track) (string, bool) {
	k := key(t)
	p := filepath.Join(c.dir, k+".jpg")
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	if data := c.local(t); data != nil {
		if err := c.store(k, data); err == nil {
			return p, true
		} else {
			log.Printf("covers: %v", err)
		}
	}
	c.enqueue(t, k)
	return "", false
}

func (c *Covers) local(t *library.Track) []byte {
	if t.Cover != "" {
		if abs, ok := c.lib.Abs(t.Root, t.Cover); ok {
			if b, err := os.ReadFile(abs); err == nil {
				return b
			}
		}
	}
	if t.Embedded {
		if abs, ok := c.lib.Abs(t.Root, t.Path); ok {
			if pic, err := tags.ReadPicture(abs); err == nil && pic != nil {
				return pic.Data
			}
		}
	}
	return nil
}

// store downscales JPEG/PNG to MaxSize and writes it as JPEG. Formats the
// standard library can't decode (WebP) are stored as they are; browsers
// sniff the type.
func (c *Covers) store(k string, data []byte) error {
	if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, downscale(img, MaxSize), &jpeg.Options{Quality: 85}); err == nil {
			data = buf.Bytes()
		}
	}
	return library.WriteFileAtomic(filepath.Join(c.dir, k+".jpg"), data)
}

func (c *Covers) enqueue(t *library.Track, k string) {
	if !c.fetch || t.Album == "" || t.Artist == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queued[k] || time.Since(time.Unix(c.misses[k], 0)) < retryAfter {
		return
	}
	select {
	case c.queue <- t:
		c.queued[k] = true
	default:
	}
}

// Run performs queued remote lookups until ctx ends.
func (c *Covers) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-c.queue:
			k := key(t)
			data, err := c.mb.front(ctx, t.SortArtist(), t.Album)
			if ctx.Err() != nil {
				return
			}
			c.mu.Lock()
			delete(c.queued, k)
			if data == nil {
				c.misses[k] = time.Now().Unix()
			}
			b, _ := json.Marshal(c.misses)
			c.mu.Unlock()
			if data != nil {
				if err := c.store(k, data); err != nil {
					log.Printf("covers: %v", err)
				}
			} else {
				if err != nil {
					log.Printf("covers: %s / %s: %v", t.SortArtist(), t.Album, err)
				}
				library.WriteFileAtomic(c.missesPath(), b)
			}
		}
	}
}

// downscale shrinks img so its longest edge is at most max, averaging the
// source pixels that fall into each destination pixel (a box filter: plenty
// for album art, and no dependency on x/image).
func downscale(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return img
	}
	nw, nh := max, h*max/w
	if h > w {
		nw, nh = w*max/h, max
	}
	nw, nh = maxInt(nw, 1), maxInt(nh, 1)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := b.Min.Y+y*h/nh, b.Min.Y+(y+1)*h/nh
		for x := 0; x < nw; x++ {
			x0, x1 := b.Min.X+x*w/nw, b.Min.X+(x+1)*w/nw
			var r, g, bl, a, n uint64
			for sy := y0; sy < maxInt(y1, y0+1); sy++ {
				for sx := x0; sx < maxInt(x1, x0+1); sx++ {
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(r / n >> 8)
			dst.Pix[i+1] = uint8(g / n >> 8)
			dst.Pix[i+2] = uint8(bl / n >> 8)
			dst.Pix[i+3] = uint8(a / n >> 8)
		}
	}
	return dst
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// httpClient is shared by the remote lookups.
var httpClient = &http.Client{Timeout: 20 * time.Second}
