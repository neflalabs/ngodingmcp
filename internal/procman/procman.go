// Package procman manages long-running background processes started through
// MCP. Commands are validated against the same allowlist used by run_command
// and are always executed without a shell.
package procman

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ngodingmcp/internal/runner"
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
	root         string
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
	return &Manager{
		root:         opts.Root,
		run:          opts.Runner,
		maxProcesses: maxProcs,
		procs:        make(map[string]*Process),
	}, nil
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
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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
	if root, err := filepath.EvalSymlinks(m.root); err == nil {
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
		m.stopNames(context.Background(), running, 3*time.Second)
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
	root, err := filepath.EvalSymlinks(m.root)
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

func normalizeEnv(env map[string]string) (map[string]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		key := strings.TrimSpace(k)
		if !allowedEnvKeys[key] {
			return nil, fmt.Errorf("%w: %s", ErrInvalidEnv, key)
		}
		if strings.ContainsRune(v, 0) || strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("%w: %s contains control characters", ErrInvalidEnv, key)
		}
		out[key] = v
	}
	return out, nil
}

func envPairs(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func copyEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

func compileReady(expr string) (*regexp.Regexp, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid wait_for_log regex: %w", err)
	}
	return re, nil
}

func (m *Manager) waitReady(p *Process, re *regexp.Regexp, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if !p.isRunning() {
			info := p.info()
			return fmt.Errorf("%w: %s exited with code %d before becoming ready", ErrNotReady, p.name, info.ExitCode)
		}
		logOK := re == nil || p.logs.Match(re)
		portOK := port == 0 || portOpen(port)
		if logOK && portOK {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%w after %s (log matched: %t, port %d open: %t)",
				ErrNotReady, timeout.Round(time.Millisecond), logOK, port, portOK)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func portOpen(port int) bool {
	hosts := []string{"127.0.0.1", "::1"}
	for _, host := range hosts {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

// Process is one supervised child process.
type Process struct {
	name      string
	argv      []string
	dir       string
	env       map[string]string
	cmd       *exec.Cmd
	tree      *processTree
	logs      *logBuffer
	startedAt time.Time
	done      chan struct{}

	mu       sync.Mutex
	pid      int
	running  bool
	stopping bool
	ready    bool
	exitCode int
	errMsg   string
	endedAt  time.Time
}

func (p *Process) wait() {
	err := p.cmd.Wait()

	p.mu.Lock()
	p.running = false
	p.endedAt = time.Now()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			p.exitCode = exitErr.ExitCode()
		} else {
			p.exitCode = -1
			p.errMsg = err.Error()
		}
	}
	if p.tree != nil {
		p.tree.Close()
		p.tree = nil
	}
	p.mu.Unlock()
	close(p.done)
}

func (p *Process) isRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *Process) endedTime() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.endedAt
}

func (p *Process) setReady() {
	p.mu.Lock()
	p.ready = true
	p.mu.Unlock()
}

func (p *Process) terminate() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tree == nil {
		return nil
	}
	return p.tree.Terminate()
}

func (p *Process) kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tree == nil {
		return nil
	}
	return p.tree.Kill()
}

// escalate force-kills a process that ignored the graceful signal.
func (p *Process) escalate(grace time.Duration) {
	select {
	case <-p.done:
		return
	case <-time.After(grace):
		_ = p.kill()
	}
}

func (p *Process) info() Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.infoLocked()
}

func (p *Process) infoLocked() Info {
	info := Info{
		Name:       p.name,
		Argv:       append([]string(nil), p.argv...),
		Dir:        p.dir,
		PID:        p.pid,
		Running:    p.running,
		Stopping:   p.stopping,
		Ready:      p.ready,
		ExitCode:   p.exitCode,
		Error:      p.errMsg,
		StartedAt:  p.startedAt,
		LogBytes:   p.logs.Len(),
		LogDropped: p.logs.Dropped(),
	}
	end := p.endedAt
	if p.running {
		end = time.Now()
	}
	if !end.IsZero() {
		info.DurationMS = end.Sub(p.startedAt).Milliseconds()
		info.EndedAt = p.endedAt
	}
	return info
}

// ArgvString renders the command for messages.
func (i Info) ArgvString() string { return runner.FormatArgv(i.Argv) }

// ansiRE and oscRE strip terminal control sequences from process output.
var (
	ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	oscRE  = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
)

func sanitizeLine(s string) string {
	s = oscRE.ReplaceAllString(s, "")
	s = ansiRE.ReplaceAllString(s, "")
	if i := strings.LastIndexByte(s, '\r'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimRight(s, "\r")
}

// logBuffer keeps the last maxBytes of output as timestamped lines.
type logBuffer struct {
	mu       sync.Mutex
	maxBytes int
	lines    []LogLine
	partial  []byte
	nextSeq  int64
	bytes    int
	dropped  bool
}

func newLogBuffer(maxBytes int) *logBuffer {
	return &logBuffer{maxBytes: maxBytes}
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.partial = append(b.partial, p...)
	for {
		i := bytes.IndexByte(b.partial, '\n')
		if i < 0 {
			break
		}
		line := string(b.partial[:i])
		b.partial = b.partial[i+1:]
		b.addLineLocked(line)
	}
	if len(b.partial) > b.maxBytes {
		b.partial = append([]byte(nil), b.partial[len(b.partial)-b.maxBytes:]...)
		b.dropped = true
	}
	return len(p), nil
}

func (b *logBuffer) addLineLocked(line string) {
	line = sanitizeLine(line)
	if len(line) > b.maxBytes {
		line = line[:b.maxBytes]
		b.dropped = true
	}
	b.nextSeq++
	b.lines = append(b.lines, LogLine{Seq: b.nextSeq, Time: time.Now(), Text: line})
	b.bytes += len(line) + 1
	for b.bytes > b.maxBytes && len(b.lines) > 1 {
		b.bytes -= len(b.lines[0].Text) + 1
		b.lines = b.lines[1:]
		b.dropped = true
	}
}

func (b *logBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bytes
}

func (b *logBuffer) Dropped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

func (b *logBuffer) Match(re *regexp.Regexp) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, line := range b.lines {
		if re.MatchString(line.Text) {
			return true
		}
	}
	return len(b.partial) > 0 && re.Match(b.partial)
}

// Read returns the requested slice and the cursor for the next read.
func (b *logBuffer) Read(tailLines int, afterSeq int64) LogsResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []LogLine
	truncated := false
	if afterSeq <= 0 {
		start := 0
		if tailLines > 0 && len(b.lines) > tailLines {
			start = len(b.lines) - tailLines
		}
		out = append(out, b.lines[start:]...)
	} else {
		if len(b.lines) > 0 && b.lines[0].Seq > afterSeq+1 {
			truncated = true
		}
		for _, line := range b.lines {
			if line.Seq > afterSeq {
				out = append(out, line)
			}
		}
	}
	if len(b.partial) > 0 {
		out = append(out, LogLine{Seq: b.nextSeq, Time: time.Now(), Text: sanitizeLine(string(b.partial))})
	}
	return LogsResult{
		Lines:     out,
		NextSeq:   b.nextSeq,
		Truncated: truncated,
		Dropped:   b.dropped,
	}
}
