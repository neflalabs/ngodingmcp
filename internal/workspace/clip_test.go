package workspace

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestClipAroundMatchInvalidUTF8 covers the panic reported in review:
// windows made entirely of UTF-8 continuation bytes underflowed/overshot the
// boundary loops. These inputs are not valid UTF-8, which looksBinary does
// not filter (it only screens NUL bytes), so they reach clipAroundMatch.
func TestClipAroundMatchInvalidUTF8(t *testing.T) {
	// 400 continuation bytes, zero-width match at the start.
	line := strings.Repeat("\x9C", 400)
	re := regexp.MustCompile("^")
	got := clipAroundMatch(re, line, MaxMatchTextBytes)
	if got == "" {
		t.Fatal("expected a non-empty clipped window for a zero-width match")
	}

	// A 400-byte ASCII prefix followed by 2000 continuation bytes and a
	// trailing z: the first match starts deep inside the continuation run.
	line = strings.Repeat("a", 400) + strings.Repeat("\x80", 2000) + "z"
	re = regexp.MustCompile(`.{300}z`)
	got = clipAroundMatch(re, line, MaxMatchTextBytes)
	if got == "" {
		t.Fatal("expected a non-empty clipped window")
	}
}

func TestClipAroundMatchBoundaries(t *testing.T) {
	// Short line passes through unchanged.
	line := "short line with NEEDLE"
	re := regexp.MustCompile("NEEDLE")
	if got := clipAroundMatch(re, line, MaxMatchTextBytes); got != line {
		t.Fatalf("short line changed: %q", got)
	}

	// Match near the end of a long line stays visible and keeps the tail.
	line = strings.Repeat("x", 1000) + "NEEDLE" + strings.Repeat("y", 50)
	got := clipAroundMatch(re, line, MaxMatchTextBytes)
	if !strings.Contains(got, "NEEDLE") {
		t.Fatalf("match lost near end of line: %q", got)
	}
	if !strings.HasSuffix(got, "…") && !strings.HasSuffix(got, "y") {
		t.Fatalf("unexpected end of clipped line: %q", got)
	}

	// 3-byte runes on an odd byte offset must not be split.
	line = strings.Repeat("✓", 500) + "NEEDLE" + strings.Repeat("✓", 500)
	got = clipAroundMatch(re, line, MaxMatchTextBytes)
	if !utf8.ValidString(got) {
		t.Fatalf("clip split a rune: %q", got)
	}
	if !strings.Contains(got, "NEEDLE") {
		t.Fatalf("match lost: %q", got)
	}
}

// TestSearchTextInvalidUTF8Line ensures the full path does not panic on a
// file whose lines are not valid UTF-8.
func TestSearchTextInvalidUTF8Line(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "inv.txt", strings.Repeat("\x9C", 400))

	got, err := ws.SearchText("", "^", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 hit, got %+v", got)
	}
}

// TestSearchReplaceExplicitHeavyDir: pointing at node_modules directly still
// operates on it, even though bulk walks skip it.
func TestSearchReplaceExplicitHeavyDir(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "node_modules/pkg/a.js", "needle\n")

	res, err := ws.SearchReplace("node_modules", "needle", "repl", "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("explicit heavy dir should be replaceable, got %+v", res)
	}
}
