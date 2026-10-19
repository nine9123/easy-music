module github.com/nine9123/easy-music

// Standard library only, like idlegate: no go.sum, nothing to audit or update.
// Tag parsing, playlists and the web UI are all in-tree; yt-dlp and ffmpeg are
// external programs in the Docker image, not Go dependencies.
go 1.23
