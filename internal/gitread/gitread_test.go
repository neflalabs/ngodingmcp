package gitread

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ngodingmcp/internal/workspace"
)

// isolate keeps the developer's own git config out of the tests and gives
// git a fixed identity, mirroring the gitwrite tests.
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

func commitAll(t *testing.T, root, msg string) {
	t.Helper()
	gitOut(t, root, "add", ".")
	gitOut(t, root, "commit", "-qm", msg)
}

func TestStatusExcludesSensitiveFiles(t *testing.T) {
	g, root := newGit(t)
	write(t, root, "main.go", "package main\n")
	write(t, root, ".env", "TOKEN=supersecret\n")
	write(t, root, "sub/.env", "TOKEN=nested\n")
	gitOut(t, root, "init", "-q")
	commitAll(t, root, "init")
	// Make both visible as modifications to prove they would be listed if
	// the guard did not exclude them.
	write(t, root, "main.go", "package main // changed\n")
	write(t, root, ".env", "TOKEN=changed\n")

	out, err := g.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Output, ".env") {
		t.Fatalf("status output leaks sensitive file:\n%s", out.Output)
	}
	if !strings.Contains(out.Output, "main.go") {
		t.Fatalf("status missing normal file:\n%s", out.Output)
	}
}

func TestDiffExcludesSensitiveFiles(t *testing.T) {
	g, root := newGit(t)
	write(t, root, ".env", "TOKEN=before\n")
	gitOut(t, root, "init", "-q")
	commitAll(t, root, "init")
	write(t, root, ".env", "TOKEN=after\n")

	out, err := g.Diff(context.Background(), DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Output, "TOKEN=") {
		t.Fatalf("diff output leaks sensitive file contents:\n%s", out.Output)
	}
}

func TestLogLimitIsClamped(t *testing.T) {
	g, root := newGit(t)
	gitOut(t, root, "init", "-q")
	for i := 0; i < 5; i++ {
		write(t, root, "f.txt", strings.Repeat("x", i+1)+"\n")
		commitAll(t, root, "c")
	}
	out, err := g.Log(context.Background(), LogOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(out.Output), "\n") + 1; got != 2 {
		t.Fatalf("log returned %d lines, want 2:\n%s", got, out.Output)
	}
	// Over-limit input must not be rejected, just clamped.
	if _, err := g.Log(context.Background(), LogOptions{Limit: 100000}); err != nil {
		t.Fatal(err)
	}
}

func TestDiffRejectsRefRange(t *testing.T) {
	g, root := newGit(t)
	gitOut(t, root, "init", "-q")
	write(t, root, "f.txt", "x\n")
	commitAll(t, root, "init")
	write(t, root, "f.txt", "y\n")
	commitAll(t, root, "second")
	for _, ref := range []string{"main..main", "HEAD...dev", "-f"} {
		if _, err := g.Diff(context.Background(), DiffOptions{Ref: ref}); err == nil {
			t.Fatalf("Diff with ref %q accepted", ref)
		}
	}
	if _, err := g.Diff(context.Background(), DiffOptions{Ref: "HEAD~1"}); err != nil {
		t.Fatalf("Diff with HEAD~1: %v", err)
	}
}

func TestDiffDoesNotRunTextconvDrivers(t *testing.T) {
	g, root := newGit(t)
	marker := filepath.Join(t.TempDir(), "textconv-ran")
	write(t, root, "evil.txt", "data\n")
	gitOut(t, root, "init", "-q")
	gitOut(t, root, "config", "diff.evil.textconv", "touch "+marker)
	write(t, root, ".gitattributes", "evil.txt diff=evil\n")
	commitAll(t, root, "init")
	write(t, root, "evil.txt", "data2\n")
	// The repo's own config names a textconv program, so every gitread
	// command must refuse before git ever runs.
	if _, err := g.Diff(context.Background(), DiffOptions{}); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("Diff err = %v, want refusal", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("textconv driver executed during git diff (marker exists)")
	}
}

func TestLocalConfigGuardRefusesLocalPrograms(t *testing.T) {
	g, root := newGit(t)
	marker := filepath.Join(t.TempDir(), "pwned")
	write(t, root, "a.txt", "x\n")
	gitOut(t, root, "init", "-q")
	gitOut(t, root, "config", "filter.evil.clean", "sh -c 'touch "+marker+"'")
	write(t, root, ".gitattributes", "a.txt filter=evil\n")
	gitOut(t, root, "add", ".")
	gitOut(t, root, "commit", "-qm", "init")
	// The raw git setup above runs the filter once by design; remove the
	// marker so the guarded call below is measured cleanly.
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	if _, err := g.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("Status err = %v, want refusal", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("filter executed during gitread (marker exists)")
	}
}

func TestValidRef(t *testing.T) {
	for _, ok := range []string{"main", "HEAD~1", "origin/x", "v1.2.3"} {
		if err := validRef(ok); err != nil {
			t.Fatalf("validRef(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"main..dev", "-x", "HEAD~1 --exec", ""} {
		if err := validRef(bad); err == nil {
			t.Fatalf("validRef(%q) accepted", bad)
		}
	}
}
