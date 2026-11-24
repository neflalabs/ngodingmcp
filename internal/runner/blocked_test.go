package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckBlockedFlags(t *testing.T) {
	blocked := [][]string{
		{"git", "diff", "--no-index", "/etc/passwd", "/etc/shadow"},
		{"go", "vet", "-vettool=./evil", "."},
		{"go", "vet", "--vettool", "./evil", "."},
		{"git", "diff", "--exec", "sh -c id"},
		{"go", "build", "-toolexec=sh -c id", "."},
		{"go", "vet", "--exec", "sh", "."},
		{"git", "log", "--output=/tmp/out"},
	}
	for _, argv := range blocked {
		if err := checkBlockedFlags(argv); err == nil {
			t.Errorf("argv %v must be blocked", argv)
		}
	}

	allowed := [][]string{
		{"go", "build", "-o", "dist/bin", "."}, // -o stays legal
		{"go", "test", "./..."},
		{"git", "diff"},
		{"git", "status"},
	}
	for _, argv := range allowed {
		if err := checkBlockedFlags(argv); err != nil {
			t.Errorf("argv %v must not be blocked: %v", argv, err)
		}
	}

	// And through the full policy check.
	prefix, exact := DefaultPolicy()
	r, err := New(Options{Root: t.TempDir(), Allowlist: prefix, ExactAllowlist: exact})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Check([]string{"git", "diff", "--no-index", "/etc/passwd", "/etc/shadow"}); err == nil {
		t.Fatal("Runner.Check accepted git diff --no-index")
	}
}

func TestCheckRejectsOutsidePaths(t *testing.T) {
	root := t.TempDir()
	prefix, exact := DefaultPolicy()
	r, err := New(Options{Root: root, Allowlist: prefix, ExactAllowlist: exact})
	if err != nil {
		t.Fatal(err)
	}

	blocked := [][]string{
		{"gofmt", "-l", "/etc/passwd"},            // would inspect a host file
		{"go", "build", "-o", "/tmp/evil", "."},   // would write outside root
		{"go", "test", "-o=../outside.test", "."}, // relative escape
		{"git", "diff", "../../etc/passwd"},
	}
	for _, argv := range blocked {
		if err := r.Check(argv); err == nil {
			t.Errorf("argv %v must be blocked", argv)
		}
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := r.Check([]string{"gofmt", "-l", "link/host.go"}); err == nil {
		t.Error("path through a symlink outside the root must be blocked")
	}

	allowed := [][]string{
		{"go", "test", "./..."},
		{"go", "build", "-o", "dist/bin", "."},
		{"gofmt", "-l", "internal"},
		{"go", "test", "-run", "TestFoo"},
	}
	for _, argv := range allowed {
		if err := r.Check(argv); err != nil {
			t.Errorf("argv %v must be allowed: %v", argv, err)
		}
	}
}
