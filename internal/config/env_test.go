package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clearEnv blanks every variable Resolve reads, so the host environment
// cannot leak into a test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"NGODINGMCP_ROOT", "NGODINGMCP_ADDR", "NGODINGMCP_TOKEN",
		"NGODINGMCP_ALLOW_RUN", "NGODINGMCP_RUN_ALLOWLIST", "NGODINGMCP_RUN_ALLOWLIST_EXACT", "NGODINGMCP_RUN_TIMEOUT",
		"NGODINGMCP_ALLOW_SENSITIVE", "NGODINGMCP_ALLOW_DOWNLOAD", "NGODINGMCP_DOWNLOAD_ALLOW_HOSTS",
		"NGODINGMCP_DOWNLOAD_MAX_BYTES", "NGODINGMCP_UPLOAD_MAX_BYTES",
		"NGODINGMCP_PUBLIC_URL", "NGODINGMCP_OAUTH", "NGODINGMCP_TRUSTED_PROXIES",
		"NGODINGMCP_DISABLE_TOOLS",
	} {
		t.Setenv(k, "")
	}
}

func TestEnvBoolAndDuration(t *testing.T) {
	t.Setenv("RUNNER_TEST_BOOL", "true")
	b, err := EnvBool("RUNNER_TEST_BOOL", false)
	if err != nil || !b {
		t.Errorf("EnvBool(true) = %v, %v", b, err)
	}
	t.Setenv("RUNNER_TEST_BOOL", "not-a-bool")
	if b, err := EnvBool("RUNNER_TEST_BOOL", false); err == nil {
		t.Errorf("EnvBool(invalid) = %v, want error", b)
	}
	t.Setenv("RUNNER_TEST_BOOL", "")
	if b, err := EnvBool("RUNNER_TEST_BOOL", true); err != nil || !b {
		t.Errorf("EnvBool(unset) = %v, %v, want fallback true", b, err)
	}

	t.Setenv("RUNNER_TEST_DUR", "45s")
	d, err := EnvDuration("RUNNER_TEST_DUR", time.Minute)
	if err != nil || d != 45*time.Second {
		t.Errorf("EnvDuration = %v, %v", d, err)
	}
	t.Setenv("RUNNER_TEST_DUR", "bogus")
	if _, err := EnvDuration("RUNNER_TEST_DUR", time.Minute); err == nil {
		t.Error("EnvDuration(bogus): want error")
	}
	t.Setenv("RUNNER_TEST_DUR", "-5s")
	if _, err := EnvDuration("RUNNER_TEST_DUR", time.Minute); err == nil {
		t.Error("EnvDuration(negative): want error")
	}

	t.Setenv("RUNNER_TEST_INT", "0")
	if _, err := EnvInt64("RUNNER_TEST_INT", 1); err == nil {
		t.Error("EnvInt64(0): want error")
	}
	t.Setenv("RUNNER_TEST_INT", " 42 ")
	if n, err := EnvInt64("RUNNER_TEST_INT", 1); err != nil || n != 42 {
		t.Errorf("EnvInt64 = %d, %v", n, err)
	}
}

func TestResolveDefaults(t *testing.T) {
	clearEnv(t)
	s, err := Resolve(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Addr != DefaultAddr || s.Root != DefaultRoot() || s.Token != "" {
		t.Errorf("addr/root/token = %q %q %q", s.Addr, s.Root, s.Token)
	}
	if filepath.Base(s.Root) != "ngodingmcp-workspace" {
		t.Errorf("default root = %q", s.Root)
	}
	if !s.OAuth || s.AllowRun || s.AllowSensitive || s.AllowDownload {
		t.Errorf("toggles = %+v, want only OAuth on", s)
	}
	if s.RunTimeout != DefaultRunTimeout || s.DownloadMaxBytes != DefaultTransferMaxBytes || s.UploadMaxBytes != DefaultTransferMaxBytes {
		t.Errorf("limits = %v %d %d", s.RunTimeout, s.DownloadMaxBytes, s.UploadMaxBytes)
	}
	if s.TrustedProxies != DefaultTrustedProxies {
		t.Errorf("trusted proxies = %q", s.TrustedProxies)
	}
}

func TestResolveEnvBeatsConfig(t *testing.T) {
	clearEnv(t)
	off := false
	saved := Config{
		Root: "/cfg/root", Addr: "127.0.0.1:1", Token: "cfg",
		Run:            RunConfig{Enabled: true},
		AllowSensitive: true,
		AllowDownload:  true,
		PublicURL:      "https://cfg.example",
		OAuth:          &off,
		TrustedProxies: []string{"private"},
		DisableTools:   []string{"inspect", "git_remote"},
	}

	// config.json alone.
	s, err := Resolve(saved)
	if err != nil {
		t.Fatal(err)
	}
	if s.Root != "/cfg/root" || s.Addr != "127.0.0.1:1" || s.Token != "cfg" || !s.AllowRun || !s.AllowDownload ||
		s.OAuth || s.PublicURL != "https://cfg.example" || s.TrustedProxies != "private" || s.DisableTools != "inspect,git_remote" {
		t.Errorf("config not applied: %+v", s)
	}
	if s.AllowSensitive {
		t.Error("AllowSensitive must not come from config.json")
	}

	// The environment wins over config.json.
	t.Setenv("NGODINGMCP_ROOT", "/env/root")
	t.Setenv("NGODINGMCP_ADDR", "127.0.0.1:2")
	t.Setenv("NGODINGMCP_TOKEN", "env")
	t.Setenv("NGODINGMCP_ALLOW_RUN", "false")
	t.Setenv("NGODINGMCP_ALLOW_DOWNLOAD", "0")
	t.Setenv("NGODINGMCP_OAUTH", "true")
	t.Setenv("NGODINGMCP_PUBLIC_URL", "https://env.example")
	t.Setenv("NGODINGMCP_TRUSTED_PROXIES", "none")
	t.Setenv("NGODINGMCP_RUN_TIMEOUT", "5m")
	t.Setenv("NGODINGMCP_DISABLE_TOOLS", "git_write")
	s, err = Resolve(saved)
	if err != nil {
		t.Fatal(err)
	}
	if s.Root != "/env/root" || s.Addr != "127.0.0.1:2" || s.Token != "env" || s.AllowRun || s.AllowDownload ||
		!s.OAuth || s.PublicURL != "https://env.example" || s.TrustedProxies != "none" || s.RunTimeout != 5*time.Minute ||
		s.DisableTools != "git_write" {
		t.Errorf("env did not win: %+v", s)
	}
}

func TestResolveReportsEveryBadVariable(t *testing.T) {
	clearEnv(t)
	t.Setenv("NGODINGMCP_OAUTH", "yes")
	t.Setenv("NGODINGMCP_RUN_TIMEOUT", "30")
	t.Setenv("NGODINGMCP_UPLOAD_MAX_BYTES", "-1")
	_, err := Resolve(Config{})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, key := range []string{"NGODINGMCP_OAUTH", "NGODINGMCP_RUN_TIMEOUT", "NGODINGMCP_UPLOAD_MAX_BYTES"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not name %s", err, key)
		}
	}
}
