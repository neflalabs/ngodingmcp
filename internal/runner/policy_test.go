package runner

import (
	"context"
	"errors"
	"testing"
)

func TestDefaultPolicy(t *testing.T) {
	prefix, exact := DefaultPolicy()
	if len(prefix) == 0 || len(exact) == 0 {
		t.Fatalf("default policy is empty: %v %v", prefix, exact)
	}
	if !contains(prefix, "go test") {
		t.Errorf("default prefixes missing go test: %v", prefix)
	}
	if !contains(exact, "bun install --frozen-lockfile") {
		t.Errorf("default exact missing bun install: %v", exact)
	}

	// Mutating the returned slice must not change later calls.
	prefix[0] = "rm -rf /"
	again, _ := DefaultPolicy()
	if again[0] == "rm -rf /" {
		t.Error("DefaultPolicy returned a shared slice")
	}
}

func TestDefaultPolicyRejectsUnlistedCommands(t *testing.T) {
	prefix, exact := DefaultPolicy()
	r, err := New(Options{Root: t.TempDir(), Allowlist: prefix, ExactAllowlist: exact})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	denied := [][]string{
		{"sh", "-c", "echo pwned"},
		{"go", "run", "."},
		{"bun", "install"}, // exact entry requires --frozen-lockfile
		{"bun", "install", "evil-pkg"},
		{"curl", "http://example.com"},
	}
	for _, argv := range denied {
		if _, err := r.Run(context.Background(), argv); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%v: err = %v, want ErrNotAllowed", argv, err)
		}
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
