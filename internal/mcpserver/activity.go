package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ActivityPrefix starts the message of every tool-activity log record, so UIs
// such as the TUI can recognise and highlight them.
const ActivityPrefix = "tool: "

// maxTargetLen caps the target shown in an activity line.
const maxTargetLen = 60

// activityMiddleware logs one concise line per tools/call, for example
// "edit_file main.go ✓ 3ms" or "run_command go test ./... ✗ exit 1 1.2s".
//
// Only the tool name, its main target (a path, a command or a process name),
// the outcome and the duration are logged. File contents, edit text,
// environment values, tokens and error messages are never logged.
func activityMiddleware(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			started := time.Now()
			res, err := next(ctx, method, req)

			name, target := "unknown", ""
			if r, ok := req.(*mcp.CallToolRequest); ok && r.Params != nil {
				name = cleanTarget(r.Params.Name)
				target = activityTarget(r.Params.Arguments)
			}
			ok, detail := activityOutcome(res, err)
			line := ActivityLine(name, target, ok, detail, time.Since(started))
			if ok {
				logger.Info(ActivityPrefix + line)
			} else {
				logger.Warn(ActivityPrefix + line)
			}
			return res, err
		}
	}
}

// ActivityLine formats one activity entry: tool, target, ✓/✗, optional detail
// and duration. Empty parts are left out.
func ActivityLine(tool, target string, ok bool, detail string, d time.Duration) string {
	parts := []string{tool}
	if target != "" {
		parts = append(parts, target)
	}
	if ok {
		parts = append(parts, "✓")
	} else {
		parts = append(parts, "✗")
	}
	if detail != "" {
		parts = append(parts, detail)
	}
	parts = append(parts, shortDur(d))
	return strings.Join(parts, " ")
}

// activityOutcome decides success and a short, content-free detail.
func activityOutcome(res mcp.Result, err error) (bool, string) {
	if err != nil {
		return false, "error"
	}
	r, isTool := res.(*mcp.CallToolResult)
	if !isTool || r == nil {
		return true, ""
	}
	if r.IsError {
		return false, "error"
	}
	// run_command reports a failing command as a normal result with a
	// non-zero exit code; surface that as a failure.
	if r.StructuredContent != nil {
		if raw, mErr := json.Marshal(r.StructuredContent); mErr == nil {
			var st struct {
				ExitCode *int `json:"exitCode"`
				TimedOut bool `json:"timedOut"`
			}
			if json.Unmarshal(raw, &st) == nil {
				switch {
				case st.TimedOut:
					return false, "timed out"
				case st.ExitCode != nil && *st.ExitCode != 0 && hasCommand(raw):
					return false, fmt.Sprintf("exit %d", *st.ExitCode)
				}
			}
		}
	}
	return true, ""
}

// hasCommand limits the exit-code rule to run_command results (they carry
// "command"), so process info from stop_process or list_processes, which
// also has argv and an old exitCode, is not reported as a failed call.
func hasCommand(raw []byte) bool {
	var st struct {
		Command string `json:"command"`
	}
	return json.Unmarshal(raw, &st) == nil && st.Command != ""
}

// activityTarget extracts the main target from the call arguments. Only
// keys that name a target are read; content-bearing keys (content, old, new,
// edits, new_content, replacement, env, token) are never touched. For
// source_url only the host is kept: signed URLs carry a token in the query.
func activityTarget(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return ""
	}
	str := func(k string) string {
		s, _ := args[k].(string)
		return s
	}

	var target string
	switch {
	case str("command") != "":
		target = str("command")
		if extra, ok := args["args"].([]any); ok {
			for _, a := range extra {
				if s, ok := a.(string); ok {
					target += " " + s
				}
			}
		}
	case str("source_url") != "":
		target = urlHost(str("source_url")) + " → " + str("path")
	case str("from") != "" || str("to") != "":
		target = str("from") + " → " + str("to")
	case str("path") != "":
		target = str("path")
	case str("name") != "":
		target = str("name")
	case args["paths"] != nil:
		if ps, ok := args["paths"].([]any); ok && len(ps) > 0 {
			first, _ := ps[0].(string)
			target = first
			if len(ps) > 1 {
				target += fmt.Sprintf(" (+%d)", len(ps)-1)
			}
		}
	case str("pattern") != "":
		target = fmt.Sprintf("%q", str("pattern"))
	}
	return cleanTarget(target)
}

// urlHost returns only the host of raw, or "?" when it does not parse.
func urlHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "?"
	}
	return u.Hostname()
}

// cleanTarget keeps a target on one short printable line.
func cleanTarget(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxTargetLen {
		s = string(r[:maxTargetLen-1]) + "…"
	}
	return s
}

// shortDur renders a duration compactly: 850µs, 12ms, 1.2s, 2m3s.
func shortDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

// CountTools reports how many tools s exposes, by listing them over an
// in-memory session. The TUI shows this number while the server runs.
func CountTools(ctx context.Context, s *mcp.Server) (int, error) {
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, serverT, nil)
	if err != nil {
		return 0, err
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "ngodingmcp-count", Version: "1"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		return 0, err
	}
	defer cs.Close()
	n := 0
	params := &mcp.ListToolsParams{}
	for {
		res, err := cs.ListTools(ctx, params)
		if err != nil {
			return 0, err
		}
		n += len(res.Tools)
		if res.NextCursor == "" {
			return n, nil
		}
		params.Cursor = res.NextCursor
	}
}
