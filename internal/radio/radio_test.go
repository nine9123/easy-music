package radio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestParsePLS(t *testing.T) {
	got, err := Parse([]byte("\xEF\xBB\xBF[playlist]\nNumberOfEntries=2\nFile2=http://b.example/stream\nTitle2=B\nFile1=https://a.example/live.mp3\nTitle1=Radio A\nLength1=-1\nVersion=2\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{"Radio A", "https://a.example/live.mp3"}, {"B", "http://b.example/stream"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestParseM3U(t *testing.T) {
	got, err := Parse([]byte("#EXTM3U\n#EXTINF:-1,Jazz FM\nhttp://jazz.example:8000/stream\n/local/file.mp3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []Entry{{"Jazz FM", "http://jazz.example:8000/stream"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if _, err := Parse([]byte("/music/a.mp3\n/music/b.mp3\n")); err != ErrNoStreams {
		t.Errorf("local m3u: err = %v", err)
	}
}

func TestResolve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/station.pls":
			w.Write([]byte("[playlist]\nFile1=http://stream.example/live\nTitle1=Live\n"))
		case "/hls.m3u8":
			w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:10,\nseg1.ts\n"))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if s, n, err := Resolve(ctx, srv.Client(), srv.URL+"/station.pls"); err != nil || s != "http://stream.example/live" || n != "Live" {
		t.Errorf("pls: %q %q %v", s, n, err)
	}
	if s, _, err := Resolve(ctx, srv.Client(), srv.URL+"/hls.m3u8"); err != nil || s != srv.URL+"/hls.m3u8" {
		t.Errorf("hls: %q %v", s, err)
	}
	if s, _, err := Resolve(ctx, srv.Client(), "http://direct.example/stream"); err != nil || s != "http://direct.example/stream" {
		t.Errorf("direct: %q %v", s, err)
	}
	if _, _, err := Resolve(ctx, srv.Client(), "file:///etc/passwd"); err == nil {
		t.Errorf("non-http URL accepted")
	}
}
