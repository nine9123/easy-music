package hotcache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func src(t *testing.T, dir, id string, size int) Source {
	t.Helper()
	p := filepath.Join(dir, id+".mp3")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	return Source{ID: id, Path: p, Size: st.Size(), ModTime: st.ModTime().UnixNano()}
}

func waitCached(t *testing.T, c *Cache, s Source) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if _, ok := c.Lookup(s.ID, s.Size, s.ModTime); ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s was not cached", s.ID)
}

func TestCacheAndEvict(t *testing.T) {
	hdd, ssd := t.TempDir(), t.TempDir()
	scores := map[string]float64{"cold": 1, "warm": 5, "hot": 10, "lukewarm": 3}
	c, err := Open(ssd, 250, 2, func(id string) float64 { return scores[id] })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	cold, warm, hot := src(t, hdd, "cold", 100), src(t, hdd, "warm", 100), src(t, hdd, "hot", 100)
	c.Consider(cold, 1) // below minPlays
	c.Consider(cold, 2)
	waitCached(t, c, cold)
	c.Consider(warm, 5)
	waitCached(t, c, warm)

	// No room for hot: the coldest copy goes.
	c.Consider(hot, 10)
	waitCached(t, c, hot)
	if _, ok := c.Lookup("cold", cold.Size, cold.ModTime); ok {
		t.Errorf("cold should have been evicted")
	}
	// lukewarm is colder than everything cached: nothing is evicted for it.
	luke := src(t, hdd, "lukewarm", 100)
	c.Consider(luke, 3)
	time.Sleep(50 * time.Millisecond)
	if _, ok := c.Lookup("lukewarm", luke.Size, luke.ModTime); ok {
		t.Errorf("lukewarm displaced a hotter track")
	}
	if used, _, n := c.Usage(); used != 200 || n != 2 {
		t.Errorf("usage = %d bytes, %d files", used, n)
	}

	// A changed source no longer matches and is pruned.
	if _, ok := c.Lookup("hot", hot.Size+1, hot.ModTime); ok {
		t.Errorf("stale copy matched")
	}
	c.Prune(func(id string, size, mtime int64) bool { return id != "hot" })
	if _, ok := c.Lookup("hot", hot.Size, hot.ModTime); ok {
		t.Errorf("pruned copy still served")
	}

	// Reopen: the index is rebuilt from file names.
	c2, _ := Open(ssd, 250, 2, func(string) float64 { return 0 })
	if p, ok := c2.Lookup("warm", warm.Size, warm.ModTime); !ok || !strings.HasPrefix(p, ssd) {
		t.Errorf("reopened cache lost warm: %q %v", p, ok)
	}
}

func TestDisabled(t *testing.T) {
	c, _ := Open(t.TempDir(), 0, 1, func(string) float64 { return 0 })
	c.Consider(Source{ID: "x", Size: 1}, 100)
	if len(c.queue) != 0 {
		t.Errorf("disabled cache queued work")
	}
}
