// Package tags reads the handful of metadata fields a music library needs
// (title, artist, album, genre, year, track/disc, duration, embedded cover)
// from MP3 (ID3v1/v2), FLAC, Ogg Vorbis/Opus and MP4/M4A files.
//
// It is deliberately small: unknown or damaged files return whatever could be
// read plus an error, and the caller falls back to the file name.
package tags

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Info is the metadata of one file. Zero values mean "not present".
type Info struct {
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Genre       string
	Year        int
	Track       int
	Disc        int
	Duration    float64 // seconds
	HasPicture  bool
}

// Picture is an embedded cover image.
type Picture struct {
	MIME string
	Data []byte
}

// ErrUnsupported is returned for formats this package does not parse.
var ErrUnsupported = errors.New("tags: unsupported format")

// maxPicture bounds how much of an embedded picture we are willing to load.
const maxPicture = 16 << 20

// Read returns the metadata of the file at path, chosen by extension.
func Read(path string) (Info, error) {
	info, _, err := read(path, false)
	return info, err
}

// ReadPicture returns the embedded front cover (or the first picture) of the
// file at path, or nil if it has none.
func ReadPicture(path string) (*Picture, error) {
	_, pic, err := read(path, true)
	return pic, err
}

func read(path string, wantPic bool) (Info, *Picture, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Info{}, nil, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return readMP3(f, st.Size(), wantPic)
	case ".flac":
		return readFLAC(f, st.Size(), wantPic)
	case ".ogg", ".oga", ".opus":
		return readOgg(f, st.Size(), wantPic)
	case ".m4a", ".m4b", ".mp4", ".m4v", ".mov", ".aac":
		return readMP4(f, st.Size(), wantPic)
	}
	return Info{}, nil, ErrUnsupported
}

// setField applies a "vorbis comment"-style key/value. ID3 and MP4 map their
// own frame names onto these keys so all formats share one code path.
func (in *Info) setField(key, val string) {
	val = strings.TrimSpace(strings.TrimRight(val, "\x00"))
	if val == "" {
		return
	}
	switch strings.ToUpper(key) {
	case "TITLE":
		in.Title = firstNonEmpty(in.Title, val)
	case "ARTIST":
		in.Artist = firstNonEmpty(in.Artist, val)
	case "ALBUM":
		in.Album = firstNonEmpty(in.Album, val)
	case "ALBUMARTIST", "ALBUM ARTIST", "ALBUM_ARTIST":
		in.AlbumArtist = firstNonEmpty(in.AlbumArtist, val)
	case "GENRE":
		in.Genre = firstNonEmpty(in.Genre, val)
	case "DATE", "YEAR", "ORIGINALDATE", "ORIGINALYEAR":
		if in.Year == 0 {
			in.Year = parseYear(val)
		}
	case "TRACKNUMBER", "TRACK":
		if in.Track == 0 {
			in.Track = leadingInt(val)
		}
	case "DISCNUMBER", "DISC":
		if in.Disc == 0 {
			in.Disc = leadingInt(val)
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// parseYear finds the first plausible 4-digit year in s ("2003-05-01",
// "2003", "May 2003").
func parseYear(s string) int {
	for i := 0; i+4 <= len(s); i++ {
		if n, err := strconv.Atoi(s[i : i+4]); err == nil && n >= 1000 && n <= 2999 {
			if (i == 0 || !isDigit(s[i-1])) && (i+4 == len(s) || !isDigit(s[i+4])) {
				return n
			}
		}
	}
	return 0
}

// leadingInt parses "3", "03/12" or "3 of 12" as 3.
func leadingInt(s string) int {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && isDigit(s[end]) {
		end++
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// Genre names for ID3v1 genre numbers and "(17)"-style ID3v2.3 references.
var id3Genres = [...]string{
	"Blues", "Classic Rock", "Country", "Dance", "Disco", "Funk", "Grunge", "Hip-Hop",
	"Jazz", "Metal", "New Age", "Oldies", "Other", "Pop", "R&B", "Rap", "Reggae", "Rock",
	"Techno", "Industrial", "Alternative", "Ska", "Death Metal", "Pranks", "Soundtrack",
	"Euro-Techno", "Ambient", "Trip-Hop", "Vocal", "Jazz+Funk", "Fusion", "Trance",
	"Classical", "Instrumental", "Acid", "House", "Game", "Sound Clip", "Gospel", "Noise",
	"Alternative Rock", "Bass", "Soul", "Punk", "Space", "Meditative", "Instrumental Pop",
	"Instrumental Rock", "Ethnic", "Gothic", "Darkwave", "Techno-Industrial", "Electronic",
	"Pop-Folk", "Eurodance", "Dream", "Southern Rock", "Comedy", "Cult", "Gangsta", "Top 40",
	"Christian Rap", "Pop/Funk", "Jungle", "Native American", "Cabaret", "New Wave",
	"Psychedelic", "Rave", "Showtunes", "Trailer", "Lo-Fi", "Tribal", "Acid Punk",
	"Acid Jazz", "Polka", "Retro", "Musical", "Rock & Roll", "Hard Rock", "Folk",
	"Folk-Rock", "National Folk", "Swing", "Fast Fusion", "Bebop", "Latin", "Revival",
	"Celtic", "Bluegrass", "Avantgarde", "Gothic Rock", "Progressive Rock",
	"Psychedelic Rock", "Symphonic Rock", "Slow Rock", "Big Band", "Chorus",
	"Easy Listening", "Acoustic", "Humour", "Speech", "Chanson", "Opera", "Chamber Music",
	"Sonata", "Symphony", "Booty Bass", "Primus", "Porn Groove", "Satire", "Slow Jam",
	"Club", "Tango", "Samba", "Folklore", "Ballad", "Power Ballad", "Rhythmic Soul",
	"Freestyle", "Duet", "Punk Rock", "Drum Solo", "A Cappella", "Euro-House",
	"Dance Hall", "Goa", "Drum & Bass", "Club-House", "Hardcore Techno", "Terror", "Indie",
	"BritPop", "Negerpunk", "Polsk Punk", "Beat", "Christian Gangsta Rap", "Heavy Metal",
	"Black Metal", "Crossover", "Contemporary Christian", "Christian Rock", "Merengue",
	"Salsa", "Thrash Metal", "Anime", "Jpop", "Synthpop",
}

func genreByIndex(n int) string {
	if n >= 0 && n < len(id3Genres) {
		return id3Genres[n]
	}
	return ""
}

// resolveID3Genre turns "(17)", "17", "(17)Rock" or "(RX)" into a name.
func resolveID3Genre(s string) string {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil {
		return genreByIndex(n)
	}
	if strings.HasPrefix(s, "(") {
		end := strings.IndexByte(s, ')')
		if end > 0 {
			rest := strings.TrimSpace(s[end+1:])
			if rest != "" {
				return rest
			}
			switch ref := s[1:end]; ref {
			case "RX":
				return "Remix"
			case "CR":
				return "Cover"
			default:
				if n, err := strconv.Atoi(ref); err == nil {
					return genreByIndex(n)
				}
			}
		}
	}
	return s
}
