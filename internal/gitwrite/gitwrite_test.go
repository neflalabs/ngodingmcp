package gitwrite

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ngodingmcp/internal/gitutil"
	"ngodingmcp/internal/workspace"
)

// isolate keeps the developer's own git config (hooks, signing, identity)
// out of the tests and gives git a fixed identity.
func isolate(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

func newGit(t *testing.T) (*Git, string) {
	t.Helper()
	isolate(t)
	root := t.TempDir()
	ws, err := workspace.New(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	return New(ws), ws.Root()
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitOut(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRunCapsHugeOutput(t *testing.T) {
	g, root := newGit(t)
	gitOut(t, root, "init", "-q")
	write(t, root, "f.txt", "data")
	gitOut(t, root, "add", "f.txt")
	commit := exec.Command("git", "commit", "-q", "-F", "-")
	commit.Dir = root
	commit.Stdin = strings.NewReader(strings.Repeat("x", 300<<10))
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %.200s", err, out)
	}

	out, _, err := g.run(context.Background(), runOpts{}, "log", "-1", "--format=%B")
	if err != nil {
		t.Fatal(err)
	}
	// The truncation notice is allowed on top of the cap.
	if len(out) > gitutil.MaxOutputBytes+len("\n[git output truncated]") {
		t.Fatalf("captured %d bytes, limit is %d", len(out), gitutil.MaxOutputBytes)
	}
	if !strings.Contains(out, "[git output truncated]") {
		t.Fatal("huge output was capped without a notice")
	}
}

func TestInitAddCommit(t *testing.T) {
	g, root := newGit(t)
	ctx := context.Background()
	if _, err := g.Add(ctx, AddOptions{All: true}); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("add before init: err = %v, want ErrNotRepo", err)
	}
	msg, err := g.Init(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, root) {
		t.Errorf("init message leaks host path: %q", msg)
	}
	if b := gitOut(t, root, "symbolic-ref", "--short", "HEAD"); b != "main" {
		t.Errorf("initial branch = %q, want main", b)
	}
	if _, err := g.Init(ctx, "main"); !errors.Is(err, ErrAlreadyRepo) {
		t.Errorf("second init: err = %v, want ErrAlreadyRepo", err)
	}

	write(t, root, "main.go", "package main\n")
	write(t, root, "docs/a.md", "# a\n")
	write(t, root, ".env", "SECRET=1\n")
	write(t, root, ".env.example", "SECRET=\n")
	write(t, root, "keys/id_rsa", "key\n")
	write(t, root, "build/out.bin", "x\n")
	write(t, root, ".gitignore", "build/\n")

	res, err := g.Add(ctx, AddOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	wantStaged := []string{".env.example", ".gitignore", "docs/a.md", "main.go"}
	if !slices.Equal(res.Staged, wantStaged) {
		t.Errorf("staged = %v, want %v", res.Staged, wantStaged)
	}
	if !slices.Equal(res.Skipped, []string{".env", "keys/id_rsa"}) {
		t.Errorf("skipped = %v", res.Skipped)
	}

	c, err := g.Commit(ctx, CommitOptions{Message: "  first commit\n\nbody  "})
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "first commit" || c.Branch != "main" || c.Hash == "" || len(c.Files) != 4 {
		t.Errorf("commit result = %+v", c)
	}
	tracked := gitOut(t, root, "ls-files")
	if strings.Contains(tracked, "id_rsa") || strings.Contains(tracked, ".env\n") || strings.Contains(tracked, "out.bin") {
		t.Errorf("sensitive or ignored file committed:\n%s", tracked)
	}
	if _, err := g.Commit(ctx, CommitOptions{Message: "again"}); !errors.Is(err, ErrNothingStaged) {
		t.Errorf("empty commit: err = %v, want ErrNothingStaged", err)
	}
}

func TestAddPathsAndUnstage(t *testing.T) {
	g, root := newGit(t)
	ctx := context.Background()
	if _, err := g.Init(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.txt", "a\n")
	write(t, root, "b.txt", "b\n")
	if _, err := g.Add(ctx, AddOptions{}); err == nil {
		t.Error("add without paths or all should fail")
	}
	res, err := g.Add(ctx, AddOptions{Paths: []string{"a.txt"}})
	if err != nil || !slices.Equal(res.Staged, []string{"a.txt"}) {
		t.Fatalf("add a.txt: %v %v", res, err)
	}
	// Unstage works before the first commit and keeps the file.
	if err := g.Unstage(ctx, []string{"a.txt"}, false); err != nil {
		t.Fatal(err)
	}
	if s := gitOut(t, root, "diff", "--cached", "--name-only"); s != "" {
		t.Errorf("still staged: %q", s)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Errorf("unstage touched the work tree: %v", err)
	}
	for _, bad := range []string{"../outside", ".env", ".git/config"} {
		if _, err := g.Add(ctx, AddOptions{Paths: []string{bad}}); err == nil {
			t.Errorf("add %q should fail", bad)
		}
	}
	// A literal pathspec never expands glob magic.
	res, err = g.Add(ctx, AddOptions{Paths: []string{"*.txt"}})
	if err != nil || len(res.Staged) != 0 {
		t.Errorf("glob path staged %v (err %v), want nothing", res.Staged, err)
	}
}

func TestCommitRefusesStagedSensitiveFile(t *testing.T) {
	g, root := newGit(t)
	ctx := context.Background()
	if _, err := g.Init(ctx, ""); err != nil {
		t.Fatal(err)
	}
	write(t, root, ".env", "SECRET=1\n")
	write(t, root, "ok.txt", "ok\n")
	// Staged by hand outside the tool.
	gitOut(t, root, "add", ".env", "ok.txt")
	_, err := g.Commit(ctx, CommitOptions{Message: "leak"})
	if err == nil || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("err = %v, want refusal naming .env", err)
	}
	if err := g.Unstage(ctx, []string{".env"}, false); err == nil {
		t.Log("unstaging a sensitive path by name is blocked by the guard; use all=true")
	}
	if err := g.Unstage(ctx, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Add(ctx, AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit(ctx, CommitOptions{Message: "ok"}); err != nil {
		t.Fatal(err)
	}
}

func TestCommitDoesNotRunHooks(t *testing.T) {
	g, root := newGit(t)
	ctx := context.Background()
	if _, err := g.Init(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// An agent could write a hook into the workspace and point hooksPath at
	// it (as husky does). It must never run.
	marker := filepath.Join(root, "hook-ran")
	write(t, root, ".husky/pre-commit", "#!/bin/sh\ntouch "+marker+"\n")
	write(t, root, ".husky/post-commit", "#!/bin/sh\ntouch "+marker+"\n")
	for _, h := range []string{"pre-commit", "post-commit"} {
		if err := os.Chmod(filepath.Join(root, ".husky", h), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitOut(t, root, "config", "core.hooksPath", ".husky")
	gitOut(t, root, "config", "commit.gpgSign", "true")
	write(t, root, "f.txt", "f\n")
	if _, err := g.Add(ctx, AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit(ctx, CommitOptions{Message: "no hooks"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository hook ran during commit")
	}
}

func TestCommitIdentity(t *testing.T) {
	g, root := newGit(t)
	ctx := context.Background()
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if _, err := g.Init(ctx, ""); err != nil {
		t.Fatal(err)
	}
	write(t, root, "f.txt", "f\n")
	if _, err := g.Add(ctx, AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit(ctx, CommitOptions{Message: "x", AuthorName: "Nefla"}); err == nil {
		t.Error("name without email should fail")
	}
	if _, err := g.Commit(ctx, CommitOptions{Message: "x", AuthorName: "a\nb", AuthorEmail: "a@b"}); err == nil {
		t.Error("newline in name should fail")
	}
	if _, err := g.Commit(ctx, CommitOptions{Message: "x", AuthorName: "Nefla", AuthorEmail: "nefla@example.com"}); err != nil {
		t.Fatal(err)
	}
	if a := gitOut(t, root, "log", "-1", "--pretty=%an <%ae>"); a != "Nefla <nefla@example.com>" {
		t.Errorf("author = %q", a)
	}
}

func TestBranches(t *testing.T) {
	g, root := newGit(t)
	ctx := context.Background()
	if _, err := g.Init(ctx, ""); err != nil {
		t.Fatal(err)
	}
	write(t, root, "f.txt", "1\n")
	if _, err := g.Add(ctx, AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit(ctx, CommitOptions{Message: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CreateBranch(ctx, "feature/git", "", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := g.CurrentBranch(ctx); b != "main" {
		t.Errorf("create without switch moved HEAD to %q", b)
	}
	if _, err := g.CreateBranch(ctx, "feature/git", "", true); err == nil {
		t.Error("creating an existing branch should fail")
	}
	if _, err := g.Switch(ctx, "feature/git"); err != nil {
		t.Fatal(err)
	}
	if b, _ := g.CurrentBranch(ctx); b != "feature/git" {
		t.Errorf("current = %q", b)
	}
	if _, err := g.Switch(ctx, "nope"); err == nil {
		t.Error("switching to a missing branch should fail")
	}
	if _, err := g.CreateBranch(ctx, "exp", "main", true); err != nil {
		t.Fatal(err)
	}

	// Uncommitted work that would be overwritten blocks the switch.
	write(t, root, "f.txt", "exp\n")
	gitOut(t, root, "commit", "-qam", "exp")
	write(t, root, "f.txt", "dirty\n")
	if _, err := g.Switch(ctx, "main"); err == nil {
		t.Error("switch should refuse to overwrite local changes")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "f.txt")); string(b) != "dirty\n" {
		t.Errorf("local change lost: %q", b)
	}
}

func TestNestedWorkspaceIsRefused(t *testing.T) {
	isolate(t)
	outer := t.TempDir()
	gitOut(t, outer, "init", "-q")
	inner := filepath.Join(outer, "sub")
	ws, err := workspace.New(inner, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := New(ws)
	ctx := context.Background()
	write(t, inner, "f.txt", "f\n")
	if _, err := g.Add(ctx, AddOptions{All: true}); !errors.Is(err, ErrNotRoot) {
		t.Fatalf("add in nested workspace: err = %v, want ErrNotRoot", err)
	}
	msg, err := g.Init(ctx, "")
	if err != nil || !strings.Contains(msg, "its own") {
		t.Fatalf("nested init: %q %v", msg, err)
	}
	if _, err := g.Add(ctx, AddOptions{All: true}); err != nil {
		t.Fatalf("add after nested init: %v", err)
	}
}

func TestValidBranch(t *testing.T) {
	for _, ok := range []string{"main", "feature/git-write", "v2026.11.9", "fix_1"} {
		if err := ValidBranch(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-f", "--force", "a..b", "a b", "a~1", "a^", "x.lock", "x/", "/x", ".hidden", "a//b", "HEAD", "a:b", "a@{1}"} {
		if err := ValidBranch(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
