package mcpserver

// Compact rendering for run_command output. run_command is the only tool that
// can return an arbitrarily large transcript (test logs, build output), and
// every byte stays in the model context for the rest of the session. The
// default result therefore keeps failing test output plus one summary line per
// package, and for other commands the first and last few lines. full=true
// returns the untouched output.

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	// compactHeadLines and compactTailLines bound a non-go-test transcript.
	compactHeadLines = 40
	compactTailLines = 40
	// compactMinLines leaves short output untouched; below this the saving
	// does not justify the extra note.
	compactMinLines = compactHeadLines + compactTailLines
)

// compactRunOutput shortens a command transcript. compacted is false when the
// output was returned unchanged.
func compactRunOutput(argv []string, output string) (text string, compacted bool) {
	trimmed := strings.TrimSuffix(output, "\n")
	if strings.TrimSpace(trimmed) == "" {
		return output, false
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= compactMinLines {
		return output, false
	}
	if isGoTestCommand(argv) {
		if kept, ok := compactGoTest(lines); ok {
			return strings.Join(kept, "\n") + "\n", true
		}
		// go test output is already one summary line per package; cutting
		// it in half would hide which packages ran.
		if goTestSummariesOnly(lines) {
			return output, false
		}
	}
	if len(lines) <= compactHeadLines+compactTailLines {
		return output, false
	}
	omitted := len(lines) - compactHeadLines - compactTailLines
	var b strings.Builder
	for _, line := range lines[:compactHeadLines] {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "... %d lines omitted ...\n", omitted)
	for _, line := range lines[len(lines)-compactTailLines:] {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), true
}

func isGoTestCommand(argv []string) bool {
	return len(argv) >= 2 && filepath.Base(argv[0]) == "go" && argv[1] == "test"
}

// parse modes for compactGoTest.
const (
	goTestBuildErrors = iota + 1 // keep every line after a "# package" header
	goTestFailDetail             // keep indented failure detail lines
	goTestPanic                  // keep the whole panic trace
)

// compactGoTest keeps package summary lines (ok, FAIL, ?) and the blocks of
// failing tests, dropping passing verbose output. ok=false means the output
// had no failure marker, so the caller should use another strategy.
func compactGoTest(lines []string) (kept []string, ok bool) {
	keep := make([]bool, len(lines))
	changed := false
	mode := 0
	runStart := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "=== RUN"), strings.HasPrefix(t, "=== PAUSE"), strings.HasPrefix(t, "=== CONT"):
			runStart = i
			mode = 0
		case strings.HasPrefix(t, "--- FAIL:"):
			changed = true
			if runStart >= 0 && runStart < i {
				for j := runStart; j <= i; j++ {
					keep[j] = true
				}
			} else {
				keep[i] = true
			}
			mode = goTestFailDetail
		case strings.HasPrefix(t, "--- PASS:"), strings.HasPrefix(t, "--- SKIP:"):
			runStart = -1
			mode = 0
		case strings.HasPrefix(t, "ok "), strings.HasPrefix(t, "ok\t"), strings.HasPrefix(t, "? "), t == "PASS":
			keep[i] = true
		case strings.HasPrefix(t, "FAIL"):
			changed = true
			keep[i] = true
			mode = 0
		case strings.HasPrefix(line, "# "):
			changed = true
			keep[i] = true
			mode = goTestBuildErrors
		case strings.HasPrefix(t, "panic:"), strings.HasPrefix(t, "fatal error:"):
			changed = true
			keep[i] = true
			mode = goTestPanic
		default:
			switch mode {
			case goTestBuildErrors, goTestPanic:
				keep[i] = true
			case goTestFailDetail:
				if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
					keep[i] = true
				} else if t != "" {
					mode = 0
				}
			}
		}
	}
	if !changed {
		return nil, false
	}
	kept = make([]string, 0, len(lines))
	for i, line := range lines {
		if keep[i] {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return nil, false
	}
	return kept, true
}

// goTestSummariesOnly reports whether every non-empty line is a package
// summary (ok / FAIL / no test files), i.e. the output is already compact.
func goTestSummariesOnly(lines []string) bool {
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "ok ") && !strings.HasPrefix(t, "ok\t") &&
			!strings.HasPrefix(t, "? ") && !strings.HasPrefix(t, "FAIL") && t != "PASS" {
			return false
		}
	}
	return true
}
