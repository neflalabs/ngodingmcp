package mcpserver_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAgentsResource(t *testing.T) {
	s, ws := newExtraSession(t)
	ctx := context.Background()

	list, err := s.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	found := false
	for _, r := range list.Resources {
		if r.URI == "ngodingmcp://agents" {
			found = true
			if r.MIMEType != "text/markdown" {
				t.Errorf("mime type = %q", r.MIMEType)
			}
		}
	}
	if !found {
		t.Fatalf("ngodingmcp://agents not listed: %+v", list.Resources)
	}

	if _, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: "ngodingmcp://agents"}); err == nil {
		t.Fatal("reading without AGENTS.md should fail")
	}

	writeWorkspaceFile(t, ws, "AGENTS.md", "# Guide\n\nRun make check.\n")
	res, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: "ngodingmcp://agents"})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(res.Contents) != 1 || !strings.Contains(res.Contents[0].Text, "make check") {
		t.Fatalf("unexpected contents: %+v", res.Contents)
	}

	if err := os.Remove(filepath.Join(ws.Root(), "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: "ngodingmcp://agents"}); err == nil {
		t.Fatal("reading a removed AGENTS.md should fail")
	}
}

func TestInstructionsMentionReadWorkflow(t *testing.T) {
	s, _ := newExtraSession(t)
	text := s.InitializeResult().Instructions
	for _, want := range []string{"ngodingmcp://agents", "outline", "read_symbol", "if_changed_since", "replace_text"} {
		if !strings.Contains(text, want) {
			t.Errorf("instructions miss %q:\n%s", want, text)
		}
	}
}
