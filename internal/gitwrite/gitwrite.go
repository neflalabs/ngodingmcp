// Package gitwrite runs a fixed set of non-destructive git write commands
// inside the workspace root: init, add, unstage, commit, create branch and
// switch branch. Callers never pass raw git arguments: every option is built
// here, paths are validated by the workspace first, and sensitive files (see
// workspace.IsSensitive) are never staged or committed while the guard is on.
//
// Nothing here can lose work: there is no push, pull, fetch, merge, rebase,
// reset --hard, checkout of files, clean, stash drop, branch delete, amend or
// force option. Repository hooks and commit signing never run, so a commit
// cannot execute a script the agent wrote into the workspace.
package gitwrite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ngodingmcp/internal/workspace"
)

// DefaultTimeout bounds one git command.
const DefaultTimeout = 30 * time.Second

// MaxMessageBytes caps a commit message.
const MaxMessageBytes = 8 << 10

// addBatch is the number of pathspecs passed to one `git add` call.
const addBatch = 200

var (
	// ErrNoGit is returned when the git binary is not installed.
	ErrNoGit = errors.New("git is not installed on the server")
	// ErrNotRepo is returned when the workspace root is not a git work tree.
	ErrNotRepo = errors.New("workspace is not a git repository; run git_init first")
	// ErrNotRoot is returned when the workspace sits inside a repository whose
	// top level is outside the workspace. Writing there could stage or commit
	// files the workspace cannot see.
	ErrNotRoot = errors.New("the workspace is inside a larger git repository whose root is outside the workspace; git write tools only work when the workspace root is the repository root")
	// ErrAlreadyRepo is returned by Init when the workspace root already is
	// the root of a repository.
	ErrAlreadyRepo = errors.New("workspace is already a git repository")
	// ErrNothingStaged is returned by Commit when the index matches HEAD.
	ErrNothingStaged = errors.New("nothing staged to commit; stage files with git_add first")
)

// Git runs git write commands for one workspace.
type Git struct {
	ws      *workspace.Workspace
	timeout time.Duration
}

// New returns a Git bound to ws.
func New(ws *workspace.Workspace) *Git {
	return &Git{ws: ws, timeout: DefaultTimeout}
}

// Init creates a repository at the workspace root with the given initial
// branch (default "main"). It refuses when the root already is a repository.
// A workspace nested inside a larger repository gets its own repository.
func (g *Git) Init(ctx context.Context, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = "main"
	}
	if err := ValidBranch(branch); err != nil {
		return "", err
	}
	nested := false
	switch err := g.requireRoot(ctx); {
	case err == nil:
		return "", ErrAlreadyRepo
	case errors.Is(err, ErrNotRoot):
		nested = true
	case !errors.Is(err, ErrNotRepo):
		return "", err
	}
	if _, err := g.git(ctx, "", "init", "--quiet", "--initial-branch="+branch); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Initialized an empty git repository at the workspace root (branch %s).", branch)
	if nested {
		msg += " The workspace was inside a larger repository; it now has its own."
	}
	return msg, nil
}

// AddOptions selects what Add stages.
type AddOptions struct {
	Paths []string // files or directories, relative to the workspace root
	All   bool     // stage every change in the workspace (only when Paths is empty)
}

// AddResult lists what Add staged and what it left out.
type AddResult struct {
	Staged  []string `json:"staged"`
	Skipped []string `json:"skippedSensitive,omitempty"`
}

// Add stages new, modified and deleted files under the given paths (or the
// whole workspace with All). Ignored files are never staged and there is no
// force option. Sensitive files are skipped and reported while the guard is
// on.
func (g *Git) Add(ctx context.Context, o AddOptions) (AddResult, error) {
	scope, err := g.scope(o.Paths, o.All)
	if err != nil {
		return AddResult{}, err
	}
	if err := g.requireRoot(ctx); err != nil {
		return AddResult{}, err
	}
	out, err := g.git(ctx, "", append([]string{"ls-files", "-z", "--modified", "--deleted", "--others", "--exclude-standard", "--"}, scope...)...)
	if err != nil {
		return AddResult{}, err
	}
	res := AddResult{Staged: []string{}}
	seen := map[string]bool{}
	for _, p := range strings.Split(out, "\x00") {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if !g.ws.AllowSensitive() && workspace.IsSensitive(p) {
			res.Skipped = append(res.Skipped, p)
			continue
		}
		res.Staged = append(res.Staged, p)
	}
	sort.Strings(res.Staged)
	sort.Strings(res.Skipped)
	for i := 0; i < len(res.Staged); i += addBatch {
		end := min(i+addBatch, len(res.Staged))
		args := []string{"add", "--all", "--"}
		for _, p := range res.Staged[i:end] {
			args = append(args, ":(literal)"+p)
		}
		if _, err := g.git(ctx, "", args...); err != nil {
			return AddResult{}, err
		}
	}
	return res, nil
}

// Unstage removes paths (or everything with all) from the index without
// touching the files in the work tree. It also works before the first commit.
func (g *Git) Unstage(ctx context.Context, paths []string, all bool) error {
	scope, err := g.scope(paths, all)
	if err != nil {
		return err
	}
	if err := g.requireRoot(ctx); err != nil {
		return err
	}
	_, err = g.git(ctx, "", append([]string{"reset", "--quiet", "--"}, scope...)...)
	return err
}

// CommitOptions describes one commit.
type CommitOptions struct {
	Message     string
	AuthorName  string // optional; falls back to git config user.name
	AuthorEmail string // optional; falls back to git config user.email
}

// CommitResult describes a created commit.
type CommitResult struct {
	Hash    string   `json:"hash"`
	Branch  string   `json:"branch"`
	Subject string   `json:"subject"`
	Files   []string `json:"files"`
}

// Commit records the staged changes. It never amends, never runs hooks and
// never signs, and it refuses when a sensitive file is staged while the
// guard is on.
func (g *Git) Commit(ctx context.Context, o CommitOptions) (CommitResult, error) {
	msg := strings.TrimSpace(strings.ReplaceAll(o.Message, "\r\n", "\n"))
	switch {
	case msg == "":
		return CommitResult{}, errors.New("commit message is required")
	case len(msg) > MaxMessageBytes:
		return CommitResult{}, fmt.Errorf("commit message is longer than %d bytes", MaxMessageBytes)
	case strings.ContainsRune(msg, 0):
		return CommitResult{}, errors.New("commit message contains a NUL byte")
	}
	var identity []string
	name, email := strings.TrimSpace(o.AuthorName), strings.TrimSpace(o.AuthorEmail)
	if name != "" || email != "" {
		if err := validIdentity(name, email); err != nil {
			return CommitResult{}, err
		}
		identity = []string{"-c", "user.name=" + name, "-c", "user.email=" + email}
	}
	if err := g.requireRoot(ctx); err != nil {
		return CommitResult{}, err
	}
	staged, err := g.git(ctx, "", "diff", "--cached", "--name-only", "-z", "--no-renames", "--no-ext-diff")
	if err != nil {
		return CommitResult{}, err
	}
	var files, blocked []string
	for _, p := range strings.Split(staged, "\x00") {
		if p == "" {
			continue
		}
		files = append(files, p)
		if workspace.IsSensitive(p) {
			blocked = append(blocked, p)
		}
	}
	if len(files) == 0 {
		return CommitResult{}, ErrNothingStaged
	}
	if len(blocked) > 0 && !g.ws.AllowSensitive() {
		return CommitResult{}, fmt.Errorf("refusing to commit sensitive files: %s (unstage them with git_unstage)", strings.Join(blocked, ", "))
	}
	args := append(identity, "commit", "--quiet", "--no-verify", "--no-edit", "--cleanup=strip", "--file=-")
	if _, err := g.git(ctx, msg+"\n", args...); err != nil {
		if strings.Contains(err.Error(), "user.email") || strings.Contains(err.Error(), "Author identity unknown") {
			return CommitResult{}, errors.New("git does not know who you are: set user.name and user.email in your git config, or pass author_name and author_email")
		}
		return CommitResult{}, err
	}
	head, err := g.git(ctx, "", "log", "-1", "--no-color", "--pretty=format:%h%x00%s")
	if err != nil {
		return CommitResult{}, err
	}
	hash, subject, _ := strings.Cut(head, "\x00")
	branch, _ := g.CurrentBranch(ctx)
	return CommitResult{Hash: hash, Branch: branch, Subject: subject, Files: files}, nil
}

// CurrentBranch returns the checked-out branch, or "" on a detached HEAD.
func (g *Git) CurrentBranch(ctx context.Context) (string, error) {
	out, err := g.git(ctx, "", "branch", "--show-current")
	return strings.TrimSpace(out), err
}

// CreateBranch creates a local branch at start (default HEAD) and, with
// switchTo, checks it out. Existing branches are never overwritten.
func (g *Git) CreateBranch(ctx context.Context, name, start string, switchTo bool) (string, error) {
	if err := ValidBranch(name); err != nil {
		return "", err
	}
	start = strings.TrimSpace(start)
	if start != "" {
		if err := validRef(start); err != nil {
			return "", err
		}
	}
	if err := g.requireRoot(ctx); err != nil {
		return "", err
	}
	if g.branchExists(ctx, name) {
		return "", fmt.Errorf("branch %q already exists", name)
	}
	var args []string
	if switchTo {
		args = []string{"switch", "--quiet", "--no-guess", "--create", name}
	} else {
		args = []string{"branch", "--no-track", name}
	}
	if start != "" {
		args = append(args, start)
	}
	if _, err := g.git(ctx, "", args...); err != nil {
		return "", err
	}
	if switchTo {
		return fmt.Sprintf("Created branch %s and switched to it.", name), nil
	}
	return fmt.Sprintf("Created branch %s.", name), nil
}

// Switch checks out an existing local branch. Git refuses when uncommitted
// changes would be overwritten; there is no force or discard option, so no
// work is lost. Changes that do not conflict are carried over.
func (g *Git) Switch(ctx context.Context, name string) (string, error) {
	if err := ValidBranch(name); err != nil {
		return "", err
	}
	if err := g.requireRoot(ctx); err != nil {
		return "", err
	}
	if !g.branchExists(ctx, name) {
		return "", fmt.Errorf("branch %q does not exist; create it with git_create_branch", name)
	}
	if _, err := g.git(ctx, "", "switch", "--quiet", "--no-guess", name); err != nil {
		return "", err
	}
	return fmt.Sprintf("Switched to branch %s.", name), nil
}

func (g *Git) branchExists(ctx context.Context, name string) bool {
	_, err := g.git(ctx, "", "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// scope turns optional user paths into literal pathspecs. Without paths, all
// must be set and the whole workspace is used.
func (g *Git) scope(paths []string, all bool) ([]string, error) {
	var out []string
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		clean, err := g.ws.CleanPath(p)
		if err != nil {
			return nil, err
		}
		out = append(out, ":(literal)"+clean)
	}
	if len(out) == 0 {
		if !all {
			return nil, errors.New("give one or more paths, or set all=true for the whole workspace")
		}
		out = []string{"."}
	}
	return out, nil
}

// requireRoot checks that the workspace root is the top level of a git work
// tree. It returns ErrNotRepo or ErrNotRoot otherwise.
func (g *Git) requireRoot(ctx context.Context) error {
	out, err := g.git(ctx, "", "rev-parse", "--show-toplevel")
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return ErrNotRepo
		}
		return err
	}
	top, err := filepath.EvalSymlinks(strings.TrimSpace(out))
	if err != nil || filepath.Clean(top) != filepath.Clean(g.ws.Root()) {
		return ErrNotRoot
	}
	return nil
}

// git runs one git command in the workspace root with hooks, signing,
// fsmonitor, pagers and prompts turned off, and returns stdout.
func (g *Git) git(ctx context.Context, stdin string, args ...string) (string, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return "", ErrNoGit
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	full := []string{
		"--no-pager",
		"-c", "core.fsmonitor=false",
		"-c", "core.quotepath=off",
		"-c", "color.ui=false",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "advice.detachedHead=false",
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Dir = g.ws.Root()
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "GIT_EDITOR=true", "LC_ALL=C")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		name := firstCommand(args)
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git %s timed out after %s: %w", name, g.timeout, context.DeadlineExceeded)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", g.ws.SafeError(fmt.Errorf("git %s: %s", name, msg))
	}
	return stdout.String(), nil
}

// firstCommand returns the git subcommand in args, skipping -c pairs.
func firstCommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

// ValidBranch accepts conservative branch names only: letters, digits and
// ._/- with no leading dash, no "..", no "@{", no trailing ".lock" or "/".
func ValidBranch(name string) error {
	bad := fmt.Errorf("invalid branch name %q", name)
	if name == "" || len(name) > 100 || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") ||
		strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "/.") ||
		strings.HasPrefix(name, ".") || name == "HEAD" {
		return bad
	}
	for _, c := range name {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._/-", c)
		if !ok {
			return bad
		}
	}
	return nil
}

// validRef accepts ordinary revision names only (same rule as gitread).
func validRef(ref string) error {
	if strings.HasPrefix(ref, "-") || len(ref) > 100 {
		return fmt.Errorf("invalid ref %q", ref)
	}
	for _, c := range ref {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._/-~^@", c)
		if !ok {
			return fmt.Errorf("invalid ref %q", ref)
		}
	}
	return nil
}

func validIdentity(name, email string) error {
	if name == "" || email == "" {
		return errors.New("author_name and author_email must be given together")
	}
	if len(name) > 200 || len(email) > 200 || strings.ContainsAny(name+email, "<>\n\r\x00") || !strings.Contains(email, "@") {
		return errors.New("invalid author_name or author_email")
	}
	return nil
}
