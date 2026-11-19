package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry describes a file or directory.
type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

const (
	// MaxListEntries is the hard cap for one listing.
	MaxListEntries = 2000
	// DefaultRecursiveListEntries keeps a recursive walk (or a depth-limited
	// one) small unless the caller asks for more.
	DefaultRecursiveListEntries = 300
)

// ListOptions tunes ListFilesWithOptions.
type ListOptions struct {
	Recursive  bool
	Depth      int // levels below rel; 0 means no depth limit
	MaxEntries int // 0 means the mode default, capped at MaxListEntries
}

// EntryLimit returns the effective entry cap for the options. Zero means no
// cap: a flat listing keeps its historical unlimited behavior, while a
// recursive walk defaults to DefaultRecursiveListEntries.
func (o ListOptions) EntryLimit() int {
	limit := o.MaxEntries
	if limit <= 0 {
		if !o.Recursive && o.Depth <= 0 {
			return 0
		}
		limit = DefaultRecursiveListEntries
	}
	if limit > MaxListEntries {
		limit = MaxListEntries
	}
	return limit
}

// ListFiles lists entries in rel. If recursive, it walks the whole subtree.
// Recursive listings are capped at MaxListEntries entries (dependency and
// build-cache directories are skipped; point path directly at one to list it)
// and report truncated when that cap is hit.
func (w *Workspace) ListFiles(rel string, recursive bool) ([]Entry, bool, error) {
	// Preserve the historical behavior for callers that do not opt into the
	// token-oriented defaults used by the MCP tool: recursive listings keep
	// the 2000-entry cap, flat listings stay unlimited.
	opts := ListOptions{Recursive: recursive}
	if recursive {
		opts.MaxEntries = MaxListEntries
	}
	return w.ListFilesWithOptions(rel, opts)
}

// ListFilesWithOptions is ListFiles with a depth limit and a configurable
// entry cap. A positive Depth implies a recursive walk and limits how many
// levels below rel are listed. Dependency and build-cache directories are
// still skipped unless rel points at one directly.
func (w *Workspace) ListFilesWithOptions(rel string, o ListOptions) ([]Entry, bool, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return nil, false, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		return nil, false, ErrNotDir
	}
	limit := o.EntryLimit()
	recursive := o.Recursive || o.Depth > 0
	var entries []Entry
	var truncated bool
	if recursive {
		err = filepath.WalkDir(full, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if skip, err := w.skipBlocked(full, p, d); skip {
				return err
			}
			if skipHeavy(full, p, d) {
				return filepath.SkipDir
			}
			if p == full {
				return nil
			}
			if o.Depth > 0 {
				relp, err := filepath.Rel(full, p)
				if err != nil || listDepth(relp) > o.Depth {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			if limit > 0 && len(entries) >= limit {
				truncated = true
				return filepath.SkipAll
			}
			if !d.IsDir() && !w.walkFileSafe(p, d) {
				return nil // symlink escaping the root (or broken link)
			}
			di, err := d.Info()
			if err != nil {
				return nil // skip unreadable entries instead of aborting the walk
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
			return nil, false, err
		}
	} else {
		des, err := os.ReadDir(full)
		if err != nil {
			return nil, false, err
		}
		for _, d := range des {
			if w.blocked(filepath.Join(full, d.Name())) {
				continue
			}
			if limit > 0 && len(entries) >= limit {
				truncated = true
				break
			}
			di, err := d.Info()
			if err != nil {
				return nil, false, err
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
	return entries, truncated, nil
}

// listDepth is the number of path segments in rel, where the direct children
// of the listing root are depth 1.
func listDepth(rel string) int {
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
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
	relp := w.relPath(full)
	w.hub.notify("mkdir", relp, "")
	return relp, nil
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
	fromRel := w.relPath(src)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	toRel := w.relPath(dst)
	w.hub.notify("rename", toRel, fromRel)
	return toRel, nil
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
	relp := w.relPath(full)
	if len(recursive) > 0 && recursive[0] {
		if err := w.checkNoSensitiveInside(full); err != nil {
			return err
		}
		if err := os.RemoveAll(full); err != nil {
			return err
		}
		w.hub.notify("delete", relp, "")
		return nil
	}
	if err := os.Remove(full); err != nil {
		return err
	}
	w.hub.notify("delete", relp, "")
	return nil
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
