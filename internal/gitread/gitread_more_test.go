package gitread

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ngodingmcp/internal/workspace"
)

func TestStatusOutsideRepo(t *testing.T) {
	g, _ := newGit(t)
	// The temp workspace has no .git, so every read must say so instead of
	// letting git diff fall back to comparing plain files.
	if _, err := g.Status(context.Background()); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("Status err = %v, want ErrNotRepo", err)
	}
	if _, err := g.Diff(context.Background(), DiffOptions{}); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("Diff err = %v, want ErrNotRepo", err)
	}
}

func TestLogPathShowsOnlyMatchingCommits(t *testing.T) {
	g, root := newGit(t)
	gitOut(t, root, "init", "-q")
	write(t, root, "a.txt", "a\n")
	commitAll(t, root, "touch a")
	write(t, root, "b.txt", "b\n")
	commitAll(t, root, "touch b")

	out, err := g.Log(context.Background(), LogOptions{Path: "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Output, "touch a") || strings.Contains(out.Output, "touch b") {
		t.Fatalf("log for a.txt = %q, want only the commit that touched a.txt", out.Output)
	}
}

func TestPathOutsideWorkspaceIsRejected(t *testing.T) {
	g, root := newGit(t)
	gitOut(t, root, "init", "-q")
	write(t, root, "a.txt", "a\n")
	commitAll(t, root, "init")

	if _, err := g.Log(context.Background(), LogOptions{Path: "../outside"}); err == nil {
		t.Fatal("Log accepted a path outside the workspace")
	}
	if _, err := g.Diff(context.Background(), DiffOptions{Path: "../outside"}); err == nil {
		t.Fatal("Diff accepted a path outside the workspace")
	}
}

func TestDiffUnknownRefReportsGitError(t *testing.T) {
	g, root := newGit(t)
	gitOut(t, root, "init", "-q")
	write(t, root, "a.txt", "a\n")
	commitAll(t, root, "init")

	_, err := g.Diff(context.Background(), DiffOptions{Ref: "no-such-branch"})
	if err == nil || !strings.Contains(err.Error(), "git diff:") {
		t.Fatalf("Diff err = %v, want a git diff error", err)
	}
}

func TestAllowSensitiveShowsSensitiveFiles(t *testing.T) {
	g, root := newGit(t)
	gitOut(t, root, "init", "-q")
	write(t, root, "main.go", "package main\n")
	commitAll(t, root, "init")
	write(t, root, ".env", "TOKEN=x\n")

	out, err := g.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Output, ".env") {
		t.Fatalf("guarded status lists .env:\n%s", out.Output)
	}

	// With the guard off the file is an ordinary untracked file.
	g.ws.(*workspace.Workspace).SetAllowSensitive(true)
	out, err = g.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Output, ".env") {
		t.Fatalf("status with AllowSensitive misses .env:\n%s", out.Output)
	}
}

func TestLogDefaultLimitIsTen(t *testing.T) {
	g, root := newGit(t)
	gitOut(t, root, "init", "-q")
	for i := 0; i < 12; i++ {
		write(t, root, "f.txt", fmt.Sprintf("%d\n", i))
		commitAll(t, root, fmt.Sprintf("commit %02d", i))
	}
	out, err := g.Log(context.Background(), LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(out.Output), "\n") + 1; got != 10 {
		t.Fatalf("default log returned %d commits, want 10:\n%s", got, out.Output)
	}
	if !strings.Contains(out.Output, "commit 11") || strings.Contains(out.Output, "commit 01") {
		t.Fatalf("default log is not the newest 10 commits:\n%s", out.Output)
	}
}

func TestLogBody(t *testing.T) {
	g, root := newGit(t)
	gitOut(t, root, "init", "-q")
	write(t, root, "f.txt", "x\n")
	commitAll(t, root, "subject line\n\nbody detail")

	plain, err := g.Log(context.Background(), LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.Output, "body detail") {
		t.Fatalf("body leaked without body=true:\n%s", plain.Output)
	}
	withBody, err := g.Log(context.Background(), LogOptions{Body: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withBody.Output, "subject line") || !strings.Contains(withBody.Output, "body detail") {
		t.Fatalf("body=true output = %q", withBody.Output)
	}
}

func TestDiffStagedAndStat(t *testing.T) {
	g, root := newGit(t)
	gitOut(t, root, "init", "-q")
	write(t, root, "a.txt", "one\n")
	commitAll(t, root, "init")
	write(t, root, "a.txt", "two\n")
	gitOut(t, root, "add", "a.txt")

	// The change is staged, so the plain diff (work tree vs index) is empty.
	plain, err := g.Diff(context.Background(), DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(plain.Output) != "" {
		t.Fatalf("unstaged diff = %q, want empty", plain.Output)
	}
	staged, err := g.Diff(context.Background(), DiffOptions{Staged: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged.Output, "+two") {
		t.Fatalf("staged diff misses the change:\n%s", staged.Output)
	}
	stat, err := g.Diff(context.Background(), DiffOptions{Staged: true, Stat: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stat.Output, "a.txt") || strings.Contains(stat.Output, "+two") {
		t.Fatalf("stat diff = %q, want a per-file summary only", stat.Output)
	}
}
