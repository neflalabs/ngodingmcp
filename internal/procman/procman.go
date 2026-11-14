// Package procman manages long-running background processes started through
// MCP. Commands are validated against the same allowlist used by run_command
// and are always executed without a shell.
package procman

import (
	"context"
	"errors"
	"fmt"
	"ngodingmcp/internal/runner"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultMaxProcesses is the number of processes allowed to run at once.
	DefaultMaxProcesses = 8
	// MaxLogBytes bounds the per-process output ring buffer.
	MaxLogBytes = 128 << 10
	// DefaultReadyTimeout is used when readiness is requested without a timeout.
	DefaultReadyTimeout = 10 * time.Second
	// MaxReadyTimeout caps the readiness wait.
	MaxReadyTimeout = 60 * time.Second
	// maxEntries bounds total tracked entries (running + exited).
	maxEntries = 64
	// stopGrace is the default SIGTERM-to-SIGKILL grace period.
	stopGrace = 5 * time.Second
	// maxTailLines caps process_logs tail requests.
	maxTailLines = 1000
)

// Sentinel errors returned by the manager.
var (
	ErrClosed      = errors.New("process manager is shut down")
	ErrNotFound    = errors.New("process not found")
	ErrExists      = errors.New("process name already exists")
	ErrTooMany     = errors.New("too many processes")
	ErrNotReady    = errors.New("process did not become ready")
	ErrInvalidDir  = errors.New("invalid working directory")
	ErrInvalidEnv  = errors.New("environment variable is not allowed")
	ErrInvalidPort = errors.New("invalid port")
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

// allowedEnvKeys limits which environment variables a caller may inject. Keys
// that can influence dynamic linking, shells, or runtimes are deliberately
// excluded (PATH, LD_PRELOAD, NODE_OPTIONS, ...).
var allowedEnvKeys = map[string]bool{
	"PORT":     true,
	"HOST":     true,
	"HOSTNAME": true,
	"NODE_ENV": true,
	"BUN_ENV":  true,
	"CI":       true,
}

// Options configures a Manager.
type Options struct {
	Root         string
	Runner       *runner.Runner
	MaxProcesses int
}

// StartOptions describes one background process launch.
type StartOptions struct {
	// Name is optional. Empty names are generated as proc-1, proc-2, ...
	Name string
	Argv []string
	// Dir is a workspace-relative working directory. Empty means the root.
	Dir string
	// Env holds extra environment variables. Only allowedEnvKeys are accepted.
	Env map[string]string
	// ReadyLog is a regular expression that must match the process output
	// before Start returns. Empty disables the log check.
	ReadyLog string
	// ReadyPort waits until a TCP listener accepts connections on this port.
	ReadyPort int
	// ReadyTimeout bounds the readiness wait. Zero means DefaultReadyTimeout.
	ReadyTimeout time.Duration
}

// Info is a snapshot of one process.
type Info struct {
	Name       string    `json:"name"`
	Argv       []string  `json:"argv"`
	Dir        string    `json:"dir,omitempty"`
	PID        int       `json:"pid"`
	Running    bool      `json:"running"`
	Stopping   bool      `json:"stopping"`
	Ready      bool      `json:"ready"`
	ExitCode   int       `json:"exitCode"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	EndedAt    time.Time `json:"endedAt,omitempty"`
	DurationMS int64     `json:"durationMs"`
	LogBytes   int       `json:"logBytes"`
	LogDropped bool      `json:"logDropped"`
}

// LogLine is one timestamped line of process output.
type LogLine struct {
	Seq  int64     `json:"seq"`
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

// LogsResult is an incremental read from a process log.
type LogsResult struct {
	Lines []LogLine `json:"lines"`
	// NextSeq is the cursor to pass as after_seq on the next read.
	NextSeq int64 `json:"nextSeq"`
	// Truncated reports that lines between the requested cursor and the
	// retained window were dropped.
	Truncated bool `json:"truncated"`
	// Dropped reports that the ring buffer has discarded older output.
	Dropped bool `json:"dropped"`
}

// Manager supervises background processes for one workspace.
type Manager struct {
	root         atomic.Value // string; swapped by SwitchRoot
	run          *runner.Runner
	maxProcesses int

	mu     sync.Mutex
	procs  map[string]*Process
	order  []string
	seq    int
	closed bool
}

// New builds a Manager. Runner defines the command allowlist and must not be
// nil.
func New(opts Options) (*Manager, error) {
	if opts.Runner == nil {
		return nil, errors.New("procman: runner is required")
	}
	maxProcs := opts.MaxProcesses
	if maxProcs <= 0 {
		maxProcs = DefaultMaxProcesses
	}
	m := &Manager{
		run:          opts.Runner,
		maxProcesses: maxProcs,
		procs:        make(map[string]*Process),
	}
	m.root.Store(opts.Root)
	return m, nil
}

// Root returns the workspace root new processes start in.
func (m *Manager) Root() string {
	s, _ := m.root.Load().(string)
	return s
}

// SwitchRoot is used when the active workspace changes. It stops every
// running process (they belong to the old workspace), forgets all processes
// with their logs, and makes later starts use root. It returns the names of
// the processes that were still running. A process that does not stop within
// ctx is left in the list so it stays visible.
func (m *Manager) SwitchRoot(ctx context.Context, root string, grace time.Duration) []string {
	var stopped []string
	for _, info := range m.List() {
		if info.Running {
			stopped = append(stopped, info.Name)
		}
	}
	m.StopAll(ctx, grace)
	for _, info := range m.List() {
		m.remove(info.Name) // remove keeps processes that still run
	}
	m.root.Store(root)
	return stopped
}

// Start launches a process with only a name and argv.
func (m *Manager) Start(name string, argv []string) (Info, error) {
	return m.StartWithOptions(StartOptions{Name: name, Argv: argv})
}

// StartWithOptions validates argv against the allowlist and launches it in the
// background. When readiness checks are requested, StartWithOptions blocks
// until they pass, the process exits, or the timeout fires.
func (m *Manager) StartWithOptions(opts StartOptions) (Info, error) {
	if err := m.run.Check(opts.Argv); err != nil {
		return Info{}, err
	}
	name := strings.TrimSpace(opts.Name)
	if name != "" && !nameRE.MatchString(name) {
		return Info{}, fmt.Errorf("invalid process name %q (use letters, digits, _ or -)", opts.Name)
	}
	dir, err := m.resolveDir(opts.Dir)
	if err != nil {
		return Info{}, err
	}
	env, err := normalizeEnv(opts.Env)
	if err != nil {
		return Info{}, err
	}
	readyRE, err := compileReady(opts.ReadyLog)
	if err != nil {
		return Info{}, err
	}
	if opts.ReadyPort < 0 || opts.ReadyPort > 65535 {
		return Info{}, fmt.Errorf("%w: %d", ErrInvalidPort, opts.ReadyPort)
	}
	timeout := opts.ReadyTimeout
	if timeout <= 0 {
		timeout = DefaultReadyTimeout
	}
	if timeout > MaxReadyTimeout {
		timeout = MaxReadyTimeout
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Info{}, ErrClosed
	}
	if name == "" {
		name = m.nextNameLocked()
	}
	if _, exists := m.procs[name]; exists {
		m.mu.Unlock()
		return Info{}, fmt.Errorf("%w: %s", ErrExists, name)
	}
	if m.runningLocked() >= m.maxProcesses {
		m.mu.Unlock()
		return Info{}, ErrTooMany
	}
	if len(m.procs) >= maxEntries && !m.evictLocked() {
		m.mu.Unlock()
		return Info{}, ErrTooMany
	}

	p := &Process{
		name:      name,
		argv:      append([]string(nil), opts.Argv...),
		dir:       dir,
		env:       env,
		logs:      newLogBuffer(MaxLogBytes),
		startedAt: time.Now(),
		ready:     readyRE == nil && opts.ReadyPort == 0,
		done:      make(chan struct{}),
	}
	cmd := exec.Command(opts.Argv[0], opts.Argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(runner.CleanEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0")
	for _, kv := range envPairs(env) {
		cmd.Env = append(cmd.Env, kv)
	}
	prepareCmd(cmd)
	cmd.Stdout = p.logs
	cmd.Stderr = p.logs
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		return Info{}, fmt.Errorf("start %s: %w", runner.FormatArgv(opts.Argv), err)
	}

	tree, err := attachTree(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		// Reap the child we just killed, otherwise it lingers as a zombie
		// for the lifetime of the server. Wait also drains the output
		// pipes via p.logs.
		go func() { _ = cmd.Wait() }()
		m.mu.Unlock()
		return Info{}, fmt.Errorf("attach %s: %w", runner.FormatArgv(opts.Argv), err)
	}
	p.cmd = cmd
	p.tree = tree
	p.pid = cmd.Process.Pid
	p.running = true
	m.procs[name] = p
	m.order = append(m.order, name)

	go p.wait()
	m.mu.Unlock()

	if readyRE != nil || opts.ReadyPort > 0 {
		if err := m.waitReady(p, readyRE, opts.ReadyPort, timeout); err != nil {
			return p.info(), err
		}
		p.setReady()
	}
	return p.info(), nil
}

// List returns snapshots in start order.
func (m *Manager) List() []Info {
	m.mu.Lock()
	names := append([]string(nil), m.order...)
	procs := make([]*Process, 0, len(names))
	for _, name := range names {
		if p := m.procs[name]; p != nil {
			procs = append(procs, p)
		}
	}
	m.mu.Unlock()

	out := make([]Info, 0, len(procs))
	for _, p := range procs {
		out = append(out, p.info())
	}
	return out
}

// Get returns a snapshot for one process.
func (m *Manager) Get(name string) (Info, error) {
	p := m.get(name)
	if p == nil {
		return Info{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return p.info(), nil
}

// Logs returns an incremental read of process output. A non-positive afterSeq
// selects the last tailLines lines (default 100). A positive afterSeq returns
// only lines newer than that cursor.
func (m *Manager) Logs(name string, tailLines int, afterSeq int64) (Info, LogsResult, error) {
	p := m.get(name)
	if p == nil {
		return Info{}, LogsResult{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if tailLines <= 0 {
		tailLines = 100
	}
	if tailLines > maxTailLines {
		tailLines = maxTailLines
	}
	return p.info(), p.logs.Read(tailLines, afterSeq), nil
}

// Allowlist exposes the command policy applied to background processes.
func (m *Manager) Allowlist() (prefix, exact []string) {
	return m.run.Allowlist(), m.run.ExactAllowlist()
}
