// Package runner executes a small, explicitly allowlisted set of commands
// inside the workspace root. It never invokes a shell.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultMaxOutputBytes caps how much combined output is kept in memory.
const DefaultMaxOutputBytes = 256 << 10

// DefaultTimeout is used when Options.Timeout is zero.
const DefaultTimeout = 60 * time.Second

// Sentinel errors returned by the runner.
var (
	ErrEmptyCommand = errors.New("empty command")
	ErrNotAllowed   = errors.New("command is not in the allowlist")
	// ErrCallerDeadline distinguishes a deadline from the caller from the
	// runner's own timeout, which is reported as Result.TimedOut instead.
	ErrCallerDeadline = errors.New("caller deadline exceeded")
)

// blockedFlags are arguments that turn an allowlisted command into a way to
// run other programs or write outside the workspace: -exec/-toolexec make the
// go toolchain invoke an alternate program, --exec covers git helpers,
// --output lets git diff/log overwrite files anywhere the user can write,
// --no-index lets git diff read files the user can read (e.g. /etc/passwd),
// -vettool makes go vet load a custom analysis program, and staticcheck's
// -debug.* profiling flags write files to any path.
var blockedFlags = []string{
	"-exec", "--exec", "-toolexec", "--toolexec", "--output", "-output", "--no-index", "-vettool", "--vettool",
	"-debug.cpuprofile", "--debug.cpuprofile", "-debug.memprofile", "--debug.memprofile",
	"-debug.trace", "--debug.trace", "-debug.measure-analyzers", "--debug.measure-analyzers",
}

// RestartCommand is the exact command that restarts the ngodingmcp service
// itself. It is never executed as a program: Run hands it to
// Options.Restart, which queues the restart so the reply goes out first.
const RestartCommand = "ngodingmcp restart"

// ErrRestartUnavailable is returned for RestartCommand when the Runner has no
// Options.Restart hook.
var ErrRestartUnavailable = errors.New("ngodingmcp restart is not available in this server")

// Options configures a Runner.
type Options struct {
	// Root is the working directory for every command.
	Root string
	// Allowlist holds command prefixes, e.g. "go test". A request is accepted
	// when its argv starts with one of these prefixes. Prefer ExactAllowlist
	// for commands such as package installers.
	Allowlist []string
	// ExactAllowlist holds complete argv values, e.g. "bun install --frozen-lockfile".
	// A request must match every token exactly.
	ExactAllowlist []string
	// Timeout bounds a single execution. Zero means DefaultTimeout.
	Timeout time.Duration
	// MaxOutputBytes bounds captured output. Zero means DefaultMaxOutputBytes.
	MaxOutputBytes int
	// Restart handles RestartCommand when the allowlist admits it: it must
	// only schedule the restart and return the message for the result. When
	// nil, RestartCommand fails with ErrRestartUnavailable.
	Restart func() (string, error)
}

// Result describes one finished command.
type Result struct {
	Argv       []string `json:"argv"`
	ExitCode   int      `json:"exitCode"`
	Output     string   `json:"output"`
	Truncated  bool     `json:"truncated"`
	TimedOut   bool     `json:"timedOut"`
	DurationMS int64    `json:"durationMs"`
}

// Runner executes allowlisted commands without a shell.
type Runner struct {
	root         atomic.Value // string; swapped by SetRoot on a workspace switch
	prefix       [][]string
	prefixLabels []string
	exact        [][]string
	exactLabels  []string
	timeout      time.Duration
	maxOutput    int
	restart      func() (string, error)
}

// New validates the allowlists and builds a Runner.
func New(opts Options) (*Runner, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxOutput := opts.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = DefaultMaxOutputBytes
	}

	prefix, prefixLabels, err := parseEntries(opts.Allowlist, "allowlist")
	if err != nil {
		return nil, err
	}
	exact, exactLabels, err := parseEntries(opts.ExactAllowlist, "exact allowlist")
	if err != nil {
		return nil, err
	}
	if len(prefix) == 0 && len(exact) == 0 {
		return nil, errors.New("runner: allowlist is empty (add at least one prefix or exact command)")
	}

	r := &Runner{
		prefix:       prefix,
		prefixLabels: prefixLabels,
		exact:        exact,
		exactLabels:  exactLabels,
		timeout:      timeout,
		maxOutput:    maxOutput,
		restart:      opts.Restart,
	}
	r.root.Store(opts.Root)
	return r, nil
}

// Root returns the working directory used for every command.
func (r *Runner) Root() string {
	s, _ := r.root.Load().(string)
	return s
}

// SetRoot changes the working directory for commands started afterwards. It
// is used when the active workspace is switched; running commands keep the
// directory they were started in.
func (r *Runner) SetRoot(root string) { r.root.Store(root) }

func parseEntries(entries []string, kind string) ([][]string, []string, error) {
	var tokens [][]string
	var labels []string
	for _, entry := range entries {
		label := strings.TrimSpace(entry)
		if label == "" {
			continue
		}
		parts := strings.Fields(label)
		if len(parts) == 0 {
			continue
		}
		if err := validateTokens(parts); err != nil {
			return nil, nil, fmt.Errorf("runner: %s entry %q: %w", kind, label, err)
		}
		tokens = append(tokens, parts)
		labels = append(labels, label)
	}
	return tokens, labels, nil
}

// Allowlist returns the configured prefix entries.
func (r *Runner) Allowlist() []string {
	out := make([]string, len(r.prefixLabels))
	copy(out, r.prefixLabels)
	return out
}

// ExactAllowlist returns the configured exact-argv entries.
func (r *Runner) ExactAllowlist() []string {
	out := make([]string, len(r.exactLabels))
	copy(out, r.exactLabels)
	return out
}

// Timeout returns the maximum execution time for one command.
func (r *Runner) Timeout() time.Duration { return r.timeout }

// Run executes argv if it matches the allowlist.
func (r *Runner) Run(ctx context.Context, argv []string) (Result, error) {
	if err := r.Check(argv); err != nil {
		return Result{}, err
	}
	if IsRestart(argv) {
		return r.runRestart(argv)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = r.Root()
	// Children could exfiltrate the parent's environment, so secrets are
	// stripped; an allowlist of flags/commands still governs what runs.
	cmd.Env = append(CleanEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0")
	// Ensure cancellation (timeout) also terminates child processes spawned by
	// the command, e.g. the workers started by package managers.
	setProcessGroup(cmd)

	out := &cappedBuffer{limit: r.maxOutput}
	cmd.Stdout = out
	cmd.Stderr = out

	runErr := cmd.Run()
	res := Result{
		Argv:       append([]string(nil), argv...),
		Output:     out.String(),
		Truncated:  out.Truncated(),
		DurationMS: time.Since(start).Milliseconds(),
	}
	// Check the caller first: runCtx inherits the caller's deadline, so its
	// DeadlineExceeded alone cannot tell whose deadline fired. A caller
	// deadline or cancel is an error; our own timeout stays a TimedOut result.
	if err := ctx.Err(); err != nil {
		res.ExitCode = -1
		if errors.Is(err, context.DeadlineExceeded) {
			return res, fmt.Errorf("%w: %w", ErrCallerDeadline, err)
		}
		return res, err
	}
	if runCtx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	if runErr == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if errors.Is(runErr, exec.ErrWaitDelay) {
		// The command itself is gone; a detached child kept the pipes
		// open past WaitDelay. Surface the partial output with a clear
		// error instead of a bare wrapped exec error.
		res.ExitCode = -1
		return res, fmt.Errorf("%s: output pipes stayed open past the wait delay (detached children); output may be incomplete", argv[0])
	}
	return res, fmt.Errorf("run %s: %w", argv[0], runErr)
}

// IsRestart reports whether argv is exactly RestartCommand.
func IsRestart(argv []string) bool {
	return FormatArgv(argv) == RestartCommand
}

// runRestart answers RestartCommand through the Restart hook instead of
// starting a program: the hook queues the restart and returns at once, so
// this tool result is written before the server stops.
func (r *Runner) runRestart(argv []string) (Result, error) {
	if r.restart == nil {
		return Result{}, ErrRestartUnavailable
	}
	msg, err := r.restart()
	if err != nil {
		return Result{}, err
	}
	return Result{Argv: append([]string(nil), argv...), Output: msg}, nil
}

// Check reports whether argv is allowed to execute. It applies the same
// validation used by Run, so long-running process managers can enforce the
// policy without executing anything.
func (r *Runner) Check(argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return ErrEmptyCommand
	}
	if err := validateArgv(argv); err != nil {
		return err
	}
	if err := checkBlockedFlags(argv); err != nil {
		return err
	}
	if err := r.checkOutsidePaths(argv); err != nil {
		return err
	}
	if !r.allowed(argv) {
		return fmt.Errorf("%w: %s", ErrNotAllowed, FormatArgv(argv))
	}
	return nil
}

// checkBlockedFlags rejects argv that would hand program execution (or an
// arbitrary file to overwrite) to an allowlisted command. The allowlist
// constrains which commands may run; these flags are never legitimate here.
func checkBlockedFlags(argv []string) error {
	for _, tok := range argv[1:] {
		for _, flag := range blockedFlags {
			if tok == flag || strings.HasPrefix(tok, flag+"=") {
				return fmt.Errorf("%w: argument %q is not allowed (it would run another program or write outside the workspace)", ErrNotAllowed, tok)
			}
		}
	}
	return nil
}

// checkOutsidePaths rejects arguments that name files outside the workspace
// root. The allowlist bounds which program may run, not which files it can
// reach: `go build -o /tmp/x` writes outside the root, and a symlinked path
// can hide the same escape behind a relative name.
// ponytail: path checks are not an OS sandbox; add Landlock/bubblewrap when
// untrusted workspaces need real filesystem isolation (a new dependency).
func (r *Runner) checkOutsidePaths(argv []string) error {
	root, err := filepath.EvalSymlinks(r.Root())
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	for _, tok := range argv[1:] {
		p := strings.ReplaceAll(tok, "\\", "/")
		if filepath.IsAbs(tok) || strings.HasPrefix(p, "/") {
			return fmt.Errorf("%w: argument %q is an absolute path", ErrNotAllowed, tok)
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == ".." {
				return fmt.Errorf("%w: argument %q escapes the workspace root", ErrNotAllowed, tok)
			}
		}
		value := strings.TrimLeft(tok, "-")
		if i := strings.IndexByte(value, '='); i >= 0 {
			value = value[i+1:]
		}
		if value == "" {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(value))
		for {
			if _, err := os.Lstat(full); err == nil {
				resolved, err := filepath.EvalSymlinks(full)
				if err != nil {
					return fmt.Errorf("resolve %q: %w", tok, err)
				}
				if pathOutside(root, resolved) {
					return fmt.Errorf("%w: argument %q points outside the workspace root", ErrNotAllowed, tok)
				}
				break
			}
			parent := filepath.Dir(full)
			if parent == full {
				break
			}
			full = parent
		}
	}
	return nil
}

func pathOutside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (r *Runner) allowed(argv []string) bool {
	for _, want := range r.exact {
		if argvEqual(argv, want) {
			return true
		}
	}
	for _, prefix := range r.prefix {
		if hasPrefix(argv, prefix) {
			return true
		}
	}
	return false
}

func argvEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasPrefix(argv, prefix []string) bool {
	if len(argv) < len(prefix) {
		return false
	}
	for i := range prefix {
		if argv[i] != prefix[i] {
			return false
		}
	}
	return true
}

// validateArgv rejects arguments that cannot be passed to exec safely. Shell
// metacharacters are allowed here because no shell is ever involved.
func validateArgv(argv []string) error {
	for _, tok := range argv {
		if strings.ContainsRune(tok, 0) || strings.ContainsAny(tok, "\r\n") {
			return errors.New("argument contains control characters")
		}
	}
	return nil
}

// validateTokens rejects control characters and shell metacharacters so a
// misconfigured allowlist entry cannot be smuggled through a shell later.
func validateTokens(tokens []string) error {
	for _, tok := range tokens {
		if strings.ContainsRune(tok, 0) || strings.ContainsAny(tok, "\r\n") {
			return errors.New("contains control characters")
		}
		if strings.ContainsAny(tok, ";&|<>$`") {
			return errors.New("contains shell metacharacters")
		}
	}
	return nil
}

// secretEnvMarkers mark an environment variable as carrying a secret. Any
// child process of an allowlisted command could otherwise exfiltrate the
// parent's environment (API keys, tokens, passwords) simply by reading it.
// Matching is case-insensitive and substring-based, on purpose: it is
// better to drop a harmless variable than to leak a credential.
var secretEnvMarkers = []string{
	"TOKEN", "SECRET", "PASSWORD", "PASSWD", "PASSPHRASE",
	"APIKEY", "API_KEY", "ACCESS_KEY", "PRIVATE_KEY",
	"CREDENTIAL", "CREDENTIALS", "AUTH_TOKEN", "AUTH_HEADER",
}

// CleanEnv returns env with secret-bearing variables removed. Everything
// else is kept: PATH, HOME, SSH_AUTH_SOCK, GOPATH and the rest of the
// toolchain environment are required for normal builds and tests.
func CleanEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		upper := strings.ToUpper(name)
		blocked := false
		for _, marker := range secretEnvMarkers {
			if strings.Contains(upper, marker) {
				blocked = true
				break
			}
		}
		if !blocked {
			out = append(out, kv)
		}
	}
	return out
}

// FormatArgv renders argv for display, quoting arguments with spaces.
func FormatArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\"'") {
			parts[i] = strconv.Quote(a)
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

// cappedBuffer is an io.Writer that keeps at most limit bytes and drains the
// rest. It is safe for concurrent use (os/exec may write stdout and stderr from
// separate goroutines).
type cappedBuffer struct {
	mu      sync.Mutex
	buf     strings.Builder
	limit   int
	dropped bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) <= remaining {
			b.buf.Write(p)
		} else {
			b.buf.Write(p[:remaining])
			b.dropped = true
		}
	} else if len(p) > 0 {
		b.dropped = true
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *cappedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}
