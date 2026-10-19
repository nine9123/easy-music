// Package mix picks what to play next in radio/random mode.
//
// The goal is "more like this, but not the same thing again". Similarity comes
// from genre, era, and which artists you listen to together (consecutive plays
// and shared playlists). Variety is enforced, not hoped for:
//
//   - an artist cannot come back until ArtistGap other tracks have played
//     (featured artists count, so "A feat. B" blocks both A and B);
//   - an album cannot come back until AlbumGap tracks have played;
//   - the next pick is similar to a blend of the seed and the previous pick,
//     so the station drifts instead of orbiting the seed;
//   - among the best candidates the pick is random (weighted by score), so the
//     same seed gives a different station every time;
//   - recently played and often-skipped tracks are pushed down.
package mix

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/nine9123/easy-music/internal/library"
	"github.com/nine9123/easy-music/internal/state"
)

// Defaults for the variety rules.
const (
	DefaultArtistGap = 8
	DefaultAlbumGap  = 30
	topK             = 30   // candidates the weighted draw chooses from
	temperature      = 0.08 // lower = stick closer to the best match
	shuffleSample    = 400  // candidates considered per pick without a seed
	sessionGap       = 20 * 60
)

// Input is everything the mixer learns from.
type Input struct {
	Tracks    []*library.Track
	Stats     map[string]state.Stats
	Favorites map[string]int64
	History   []state.Play // oldest first
	Playlists [][]string   // track IDs per playlist
	Now       time.Time
}

type feat struct {
	t       *library.Track
	genres  []string // normalised whole genres
	words   []string // genre words, for partial matches ("progressive rock" ~ "rock")
	year    int
	artists []string
	album   string
	bonus   float64 // favorites, plays, skips, recency; independent of the anchor
}

// Mixer is an immutable snapshot built from an Input. Build one per library
// or stats change; Next is safe for concurrent use.
type Mixer struct {
	feats []feat
	byID  map[string]int
	rel   map[string]map[string]float64 // artist -> artist -> relatedness 0..1
}

// New precomputes features and the artist relatedness graph.
func New(in Input) *Mixer {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	m := &Mixer{byID: make(map[string]int, len(in.Tracks))}
	maxPlays := 1
	for _, st := range in.Stats {
		maxPlays = max(maxPlays, st.Plays)
	}
	now := in.Now.Unix()
	for _, t := range in.Tracks {
		g, w := genreTokens(t.Genre)
		f := feat{t: t, genres: g, words: w, year: t.Year, artists: t.ArtistKeys(), album: mixAlbumKey(t)}
		st := in.Stats[t.ID]
		if _, ok := in.Favorites[t.ID]; ok {
			f.bonus += 0.08
		}
		f.bonus += 0.04 * math.Log1p(float64(st.Plays)) / math.Log1p(float64(maxPlays))
		if n := st.Plays + st.Skips; n >= 2 {
			f.bonus -= 0.25 * float64(st.Skips) / float64(n)
		}
		switch age := now - st.LastPlayed; {
		case st.LastPlayed == 0:
		case age < 6*3600:
			f.bonus -= 0.3
		case age < 3*86400:
			f.bonus -= 0.1
		}
		m.byID[t.ID] = len(m.feats)
		m.feats = append(m.feats, f)
	}
	m.rel = m.buildRelatedness(in)
	return m
}

// buildRelatedness counts how often two artists are played close together
// (same listening session, within 3 plays) or sit near each other in a
// playlist, normalised so prolific artists don't relate to everything.
func (m *Mixer) buildRelatedness(in Input) map[string]map[string]float64 {
	co := map[string]map[string]float64{}
	deg := map[string]float64{}
	add := func(a, b []string, w float64) {
		for _, x := range a {
			for _, y := range b {
				if x == y {
					continue
				}
				if co[x] == nil {
					co[x] = map[string]float64{}
				}
				if co[y] == nil {
					co[y] = map[string]float64{}
				}
				co[x][y] += w
				co[y][x] += w
				deg[x] += w
				deg[y] += w
			}
		}
	}
	artistsOf := func(id string) []string {
		if i, ok := m.byID[id]; ok {
			return m.feats[i].artists
		}
		return nil
	}
	h := in.History
	for i := range h {
		for j := i + 1; j < len(h) && j <= i+3; j++ {
			if h[j].At-h[j-1].At > sessionGap {
				break
			}
			add(artistsOf(h[i].Track), artistsOf(h[j].Track), 1)
		}
	}
	for _, pl := range in.Playlists {
		for i := range pl {
			for j := i + 1; j < len(pl) && j <= i+5; j++ {
				add(artistsOf(pl[i]), artistsOf(pl[j]), 2) // a deliberate choice counts double
			}
		}
	}
	for x, row := range co {
		for y, c := range row {
			row[y] = math.Min(1, 2*c/math.Sqrt(deg[x]*deg[y]))
		}
	}
	return co
}

// Request asks for Count tracks following Seed (optional) and Recent (the
// session so far, oldest first; it is excluded and drives the gaps).
type Request struct {
	Seed      string
	Recent    []string
	Count     int
	ArtistGap int
	AlbumGap  int
	Rand      *rand.Rand
}

// Next returns up to req.Count tracks.
func (m *Mixer) Next(req Request) []*library.Track {
	if req.ArtistGap <= 0 {
		req.ArtistGap = DefaultArtistGap
	}
	if req.AlbumGap <= 0 {
		req.AlbumGap = DefaultAlbumGap
	}
	if req.Rand == nil {
		req.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	seed := -1
	if i, ok := m.byID[req.Seed]; ok {
		seed = i
	}
	used := map[int]bool{}
	var seq []int // seed + recent + picks, for the gap rules
	if seed >= 0 {
		seq = append(seq, seed)
		used[seed] = true
	}
	for _, id := range req.Recent {
		if i, ok := m.byID[id]; ok {
			seq = append(seq, i)
			used[i] = true
		}
	}

	var out []*library.Track
	for len(out) < req.Count {
		i := m.pick(req, seed, seq, used)
		if i < 0 {
			break
		}
		used[i] = true
		seq = append(seq, i)
		out = append(out, m.feats[i].t)
	}
	return out
}

// pick chooses one track, relaxing the variety rules only if nothing passes
// them (a tiny library, or one artist's discography).
func (m *Mixer) pick(req Request, seed int, seq []int, used map[int]bool) int {
	last := -1
	if len(seq) > 0 {
		last = seq[len(seq)-1]
	}
	for _, gaps := range [][2]int{{req.ArtistGap, req.AlbumGap}, {req.ArtistGap / 2, req.AlbumGap / 3}, {1, 0}, {0, 0}} {
		blockedArtists := map[string]bool{}
		for _, i := range tail(seq, gaps[0]) {
			for _, a := range m.feats[i].artists {
				blockedArtists[a] = true
			}
		}
		blockedAlbums := map[string]bool{}
		for _, i := range tail(seq, gaps[1]) {
			blockedAlbums[m.feats[i].album] = true
		}
		ok := func(i int) bool {
			if used[i] || blockedAlbums[m.feats[i].album] {
				return false
			}
			for _, a := range m.feats[i].artists {
				if blockedArtists[a] {
					return false
				}
			}
			return true
		}
		if i := m.choose(req.Rand, seed, last, ok); i >= 0 {
			return i
		}
	}
	return -1
}

type scored struct {
	i int
	s float64
}

func (m *Mixer) choose(rng *rand.Rand, seed, last int, ok func(int) bool) int {
	var cands []scored
	if seed >= 0 {
		// Radio: score everything against the seed and the previous pick.
		ws, wl := 0.6, 0.4
		if last == seed || last < 0 {
			ws, wl = 1, 0
		}
		for i := range m.feats {
			if !ok(i) {
				continue
			}
			s := ws*m.sim(i, seed) + m.feats[i].bonus
			if wl > 0 {
				s += wl * m.sim(i, last)
			}
			cands = append(cands, scored{i, s})
		}
	} else {
		// Shuffle: a random sample, nudged towards favorites and towards
		// sounding a bit like the previous track, so it isn't jarring.
		n := len(m.feats)
		for tries := 0; tries < shuffleSample*3 && len(cands) < shuffleSample; tries++ {
			i := rng.Intn(max(n, 1))
			if n == 0 || !ok(i) {
				continue
			}
			s := m.feats[i].bonus + rng.Float64()*0.3
			if last >= 0 {
				s += 0.3 * m.sim(i, last)
			}
			cands = append(cands, scored{i, s})
		}
		if len(cands) == 0 { // sampling missed; fall back to a full pass
			for i := range m.feats {
				if ok(i) {
					cands = append(cands, scored{i, m.feats[i].bonus})
				}
			}
		}
	}
	if len(cands) == 0 {
		return -1
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].s > cands[b].s })
	// At most perArtist candidates per artist, or one artist with a big
	// discography fills the whole pool and the draw is no draw at all.
	pool := cands[:0:0]
	perArtist := map[string]int{}
	for _, c := range cands {
		a := ""
		if as := m.feats[c.i].artists; len(as) > 0 {
			a = as[0]
		}
		if perArtist[a] >= 2 {
			continue
		}
		perArtist[a]++
		pool = append(pool, c)
		if len(pool) == topK {
			break
		}
	}
	cands = pool
	total := 0.0
	w := make([]float64, len(cands))
	for k, c := range cands {
		w[k] = math.Exp((c.s - cands[0].s) / temperature)
		total += w[k]
	}
	r := rng.Float64() * total
	for k := range cands {
		if r -= w[k]; r <= 0 {
			return cands[k].i
		}
	}
	return cands[len(cands)-1].i
}

// sim is the similarity of two tracks in 0..1. Missing data counts as
// "neutral" rather than "different", so an untagged library still works (it
// just behaves more like a shuffle).
func (m *Mixer) sim(a, b int) float64 {
	fa, fb := &m.feats[a], &m.feats[b]
	genre := 0.25
	if len(fa.genres) > 0 && len(fb.genres) > 0 {
		genre = 0.7*jaccard(fa.genres, fb.genres) + 0.3*jaccard(fa.words, fb.words)
	}
	year := 0.4
	if fa.year > 0 && fb.year > 0 {
		year = math.Exp(-math.Abs(float64(fa.year-fb.year)) / 8)
	}
	rel := 0.0
	for _, x := range fa.artists {
		for _, y := range fb.artists {
			if x == y {
				rel = 1 // same artist: similar, but the gap rule keeps it rare
			} else if r := m.rel[x][y]; r > rel {
				rel = r
			}
		}
	}
	return 0.35*genre + 0.15*year + 0.5*rel
}

func tail(s []int, n int) []int {
	if n <= 0 {
		return nil
	}
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for _, x := range a {
		for _, y := range b {
			if x == y {
				inter++
				break
			}
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

var genreStop = map[string]bool{"music": true, "and": true, "the": true, "n": true}

// genreTokens splits "Hip-Hop/Rap; Progressive Rock" into whole genres
// {"hiphop", "rap", "progressiverock"} and words {"hip", "hop", "rap",
// "progressive", "rock"}.
func genreTokens(s string) (genres, words []string) {
	for _, g := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return strings.ContainsRune("/,;|", r) }) {
		var b strings.Builder
		for _, r := range g {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
		if k := b.String(); k != "" && !contains(genres, k) {
			genres = append(genres, k)
		}
		for _, w := range strings.FieldsFunc(g, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			if !genreStop[w] && !contains(words, w) {
				words = append(words, w)
			}
		}
	}
	return genres, words
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// mixAlbumKey is the album the album gap applies to. Untagged imports all sit
// in one folder, so AlbumKey would make them a single "album" and the gap
// would let only one of them play per AlbumGap picks; each is its own here.
func mixAlbumKey(t *library.Track) string {
	if t.Album == "" && t.Root == library.DownloadsRoot {
		return "track:" + t.ID
	}
	return t.AlbumKey()
}
