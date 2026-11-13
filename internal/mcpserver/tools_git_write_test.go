package mcpserver_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/mcpserver"
	"ngodingmcp/internal/workspace"
)

func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

func newGitWriteSession(t *testing.T) (*mcp.ClientSession, string) {
	t.Helper()
	isolateGit(t)
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	s := mcpserver.New(ws, "test")
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ws.Root()
}

// The git write tools are a core module: registered by plain New, with no
// flag or option.
func TestGitWriteToolsAreCore(t *testing.T) {
	session, _ := newGitWriteSession(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tool := range res.Tools {
		have[tool.Name] = true
	}
	for _, name := range []string{"git_init", "git_add", "git_unstage", "git_commit", "git_create_branch", "git_switch_branch"} {
		if !have[name] {
			t.Errorf("%s not registered by default", name)
		}
	}
	for _, name := range []string{"git_push", "git_pull", "git_reset", "git_clean", "git_checkout"} {
		if have[name] {
			t.Errorf("%s must not exist", name)
		}
	}
}

func TestGitWriteToolsEndToEnd(t *testing.T) {
	session, root := newGitWriteSession(t)

	// Read-only tools point at git_init before a repository exists.
	res := callTool(t, session, "git_status", map[string]any{})
	if res.IsError || !strings.Contains(getTextContent(res), "git_init") {
		t.Fatalf("git_status before init: %s", getTextContent(res))
	}

	res = callTool(t, session, "git_init", map[string]any{})
	if res.IsError || !strings.Contains(getTextContent(res), "main") {
		t.Fatalf("git_init: %s", getTextContent(res))
	}
	if strings.Contains(getTextContent(res), root) {
		t.Fatalf("git_init leaks host path: %s", getTextContent(res))
	}

	for name, body := range map[string]string{"main.go": "package main\n", ".env": "SECRET=1\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res = callTool(t, session, "git_add", map[string]any{"all": true})
	text := getTextContent(res)
	if res.IsError || !strings.Contains(text, "main.go") || !strings.Contains(text, "skipped 1 sensitive") {
		t.Fatalf("git_add: %s", text)
	}

	res = callTool(t, session, "git_commit", map[string]any{"message": "feat: first"})
	text = getTextContent(res)
	if res.IsError || !strings.Contains(text, "[main ") || !strings.Contains(text, "feat: first") {
		t.Fatalf("git_commit: %s", text)
	}

	res = callTool(t, session, "git_commit", map[string]any{"message": "empty"})
	if !res.IsError || !strings.Contains(getTextContent(res), "nothing staged") {
		t.Fatalf("empty git_commit: %s", getTextContent(res))
	}

	res = callTool(t, session, "git_create_branch", map[string]any{"name": "feature/x", "switch": true})
	if res.IsError {
		t.Fatalf("git_create_branch: %s", getTextContent(res))
	}
	res = callTool(t, session, "git_create_branch", map[string]any{"name": "--force"})
	if !res.IsError {
		t.Fatal("option-like branch name accepted")
	}
	res = callTool(t, session, "git_switch_branch", map[string]any{"name": "main"})
	if res.IsError || !strings.Contains(getTextContent(res), "main") {
		t.Fatalf("git_switch_branch: %s", getTextContent(res))
	}

	res = callTool(t, session, "git_log", map[string]any{})
	if res.IsError || !strings.Contains(getTextContent(res), "feat: first") {
		t.Fatalf("git_log: %s", getTextContent(res))
	}
	res = callTool(t, session, "git_add", map[string]any{"paths": []string{"../outside"}})
	if !res.IsError || strings.Contains(getTextContent(res), root) {
		t.Fatalf("git_add outside workspace: %s", getTextContent(res))
	}
}
