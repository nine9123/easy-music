package tags

import (
	"math"
	"path/filepath"
	"testing"
)

// The files in testdata were produced by ffmpeg (2 s, 8 kHz sine; MP3 adds
// ~0.16 s of encoder padding, as ffprobe also reports) so the
// parsers are checked against a real muxer rather than against themselves.
func TestRead(t *testing.T) {
	tests := []struct {
		file string
		want Info
	}{
		{"v23.mp3", Info{Title: "Tést Song", Artist: "The Artist feat. Other", Album: "Alb", Genre: "Rock", Year: 2004, Track: 3, Disc: 1, HasPicture: true}},
		{"cbr24.mp3", Info{Title: "V4", Artist: "A"}},
		{"t.flac", Info{Title: "Flac T", Artist: "FA", AlbumArtist: "AA", Genre: "Jazz", Year: 1999, Track: 5, HasPicture: true}},
		{"t.opus", Info{Title: "Opus T", Artist: "OA"}},
		{"t.ogg", Info{Title: "Vorbis T", Artist: "VA", Genre: "Ambient"}},
		{"t.m4a", Info{Title: "M4A T", Artist: "MA", Album: "MAlb", Genre: "Pop", Year: 2011, Track: 2, HasPicture: true}},
		{"t.mp4", Info{Title: "Vid", Artist: "VidA"}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got, err := Read(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(got.Duration-2) > 0.2 {
				t.Errorf("duration = %.3f, want ~2", got.Duration)
			}
			got.Duration = 0
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestReadPicture(t *testing.T) {
	for file, mime := range map[string]string{"v23.mp3": "image/jpeg", "t.flac": "image/png", "t.m4a": "image/jpeg"} {
		p, err := ReadPicture(filepath.Join("testdata", file))
		if err != nil || p == nil {
			t.Fatalf("%s: picture %v, err %v", file, p, err)
		}
		if p.MIME != mime || len(p.Data) < 100 {
			t.Errorf("%s: mime %q, %d bytes", file, p.MIME, len(p.Data))
		}
	}
	if p, _ := ReadPicture(filepath.Join("testdata", "t.opus")); p != nil {
		t.Errorf("opus: unexpected picture")
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{"(17)": "Rock", "17": "Rock", "(17)Hard Rock": "Hard Rock", "Trance": "Trance", "(RX)": "Remix"} {
		if got := resolveID3Genre(in); got != want {
			t.Errorf("resolveID3Genre(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]int{"2003-05-01": 2003, "May 1987": 1987, "12345": 0, "": 0} {
		if got := parseYear(in); got != want {
			t.Errorf("parseYear(%q) = %d, want %d", in, got, want)
		}
	}
	if got := leadingInt("03/12"); got != 3 {
		t.Errorf("leadingInt = %d", got)
	}
	if got := unsync([]byte{0xFF, 0x00, 0xE0, 0x01}); string(got) != "\xFF\xE0\x01" {
		t.Errorf("unsync = %x", got)
	}
}
