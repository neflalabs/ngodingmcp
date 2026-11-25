package mcpserver_test

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/mcpserver"
	"ngodingmcp/internal/workspace"
)

// TestServerIdentityOptions pins the template-facing options without changing
// the defaults used by the existing server tests.
func TestServerIdentityOptions(t *testing.T) {
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	srv := mcpserver.NewWithOptions(ws, "v1", mcpserver.Options{
		Name:         "custom-server",
		Title:        "Custom Server",
		Instructions: "custom instructions",
		AgentsURI:    "custom://guide",
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	info := session.InitializeResult().ServerInfo
	if info == nil || info.Name != "custom-server" || info.Title != "Custom Server" {
		t.Fatalf("serverInfo = %+v", info)
	}
	if got := session.InitializeResult().Instructions; got != "custom instructions" {
		t.Fatalf("instructions = %q", got)
	}
	list, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	found := false
	for _, r := range list.Resources {
		if r.URI == "custom://guide" {
			found = true
		}
		if r.URI == "ngodingmcp://agents" {
			t.Fatal("default agents URI still registered")
		}
	}
	if !found {
		t.Fatalf("custom resource URI missing: %+v", list.Resources)
	}
}
