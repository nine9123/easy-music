package server

import (
	"net/http"
)

func (s *Server) listDownloads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]any{"available": s.yt.Available(), "jobs": s.st.Jobs()})
}

// addDownload: POST {"url": "...", "playlistId"?: "..."} queues an import;
// when it finishes the track is added to the playlist.
func (s *Server) addDownload(w http.ResponseWriter, r *http.Request) {
	if !s.yt.Available() {
		writeError(w, r, http.StatusServiceUnavailable, "imports need DOWNLOAD_DIR and yt-dlp")
		return
	}
	var body struct {
		URL        string `json:"url"`
		PlaylistID string `json:"playlistId"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.PlaylistID != "" {
		if _, err := s.st.Playlist(body.PlaylistID); err != nil {
			writeError(w, r, http.StatusBadRequest, "unknown playlist")
			return
		}
	}
	j, err := s.yt.Enqueue(body.URL, body.PlaylistID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, r, http.StatusAccepted, j)
}

func (s *Server) retryDownload(w http.ResponseWriter, r *http.Request) {
	if s.yt == nil {
		writeError(w, r, http.StatusNotFound, "not found")
		return
	}
	if err := s.yt.Retry(r.PathValue("id")); err != nil {
		notFoundOr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteDownload cancels a running import or forgets a finished one. The
// downloaded files stay: they are a track in the library by then.
func (s *Server) deleteDownload(w http.ResponseWriter, r *http.Request) {
	var err error
	if s.yt != nil {
		err = s.yt.Cancel(r.PathValue("id"))
	} else {
		err = s.st.DeleteJob(r.PathValue("id"))
	}
	if err != nil {
		notFoundOr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
