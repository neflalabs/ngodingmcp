package mcpserver

import (
	"fmt"
	"strings"
	"testing"

	"ngodingmcp/internal/workspace"
)

func TestCompactRunOutputKeepsShortOutput(t *testing.T) {
	out := "building...\ndone\n"
	got, compacted := compactRunOutput([]string{"make", "build"}, out)
	if compacted || got != out {
		t.Fatalf("short output changed: compacted=%t got=%q", compacted, got)
	}
}

func TestCompactRunOutputHeadTail(t *testing.T) {
	var lines []string
	for i := 1; i <= 200; i++ {
		lines = append(lines, fmt.Sprintf("line %03d", i))
	}
	got, compacted := compactRunOutput([]string{"make", "build"}, strings.Join(lines, "\n")+"\n")
	if !compacted {
		t.Fatal("expected long output to be compacted")
	}
	if !strings.Contains(got, "line 001") || !strings.Contains(got, "line 200") {
		t.Fatalf("head or tail missing:\n%s", got)
	}
	if strings.Contains(got, "line 100") {
		t.Fatalf("middle line was not dropped:\n%s", got)
	}
	if !strings.Contains(got, "120 lines omitted") {
		t.Fatalf("missing omitted count:\n%s", got)
	}
	if n := strings.Count(strings.TrimSpace(got), "\n") + 1; n != compactHeadLines+compactTailLines+1 {
		t.Fatalf("got %d lines, want %d", n, compactHeadLines+compactTailLines+1)
	}
}

func TestCompactGoTestKeepsFailuresOnly(t *testing.T) {
	var lines []string
	for i := 0; i < 90; i++ {
		lines = append(lines, fmt.Sprintf("    pass_test.go:%d: verbose passing output", i+1))
	}
	// Put the verbose noise inside a passing test block.
	lines = append([]string{
		"=== RUN   TestPass",
	}, lines...)
	lines = append(lines,
		"--- PASS: TestPass (0.01s)",
		"=== RUN   TestFail",
		"    fail_test.go:12: expected 1, got 2",
		"--- FAIL: TestFail (0.00s)",
		"FAIL",
		"FAIL\texample.com/pkg\t0.003s",
		"ok  \texample.com/other\t0.001s",
	)
	got, compacted := compactRunOutput([]string{"go", "test", "./..."}, strings.Join(lines, "\n")+"\n")
	if !compacted {
		t.Fatal("expected go test output to be compacted")
	}
	if !strings.Contains(got, "expected 1, got 2") {
		t.Fatalf("failure detail dropped:\n%s", got)
	}
	if !strings.Contains(got, "--- FAIL: TestFail") {
		t.Fatalf("failure marker dropped:\n%s", got)
	}
	if !strings.Contains(got, "FAIL\texample.com/pkg") || !strings.Contains(got, "ok  \texample.com/other") {
		t.Fatalf("package summary dropped:\n%s", got)
	}
	if strings.Contains(got, "verbose passing output") {
		t.Fatalf("passing verbose output was kept:\n%s", got)
	}
}

func TestCompactGoTestBuildError(t *testing.T) {
	lines := []string{
		"# example.com/broken",
		"./main.go:3:2: undefined: x",
		"FAIL\texample.com/broken [build failed]",
		"FAIL",
		"ok  \texample.com/fine\t0.001s",
	}
	kept, ok := compactGoTest(lines)
	if !ok {
		t.Fatal("build failure output was not recognized")
	}
	got := strings.Join(kept, "\n")
	for _, want := range []string{"# example.com/broken", "undefined: x", "FAIL\texample.com/broken", "ok  \texample.com/fine"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

func TestCompactRunOutputKeepsGoTestSummaryLines(t *testing.T) {
	var lines []string
	for i := 0; i < 120; i++ {
		lines = append(lines, fmt.Sprintf("ok  \texample.com/pkg%03d\t(cached)", i))
	}
	out := strings.Join(lines, "\n") + "\n"
	got, compacted := compactRunOutput([]string{"go", "test", "./..."}, out)
	if compacted || got != out {
		t.Fatalf("summary-only go test output should be untouched: compacted=%t", compacted)
	}
}

func TestFormatSearchMatchesGroupsByPath(t *testing.T) {
	text, capped := formatSearchMatches([]workspace.Match{
		{Path: "a.go", Line: 1, Text: "first"},
		{Path: "a.go", Line: 9, Text: "second"},
		{Path: "b.go", Line: 3, Text: "third"},
	})
	if capped {
		t.Fatal("small result was capped")
	}
	want := "a.go (2 matches)\n  1: first\n  9: second\nb.go (1 match)\n  3: third\n"
	if text != want {
		t.Fatalf("grouped output =\n%q\nwant\n%q", text, want)
	}
}
