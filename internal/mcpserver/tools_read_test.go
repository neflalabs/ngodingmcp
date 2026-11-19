package mcpserver_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func writeWorkspaceFile(t *testing.T, ws interface{ Root() string }, name, content string) {
	t.Helper()
	full := filepath.Join(ws.Root(), filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOutlineAndReadSymbolTools(t *testing.T) {
	s, ws := newExtraSession(t)
	const src = `package demo

const Answer = 42

func New() int {
	return 42
}
`
	writeWorkspaceFile(t, ws, "demo.go", src)

	outline := getTextContent(callTool(t, s, "outline", map[string]any{"path": "demo.go"}))
	for _, want := range []string{"demo.go (go, 7 lines, hash ", "const", "Answer", "L3-L3", "func", "New", "L5-L7"} {
		if !strings.Contains(outline, want) {
			t.Errorf("outline misses %q:\n%s", want, outline)
		}
	}
	hash := regexp.MustCompile(`hash ([0-9a-f]{16})`).FindStringSubmatch(outline)
	if hash == nil {
		t.Fatalf("outline has no hash:\n%s", outline)
	}

	unchanged := getTextContent(callTool(t, s, "outline", map[string]any{
		"path":             "demo.go",
		"if_changed_since": hash[1],
	}))
	if !strings.HasPrefix(unchanged, "unchanged demo.go") || strings.Contains(unchanged, "func New") {
		t.Errorf("outline did not skip an unchanged file:\n%s", unchanged)
	}

	symbol := getTextContent(callTool(t, s, "read_symbol", map[string]any{"path": "demo.go", "symbol": "New"}))
	if !strings.Contains(symbol, "func New (lines 5-7 of 7") {
		t.Errorf("unexpected read_symbol header:\n%s", symbol)
	}
	if !strings.Contains(symbol, "return 42") || strings.Contains(symbol, "const Answer") {
		t.Errorf("read_symbol did not isolate the declaration:\n%s", symbol)
	}

	unchanged = getTextContent(callTool(t, s, "read_symbol", map[string]any{
		"path":             "demo.go",
		"symbol":           "New",
		"if_changed_since": hash[1],
	}))
	if !strings.HasPrefix(unchanged, "unchanged demo.go") || strings.Contains(unchanged, "return 42") {
		t.Errorf("read_symbol did not skip an unchanged file:\n%s", unchanged)
	}

	missing := callTool(t, s, "read_symbol", map[string]any{"path": "demo.go", "symbol": "Missing"})
	if !missing.IsError {
		t.Errorf("missing symbol should fail, got: %s", getTextContent(missing))
	}
}

func TestReadFilesTool(t *testing.T) {
	s, ws := newExtraSession(t)
	writeWorkspaceFile(t, ws, "a.txt", "line one\nline two\nline three\n")
	writeWorkspaceFile(t, ws, "b.txt", "beta one\nbeta two\n")

	out := getTextContent(callTool(t, s, "read_files", map[string]any{
		"files": []map[string]any{
			{"path": "a.txt", "offset": 2, "limit": 1},
			{"path": "b.txt"},
		},
	}))
	for _, want := range []string{"== a.txt (lines 2-2 of 3", "line two", "== b.txt (lines 1-2 of 2", "beta two"} {
		if !strings.Contains(out, want) {
			t.Errorf("read_files misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "line one") || strings.Contains(out, "line three") {
		t.Errorf("read_files ignored offset/limit:\n%s", out)
	}

	hash := regexp.MustCompile(`== a\.txt \(lines [^\n]*hash ([0-9a-f]{16})\)`).FindStringSubmatch(out)
	if hash == nil {
		t.Fatalf("read_files has no hash for a.txt:\n%s", out)
	}
	unchanged := getTextContent(callTool(t, s, "read_files", map[string]any{
		"files": []map[string]any{
			{"path": "a.txt", "offset": 2, "limit": 1, "if_changed_since": hash[1]},
			{"path": "b.txt"},
		},
	}))
	if !strings.Contains(unchanged, "== a.txt (unchanged, hash "+hash[1]+")") {
		t.Errorf("read_files did not skip the unchanged file:\n%s", unchanged)
	}
	if strings.Contains(unchanged, "line two") {
		t.Errorf("unchanged file returned content:\n%s", unchanged)
	}
	if !strings.Contains(unchanged, "beta two") {
		t.Errorf("read_files dropped the other file:\n%s", unchanged)
	}

	small := getTextContent(callTool(t, s, "read_files", map[string]any{
		"files":     []map[string]any{{"path": "a.txt"}, {"path": "b.txt"}},
		"max_bytes": 10,
	}))
	if !strings.Contains(small, "batch budget 10 bytes reached; file truncated") {
		t.Errorf("missing truncation notice:\n%s", small)
	}
	if !strings.Contains(small, "not shown: b.txt") {
		t.Errorf("missing skipped-file notice:\n%s", small)
	}
	if strings.Contains(small, "beta one") {
		t.Errorf("budget did not stop the batch:\n%s", small)
	}

	if res := callTool(t, s, "read_files", map[string]any{"files": []map[string]any{}}); !res.IsError {
		t.Errorf("empty files should fail, got: %s", getTextContent(res))
	}
}

func TestReadFileIfChangedSince(t *testing.T) {
	s, ws := newExtraSession(t)
	writeWorkspaceFile(t, ws, "a.txt", "one\ntwo\n")

	first := getTextContent(callTool(t, s, "read_file", map[string]any{"path": "a.txt"}))
	m := regexp.MustCompile(`hash ([0-9a-f]{16})`).FindStringSubmatch(first)
	if m == nil {
		t.Fatalf("read_file output has no hash:\n%s", first)
	}
	if !strings.Contains(first, "one") {
		t.Fatalf("first read is missing content:\n%s", first)
	}

	same := getTextContent(callTool(t, s, "read_file", map[string]any{
		"path":             "a.txt",
		"if_changed_since": "sha256:" + strings.ToUpper(m[1]),
	}))
	if !strings.HasPrefix(same, "unchanged a.txt") {
		t.Fatalf("expected unchanged, got:\n%s", same)
	}
	if strings.Contains(same, "one") {
		t.Fatalf("unchanged result returned content:\n%s", same)
	}

	writeWorkspaceFile(t, ws, "a.txt", "one\nTWO\n")
	changed := getTextContent(callTool(t, s, "read_file", map[string]any{
		"path":             "a.txt",
		"if_changed_since": m[1],
	}))
	if strings.HasPrefix(changed, "unchanged") || !strings.Contains(changed, "TWO") {
		t.Fatalf("changed file was skipped:\n%s", changed)
	}
}
