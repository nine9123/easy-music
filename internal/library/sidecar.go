package library

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// infoJSON is the subset of yt-dlp's --write-info-json output we use. Keeping
// the sidecar next to the media (rather than only in our cache) is what makes
// imports rebuildable: a fresh scan recovers title, artist and source URL.
type infoJSON struct {
	Title       string   `json:"title"`
	Track       string   `json:"track"`
	Artist      string   `json:"artist"`
	Artists     []string `json:"artists"`
	Creator     string   `json:"creator"`
	Album       string   `json:"album"`
	ReleaseYear int      `json:"release_year"`
	UploadDate  string   `json:"upload_date"`
	Uploader    string   `json:"uploader"`
	Channel     string   `json:"channel"`
	Duration    float64  `json:"duration"`
	WebpageURL  string   `json:"webpage_url"`
	Genre       string   `json:"genre"`
}

func applyInfoJSON(t *Track, name string) {
	b, err := os.ReadFile(name)
	if err != nil {
		return
	}
	var in infoJSON
	if json.Unmarshal(b, &in) != nil {
		return
	}
	artist, title := in.Artist, in.Track
	if artist == "" && len(in.Artists) > 0 {
		artist = strings.Join(in.Artists, ", ")
	}
	if artist == "" || title == "" {
		a, ti := ParseVideoTitle(in.Title, firstNonEmpty(in.Uploader, in.Channel, in.Creator))
		artist, title = firstNonEmpty(artist, a), firstNonEmpty(title, ti)
	}
	t.Title = firstNonEmpty(t.Title, title)
	t.Artist = firstNonEmpty(t.Artist, artist)
	t.Album = firstNonEmpty(t.Album, in.Album)
	t.Genre = firstNonEmpty(t.Genre, in.Genre)
	if t.Year == 0 {
		t.Year = in.ReleaseYear
	}
	if t.Year == 0 && len(in.UploadDate) >= 4 {
		t.Year, _ = strconv.Atoi(in.UploadDate[:4])
	}
	if t.Duration == 0 {
		t.Duration = in.Duration
	}
	t.Source = in.WebpageURL
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

var (
	// "(Official Music Video)", "[Lyrics]", "(HD Remastered)", "| Official Audio" ...
	videoNoise   = regexp.MustCompile(`(?i)\s*(?:[(\[][^)\]]*\b(?:official|lyrics?|audio|video|visuali[sz]er|hd|hq|4k|remaster(?:ed)?|m/?v|clip)\b[^)\]]*[)\]]|\|\s*official.*$)`)
	channelNoise = regexp.MustCompile(`(?i)(?:\s*-\s*topic|vevo|\s*official)$`)
	quoted       = regexp.MustCompile(`^["“'](.+)["”']$`)
)

// ParseVideoTitle extracts artist and title from a video title such as
// "Artist - Song (Official Video)". Without a " - " the channel name (minus
// " - Topic"/"VEVO") stands in for the artist.
func ParseVideoTitle(videoTitle, channel string) (artist, title string) {
	s := strings.TrimSpace(videoTitle)
	for {
		c := videoNoise.ReplaceAllString(s, "")
		if c == s {
			break
		}
		s = strings.TrimSpace(c)
	}
	for _, sep := range []string{" - ", " – ", " — ", " | "} {
		if a, ti, ok := strings.Cut(s, sep); ok && strings.TrimSpace(a) != "" && strings.TrimSpace(ti) != "" {
			artist, title = strings.TrimSpace(a), strings.TrimSpace(ti)
			break
		}
	}
	if title == "" {
		title = s
		artist = strings.TrimSpace(channelNoise.ReplaceAllString(strings.TrimSpace(channel), ""))
	}
	if m := quoted.FindStringSubmatch(title); m != nil {
		title = m[1]
	}
	if title == "" {
		title = strings.TrimSpace(videoTitle)
	}
	return artist, title
}
