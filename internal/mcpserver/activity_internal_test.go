package mcpserver

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/runner"
)

func TestActivityOutcome(t *testing.T) {
	cases := []struct {
		name   string
		res    mcp.Result
		err    error
		ok     bool
		detail string
	}{
		{"protocol error", nil, errors.New("boom"), false, "error"},
		{"tool error", &mcp.CallToolResult{IsError: true}, nil, false, "error"},
		{"plain ok", &mcp.CallToolResult{}, nil, true, ""},
		{"command exit 1", &mcp.CallToolResult{Meta: runMeta(runner.Result{ExitCode: 1})}, nil, false, "exit 1"},
		{"command exit 0", &mcp.CallToolResult{Meta: runMeta(runner.Result{ExitCode: 0})}, nil, true, ""},
		{"command timeout", &mcp.CallToolResult{Meta: runMeta(runner.Result{ExitCode: -1, TimedOut: true})}, nil, false, "timed out"},
		{"command exit after JSON round trip", &mcp.CallToolResult{Meta: mcp.Meta{metaExitCode: float64(2)}}, nil, false, "exit 2"},
		{"stopped process info with old exit", &mcp.CallToolResult{StructuredContent: map[string]any{"name": "dev", "argv": []string{"bun", "run", "dev"}, "exitCode": 2, "pid": 42}}, nil, true, ""},
		{"unrelated meta", &mcp.CallToolResult{Meta: mcp.Meta{"progressToken": "x"}}, nil, true, ""},
	}
	for _, c := range cases {
		ok, detail := activityOutcome(c.res, c.err)
		if ok != c.ok || detail != c.detail {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", c.name, ok, detail, c.ok, c.detail)
		}
	}
}

func TestActivityTargetSkipsContent(t *testing.T) {
	const secret = "hunter2-SECRET"
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"command": "go test", "args": []string{"./..."}, "env": map[string]string{"TOKEN": secret}}, "go test (+1 args)"},
		{map[string]any{"path": "a.go", "content": secret}, "a.go"},
		{map[string]any{"path": "a.go", "new_content": secret, "from": 1, "to": 2}, "a.go"},
		{map[string]any{"from": "a.go", "to": "b.go"}, "a.go → b.go"},
		{map[string]any{"name": "dev"}, "dev"},
		{map[string]any{"paths": []string{"x", "y", "z"}}, "x (+2)"},
		{map[string]any{"pattern": "foo", "replacement": secret}, `"foo"`},
		{map[string]any{"path": "multi\nline\tpath"}, "multi line path"},
		{map[string]any{"source_url": "https://cdn.example.com/a/b.webp?sig=" + secret, "path": "img/b.webp"}, "cdn.example.com → img/b.webp"},
		{map[string]any{"source_url": "::not a url" + secret, "path": "x"}, "? → x"},
		{map[string]any{"action": "add", "name": "origin", "url": "https://u:" + secret + "@github.com/a/b.git"}, "origin"},
		{map[string]any{"action": "list", "url": "https://" + secret + "@github.com/a/b.git"}, ""},
		{map[string]any{"ref": "origin/main", "message": secret}, "origin/main"},
		{map[string]any{"remote": "origin", "set_upstream": true}, "origin"},
	}
	for _, c := range cases {
		raw, _ := json.Marshal(c.args)
		got := activityTarget(raw)
		if got != c.want {
			t.Errorf("activityTarget(%s) = %q, want %q", raw, got, c.want)
		}
		if strings.Contains(got, secret) {
			t.Errorf("target leaked secret: %q", got)
		}
	}
	long := strings.Repeat("a", 200)
	raw, _ := json.Marshal(map[string]any{"path": long})
	if got := activityTarget(raw); len([]rune(got)) != maxTargetLen {
		t.Errorf("long target not truncated: %d runes", len([]rune(got)))
	}
}
