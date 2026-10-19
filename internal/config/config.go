// Package config reads easy-music's settings from the environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the whole runtime configuration. See README.md for the meaning
// of each environment variable.
type Config struct {
	Listen string

	// MusicDirs are scanned for music and treated as read-only.
	MusicDirs []string
	// DownloadDir receives YouTube imports. It is scanned like a music dir
	// but must be writable. Empty disables imports.
	DownloadDir string
	// CacheDir holds everything that can be rebuilt: the library index,
	// covers, fetched metadata and the hot-track copies. Put it on the SSD.
	CacheDir string
	// StateDir holds what cannot be rebuilt: play counts, favorites,
	// playlists, radio stations, the download queue. Small; back it up.
	StateDir string

	ScanOnStart  bool
	ScanInterval time.Duration

	HotCacheSize     int64 // bytes; 0 disables
	HotCacheMinPlays int

	CoverFetch bool // look up missing covers on MusicBrainz / Cover Art Archive

	YTDLP     string   // yt-dlp binary
	YTDLPArgs []string // extra arguments
}

// FromEnv builds a Config from environment variables, applying defaults.
func FromEnv() (Config, error) {
	c := Config{
		Listen:           env("LISTEN", ":8080"),
		MusicDirs:        splitList(env("MUSIC_DIRS", "/music")),
		DownloadDir:      env("DOWNLOAD_DIR", "/downloads"),
		CacheDir:         env("CACHE_DIR", "/cache"),
		StateDir:         env("STATE_DIR", "/config"),
		YTDLP:            env("YTDLP", "yt-dlp"),
		YTDLPArgs:        strings.Fields(os.Getenv("YTDLP_ARGS")),
		HotCacheMinPlays: 3,
	}
	var err error
	if c.ScanOnStart, err = boolEnv("SCAN_ON_START", true); err != nil {
		return c, err
	}
	if c.CoverFetch, err = boolEnv("COVER_FETCH", true); err != nil {
		return c, err
	}
	if v := os.Getenv("SCAN_INTERVAL"); v != "" && v != "0" {
		if c.ScanInterval, err = time.ParseDuration(v); err != nil {
			return c, fmt.Errorf("SCAN_INTERVAL: %w", err)
		}
	}
	if c.HotCacheSize, err = ParseSize(env("HOTCACHE_SIZE", "4G")); err != nil {
		return c, fmt.Errorf("HOTCACHE_SIZE: %w", err)
	}
	if v := os.Getenv("HOTCACHE_MIN_PLAYS"); v != "" {
		if c.HotCacheMinPlays, err = strconv.Atoi(v); err != nil || c.HotCacheMinPlays < 1 {
			return c, fmt.Errorf("HOTCACHE_MIN_PLAYS: want a positive integer, got %q", v)
		}
	}
	for i, d := range c.MusicDirs {
		c.MusicDirs[i] = filepath.Clean(d)
	}
	if c.DownloadDir != "" {
		c.DownloadDir = filepath.Clean(c.DownloadDir)
	}
	if len(c.MusicDirs) == 0 && c.DownloadDir == "" {
		return c, fmt.Errorf("no MUSIC_DIRS and no DOWNLOAD_DIR: nothing to serve")
	}
	return c, nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return strings.TrimSpace(v)
	}
	return def
}

func boolEnv(k string, def bool) (bool, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s: %w", k, err)
	}
	return b, nil
}

// splitList splits on commas and newlines (not colons: they are legal in
// paths, and a list that breaks on one is a miserable thing to debug).
func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseSize parses "0", "512M", "4G", "1.5T" or a plain byte count.
// Suffixes are binary (G = 2^30).
func ParseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimSuffix(strings.TrimSuffix(s, "B"), "I")
	mult := float64(1)
	if s != "" {
		switch s[len(s)-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		case 'T':
			mult = 1 << 40
		}
		if mult != 1 {
			s = s[:len(s)-1]
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return int64(f * mult), nil
}
