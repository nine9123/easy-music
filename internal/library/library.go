// Package library scans music directories into an index of tracks.
//
// The music itself is only ever read. The index lives in the cache directory
// (meant for the SSD) and is fully rebuildable: delete it and the next scan
// recreates it. Track IDs are derived from file paths, so a rebuild yields the
// same IDs and playlists, play counts and favorites (kept elsewhere) survive.
package library

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Track is one playable item: an audio file, optionally with a companion music
// video, or a standalone video file.
type Track struct {
	ID          string  `json:"id"`
	Root        string  `json:"root"`
	Path        string  `json:"path"`            // relative to the root, slash-separated
	Video       string  `json:"video,omitempty"` // relative path of the music video, may equal Path
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album,omitempty"`
	AlbumArtist string  `json:"albumArtist,omitempty"`
	Genre       string  `json:"genre,omitempty"`
	Year        int     `json:"year,omitempty"`
	TrackNo     int     `json:"trackNo,omitempty"`
	DiscNo      int     `json:"discNo,omitempty"`
	Duration    float64 `json:"duration,omitempty"`
	Size        int64   `json:"size"`
	ModTime     int64   `json:"mtime"`           // unix nanoseconds of the newest of audio and sidecar
	Cover       string  `json:"cover,omitempty"` // relative path of a cover image file
	Embedded    bool    `json:"embedded,omitempty"`
	Source      string  `json:"source,omitempty"` // original URL for imports
	Added       int64   `json:"added"`            // unix seconds
}

// Root is a scanned directory. Name is its stable identifier in the index.
type Root struct {
	Name string
	Dir  string
}

// Library is the in-memory index. Tracks are immutable once published: a scan
// builds new ones and swaps the whole set, so readers never need to copy.
type Library struct {
	roots     []Root
	indexPath string

	mu      sync.RWMutex
	byID    map[string]*Track
	list    []*Track // sorted by artist, album, disc, track
	version int64

	scanMu   sync.Mutex // one scan at a time
	scanning atomic.Bool
	scanned  atomic.Int64 // files seen by the running scan
	lastScan atomic.Int64 // unix seconds
}

const indexVersion = 1

type indexFile struct {
	Version int      `json:"version"`
	Scanned int64    `json:"scanned"`
	Tracks  []*Track `json:"tracks"`
}

// DownloadsRoot is the root name of the download directory.
const DownloadsRoot = "downloads"

// NewRoots names directories by their base name, made unique. The download
// directory is always DownloadsRoot, even if a music dir is also called that.
func NewRoots(dirs []string, downloadDir string) []Root {
	var roots []Root
	used := map[string]bool{}
	if downloadDir != "" {
		used[DownloadsRoot] = true
	}
	add := func(name, dir string) {
		base := name
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		used[name] = true
		roots = append(roots, Root{Name: name, Dir: dir})
	}
	for _, d := range dirs {
		name := filepath.Base(d)
		if name == "/" || name == "." {
			name = "music"
		}
		add(name, d)
	}
	if downloadDir != "" {
		roots = append(roots, Root{Name: DownloadsRoot, Dir: downloadDir})
	}
	return roots
}

// Open loads the index from cacheDir if present. A missing or outdated index
// is not an error; the library just starts empty until the first scan.
func Open(roots []Root, cacheDir string) (*Library, error) {
	l := &Library{roots: roots, indexPath: filepath.Join(cacheDir, "library.json"), byID: map[string]*Track{}}
	b, err := os.ReadFile(l.indexPath)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	var idx indexFile
	if err := json.Unmarshal(b, &idx); err != nil || idx.Version != indexVersion {
		log.Printf("library: ignoring unreadable/outdated index %s (will rescan)", l.indexPath)
		return l, nil
	}
	known := map[string]bool{}
	for _, r := range roots {
		known[r.Name] = true
	}
	var tracks []*Track
	for _, t := range idx.Tracks {
		if known[t.Root] {
			tracks = append(tracks, t)
		}
	}
	l.publish(tracks)
	l.lastScan.Store(idx.Scanned)
	return l, nil
}

func (l *Library) publish(tracks []*Track) {
	byID := make(map[string]*Track, len(tracks))
	for _, t := range tracks {
		byID[t.ID] = t
	}
	sortTracks(tracks)
	l.mu.Lock()
	l.byID, l.list = byID, tracks
	l.version++
	l.mu.Unlock()
}

func sortTracks(ts []*Track) {
	sort.Slice(ts, func(i, j int) bool {
		a, b := ts[i], ts[j]
		if x, y := strings.ToLower(a.SortArtist()), strings.ToLower(b.SortArtist()); x != y {
			return x < y
		}
		if x, y := strings.ToLower(a.Album), strings.ToLower(b.Album); x != y {
			return x < y
		}
		if a.DiscNo != b.DiscNo {
			return a.DiscNo < b.DiscNo
		}
		if a.TrackNo != b.TrackNo {
			return a.TrackNo < b.TrackNo
		}
		return a.Path < b.Path
	})
}

// All returns every track. The slice and the tracks must not be modified.
func (l *Library) All() []*Track {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.list
}

// Get returns the track with the given ID.
func (l *Library) Get(id string) (*Track, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	t, ok := l.byID[id]
	return t, ok
}

// Version changes whenever the set of tracks changes.
func (l *Library) Version() int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.version
}

// Status reports scan progress.
func (l *Library) Status() (scanning bool, seen int64, lastScan time.Time) {
	var last time.Time
	if s := l.lastScan.Load(); s > 0 {
		last = time.Unix(s, 0)
	}
	return l.scanning.Load(), l.scanned.Load(), last
}

// Abs returns the absolute path of a file relative to the named root.
func (l *Library) Abs(root, rel string) (string, bool) {
	for _, r := range l.roots {
		if r.Name == root {
			return filepath.Join(r.Dir, filepath.FromSlash(rel)), true
		}
	}
	return "", false
}

// RootDir returns the directory of the named root.
func (l *Library) RootDir(name string) (string, bool) { return l.Abs(name, "") }

// IDFor returns the ID a file gets when scanned. Only the relative path goes
// in, so remounting a directory somewhere else keeps every ID.
func IDFor(rel string) string {
	h := sha1.Sum([]byte(rel))
	return hex.EncodeToString(h[:8])
}

// TrackID resolves the ID a file under a root currently has (or would have).
func (l *Library) TrackID(root, rel string) string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, t := range l.list {
		if t.Root == root && t.Path == rel {
			return t.ID
		}
	}
	return IDFor(rel)
}

func (l *Library) save(tracks []*Track) error {
	b, err := json.Marshal(indexFile{Version: indexVersion, Scanned: l.lastScan.Load(), Tracks: tracks})
	if err != nil {
		return err
	}
	return WriteFileAtomic(l.indexPath, b)
}

// WriteFileAtomic writes via a temp file and rename, so a stop in the middle
// (idlegate's docker stop, a crash) never leaves a half-written file.
func WriteFileAtomic(name string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), name)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// SortArtist is the album artist if set, else the artist.
func (t *Track) SortArtist() string {
	if t.AlbumArtist != "" {
		return t.AlbumArtist
	}
	return t.Artist
}

// HasVideo reports whether the track has a music video.
func (t *Track) HasVideo() bool { return t.Video != "" }

// AlbumKey groups tracks of one album. Tracks without an album tag are
// grouped by directory, which is usually how loose files are organised.
func (t *Track) AlbumKey() string {
	if t.Album == "" {
		return "dir:" + t.Root + "/" + path.Dir(t.Path)
	}
	return strings.ToLower(t.SortArtist() + "\x00" + t.Album)
}

// ArtistKeys returns normalised keys for every credited artist:
// "A feat. B", "A & B", "A, B" and "A x B" all count as both A and B.
func (t *Track) ArtistKeys() []string { return SplitArtists(t.Artist) }

// SplitArtists normalises and splits an artist credit.
func SplitArtists(s string) []string {
	var out []string
	for _, c := range Credits(s) {
		k := strings.TrimPrefix(strings.ToLower(c), "the ")
		if k != "" && !contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

var creditSeps = []string{" featuring ", " feat. ", " feat ", " ft. ", " ft ", " vs. ", " vs ", " x ", " & ", ", ", "; ", " / "}

// Credits splits an artist credit into the individual artists, keeping
// their original spelling: "Daft Punk feat. Pharrell" -> [Daft Punk, Pharrell].
func Credits(s string) []string {
	var out []string
	for s != "" {
		lower := strings.ToLower(s)
		cut, sepLen := len(s), 0
		for _, sep := range creditSeps {
			if i := strings.Index(lower, sep); i >= 0 && i < cut {
				cut, sepLen = i, len(sep)
			}
		}
		if p := strings.TrimSpace(strings.Trim(strings.TrimSpace(s[:cut]), "()[]")); p != "" {
			out = append(out, p)
		}
		if cut == len(s) {
			break
		}
		s = s[cut+sepLen:]
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
