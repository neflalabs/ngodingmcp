package tui

import (
	"strings"
	"testing"

	"ngodingmcp/internal/config"
)

// The control panel resolves settings like `serve`: flag > env >
// config.json > default.
func TestNewWithPresetPrecedenceMatchesServe(t *testing.T) {
	isolateConfig(t)
	t.Setenv("NGODINGMCP_ALLOW_RUN", "")
	t.Setenv("NGODINGMCP_OAUTH", "")
	off := false
	if err := config.Save(config.Config{Root: "/cfg/root", Addr: "127.0.0.1:1", Token: "cfg", OAuth: &off}); err != nil {
		t.Fatal(err)
	}

	m := NewWithPreset("v-test", "", "", "")
	if got := m.inputs[fieldRoot].Value(); got != "/cfg/root" {
		t.Errorf("root from config = %q", got)
	}
	if m.settings.OAuth {
		t.Error("OAuth should be off from config.json")
	}

	// The environment now beats config.json (it used to be the other way
	// round in the TUI).
	t.Setenv("NGODINGMCP_ROOT", "/env/root")
	t.Setenv("NGODINGMCP_ADDR", "127.0.0.1:2")
	t.Setenv("NGODINGMCP_TOKEN", "env")
	t.Setenv("NGODINGMCP_OAUTH", "1")
	m = NewWithPreset("v-test", "", "", "")
	if m.inputs[fieldRoot].Value() != "/env/root" || m.inputs[fieldAddr].Value() != "127.0.0.1:2" || m.inputs[fieldToken].Value() != "env" {
		t.Errorf("env did not beat config: root=%q addr=%q token=%q",
			m.inputs[fieldRoot].Value(), m.inputs[fieldAddr].Value(), m.inputs[fieldToken].Value())
	}
	if !m.settings.OAuth {
		t.Error("NGODINGMCP_OAUTH=1 should beat config.json")
	}

	// Flags beat both.
	m = NewWithPreset("v-test", "/flag/root", "127.0.0.1:3", "flag")
	if m.inputs[fieldRoot].Value() != "/flag/root" || m.inputs[fieldAddr].Value() != "127.0.0.1:3" || m.inputs[fieldToken].Value() != "flag" {
		t.Errorf("flags did not win")
	}
}

func TestNewWithPresetReportsBadEnvAndRefusesStart(t *testing.T) {
	isolateConfig(t)
	t.Setenv("NGODINGMCP_OAUTH", "yes")
	m := NewWithPreset("v-test", t.TempDir(), "127.0.0.1:0", "")
	err := m.ConfigError()
	if err == nil || !strings.Contains(err.Error(), "NGODINGMCP_OAUTH") {
		t.Fatalf("ConfigError = %v, want one naming NGODINGMCP_OAUTH", err)
	}
	if !strings.Contains(m.View(), "NGODINGMCP_OAUTH") {
		t.Error("view should show the bad variable")
	}
	m.start()
	if m.running || m.srv != nil {
		t.Fatal("server started despite a malformed environment")
	}
	if !strings.Contains(m.errMsg, "cannot start") {
		t.Errorf("errMsg = %q", m.errMsg)
	}
}
