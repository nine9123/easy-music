package library

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func copyFixture(t *testing.T, fixture, dst string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "tags", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	write(t, dst, string(b))
}

func write(t *testing.T, dst, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	music, dl, cache := t.TempDir(), t.TempDir(), t.TempDir()
	copyFixture(t, "t.flac", filepath.Join(music, "Jazz Guy", "Blue Album", "01 - Untagged.ogg")) // not really ogg: tags fail, path fills in
	copyFixture(t, "v23.mp3", filepath.Join(music, "Rock", "song.mp3"))
	copyFixture(t, "t.mp4", filepath.Join(music, "Rock", "song.mp4")) // companion video
	write(t, filepath.Join(music, "Rock", "cover.jpg"), "jpeg")
	copyFixture(t, "t.mp4", filepath.Join(music, "Videos", "Some Band - Live.mp4")) // standalone video
	copyFixture(t, "t.m4a", filepath.Join(dl, "youtube", "abc123.m4a"))
	copyFixture(t, "t.mp4", filepath.Join(dl, "youtube", "abc123.mp4"))
	write(t, filepath.Join(dl, "youtube", "abc123.jpg"), "thumb")
	write(t, filepath.Join(dl, "youtube", "abc123.info.json"), `{"title":"Ignored - Because Tags Win (Official Video)","webpage_url":"https://youtu.be/abc123","upload_date":"20200101"}`)
	write(t, filepath.Join(music, ".hidden", "x.mp3"), "nope")

	roots := NewRoots([]string{music}, dl)
	lib, err := Open(roots, cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := lib.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	byPath := map[string]*Track{}
	for _, tr := range lib.All() {
		byPath[tr.Root+"/"+tr.Path] = tr
	}
	if len(byPath) != 4 {
		t.Fatalf("got %d tracks: %v", len(byPath), keys(byPath))
	}
	mroot := filepath.Base(music)

	untagged := byPath[mroot+"/Jazz Guy/Blue Album/01 - Untagged.ogg"]
	if untagged == nil || untagged.Title != "Untagged" || untagged.Artist != "Jazz Guy" || untagged.Album != "Blue Album" || untagged.TrackNo != 1 {
		t.Errorf("path fallback: %+v", untagged)
	}
	song := byPath[mroot+"/Rock/song.mp3"]
	if song == nil || song.Video != "Rock/song.mp4" || song.Cover != "Rock/cover.jpg" || song.Title != "Tést Song" || !song.Embedded {
		t.Errorf("companion video: %+v", song)
	}
	live := byPath[mroot+"/Videos/Some Band - Live.mp4"]
	if live == nil || live.Video != live.Path || live.Title != "Vid" {
		t.Errorf("standalone video: %+v", live)
	}
	yt := byPath["downloads/youtube/abc123.m4a"]
	if yt == nil || yt.Video != "youtube/abc123.mp4" || yt.Cover != "youtube/abc123.jpg" || yt.Source != "https://youtu.be/abc123" || yt.Title != "M4A T" {
		t.Errorf("import: %+v", yt)
	}
	if yt.ID != lib.TrackID("downloads", "youtube/abc123.m4a") || yt.ID != IDFor("youtube/abc123.m4a") {
		t.Errorf("ID not derivable from path")
	}

	// Reopening from the cached index gives the same tracks without a scan.
	lib2, err := Open(roots, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib2.All()) != 4 {
		t.Fatalf("reopened index has %d tracks", len(lib2.All()))
	}
	got, _ := lib2.Get(song.ID)
	if !reflect.DeepEqual(got, song) {
		t.Errorf("index round trip:\n%+v\n%+v", got, song)
	}

	// Removing a file drops it on rescan; Added survives for the rest.
	os.Remove(filepath.Join(music, "Rock", "song.mp3"))
	if err := lib2.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := lib2.Get(song.ID); ok {
		t.Errorf("deleted track still indexed")
	}
	if tr, _ := lib2.Get(yt.ID); tr == nil || tr.Added != yt.Added {
		t.Errorf("added time not preserved")
	}
	// song.mp4 is now a standalone video track.
	if _, ok := lib2.Get(IDFor("Rock/song.mp4")); !ok {
		t.Errorf("orphaned video should become a track")
	}
}

func TestUnreadableRootKeepsTracks(t *testing.T) {
	music, cache := t.TempDir(), t.TempDir()
	copyFixture(t, "v23.mp3", filepath.Join(music, "a.mp3"))
	roots := []Root{{Name: "m", Dir: music}}
	lib, _ := Open(roots, cache)
	lib.Scan(context.Background())
	os.RemoveAll(music) // disk unmounted
	lib.Scan(context.Background())
	if len(lib.All()) != 1 {
		t.Errorf("tracks of an unreadable root were dropped")
	}
}

func TestNewRoots(t *testing.T) {
	got := NewRoots([]string{"/hdd/music", "/other/music", "/x/downloads"}, "/dl")
	want := []Root{{"music", "/hdd/music"}, {"music-2", "/other/music"}, {"downloads-2", "/x/downloads"}, {DownloadsRoot, "/dl"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewRoots = %+v", got)
	}
}

func TestParseVideoTitle(t *testing.T) {
	tests := []struct{ title, channel, artist, song string }{
		{"Daft Punk - Around the World (Official Music Video)", "Daft Punk", "Daft Punk", "Around the World"},
		{"Massive Attack - Teardrop [HD Remastered]", "x", "Massive Attack", "Teardrop"},
		{"Some Song (Lyrics)", "Cool Singer - Topic", "Cool Singer", "Some Song"},
		{"Another One", "BandVEVO", "Band", "Another One"},
		{`Artist – "Quoted Title" | Official Video`, "", "Artist", "Quoted Title"},
	}
	for _, tt := range tests {
		a, s := ParseVideoTitle(tt.title, tt.channel)
		if a != tt.artist || s != tt.song {
			t.Errorf("ParseVideoTitle(%q, %q) = %q, %q; want %q, %q", tt.title, tt.channel, a, s, tt.artist, tt.song)
		}
	}
}

func TestSplitArtists(t *testing.T) {
	got := SplitArtists("The Weeknd feat. Daft Punk & Someone")
	want := []string{"weeknd", "daft punk", "someone"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitArtists = %q, want %q", got, want)
	}
}

func keys(m map[string]*Track) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
