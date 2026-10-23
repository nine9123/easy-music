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

	// AuthUser and AuthPassword turn on HTTP Basic authentication. An empty
	// password leaves the server open.
	AuthUser     string
	AuthPassword string
}

// MinPasswordLength is enforced on AUTH_PASSWORD: the server can't tell a
// brute-force attempt behind a proxy from its owner, so the password itself
// has to be out of reach of guessing.
const MinPasswordLength = 12

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
		AuthUser:         env("AUTH_USER", "admin"),
	}
	var err error
	if c.AuthPassword, err = secretEnv("AUTH_PASSWORD"); err != nil {
		return c, err
	}
	if c.AuthPassword != "" && len([]rune(c.AuthPassword)) < MinPasswordLength {
		return c, fmt.Errorf("AUTH_PASSWORD: use at least %d characters", MinPasswordLength)
	}
	if c.AuthPassword != "" && c.AuthUser == "" {
		return c, fmt.Errorf("AUTH_USER: empty, but AUTH_PASSWORD is set")
	}
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

// secretEnv reads k, or the file named by k_FILE (Docker secrets). Setting
// both is an error rather than a guess about which one wins. Surrounding
// whitespace (a trailing newline in the file) is not part of the secret.
func secretEnv(k string) (string, error) {
	v, file := os.Getenv(k), os.Getenv(k+"_FILE")
	switch {
	case v != "" && file != "":
		return "", fmt.Errorf("%s and %s_FILE are both set", k, k)
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", k, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(v), nil
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
