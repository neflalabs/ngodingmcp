package tui

import (
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"ngodingmcp/internal/config"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NGODINGMCP_ROOT", "")
	t.Setenv("NGODINGMCP_ADDR", "")
	t.Setenv("NGODINGMCP_TOKEN", "")
}

func TestNewWithPresetLoadsAllowSensitive(t *testing.T) {
	isolateConfig(t)
	if err := config.Save(config.Config{Root: "/tmp/ws", AllowSensitive: true}); err != nil {
		t.Fatalf("config.Save: %v", err)
	}
	m := NewWithPreset("v-test", "", "", "")
	if !m.allowSensitive {
		t.Fatal("allowSensitive = false, want true from config")
	}
	if !strings.Contains(m.View(), sensitiveWarning) {
		t.Error("view should show the sensitive warning when the guard is off")
	}
}

func TestAllowSensitiveDefaultsOffWithoutWarning(t *testing.T) {
	isolateConfig(t)
	m := NewWithPreset("v-test", "/tmp/ws", "", "")
	if m.allowSensitive {
		t.Fatal("allowSensitive should default to false")
	}
	if strings.Contains(m.View(), sensitiveWarning) {
		t.Error("warning must not show while the guard is on")
	}
}

func TestSpaceTogglesAllowSensitive(t *testing.T) {
	isolateConfig(t)
	m := NewWithPreset("v-test", "/tmp/ws", "", "")
	space := tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}

	// Move focus to the sensitive toggle (root -> addr -> token -> run -> sensitive).
	for m.focus != fieldAllowSensitive {
		m.cycleFocus(1)
	}
	m.Update(space)
	if !m.allowSensitive {
		t.Fatal("space should enable allowSensitive")
	}
	if m.runEnabled {
		t.Error("space on the sensitive toggle must not change runEnabled")
	}
	m.Update(space)
	if m.allowSensitive {
		t.Fatal("second space should disable allowSensitive")
	}

	// A space in a text field is typed, not treated as a toggle.
	for m.focus != fieldRoot {
		m.cycleFocus(1)
	}
	m.Update(space)
	if m.allowSensitive || m.runEnabled {
		t.Error("space in a text field must not flip toggles")
	}
	if got := m.inputs[fieldRoot].Value(); got != "/tmp/ws " {
		t.Errorf("root = %q, want trailing space typed", got)
	}
}

func TestLogLineCountAccountsForWarning(t *testing.T) {
	isolateConfig(t)
	m := NewWithPreset("v-test", "/tmp/ws", "", "")
	m.height = 60
	off := m.logLineCount(0)
	m.allowSensitive = true
	if on := m.logLineCount(0); on != off-1 {
		t.Errorf("log lines with warning = %d, want %d", on, off-1)
	}
}
