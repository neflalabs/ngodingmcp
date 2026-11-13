package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	want := Config{
		Root:  "/tmp/ws",
		Addr:  "127.0.0.1:9999",
		Token: "secret",
		Run:   RunConfig{Enabled: true},
	}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := filepath.Join(dir, "ngodingmcp", "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}

	got := Load()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
}

func TestLoadLegacyConfigWithoutRun(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	path := filepath.Join(dir, "ngodingmcp", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"root":"/tmp/ws","addr":"127.0.0.1:8080","token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := Load()
	if got.Root != "/tmp/ws" {
		t.Errorf("root = %q", got.Root)
	}
	if got.Run.Enabled {
		t.Error("run policy should be disabled by default")
	}
}

func TestLoadIgnoresOldRunPolicyFields(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	path := filepath.Join(dir, "ngodingmcp", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"root":"/tmp/ws","run":{"enabled":true,"prefix":["rm -rf"],"exact":["sh -c evil"]}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	got := Load()
	if !got.Run.Enabled {
		t.Error("enabled flag should still load")
	}
	// Unknown fields are ignored; they cannot widen the built-in policy.
	if got.Root != "/tmp/ws" {
		t.Errorf("root = %q", got.Root)
	}
}

func TestLoadMissingReturnsZero(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := Load(); !reflect.DeepEqual(got, Config{}) {
		t.Fatalf("Load = %+v, want zero value", got)
	}
}

func TestSplitList(t *testing.T) {
	got := SplitList(" go test , ,git diff,  ")
	want := []string{"go test", "git diff"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitList = %v, want %v", got, want)
	}
	if got := SplitList(""); len(got) != 0 {
		t.Errorf("SplitList(empty) = %v", got)
	}
}
