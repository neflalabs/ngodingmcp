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
