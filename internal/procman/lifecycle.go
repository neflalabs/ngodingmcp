package procman

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Stop terminates a process, escalating to SIGKILL after grace. When wait is
// false it sends the signal and returns immediately; escalation continues in
// the background.
func (m *Manager) Stop(name string, grace time.Duration, wait bool) (Info, error) {
	p := m.get(name)
	if p == nil {
		return Info{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if grace <= 0 {
		grace = stopGrace
	}

	p.mu.Lock()
	if !p.running {
		info := p.infoLocked()
		p.mu.Unlock()
		return info, nil
	}
	alreadyStopping := p.stopping
	p.stopping = true
	p.mu.Unlock()

	if !alreadyStopping {
		_ = p.terminate()
	}
	if !wait {
		if !alreadyStopping {
			go p.escalate(grace)
		}
		return p.info(), nil
	}

	select {
	case <-p.done:
	case <-time.After(grace):
		_ = p.kill()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
	}
	return p.info(), nil
}

// StopAll stops every running process without closing the manager.
func (m *Manager) StopAll(ctx context.Context, grace time.Duration) []Info {
	if grace <= 0 {
		grace = stopGrace
	}
	names := make([]string, 0)
	for _, info := range m.List() {
		if info.Running {
			names = append(names, info.Name)
		}
	}
	m.stopNames(ctx, names, grace)
	return m.List()
}

// Restart stops a process and starts it again with the same argv, directory and
// environment.
func (m *Manager) Restart(name string, grace time.Duration) (Info, error) {
	p := m.get(name)
	if p == nil {
		return Info{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	argv := append([]string(nil), p.argv...)
	dir := p.dir
	env := copyEnv(p.env)

	if _, err := m.Stop(name, grace, true); err != nil {
		return Info{}, err
	}
	m.remove(name)
	rel := ""
	if root, err := filepath.EvalSymlinks(m.Root()); err == nil {
		if r, err := filepath.Rel(root, dir); err == nil && r != "." {
			rel = r
		}
	}
	return m.StartWithOptions(StartOptions{Name: name, Argv: argv, Dir: rel, Env: env})
}

// Shutdown stops every running process and marks the manager closed.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	names := append([]string(nil), m.order...)
	m.mu.Unlock()

	running := make([]string, 0, len(names))
	for _, name := range names {
		if p := m.get(name); p != nil && p.isRunning() {
			running = append(running, name)
		}
	}

	done := make(chan struct{})
	go func() {
		m.stopNames(ctx, running, 3*time.Second)
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		m.forceKillAll()
		return ctx.Err()
	}
}

func (m *Manager) stopNames(ctx context.Context, names []string, grace time.Duration) {
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, _ = m.Stop(name, grace, true)
		}(name)
	}

	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case <-ctx.Done():
	}
}

func (m *Manager) forceKillAll() {
	for _, info := range m.List() {
		if info.Running {
			if p := m.get(info.Name); p != nil {
				_ = p.kill()
			}
		}
	}
}

func (m *Manager) get(name string) *Process {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.procs[name]
}

func (m *Manager) remove(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.procs[name]; p != nil && p.isRunning() {
		return
	}
	delete(m.procs, name)
	for i, n := range m.order {
		if n == name {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
}

// nextNameLocked returns the first free proc-N name. Callers hold m.mu.
func (m *Manager) nextNameLocked() string {
	for {
		m.seq++
		name := fmt.Sprintf("proc-%d", m.seq)
		if _, exists := m.procs[name]; !exists {
			return name
		}
	}
}

func (m *Manager) runningLocked() int {
	n := 0
	for _, p := range m.procs {
		if p.isRunning() {
			n++
		}
	}
	return n
}

// evictLocked removes the oldest exited process. Callers hold m.mu.
func (m *Manager) evictLocked() bool {
	var oldest *Process
	for _, p := range m.procs {
		if p.isRunning() {
			continue
		}
		ended := p.endedTime()
		if oldest == nil || ended.Before(oldest.endedTime()) {
			oldest = p
		}
	}
	if oldest == nil {
		return false
	}
	delete(m.procs, oldest.name)
	for i, name := range m.order {
		if name == oldest.name {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return true
}

// resolveDir validates that rel stays inside the workspace root.
func (m *Manager) resolveDir(rel string) (string, error) {
	root, err := filepath.EvalSymlinks(m.Root())
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidDir, err)
	}
	if strings.TrimSpace(rel) == "" {
		return root, nil
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %s must be relative", ErrInvalidDir, rel)
	}
	target := filepath.Join(root, filepath.FromSlash(filepath.Clean(rel)))
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidDir, err)
	}
	check, err := filepath.Rel(root, resolved)
	if err != nil || check == ".." || strings.HasPrefix(check, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %s escapes the workspace", ErrInvalidDir, rel)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrInvalidDir, rel)
	}
	return resolved, nil
}
