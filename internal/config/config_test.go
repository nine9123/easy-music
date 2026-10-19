package config

import (
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
