package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.RecordPlay("t1", 200)
	s.RecordPlay("t1", 100)
	s.RecordSkip("t2")
	s.SetFavorite("t1", true)
	p := s.CreatePlaylist("Mix", []string{"t1", "t2"})
	s.UpdatePlaylist(p.ID, func(p *Playlist) { p.Tracks = append(p.Tracks, "t3") })
	st := s.AddStation("Radio", "http://example.com/stream")
	if again := s.AddStation("Dup", "http://example.com/stream"); again.ID != st.ID {
		t.Errorf("duplicate station URL created a second station")
	}
	j := s.AddJob("https://youtu.be/x", p.ID)
	s.UpdateJob(j.ID, true, func(j *Job) { j.Status = JobRunning })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.StatsFor("t1"); got.Plays != 2 || got.Seconds != 300 || got.LastPlayed == 0 {
		t.Errorf("stats = %+v", got)
	}
	if got := s2.StatsFor("t2"); got.Skips != 1 {
		t.Errorf("skips = %+v", got)
	}
	if _, ok := s2.Favorites()["t1"]; !ok {
		t.Errorf("favorite lost")
	}
	if pl, _ := s2.Playlist(p.ID); len(pl.Tracks) != 3 || pl.Name != "Mix" {
		t.Errorf("playlist = %+v", pl)
	}
	if len(s2.Stations()) != 1 {
		t.Errorf("stations = %+v", s2.Stations())
	}
	// A job that was running when the process stopped is re-queued.
	if next, ok := s2.NextJob(); !ok || next.ID != j.ID {
		t.Errorf("running job not re-queued: %+v", s2.Jobs())
	}
	if h := s2.History(0); len(h) != 2 || h[0].Track != "t1" {
		t.Errorf("history = %+v", h)
	}
}

func TestDebouncedSave(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.debounce = 10 * time.Millisecond
	s.SetFavorite("x", true)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "state.json")); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("state was not saved after a change")
}

func TestCorruptStateIsAnError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "state.json"), []byte("{nope"), 0o644)
	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt state must not be silently replaced")
	}
}
