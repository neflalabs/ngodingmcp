package mcpserver_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/fetch"
	"ngodingmcp/internal/mcpserver"
	"ngodingmcp/internal/workspace"
)

func newDownloadSession(t *testing.T, srv *httptest.Server) (*mcp.ClientSession, string, *syncBuffer) {
	t.Helper()
	root := t.TempDir()
	ws, err := workspace.New(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	dl := fetch.New(fetch.Options{MaxBytes: 1024, Testing: fetch.TestingOptions{AllowLoopback: true, RootCAs: pool}})
	buf := &syncBuffer{}
	s := mcpserver.NewWithOptions(ws, "test", mcpserver.Options{
		Downloader: dl,
		Logger:     slog.New(slog.NewTextHandler(buf, nil)),
	})
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
	return session, root, buf
}

func TestDownloadFileToolOptIn(t *testing.T) {
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base, err := mcpserver.CountTools(ctx, mcpserver.New(ws, "test"))
	if err != nil {
		t.Fatal(err)
	}
	// 23 base tools + 6 core git write tools (git_init, git_add, git_unstage,
	// git_commit, git_create_branch, git_switch_branch) + 9 git remote & GitHub
	// tools (git_remote, git_fetch, git_pull, git_push, git_merge,
	// git_github_create_repo, git_github_runs, git_github_run_view,
	// git_github_run_rerun).
	if base != 38 {
		t.Fatalf("default tool count = %d, want 38 (download_file must be opt-in)", base)
	}
	withDL, err := mcpserver.CountTools(ctx, mcpserver.NewWithOptions(ws, "test", mcpserver.Options{Downloader: fetch.New(fetch.Options{})}))
	if err != nil {
		t.Fatal(err)
	}
	if withDL != base+1 {
		t.Fatalf("tool count with downloader = %d, want %d", withDL, base+1)
	}
}

func TestDownloadFileToolEndToEnd(t *testing.T) {
	body := []byte("\x89PNG binary \x00\x01 data")
	mux := http.NewServeMux()
	mux.HandleFunc("/img", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	session, root, logs := newDownloadSession(t, srv)

	const token = "SIG-TOKEN-abc123"
	res := callTool(t, session, "download_file", map[string]any{
		"source_url": srv.URL + "/img?X-Goog-Signature=" + token,
		"path":       "static/images/logo.png",
	})
	if res.IsError {
		t.Fatalf("download_file failed: %s", getTextContent(res))
	}
	got, err := os.ReadFile(filepath.Join(root, "static", "images", "logo.png"))
	if err != nil || string(got) != string(body) {
		t.Fatalf("file = %q, %v", got, err)
	}
	text := getTextContent(res)
	if !strings.Contains(text, "static/images/logo.png") || !strings.Contains(text, "image/png") {
		t.Fatalf("result text = %s", text)
	}

	// Second call without overwrite fails; its error must not leak the
	// host path, and nothing may log the signed URL's token.
	res = callTool(t, session, "download_file", map[string]any{
		"source_url": srv.URL + "/img?X-Goog-Signature=" + token,
		"path":       "static/images/logo.png",
	})
	if !res.IsError || !strings.Contains(getTextContent(res), "overwrite") {
		t.Fatalf("expected conflict error, got %s", getTextContent(res))
	}
	if strings.Contains(getTextContent(res), root) {
		t.Fatalf("error leaks host path: %s", getTextContent(res))
	}

	log := logs.String()
	if !strings.Contains(log, "download_file") || !strings.Contains(log, "127.0.0.1") || !strings.Contains(log, "static/images/logo.png") {
		t.Fatalf("activity log missing host or path: %s", log)
	}
	if strings.Contains(log, token) || strings.Contains(log, "X-Goog-Signature") || strings.Contains(log, "/img") {
		t.Fatalf("activity log leaks the signed URL: %s", log)
	}
}
