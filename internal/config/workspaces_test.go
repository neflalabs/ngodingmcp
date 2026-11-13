package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestUpdateKeepsOtherFields(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	allow := true
	if err := Save(Config{Token: "tok", Workspaces: []SavedWorkspace{{Name: "a", Path: "/tmp/a", AllowSensitive: &allow}}}); err != nil {
		t.Fatal(err)
	}
	if err := Update(func(c *Config) { c.Root = "/tmp/b" }); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.Token != "tok" || got.Root != "/tmp/b" {
		t.Errorf("token/root = %q/%q", got.Token, got.Root)
	}
	if len(got.Workspaces) != 1 || got.Workspaces[0].Name != "a" || got.Workspaces[0].AllowSensitive == nil || !*got.Workspaces[0].AllowSensitive {
		t.Errorf("workspaces not kept: %+v", got.Workspaces)
	}
}

func TestUpdateRefusesBrokenFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(func(c *Config) { c.Root = "/x" }); err == nil {
		t.Fatal("Update overwrote a broken config file")
	}
	data, _ := os.ReadFile(p)
	if string(data) != "{not json" {
		t.Errorf("broken file was changed: %q", data)
	}
}
