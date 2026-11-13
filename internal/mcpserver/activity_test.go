package mcpserver_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/mcpserver"
	"ngodingmcp/internal/workspace"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newActivitySession(t *testing.T) (*mcp.ClientSession, *workspace.Workspace, *syncBuffer) {
	t.Helper()
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	buf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	srv := mcpserver.NewWithOptions(ws, "test", mcpserver.Options{Logger: logger})
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ws, buf
}

func TestActivityLogLinesWithoutSecrets(t *testing.T) {
	s, ws, buf := newActivitySession(t)
	const secret = "sk-SUPERSECRET-123"

	// Successful write: path logged, content never.
	res := callTool(t, s, "write_file", map[string]any{
		"path":    "notes/main.go",
		"content": "package main // token=" + secret,
	})
	if res.IsError {
		t.Fatalf("write_file failed: %s", getTextContent(res))
	}
	// Successful edit: edit text never logged.
	res = callTool(t, s, "edit_file", map[string]any{
		"path":  "notes/main.go",
		"edits": []map[string]any{{"old": "token=" + secret, "new": "token=" + secret + "X"}},
	})
	if res.IsError {
		t.Fatalf("edit_file failed: %s", getTextContent(res))
	}
	// Failing call: reported with ✗, error message not logged.
	res = callTool(t, s, "read_file", map[string]any{"path": "missing-" + "file.txt"})
	if !res.IsError {
		t.Fatal("read_file on a missing file should fail")
	}

	out := buf.String()
	for _, want := range []string{
		`tool: write_file notes/main.go ✓`,
		`tool: edit_file notes/main.go ✓`,
		`tool: read_file missing-file.txt ✗ error`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q\nlog:\n%s", want, out)
		}
	}
	if strings.Contains(out, secret) || strings.Contains(out, "package main") {
		t.Errorf("log leaked file contents or secrets:\n%s", out)
	}
	if strings.Contains(out, ws.Root()) {
		t.Errorf("log leaked the host root path:\n%s", out)
	}
	if strings.Contains(out, "level=WARN") == false {
		t.Errorf("failed call should be logged at WARN:\n%s", out)
	}
	if got := strings.Count(out, "tool: "); got != 3 {
		t.Errorf("activity lines = %d, want 3\n%s", got, out)
	}
	if b, _ := os.ReadFile(filepath.Join(ws.Root(), "notes", "main.go")); !strings.Contains(string(b), secret+"X") {
		t.Errorf("edit not applied: %q", b)
	}
}

func TestActivityNotLoggedWithoutLogger(t *testing.T) {
	// The plain constructor keeps working and logs nothing.
	s, _ := newExtraSession(t)
	res := callTool(t, s, "list_files", map[string]any{})
	if res.IsError {
		t.Fatalf("list_files: %s", getTextContent(res))
	}
}

func TestActivityLineFormat(t *testing.T) {
	cases := []struct {
		tool, target, detail string
		ok                   bool
		d                    time.Duration
		want                 string
	}{
		{"edit_file", "main.go", "", true, 3 * time.Millisecond, "edit_file main.go ✓ 3ms"},
		{"run_command", "go test", "exit 1", false, 1200 * time.Millisecond, "run_command go test ✗ exit 1 1.2s"},
		{"list_files", "", "", true, 500 * time.Microsecond, "list_files ✓ 500µs"},
	}
	for _, c := range cases {
		if got := mcpserver.ActivityLine(c.tool, c.target, c.ok, c.detail, c.d); got != c.want {
			t.Errorf("ActivityLine = %q, want %q", got, c.want)
		}
	}
}

func TestCountTools(t *testing.T) {
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	srv := mcpserver.New(ws, "test")
	n, err := mcpserver.CountTools(context.Background(), srv)
	if err != nil {
		t.Fatalf("CountTools: %v", err)
	}
	// Must match what a real client sees.
	s := connectExtra(t, ws)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(res.Tools) || n == 0 {
		t.Errorf("CountTools = %d, client sees %d", n, len(res.Tools))
	}
}
