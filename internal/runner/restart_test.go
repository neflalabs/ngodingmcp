package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDefaultPolicyAllowsBuildStaticcheckAndRestart(t *testing.T) {
	prefix, exact := DefaultPolicy()
	r, err := New(Options{Root: t.TempDir(), Allowlist: prefix, ExactAllowlist: exact})
	if err != nil {
		t.Fatal(err)
	}
	allowed := [][]string{
		{"go", "build", "-o", "ngodingmcp", "."},
		{"staticcheck", "./..."},
		{"ngodingmcp", "restart"},
	}
	for _, argv := range allowed {
		if err := r.Check(argv); err != nil {
			t.Errorf("Check(%v) = %v, want allowed", argv, err)
		}
	}
	// Only the exact restart command; no other service subcommand and no
	// extra arguments.
	denied := [][]string{
		{"ngodingmcp", "stop"},
		{"ngodingmcp", "restart", "--now"},
		{"ngodingmcp", "-d"},
		{"ngodingmcp"},
		{"staticcheck", "-debug.cpuprofile=/tmp/x", "./..."},
		{"staticcheck", "--debug.trace", "/tmp/x", "./..."},
	}
	for _, argv := range denied {
		if err := r.Check(argv); err == nil {
			t.Errorf("Check(%v) allowed, want denied", argv)
		}
	}
}

func TestRunRestartUsesHookNotProgram(t *testing.T) {
	calls := 0
	r, err := New(Options{
		Root:           t.TempDir(),
		ExactAllowlist: []string{RestartCommand},
		Restart: func() (string, error) {
			calls++
			return "restart scheduled", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []string{"ngodingmcp", "restart"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || res.ExitCode != 0 || !strings.Contains(res.Output, "scheduled") {
		t.Fatalf("calls=%d result=%+v", calls, res)
	}
}

func TestRunRestartErrors(t *testing.T) {
	r, err := New(Options{Root: t.TempDir(), ExactAllowlist: []string{RestartCommand}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []string{"ngodingmcp", "restart"}); !errors.Is(err, ErrRestartUnavailable) {
		t.Fatalf("no hook: err = %v, want ErrRestartUnavailable", err)
	}

	hookErr := errors.New("not a service")
	r, err = New(Options{
		Root:           t.TempDir(),
		ExactAllowlist: []string{RestartCommand},
		Restart:        func() (string, error) { return "", hookErr },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []string{"ngodingmcp", "restart"}); !errors.Is(err, hookErr) {
		t.Fatalf("hook error: err = %v", err)
	}
}

func TestRestartNeedsAllowlistEntry(t *testing.T) {
	called := false
	r, err := New(Options{
		Root:      t.TempDir(),
		Allowlist: []string{"go test"},
		Restart:   func() (string, error) { called = true; return "", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []string{"ngodingmcp", "restart"}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
	if called {
		t.Fatal("restart hook ran without an allowlist entry")
	}
}
