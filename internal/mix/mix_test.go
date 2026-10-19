package mix

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/nine9123/easy-music/internal/library"
	"github.com/nine9123/easy-music/internal/state"
)

// fakeLibrary: 10 rock and 10 jazz artists, 3 albums of 10 tracks each.
func fakeLibrary() []*library.Track {
	var ts []*library.Track
	for a := 0; a < 20; a++ {
		genre, year := "Rock", 1990+a%5
		if a >= 10 {
			genre, year = "Jazz", 1960+a%5
		}
		for al := 0; al < 3; al++ {
			for n := 0; n < 10; n++ {
				ts = append(ts, &library.Track{
					ID:     fmt.Sprintf("a%d-al%d-t%d", a, al, n),
					Artist: fmt.Sprintf("Artist %d", a),
					Album:  fmt.Sprintf("Album %d-%d", a, al),
					Genre:  genre,
					Year:   year,
				})
			}
		}
	}
	return ts
}

func byID(ts []*library.Track) map[string]*library.Track {
	m := map[string]*library.Track{}
	for _, t := range ts {
		m[t.ID] = t
	}
	return m
}

func TestVarietyRules(t *testing.T) {
	lib := fakeLibrary()
	m := New(Input{Tracks: lib})
	seed := byID(lib)["a0-al0-t0"]
	for run := int64(0); run < 20; run++ {
		got := m.Next(Request{Seed: seed.ID, Count: 40, Rand: rand.New(rand.NewSource(run))})
		if len(got) != 40 {
			t.Fatalf("got %d tracks", len(got))
		}
		seq := append([]*library.Track{seed}, got...)
		rock := 0
		for i, tr := range seq {
			for j := max(0, i-DefaultArtistGap); j < i; j++ {
				if seq[j].Artist == tr.Artist {
					t.Fatalf("run %d: %s repeated after %d tracks", run, tr.Artist, i-j)
				}
			}
			for j := max(0, i-DefaultAlbumGap); j < i; j++ {
				if seq[j].Album == tr.Album {
					t.Fatalf("run %d: album %s repeated after %d tracks", run, tr.Album, i-j)
				}
			}
			if tr.Genre == "Rock" {
				rock++
			}
		}
		// Similar, not random: a rock seed should give a mostly rock station.
		// 40 picks with an 8-artist gap across 10 rock artists leaves room.
		if rock < 30 {
			t.Errorf("run %d: only %d/41 rock tracks for a rock seed", run, rock)
		}
	}
}

func TestDifferentEachTime(t *testing.T) {
	lib := fakeLibrary()
	m := New(Input{Tracks: lib})
	a := m.Next(Request{Seed: "a0-al0-t0", Count: 10, Rand: rand.New(rand.NewSource(1))})
	b := m.Next(Request{Seed: "a0-al0-t0", Count: 10, Rand: rand.New(rand.NewSource(2))})
	same := 0
	for i := range a {
		if a[i].ID == b[i].ID {
			same++
		}
	}
	if same > 3 {
		t.Errorf("two stations from the same seed share %d/10 positions", same)
	}
}

func TestListeningHistoryRelatesArtists(t *testing.T) {
	lib := fakeLibrary()
	// The user keeps playing Artist 0 followed by Artist 15 (a jazz artist).
	var hist []state.Play
	at := time.Now().Add(-30 * 24 * time.Hour).Unix()
	for i := 0; i < 30; i++ {
		hist = append(hist, state.Play{Track: "a0-al0-t1", At: at}, state.Play{Track: fmt.Sprintf("a15-al0-t%d", i%10), At: at + 200})
		at += 86400 / 2
	}
	with := New(Input{Tracks: lib, History: hist})
	without := New(Input{Tracks: lib})
	count := func(m *Mixer) int {
		n := 0
		for run := int64(0); run < 50; run++ {
			for _, tr := range m.Next(Request{Seed: "a0-al0-t0", Count: 3, Rand: rand.New(rand.NewSource(run))}) {
				if tr.Artist == "Artist 15" {
					n++
				}
			}
		}
		return n
	}
	if w, wo := count(with), count(without); w <= wo || w < 8 {
		t.Errorf("Artist 15 suggested %d times with history, %d without", w, wo)
	}
}

func TestSkipsAndRecentPlaysArePushedDown(t *testing.T) {
	lib := fakeLibrary()
	now := time.Now()
	stats := map[string]state.Stats{}
	// Every track of Artist 1 is skipped a lot; Artist 2 was all just played.
	for _, tr := range lib {
		switch tr.Artist {
		case "Artist 1":
			stats[tr.ID] = state.Stats{Plays: 1, Skips: 9}
		case "Artist 2":
			stats[tr.ID] = state.Stats{Plays: 1, LastPlayed: now.Add(-time.Hour).Unix()}
		}
	}
	m := New(Input{Tracks: lib, Stats: stats, Now: now})
	bad := 0
	for run := int64(0); run < 50; run++ {
		for _, tr := range m.Next(Request{Seed: "a0-al0-t0", Count: 3, Rand: rand.New(rand.NewSource(run))}) {
			if tr.Artist == "Artist 1" || tr.Artist == "Artist 2" {
				bad++
			}
		}
	}
	if bad > 5 {
		t.Errorf("skipped/just-played artists picked %d/150 times", bad)
	}
}

func TestTinyLibraryRelaxesRules(t *testing.T) {
	var lib []*library.Track
	for i := 0; i < 5; i++ {
		lib = append(lib, &library.Track{ID: fmt.Sprint(i), Artist: "Only One", Album: "Only Album"})
	}
	got := New(Input{Tracks: lib}).Next(Request{Seed: "0", Count: 10})
	if len(got) != 4 {
		t.Errorf("got %d tracks, want the 4 non-seed tracks", len(got))
	}
}

func TestShuffleWithoutSeed(t *testing.T) {
	lib := fakeLibrary()
	got := New(Input{Tracks: lib}).Next(Request{Count: 30, Rand: rand.New(rand.NewSource(3))})
	if len(got) != 30 {
		t.Fatalf("got %d", len(got))
	}
	for i := range got {
		for j := max(0, i-DefaultArtistGap); j < i; j++ {
			if got[j].Artist == got[i].Artist {
				t.Fatalf("shuffle repeated %s after %d tracks", got[i].Artist, i-j)
			}
		}
	}
}

func TestFeaturedArtistsCountForTheGap(t *testing.T) {
	lib := []*library.Track{
		{ID: "seed", Artist: "A", Album: "x", Genre: "Pop"},
		{ID: "feat", Artist: "B feat. A", Album: "y", Genre: "Pop"},
		{ID: "other", Artist: "C", Album: "z", Genre: "Pop"},
	}
	got := New(Input{Tracks: lib}).Next(Request{Seed: "seed", Count: 1})
	if len(got) != 1 || got[0].ID != "other" {
		t.Errorf("got %v, want the track not featuring the seed artist", got)
	}
}

func TestGenreTokens(t *testing.T) {
	g, w := genreTokens("Hip-Hop/Rap; Progressive Rock")
	if fmt.Sprint(g) != "[hiphop rap progressiverock]" || fmt.Sprint(w) != "[hip hop rap progressive rock]" {
		t.Errorf("genres %v words %v", g, w)
	}
}

// A 50k-track library: building the mixer and picking 5 must stay well
// under what a click can wait for.
func BenchmarkNext50k(b *testing.B) {
	genres := []string{"Rock", "Jazz", "Electronic", "Hip-Hop", "Classical", "Pop", "Metal", "Folk"}
	var lib []*library.Track
	for i := 0; i < 50000; i++ {
		lib = append(lib, &library.Track{
			ID: fmt.Sprint(i), Artist: fmt.Sprintf("Artist %d", i/12), Album: fmt.Sprintf("Album %d", i/12),
			Genre: genres[i/12%len(genres)], Year: 1960 + i%60,
		})
	}
	b.Run("New", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			New(Input{Tracks: lib})
		}
	})
	m := New(Input{Tracks: lib})
	b.Run("Next5", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			m.Next(Request{Seed: "100", Count: 5})
		}
	})
}
