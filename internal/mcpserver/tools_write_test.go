package mcpserver_test

import (
	"strings"
	"testing"
)

func TestWriteFileSuggestsTargetedEdit(t *testing.T) {
	s, ws := newExtraSession(t)

	original := strings.Repeat("line content\n", 500)
	updated := strings.Replace(original, "line content\n", "line changed\n", 1)
	writeWorkspaceFile(t, ws, "big.txt", original)

	out := getTextContent(callTool(t, s, "write_file", map[string]any{
		"path":    "big.txt",
		"content": updated,
	}))
	if !strings.Contains(out, "replace_text or edit_file") {
		t.Errorf("small overwrite has no suggestion:\n%s", out)
	}

	// A wholesale rewrite with almost nothing in common must not warn.
	other := strings.Repeat("something else\n", 500)
	out = getTextContent(callTool(t, s, "write_file", map[string]any{
		"path":    "big.txt",
		"content": other,
	}))
	if strings.Contains(out, "note:") {
		t.Errorf("wholesale rewrite warned:\n%s", out)
	}

	// New files never warn.
	out = getTextContent(callTool(t, s, "write_file", map[string]any{
		"path":    "new.txt",
		"content": original,
	}))
	if strings.Contains(out, "note:") {
		t.Errorf("new file warned:\n%s", out)
	}
}
