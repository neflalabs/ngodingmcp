package mcpserver

// Small helpers shared by the tool files: text results, the annotation
// presets every tool picks from, and description/duration utilities.

import (
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// textResult wraps text as the human-readable part of a tool result. The
// structured part comes from the handler's typed output.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// boolPtr returns a pointer to b, for the optional *bool annotation hints.
func boolPtr(b bool) *bool { return &b }

// readOnly marks a tool that never changes the workspace or any process.
func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true}
}

// destructive marks a tool that may overwrite or remove existing data.
func destructive() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{DestructiveHint: boolPtr(true)}
}

// additive marks a tool that changes the workspace but only adds to it.
func additive() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{DestructiveHint: boolPtr(false)}
}

// seconds converts a whole-second tool argument to a duration. Zero or a
// negative value is passed through so the callee applies its own default.
func seconds(n int) time.Duration {
	return time.Duration(n) * time.Second
}

// allowlistDescription lists the allowed commands for run_command and
// start_process descriptions. It returns "" when both lists are empty.
func allowlistDescription(prefix, exact []string) string {
	var desc string
	if len(prefix) > 0 {
		desc += "Allowed prefixes: " + strings.Join(prefix, ", ") + ". "
	}
	if len(exact) > 0 {
		desc += "Allowed exact commands (no extra arguments): " + strings.Join(exact, ", ") + ". "
	}
	return desc
}
