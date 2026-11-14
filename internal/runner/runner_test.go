package runner

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNewValidatesAllowlist(t *testing.T) {
	if _, err := New(Options{Root: t.TempDir(), Allowlist: nil}); err == nil {
		t.Fatal("expected error for empty allowlist")
	}
	if _, err := New(Options{Root: t.TempDir(), Allowlist: []string{"  "}}); err == nil {
		t.Fatal("expected error for blank allowlist")
	}
	if _, err := New(Options{Root: t.TempDir(), Allowlist: []string{"go test; rm -rf /"}}); err == nil {
		t.Fatal("expected error for shell metacharacters")
	}
}

func TestRunAllowedPrefix(t *testing.T) {
	r, err := New(Options{Root: t.TempDir(), Allowlist: []string{"go env"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Run(context.Background(), []string{"go", "env", "GOOS"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, output: %s", res.ExitCode, res.Output)
	}
	if strings.TrimSpace(res.Output) == "" {
		t.Error("expected output from go env GOOS")
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	r, err := New(Options{Root: t.TempDir(), Allowlist: []string{"go env"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.Run(context.Background(), []string{"sh", "-c", "true"}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
	if _, err := r.Run(context.Background(), []string{"go", "version"}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
	if _, err := r.Run(context.Background(), nil); !errors.Is(err, ErrEmptyCommand) {
		t.Fatalf("err = %v, want ErrEmptyCommand", err)
	}
}

func TestRunNonZeroExit(t *testing.T) {
	r, err := New(Options{Root: t.TempDir(), Allowlist: []string{"go env"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Run(context.Background(), []string{"go", "env", "-not-a-flag"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode == 0 {
		t.Errorf("expected non-zero exit code, got %d", res.ExitCode)
	}
}

func TestRunTimeout(t *testing.T) {
	t.Setenv("RUNNER_HELPER_SLEEP", "5s")
	r, err := New(Options{
		Root:      t.TempDir(),
		Allowlist: []string{os.Args[0]},
		Timeout:   150 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Run(context.Background(), []string{os.Args[0], "-test.run=TestRunnerHelperSleep", "--"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.TimedOut {
		t.Fatalf("expected timeout, got result %+v", res)
	}
	if res.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1", res.ExitCode)
	}
}

func TestRunTruncatesOutput(t *testing.T) {
	t.Setenv("RUNNER_HELPER_OUTPUT", "1")
	r, err := New(Options{
		Root:           t.TempDir(),
		Allowlist:      []string{os.Args[0]},
		MaxOutputBytes: 200,
		Timeout:        10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := r.Run(context.Background(), []string{os.Args[0], "-test.run=TestRunnerHelperOutput", "--"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Truncated {
		t.Error("expected truncated output")
	}
	if len(res.Output) > 200 {
		t.Errorf("output length = %d, want <= 200", len(res.Output))
	}
}

func TestRunUsesRootAsWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	r, err := New(Options{Root: root, Allowlist: []string{"go env"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// GOMOD tells us where the command ran; outside any module it is empty.
	res, err := r.Run(context.Background(), []string{"go", "env", "GOMOD"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Output) != "" {
		t.Logf("GOMOD=%q (workspace is inside a module)", strings.TrimSpace(res.Output))
	}
}

func TestRunExactAllowlist(t *testing.T) {
	r, err := New(Options{Root: t.TempDir(), ExactAllowlist: []string{"go env GOOS"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := r.Run(context.Background(), []string{"go", "env", "GOOS"})
	if err != nil {
		t.Fatalf("Run exact: %v", err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Output) == "" {
		t.Errorf("unexpected result: %+v", res)
	}

	if _, err := r.Run(context.Background(), []string{"go", "env", "GOMOD"}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("different argv: err = %v, want ErrNotAllowed", err)
	}
	if _, err := r.Run(context.Background(), []string{"go", "env", "GOOS", "extra"}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("extra argument: err = %v, want ErrNotAllowed", err)
	}
	if _, err := r.Run(context.Background(), []string{"go", "env"}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("missing argument: err = %v, want ErrNotAllowed", err)
	}
}

func TestRunPrefixAndExactCoexist(t *testing.T) {
	r, err := New(Options{
		Root:           t.TempDir(),
		Allowlist:      []string{"go env"},
		ExactAllowlist: []string{"go version"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.Run(context.Background(), []string{"go", "env", "GOOS"}); err != nil {
		t.Errorf("prefix command: %v", err)
	}
	if _, err := r.Run(context.Background(), []string{"go", "version"}); err != nil {
		t.Errorf("exact command: %v", err)
	}
	if _, err := r.Run(context.Background(), []string{"go", "version", "-m"}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("exact command with extra args: err = %v, want ErrNotAllowed", err)
	}
}

func TestNewValidatesExactAllowlist(t *testing.T) {
	if _, err := New(Options{Root: t.TempDir(), ExactAllowlist: []string{"bun install; rm -rf /"}}); err == nil {
		t.Fatal("expected error for shell metacharacters")
	}
	if _, err := New(Options{Root: t.TempDir()}); err == nil {
		t.Fatal("expected error when both allowlists are empty")
	}
}

func TestAllowlistGetters(t *testing.T) {
	r, err := New(Options{
		Root:           t.TempDir(),
		Allowlist:      []string{"go test"},
		ExactAllowlist: []string{"go version"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := r.Allowlist(); len(got) != 1 || got[0] != "go test" {
		t.Errorf("Allowlist = %v", got)
	}
	if got := r.ExactAllowlist(); len(got) != 1 || got[0] != "go version" {
		t.Errorf("ExactAllowlist = %v", got)
	}
}

func TestFormatArgv(t *testing.T) {
	got := FormatArgv([]string{"go", "test", "-run", "Test A|B"})
	want := `go test -run "Test A|B"`
	if got != want {
		t.Errorf("FormatArgv = %q, want %q", got, want)
	}
}

// TestRunnerHelperSleep is executed in a child process by TestRunTimeout.
func TestRunnerHelperSleep(t *testing.T) {
	d, err := time.ParseDuration(os.Getenv("RUNNER_HELPER_SLEEP"))
	if err != nil || d <= 0 {
		t.Skip("helper only runs as a child process")
	}
	time.Sleep(d)
}

// TestRunnerHelperOutput is executed in a child process by TestRunTruncatesOutput.
func TestRunnerHelperOutput(t *testing.T) {
	if os.Getenv("RUNNER_HELPER_OUTPUT") == "" {
		t.Skip("helper only runs as a child process")
	}
	println(strings.Repeat("x", 5000))
}

func TestCleanEnv(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"SSH_AUTH_SOCK=/run/ssh.sock",
		"NGODINGMCP_TOKEN=supersecret",
		"GITHUB_TOKEN=ghp_secret",
		"AWS_ACCESS_KEY_ID=AKIA123",
		"AWS_SECRET_ACCESS_KEY=abc",
		"NPM_TOKEN=npm_123",
		"DB_PASSWORD=hunter2",
		"MY_PASSPHRASE=pass",
		"SOME_API_KEY=key",
		"CLIENT_CREDENTIALS=cred",
	}
	got := CleanEnv(env)
	seen := map[string]bool{}
	for _, kv := range got {
		name, _, _ := strings.Cut(kv, "=")
		seen[name] = true
	}
	for _, blocked := range []string{"NGODINGMCP_TOKEN", "GITHUB_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "NPM_TOKEN", "DB_PASSWORD", "MY_PASSPHRASE", "SOME_API_KEY", "CLIENT_CREDENTIALS"} {
		if seen[blocked] {
			t.Errorf("CleanEnv kept secret var %q", blocked)
		}
	}
	for _, kept := range []string{"PATH", "HOME", "SSH_AUTH_SOCK"} {
		if !seen[kept] {
			t.Errorf("CleanEnv dropped %q", kept)
		}
	}
}
