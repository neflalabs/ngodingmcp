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
	if !contains(prefix, "node") {
		t.Errorf("default prefixes missing node: %v", prefix)
	}
	if !contains(exact, "bun install --frozen-lockfile") {
		t.Errorf("default exact missing bun install: %v", exact)
	}
	for _, want := range []string{"bun init -y", "bun install", "npm init -y", "npm install", "npm ci", "npm test", "npm run build", "npm run dev"} {
		if !contains(exact, want) {
			t.Errorf("default exact missing %q: %v", want, exact)
		}
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
		{"bun", "install", "evil-pkg"}, // exact entries only
		{"curl", "http://example.com"},
	}
	for _, argv := range denied {
		if _, err := r.Run(context.Background(), argv); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%v: err = %v, want ErrNotAllowed", argv, err)
		}
	}
}

func TestDefaultPolicyJsCommands(t *testing.T) {
	prefix, exact := DefaultPolicy()
	r, err := New(Options{Root: t.TempDir(), Allowlist: prefix, ExactAllowlist: exact})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	allowed := [][]string{
		{"node", "script.js"},
		{"node", "--version"},
		{"bun", "init", "-y"},
		{"bun", "install"},
		{"bun", "install", "--frozen-lockfile"},
		{"npm", "init", "-y"},
		{"npm", "install"},
		{"npm", "ci"},
		{"npm", "test"},
		{"npm", "run", "build"},
		{"npm", "run", "dev"},
	}
	for _, argv := range allowed {
		if err := r.Check(argv); err != nil {
			t.Errorf("%v: err = %v, want allowed", argv, err)
		}
	}
	denied := [][]string{
		{"bun", "add", "left-pad"}, // exact match only
		{"bunx", "create-react-app"},
		{"npm", "install", "-g", "evil"}, // exact match only
		{"npm", "install", "left-pad"},
		{"npm", "install", "--prefix", "/tmp"},
		{"npx", "create-react-app"},
	}
	for _, argv := range denied {
		if err := r.Check(argv); err == nil {
			t.Errorf("%v: err = nil, want refusal", argv)
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
