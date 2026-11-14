package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
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
	var matches []Match
	stop := errors.New("stop")
	err = filepath.WalkDir(full, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skip, err := w.skipBlocked(full, p, d); skip {
			return err
		}
		if !d.IsDir() && !w.walkFileSafe(p, d) {
			return nil // symlink escaping the root (or broken link)
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		relp := w.relPath(p)
		target := relp
		if !matchFullPath {
			target = path.Base(relp)
		}
		if !globRe.MatchString(target) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // skip unreadable file instead of aborting the search
		}
		if info.Size() > w.MaxRead() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if looksBinary(data) {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, Match{Path: relp, Line: i + 1, Text: strings.TrimRight(line, "\r")})
				if len(matches) >= maxResults {
					return stop
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return nil, err
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
	var planned []plannedEdit

	err = filepath.WalkDir(full, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skip, err := w.skipBlocked(full, p, d); skip {
			return err
		}
		if !d.IsDir() && !w.walkFileSafe(p, d) {
			return nil // symlink escaping the root (or broken link)
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		relp := w.relPath(p)
		target := relp
		if !matchFullPath {
			target = path.Base(relp)
		}
		if !globRe.MatchString(target) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > w.MaxRead() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if looksBinary(data) {
			return nil
		}
		content := string(data)
		count := len(re.FindAllString(content, -1))
		if count == 0 {
			return nil
		}
		// Check truncation before adding this file.
		if result.FilesChanged >= maxFiles {
			result.Truncated = true
			return filepath.SkipAll
		}
		result.Files = append(result.Files, SearchReplaceFileResult{
			Path:         relp,
			Replacements: count,
			Changed:      !dryRun,
		})
		result.FilesChanged++
		result.TotalReplacements += count
		if !dryRun {
			planned = append(planned, plannedEdit{
				path:     relp,
				original: content,
				updated:  re.ReplaceAllString(content, replacement),
			})
		}
		return nil
	})
	if err != nil {
		return SearchReplaceResult{}, err
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
