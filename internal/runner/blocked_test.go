package runner

import "testing"

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
