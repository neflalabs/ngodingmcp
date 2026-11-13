package tui

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestActivityLogLineParsesSlogRecord(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("tool: edit_file main.go ✓ 3ms")
	line := strings.TrimSpace(buf.String())

	got, ok := activityLogLine(line)
	if !ok {
		t.Fatalf("not recognised: %q", line)
	}
	if !strings.HasSuffix(got, " ▸ edit_file main.go ✓ 3ms") {
		t.Errorf("got %q", got)
	}
	if len(got) < 8 || got[2] != ':' || got[5] != ':' {
		t.Errorf("missing HH:MM:SS clock: %q", got)
	}
}

func TestActivityLogLineIgnoresOtherRecords(t *testing.T) {
	for _, line := range []string{
		`time=2026-11-13T09:30:00.000+09:00 level=INFO msg=request method=POST path=/mcp`,
		`time=2026-11-13T09:30:00.000+09:00 level=WARN msg="sensitive-file protection is disabled"`,
		`plain output from a process`,
	} {
		if _, ok := activityLogLine(line); ok {
			t.Errorf("unexpectedly recognised %q", line)
		}
	}
}

func TestSummaryLine(t *testing.T) {
	s := summaryLine(27, true, true)
	for _, want := range []string{"27 active", "run: on", "sensitive guard: on"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q missing %q", s, want)
		}
	}
	s = summaryLine(20, false, false)
	for _, want := range []string{"20 active", "run: off", "sensitive guard: OFF"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q missing %q", s, want)
		}
	}
}
