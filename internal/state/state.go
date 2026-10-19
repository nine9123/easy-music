// Package state keeps what cannot be rebuilt from the music files: play
// statistics, favorites, playlists, radio stations and the download queue.
//
// It is one small JSON file, written atomically a moment after each change
// and on shutdown, so a stop (idlegate putting the container to sleep) loses
// at most the last couple of seconds.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Stats are the listening statistics of one track (or station).
type Stats struct {
	Plays       int     `json:"plays"`
	Skips       int     `json:"skips,omitempty"`
	Seconds     float64 `json:"seconds,omitempty"` // total time listened
	FirstPlayed int64   `json:"firstPlayed,omitempty"`
	LastPlayed  int64   `json:"lastPlayed,omitempty"`
}

// Playlist is an ordered list of track IDs.
type Playlist struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Tracks  []string `json:"tracks"`
	Created int64    `json:"created"`
	Updated int64    `json:"updated"`
}

// Station is an internet radio stream.
type Station struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Added      int64  `json:"added"`
	Plays      int    `json:"plays"`
	LastPlayed int64  `json:"lastPlayed,omitempty"`
	Favorite   bool   `json:"favorite,omitempty"`
}

// Job statuses.
const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

// Job is a queued or finished URL import.
type Job struct {
	ID         string  `json:"id"`
	URL        string  `json:"url"`
	Title      string  `json:"title,omitempty"`
	Status     string  `json:"status"`
	Progress   float64 `json:"progress"` // 0..100
	Error      string  `json:"error,omitempty"`
	PlaylistID string  `json:"playlistId,omitempty"`
	TrackID    string  `json:"trackId,omitempty"`
	Created    int64   `json:"created"`
	Updated    int64   `json:"updated"`
}

// Play is one entry of the listening history.
type Play struct {
	Track string `json:"t"`
	At    int64  `json:"at"`
}

// historyMax bounds the listening history kept for the mixer.
const historyMax = 5000

type data struct {
	Version   int               `json:"version"`
	Stats     map[string]*Stats `json:"stats"`
	Favorites map[string]int64  `json:"favorites"` // track ID -> unix time favorited
	Playlists []*Playlist       `json:"playlists"`
	Stations  []*Station        `json:"stations"`
	Jobs      []*Job            `json:"jobs"`
	History   []Play            `json:"history"`
}

// ErrNotFound is returned for unknown playlist/station/job IDs.
var ErrNotFound = errors.New("not found")

// Store is the state, safe for concurrent use.
type Store struct {
	path string
	now  func() time.Time

	mu      sync.RWMutex
	d       data
	version int64

	saveMu   sync.Mutex
	timer    *time.Timer
	debounce time.Duration
}

// Open loads state.json from dir, or starts empty.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "state.json"), now: time.Now, debounce: 2 * time.Second}
	b, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &s.d); err != nil {
			// Never silently replace state we could not read: it is the one
			// thing that can't be rebuilt.
			return nil, errors.New("state: " + s.path + " is corrupt (" + err.Error() + "); fix or move it aside")
		}
	}
	if s.d.Stats == nil {
		s.d.Stats = map[string]*Stats{}
	}
	if s.d.Favorites == nil {
		s.d.Favorites = map[string]int64{}
	}
	s.d.Version = 1
	// A download that was running when we were stopped starts over (yt-dlp
	// resumes its partial file).
	for _, j := range s.d.Jobs {
		if j.Status == JobRunning {
			j.Status = JobQueued
		}
	}
	return s, nil
}

// Version changes on every modification (for ETags).
func (s *Store) Version() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// changed must be called with mu held for writing.
func (s *Store) changed() {
	s.version++
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.timer == nil {
		s.timer = time.AfterFunc(s.debounce, func() {
			s.saveMu.Lock()
			s.timer = nil
			s.saveMu.Unlock()
			if err := s.Save(); err != nil {
				log.Printf("state: %v", err)
			}
		})
	}
}

// Save writes the state now.
func (s *Store) Save() error {
	s.mu.RLock()
	b, err := json.Marshal(&s.d)
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return writeAtomic(s.path, b)
}

// Close flushes pending changes.
func (s *Store) Close() error {
	s.saveMu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.saveMu.Unlock()
	return s.Save()
}

func writeAtomic(name string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), ".state-*.json")
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

// NewID returns a random identifier.
func NewID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ---- stats ----

// RecordPlay counts a listen of seconds length.
func (s *Store) RecordPlay(id string, seconds float64) Stats {
	now := s.now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.d.Stats[id]
	if st == nil {
		st = &Stats{FirstPlayed: now}
		s.d.Stats[id] = st
	}
	st.Plays++
	st.Seconds += seconds
	st.LastPlayed = now
	s.d.History = append(s.d.History, Play{Track: id, At: now})
	if over := len(s.d.History) - historyMax; over > 0 {
		s.d.History = append([]Play(nil), s.d.History[over:]...)
	}
	s.changed()
	return *st
}

// RecordSkip counts a track skipped early. The mixer uses skips to learn
// what not to suggest.
func (s *Store) RecordSkip(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.d.Stats[id]
	if st == nil {
		st = &Stats{}
		s.d.Stats[id] = st
	}
	st.Skips++
	s.changed()
}

// Stats returns a copy of every track's stats.
func (s *Store) Stats() map[string]Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Stats, len(s.d.Stats))
	for k, v := range s.d.Stats {
		out[k] = *v
	}
	return out
}

// StatsFor returns one track's stats.
func (s *Store) StatsFor(id string) Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st := s.d.Stats[id]; st != nil {
		return *st
	}
	return Stats{}
}

// History returns the most recent plays, oldest first.
func (s *Store) History(n int) []Play {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.d.History
	if n > 0 && len(h) > n {
		h = h[len(h)-n:]
	}
	return append([]Play(nil), h...)
}

// ---- favorites ----

// SetFavorite marks or unmarks a track.
func (s *Store) SetFavorite(id string, fav bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, was := s.d.Favorites[id]
	if was == fav {
		return
	}
	if fav {
		s.d.Favorites[id] = s.now().Unix()
	} else {
		delete(s.d.Favorites, id)
	}
	s.changed()
}

// Favorites returns favorited track IDs with the time they were favorited.
func (s *Store) Favorites() map[string]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]int64, len(s.d.Favorites))
	for k, v := range s.d.Favorites {
		out[k] = v
	}
	return out
}

// ---- playlists ----

func copyPlaylist(p *Playlist) Playlist {
	c := *p
	c.Tracks = append([]string{}, p.Tracks...)
	return c
}

// Playlists returns all playlists sorted by name.
func (s *Store) Playlists() []Playlist {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Playlist, 0, len(s.d.Playlists))
	for _, p := range s.d.Playlists {
		out = append(out, copyPlaylist(p))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Playlist returns one playlist.
func (s *Store) Playlist(id string) (Playlist, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.d.Playlists {
		if p.ID == id {
			return copyPlaylist(p), nil
		}
	}
	return Playlist{}, ErrNotFound
}

// CreatePlaylist adds a playlist.
func (s *Store) CreatePlaylist(name string, tracks []string) Playlist {
	now := s.now().Unix()
	p := &Playlist{ID: NewID(), Name: name, Tracks: append([]string{}, tracks...), Created: now, Updated: now}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Playlists = append(s.d.Playlists, p)
	s.changed()
	return copyPlaylist(p)
}

// UpdatePlaylist applies fn to a playlist under the lock.
func (s *Store) UpdatePlaylist(id string, fn func(p *Playlist)) (Playlist, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.d.Playlists {
		if p.ID == id {
			fn(p)
			p.Updated = s.now().Unix()
			s.changed()
			return copyPlaylist(p), nil
		}
	}
	return Playlist{}, ErrNotFound
}

// DeletePlaylist removes a playlist.
func (s *Store) DeletePlaylist(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.d.Playlists {
		if p.ID == id {
			s.d.Playlists = append(s.d.Playlists[:i], s.d.Playlists[i+1:]...)
			s.changed()
			return nil
		}
	}
	return ErrNotFound
}

// ---- stations ----

// Stations returns all stations.
func (s *Store) Stations() []Station {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Station, 0, len(s.d.Stations))
	for _, st := range s.d.Stations {
		out = append(out, *st)
	}
	return out
}

// Station returns one station.
func (s *Store) Station(id string) (Station, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, st := range s.d.Stations {
		if st.ID == id {
			return *st, nil
		}
	}
	return Station{}, ErrNotFound
}

// AddStation adds a station unless one with the same URL exists, in which
// case that one is returned.
func (s *Store) AddStation(name, url string) Station {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.d.Stations {
		if st.URL == url {
			return *st
		}
	}
	st := &Station{ID: NewID(), Name: name, URL: url, Added: s.now().Unix()}
	s.d.Stations = append(s.d.Stations, st)
	s.changed()
	return *st
}

// UpdateStation applies fn to a station under the lock.
func (s *Store) UpdateStation(id string, fn func(st *Station)) (Station, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.d.Stations {
		if st.ID == id {
			fn(st)
			s.changed()
			return *st, nil
		}
	}
	return Station{}, ErrNotFound
}

// DeleteStation removes a station.
func (s *Store) DeleteStation(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, st := range s.d.Stations {
		if st.ID == id {
			s.d.Stations = append(s.d.Stations[:i], s.d.Stations[i+1:]...)
			s.changed()
			return nil
		}
	}
	return ErrNotFound
}

// ---- jobs ----

// Jobs returns all jobs, newest first.
func (s *Store) Jobs() []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Job, 0, len(s.d.Jobs))
	for i := len(s.d.Jobs) - 1; i >= 0; i-- {
		out = append(out, *s.d.Jobs[i])
	}
	return out
}

// AddJob queues an import.
func (s *Store) AddJob(url, playlistID string) Job {
	now := s.now().Unix()
	j := &Job{ID: NewID(), URL: url, Status: JobQueued, PlaylistID: playlistID, Created: now, Updated: now}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Jobs = append(s.d.Jobs, j)
	s.changed()
	return *j
}

// NextJob returns the oldest queued job.
func (s *Store) NextJob() (Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, j := range s.d.Jobs {
		if j.Status == JobQueued {
			return *j, true
		}
	}
	return Job{}, false
}

// UpdateJob applies fn to a job under the lock. Progress-only updates are not
// worth a disk write, so persist=false just updates memory.
func (s *Store) UpdateJob(id string, persist bool, fn func(j *Job)) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.d.Jobs {
		if j.ID == id {
			fn(j)
			j.Updated = s.now().Unix()
			if persist {
				s.changed()
			} else {
				s.version++
			}
			return *j, nil
		}
	}
	return Job{}, ErrNotFound
}

// DeleteJob forgets a job.
func (s *Store) DeleteJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, j := range s.d.Jobs {
		if j.ID == id {
			s.d.Jobs = append(s.d.Jobs[:i], s.d.Jobs[i+1:]...)
			s.changed()
			return nil
		}
	}
	return ErrNotFound
}
