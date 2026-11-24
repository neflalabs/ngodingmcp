package workspace

import (
	"errors"
	"fmt"
	"strings"
)

// Edit is one exact-text replacement inside a file.
type Edit struct {
	Old        string `json:"old"`
	New        string `json:"new"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// EditResult is returned by EditFile.
type EditResult struct {
	Path         string `json:"path"`
	Edits        int    `json:"edits"`
	Replacements int    `json:"replacements"`
	DryRun       bool   `json:"dryRun"`
	Diff         string `json:"diff"`
	TotalLines   int    `json:"totalLines"`
}

// EditFile applies edits to rel in order, all or nothing. Each edit sees the
// result of the previous one. When any edit fails (old text missing or
// ambiguous), nothing is written and the error names the failing edit. With
// dryRun the file is left untouched and only the diff is returned.
func (w *Workspace) EditFile(rel string, edits []Edit, dryRun bool) (EditResult, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if len(edits) == 0 {
		return EditResult{}, errors.New("edits must not be empty")
	}
	full, content, err := w.readAll(rel)
	if err != nil {
		return EditResult{}, err
	}
	relp := w.relPath(full)

	updated := content
	total := 0
	for i, e := range edits {
		if e.Old == "" {
			return EditResult{}, fmt.Errorf("edit %d: old text must not be empty", i+1)
		}
		count := strings.Count(updated, e.Old)
		switch {
		case count == 0:
			return EditResult{}, fmt.Errorf("edit %d: old text not found (nothing was written)", i+1)
		case count > 1 && !e.ReplaceAll:
			return EditResult{}, fmt.Errorf("edit %d: old text occurs %d times; add context or set replace_all (nothing was written)", i+1, count)
		}
		n := 1
		if e.ReplaceAll {
			n = count
		}
		updated = strings.Replace(updated, e.Old, e.New, n)
		total += n
	}

	res := EditResult{
		Path:         relp,
		Edits:        len(edits),
		Replacements: total,
		DryRun:       dryRun,
		Diff:         UnifiedDiff(relp, content, updated),
		TotalLines:   countLines(updated),
	}
	if !dryRun && updated != content {
		if _, _, err := w.writeFileLocked(relp, updated); err != nil {
			return EditResult{}, err
		}
	}
	return res, nil
}

// maxDiffCells bounds the LCS table used by UnifiedDiff. Larger changed
// regions fall back to a plain "remove all, add all" hunk.
const maxDiffCells = 4_000_000

// diffContext is the number of unchanged lines shown around each change.
const diffContext = 3

type diffOp struct {
	kind byte // ' ', '-', '+'
	text string
}

// UnifiedDiff returns a unified diff (like `diff -u`) between before and
// after, labelled with name. It returns "" when nothing changed.
func UnifiedDiff(name, before, after string) string {
	if before == after {
		return ""
	}
	a := splitLines(before)
	b := splitLines(after)

	// Trim the common prefix and suffix so the LCS only covers the changed
	// middle, which keeps typical edits cheap even in large files.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}

	var ops []diffOp
	for _, l := range a[:pre] {
		ops = append(ops, diffOp{' ', l})
	}
	ops = append(ops, diffMiddle(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffOp{' ', l})
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n", name, name)
	writeHunks(&sb, ops)
	return sb.String()
}

// splitLines splits s into lines, keeping a marker for a missing final
// newline so that adding one shows up in the diff.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	lines[len(lines)-1] += "\n\\ No newline at end of file"
	return lines
}

func diffMiddle(a, b []string) []diffOp {
	var ops []diffOp
	if len(a)*len(b) > maxDiffCells || len(a) == 0 || len(b) == 0 {
		for _, l := range a {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{'+', l})
		}
		return ops
	}
	// lcs[i][j] = LCS length of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// writeHunks groups ops into hunks with diffContext lines of context.
func writeHunks(sb *strings.Builder, ops []diffOp) {
	// Line numbers (1-based) in the old and new file at each op index.
	oldAt := make([]int, len(ops)+1)
	newAt := make([]int, len(ops)+1)
	o, n := 1, 1
	for k, op := range ops {
		oldAt[k], newAt[k] = o, n
		if op.kind != '+' {
			o++
		}
		if op.kind != '-' {
			n++
		}
	}
	oldAt[len(ops)], newAt[len(ops)] = o, n

	k := 0
	for k < len(ops) {
		// Find the next change.
		for k < len(ops) && ops[k].kind == ' ' {
			k++
		}
		if k == len(ops) {
			return
		}
		start := k - diffContext
		if start < 0 {
			start = 0
		}
		// Extend the hunk while changes are within 2*context of each other.
		end := k
		for {
			for end < len(ops) && ops[end].kind != ' ' {
				end++
			}
			next := end
			for next < len(ops) && ops[next].kind == ' ' {
				next++
			}
			if next < len(ops) && next-end <= 2*diffContext {
				end = next
				continue
			}
			break
		}
		stop := end + diffContext
		if stop > len(ops) {
			stop = len(ops)
		}

		oldCount, newCount := 0, 0
		for _, op := range ops[start:stop] {
			if op.kind != '+' {
				oldCount++
			}
			if op.kind != '-' {
				newCount++
			}
		}
		oldStart, newStart := oldAt[start], newAt[start]
		if oldCount == 0 {
			oldStart--
		}
		if newCount == 0 {
			newStart--
		}
		fmt.Fprintf(sb, "@@ -%s +%s @@\n", hunkRange(oldStart, oldCount), hunkRange(newStart, newCount))
		for _, op := range ops[start:stop] {
			sb.WriteByte(op.kind)
			sb.WriteString(op.text)
			sb.WriteByte('\n')
		}
		k = stop
	}
}

func hunkRange(start, count int) string {
	if count == 1 {
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}
