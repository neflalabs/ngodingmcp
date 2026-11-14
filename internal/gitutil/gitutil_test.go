package gitutil

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
}

func initRepo(t *testing.T) string {
	t.Helper()
	isolate(t)
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return root
}

func TestCheckLocalConfigRefusalInHostileRepo(t *testing.T) {
	root := initRepo(t)
	cmd := exec.Command("git", "config", "filter.evil.process", "sh -c 'touch /tmp/pwned'")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	err := CheckLocalConfig(context.Background(), root)
	if err == nil {
		t.Fatal("hostile repo accepted")
	}
	if !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "filter.evil.process") {
		t.Fatalf("err does not name the offending key: %v", err)
	}
}

func TestCheckLocalConfigCleanRepo(t *testing.T) {
	root := initRepo(t)
	if err := CheckLocalConfig(context.Background(), root); err != nil {
		t.Fatalf("clean repo refused: %v", err)
	}
}

func TestCheckLocalConfigNotARepo(t *testing.T) {
	isolate(t)
	// Init flow calls git before a repository exists; the guard must not
	// block it.
	if err := CheckLocalConfig(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("non-repo refused: %v", err)
	}
}

func TestSanitizeEnvDropsSteering(t *testing.T) {
	got := SanitizeEnv([]string{
		"PATH=/usr/bin",
		"GIT_DIR=/elsewhere",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.sshCommand",
		"GIT_CONFIG_VALUE_0=ssh evil",
		"GIT_CONFIG_PARAMETERS='core.fsmonitor=sh evil'",
		"GIT_SSH_COMMAND=ssh evil",
		"GIT_EXTERNAL_DIFF=/tmp/evil",
		"HOME=/home/u",
	})
	for _, kv := range got {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_CONFIG") || strings.HasPrefix(name, "GIT_DIR") ||
			name == "GIT_SSH_COMMAND" || name == "GIT_EXTERNAL_DIFF" {
			t.Fatalf("kept steering var %q", kv)
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want PATH and HOME only", got)
	}
}

func TestValidRef(t *testing.T) {
	for _, bad := range []string{"main..dev", "HEAD...x", "-f", "a b", ""} {
		if err := ValidRef(bad); err == nil {
			t.Fatalf("ValidRef(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"main", "HEAD~1", "origin/feature-x", "v1.2.3", "release/2026.11"} {
		if err := ValidRef(ok); err != nil {
			t.Fatalf("ValidRef(%q) = %v", ok, err)
		}
	}
}
