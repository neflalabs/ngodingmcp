package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestWorkspace(t *testing.T) *Workspace {
	t.Helper()
	ws, err := New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ws
}

func mustWrite(t *testing.T, ws *Workspace, rel, content string) {
	t.Helper()
	if _, _, err := ws.WriteFile(rel, content); err != nil {
		t.Fatalf("WriteFile(%q): %v", rel, err)
	}
}

func mustRead(t *testing.T, ws *Workspace, rel string) string {
	t.Helper()
	res, err := ws.ReadFile(rel, 0, 0)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", rel, err)
	}
	return res.Content
}

func TestWriteReadRoundTrip(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "a/b/c.txt", "one\ntwo\n")
	if got := mustRead(t, ws, "a/b/c.txt"); got != "one\ntwo\n" {
		t.Fatalf("content = %q", got)
	}
	// Leading slashes are treated as relative to the root.
	mustWrite(t, ws, "/rooted.txt", "x")
	if _, err := os.Stat(filepath.Join(ws.Root(), "rooted.txt")); err != nil {
		t.Fatalf("rooted file missing: %v", err)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	ws := newTestWorkspace(t)
	for _, p := range []string{"../x", "a/../../x", "..", "/../x"} {
		if _, _, err := ws.WriteFile(p, "x"); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("WriteFile(%q) err = %v, want ErrOutsideRoot", p, err)
		}
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	ws := newTestWorkspace(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws.Root(), "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, _, err := ws.WriteFile("link/secret.txt", "x"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("write via symlink err = %v, want ErrOutsideRoot", err)
	}
	if _, err := ws.ReadFile("link/secret.txt", 0, 0); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("read via symlink err = %v, want ErrOutsideRoot", err)
	}
}

func TestReadFileSlice(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "f.txt", "l1\nl2\nl3\nl4\nl5\n")
	res, err := ws.ReadFile("f.txt", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "l2\nl3\n" {
		t.Fatalf("content = %q", res.Content)
	}
	if res.StartLine != 2 || res.EndLine != 3 || res.TotalLines != 5 {
		t.Fatalf("range = %d-%d of %d", res.StartLine, res.EndLine, res.TotalLines)
	}
}

func TestReplaceText(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "f.txt", "foo bar foo\n")

	if _, _, err := ws.ReplaceText("f.txt", "foo", "baz", false); err == nil {
		t.Fatal("expected ambiguity error")
	}
	_, n, err := ws.ReplaceText("f.txt", "foo", "baz", true)
	if err != nil || n != 2 {
		t.Fatalf("replace all: n=%d err=%v", n, err)
	}
	if got := mustRead(t, ws, "f.txt"); got != "baz bar baz\n" {
		t.Fatalf("content = %q", got)
	}
	if _, _, err := ws.ReplaceText("f.txt", "missing", "x", false); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestInsertText(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "f.txt", "a\nc\n")

	if _, err := ws.InsertText("f.txt", 2, "b"); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, ws, "f.txt"); got != "a\nb\nc\n" {
		t.Fatalf("content = %q", got)
	}
	// Append at the end.
	if _, err := ws.InsertText("f.txt", 4, "d"); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, ws, "f.txt"); got != "a\nb\nc\nd\n" {
		t.Fatalf("content after append = %q", got)
	}
	// Out of range.
	if _, err := ws.InsertText("f.txt", 99, "x"); err == nil {
		t.Fatal("expected range error")
	}
}

func TestInsertTextNoTrailingNewline(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "f.txt", "a")
	if _, err := ws.InsertText("f.txt", 2, "b"); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, ws, "f.txt"); got != "a\nb\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestDeleteLines(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "f.txt", "1\n2\n3\n4\n")
	_, remaining, err := ws.DeleteLines("f.txt", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("remaining = %d", remaining)
	}
	if got := mustRead(t, ws, "f.txt"); got != "1\n4\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestDeleteLastLineWithoutNewline(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "f.txt", "1\n2")
	if _, _, err := ws.DeleteLines("f.txt", 2, 2); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, ws, "f.txt"); got != "1\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestListFiles(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "z.txt", "z")
	mustWrite(t, ws, "dir/a.txt", "a")

	entries, _, err := ws.ListFiles("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries", len(entries))
	}
	if !entries[0].IsDir || entries[0].Name != "dir" {
		t.Fatalf("dirs should sort first: %+v", entries[0])
	}

	all, _, err := ws.ListFiles("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("recursive got %d entries: %+v", len(all), all)
	}
}

func TestSearchText(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "a.go", "package main\nfunc main() {}\n")
	mustWrite(t, ws, "b.txt", "nothing here\n")
	mustWrite(t, ws, "dir/c.go", "func helper() {}\n")

	matches, err := ws.SearchText("", `func \w+`, "a.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Path != "a.go" || matches[0].Line != 2 {
		t.Fatalf("matches = %+v", matches)
	}

	byExt, err := ws.SearchText("", `func \w+`, "*.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(byExt) != 2 {
		t.Fatalf("*.go should match basenames across dirs, got %d: %+v", len(byExt), byExt)
	}

	full, err := ws.SearchText("", `func \w+`, "**/*.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 2 {
		t.Fatalf("**/*.go got %d matches: %+v", len(full), full)
	}

	dirOnly, err := ws.SearchText("", `func \w+`, "dir/*", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirOnly) != 1 || dirOnly[0].Path != "dir/c.go" {
		t.Fatalf("dir/* matches = %+v", dirOnly)
	}

	if _, err := ws.SearchText("", "(", "", 10); err == nil {
		t.Fatal("expected invalid regexp error")
	}
}

func TestBinaryRejected(t *testing.T) {
	ws := newTestWorkspace(t)
	if _, _, err := ws.WriteFile("bin.dat", "abc\x00def"); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ReadFile("bin.dat", 0, 0); !errors.Is(err, ErrBinary) {
		t.Fatalf("err = %v, want ErrBinary", err)
	}
}

func TestTooLargeRejected(t *testing.T) {
	ws, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, ws, "big.txt", "toolong")
	if _, err := ws.ReadFile("big.txt", 0, 0); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestDeleteRootRefused(t *testing.T) {
	ws := newTestWorkspace(t)
	if err := ws.Delete(""); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("err = %v, want refusal", err)
	}
}

func TestRename(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "old.txt", "data")
	if _, err := ws.Rename("old.txt", "new/name.txt"); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, ws, "new/name.txt"); got != "data" {
		t.Fatalf("content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(ws.Root(), "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old file still exists: %v", err)
	}
}

func TestReplaceLines(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "f.txt", "1\n2\n3\n4\n5\n")

	// Replace middle range.
	_, total, err := ws.ReplaceLines("f.txt", 2, 3, "X\nY\n")
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if got := mustRead(t, ws, "f.txt"); got != "1\nX\nY\n4\n5\n" {
		t.Fatalf("content = %q", got)
	}

	// Replace with empty removes lines.
	_, total, err = ws.ReplaceLines("f.txt", 2, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("total after empty replace = %d, want 3", total)
	}
	if got := mustRead(t, ws, "f.txt"); got != "1\n4\n5\n" {
		t.Fatalf("content after empty replace = %q", got)
	}

	// Trailing newline is added automatically.
	_, _, err = ws.ReplaceLines("f.txt", 1, 1, "one")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, ws, "f.txt"); !strings.HasPrefix(got, "one\n") {
		t.Fatalf("newline not added: %q", got)
	}

	// Out-of-range rejected.
	if _, _, err := ws.ReplaceLines("f.txt", 0, 1, "x"); err == nil {
		t.Fatal("expected error for from=0")
	}
	if _, _, err := ws.ReplaceLines("f.txt", 2, 1, "x"); err == nil {
		t.Fatal("expected error for to < from")
	}
	if _, _, err := ws.ReplaceLines("f.txt", 1, 999, "x"); err == nil {
		t.Fatal("expected error for to > total")
	}
}

func TestWorkspaceStats(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "a.go", "package main\nfunc main() {}\n")
	mustWrite(t, ws, "b.go", "package main\n")
	mustWrite(t, ws, "README.md", "# Title\n\nSome text.\n")

	stats, err := ws.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalFiles != 3 {
		t.Fatalf("TotalFiles = %d, want 3", stats.TotalFiles)
	}
	if stats.TotalLines != 6 {
		t.Fatalf("TotalLines = %d, want 6", stats.TotalLines)
	}
	if _, ok := stats.ByExtension[".go"]; !ok {
		t.Fatal("missing .go in ByExtension")
	}
	if stats.ByExtension[".go"].Files != 2 {
		t.Fatalf(".go files = %d, want 2", stats.ByExtension[".go"].Files)
	}
	if len(stats.LargestFiles) == 0 {
		t.Fatal("LargestFiles is empty")
	}
	if len(stats.RecentFiles) == 0 {
		t.Fatal("RecentFiles is empty")
	}
	// LargestFiles should be sorted desc by lines.
	for i := 1; i < len(stats.LargestFiles); i++ {
		if stats.LargestFiles[i].Lines > stats.LargestFiles[i-1].Lines {
			t.Fatalf("LargestFiles not sorted desc at [%d]: %+v", i, stats.LargestFiles)
		}
	}
}

func TestSearchReplace(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "pkg/a.go", "package a\nvar foo = 1\nvar bar = foo + 1\n")
	mustWrite(t, ws, "pkg/b.go", "package a\nvar baz = foo * 2\n")

	// Dry run first
	dryRes, err := ws.SearchReplace(".", `foo`, "qux", "*.go", true, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !dryRes.DryRun {
		t.Fatal("expected DryRun = true")
	}
	if dryRes.FilesChanged != 2 {
		t.Fatalf("dry run FilesChanged = %d, want 2", dryRes.FilesChanged)
	}
	if dryRes.TotalReplacements != 3 {
		t.Fatalf("dry run TotalReplacements = %d, want 3", dryRes.TotalReplacements)
	}
	// Verify content didn't change
	if got := mustRead(t, ws, "pkg/a.go"); !strings.Contains(got, "var foo = 1") {
		t.Fatal("file was modified during dry run")
	}

	// Real run
	res, err := ws.SearchReplace(".", `foo`, "qux", "*.go", false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun {
		t.Fatal("expected DryRun = false")
	}
	if res.TotalReplacements != 3 {
		t.Fatalf("TotalReplacements = %d, want 3", res.TotalReplacements)
	}
	if got := mustRead(t, ws, "pkg/a.go"); !strings.Contains(got, "var qux = 1") || strings.Contains(got, "var foo") {
		t.Fatalf("unexpected content after replace: %q", got)
	}
}

func TestValidateSyntax(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "valid.go", "package main\n\nfunc main() {}\n")
	mustWrite(t, ws, "invalid.go", "package main\n\nfunc main() {\n")
	mustWrite(t, ws, "valid.json", `{"key": "value", "list": [1, 2, 3]}`)
	mustWrite(t, ws, "invalid.json", `{"key": "value",}`)

	// Valid Go
	res, err := ws.ValidateSyntax("valid.go")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.Language != "go" {
		t.Fatalf("expected valid go, got %+v", res)
	}

	// Invalid Go
	res, err = ws.ValidateSyntax("invalid.go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid || len(res.Errors) == 0 {
		t.Fatalf("expected invalid go with errors, got %+v", res)
	}

	// Valid JSON
	res, err = ws.ValidateSyntax("valid.json")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.Language != "json" {
		t.Fatalf("expected valid json, got %+v", res)
	}

	// Invalid JSON
	res, err = ws.ValidateSyntax("invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid || len(res.Errors) == 0 {
		t.Fatalf("expected invalid json with errors, got %+v", res)
	}
}

func TestCountTokens(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "a.txt", "1234567890123456\n")     // 17 bytes
	mustWrite(t, ws, "dir/b.txt", "abcdefghijklmnop\n") // 17 bytes

	counts, err := ws.CountTokens([]string{"a.txt", "dir"})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 2 {
		t.Fatalf("expected 2 counts, got %d", len(counts))
	}
	if counts[0].Path != "a.txt" || counts[0].Bytes != 17 || counts[0].EstimatedTokens != 4 {
		t.Fatalf("unexpected token count for a.txt: %+v", counts[0])
	}
	if counts[1].Bytes != 17 || counts[1].EstimatedTokens != 4 {
		t.Fatalf("unexpected token count for dir: %+v", counts[1])
	}
}

func TestReplaceLines_EdgeCases(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "empty.txt", "")
	// Empty file error
	if _, _, err := ws.ReplaceLines("empty.txt", 1, 1, "hello"); err == nil {
		t.Fatal("expected error on empty file")
	}

	// Non-existent file
	if _, _, err := ws.ReplaceLines("nonexistent.txt", 1, 1, "hello"); err == nil {
		t.Fatal("expected error on nonexistent file")
	}

	// Path traversal
	if _, _, err := ws.ReplaceLines("../outside.txt", 1, 1, "hello"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("expected ErrOutsideRoot, got %v", err)
	}

	// Target is directory
	if _, err := ws.Mkdir("mydir"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.ReplaceLines("mydir", 1, 1, "hello"); !errors.Is(err, ErrIsDir) {
		t.Fatalf("expected ErrIsDir, got %v", err)
	}

	// Negative range
	mustWrite(t, ws, "content.txt", "line1\nline2\n")
	if _, _, err := ws.ReplaceLines("content.txt", -1, 1, "x"); err == nil {
		t.Fatal("expected error for from = -1")
	}

	// Replace entire file
	rel, lines, err := ws.ReplaceLines("content.txt", 1, 2, "single line replacement")
	if err != nil {
		t.Fatal(err)
	}
	if lines != 1 || rel != "content.txt" {
		t.Fatalf("lines = %d, rel = %s", lines, rel)
	}
	if got := mustRead(t, ws, "content.txt"); got != "single line replacement\n" {
		t.Fatalf("unexpected content: %q", got)
	}

	// Single line without newline at end of file
	mustWrite(t, ws, "no_nl.txt", "single line without newline")
	_, _, err = ws.ReplaceLines("no_nl.txt", 1, 1, "new single line")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, ws, "no_nl.txt"); got != "new single line\n" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestWorkspaceStats_EdgeCases(t *testing.T) {
	ws := newTestWorkspace(t)
	// 1. Empty workspace
	stats, err := ws.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalFiles != 0 || stats.TotalLines != 0 || stats.TotalBytes != 0 {
		t.Fatalf("empty stats mismatch: %+v", stats)
	}

	// 2. .git directory ignored
	gitDir := filepath.Join(ws.Root(), ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. Binary file ignored
	if err := os.WriteFile(filepath.Join(ws.Root(), "binary.bin"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}

	// 4. File without extension -> "(no ext)"
	mustWrite(t, ws, "Makefile", "all:\n\techo ok\n")

	// 5. Normal files (> 5 files to test Top 5 truncation)
	for i := 1; i <= 7; i++ {
		mustWrite(t, ws, fmt.Sprintf("file%d.txt", i), strings.Repeat("line\n", i*10))
	}

	stats, err = ws.Stats()
	if err != nil {
		t.Fatal(err)
	}

	// Makefile (1) + file1..file7 (7) = 8 text files (.git and binary ignored)
	if stats.TotalFiles != 8 {
		t.Fatalf("TotalFiles = %d, want 8", stats.TotalFiles)
	}
	if _, ok := stats.ByExtension["(no ext)"]; !ok {
		t.Fatal("expected '(no ext)' in ByExtension")
	}
	if len(stats.LargestFiles) != 5 {
		t.Fatalf("LargestFiles length = %d, want 5 (top 5 cap)", len(stats.LargestFiles))
	}
	if stats.LargestFiles[0].Path != "file7.txt" || stats.LargestFiles[0].Lines != 70 {
		t.Fatalf("top largest file mismatch: %+v", stats.LargestFiles[0])
	}
	if len(stats.RecentFiles) != 5 {
		t.Fatalf("RecentFiles length = %d, want 5 (top 5 cap)", len(stats.RecentFiles))
	}
}

func TestSearchReplace_EdgeCases(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "f1.txt", "hello 123 world\n")
	mustWrite(t, ws, "f2.txt", "hello 456 world\n")
	mustWrite(t, ws, "f3.txt", "hello 789 world\n")

	// 1. Empty pattern rejected
	if _, err := ws.SearchReplace(".", "", "replacement", "*", false, 50); err == nil {
		t.Fatal("expected error on empty pattern")
	}

	// 2. Invalid regex pattern
	if _, err := ws.SearchReplace(".", "[unclosed", "rep", "*", false, 50); err == nil {
		t.Fatal("expected error on invalid regex")
	}

	// 3. Path traversal rejected
	if _, err := ws.SearchReplace("../../outside", "pattern", "rep", "*", false, 50); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("expected ErrOutsideRoot, got %v", err)
	}

	// 4. Capture groups $1, $2
	res, err := ws.SearchReplace(".", `hello (\d+) world`, `greeting-$1`, "*.txt", false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalReplacements != 3 || res.FilesChanged != 3 {
		t.Fatalf("unexpected res: %+v", res)
	}
	if got := mustRead(t, ws, "f1.txt"); got != "greeting-123\n" {
		t.Fatalf("capture group failed: %q", got)
	}

	// 5. maxFiles truncation
	mustWrite(t, ws, "trunc1.log", "target\n")
	mustWrite(t, ws, "trunc2.log", "target\n")
	mustWrite(t, ws, "trunc3.log", "target\n")

	truncRes, err := ws.SearchReplace(".", "target", "replacement", "*.log", false, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !truncRes.Truncated {
		t.Fatal("expected Truncated = true")
	}
	if truncRes.FilesChanged != 2 {
		t.Fatalf("FilesChanged = %d, want 2", truncRes.FilesChanged)
	}
}

func TestValidateSyntax_EdgeCases(t *testing.T) {
	ws := newTestWorkspace(t)

	// 1. Missing file returns error
	if _, err := ws.ValidateSyntax("missing.go"); err == nil {
		t.Fatal("expected error on nonexistent file")
	}
	if _, err := ws.ValidateSyntax("missing.yaml"); err == nil {
		t.Fatal("expected error on nonexistent yaml file")
	}

	// 2. Directory returns ErrIsDir
	if _, err := ws.Mkdir("testdir"); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ValidateSyntax("testdir"); !errors.Is(err, ErrIsDir) {
		t.Fatalf("expected ErrIsDir, got %v", err)
	}

	// 3. Path traversal returns ErrOutsideRoot
	if _, err := ws.ValidateSyntax("../outside.go"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("expected ErrOutsideRoot, got %v", err)
	}

	// 4. YAML / YML returns valid: true, language: yaml
	mustWrite(t, ws, "config.yaml", "key: value\n")
	res, err := ws.ValidateSyntax("config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.Language != "yaml" {
		t.Fatalf("yaml validation mismatch: %+v", res)
	}

	// 5. Unknown extension returns valid: true, language: unknown
	mustWrite(t, ws, "notes.txt", "just some text\n")
	res, err = ws.ValidateSyntax("notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.Language != "unknown" {
		t.Fatalf("unknown validation mismatch: %+v", res)
	}

	// 6. Binary file with .json returns ErrBinary
	if err := os.WriteFile(filepath.Join(ws.Root(), "bad.json"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ValidateSyntax("bad.json"); !errors.Is(err, ErrBinary) {
		t.Fatalf("expected ErrBinary for binary json, got %v", err)
	}
}

func TestCountTokens_EdgeCases(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "root.txt", "12345678\n") // 9 bytes

	// 1. Root directory "." produces "./" path
	counts, err := ws.CountTokens([]string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[0].Path != "./" {
		t.Fatalf("expected './' path, got %+v", counts)
	}
	if counts[0].Bytes != 9 || counts[0].EstimatedTokens != 2 {
		t.Fatalf("bytes/tokens mismatch: %+v", counts[0])
	}

	// 2. Path traversal rejected
	if _, err := ws.CountTokens([]string{"../../outside"}); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("expected ErrOutsideRoot, got %v", err)
	}

	// 3. Missing path returns error
	if _, err := ws.CountTokens([]string{"missing_file.txt"}); err == nil {
		t.Fatal("expected error on nonexistent file")
	}

	// 4. Empty slice returns empty slice, nil error
	counts, err = ws.CountTokens([]string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 0 {
		t.Fatalf("expected 0 counts, got %d", len(counts))
	}

	// 5. Binary file in directory: bytes counted, lines 0
	if err := os.WriteFile(filepath.Join(ws.Root(), "data.bin"), []byte{0, 1, 2, 3, 4, 5, 6, 7}, 0o644); err != nil {
		t.Fatal(err)
	}
	counts, err = ws.CountTokens([]string{"data.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[0].Bytes != 8 || counts[0].Lines != 0 || counts[0].EstimatedTokens != 2 {
		t.Fatalf("binary token count mismatch: %+v", counts[0])
	}
}

func TestWalkersSkipSymlinksEscapingRoot(t *testing.T) {
	ws := newTestWorkspace(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOPSECRET word sword\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(ws.Root(), "link.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	mustWrite(t, ws, "real.txt", "sword\n")

	matches, err := ws.SearchText("", "sword", "*.txt", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		if m.Path == "link.txt" {
			t.Fatalf("SearchText read a symlink pointing outside the root: %+v", m)
		}
	}
	found := false
	for _, m := range matches {
		if m.Path == "real.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("SearchText missed real.txt: %+v", matches)
	}

	st, err := ws.Stats()
	if err != nil {
		t.Fatal(err)
	}
	_ = st // Stats walked the whole tree; it must not have errored on the link
	tc, err := ws.CountTokens([]string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if len(tc) != 1 || tc[0].Lines != 1 {
		t.Fatalf("CountTokens = %+v, want one entry with 1 line (link must be skipped)", tc)
	}

	res, err := ws.SearchReplace(".", "sword", "staff", "*.txt", false, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if f.Path == "link.txt" {
			t.Fatalf("SearchReplace planned to rewrite a symlink escaping the root")
		}
	}
	data, err := os.ReadFile(secret)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "TOPSECRET word sword\n" {
		t.Fatalf("outside-root file was rewritten: %q", data)
	}
}

func TestWriteFilePreservesMode(t *testing.T) {
	ws := newTestWorkspace(t)
	p := filepath.Join(ws.Root(), "keep.txt")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.WriteFile("keep.txt", "y"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
}

func TestSensitiveSymlinkTargetRejected(t *testing.T) {
	ws := newTestWorkspace(t)
	p := filepath.Join(ws.Root(), ".env")
	if err := os.WriteFile(p, []byte("TOPSECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(p, filepath.Join(ws.Root(), "link.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ws.ReadFile("link.txt", 0, 0); !errors.Is(err, ErrSensitive) {
		t.Fatalf("read via neutral symlink err = %v, want ErrSensitive", err)
	}
	if _, _, err := ws.WriteFile("link.txt", "x"); !errors.Is(err, ErrSensitive) {
		t.Fatalf("write via neutral symlink err = %v, want ErrSensitive", err)
	}

	// A directory symlink to a sensitive directory must not leak its tree.
	sshDir := filepath.Join(ws.Root(), ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("PRIVATE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sshDir, filepath.Join(ws.Root(), "notes")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ws.ReadFile("notes/id_rsa", 0, 0); !errors.Is(err, ErrSensitive) {
		t.Fatalf("read via dir symlink err = %v, want ErrSensitive", err)
	}

	// With the guard explicitly off, the same path is allowed.
	ws.SetAllowSensitive(true)
	if _, err := ws.ReadFile("notes/id_rsa", 0, 0); err != nil {
		t.Fatalf("allow-sensitive read err = %v, want nil", err)
	}
}

func TestSensitiveNameCoverage(t *testing.T) {
	ws := newTestWorkspace(t)
	for _, rel := range []string{
		".envrc", "credentials.json", "credentials", "kubeconfig",
		"app.tfvars", "terraform.tfstate", ".pgpass", ".my.cnf",
		".kube/config", ".docker/config.json",
	} {
		full := filepath.Join(ws.Root(), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ws.ReadFile(rel, 0, 0); !errors.Is(err, ErrSensitive) {
			t.Errorf("ReadFile(%q) err = %v, want ErrSensitive", rel, err)
		}
	}
}
