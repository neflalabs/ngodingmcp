package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"ngodingmcp/internal/config"
)

func TestAllowDownloadDefaultsOffAndLoadsFromConfig(t *testing.T) {
	isolateConfig(t)
	if m := NewWithPreset("v-test", "/tmp/ws", "", ""); m.allowDownload {
		t.Fatal("allowDownload should default to false")
	}
	if err := config.Save(config.Config{Root: "/tmp/ws", AllowDownload: true}); err != nil {
		t.Fatalf("config.Save: %v", err)
	}
	if m := NewWithPreset("v-test", "", "", ""); !m.allowDownload {
		t.Fatal("allowDownload = false, want true from config")
	}
}

func TestSpaceTogglesAllowDownloadOnly(t *testing.T) {
	isolateConfig(t)
	m := NewWithPreset("v-test", "/tmp/ws", "", "")
	space := tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	for m.focus != fieldAllowDownload {
		m.cycleFocus(1)
	}
	m.Update(space)
	if !m.allowDownload {
		t.Fatal("space should enable allowDownload")
	}
	if m.runEnabled || m.allowSensitive {
		t.Error("the download toggle must not change the other toggles")
	}
	if !strings.Contains(m.View(), "download_file") {
		t.Error("view should show the download toggle")
	}
	m.Update(space)
	if m.allowDownload {
		t.Fatal("second space should disable allowDownload")
	}
}

// The run_command bug: a setting chosen in the TUI never reached the server.
// Start a real server from the TUI and check download_file is really on and
// the choice is saved.
func TestStartFromTUIPassesAllowDownload(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	m := NewWithPreset("v-test", root, "127.0.0.1:0", "")
	m.allowDownload = true
	m.start()
	if m.errMsg != "" {
		t.Fatalf("start: %s", m.errMsg)
	}
	t.Cleanup(m.stop)
	if m.srv == nil || !m.srv.DownloadEnabled() {
		t.Fatal("server started from the TUI must have download_file enabled")
	}
	if !strings.Contains(m.View(), "download: on") {
		t.Error("summary line should say download: on")
	}
	if saved := config.Load(); !saved.AllowDownload {
		t.Error("allowDownload not saved to config")
	}
}
