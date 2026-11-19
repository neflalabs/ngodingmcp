package mcpserver_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/mcpserver"
	"ngodingmcp/internal/workspace"
)

// newExtraSession starts a plain server (no runner) on a fresh workspace.
func newExtraSession(t *testing.T) (*mcp.ClientSession, *workspace.Workspace) {
	t.Helper()
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	return connectExtra(t, ws), ws
}

func connectExtra(t *testing.T, ws *workspace.Workspace) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := mcpserver.New(ws, "test").Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	return session
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: mustMarshal(t, args)})
	if err != nil {
		t.Fatalf("%s CallTool: %v", name, err)
	}
	return res
}

func TestEditFileTool(t *testing.T) {
	s, ws := newExtraSession(t)
	file := filepath.Join(ws.Root(), "a.txt")
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Dry run returns a diff and leaves the file alone.
	res := callTool(t, s, "edit_file", map[string]any{
		"path":    "a.txt",
		"edits":   []map[string]any{{"old": "two", "new": "TWO"}},
		"dry_run": true,
	})
	if res.IsError {
		t.Fatalf("dry run error: %s", getTextContent(res))
	}
	text := getTextContent(res)
	if !strings.Contains(text, "[DRY RUN]") || !strings.Contains(text, "-two") || !strings.Contains(text, "+TWO") {
		t.Errorf("unexpected dry-run output:\n%s", text)
	}
	if b, _ := os.ReadFile(file); string(b) != "one\ntwo\nthree\n" {
		t.Fatalf("dry run modified the file: %q", b)
	}

	// Two edits applied together.
	res = callTool(t, s, "edit_file", map[string]any{
		"path":  "a.txt",
		"edits": []map[string]any{{"old": "one", "new": "ONE"}, {"old": "three", "new": "THREE"}},
	})
	if res.IsError {
		t.Fatalf("edit error: %s", getTextContent(res))
	}
	if b, _ := os.ReadFile(file); string(b) != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("unexpected content after edit: %q", b)
	}

	// A failing second edit means nothing is written.
	res = callTool(t, s, "edit_file", map[string]any{
		"path":  "a.txt",
		"edits": []map[string]any{{"old": "two", "new": "2"}, {"old": "missing", "new": "x"}},
	})
	if !res.IsError || !strings.Contains(getTextContent(res), "edit 2") {
		t.Fatalf("expected edit 2 failure, got: %s", getTextContent(res))
	}
	if b, _ := os.ReadFile(file); string(b) != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("failed edit still wrote the file: %q", b)
	}
}

func TestErrorsDoNotLeakHostPaths(t *testing.T) {
	s, ws := newExtraSession(t)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"read_file", map[string]any{"path": "missing/file.txt"}},
		{"file_info", map[string]any{"path": "nope.go"}},
		{"list_files", map[string]any{"path": "no-such-dir"}},
		{"delete_path", map[string]any{"path": "ghost.txt"}},
		{"move_path", map[string]any{"from": "ghost.txt", "to": "other.txt"}},
	} {
		res := callTool(t, s, tc.name, tc.args)
		if !res.IsError {
			t.Errorf("%s: expected a tool error", tc.name)
			continue
		}
		if text := getTextContent(res); strings.Contains(text, ws.Root()) {
			t.Errorf("%s leaked the host path: %s", tc.name, text)
		}
	}
}

func TestSensitiveFilesBlockedByDefault(t *testing.T) {
	s, ws := newExtraSession(t)
	for name, body := range map[string]string{".env": "SECRET=1\n", ".env.example": "SECRET=\n", "main.go": "package main\n"} {
		if err := os.WriteFile(filepath.Join(ws.Root(), name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res := callTool(t, s, "read_file", map[string]any{"path": ".env"})
	if !res.IsError || !strings.Contains(getTextContent(res), "sensitive") {
		t.Fatalf("expected .env to be blocked, got: %s", getTextContent(res))
	}
	if res := callTool(t, s, "read_file", map[string]any{"path": ".env.example"}); res.IsError {
		t.Fatalf(".env.example should stay readable: %s", getTextContent(res))
	}
	list := getTextContent(callTool(t, s, "list_files", map[string]any{}))
	if strings.Contains(list, ".env ") || !strings.Contains(list, ".env.example") || !strings.Contains(list, "main.go") {
		t.Errorf("unexpected listing:\n%s", list)
	}

	// With --allow-sensitive the file is readable again.
	ws.SetAllowSensitive(true)
	open := connectExtra(t, ws)
	if res := callTool(t, open, "read_file", map[string]any{"path": ".env"}); res.IsError {
		t.Fatalf("allow-sensitive should permit .env: %s", getTextContent(res))
	}
}

func TestValidateSyntaxJavaScript(t *testing.T) {
	s, ws := newExtraSession(t)
	if err := os.WriteFile(filepath.Join(ws.Root(), "bad.js"), []byte("function f() {\n  return [1, 2;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root(), "ok.ts"), []byte("const a = `x ${'}'} y`;\nconst r = /[}]/g;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := getTextContent(callTool(t, s, "validate_syntax", map[string]any{"path": "bad.js"}))
	if !strings.Contains(bad, "syntax errors") {
		t.Errorf("expected errors for bad.js, got: %s", bad)
	}
	ok := getTextContent(callTool(t, s, "validate_syntax", map[string]any{"path": "ok.ts"}))
	if !strings.Contains(ok, "syntax OK") || !strings.Contains(ok, "delimiters") {
		t.Errorf("expected ok.ts to pass the delimiter check, got: %s", ok)
	}
}

func TestGitToolsWithoutRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	s, _ := newExtraSession(t)
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	for _, name := range []string{"git_status", "git_diff", "git_log"} {
		res := callTool(t, s, name, map[string]any{})
		if res.IsError {
			t.Errorf("%s: no repo should not be a tool error: %s", name, getTextContent(res))
			continue
		}
		if !strings.Contains(getTextContent(res), "not a git repository") {
			t.Errorf("%s: unexpected text: %s", name, getTextContent(res))
		}
	}
}

func TestListFilesDepthAndEntryCap(t *testing.T) {
	s, ws := newExtraSession(t)
	for _, name := range []string{"root.txt", "dir/child.txt", "dir/sub/grand.txt"} {
		full := filepath.Join(ws.Root(), filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	depth1 := getTextContent(callTool(t, s, "list_files", map[string]any{"depth": 1}))
	if !strings.Contains(depth1, "root.txt") || !strings.Contains(depth1, "dir/") {
		t.Errorf("depth 1 misses root entries:\n%s", depth1)
	}
	if strings.Contains(depth1, "child.txt") || strings.Contains(depth1, "grand.txt") {
		t.Errorf("depth 1 walks too far:\n%s", depth1)
	}

	depth2 := getTextContent(callTool(t, s, "list_files", map[string]any{"depth": 2}))
	if !strings.Contains(depth2, "child.txt") || strings.Contains(depth2, "grand.txt") {
		t.Errorf("depth 2 should include children but not grandchildren:\n%s", depth2)
	}

	capped := getTextContent(callTool(t, s, "list_files", map[string]any{"recursive": true, "max_entries": 2}))
	if !strings.Contains(capped, "listing truncated at 2 entries") {
		t.Errorf("max_entries cap not reported:\n%s", capped)
	}
}

func TestSearchTextDefaultLimitAndGrouping(t *testing.T) {
	s, ws := newExtraSession(t)
	if err := os.WriteFile(filepath.Join(ws.Root(), "a.go"), []byte("needle one\nother\nneedle two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	grouped := getTextContent(callTool(t, s, "search_text", map[string]any{"pattern": "needle"}))
	if !strings.Contains(grouped, "a.go (2 matches)") {
		t.Errorf("matches are not grouped by path:\n%s", grouped)
	}
	if strings.Count(grouped, "a.go") != 1 {
		t.Errorf("path repeated on match lines:\n%s", grouped)
	}

	var many strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&many, "needle line %d\n", i+1)
	}
	if err := os.WriteFile(filepath.Join(ws.Root(), "many.txt"), []byte(many.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	limited := getTextContent(callTool(t, s, "search_text", map[string]any{"pattern": "needle", "glob": "*.txt"}))
	if !strings.Contains(limited, "[limit 30 matches reached;") {
		t.Errorf("default max_results not reported:\n%s", limited)
	}
}

func TestGitToolsWithRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	s, ws := newExtraSession(t)
	root := ws.Root()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("add", "main.go")
	git("commit", "-q", "-m", "first commit")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status := getTextContent(callTool(t, s, "git_status", map[string]any{}))
	if !strings.Contains(status, "main.go") || strings.Contains(status, ".env") {
		t.Errorf("unexpected git_status:\n%s", status)
	}
	// No path and no full flag: a --stat summary, not the whole patch.
	diff := getTextContent(callTool(t, s, "git_diff", map[string]any{}))
	if !strings.Contains(diff, "1 file changed") || strings.Contains(diff, "+func main() {}") {
		t.Errorf("unexpected summary git_diff:\n%s", diff)
	}
	if !strings.Contains(diff, "full=true") {
		t.Errorf("summary diff should point at full=true:\n%s", diff)
	}
	fullDiff := getTextContent(callTool(t, s, "git_diff", map[string]any{"full": true}))
	if !strings.Contains(fullDiff, "+func main() {}") {
		t.Errorf("unexpected full git_diff:\n%s", fullDiff)
	}
	// A path selects the patch for that file.
	pathDiff := getTextContent(callTool(t, s, "git_diff", map[string]any{"path": "main.go"}))
	if !strings.Contains(pathDiff, "+func main() {}") {
		t.Errorf("unexpected path git_diff:\n%s", pathDiff)
	}
	log := getTextContent(callTool(t, s, "git_log", map[string]any{"limit": 5}))
	if !strings.Contains(log, "first commit") {
		t.Errorf("unexpected git_log:\n%s", log)
	}
	if res := callTool(t, s, "git_diff", map[string]any{"ref": "--output=/tmp/x"}); !res.IsError {
		t.Error("an option-looking ref must be rejected")
	}
}
