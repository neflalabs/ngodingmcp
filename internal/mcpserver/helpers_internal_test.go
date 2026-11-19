package mcpserver

import (
	"strings"
	"testing"

	"ngodingmcp/internal/runner"
)

func TestAllowlistDescriptionExplainsRestart(t *testing.T) {
	plain := allowlistDescription([]string{"go test"}, []string{"bun test"})
	if strings.Contains(plain, "restarts") {
		t.Fatalf("description mentions restart without the entry: %q", plain)
	}
	desc := allowlistDescription([]string{"go build"}, []string{runner.RestartCommand})
	if !strings.Contains(desc, "run_command only") || !strings.Contains(desc, "after replying") {
		t.Fatalf("description does not explain %s: %q", runner.RestartCommand, desc)
	}
}
