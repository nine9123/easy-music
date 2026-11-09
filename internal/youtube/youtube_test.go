package youtube

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nine9123/easy-music/internal/state"
)

type harness struct {
	im   *Importer
	st   *state.Store
	dir  string
	mu   sync.Mutex
	done []string // audioRel of finished jobs
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	bin, _ := filepath.Abs("testdata/fake-yt-dlp")
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{st: st, dir: t.TempDir()}
	h.im = New(bin, nil, h.dir, st, func(ctx context.Context, j state.Job, audioRel string) (string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.done = append(h.done, audioRel)
		return "track-" + audioRel, nil
	})
	return h
}

func waitJob(t *testing.T, st *state.Store, id string, want string) state.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range st.Jobs() {
			if j.ID == id && j.Status == want {
				return j
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %s: %+v", id, want, st.Jobs())
	return state.Job{}
}

func TestImport(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.im.Run(ctx)

	if _, err := h.im.Enqueue("javascript:alert(1)", "", false); err == nil {
		t.Errorf("non-http URL accepted")
	}
	j, err := h.im.Enqueue("https://www.youtube.com/watch?v=vid123", "pl1", false)
	if err != nil {
		t.Fatal(err)
	}
	got := waitJob(t, h.st, j.ID, state.JobDone)
	if got.TrackID != "track-youtube/vid123.m4a" || got.Title != "Artist - Song (Official Video)" || got.PlaylistID != "pl1" {
		t.Errorf("job = %+v", got)
	}
	for _, f := range []string{"vid123.mp4", "vid123.m4a", "vid123.info.json"} {
		if _, err := os.Stat(filepath.Join(h.dir, Subdir, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}

	// Importing the same video again doesn't download, just links.
	j2, _ := h.im.Enqueue("https://youtu.be/vid123", "", false)
	waitJob(t, h.st, j2.ID, state.JobDone)
	if len(h.done) != 2 {
		t.Errorf("done called %d times", len(h.done))
	}

	bad, _ := h.im.Enqueue("https://youtu.be/fail", "", false)
	if j := waitJob(t, h.st, bad.ID, state.JobFailed); !strings.Contains(j.Error, "Video unavailable") {
		t.Errorf("error not surfaced: %q", j.Error)
	}
}

func TestImportPlaylist(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.im.Run(ctx)

	j, err := h.im.Enqueue("https://www.youtube.com/playlist?list=PL1", "pl1", true)
	if err != nil {
		t.Fatal(err)
	}
	got := waitJob(t, h.st, j.ID, state.JobDone)
	if got.Title != "My Mix (3 videos)" || got.Entries != 3 || got.TrackID != "" {
		t.Errorf("playlist job = %+v", got)
	}
	// One job per resolvable entry, in playlist order, into the same
	// playlist; the unresolvable private video is skipped.
	var entries []state.Job
	for _, e := range h.st.Jobs() { // newest first
		if e.ID != j.ID {
			entries = append([]state.Job{e}, entries...)
		}
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	for i, id := range []string{"plA", "plB", "plC"} {
		e := waitJob(t, h.st, entries[i].ID, state.JobDone)
		if e.URL != "https://www.youtube.com/watch?v="+id || e.PlaylistID != "pl1" || e.WholePlaylist || e.TrackID != "track-youtube/"+id+".m4a" {
			t.Errorf("entry %d = %+v", i, e)
		}
	}
	h.mu.Lock()
	if strings.Join(h.done, ",") != "youtube/plA.m4a,youtube/plB.m4a,youtube/plC.m4a" {
		t.Errorf("imported in order %v", h.done)
	}
	h.mu.Unlock()

	// A plain video URL with the option set is a one-video "playlist".
	one, _ := h.im.Enqueue("https://www.youtube.com/watch?v=solo", "", true)
	if got := waitJob(t, h.st, one.ID, state.JobDone); got.Title != "Playlist (1 video)" || got.Entries != 1 {
		t.Errorf("single video job = %+v", got)
	}

	empty, _ := h.im.Enqueue("https://www.youtube.com/playlist?list=empty", "", true)
	if got := waitJob(t, h.st, empty.ID, state.JobFailed); got.Error != "no videos found at this URL" {
		t.Errorf("empty playlist job = %+v", got)
	}
	bad, _ := h.im.Enqueue("https://www.youtube.com/playlist?list=fail", "", true)
	if got := waitJob(t, h.st, bad.ID, state.JobFailed); !strings.Contains(got.Error, "Video unavailable") {
		t.Errorf("failing playlist job = %+v", got)
	}
}

func TestCancelAndShutdown(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.im.Run(ctx); close(stopped) }()

	j, _ := h.im.Enqueue("https://youtu.be/slow", "", false)
	waitJob(t, h.st, j.ID, state.JobRunning)
	h.im.Cancel(j.ID)
	if got := waitJob(t, h.st, j.ID, state.JobFailed); got.Error != "canceled" {
		t.Errorf("canceled job: %+v", got)
	}

	// Shutdown mid-download: the job goes back to the queue for next time.
	j2, _ := h.im.Enqueue("https://youtu.be/slow2", "", false)
	waitJob(t, h.st, j2.ID, state.JobRunning)
	cancel()
	<-stopped
	waitJob(t, h.st, j2.ID, state.JobQueued)
}

func TestTrackProgress(t *testing.T) {
	var got []float64
	trackProgress(strings.NewReader("[download]  50.0% of 1MiB\n[download] 100.0% of 1MiB\n[download]  20.0%\n[download] 100.0% of 1MiB\n"), func(p float64) { got = append(got, p) })
	if len(got) == 0 || got[len(got)-1] != 99 {
		t.Errorf("progress = %v", got)
	}
	// The second stream must not go backwards to below 50%.
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Errorf("progress went backwards: %v", got)
		}
	}
}
