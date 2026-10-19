// Package radio parses internet radio playlist files (.pls, .m3u/.m3u8) and
// resolves a URL that may point at one of those rather than at a stream.
package radio

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Entry is one stream in a playlist file.
type Entry struct {
	Title string
	URL   string
}

// ErrNoStreams is returned when a playlist contains no http(s) streams.
var ErrNoStreams = errors.New("radio: no http(s) streams in playlist")

// Parse reads a .pls or .m3u/.m3u8 playlist. Only http(s) entries are kept:
// local paths in an m3u are a music playlist, not a radio station.
func Parse(data []byte) ([]Entry, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	var out []Entry
	if bytes.Contains(bytes.ToLower(data[:min(len(data), 512)]), []byte("[playlist]")) {
		out = parsePLS(data)
	} else {
		out = parseM3U(data)
	}
	if len(out) == 0 {
		return nil, ErrNoStreams
	}
	return out, nil
}

func parsePLS(data []byte) []Entry {
	files, titles := map[int]string{}, map[int]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		lk := strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch {
		case strings.HasPrefix(lk, "file"):
			if n, err := strconv.Atoi(lk[4:]); err == nil {
				files[n] = v
			}
		case strings.HasPrefix(lk, "title"):
			if n, err := strconv.Atoi(lk[5:]); err == nil {
				titles[n] = v
			}
		}
	}
	nums := make([]int, 0, len(files))
	for n := range files {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var out []Entry
	for _, n := range nums {
		if isStreamURL(files[n]) {
			out = append(out, Entry{Title: titles[n], URL: files[n]})
		}
	}
	return out
}

func parseM3U(data []byte) []Entry {
	var out []Entry
	var title string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXTINF:"):
			if _, t, ok := strings.Cut(line, ","); ok {
				title = strings.TrimSpace(t)
			}
		case strings.HasPrefix(line, "#"):
		default:
			if isStreamURL(line) {
				out = append(out, Entry{Title: title, URL: line})
			}
			title = ""
		}
	}
	return out
}

func isStreamURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// LooksLikePlaylist reports whether a URL or file name is a playlist file.
func LooksLikePlaylist(name string) bool {
	if u, err := url.Parse(name); err == nil {
		name = u.Path
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".pls", ".m3u", ".m3u8":
		return true
	}
	return false
}

// Resolve turns a station URL into a playable stream URL and a name. If the
// URL is a playlist (by extension or content type) it is fetched and its first
// stream is used. HLS (.m3u8 with segments) is returned as is: browsers that
// play HLS want the playlist URL itself.
func Resolve(ctx context.Context, hc *http.Client, raw string) (stream, name string, err error) {
	if !isStreamURL(raw) {
		return "", "", fmt.Errorf("radio: not an http(s) URL: %q", raw)
	}
	if !LooksLikePlaylist(raw) {
		return raw, "", nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("radio: fetching playlist: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if bytes.Contains(data, []byte("#EXT-X-")) {
		return raw, "", nil // HLS
	}
	entries, err := Parse(data)
	if err != nil {
		return "", "", err
	}
	return entries[0].URL, entries[0].Title, nil
}
