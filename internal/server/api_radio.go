package server

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nine9123/easy-music/internal/radio"
	"github.com/nine9123/easy-music/internal/state"
)

func (s *Server) listStations(w http.ResponseWriter, r *http.Request) {
	list := s.st.Stations()
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Favorite != list[j].Favorite {
			return list[i].Favorite
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	writeJSON(w, r, http.StatusOK, list)
}

// addStation accepts either JSON {"name", "url"} (a stream URL or a URL of
// a .pls/.m3u) or a multipart upload of a .pls/.m3u file in field "file".
func (s *Server) addStation(w http.ResponseWriter, r *http.Request) {
	var entries []radio.Entry
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		f, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "expected a file field")
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 1<<20))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		if entries, err = radio.Parse(data); err != nil {
			writeError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		entries = distinctStations(entries)
	} else {
		var body struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		stream, name, err := radio.Resolve(ctx, s.radio, strings.TrimSpace(body.URL))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		if body.Name = strings.TrimSpace(body.Name); body.Name != "" {
			name = body.Name
		}
		entries = []radio.Entry{{Title: name, URL: stream}}
	}
	var added []state.Station
	for _, e := range entries {
		name := e.Title
		if name == "" {
			if u, err := url.Parse(e.URL); err == nil {
				name = u.Host
			}
		}
		added = append(added, s.st.AddStation(name, e.URL))
	}
	writeJSON(w, r, http.StatusCreated, added)
}

// distinctStations: a .pls usually lists mirrors of one station (same or no
// titles) — keep the first. An m3u with differently titled entries is a list
// of stations — keep them all.
func distinctStations(es []radio.Entry) []radio.Entry {
	titles := map[string]bool{}
	for _, e := range es {
		titles[e.Title] = true
	}
	if len(titles) <= 1 {
		return es[:1]
	}
	return es
}

func (s *Server) updateStation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     *string `json:"name"`
		Favorite *bool   `json:"favorite"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	st, err := s.st.UpdateStation(r.PathValue("id"), func(st *state.Station) {
		if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
			st.Name = strings.TrimSpace(*body.Name)
		}
		if body.Favorite != nil {
			st.Favorite = *body.Favorite
		}
	})
	if err != nil {
		notFoundOr(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, st)
}

func (s *Server) deleteStation(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteStation(r.PathValue("id")); err != nil {
		notFoundOr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) stationPlayed(w http.ResponseWriter, r *http.Request) {
	_, err := s.st.UpdateStation(r.PathValue("id"), func(st *state.Station) {
		st.Plays++
		st.LastPlayed = time.Now().Unix()
	})
	if err != nil {
		notFoundOr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// streamStation relays a radio stream through the server. That avoids
// mixed-content blocking (an http:// stream on an https:// page) and, behind
// idlegate, the open stream keeps the container awake while you listen.
func (s *Server) streamStation(w http.ResponseWriter, r *http.Request) {
	st, err := s.st.Station(r.PathValue("id"))
	if err != nil {
		notFoundOr(w, r, err)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, st.URL, nil)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	req.Header.Set("User-Agent", "easy-music/1.0")
	resp, err := s.radio.Do(req)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, r, http.StatusBadGateway, "station answered "+resp.Status)
		return
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || strings.HasPrefix(ct, "text/") { // some Icecast/SHOUTcast servers lie
		ct = "audio/mpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	if name := resp.Header.Get("Icy-Name"); name != "" {
		w.Header().Set("X-Station-Name", name)
	}
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 16<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
