package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	mayamcpapi "github.com/neflalabs/mayamcp/api"
)

// maxTargetLen is kept for the internal truncation test; the actual bound is
// enforced by mayamcpapi.SanitizeTarget.
const maxTargetLen = 60

// ActivityPrefix is the shared contract prefix; it comes from mayamcp so the
// TUI recognises both template and consumer activity lines.
const ActivityPrefix = mayamcpapi.ActivityPrefix

// ActivityLine delegates to the core formatter. The line format is a contract.
func ActivityLine(tool, target string, ok bool, detail string, d time.Duration) string {
	return mayamcpapi.ActivityLine(tool, target, ok, detail, d)
}

// ActivityMiddleware is the exported form used by the app-level C-lite wrapper.
func ActivityMiddleware(logger *slog.Logger, namespace string) mcp.Middleware {
	return activityMiddleware(logger, namespace)
}

// activityMiddleware logs one concise line per tools/call. Byte/token
// accounting and the line format come from mayamcp; only target extraction and
// _meta outcome detection remain domain-specific here.
func activityMiddleware(logger *slog.Logger, namespace string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			started := time.Now()
			res, err := next(ctx, method, req)

			name, target := "unknown", ""
			if r, ok := req.(*mcp.CallToolRequest); ok && r.Params != nil {
				name = mayamcpapi.SanitizeTarget(r.Params.Name)
				target = activityTarget(r.Params.Arguments)
			}
			ok, detail := activityOutcomeWith(res, err, namespace)
			line := ActivityLine(name, target, ok, detail, time.Since(started))
			attrs := []any{}
			if size, measured := mayamcpapi.MeasureActivity(res); measured {
				line += size.Suffix(true)
				attrs = append(attrs,
					"content_bytes", size.ContentBytes,
					"content_tokens", size.ContentTokens,
					"structured_bytes", size.StructuredBytes,
					"structured_tokens", size.StructuredTokens,
				)
			}
			level := slog.LevelInfo
			if !ok {
				level = slog.LevelWarn
			}
			logger.Log(ctx, level, ActivityPrefix+line, attrs...)
			return res, err
		}
	}
}

// activityOutcome uses the default _meta namespace; tests and callers that
// configure Options.MetaNamespace should use activityOutcomeWith instead.
func activityOutcome(res mcp.Result, err error) (bool, string) {
	return activityOutcomeWith(res, err, defaultMetaNamespace)
}

// activityOutcomeWith decides success and a short, content-free detail.
func activityOutcomeWith(res mcp.Result, err error, namespace string) (bool, string) {
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
	// non-zero exit code in _meta (see runMetaWith); surface that as a failure.
	if len(r.Meta) > 0 {
		if raw, mErr := json.Marshal(r.Meta); mErr == nil {
			var meta map[string]any
			if json.Unmarshal(raw, &meta) == nil {
				exitKey, timedKey := metaKeys(namespace)
				timed, _ := meta[timedKey].(bool)
				code, hasCode := metaNumber(meta[exitKey])
				switch {
				case timed:
					return false, "timed out"
				case hasCode && code != 0:
					return false, fmt.Sprintf("exit %d", code)
				}
			}
		}
	}
	return true, ""
}

// metaNumber accepts the numeric forms _meta can carry: int from a local
// result, float64 after a JSON round trip, or json.Number from a decoder.
func metaNumber(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

// activityTarget extracts the main target from the call arguments. Only
// keys that name a target are read; content-bearing keys (content, old, new,
// edits, new_content, replacement, env, token, url) are never touched. For
// source_url only the host is kept: signed URLs carry a token in the query.
// A git remote url is never logged; git_remote shows only the remote name.
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
		// Command arguments may carry secrets (tokens, passwords) that must
		// never reach the activity log; keep only argv[0] and the count.
		target = str("command")
		if extra, ok := args["args"].([]any); ok && len(extra) > 0 {
			target += fmt.Sprintf(" (+%d args)", len(extra))
		}
	case str("source_url") != "":
		target = urlHost(str("source_url")) + " → " + str("path")
	case str("from") != "" || str("to") != "":
		target = str("from") + " → " + str("to")
	case str("path") != "":
		target = str("path")
	case str("name") != "":
		target = str("name")
	case str("ref") != "":
		target = str("ref")
	case str("remote") != "":
		target = str("remote")
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
	return mayamcpapi.SanitizeTarget(target)
}

// urlHost returns only the host of raw, or "?" when it does not parse.
func urlHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "?"
	}
	return u.Hostname()
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
