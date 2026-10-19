package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nine9123/easy-music/internal/covers"
	"github.com/nine9123/easy-music/internal/hotcache"
	"github.com/nine9123/easy-music/internal/library"
	"github.com/nine9123/easy-music/internal/state"
	"github.com/nine9123/easy-music/internal/youtube"
)

type env struct {
	t    *testing.T
	url  string
	srv  *Server
	lib  *library.Library
	st   *state.Store
	dl   string
	stop func()
}

func copyFixture(t *testing.T, fixture, dst string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "tags", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	music, dl, cache, stDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	copyFixture(t, "v23.mp3", filepath.Join(music, "Rock", "song.mp3"))
	copyFixture(t, "t.mp4", filepath.Join(music, "Rock", "song.mp4"))
	copyFixture(t, "t.flac", filepath.Join(music, "Jazz", "tune.flac"))
	copyFixture(t, "t.m4a", filepath.Join(music, "Pop", "pop.m4a"))

	lib, err := library.Open(library.NewRoots([]string{music}, dl), cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := lib.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Open(stDir)
	cv, _ := covers.New(filepath.Join(cache, "covers"), lib, false)
	e := &env{t: t, lib: lib, st: st, dl: dl}
	hot, _ := hotcache.Open(filepath.Join(cache, "hot"), 1<<30, 2, func(id string) float64 { return e.srv.HotScore(id) })
	bin, _ := filepath.Abs("../youtube/testdata/fake-yt-dlp")
	yt := youtube.New(bin, nil, dl, st, func(ctx context.Context, j state.Job, rel string) (string, error) {
		return e.srv.ImportDone(ctx, j, rel)
	})
	e.srv = New(lib, st, cv, hot, yt)
	ctx, cancel := context.WithCancel(context.Background())
	go hot.Run(ctx)
	go yt.Run(ctx)
	ts := httptest.NewServer(e.srv.Handler())
	e.url = ts.URL
	e.stop = func() { cancel(); ts.Close() }
	t.Cleanup(e.stop)
	return e
}

func (e *env) do(method, path string, body any, out any) *http.Response {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.url+path, rd)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			e.t.Fatalf("%s %s: decoding: %v", method, path, err)
		}
	} else {
		io.Copy(io.Discard, resp.Body)
	}
	return resp
}

type trackList struct {
	Total  int         `json:"total"`
	Tracks []trackView `json:"tracks"`
}

func (e *env) find(title string) trackView {
	e.t.Helper()
	var l trackList
	e.do("GET", "/api/tracks?limit=1000", nil, &l)
	for _, t := range l.Tracks {
		if t.Title == title {
			return t
		}
	}
	e.t.Fatalf("no track %q in %+v", title, l.Tracks)
	return trackView{}
}

func TestLibraryAPI(t *testing.T) {
	e := newEnv(t)
	if r := e.do("GET", "/healthz", nil, nil); r.StatusCode != 200 {
		t.Fatalf("healthz: %d", r.StatusCode)
	}
	if r := e.do("GET", "/", nil, nil); r.StatusCode != 200 {
		t.Fatalf("index: %d", r.StatusCode)
	}

	var l trackList
	e.do("GET", "/api/tracks?q=flac", nil, &l)
	if l.Total != 1 || l.Tracks[0].Artist != "FA" {
		t.Errorf("search: %+v", l)
	}
	song := e.find("Tést Song")
	if !song.HasVideo || song.Genre != "Rock" {
		t.Errorf("song: %+v", song)
	}

	// Range request for seeking.
	req, _ := http.NewRequest("GET", e.url+"/api/tracks/"+song.ID+"/stream", nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || len(b) != 10 || string(b[:3]) != "ID3" {
		t.Errorf("range: %d, %q", resp.StatusCode, b)
	}
	if r := e.do("GET", "/api/tracks/"+song.ID+"/video", nil, nil); r.StatusCode != 200 || r.Header.Get("Content-Type") != "video/mp4" {
		t.Errorf("video: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	if r := e.do("GET", "/api/tracks/"+song.ID+"/cover", nil, nil); r.StatusCode != 200 {
		t.Errorf("embedded cover: %d", r.StatusCode)
	}
	if r := e.do("GET", "/api/tracks/nope/stream", nil, nil); r.StatusCode != 404 {
		t.Errorf("unknown track: %d", r.StatusCode)
	}

	// Plays, favorites and the sorts/filters built on them.
	for i := 0; i < 3; i++ {
		e.do("POST", "/api/tracks/"+song.ID+"/played", map[string]float64{"seconds": 2}, nil)
	}
	e.do("PUT", "/api/tracks/"+song.ID+"/favorite", nil, nil)
	e.do("GET", "/api/tracks?sort=plays&order=desc", nil, &l)
	if l.Tracks[0].ID != song.ID || l.Tracks[0].Plays != 3 || !l.Tracks[0].Favorite {
		t.Errorf("sort by plays: %+v", l.Tracks[0])
	}
	e.do("GET", "/api/tracks?filter=favorites", nil, &l)
	if l.Total != 1 {
		t.Errorf("favorites filter: %d", l.Total)
	}
	var stats struct {
		Plays      int `json:"plays"`
		TopArtists []struct {
			Name  string `json:"name"`
			Plays int    `json:"plays"`
		} `json:"topArtists"`
	}
	e.do("GET", "/api/stats", nil, &stats)
	if stats.Plays != 3 || len(stats.TopArtists) != 2 || stats.TopArtists[0].Plays != 3 {
		t.Errorf("stats: %+v", stats)
	}

	// Third play crossed HOTCACHE_MIN_PLAYS: the next stream comes from the SSD.
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := e.do("GET", "/api/tracks/"+song.ID+"/stream", nil, nil)
		if r.Header.Get("X-Hot-Cache") == "true" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("track never served from the hot cache")
		}
		time.Sleep(20 * time.Millisecond)
	}

	var artists []artistView
	e.do("GET", "/api/artists", nil, &artists)
	var ta artistView
	for _, a := range artists {
		if a.Name == "The Artist" {
			ta = a
		}
	}
	e.do("GET", "/api/tracks?artist="+ta.ID, nil, &l)
	if ta.ID == "" || l.Total != 1 {
		t.Errorf("artist filter: %+v %+v", artists, l)
	}

	var mixed []trackView
	e.do("POST", "/api/mix", map[string]any{"seed": song.ID, "count": 5}, &mixed)
	if len(mixed) != 2 {
		t.Errorf("mix: %+v", mixed)
	}
	for _, m := range mixed {
		if m.ID == song.ID {
			t.Errorf("mix returned the seed")
		}
	}

	// Listening time comes from the client and is clamped to the track.
	var ps struct {
		Seconds float64 `json:"seconds"`
	}
	e.do("POST", "/api/tracks/"+song.ID+"/played", map[string]float64{"seconds": -1e308}, &ps)
	if ps.Seconds != 6 {
		t.Errorf("negative seconds: total %v, want 6", ps.Seconds)
	}
	e.do("POST", "/api/tracks/"+song.ID+"/played", map[string]float64{"seconds": 1e9}, &ps)
	if ps.Seconds > 6+song.Duration+60 {
		t.Errorf("huge seconds: total %v", ps.Seconds)
	}
}

func TestPlaylistsAndImport(t *testing.T) {
	e := newEnv(t)
	song, tune := e.find("Tést Song"), e.find("Flac T")

	var pl playlistSummary
	if r := e.do("POST", "/api/playlists", map[string]any{"name": "Faves", "tracks": []string{song.ID}}, &pl); r.StatusCode != 201 {
		t.Fatalf("create: %d", r.StatusCode)
	}
	e.do("POST", "/api/playlists/"+pl.ID+"/tracks", map[string]any{"tracks": []string{tune.ID, song.ID, "bogus"}}, &pl)
	if pl.Count != 2 {
		t.Errorf("append should skip duplicates and unknown IDs: %+v", pl)
	}
	e.do("PATCH", "/api/playlists/"+pl.ID, map[string]any{"name": "Renamed", "tracks": []string{tune.ID, song.ID}}, &pl)

	// Dropping a track between two rows inserts it there.
	var other playlistSummary
	e.do("POST", "/api/playlists", map[string]any{"name": "Other", "tracks": []string{song.ID}}, &other)
	e.do("POST", "/api/playlists/"+other.ID+"/tracks", map[string]any{"tracks": []string{tune.ID, tune.ID}, "at": 0}, &other)
	var otherFull struct {
		Tracks []trackView `json:"tracks"`
	}
	e.do("GET", "/api/playlists/"+other.ID, nil, &otherFull)
	if len(otherFull.Tracks) != 2 || otherFull.Tracks[0].ID != tune.ID || otherFull.Tracks[1].ID != song.ID {
		t.Errorf("insert at 0: %+v", otherFull.Tracks)
	}
	e.do("POST", "/api/playlists/"+other.ID+"/tracks", map[string]any{"tracks": []string{song.ID}, "at": 99}, &other)
	if other.Count != 2 {
		t.Errorf("insert should still skip duplicates: %+v", other)
	}

	// Import a video straight into the playlist.
	var job state.Job
	if r := e.do("POST", "/api/downloads", map[string]any{"url": "https://www.youtube.com/watch?v=vid123", "playlistId": pl.ID}, &job); r.StatusCode != 202 {
		t.Fatalf("download: %d", r.StatusCode)
	}
	deadline := time.Now().Add(10 * time.Second)
	for job.Status != state.JobDone {
		if time.Now().After(deadline) || job.Status == state.JobFailed {
			t.Fatalf("job: %+v", job)
		}
		time.Sleep(20 * time.Millisecond)
		var res struct{ Jobs []state.Job }
		e.do("GET", "/api/downloads", nil, &res)
		job = res.Jobs[0]
	}
	var full struct {
		Name   string      `json:"name"`
		Tracks []trackView `json:"tracks"`
	}
	e.do("GET", "/api/playlists/"+pl.ID, nil, &full)
	if full.Name != "Renamed" || len(full.Tracks) != 3 || full.Tracks[0].ID != tune.ID {
		t.Fatalf("playlist: %+v", full)
	}
	imp := full.Tracks[2]
	if imp.ID != job.TrackID || imp.Artist != "Artist" || imp.Title != "Song" || !imp.HasVideo || imp.Source == "" {
		t.Errorf("imported track: %+v", imp)
	}

	// A file that disappears stays in the playlist, marked missing.
	os.Remove(filepath.Join(e.dl, "youtube", "vid123.m4a"))
	os.Remove(filepath.Join(e.dl, "youtube", "vid123.mp4"))
	e.lib.Scan(context.Background())
	e.do("GET", "/api/playlists/"+pl.ID, nil, &full)
	if len(full.Tracks) != 3 || !full.Tracks[2].Missing {
		t.Errorf("missing entry: %+v", full.Tracks)
	}

	if r := e.do("DELETE", "/api/playlists/"+pl.ID, nil, nil); r.StatusCode != 204 {
		t.Errorf("delete: %d", r.StatusCode)
	}
	if r := e.do("GET", "/api/playlists/"+pl.ID, nil, nil); r.StatusCode != 404 {
		t.Errorf("deleted playlist: %d", r.StatusCode)
	}
}

func TestRadio(t *testing.T) {
	e := newEnv(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/listen.pls":
			fmt.Fprintf(w, "[playlist]\nFile1=http://%s/live\nTitle1=Test FM\n", r.Host)
		case "/live":
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Header().Set("Icy-Name", "Test FM")
			w.Write([]byte("STREAMDATA"))
		}
	}))
	defer upstream.Close()

	var added []state.Station
	if r := e.do("POST", "/api/stations", map[string]string{"url": upstream.URL + "/listen.pls"}, &added); r.StatusCode != 201 {
		t.Fatalf("add: %d", r.StatusCode)
	}
	if len(added) != 1 || added[0].Name != "Test FM" || added[0].URL != upstream.URL+"/live" {
		t.Fatalf("station: %+v", added)
	}

	// Upload a .m3u with two different stations.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "list.m3u")
	fw.Write([]byte("#EXTM3U\n#EXTINF:-1,One\nhttp://one.example/s\n#EXTINF:-1,Two\nhttp://two.example/s\n"))
	mw.Close()
	resp, err := http.Post(e.url+"/api/stations", mw.FormDataContentType(), &buf)
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("upload: %v %v", resp.StatusCode, err)
	}
	resp.Body.Close()
	var all []state.Station
	e.do("GET", "/api/stations", nil, &all)
	if len(all) != 3 {
		t.Errorf("stations: %+v", all)
	}

	resp, err = http.Get(e.url + "/api/stations/" + added[0].ID + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "STREAMDATA" || resp.Header.Get("Content-Type") != "audio/mpeg" {
		t.Errorf("proxied stream: %q %s", body, resp.Header.Get("Content-Type"))
	}
	e.do("POST", "/api/stations/"+added[0].ID+"/played", nil, nil)
	e.do("PATCH", "/api/stations/"+added[0].ID, map[string]any{"favorite": true}, nil)
	e.do("GET", "/api/stations", nil, &all)
	if all[0].ID != added[0].ID || !all[0].Favorite || all[0].Plays != 1 {
		t.Errorf("favorite station first: %+v", all)
	}
	if r := e.do("POST", "/api/stations", map[string]string{"url": "ftp://x"}, nil); r.StatusCode != 400 {
		t.Errorf("bad URL accepted: %d", r.StatusCode)
	}
}

func TestGzipJSON(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest("GET", e.url+"/api/tracks", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Encoding"), "gzip") {
		t.Errorf("large JSON not compressed")
	}
}
