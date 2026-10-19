package server

import (
	"net/http"
	"strings"

	"github.com/nine9123/easy-music/internal/state"
)

type playlistSummary struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Count    int     `json:"count"`
	Duration float64 `json:"duration"`
	Updated  int64   `json:"updated"`
	Cover    string  `json:"coverTrack,omitempty"`
}

func (s *Server) summarize(p state.Playlist) playlistSummary {
	sum := playlistSummary{ID: p.ID, Name: p.Name, Count: len(p.Tracks), Updated: p.Updated}
	for _, id := range p.Tracks {
		if t, ok := s.lib.Get(id); ok {
			sum.Duration += t.Duration
			if sum.Cover == "" {
				sum.Cover = id
			}
		}
	}
	return sum
}

func (s *Server) listPlaylists(w http.ResponseWriter, r *http.Request) {
	out := []playlistSummary{}
	for _, p := range s.st.Playlists() {
		out = append(out, s.summarize(p))
	}
	writeJSON(w, r, http.StatusOK, out)
}

func (s *Server) createPlaylist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string   `json:"name"`
		Tracks []string `json:"tracks"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeError(w, r, http.StatusBadRequest, "name is required")
		return
	}
	p := s.st.CreatePlaylist(body.Name, body.Tracks)
	writeJSON(w, r, http.StatusCreated, s.summarize(p))
}

// getPlaylist returns the playlist with its tracks. Entries whose file is
// gone are kept (marked missing) so moving files around doesn't silently
// shrink a playlist.
func (s *Server) getPlaylist(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.Playlist(r.PathValue("id"))
	if err != nil {
		notFoundOr(w, r, err)
		return
	}
	v := s.newView()
	tracks := make([]trackView, 0, len(p.Tracks))
	for _, id := range p.Tracks {
		if t, ok := s.lib.Get(id); ok {
			tracks = append(tracks, v.track(t))
		} else {
			tracks = append(tracks, trackView{ID: id, Title: "(missing file)", Missing: true})
		}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"id": p.ID, "name": p.Name, "updated": p.Updated, "tracks": tracks})
}

// updatePlaylist: PATCH {"name"?: "...", "tracks"?: [ids]} — tracks replaces
// the list (reorder, remove).
func (s *Server) updatePlaylist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   *string   `json:"name"`
		Tracks *[]string `json:"tracks"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) == "" {
		writeError(w, r, http.StatusBadRequest, "name cannot be empty")
		return
	}
	p, err := s.st.UpdatePlaylist(r.PathValue("id"), func(p *state.Playlist) {
		if body.Name != nil {
			p.Name = strings.TrimSpace(*body.Name)
		}
		if body.Tracks != nil {
			p.Tracks = append([]string{}, (*body.Tracks)...)
		}
	})
	if err != nil {
		notFoundOr(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, s.summarize(p))
}

// addToPlaylist: POST {"tracks": [ids]} appends tracks not already in it.
func (s *Server) addToPlaylist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tracks []string `json:"tracks"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	p, err := s.st.UpdatePlaylist(r.PathValue("id"), func(p *state.Playlist) {
		for _, id := range body.Tracks {
			if _, ok := s.lib.Get(id); ok && !containsStr(p.Tracks, id) {
				p.Tracks = append(p.Tracks, id)
			}
		}
	})
	if err != nil {
		notFoundOr(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, s.summarize(p))
}

func (s *Server) deletePlaylist(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeletePlaylist(r.PathValue("id")); err != nil {
		notFoundOr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
