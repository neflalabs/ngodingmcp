// Package gitread runs a fixed set of read-only git commands (status, diff,
// log) inside the workspace root. Callers never pass raw git arguments: every
// option is built here, paths are validated by the workspace first, and
// sensitive files (see workspace.IsSensitive) are excluded from the output.
package gitread

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ngodingmcp/internal/gitutil"
	"ngodingmcp/internal/workspace"
)

// MaxOutputBytes caps the output captured from one git command.
const MaxOutputBytes = gitutil.MaxOutputBytes

// DefaultTimeout bounds one git command.
const DefaultTimeout = 20 * time.Second

// ErrNoGit is returned when the git binary is not installed.
var ErrNoGit = errors.New("git is not installed on the server")

// ErrNotRepo is returned when the workspace root is not inside a git work tree.
var ErrNotRepo = errors.New("workspace is not a git repository")

// Workspace is the minimal contract read-only git needs. Narrowing it keeps
// gitread usable with any workspace implementation.
type Workspace interface {
	Root() string
	CleanPath(rel string) (string, error)
	AllowSensitive() bool
	SafeError(err error) error
}

// Git runs read-only git commands for one workspace.
type Git struct {
	ws      Workspace
	timeout time.Duration
}

// New returns a Git bound to ws.
func New(ws Workspace) *Git {
	return &Git{ws: ws, timeout: DefaultTimeout}
}

// Result is the captured output of one command.
type Result struct {
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
}

// Status returns `git status --short --branch` for the workspace.
func (g *Git) Status(ctx context.Context) (Result, error) {
	paths, err := g.paths("")
	if err != nil {
		return Result{}, err
	}
	return g.run(ctx, []string{"status", "--short", "--branch", "--untracked-files=normal"}, paths)
}

// DiffOptions selects what Diff shows.
type DiffOptions struct {
	Path   string // optional file or directory, relative to the workspace root
	Staged bool   // compare the index with HEAD instead of the work tree with the index
	Stat   bool   // only a per-file summary
	Ref    string // optional commit/branch to compare against (e.g. HEAD~1)
}

// Diff returns `git diff` for the workspace or one path.
func (g *Git) Diff(ctx context.Context, o DiffOptions) (Result, error) {
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color"}
	if o.Staged {
		args = append(args, "--cached")
	}
	if o.Stat {
		args = append(args, "--stat")
	}
	if o.Ref != "" {
		if err := validRef(o.Ref); err != nil {
			return Result{}, err
		}
		args = append(args, o.Ref)
	}
	paths, err := g.paths(o.Path)
	if err != nil {
		return Result{}, err
	}
	return g.run(ctx, args, paths)
}

// LogOptions selects what Log shows.
type LogOptions struct {
	Path  string // optional file or directory, relative to the workspace root
	Limit int    // number of commits (default 10, max 200)
	Body  bool   // include the commit body below the subject line
}

// Log returns a one-line-per-commit history (plus the body when asked).
func (g *Git) Log(ctx context.Context, o LogOptions) (Result, error) {
	limit := o.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 200 {
		limit = 200
	}
	format := "%h %ad %an  %s"
	if o.Body {
		format += "%n%b"
	}
	args := []string{"log", "--no-color", "-n", strconv.Itoa(limit), "--date=short", "--pretty=format:" + format}
	paths, err := g.paths(o.Path)
	if err != nil {
		return Result{}, err
	}
	return g.run(ctx, args, paths)
}

// paths turns an optional user path into a pathspec list that always
// excludes sensitive files.
func (g *Git) paths(rel string) ([]string, error) {
	var out []string
	if strings.TrimSpace(rel) != "" {
		clean, err := g.ws.CleanPath(rel)
		if err != nil {
			return nil, err
		}
		out = append(out, ":(literal)"+clean)
	} else {
		out = append(out, ".")
	}
	if !g.ws.AllowSensitive() {
		out = append(out, workspace.SensitivePatterns()...)
	}
	return out, nil
}

// validRef accepts ordinary revision names only, so a ref can never be read
// as an option or a range with side effects (see internal/gitutil).
func validRef(ref string) error { return gitutil.ValidRef(ref) }

func (g *Git) run(ctx context.Context, args, paths []string) (Result, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return Result{}, ErrNoGit
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := gitutil.CheckLocalConfig(ctx, g.ws.Root()); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	// Repository config can name programs git would run (fsmonitor, pagers,
	// external diff, textconv). Turn those off so a read never executes code.
	full := []string{
		"--no-pager",
		"-c", "core.fsmonitor=false",
		"-c", "core.quotepath=off",
		"-c", "color.ui=false",
		"-c", "diff.external=",
	}
	full = append(full, args...)
	full = append(full, "--")
	full = append(full, paths...)

	// Outside a repository some commands (git diff) silently fall back to
	// comparing plain files, so check for a work tree first.
	probe := exec.CommandContext(ctx, bin, "rev-parse", "--is-inside-work-tree")
	probe.Dir = g.ws.Root()
	probe.Env = append(gitutil.SanitizeEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	probe.WaitDelay = 5 * time.Second
	if out, err := probe.Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		if ctx.Err() == context.DeadlineExceeded {
			return Result{}, fmt.Errorf("git %s timed out after %s", args[0], g.timeout)
		}
		return Result{}, ErrNotRepo
	}

	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Dir = g.ws.Root()
	cmd.Env = append(gitutil.SanitizeEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "LC_ALL=C")
	cmd.WaitDelay = 5 * time.Second
	// Cap while reading; trimming after cmd.Run would still buffer everything.
	stdout := gitutil.NewCappedBuffer(MaxOutputBytes)
	stderr := gitutil.NewCappedBuffer(MaxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "not a git repository") {
			return Result{}, ErrNotRepo
		}
		if ctx.Err() == context.DeadlineExceeded {
			return Result{}, fmt.Errorf("git %s timed out after %s", args[0], g.timeout)
		}
		if msg == "" {
			msg = err.Error()
		}
		return Result{}, g.ws.SafeError(fmt.Errorf("git %s: %s", args[0], msg))
	}

	return Result{Output: stdout.String(), Truncated: stdout.Truncated()}, nil
}
