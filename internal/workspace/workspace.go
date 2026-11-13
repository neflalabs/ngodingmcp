// Package workspace provides sandboxed, safe file operations inside a root
// directory. Every path accepted by this package is relative to the workspace
// root and is validated so it cannot escape the root (including via symlinks).
package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrOutsideRoot is returned when a path would escape the workspace root.
	ErrOutsideRoot = errors.New("path is outside the workspace root")
	// ErrIsDir is returned when a file operation targets a directory.
	ErrIsDir = errors.New("path is a directory")
	// ErrNotDir is returned when a directory operation targets a file.
	ErrNotDir = errors.New("path is not a directory")
	// ErrTooLarge is returned when a file exceeds the maximum readable size.
	ErrTooLarge = errors.New("file is too large to read")
	// ErrBinary is returned when a file looks binary.
	ErrBinary = errors.New("file appears to be binary")
)

// Workspace is a sandboxed root directory. The root and its settings can be
// switched at runtime (see switch.go); callers that need one consistent root
// for a whole operation hold the workspace with Hold.
type Workspace struct {
	st atomic.Pointer[state]
	// gate is held for reading by every tool call or API request (Hold) and
	// for writing while the workspace is switched (Switch).
	gate sync.RWMutex
}

// state is the immutable root and settings of one workspace. A switch
// replaces the whole value.
type state struct {
	root    string
	maxRead int64
	// allowSensitive disables the sensitive-path guard (see sensitive.go).
	allowSensitive bool
}

// New creates (if needed) and returns a Workspace rooted at root.
func New(root string, maxRead int64) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve root symlinks: %w", err)
	}
	w := &Workspace{}
	w.st.Store(&state{root: resolved, maxRead: normalizeMaxRead(maxRead)})
	return w, nil
}

// DefaultMaxRead is the per-file limit used when none is given.
const DefaultMaxRead int64 = 2 << 20

func normalizeMaxRead(n int64) int64 {
	if n <= 0 {
		return DefaultMaxRead
	}
	return n
}

// Root returns the absolute, symlink-resolved workspace root.
func (w *Workspace) Root() string { return w.st.Load().root }

// MaxRead returns the maximum readable file size in bytes.
func (w *Workspace) MaxRead() int64 { return w.st.Load().maxRead }

// relPath converts an absolute path into a slash-separated path relative to
// the workspace root.
func (w *Workspace) relPath(full string) string {
	rel, err := filepath.Rel(w.Root(), full)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

func isOutside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolve validates rel and returns the absolute path inside the root.
func (w *Workspace) resolve(rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		rel = "."
	}
	full := filepath.Join(w.Root(), filepath.FromSlash(path.Clean(rel)))
	if isOutside(w.Root(), full) {
		return "", ErrOutsideRoot
	}
	// Lexical checks are not enough: a symlink inside the workspace may point
	// outside it. Resolve the deepest existing ancestor and re-check.
	if err := w.checkSymlinkEscape(full); err != nil {
		return "", err
	}
	if w.blocked(full) {
		return "", ErrSensitive
	}
	return full, nil
}

// CleanPath validates rel exactly like every file operation does (inside the
// root, no symlink escape, not sensitive) and returns it as a clean,
// slash-separated path relative to the root ("." for the root itself).
func (w *Workspace) CleanPath(rel string) (string, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return "", err
	}
	if r := w.relPath(full); r != "" {
		return r, nil
	}
	return ".", nil
}

// skipBlocked is shared by the directory walkers: it hides sensitive entries
// below base and reports whether the walker should stop at p.
func (w *Workspace) skipBlocked(base, p string, d fs.DirEntry) (bool, error) {
	if p == base || !w.blocked(p) {
		return false, nil
	}
	if d.IsDir() {
		return true, filepath.SkipDir
	}
	return true, nil
}

func (w *Workspace) checkSymlinkEscape(full string) error {
	p := full
	for {
		if _, err := os.Lstat(p); err == nil {
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil {
				return fmt.Errorf("resolve symlinks: %w", err)
			}
			if isOutside(w.Root(), resolved) {
				return ErrOutsideRoot
			}
			return nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// lineStartOffset returns the byte offset where the given 1-based line starts,
// or -1 if the line does not exist.
func lineStartOffset(content string, line int) int {
	if line <= 1 {
		return 0
	}
	cur := 1
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			cur++
			if cur == line {
				return i + 1
			}
		}
	}
	return -1
}

func looksBinary(data []byte) bool {
	probe := data
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	return bytes.IndexByte(probe, 0) >= 0
}

func (w *Workspace) readAll(rel string) (string, string, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		return "", "", ErrIsDir
	}
	if info.Size() > w.MaxRead() {
		return "", "", fmt.Errorf("%w: %d bytes exceeds limit %d", ErrTooLarge, info.Size(), w.MaxRead())
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", "", err
	}
	if looksBinary(data) {
		return "", "", ErrBinary
	}
	return full, string(data), nil
}

// ReadResult is the result of ReadFile.
type ReadResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	TotalLines int    `json:"totalLines"`
	Size       int64  `json:"size"`
}

// ReadFile reads a file, optionally slicing by 1-based line offset/limit.
func (w *Workspace) ReadFile(rel string, offset, limit int) (ReadResult, error) {
	full, content, err := w.readAll(rel)
	if err != nil {
		return ReadResult{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return ReadResult{}, err
	}
	total := countLines(content)
	if offset < 1 {
		offset = 1
	}
	if offset > total {
		return ReadResult{
			Path:       w.relPath(full),
			Content:    "",
			StartLine:  offset,
			EndLine:    offset - 1,
			TotalLines: total,
			Size:       info.Size(),
		}, nil
	}
	endLine := total
	if limit > 0 && offset+limit-1 < total {
		endLine = offset + limit - 1
	}
	start := lineStartOffset(content, offset)
	end := lineStartOffset(content, endLine+1)
	if end == -1 {
		end = len(content)
	}
	return ReadResult{
		Path:       w.relPath(full),
		Content:    content[start:end],
		StartLine:  offset,
		EndLine:    endLine,
		TotalLines: total,
		Size:       info.Size(),
	}, nil
}

// WriteFile writes content to rel atomically, creating parent directories.
func (w *Workspace) WriteFile(rel, content string) (string, int, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return "", 0, err
	}
	if info, err := os.Stat(full); err == nil && info.IsDir() {
		return "", 0, ErrIsDir
	}
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(dir, ".ngodingmcp-*")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmpName, full); err != nil {
		return "", 0, err
	}
	return w.relPath(full), len(content), nil
}

// ReplaceText replaces occurrences of old with new inside rel.
func (w *Workspace) ReplaceText(rel, old, new string, all bool) (string, int, error) {
	if old == "" {
		return "", 0, errors.New("old text must not be empty")
	}
	full, content, err := w.readAll(rel)
	if err != nil {
		return "", 0, err
	}
	count := strings.Count(content, old)
	if count == 0 {
		return "", 0, errors.New("old text not found")
	}
	n := 1
	if all {
		n = count
	} else if count > 1 {
		return "", 0, fmt.Errorf("old text occurs %d times; provide more context or set replace_all=true", count)
	}
	updated := strings.Replace(content, old, new, n)
	if _, _, err := w.WriteFile(w.relPath(full), updated); err != nil {
		return "", 0, err
	}
	return w.relPath(full), n, nil
}

// InsertText inserts text before the given 1-based line. line may be
// totalLines+1 to append at the end.
func (w *Workspace) InsertText(rel string, line int, text string) (string, error) {
	full, content, err := w.readAll(rel)
	if err != nil {
		return "", err
	}
	total := countLines(content)
	if line < 1 || line > total+1 {
		return "", fmt.Errorf("line %d out of range (1-%d)", line, total+1)
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	switch {
	case total == 0:
		content = text
	case line == total+1:
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += text
	default:
		off := lineStartOffset(content, line)
		content = content[:off] + text + content[off:]
	}
	if _, _, err := w.WriteFile(w.relPath(full), content); err != nil {
		return "", err
	}
	return w.relPath(full), nil
}

// DeleteLines deletes the inclusive 1-based line range [from, to].
func (w *Workspace) DeleteLines(rel string, from, to int) (string, int, error) {
	full, content, err := w.readAll(rel)
	if err != nil {
		return "", 0, err
	}
	total := countLines(content)
	if total == 0 {
		return "", 0, errors.New("file is empty")
	}
	if from < 1 || to < from || to > total {
		return "", 0, fmt.Errorf("invalid line range %d-%d (file has %d lines)", from, to, total)
	}
	start := lineStartOffset(content, from)
	end := lineStartOffset(content, to+1)
	if end == -1 {
		end = len(content)
	}
	updated := content[:start] + content[end:]
	if _, _, err := w.WriteFile(w.relPath(full), updated); err != nil {
		return "", 0, err
	}
	return w.relPath(full), countLines(updated), nil
}

// Entry describes a file or directory.
type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

// ListFiles lists entries in rel. If recursive, it walks the whole subtree.
func (w *Workspace) ListFiles(rel string, recursive bool) ([]Entry, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrNotDir
	}
	var entries []Entry
	if recursive {
		err = filepath.WalkDir(full, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if skip, err := w.skipBlocked(full, p, d); skip {
				return err
			}
			if p == full {
				return nil
			}
			di, err := d.Info()
			if err != nil {
				return err
			}
			entries = append(entries, Entry{
				Name:  d.Name(),
				Path:  w.relPath(p),
				IsDir: d.IsDir(),
				Size:  di.Size(),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		des, err := os.ReadDir(full)
		if err != nil {
			return nil, err
		}
		for _, d := range des {
			if w.blocked(filepath.Join(full, d.Name())) {
				continue
			}
			di, err := d.Info()
			if err != nil {
				return nil, err
			}
			entries = append(entries, Entry{
				Name:  d.Name(),
				Path:  w.relPath(filepath.Join(full, d.Name())),
				IsDir: d.IsDir(),
				Size:  di.Size(),
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

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
			return err
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

// Info describes a path.
type Info struct {
	Path    string    `json:"path"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	Lines   int       `json:"lines"`
}

// Stat returns metadata about rel.
func (w *Workspace) Stat(rel string) (Info, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return Info{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return Info{}, err
	}
	out := Info{
		Path:    w.relPath(full),
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	if !info.IsDir() && info.Size() <= w.MaxRead() {
		if data, err := os.ReadFile(full); err == nil && !looksBinary(data) {
			out.Lines = countLines(string(data))
		}
	}
	return out, nil
}

// Mkdir creates a directory (and parents) inside the workspace.
func (w *Workspace) Mkdir(rel string) (string, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(full, 0o755); err != nil {
		return "", err
	}
	return w.relPath(full), nil
}

// Rename moves from to to inside the workspace.
func (w *Workspace) Rename(from, to string) (string, error) {
	src, err := w.resolve(from)
	if err != nil {
		return "", err
	}
	dst, err := w.resolve(to)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	return w.relPath(dst), nil
}

// Delete removes a file or directory. If recursive is true and rel is a directory,
// the entire subtree is removed. The workspace root itself can never be deleted.
func (w *Workspace) Delete(rel string, recursive ...bool) error {
	full, err := w.resolve(rel)
	if err != nil {
		return err
	}
	if full == w.Root() {
		return errors.New("refusing to delete the workspace root")
	}
	if len(recursive) > 0 && recursive[0] {
		if err := w.checkNoSensitiveInside(full); err != nil {
			return err
		}
		return os.RemoveAll(full)
	}
	return os.Remove(full)
}

// checkNoSensitiveInside refuses to touch a directory tree that contains a
// blocked entry, so a recursive delete cannot wipe a .git or .env by accident.
func (w *Workspace) checkNoSensitiveInside(full string) error {
	if w.AllowSensitive() {
		return nil
	}
	info, err := os.Lstat(full)
	if err != nil || !info.IsDir() {
		return nil
	}
	found := errors.New("found")
	err = filepath.WalkDir(full, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p != full && w.blocked(p) {
			return found
		}
		return nil
	})
	if errors.Is(err, found) {
		return fmt.Errorf("%w: directory contains a protected entry", ErrSensitive)
	}
	return err
}

// ReplaceLines replaces the inclusive 1-based line range [from, to] with
// newContent, writing the result atomically via WriteFile.
// Returns the relative path and new total line count.
func (w *Workspace) ReplaceLines(rel string, from, to int, newContent string) (string, int, error) {
	full, content, err := w.readAll(rel)
	if err != nil {
		return "", 0, err
	}
	total := countLines(content)
	if total == 0 {
		return "", 0, errors.New("file is empty")
	}
	if from < 1 || to < from || to > total {
		return "", 0, fmt.Errorf("invalid line range %d-%d (file has %d lines)", from, to, total)
	}
	if newContent != "" && !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	start := lineStartOffset(content, from)
	end := lineStartOffset(content, to+1)
	if end == -1 {
		end = len(content)
	}
	updated := content[:start] + newContent + content[end:]
	if _, _, err := w.WriteFile(w.relPath(full), updated); err != nil {
		return "", 0, err
	}
	return w.relPath(full), countLines(updated), nil
}

// ExtStats holds aggregate statistics for files sharing one extension.
type ExtStats struct {
	Files int   `json:"files"`
	Lines int   `json:"lines"`
	Bytes int64 `json:"bytes"`
}

// RecentFile pairs a relative path with its modification time.
type RecentFile struct {
	Path    string    `json:"path"`
	ModTime time.Time `json:"modTime"`
}

// LargestFile pairs a relative path with its line count.
type LargestFile struct {
	Path  string `json:"path"`
	Lines int    `json:"lines"`
}

// WorkspaceStats holds aggregate information about the whole workspace.
type WorkspaceStats struct {
	TotalFiles   int                 `json:"totalFiles"`
	TotalLines   int                 `json:"totalLines"`
	TotalBytes   int64               `json:"totalBytes"`
	ByExtension  map[string]ExtStats `json:"byExtension"`
	LargestFiles []LargestFile       `json:"largestFiles"`
	RecentFiles  []RecentFile        `json:"recentFiles"`
}

// Stats walks the entire workspace and returns aggregate statistics.
// It skips .git/, binary files, and files exceeding maxRead.
func (w *Workspace) Stats() (WorkspaceStats, error) {
	byExt := make(map[string]ExtStats)
	var allLargest []LargestFile
	var allRecent []RecentFile

	var totalFiles int
	var totalLines int
	var totalBytes int64

	err := filepath.WalkDir(w.Root(), func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skip, err := w.skipBlocked(w.Root(), p, d); skip {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // skip unreadable
		}
		if info.Size() > w.MaxRead() {
			return nil // too large
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if looksBinary(data) {
			return nil
		}

		relp := w.relPath(p)
		ext := filepath.Ext(d.Name())
		if ext == "" {
			ext = "(no ext)"
		}
		lines := countLines(string(data))
		sz := info.Size()

		totalFiles++
		totalLines += lines
		totalBytes += sz

		es := byExt[ext]
		es.Files++
		es.Lines += lines
		es.Bytes += sz
		byExt[ext] = es

		allLargest = append(allLargest, LargestFile{Path: relp, Lines: lines})
		allRecent = append(allRecent, RecentFile{Path: relp, ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return WorkspaceStats{}, err
	}

	// Sort largest files by lines desc, keep top 5.
	sort.Slice(allLargest, func(i, j int) bool {
		return allLargest[i].Lines > allLargest[j].Lines
	})
	if len(allLargest) > 5 {
		allLargest = allLargest[:5]
	}

	// Sort recent files by modTime desc, keep top 5.
	sort.Slice(allRecent, func(i, j int) bool {
		return allRecent[i].ModTime.After(allRecent[j].ModTime)
	})
	if len(allRecent) > 5 {
		allRecent = allRecent[:5]
	}

	return WorkspaceStats{
		TotalFiles:   totalFiles,
		TotalLines:   totalLines,
		TotalBytes:   totalBytes,
		ByExtension:  byExt,
		LargestFiles: allLargest,
		RecentFiles:  allRecent,
	}, nil
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

	err = filepath.WalkDir(full, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skip, err := w.skipBlocked(full, p, d); skip {
			return err
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
		fr := SearchReplaceFileResult{
			Path:         relp,
			Replacements: count,
			Changed:      !dryRun,
		}
		if !dryRun {
			updated := re.ReplaceAllString(content, replacement)
			if _, _, err := w.WriteFile(relp, updated); err != nil {
				return err
			}
		}
		result.Files = append(result.Files, fr)
		result.FilesChanged++
		result.TotalReplacements += count
		return nil
	})
	if err != nil {
		return SearchReplaceResult{}, err
	}
	return result, nil
}

// --- validate_syntax -------------------------------------------------------

// SyntaxError holds a single parse error with its location.
type SyntaxError struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Message string `json:"message"`
}

// SyntaxResult is returned by ValidateSyntax.
type SyntaxResult struct {
	Path     string        `json:"path"`
	Language string        `json:"language"`
	Valid    bool          `json:"valid"`
	Errors   []SyntaxError `json:"errors"`
	// Check says how deep the check went: "parser" (full parse),
	// "delimiters" (brackets, strings and comments only) or "none".
	Check string `json:"check,omitempty"`
}

// ValidateSyntax checks the syntax of the file at rel.
// Full parse: .go (via go/parser), .json (via encoding/json).
// Delimiter check (balanced brackets, closed strings/comments):
// .js/.mjs/.cjs, .ts/.mts/.cts and .css.
// .yaml/.yml returns valid=true with language="yaml" and check="none".
// Everything else returns valid=true with language="unknown" and check="none".
func (w *Workspace) ValidateSyntax(rel string) (SyntaxResult, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return SyntaxResult{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return SyntaxResult{}, err
	}
	if info.IsDir() {
		return SyntaxResult{}, ErrIsDir
	}
	if info.Size() > w.MaxRead() {
		return SyntaxResult{}, fmt.Errorf("%w: %d bytes exceeds limit %d", ErrTooLarge, info.Size(), w.MaxRead())
	}
	relp := w.relPath(full)
	ext := strings.ToLower(filepath.Ext(full))

	switch ext {
	case ".go":
		return validateGo(full, relp)
	case ".json":
		return validateJSON(full, relp)
	case ".js", ".mjs", ".cjs":
		return validateDelimiters(full, relp, "javascript", true)
	case ".ts", ".mts", ".cts":
		return validateDelimiters(full, relp, "typescript", true)
	case ".css":
		return validateDelimiters(full, relp, "css", false)
	case ".yaml", ".yml":
		return SyntaxResult{Path: relp, Language: "yaml", Valid: true, Errors: nil, Check: "none"}, nil
	default:
		return SyntaxResult{Path: relp, Language: "unknown", Valid: true, Errors: nil, Check: "none"}, nil
	}
}

// validateDelimiters runs the lightweight delimiter check for C-like
// languages. JSX/TSX are left out on purpose: text such as <p>it's</p>
// would be reported as an unterminated string.
func validateDelimiters(full, relp, lang string, jsLike bool) (SyntaxResult, error) {
	data, err := os.ReadFile(full)
	if err != nil {
		return SyntaxResult{}, err
	}
	if looksBinary(data) {
		return SyntaxResult{}, ErrBinary
	}
	errs := checkDelimiters(string(data), jsLike)
	return SyntaxResult{Path: relp, Language: lang, Valid: len(errs) == 0, Errors: errs, Check: "delimiters"}, nil
}

func validateGo(full, relp string) (SyntaxResult, error) {
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, full, nil, 0)
	if err == nil {
		return SyntaxResult{Path: relp, Language: "go", Valid: true, Errors: nil, Check: "parser"}, nil
	}
	var errs []SyntaxError
	if el, ok := err.(scanner.ErrorList); ok {
		for _, e := range el {
			errs = append(errs, SyntaxError{
				Line:    e.Pos.Line,
				Col:     e.Pos.Column,
				Message: e.Msg,
			})
		}
	} else {
		errs = append(errs, SyntaxError{Line: 0, Col: 0, Message: err.Error()})
	}
	return SyntaxResult{Path: relp, Language: "go", Valid: false, Errors: errs, Check: "parser"}, nil
}

func validateJSON(full, relp string) (SyntaxResult, error) {
	data, err := os.ReadFile(full)
	if err != nil {
		return SyntaxResult{}, err
	}
	if looksBinary(data) {
		return SyntaxResult{}, ErrBinary
	}
	var v any
	if err := json.Unmarshal(data, &v); err == nil {
		return SyntaxResult{Path: relp, Language: "json", Valid: true, Errors: nil, Check: "parser"}, nil
	} else {
		var syntaxErr *json.SyntaxError
		var se SyntaxError
		if errors.As(err, &syntaxErr) {
			// compute line number from offset
			offset := int(syntaxErr.Offset)
			if offset > len(data) {
				offset = len(data)
			}
			line := 1 + strings.Count(string(data[:offset]), "\n")
			se = SyntaxError{Line: line, Col: 0, Message: syntaxErr.Error()}
		} else {
			se = SyntaxError{Line: 0, Col: 0, Message: err.Error()}
		}
		return SyntaxResult{Path: relp, Language: "json", Valid: false, Errors: []SyntaxError{se}, Check: "parser"}, nil
	}
}

// --- count_tokens ----------------------------------------------------------

// TokenCount holds byte/line/token estimates for a single path.
type TokenCount struct {
	Path            string `json:"path"`
	Bytes           int64  `json:"bytes"`
	Lines           int    `json:"lines"`
	EstimatedTokens int    `json:"estimatedTokens"`
}

// CountTokens returns size/token estimates for each path in rels.
// Directories are walked recursively and aggregated into a single entry.
func (w *Workspace) CountTokens(rels []string) ([]TokenCount, error) {
	var results []TokenCount
	for _, rel := range rels {
		tc, err := w.countOne(rel)
		if err != nil {
			return nil, err
		}
		results = append(results, tc)
	}
	return results, nil
}

func (w *Workspace) countOne(rel string) (TokenCount, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return TokenCount{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return TokenCount{}, err
	}
	relp := w.relPath(full)

	if !info.IsDir() {
		return w.countFile(full, relp)
	}

	// Directory: walk recursively and aggregate.
	var totalBytes int64
	var totalLines int
	err = filepath.WalkDir(full, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skip, err := w.skipBlocked(full, p, d); skip {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		sz := fi.Size()
		totalBytes += sz
		// Count lines for non-binary text files within size limit.
		if sz <= w.MaxRead() {
			if data, err := os.ReadFile(p); err == nil && !looksBinary(data) {
				totalLines += countLines(string(data))
			}
		}
		return nil
	})
	if err != nil {
		return TokenCount{}, err
	}
	// Ensure directory path ends with /
	if relp == "" || relp == "." {
		relp = "./"
	} else if !strings.HasSuffix(relp, "/") {
		relp += "/"
	}
	return TokenCount{
		Path:            relp,
		Bytes:           totalBytes,
		Lines:           totalLines,
		EstimatedTokens: int(totalBytes / 4),
	}, nil
}

func (w *Workspace) countFile(full, relp string) (TokenCount, error) {
	info, err := os.Stat(full)
	if err != nil {
		return TokenCount{}, err
	}
	sz := info.Size()
	var lines int
	if sz <= w.MaxRead() {
		if data, err := os.ReadFile(full); err == nil && !looksBinary(data) {
			lines = countLines(string(data))
		}
	}
	return TokenCount{
		Path:            relp,
		Bytes:           sz,
		Lines:           lines,
		EstimatedTokens: int(sz / 4),
	}, nil
}

// NumberLines formats content with right-aligned line numbers, starting at
// startLine (1-based).
func NumberLines(content string, startLine int) string {
	if content == "" {
		return ""
	}
	trimmed := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmed, "\n")
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%6d\t%s\n", startLine+i, line)
	}
	return b.String()
}
