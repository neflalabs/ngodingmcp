// Package workspace provides sandboxed, safe file operations inside a root
// directory. Every path accepted by this package is relative to the workspace
// root and is validated so it cannot escape the root (including via symlinks).
package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	// A path whose resolved target is itself sensitive (e.g. `link.txt ->
	// .env`, or a neutral name for a `.ssh` directory) must be refused the
	// same way the target would be.
	if _, err := os.Lstat(full); err == nil {
		if resolved, err := filepath.EvalSymlinks(full); err == nil && resolved != full {
			if w.blocked(resolved) {
				return "", ErrSensitive
			}
		}
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
// walkFileSafe reports whether a WalkDir entry may be read without escaping
// the workspace. Entries that are not symlinks are always fine; symlinks
// are accepted only when their target resolves inside the root, so a
// workspace cannot smuggle host files (.ssh, config files, …) into reads or
// rewrites. Broken, unresolvable links are refused, as are targets that are
// themselves sensitive (a symlink to `.env` leaks it by any other name).
func (w *Workspace) walkFileSafe(p string, d fs.DirEntry) bool {
	if d.Type()&fs.ModeSymlink == 0 {
		return true
	}
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	if w.blocked(target) {
		return false
	}
	rel, err := filepath.Rel(w.Root(), target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

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
	// Enforce the limit while reading: the file may have grown (or been
	// swapped for a bigger one) since the Stat above.
	f, err := os.Open(full)
	if err != nil {
		return "", "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, w.MaxRead()+1))
	_ = f.Close()
	if err != nil {
		return "", "", err
	}
	if int64(len(data)) > w.MaxRead() {
		return "", "", fmt.Errorf("%w: file exceeds limit %d bytes", ErrTooLarge, w.MaxRead())
	}
	if looksBinary(data) {
		return "", "", ErrBinary
	}
	return full, string(data), nil
}
