package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveLoadAllowSensitive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := Save(Config{Root: "/tmp/ws", AllowSensitive: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load(); !got.AllowSensitive {
		t.Fatal("AllowSensitive = false after round trip, want true")
	}

	if err := Save(Config{Root: "/tmp/ws"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load(); got.AllowSensitive {
		t.Fatal("AllowSensitive = true after saving false")
	}
}

func TestLoadLegacyConfigKeepsSensitiveGuard(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "ngodingmcp", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"root":"/tmp/ws","run":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load(); got.AllowSensitive {
		t.Fatal("legacy config must keep the sensitive guard on")
	}
}

func TestSaveTightensExistingPermissions(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("permission bits are POSIX-specific")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "ngodingmcp")
	path := filepath.Join(cfgDir, "config.json")

	// Simulate a config written by an older build with loose permissions.
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"token":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Save(Config{Token: "new-secret"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	di, err := os.Stat(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 700", perm)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 600", perm)
	}
	if got := Load(); got.Token != "new-secret" {
		t.Errorf("token = %q, want new-secret", got.Token)
	}

	// No temp files may be left next to the config.
	entries, err := os.ReadDir(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}
