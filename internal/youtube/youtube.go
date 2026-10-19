// Package youtube imports videos (YouTube or anything else yt-dlp supports)
// into the download directory as a local track with its music video.
//
// Each import produces, under <download dir>/youtube/:
//
//	<id>.mp4        the video with audio (H.264/AAC preferred: plays in every browser)
//	<id>.m4a        the audio alone, so listening doesn't stream the video
//	<id>.info.json  yt-dlp's metadata: the library reads title/artist/URL from it
//	<id>.jpg        the thumbnail, used as the cover
//
// The library pairs these by base name, so an import is an ordinary track with
// a music video, and rebuilding the index from scratch recovers it fully.
package youtube

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nine9123/easy-music/internal/state"
)

// Subdir is where imports go, relative to the download directory.
const Subdir = "youtube"

// DoneFunc is called after a successful download with the paths (relative to
// the download directory) of the files produced. It returns the track ID.
type DoneFunc func(ctx context.Context, job state.Job, audioRel string) (trackID string, err error)

// Importer runs one download at a time from the persistent job queue.
type Importer struct {
	bin   string
	extra []string
	dir   string
	st    *state.Store
	done  DoneFunc

	wake chan struct{}

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// New creates an importer writing into dir.
func New(bin string, extra []string, dir string, st *state.Store, done DoneFunc) *Importer {
	return &Importer{bin: bin, extra: extra, dir: dir, st: st, done: done, wake: make(chan struct{}, 1), cancels: map[string]context.CancelFunc{}}
}

// Available reports whether yt-dlp can be found.
func (im *Importer) Available() bool {
	if im == nil || im.dir == "" {
		return false
	}
	_, err := exec.LookPath(im.bin)
	return err == nil
}

// Enqueue validates the URL and queues an import.
func (im *Importer) Enqueue(raw, playlistID string) (state.Job, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return state.Job{}, fmt.Errorf("not an http(s) URL: %q", raw)
	}
	j := im.st.AddJob(u.String(), playlistID)
	im.kick()
	return j, nil
}

func (im *Importer) kick() {
	select {
	case im.wake <- struct{}{}:
	default:
	}
}

// Cancel stops a running job (it is marked failed) or removes a queued or
// finished one.
func (im *Importer) Cancel(id string) error {
	im.mu.Lock()
	cancel := im.cancels[id]
	im.mu.Unlock()
	if cancel != nil {
		cancel()
		return nil
	}
	return im.st.DeleteJob(id)
}

// Retry re-queues a failed job.
func (im *Importer) Retry(id string) error {
	_, err := im.st.UpdateJob(id, true, func(j *state.Job) {
		if j.Status == state.JobFailed {
			j.Status, j.Error, j.Progress = state.JobQueued, "", 0
		}
	})
	im.kick()
	return err
}

// Run processes the queue until ctx ends. A job interrupted by shutdown goes
// back to the queue and is resumed (yt-dlp continues its .part files) on the
// next start — which, behind idlegate, is the next time anyone opens the app.
func (im *Importer) Run(ctx context.Context) {
	for {
		job, ok := im.st.NextJob()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-im.wake:
				continue
			}
		}
		im.run(ctx, job)
		if ctx.Err() != nil {
			return
		}
	}
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (im *Importer) run(parent context.Context, job state.Job) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	im.mu.Lock()
	im.cancels[job.ID] = cancel
	im.mu.Unlock()
	defer func() {
		im.mu.Lock()
		delete(im.cancels, job.ID)
		im.mu.Unlock()
	}()

	update := func(persist bool, fn func(j *state.Job)) { im.st.UpdateJob(job.ID, persist, fn) }
	update(true, func(j *state.Job) { j.Status, j.Error, j.Progress = state.JobRunning, "", 0 })
	log.Printf("import: %s", job.URL)

	trackID, err := im.download(ctx, job, update)
	switch {
	case parent.Err() != nil:
		update(true, func(j *state.Job) { j.Status = state.JobQueued })
	case ctx.Err() != nil:
		update(true, func(j *state.Job) { j.Status, j.Error = state.JobFailed, "canceled" })
	case err != nil:
		log.Printf("import: %s: %v", job.URL, err)
		update(true, func(j *state.Job) { j.Status, j.Error = state.JobFailed, err.Error() })
	default:
		update(true, func(j *state.Job) { j.Status, j.Progress, j.TrackID = state.JobDone, 100, trackID })
	}
}

func (im *Importer) download(ctx context.Context, job state.Job, update func(bool, func(*state.Job))) (string, error) {
	// Ask for the ID first: it names the files, and it fails fast on a bad URL.
	out, err := im.cmd(ctx, "--no-playlist", "--skip-download", "--no-warnings", "--print", "%(id)s\t%(title)s", "--", job.URL).Output()
	if err != nil {
		return "", cmdError(err)
	}
	id, title, _ := strings.Cut(strings.TrimSpace(firstLine(string(out))), "\t")
	if !safeID.MatchString(id) {
		return "", fmt.Errorf("unexpected video id %q", id)
	}
	update(true, func(j *state.Job) { j.Title = title })

	base := filepath.Join(im.dir, Subdir, id)
	audioRel := filepath.ToSlash(filepath.Join(Subdir, id+".m4a"))
	if _, err := os.Stat(base + ".m4a"); err == nil {
		return im.done(ctx, job, audioRel) // already imported; just (re)link it
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return "", err
	}

	args := []string{
		"--no-playlist", "--newline", "--no-mtime", "--no-warnings",
		// Best video up to 1080p, preferring H.264 + AAC so every browser can
		// play it; merged into one mp4.
		"-f", "bv*+ba/b", "-S", "res:1080,vcodec:h264,acodec:m4a", "--merge-output-format", "mp4",
		// ...and an audio-only m4a next to it (-k keeps the video).
		"-x", "--audio-format", "m4a", "-k",
		"--write-info-json", "--write-thumbnail", "--convert-thumbnails", "jpg",
		"-o", base + ".%(ext)s",
	}
	args = append(args, im.extra...)
	args = append(args, "--", job.URL)
	cmd := im.cmd(ctx, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr tailBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	trackProgress(stdout, func(p float64) { update(false, func(j *state.Job) { j.Progress = p }) })
	if err := cmd.Wait(); err != nil {
		if msg := stderr.String(); msg != "" {
			return "", fmt.Errorf("%v: %s", err, msg)
		}
		return "", err
	}
	if _, err := os.Stat(base + ".m4a"); err != nil {
		// No separate audio (e.g. a site that only offers a single file):
		// the video then serves as the track itself.
		if _, verr := os.Stat(base + ".mp4"); verr != nil {
			return "", fmt.Errorf("yt-dlp finished but produced neither %s.m4a nor .mp4", id)
		}
		audioRel = filepath.ToSlash(filepath.Join(Subdir, id+".mp4"))
	}
	return im.done(ctx, job, audioRel)
}

func (im *Importer) cmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, im.bin, args...)
	// Interrupt lets yt-dlp stop ffmpeg and keep its .part file for resuming.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

var progressRe = regexp.MustCompile(`^\[download\]\s+([\d.]+)%`)

// trackProgress reports download progress. yt-dlp downloads video and audio
// separately, so the first stream maps to 0–50% and the second to 50–100%.
func trackProgress(r io.Reader, report func(float64)) {
	sc := bufio.NewScanner(r)
	stream, lastPct := 0, 0.0
	var lastReport time.Time
	for sc.Scan() {
		m := progressRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		pct, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		if pct+5 < lastPct && stream == 0 {
			stream = 1
		}
		lastPct = pct
		overall := pct / 2
		if stream == 1 {
			overall = 50 + pct/2
		}
		if time.Since(lastReport) > time.Second || pct >= 100 {
			lastReport = time.Now()
			report(min(overall, 99))
		}
	}
	io.Copy(io.Discard, r)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func cmdError(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		msg := strings.TrimSpace(string(ee.Stderr))
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return errors.New(msg)
	}
	return err
}

// tailBuffer keeps the last 2 KB written to it (yt-dlp's error output).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2048 {
		t.buf = t.buf[len(t.buf)-2048:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
