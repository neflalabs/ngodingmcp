package workspace

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// globToRegexp converts a glob into a regexp. `*` matches within a path
// segment, `?` matches one non-separator character, and `**` matches across
// segments (with `**/` also matching zero directories).
func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+2 < len(glob) && glob[i+1] == '*' && glob[i+2] == '/' {
				b.WriteString("(?:.*/)?")
				i += 2
			} else if i+1 < len(glob) && glob[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// Match is a single search hit.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// MaxMatchTextBytes caps the text of a single search hit. Minified bundles and
// source maps have lines of hundreds of kilobytes; without a cap a handful of
// hits can produce megabytes of output.
const MaxMatchTextBytes = 300

// clipAroundMatch shortens line to roughly max bytes, keeping the first match
// of re visible and marking cut ends with an ellipsis. Short lines are
// returned unchanged. Cuts prefer rune boundaries; degenerate input (long
// runs of UTF-8 continuation bytes, possible in files that are not valid
// UTF-8) falls back to the raw byte window so a match stays visible instead
// of panicking.
func clipAroundMatch(re *regexp.Regexp, line string, max int) string {
	if len(line) <= max {
		return line
	}
	if max < 1 {
		max = 1
	}
	start := 0
	if loc := re.FindStringIndex(line); loc != nil {
		start = loc[0] - max/3
		if start < 0 {
			start = 0
		}
	}
	end := start + max
	if end > len(line) {
		end = len(line)
		start = end - max
	}
	rawStart, rawEnd := start, end
	// Advance start to a rune boundary, without passing end.
	for start > 0 && start < end && !utf8.RuneStart(line[start]) {
		start++
	}
	// Retreat end to a rune boundary, without passing start.
	for end < len(line) && end > start && !utf8.RuneStart(line[end]) {
		end--
	}
	if start >= end {
		// Every byte in the window is a continuation byte: keep the raw
		// window so the match stays visible rather than returning "".
		start, end = rawStart, rawEnd
	}
	out := line[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(line) {
		out += "…"
	}
	return out
}

// SearchText searches for pattern (a Go regular expression) inside rel,
// optionally filtering paths with a glob.
func (w *Workspace) SearchText(rel, pattern, glob string, maxResults int) ([]Match, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}
	full, err := w.resolve(rel)
	if err != nil {
		return nil, err
	}
	if glob == "" {
		glob = "*"
	}
	// A glob without a slash matches basenames, like ripgrep does; a glob with
	// a slash is matched against the full relative path.
	matchFullPath := strings.Contains(glob, "/")
	globRe, err := globToRegexp(glob)
	if err != nil {
		return nil, fmt.Errorf("invalid glob: %w", err)
	}
	if maxResults <= 0 {
		maxResults = 100
	}
	files, err := w.walkFiltered(full, func(relp string) bool {
		target := relp
		if !matchFullPath {
			target = path.Base(relp)
		}
		return globRe.MatchString(target)
	})
	if err != nil {
		return nil, err
	}
	type fileResult struct {
		matches []Match
	}
	results := mapFilesUntil(files, maxResults, func(p string, budget int) (fileResult, int) {
		data, _, ok := w.readForBulk(p)
		if !ok {
			return fileResult{}, 0
		}
		relpath := w.relPath(p)
		var kept []Match
		for i, line := range strings.Split(string(data), "\n") {
			if len(kept) >= budget {
				break
			}
			if re.MatchString(line) {
				text := strings.TrimRight(line, "\r")
				kept = append(kept, Match{Path: relpath, Line: i + 1, Text: clipAroundMatch(re, text, MaxMatchTextBytes)})
			}
		}
		return fileResult{matches: kept}, len(kept)
	})
	var matches []Match
	for _, r := range results {
		if len(matches) >= maxResults {
			break
		}
		for _, m := range r.matches {
			matches = append(matches, m)
			if len(matches) >= maxResults {
				break
			}
		}
	}
	return matches, nil

}

// SearchReplaceFileResult holds the result for one file in a SearchReplace operation.
type SearchReplaceFileResult struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	Changed      bool   `json:"changed"` // false when dryRun=true
}

// SearchReplaceResult holds the aggregate result of a SearchReplace operation.
type SearchReplaceResult struct {
	Pattern           string                    `json:"pattern"`
	Replacement       string                    `json:"replacement"`
	DryRun            bool                      `json:"dryRun"`
	FilesChanged      int                       `json:"filesChanged"`
	TotalReplacements int                       `json:"totalReplacements"`
	Files             []SearchReplaceFileResult `json:"files"`
	Truncated         bool                      `json:"truncated"` // true when maxFiles limit was hit
}

// plannedEdit holds one planned rewrite so the apply phase can verify the
// file still has the contents it was scanned with before writing.
type plannedEdit struct {
	path     string
	original string
	updated  string
}

// SearchReplace performs a workspace-wide regex find-and-replace.
// rel is the directory to search (empty or "." means root).
// glob filters file paths (same semantics as SearchText).
// If dryRun is true, no files are written; results show what would change.
// maxFiles caps the number of files modified/previewed (default 50).
func (w *Workspace) SearchReplace(rel, pattern, replacement, glob string, dryRun bool, maxFiles int) (SearchReplaceResult, error) {
	if pattern == "" {
		return SearchReplaceResult{}, errors.New("pattern must not be empty")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return SearchReplaceResult{}, fmt.Errorf("invalid pattern: %w", err)
	}
	full, err := w.resolve(rel)
	if err != nil {
		return SearchReplaceResult{}, err
	}
	if glob == "" {
		glob = "*"
	}
	matchFullPath := strings.Contains(glob, "/")
	globRe, err := globToRegexp(glob)
	if err != nil {
		return SearchReplaceResult{}, fmt.Errorf("invalid glob: %w", err)
	}
	if maxFiles <= 0 {
		maxFiles = 50
	}

	result := SearchReplaceResult{
		Pattern:     pattern,
		Replacement: replacement,
		DryRun:      dryRun,
	}
	files, err := w.walkFiltered(full, func(relp string) bool {
		target := relp
		if !matchFullPath {
			target = path.Base(relp)
		}
		return globRe.MatchString(target)
	})
	if err != nil {
		return SearchReplaceResult{}, err
	}
	type fileResult struct {
		matched  bool
		relpath  string
		count    int
		original string
		updated  string
	}
	results := mapFilesUntil(files, maxFiles, func(p string, budget int) (fileResult, int) {
		data, _, ok := w.readForBulk(p)
		if !ok {
			return fileResult{}, 0
		}
		content := string(data)
		count := len(re.FindAllString(content, -1))
		if count == 0 {
			return fileResult{}, 0
		}
		fr := fileResult{matched: true, relpath: w.relPath(p), count: count, original: content}
		if !dryRun {
			fr.updated = re.ReplaceAllString(content, replacement)
		}
		return fr, 1
	})
	var planned []plannedEdit
	for _, fr := range results {
		if !fr.matched {
			continue
		}
		if result.FilesChanged >= maxFiles {
			result.Truncated = true
			break
		}
		result.Files = append(result.Files, SearchReplaceFileResult{
			Path:         fr.relpath,
			Replacements: fr.count,
			Changed:      !dryRun,
		})
		result.FilesChanged++
		result.TotalReplacements += fr.count
		if !dryRun {
			planned = append(planned, plannedEdit{
				path:     fr.relpath,
				original: fr.original,
				updated:  fr.updated,
			})
		}
	}

	if len(planned) == 0 {
		return result, nil
	}
	// Refuse to start the apply phase if a planned file changed underneath
	// us while the walk was in flight; never write over a file whose
	// contents were not accounted for.
	var stale []string
	for _, pe := range planned {
		cur, err := os.ReadFile(filepath.Join(w.Root(), filepath.FromSlash(pe.path)))
		if err != nil || string(cur) != pe.original {
			stale = append(stale, pe.path)
		}
	}
	if len(stale) > 0 {
		return SearchReplaceResult{}, fmt.Errorf("search_and_replace: %d file(s) changed while the workspace was scanned, nothing was written: %s", len(stale), strings.Join(stale, ", "))
	}
	applied := 0
	for _, pe := range planned {
		if _, _, err := w.WriteFile(pe.path, pe.updated); err != nil {
			return SearchReplaceResult{}, fmt.Errorf("search_and_replace: wrote %d of %d file(s) before failing on %q: %w", applied, len(planned), pe.path, err)
		}
		applied++
	}
	return result, nil
}

// --- validate_syntax -------------------------------------------------------
