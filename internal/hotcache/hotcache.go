// Package hotcache keeps copies of the most-played tracks on the cache SSD so
// that everyday listening does not spin up (or wear) the HDD.
//
// Cached files are named <id>-<size>-<mtime><ext>. The size and mtime are the
// source file's, as recorded in the library index, so validity is checked
// without touching the HDD at all; a changed source simply stops matching and
// the stale copy is pruned.
package hotcache

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Source describes a library file that could be cached.
type Source struct {
	ID      string
	Path    string // absolute path on the HDD
	Size    int64
	ModTime int64
}

type entry struct {
	file          string
	size, modTime int64
}

// Cache is safe for concurrent use.
type Cache struct {
	dir      string
	max      int64
	minPlays int
	score    func(id string) float64 // eviction priority: higher stays

	mu      sync.Mutex
	entries map[string]entry
	used    int64
	pending map[string]bool
	queue   chan Source
}

// Open indexes an existing cache directory. max == 0 disables caching (Lookup
// still works for nothing, Consider is a no-op).
func Open(dir string, max int64, minPlays int, score func(id string) float64) (*Cache, error) {
	c := &Cache{dir: dir, max: max, minPlays: minPlays, score: score, entries: map[string]entry{}, pending: map[string]bool{}, queue: make(chan Source, 256)}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, de := range des {
		name := de.Name()
		if strings.HasPrefix(name, ".") { // leftover temp file from a stop mid-copy
			os.Remove(filepath.Join(dir, name))
			continue
		}
		id, size, mtime, ok := parseName(name)
		if !ok {
			continue
		}
		c.entries[id] = entry{file: name, size: size, modTime: mtime}
		c.used += size
	}
	return c, nil
}

func fileName(s Source) string {
	return fmt.Sprintf("%s-%d-%d%s", s.ID, s.Size, s.ModTime, strings.ToLower(filepath.Ext(s.Path)))
}

func parseName(name string) (id string, size, mtime int64, ok bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "-")
	if len(parts) != 3 {
		return "", 0, 0, false
	}
	size, err1 := strconv.ParseInt(parts[1], 10, 64)
	mtime, err2 := strconv.ParseInt(parts[2], 10, 64)
	return parts[0], size, mtime, err1 == nil && err2 == nil
}

// Lookup returns the cached copy of a track if it matches the indexed source.
func (c *Cache) Lookup(id string, size, modTime int64) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok || e.size != size || e.modTime != modTime {
		return "", false
	}
	return filepath.Join(c.dir, e.file), true
}

// Usage reports bytes used, the budget and the number of cached tracks.
func (c *Cache) Usage() (used, max int64, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used, c.max, len(c.entries)
}

// Consider queues a track for caching if it has been played often enough.
func (c *Cache) Consider(s Source, plays int) {
	if c.max <= 0 || plays < c.minPlays || s.Size > c.max {
		return
	}
	c.mu.Lock()
	if e, ok := c.entries[s.ID]; (ok && e.size == s.Size && e.modTime == s.ModTime) || c.pending[s.ID] {
		c.mu.Unlock()
		return
	}
	c.pending[s.ID] = true
	c.mu.Unlock()
	select {
	case c.queue <- s:
	default: // queue full: it will be considered again on its next play
		c.mu.Lock()
		delete(c.pending, s.ID)
		c.mu.Unlock()
	}
}

// Prune removes copies whose source is gone or changed.
func (c *Cache) Prune(valid func(id string, size, modTime int64) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, e := range c.entries {
		if !valid(id, e.size, e.modTime) {
			c.removeLocked(id)
		}
	}
}

func (c *Cache) removeLocked(id string) {
	e := c.entries[id]
	if err := os.Remove(filepath.Join(c.dir, e.file)); err != nil && !os.IsNotExist(err) {
		log.Printf("hotcache: %v", err)
	}
	c.used -= e.size
	delete(c.entries, id)
}

// Run copies queued tracks until ctx ends. One at a time: the point is to
// spare the HDD, not to hammer it.
func (c *Cache) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case s := <-c.queue:
			if err := c.add(ctx, s); err != nil && ctx.Err() == nil {
				log.Printf("hotcache: %s: %v", s.Path, err)
			}
			c.mu.Lock()
			delete(c.pending, s.ID)
			c.mu.Unlock()
		}
	}
}

func (c *Cache) add(ctx context.Context, s Source) error {
	if !c.makeRoom(s) {
		return nil // everything cached is hotter than this track
	}
	tmp, err := os.CreateTemp(c.dir, ".copy-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	src, err := os.Open(s.Path)
	if err != nil {
		tmp.Close()
		return err
	}
	n, err := io.Copy(tmp, ctxReader{ctx, src})
	src.Close()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != s.Size {
		return fmt.Errorf("size changed while copying (%d != %d)", n, s.Size)
	}
	name := fileName(s)
	if err := os.Rename(tmp.Name(), filepath.Join(c.dir, name)); err != nil {
		return err
	}
	c.mu.Lock()
	if _, ok := c.entries[s.ID]; ok {
		c.removeLocked(s.ID) // an outdated copy of the same track
	}
	c.entries[s.ID] = entry{file: name, size: s.Size, modTime: s.ModTime}
	c.used += s.Size
	c.mu.Unlock()
	return nil
}

// makeRoom evicts the coldest copies until s fits, but only copies colder
// than s itself. It reports whether s now fits.
func (c *Cache) makeRoom(s Source) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	need := c.used + s.Size - c.max
	if old, ok := c.entries[s.ID]; ok {
		need -= old.size
	}
	if need <= 0 {
		return true
	}
	mine := c.score(s.ID)
	type cand struct {
		id    string
		score float64
		size  int64
	}
	var cands []cand
	for id, e := range c.entries {
		if id == s.ID {
			continue
		}
		if sc := c.score(id); sc < mine {
			cands = append(cands, cand{id, sc, e.size})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score < cands[j].score })
	var freeable int64
	k := 0
	for ; k < len(cands) && freeable < need; k++ {
		freeable += cands[k].size
	}
	if freeable < need {
		return false
	}
	for _, cd := range cands[:k] {
		c.removeLocked(cd.id)
	}
	return true
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
