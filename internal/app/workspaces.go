package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ngodingmcp/internal/config"
	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/runner"
	"ngodingmcp/internal/webui"
	"ngodingmcp/internal/workspace"
)

// Limits for the workspace selector.
const (
	maxWorkspaceName  = 48
	maxSavedWorkspace = 50
	// switchWait is how long a switch waits for running tool calls and
	// requests; processGrace is how long each process gets to stop.
	switchWait   = 15 * time.Second
	processGrace = 5 * time.Second
)

// workspaceManager implements webui.Workspaces. Saved workspaces live in the
// config file (config.Config.Workspaces); settings left empty inherit the
// global defaults the server was started with.
type workspaceManager struct {
	mu       sync.Mutex // serializes config writes and switches
	ws       *workspace.Workspace
	runner   *runner.Runner   // nil when run_command is off
	procs    *procman.Manager // nil when run_command is off
	defaults webui.WorkspaceDefaults
	logger   *slog.Logger
}

var _ webui.Workspaces = (*workspaceManager)(nil)

func (m *workspaceManager) List() (webui.WorkspaceList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked(config.Load().Workspaces), nil
}

func (m *workspaceManager) listLocked(saved []config.SavedWorkspace) webui.WorkspaceList {
	cur := m.ws.Current()
	out := webui.WorkspaceList{
		ActiveRoot:       cur.Root,
		Effective:        webui.WorkspaceDefaults{AllowSensitive: cur.AllowSensitive, MaxFileBytes: cur.MaxRead},
		Defaults:         m.defaults,
		Workspaces:       []webui.WorkspaceItem{},
		RunningProcesses: m.runningProcesses(),
	}
	for _, s := range saved {
		item := webui.WorkspaceItem{
			Name:           s.Name,
			Path:           s.Path,
			AllowSensitive: s.AllowSensitive,
			MaxFileBytes:   s.MaxFileBytes,
		}
		if info, err := os.Stat(s.Path); err != nil || !info.IsDir() {
			item.Missing = true
		}
		if s.Path == cur.Root && out.ActiveName == "" {
			item.Active = true
			out.ActiveName = s.Name
		}
		out.Workspaces = append(out.Workspaces, item)
	}
	return out
}

func (m *workspaceManager) runningProcesses() []string {
	names := []string{}
	if m.procs == nil {
		return names
	}
	for _, p := range m.procs.List() {
		if p.Running {
			names = append(names, p.Name)
		}
	}
	return names
}

// validate checks and normalizes one entry; the folder must already exist.
func validateWorkspace(in webui.WorkspaceInput) (config.SavedWorkspace, error) {
	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		return config.SavedWorkspace{}, fmt.Errorf("%w: name must not be empty", webui.ErrWorkspaceInvalid)
	case utf8.RuneCountInString(name) > maxWorkspaceName:
		return config.SavedWorkspace{}, fmt.Errorf("%w: name is longer than %d characters", webui.ErrWorkspaceInvalid, maxWorkspaceName)
	case strings.ContainsAny(name, "/\\") || strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return config.SavedWorkspace{}, fmt.Errorf("%w: name must not contain / or \\", webui.ErrWorkspaceInvalid)
	}
	if in.MaxFileBytes < 0 {
		return config.SavedWorkspace{}, fmt.Errorf("%w: max file size must not be negative", webui.ErrWorkspaceInvalid)
	}
	root, err := workspace.ResolveRoot(in.Path)
	if err != nil {
		return config.SavedWorkspace{}, fmt.Errorf("%w: %v", webui.ErrWorkspaceInvalid, err)
	}
	return config.SavedWorkspace{
		Name:           name,
		Path:           root,
		AllowSensitive: in.AllowSensitive,
		MaxFileBytes:   in.MaxFileBytes,
	}, nil
}

func findWorkspace(list []config.SavedWorkspace, name string) int {
	for i, s := range list {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// settingsFor merges an entry with the global defaults.
func (m *workspaceManager) settingsFor(s config.SavedWorkspace) workspace.Settings {
	out := workspace.Settings{
		Root:           s.Path,
		MaxRead:        m.defaults.MaxFileBytes,
		AllowSensitive: m.defaults.AllowSensitive,
	}
	if s.MaxFileBytes > 0 {
		out.MaxRead = s.MaxFileBytes
	}
	if s.AllowSensitive != nil {
		out.AllowSensitive = *s.AllowSensitive
	}
	return out
}

func (m *workspaceManager) Save(original string, in webui.WorkspaceInput) (webui.WorkspaceList, error) {
	entry, err := validateWorkspace(in)
	if err != nil {
		return webui.WorkspaceList{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var saved []config.SavedWorkspace
	var applyActive bool
	uerr := config.Update(func(c *config.Config) {
		list := c.Workspaces
		idx := -1
		if original != "" {
			if idx = findWorkspace(list, original); idx < 0 {
				err = webui.ErrWorkspaceNotFound
				return
			}
		} else if len(list) >= maxSavedWorkspace {
			err = fmt.Errorf("%w: at most %d saved workspaces", webui.ErrWorkspaceInvalid, maxSavedWorkspace)
			return
		}
		if other := findWorkspace(list, entry.Name); other >= 0 && other != idx {
			err = webui.ErrWorkspaceExists
			return
		}
		if idx >= 0 && list[idx].Path == m.ws.Root() {
			// The active workspace keeps its folder; new settings apply now.
			if entry.Path != list[idx].Path {
				err = fmt.Errorf("%w: switch to another workspace before changing the folder of the active one", webui.ErrWorkspaceInvalid)
				return
			}
			applyActive = true
		}
		if idx >= 0 {
			list[idx] = entry
		} else {
			list = append(list, entry)
		}
		c.Workspaces = list
		saved = list
	})
	if err == nil {
		err = uerr
	}
	if err != nil {
		return webui.WorkspaceList{}, err
	}
	if applyActive {
		ctx, cancel := context.WithTimeout(context.Background(), switchWait)
		defer cancel()
		if err := m.ws.Switch(ctx, m.settingsFor(entry), nil); err != nil {
			return webui.WorkspaceList{}, fmt.Errorf("saved, but the new settings are not active yet: %w", err)
		}
	}
	return m.listLocked(saved), nil
}

func (m *workspaceManager) Delete(name string) (webui.WorkspaceList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var saved []config.SavedWorkspace
	var err error
	uerr := config.Update(func(c *config.Config) {
		idx := findWorkspace(c.Workspaces, name)
		if idx < 0 {
			err = webui.ErrWorkspaceNotFound
			return
		}
		c.Workspaces = append(c.Workspaces[:idx:idx], c.Workspaces[idx+1:]...)
		saved = c.Workspaces
	})
	if err == nil {
		err = uerr
	}
	if err != nil {
		return webui.WorkspaceList{}, err
	}
	return m.listLocked(saved), nil
}

func (m *workspaceManager) Switch(ctx context.Context, name string, stopProcesses bool) (webui.SwitchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	saved := config.Load().Workspaces
	idx := findWorkspace(saved, name)
	if idx < 0 {
		return webui.SwitchResult{}, webui.ErrWorkspaceNotFound
	}
	entry := saved[idx]
	root, err := workspace.ResolveRoot(entry.Path)
	if err != nil {
		return webui.SwitchResult{}, fmt.Errorf("%w: %v", webui.ErrWorkspaceInvalid, err)
	}
	entry.Path = root
	settings := m.settingsFor(entry)

	if running := m.runningProcesses(); len(running) > 0 && !stopProcesses {
		return webui.SwitchResult{}, &webui.ProcessesRunningError{Names: running}
	}

	ctx, cancel := context.WithTimeout(ctx, switchWait)
	defer cancel()
	var stopped []string
	err = m.ws.Switch(ctx, settings, func() error {
		// Nothing can use the workspace now: stop and forget the old
		// workspace's processes and point the runner at the new root.
		if m.procs != nil {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*processGrace)
			stopped = m.procs.SwitchRoot(stopCtx, root, processGrace)
			stopCancel()
		}
		if m.runner != nil {
			m.runner.SetRoot(root)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, workspace.ErrBusy) {
			return webui.SwitchResult{}, fmt.Errorf("%w; try again when it is finished", err)
		}
		return webui.SwitchResult{}, err
	}

	// Remember the active workspace for the next start. A failure here does
	// not undo the switch.
	if err := config.Update(func(c *config.Config) { c.Root = root }); err != nil {
		m.logger.Warn("remember active workspace", "err", err)
	}
	m.logger.Info("workspace switched", "name", entry.Name, "stopped_processes", len(stopped))
	if stopped == nil {
		stopped = []string{}
	}
	return webui.SwitchResult{
		Name:             entry.Name,
		Root:             root,
		Effective:        webui.WorkspaceDefaults{AllowSensitive: settings.AllowSensitive, MaxFileBytes: m.ws.MaxRead()},
		StoppedProcesses: stopped,
	}, nil
}
