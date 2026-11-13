package tui

import (
	"strconv"
	"strings"

	"ngodingmcp/internal/mcpserver"
)

// summaryLine is the one-line overview shown while the server runs.
func summaryLine(tools int, runOn, guardOn bool) string {
	toolsText := "tools: ? active"
	if tools > 0 {
		toolsText = "tools: " + strconv.Itoa(tools) + " active"
	}
	run := dimStyle.Render("run: off")
	if runOn {
		run = okStyle.Render("run: on")
	}
	guard := okStyle.Render("sensitive guard: on")
	if !guardOn {
		guard = errStyle.Render("sensitive guard: OFF")
	}
	sep := dimStyle.Render("  ·  ")
	return dimStyle.Render(toolsText) + sep + run + sep + guard
}

// activityLogLine recognises a tool-activity record written by the MCP
// server's slog text handler and returns "15:04:05 ▸ edit_file main.go ✓ 3ms".
func activityLogLine(line string) (string, bool) {
	const key = "msg="
	i := strings.Index(line, key)
	if i < 0 {
		return "", false
	}
	rest := line[i+len(key):]
	var msg string
	if strings.HasPrefix(rest, `"`) {
		q, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return "", false
		}
		msg, err = strconv.Unquote(q)
		if err != nil {
			return "", false
		}
	} else {
		msg, _, _ = strings.Cut(rest, " ")
	}
	if !strings.HasPrefix(msg, mcpserver.ActivityPrefix) {
		return "", false
	}
	act := strings.TrimPrefix(msg, mcpserver.ActivityPrefix)
	if clock := logClock(line); clock != "" {
		act = clock + " ▸ " + act
	}
	return act, true
}

// logClock extracts HH:MM:SS from a leading "time=2026-11-13T09:30:00.1+09:00".
func logClock(line string) string {
	if !strings.HasPrefix(line, "time=") {
		return ""
	}
	_, after, ok := strings.Cut(line, "T")
	if !ok || len(after) < 8 {
		return ""
	}
	return after[:8]
}

// renderActivity colours failed calls red and successful ones normally.
func renderActivity(s string) string {
	if strings.Contains(s, " ✗") {
		return errStyle.Render(s)
	}
	return s
}
