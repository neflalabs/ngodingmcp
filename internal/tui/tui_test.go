package tui

import (
	"runtime"
	"testing"

	"ngodingmcp/internal/config"
)

func TestNewWithPresetLoadsRunFlag(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NGODINGMCP_ROOT", "")
	t.Setenv("NGODINGMCP_ADDR", "")
	t.Setenv("NGODINGMCP_TOKEN", "")

	saved := config.Config{
		Root:  "/tmp/ws",
		Addr:  "127.0.0.1:9090",
		Token: "tok",
		Run:   config.RunConfig{Enabled: true},
	}
	if err := config.Save(saved); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	m := NewWithPreset("v-test", "", "", "")

	if !m.runEnabled {
		t.Error("runEnabled = false, want true from config")
	}
	if got := m.inputs[fieldRoot].Value(); got != saved.Root {
		t.Errorf("root = %q, want %q", got, saved.Root)
	}
	if got := m.inputs[fieldAddr].Value(); got != saved.Addr {
		t.Errorf("addr = %q, want %q", got, saved.Addr)
	}
	if got := m.inputs[fieldToken].Value(); got != saved.Token {
		t.Errorf("token = %q, want %q", got, saved.Token)
	}
}

func TestNewWithPresetFlagsOverrideConfig(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NGODINGMCP_ROOT", "")
	t.Setenv("NGODINGMCP_ADDR", "")
	t.Setenv("NGODINGMCP_TOKEN", "")

	if err := config.Save(config.Config{Root: "/tmp/saved", Addr: "127.0.0.1:1111", Token: "saved"}); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	m := NewWithPreset("v-test", "/tmp/preset", "127.0.0.1:2222", "preset-token")
	if got := m.inputs[fieldRoot].Value(); got != "/tmp/preset" {
		t.Errorf("root = %q", got)
	}
	if got := m.inputs[fieldAddr].Value(); got != "127.0.0.1:2222" {
		t.Errorf("addr = %q", got)
	}
	if got := m.inputs[fieldToken].Value(); got != "preset-token" {
		t.Errorf("token = %q", got)
	}
	if m.runEnabled {
		t.Error("runEnabled should default to false")
	}
}

func TestHeaderShowsVersionAndBuildTime(t *testing.T) {
	m := &Model{version: "v2026.11.12+4deb6fa.dirty", built: "2026-11-13 17:45 WIT"}
	if got, want := m.headerVersion(), "v2026.11.12+4deb6fa.dirty  ·  built 2026-11-13 17:45 WIT"; got != want {
		t.Fatalf("headerVersion() = %q, want %q", got, want)
	}

	m.built = ""
	if got := m.headerVersion(); got != "v2026.11.12+4deb6fa.dirty" {
		t.Fatalf("headerVersion() without build time = %q", got)
	}
}
