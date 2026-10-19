package library

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nine9123/easy-music/internal/tags"
)

var (
	audioExt = map[string]bool{".mp3": true, ".flac": true, ".m4a": true, ".m4b": true, ".aac": true, ".ogg": true, ".oga": true, ".opus": true, ".wav": true}
	videoExt = map[string]bool{".mp4": true, ".m4v": true, ".webm": true, ".mkv": true, ".mov": true}
	imageExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}
)

// Folder images, best first.
var coverNames = []string{"cover", "folder", "front", "album", "albumart", "albumartsmall"}

// checkpointEvery bounds how much scan work a stop can throw away. Behind
// idlegate the container may be stopped mid-scan; each checkpoint means the
// next start re-reads only what changed since.
const checkpointEvery = 30 * time.Second

// Scan walks every root, re-reading tags only for files whose size or mtime
// changed, and publishes the new set of tracks. Concurrent calls wait for the
// running scan and then run their own.
func (l *Library) Scan(ctx context.Context) error { return l.ScanRoots(ctx) }

// ScanRoots is Scan limited to the named roots (all if none are given); the
// other roots keep their indexed tracks without being touched. An import
// rescans only the download directory, so the HDD can stay asleep.
func (l *Library) ScanRoots(ctx context.Context, names ...string) error {
	// Serialise rather than coalesce: a scan already running may have passed
	// the directory a caller just wrote to.
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	l.scanning.Store(true)
	l.scanned.Store(0)
	defer l.scanning.Store(false)

	t0 := time.Now()
	old := map[string]*Track{}
	for _, t := range l.All() {
		old[t.Root+"\x00"+t.Path] = t
	}
	firstScan := len(old) == 0

	var found []*Track
	lastCheckpoint := time.Now()
	for _, root := range l.roots {
		if len(names) > 0 && !contains(names, root.Name) {
			for _, t := range old {
				if t.Root == root.Name {
					found = append(found, t)
				}
			}
			continue
		}
		err := walkRoot(ctx, root, func(dir string, entries []fs.DirEntry) {
			for _, t := range l.scanDir(root, dir, entries, old, firstScan) {
				found = append(found, t)
			}
			l.scanned.Add(int64(len(entries)))
			if time.Since(lastCheckpoint) > checkpointEvery {
				lastCheckpoint = time.Now()
				l.checkpoint(old, found)
			}
		})
		if err != nil {
			if ctx.Err() != nil {
				l.checkpoint(old, found)
				return ctx.Err()
			}
			// An unreadable root (unmounted disk) must not wipe its tracks:
			// keep what the index had for it.
			log.Printf("library: scan %s: %v (keeping indexed tracks)", root.Dir, err)
			for _, t := range old {
				if t.Root == root.Name {
					found = append(found, t)
				}
			}
		}
	}

	found = dedupeIDs(found)
	l.lastScan.Store(time.Now().Unix())
	l.publish(found)
	if err := l.save(found); err != nil {
		log.Printf("library: saving index: %v", err)
	}
	log.Printf("library: scanned %d tracks in %s", len(found), time.Since(t0).Round(time.Millisecond))
	return nil
}

// checkpoint saves the old index overlaid with what this scan found so far.
// Nothing is removed until a scan completes.
func (l *Library) checkpoint(old map[string]*Track, found []*Track) {
	merged := make(map[string]*Track, len(old)+len(found))
	for k, t := range old {
		merged[k] = t
	}
	for _, t := range found {
		merged[t.Root+"\x00"+t.Path] = t
	}
	list := make([]*Track, 0, len(merged))
	for _, t := range merged {
		list = append(list, t)
	}
	if err := l.save(dedupeIDs(list)); err != nil {
		log.Printf("library: checkpoint: %v", err)
	}
}

// dedupeIDs handles the (unlikely) case of the same relative path under two
// roots: the later one gets a root-qualified ID.
func dedupeIDs(ts []*Track) []*Track {
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].Root != ts[j].Root {
			return ts[i].Root < ts[j].Root
		}
		return ts[i].Path < ts[j].Path
	})
	seen := make(map[string]bool, len(ts))
	for i, t := range ts {
		if seen[t.ID] {
			c := *t
			c.ID = IDFor(t.Root + ":" + t.Path)
			ts[i] = &c
		}
		seen[ts[i].ID] = true
	}
	return ts
}

// walkRoot calls fn once per directory with its entries.
func walkRoot(ctx context.Context, root Root, fn func(dir string, entries []fs.DirEntry)) error {
	if _, err := os.Stat(root.Dir); err != nil {
		return err
	}
	return filepath.WalkDir(root.Dir, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if p == root.Dir {
				return err
			}
			log.Printf("library: %v", err)
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != root.Dir && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			log.Printf("library: %v", err)
			return nil
		}
		fn(p, entries)
		return nil
	})
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "@eaDir" || name == "#recycle" || name == "lost+found"
}

type group struct {
	audio []fs.DirEntry
	video fs.DirEntry
	info  string // name of <base>.info.json
	image string // name of <base>.jpg etc.
}

// scanDir turns one directory's files into tracks. Files sharing a base name
// belong together: "Song.flac" + "Song.mp4" is a track with a music video,
// "abc.m4a" + "abc.mp4" + "abc.info.json" + "abc.jpg" is a YouTube import.
func (l *Library) scanDir(root Root, dir string, entries []fs.DirEntry, old map[string]*Track, firstScan bool) []*Track {
	groups := map[string]*group{}
	get := func(base string) *group {
		g := groups[base]
		if g == nil {
			g = &group{}
			groups[base] = g
		}
		return g
	}
	var images []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".info.json") {
			get(name[:len(name)-len(".info.json")]).info = name
			continue
		}
		ext := filepath.Ext(lower)
		base := name[:len(name)-len(ext)]
		switch {
		case audioExt[ext]:
			g := get(base)
			g.audio = append(g.audio, e)
		case videoExt[ext]:
			if g := get(base); g.video == nil {
				g.video = e
			}
		case imageExt[ext]:
			images = append(images, name)
			get(base).image = name
		}
	}

	folderCover := pickFolderCover(images, groups)
	relDir, _ := filepath.Rel(root.Dir, dir)
	relDir = filepath.ToSlash(relDir)

	var out []*Track
	for _, g := range groups {
		files := g.audio
		if len(files) == 0 && g.video != nil {
			files = []fs.DirEntry{g.video} // standalone music video
		}
		for i, f := range files {
			rel := path.Join(relDir, f.Name())
			var video string
			if g.video != nil && (i == 0 || f == g.video) {
				video = path.Join(relDir, g.video.Name())
			}
			cover := folderCover
			if g.image != "" {
				cover = path.Join(relDir, g.image)
			} else if cover != "" {
				cover = path.Join(relDir, cover)
			}
			t := l.scanFile(root, dir, rel, f, g.info, old, firstScan)
			if t == nil {
				continue
			}
			t.Video, t.Cover = video, cover
			out = append(out, t)
		}
	}
	return out
}

func pickFolderCover(images []string, groups map[string]*group) string {
	for _, want := range coverNames {
		for _, img := range images {
			if strings.EqualFold(strings.TrimSuffix(img, filepath.Ext(img)), want) {
				return img
			}
		}
	}
	// A single loose image that isn't some track's own thumbnail.
	var loose []string
	for _, img := range images {
		g := groups[strings.TrimSuffix(img, filepath.Ext(img))]
		if g == nil || (len(g.audio) == 0 && g.video == nil) {
			loose = append(loose, img)
		}
	}
	if len(loose) == 1 {
		return loose[0]
	}
	return ""
}

// scanFile returns the track for one media file, reusing the old index entry
// when the file (and its sidecar) are unchanged.
func (l *Library) scanFile(root Root, dir, rel string, f fs.DirEntry, infoName string, old map[string]*Track, firstScan bool) *Track {
	fi, err := f.Info()
	if err != nil {
		return nil
	}
	mtime := fi.ModTime().UnixNano()
	var infoPath string
	if infoName != "" {
		infoPath = filepath.Join(dir, infoName)
		if st, err := os.Stat(infoPath); err == nil && st.ModTime().UnixNano() > mtime {
			mtime = st.ModTime().UnixNano()
		}
	}
	prev := old[root.Name+"\x00"+rel]
	if prev != nil && prev.Size == fi.Size() && prev.ModTime == mtime {
		c := *prev
		return &c
	}

	abs := filepath.Join(dir, f.Name())
	t := &Track{ID: IDFor(rel), Root: root.Name, Path: rel, Size: fi.Size(), ModTime: mtime}
	switch {
	case prev != nil:
		t.Added = prev.Added
	case firstScan:
		t.Added = fi.ModTime().Unix() // best guess for "recently added"
	default:
		t.Added = time.Now().Unix()
	}
	if in, err := tags.Read(abs); err == nil || in != (tags.Info{}) {
		t.Title, t.Artist, t.Album, t.AlbumArtist, t.Genre = in.Title, in.Artist, in.Album, in.AlbumArtist, in.Genre
		t.Year, t.TrackNo, t.DiscNo, t.Duration, t.Embedded = in.Year, in.Track, in.Disc, in.Duration, in.HasPicture
	}
	if infoPath != "" {
		applyInfoJSON(t, infoPath)
	}
	fillFromPath(t)
	return t
}

var leadingTrackNo = regexp.MustCompile(`^(\d{1,3})(?:\s*[-.)_]\s*|\s+)`)

// fillFromPath fills gaps from the usual "Artist/Album/01 - Title.ext" or
// "Artist - Title.ext" layouts.
func fillFromPath(t *Track) {
	base := path.Base(t.Path)
	base = strings.TrimSuffix(base, path.Ext(base))
	if m := leadingTrackNo.FindStringSubmatch(base); m != nil && len(base) > len(m[0]) {
		if t.TrackNo == 0 {
			t.TrackNo = leadingInt(m[1])
		}
		base = base[len(m[0]):]
	}
	if t.Title == "" {
		if a, title, ok := strings.Cut(base, " - "); ok && t.Artist == "" {
			t.Artist, t.Title = strings.TrimSpace(a), strings.TrimSpace(title)
		} else {
			t.Title = base
		}
	}
	dirs := strings.Split(path.Dir(t.Path), "/")
	if dirs[0] == "." {
		dirs = nil
	}
	if t.Album == "" && len(dirs) >= 2 {
		t.Album = dirs[len(dirs)-1]
	}
	if t.Artist == "" && len(dirs) >= 2 {
		t.Artist = dirs[len(dirs)-2]
	} else if t.Artist == "" && len(dirs) == 1 {
		t.Artist = dirs[0]
	}
	if t.Artist == "" {
		t.Artist = "Unknown artist"
	}
}

func leadingInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
