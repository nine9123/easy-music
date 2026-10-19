package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nine9123/easy-music/internal/library"
	"github.com/nine9123/easy-music/internal/mix"
	"github.com/nine9123/easy-music/internal/state"
)

// trackView is a track as the API returns it: library metadata plus the
// user's stats.
type trackView struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album,omitempty"`
	AlbumID     string  `json:"albumId"`
	AlbumArtist string  `json:"albumArtist,omitempty"`
	Genre       string  `json:"genre,omitempty"`
	Year        int     `json:"year,omitempty"`
	TrackNo     int     `json:"trackNo,omitempty"`
	DiscNo      int     `json:"discNo,omitempty"`
	Duration    float64 `json:"duration,omitempty"`
	HasVideo    bool    `json:"hasVideo,omitempty"`
	Source      string  `json:"source,omitempty"`
	File        string  `json:"file"`
	Added       int64   `json:"added"`
	Plays       int     `json:"plays,omitempty"`
	Skips       int     `json:"skips,omitempty"`
	LastPlayed  int64   `json:"lastPlayed,omitempty"`
	Favorite    bool    `json:"favorite,omitempty"`
	Missing     bool    `json:"missing,omitempty"` // playlist entry whose file is gone
}

// view holds the per-request snapshot of stats and favorites.
type view struct {
	stats map[string]state.Stats
	favs  map[string]int64
}

func (s *Server) newView() view { return view{s.st.Stats(), s.st.Favorites()} }

func (v view) track(t *library.Track) trackView {
	st := v.stats[t.ID]
	_, fav := v.favs[t.ID]
	return trackView{
		ID: t.ID, Title: t.Title, Artist: t.Artist, Album: t.Album, AlbumID: albumID(t), AlbumArtist: t.AlbumArtist,
		Genre: t.Genre, Year: t.Year, TrackNo: t.TrackNo, DiscNo: t.DiscNo, Duration: t.Duration,
		HasVideo: t.HasVideo(), Source: t.Source, File: t.Root + "/" + t.Path, Added: t.Added,
		Plays: st.Plays, Skips: st.Skips, LastPlayed: st.LastPlayed, Favorite: fav,
	}
}

func (v view) tracks(ts []*library.Track) []trackView {
	out := make([]trackView, len(ts))
	for i, t := range ts {
		out[i] = v.track(t)
	}
	return out
}

func albumID(t *library.Track) string {
	h := sha1.Sum([]byte(t.AlbumKey()))
	return hex.EncodeToString(h[:8])
}

func artistID(key string) string {
	h := sha1.Sum([]byte(key))
	return hex.EncodeToString(h[:8])
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	scanning, seen, last := s.lib.Status()
	used, max, n := s.hot.Usage()
	var lastScan int64
	if !last.IsZero() {
		lastScan = last.Unix()
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"tracks":   len(s.lib.All()),
		"scanning": scanning,
		"scanned":  seen,
		"lastScan": lastScan,
		"hotCache": map[string]any{"used": used, "max": max, "tracks": n},
		"imports":  s.yt.Available(),
	})
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := s.lib.Scan(context.Background()); err != nil {
			log.Printf("scan: %v", err)
		}
		s.OnScanned()
	}()
	w.WriteHeader(http.StatusAccepted)
}

// matcher implements the search box: every word must appear somewhere in
// title, artist, album, album artist or genre.
func matcher(q string) func(*library.Track) bool {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return func(*library.Track) bool { return true }
	}
	return func(t *library.Track) bool {
		hay := strings.ToLower(t.Title + "\x00" + t.Artist + "\x00" + t.Album + "\x00" + t.AlbumArtist + "\x00" + t.Genre)
		for _, w := range words {
			if !strings.Contains(hay, w) {
				return false
			}
		}
		return true
	}
}

func intParam(r *http.Request, name string, def, lo, hi int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	return min(max(n, lo), hi)
}

// listTracks: GET /api/tracks?q=&filter=favorites|video|imported|unplayed
// &artist=<artistId>&album=<albumId>&sort=library|title|artist|album|year|
// duration|added|plays|lastPlayed|favorited|random&order=asc|desc&offset=&limit=
func (s *Server) listTracks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := s.newView()
	match := matcher(q.Get("q"))
	filter, artist, album := q.Get("filter"), q.Get("artist"), q.Get("album")
	var ts []*library.Track
	for _, t := range s.lib.All() {
		if !match(t) {
			continue
		}
		switch filter {
		case "favorites":
			if _, ok := v.favs[t.ID]; !ok {
				continue
			}
		case "video":
			if !t.HasVideo() {
				continue
			}
		case "imported":
			if t.Source == "" {
				continue
			}
		case "unplayed":
			if v.stats[t.ID].Plays > 0 {
				continue
			}
		}
		if album != "" && albumID(t) != album {
			continue
		}
		if artist != "" {
			ok := false
			for _, k := range append(t.ArtistKeys(), library.SplitArtists(t.SortArtist())...) {
				ok = ok || artistID(k) == artist
			}
			if !ok {
				continue
			}
		}
		ts = append(ts, t)
	}
	sortTracks(ts, q.Get("sort"), q.Get("order") == "desc", v)
	total := len(ts)
	offset := intParam(r, "offset", 0, 0, total)
	limit := intParam(r, "limit", 200, 1, 5000)
	ts = ts[offset:min(offset+limit, total)]
	writeJSON(w, r, http.StatusOK, map[string]any{"total": total, "offset": offset, "tracks": v.tracks(ts)})
}

func sortTracks(ts []*library.Track, by string, desc bool, v view) {
	less := map[string]func(a, b *library.Track) bool{
		"title":      func(a, b *library.Track) bool { return strings.ToLower(a.Title) < strings.ToLower(b.Title) },
		"artist":     func(a, b *library.Track) bool { return strings.ToLower(a.Artist) < strings.ToLower(b.Artist) },
		"album":      func(a, b *library.Track) bool { return strings.ToLower(a.Album) < strings.ToLower(b.Album) },
		"year":       func(a, b *library.Track) bool { return a.Year < b.Year },
		"duration":   func(a, b *library.Track) bool { return a.Duration < b.Duration },
		"added":      func(a, b *library.Track) bool { return a.Added < b.Added },
		"plays":      func(a, b *library.Track) bool { return v.stats[a.ID].Plays < v.stats[b.ID].Plays },
		"lastPlayed": func(a, b *library.Track) bool { return v.stats[a.ID].LastPlayed < v.stats[b.ID].LastPlayed },
		"favorited":  func(a, b *library.Track) bool { return v.favs[a.ID] < v.favs[b.ID] },
	}[by]
	switch {
	case by == "random":
		rand.Shuffle(len(ts), func(i, j int) { ts[i], ts[j] = ts[j], ts[i] })
	case less != nil:
		// Stable on top of library order, so ties stay grouped by album.
		sort.SliceStable(ts, func(i, j int) bool {
			if desc {
				return less(ts[j], ts[i])
			}
			return less(ts[i], ts[j])
		})
	case desc:
		for i, j := 0, len(ts)-1; i < j; i, j = i+1, j-1 {
			ts[i], ts[j] = ts[j], ts[i]
		}
	}
}

func (s *Server) track(w http.ResponseWriter, r *http.Request) (*library.Track, bool) {
	t, ok := s.lib.Get(r.PathValue("id"))
	if !ok {
		writeError(w, r, http.StatusNotFound, "no such track")
	}
	return t, ok
}

func (s *Server) getTrack(w http.ResponseWriter, r *http.Request) {
	if t, ok := s.track(w, r); ok {
		writeJSON(w, r, http.StatusOK, s.newView().track(t))
	}
}

// streamTrack serves the audio, from the SSD hot cache when it has a copy.
func (s *Server) streamTrack(w http.ResponseWriter, r *http.Request) {
	t, ok := s.track(w, r)
	if !ok {
		return
	}
	p, cached := s.hot.Lookup(t.ID, t.Size, t.ModTime)
	if !cached {
		p, _ = s.lib.Abs(t.Root, t.Path)
	}
	w.Header().Set("X-Hot-Cache", strconv.FormatBool(cached))
	serveFile(w, r, p, path.Base(t.Path))
}

func (s *Server) streamVideo(w http.ResponseWriter, r *http.Request) {
	t, ok := s.track(w, r)
	if !ok {
		return
	}
	if t.Video == "" {
		writeError(w, r, http.StatusNotFound, "track has no video")
		return
	}
	p, _ := s.lib.Abs(t.Root, t.Video)
	serveFile(w, r, p, path.Base(t.Video))
}

// serveFile supports Range requests (seeking) via http.ServeContent. The
// handler runs for as long as the transfer does, which idlegate counts as
// activity.
func serveFile(w http.ResponseWriter, r *http.Request, p, name string) {
	f, err := os.Open(p)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "file not available")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if strings.HasSuffix(strings.ToLower(name), ".m4a") {
		w.Header().Set("Content-Type", "audio/mp4")
	}
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func (s *Server) cover(w http.ResponseWriter, r *http.Request) {
	t, ok := s.track(w, r)
	if !ok {
		return
	}
	p, ok := s.covers.Get(t)
	if !ok {
		w.Header().Set("Cache-Control", "no-store") // it may be fetched later
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, p)
}

func (s *Server) played(w http.ResponseWriter, r *http.Request) {
	t, ok := s.track(w, r)
	if !ok {
		return
	}
	var body struct {
		Seconds float64 `json:"seconds"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, &body) {
		return
	}
	st := s.st.RecordPlay(t.ID, body.Seconds)
	s.considerHot(t, st.Plays)
	writeJSON(w, r, http.StatusOK, st)
}

func (s *Server) skipped(w http.ResponseWriter, r *http.Request) {
	if t, ok := s.track(w, r); ok {
		s.st.RecordSkip(t.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) favorite(fav bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if t, ok := s.track(w, r); ok {
			s.st.SetFavorite(t.ID, fav)
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

type albumView struct {
	ID       string `json:"id"`
	Album    string `json:"album"`
	Artist   string `json:"artist"`
	Year     int    `json:"year,omitempty"`
	Tracks   int    `json:"tracks"`
	Plays    int    `json:"plays"`
	Added    int64  `json:"added"`
	HasVideo bool   `json:"hasVideo,omitempty"`
	Cover    string `json:"coverTrack"` // track whose cover to show
}

// listAlbums: GET /api/albums?q=&sort=name|artist|year|plays|added&order=
func (s *Server) listAlbums(w http.ResponseWriter, r *http.Request) {
	v := s.newView()
	match := matcher(r.URL.Query().Get("q"))
	byID := map[string]*albumView{}
	var list []*albumView
	for _, t := range s.lib.All() {
		if t.Album == "" || !match(t) {
			continue
		}
		id := albumID(t)
		a := byID[id]
		if a == nil {
			a = &albumView{ID: id, Album: t.Album, Artist: t.SortArtist(), Cover: t.ID}
			byID[id] = a
			list = append(list, a)
		}
		a.Tracks++
		a.Plays += v.stats[t.ID].Plays
		a.Year = max(a.Year, t.Year)
		a.Added = max(a.Added, t.Added)
		a.HasVideo = a.HasVideo || t.HasVideo()
	}
	q := r.URL.Query()
	less := map[string]func(a, b *albumView) bool{
		"name":   func(a, b *albumView) bool { return strings.ToLower(a.Album) < strings.ToLower(b.Album) },
		"artist": func(a, b *albumView) bool { return strings.ToLower(a.Artist) < strings.ToLower(b.Artist) },
		"year":   func(a, b *albumView) bool { return a.Year < b.Year },
		"plays":  func(a, b *albumView) bool { return a.Plays < b.Plays },
		"added":  func(a, b *albumView) bool { return a.Added < b.Added },
	}[q.Get("sort")]
	if less != nil {
		desc := q.Get("order") == "desc"
		sort.SliceStable(list, func(i, j int) bool {
			if desc {
				return less(list[j], list[i])
			}
			return less(list[i], list[j])
		})
	}
	writeJSON(w, r, http.StatusOK, list)
}

type artistView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Tracks int    `json:"tracks"`
	Plays  int    `json:"plays"`
	Cover  string `json:"coverTrack"`
}

// listArtists groups by the main credited artist (featured artists don't
// get an entry of their own, but the artist filter on /api/tracks finds
// their features too).
func (s *Server) listArtists(w http.ResponseWriter, r *http.Request) {
	v := s.newView()
	match := matcher(r.URL.Query().Get("q"))
	byKey := map[string]*artistView{}
	var list []*artistView
	for _, t := range s.lib.All() {
		if !match(t) {
			continue
		}
		credits := library.Credits(t.SortArtist())
		if len(credits) == 0 {
			continue
		}
		key := library.SplitArtists(credits[0])[0]
		a := byKey[key]
		if a == nil {
			a = &artistView{ID: artistID(key), Name: credits[0], Cover: t.ID}
			byKey[key] = a
			list = append(list, a)
		}
		a.Tracks++
		a.Plays += v.stats[t.ID].Plays
	}
	switch r.URL.Query().Get("sort") {
	case "plays":
		sort.SliceStable(list, func(i, j int) bool { return list[i].Plays > list[j].Plays })
	case "tracks":
		sort.SliceStable(list, func(i, j int) bool { return list[i].Tracks > list[j].Tracks })
	default:
		sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	}
	writeJSON(w, r, http.StatusOK, list)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	v := s.newView()
	all := s.lib.All()
	var plays int
	var seconds float64
	for _, st := range v.stats {
		plays += st.Plays
		seconds += st.Seconds
	}
	top := append([]*library.Track(nil), all...)
	sort.SliceStable(top, func(i, j int) bool { return v.stats[top[i].ID].Plays > v.stats[top[j].ID].Plays })
	n := 0
	for n < len(top) && n < 25 && v.stats[top[n].ID].Plays > 0 {
		n++
	}
	artistPlays := map[string]int{}
	artistName := map[string]string{}
	for _, t := range all {
		if p := v.stats[t.ID].Plays; p > 0 {
			for _, c := range library.Credits(t.Artist) {
				k := library.SplitArtists(c)[0]
				artistPlays[k] += p
				if _, ok := artistName[k]; !ok {
					artistName[k] = c
				}
			}
		}
	}
	type artistStat struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Plays int    `json:"plays"`
	}
	var artists []artistStat
	for k, p := range artistPlays {
		artists = append(artists, artistStat{artistID(k), artistName[k], p})
	}
	sort.Slice(artists, func(i, j int) bool {
		if artists[i].Plays != artists[j].Plays {
			return artists[i].Plays > artists[j].Plays
		}
		return artists[i].Name < artists[j].Name
	})
	if len(artists) > 25 {
		artists = artists[:25]
	}
	type recent struct {
		trackView
		At int64 `json:"at"`
	}
	var rec []recent
	hist := s.st.History(50)
	for i := len(hist) - 1; i >= 0; i-- {
		if t, ok := s.lib.Get(hist[i].Track); ok {
			rec = append(rec, recent{v.track(t), hist[i].At})
		}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"tracks":     len(all),
		"favorites":  len(v.favs),
		"plays":      plays,
		"seconds":    seconds,
		"topTracks":  v.tracks(top[:n]),
		"topArtists": artists,
		"recent":     rec,
	})
}

// mixer returns a mixer built from current data. Rebuilding is O(library)
// so it is reused until the library changes or stats are a minute stale.
func (s *Server) getMixer() *mix.Mixer {
	s.mixMu.Lock()
	defer s.mixMu.Unlock()
	lv, sv := s.lib.Version(), s.st.Version()
	if s.mixer != nil && lv == s.mixLib && (sv == s.mixState || time.Since(s.mixAt) < time.Minute) {
		return s.mixer
	}
	var pls [][]string
	for _, p := range s.st.Playlists() {
		pls = append(pls, p.Tracks)
	}
	s.mixer = mix.New(mix.Input{
		Tracks: s.lib.All(), Stats: s.st.Stats(), Favorites: s.st.Favorites(),
		History: s.st.History(0), Playlists: pls,
	})
	s.mixLib, s.mixState, s.mixAt = lv, sv, time.Now()
	return s.mixer
}

// mix: POST /api/mix {"seed": id, "recent": [ids, oldest first], "count": n}
// Without a seed it is a variety-aware shuffle of the whole library.
func (s *Server) mix(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Seed   string   `json:"seed"`
		Recent []string `json:"recent"`
		Count  int      `json:"count"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.Count = min(max(body.Count, 1), 100)
	if len(body.Recent) > 500 {
		body.Recent = body.Recent[len(body.Recent)-500:]
	}
	picks := s.getMixer().Next(mix.Request{Seed: body.Seed, Recent: body.Recent, Count: body.Count})
	writeJSON(w, r, http.StatusOK, s.newView().tracks(picks))
}

func sortBy[T any](s []T, less func(a, b T) bool) {
	sort.SliceStable(s, func(i, j int) bool { return less(s[i], s[j]) })
}
