package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrBusy is returned by Switch when tool calls or requests keep the
// workspace in use until the deadline.
var ErrBusy = errors.New("workspace is busy: a tool call or request is still running")

// Settings are the root and per-workspace settings a switch applies.
type Settings struct {
	// Root must be an absolute, symlink-resolved directory (see ResolveRoot).
	Root string
	// MaxRead is the per-file limit; 0 means DefaultMaxRead.
	MaxRead int64
	// AllowSensitive turns the sensitive-file guard off.
	AllowSensitive bool
}

// Current returns the settings the workspace uses right now.
func (w *Workspace) Current() Settings {
	s := w.st.Load()
	return Settings{Root: s.root, MaxRead: s.maxRead, AllowSensitive: s.allowSensitive}
}

// Hold marks the workspace as in use until release is called, so the root
// cannot change in the middle of an operation. Every MCP tool call and every
// Web UI file request holds the workspace; Switch waits for all of them.
func (w *Workspace) Hold() (release func()) {
	w.gate.RLock()
	return w.gate.RUnlock
}

// ResolveRoot checks that root names an existing directory and returns its
// absolute, symlink-resolved path. A leading "~/" means the home directory.
// Unlike New it never creates the directory, and it refuses the filesystem
// root.
func ResolveRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("workspace path must not be empty")
	}
	if root == "~" || strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("cannot resolve ~: " + err.Error())
		}
		root = filepath.Join(home, strings.TrimPrefix(root, "~"))
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("workspace path must be absolute (start with / or ~/)")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("folder does not exist")
		}
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return "", ErrNotDir
	}
	if filepath.Dir(resolved) == resolved {
		return "", errors.New("the filesystem root cannot be a workspace")
	}
	return resolved, nil
}

// Switch moves the workspace to s.Root with s's settings. It waits until no
// Hold is left (or ctx ends, returning ErrBusy), then calls during while
// nothing can use the workspace. When during returns an error the workspace
// is left unchanged. Hold calls made while a switch waits are not blocked, so
// a busy workspace never stalls new requests.
func (w *Workspace) Switch(ctx context.Context, s Settings, during func() error) error {
	if s.Root == "" || !filepath.IsAbs(s.Root) {
		return errors.New("switch: root must be an absolute path")
	}
	for !w.gate.TryLock() {
		select {
		case <-ctx.Done():
			return ErrBusy
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer w.gate.Unlock()
	if during != nil {
		if err := during(); err != nil {
			return err
		}
	}
	w.st.Store(&state{root: s.Root, maxRead: normalizeMaxRead(s.MaxRead), allowSensitive: s.AllowSensitive})
	w.hub.notify("switch", "", "")
	return nil
}
