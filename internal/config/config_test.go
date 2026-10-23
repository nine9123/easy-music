package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "1024": 1024, "512M": 512 << 20, "4G": 4 << 30, "4GiB": 4 << 30, "1.5t": 3 << 39, "10 GB": 10 << 30} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "lots", "-1G"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("MUSIC_DIRS", "/a, /b/ ,\n/c:with-colon")
	t.Setenv("DOWNLOAD_DIR", "")
	t.Setenv("HOTCACHE_SIZE", "1G")
	t.Setenv("SCAN_INTERVAL", "6h")
	t.Setenv("SCAN_ON_START", "false")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/a", "/b", "/c:with-colon"}; !reflect.DeepEqual(c.MusicDirs, want) {
		t.Errorf("MusicDirs = %q", c.MusicDirs)
	}
	if c.DownloadDir != "" || c.HotCacheSize != 1<<30 || c.ScanInterval != 6*time.Hour || c.ScanOnStart {
		t.Errorf("config = %+v", c)
	}
	t.Setenv("HOTCACHE_MIN_PLAYS", "0")
	if _, err := FromEnv(); err == nil {
		t.Errorf("HOTCACHE_MIN_PLAYS=0 accepted")
	}
}

func TestAuthFromEnv(t *testing.T) {
	t.Setenv("MUSIC_DIRS", "/a")
	c, err := FromEnv()
	if err != nil || c.AuthPassword != "" {
		t.Fatalf("no password: %+v, %v", c, err)
	}

	t.Setenv("AUTH_PASSWORD", "short")
	if _, err := FromEnv(); err == nil {
		t.Errorf("short AUTH_PASSWORD accepted")
	}
	t.Setenv("AUTH_PASSWORD", "correct horse battery")
	if c, err = FromEnv(); err != nil || c.AuthUser != "admin" || c.AuthPassword != "correct horse battery" {
		t.Errorf("AUTH_PASSWORD: %q %q, %v", c.AuthUser, c.AuthPassword, err)
	}
	t.Setenv("AUTH_USER", "")
	if _, err := FromEnv(); err == nil {
		t.Errorf("empty AUTH_USER accepted")
	}
	t.Setenv("AUTH_USER", "me")

	file := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(file, []byte("from a docker secret\n"), 0o600)
	t.Setenv("AUTH_PASSWORD_FILE", file)
	if _, err := FromEnv(); err == nil {
		t.Errorf("AUTH_PASSWORD and AUTH_PASSWORD_FILE both accepted")
	}
	t.Setenv("AUTH_PASSWORD", "")
	if c, err = FromEnv(); err != nil || c.AuthUser != "me" || c.AuthPassword != "from a docker secret" {
		t.Errorf("AUTH_PASSWORD_FILE: %q %q, %v", c.AuthUser, c.AuthPassword, err)
	}
	t.Setenv("AUTH_PASSWORD_FILE", file+".missing")
	if _, err := FromEnv(); err == nil {
		t.Errorf("missing AUTH_PASSWORD_FILE accepted")
	}
}
