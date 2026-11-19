package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/fetch"
	"ngodingmcp/internal/mcpserver"
	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/runner"
	"ngodingmcp/internal/workspace"
)

// estTokens mirrors the bytes/4 heuristic used by count_tokens. It is a
// rough proxy for comparison A/B, not a billing-grade tokenizer.
func estTokens(n int) int { return n / 4 }

// listAllTools builds a server with every opt-in tool enabled (same
// configuration as tools.golden.json) and returns the tools as a client
// would receive them from tools/list.
func listAllTools(t *testing.T) []*mcp.Tool {
	t.Helper()

	root := t.TempDir()
	ws, err := workspace.New(root, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	run, err := runner.New(runner.Options{
		Root:           root,
		Allowlist:      []string{"go test", "go vet"},
		ExactAllowlist: []string{"bun run dev"},
		Timeout:        10 * time.Second,
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	mgr, err := procman.New(procman.Options{Root: root, Runner: run})
	if err != nil {
		t.Fatalf("procman.New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Shutdown(ctx)
	})
	dl := fetch.New(fetch.Options{MaxBytes: 20 << 20})

	s := mcpserver.NewWithOptions(ws, "test", mcpserver.Options{Runner: run, Processes: mgr, Downloader: dl})
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	var tools []*mcp.Tool
	params := &mcp.ListToolsParams{}
	for {
		res, err := session.ListTools(ctx, params)
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		tools = append(tools, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		params.Cursor = res.NextCursor
	}
	return tools
}

// TestToolsListTokens measures the fixed per-turn cost of the tools/list
// payload (compact JSON, as sent on the wire rather than pretty-printed) and
// prints a per-tool breakdown, largest first. It never fails on size; the
// regression guard lives in Fase 1.5 of the token-efficiency plan.
//
// Run it with:
//
//	go test ./internal/mcpserver -run TestToolsListTokens -v
func TestToolsListTokens(t *testing.T) {
	tools := listAllTools(t)
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	type entry struct {
		name   string
		bytes  int
		tokens int
	}
	entries := make([]entry, 0, len(tools))
	for _, tool := range tools {
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("marshal %s: %v", tool.Name, err)
		}
		entries = append(entries, entry{name: tool.Name, bytes: len(raw), tokens: estTokens(len(raw))})
	}

	payload, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		t.Fatalf("marshal tools/list payload: %v", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].bytes == entries[j].bytes {
			return entries[i].name < entries[j].name
		}
		return entries[i].bytes > entries[j].bytes
	})

	sumBytes := 0
	for _, e := range entries {
		sumBytes += e.bytes
	}

	t.Logf("tools/list payload (compact): %d bytes, ~%d tokens, %d tools\n", len(payload), estTokens(len(payload)), len(tools))
	t.Logf("%-24s %8s %8s\n", "tool", "bytes", "~tokens")
	t.Logf("%-24s %8s %8s\n", "----", "-----", "-------")
	for _, e := range entries {
		t.Logf("%-24s %8d %8d\n", e.name, e.bytes, e.tokens)
	}
	t.Logf("%-24s %8d %8d\n", "sum of tool objects", sumBytes, estTokens(sumBytes))
	t.Logf("%-24s %8d %8d\n", "tools/list envelope", len(payload)-sumBytes, estTokens(len(payload)-sumBytes))
	if len(tools) == 0 {
		t.Fatal("no tools listed")
	}
}

// TestToolsListTokensCore measures the default "coding ringan" surface —
// core tools only, without the opt-in runner/process/download tools — for
// comparison with the all-enabled contract configuration. It never fails on
// size; see TestToolsListTokens for the run instructions.
func TestToolsListTokensCore(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.New(root, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	s := mcpserver.New(ws, "test")
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"tools": res.Tools})
	if err != nil {
		t.Fatalf("marshal tools/list payload: %v", err)
	}
	t.Logf("core-only tools/list payload (compact): %d bytes, ~%d tokens, %d tools\n", len(payload), estTokens(len(payload)), len(res.Tools))
}

// TestToolResultSize measures the double payment a client may face when it
// forwards both `content` and `structuredContent` to the model: it prints
// the byte/token cost of each part separately for a representative
// read_file call. Reported via t.Logf; see TestToolsListTokens for the run
// instructions.
func TestToolResultSize(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.New(root, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	// A file big enough to matter but small enough for the 1 MiB cap.
	const path = "sample.txt"
	const line = "the quick brown fox jumps over the lazy dog\n"
	var content string
	for i := 0; i < 300; i++ {
		content += fmt.Sprintf("%4d  %s", i+1, line)
	}
	if _, _, err := ws.WriteFile(path, content); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	srv := mcpserver.New(ws, "test")
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

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_file",
		Arguments: map[string]any{"path": path},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("read_file failed: %+v", res.Content)
	}

	contentRaw, err := json.Marshal(res.Content)
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	structuredRaw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structuredContent: %v", err)
	}

	t.Logf("read_file 300 lines:\n")
	t.Logf("  content           %8d bytes  ~%d tokens (numbered lines, sent to the model)\n", len(contentRaw), estTokens(len(contentRaw)))
	t.Logf("  structuredContent %8d bytes  ~%d tokens (raw slice, same bytes again)\n", len(structuredRaw), estTokens(len(structuredRaw)))
	t.Logf("  total             %8d bytes  ~%d tokens (what a client forwarding both pays)\n", len(contentRaw)+len(structuredRaw), estTokens(len(contentRaw)+len(structuredRaw)))
}
