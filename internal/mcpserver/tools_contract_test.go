package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
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

// updateGolden rewrites testdata/tools.golden.json from the current code:
//
//	go test ./internal/mcpserver -run TestToolContract -args -update
var updateGolden = flag.Bool("update", false, "rewrite testdata/tools.golden.json")

const toolsGolden = "testdata/tools.golden.json"

// TestToolContract pins what clients see for every tool: name, description,
// annotations and input/output schemas, with every opt-in tool enabled.
// Refactors must leave it unchanged; an intended change to a tool contract
// is made visible by regenerating the golden file with -update.
func TestToolContract(t *testing.T) {
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
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	got, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		t.Fatalf("marshal tools: %v", err)
	}
	got = append(got, '\n')

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(toolsGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(toolsGolden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d tools)", toolsGolden, len(tools))
		return
	}

	want, err := os.ReadFile(toolsGolden)
	if err != nil {
		t.Fatalf("read %s: %v (run with -args -update to create it)", toolsGolden, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("tool contract changed (%d tools); if intended, rerun with -args -update and review the diff of %s", len(tools), toolsGolden)
	}
}
