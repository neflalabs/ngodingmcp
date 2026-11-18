package main

import (
	"errors"
	"flag"
	"runtime"
	"testing"
	"time"

	"ngodingmcp/internal/config"
)

func TestParseServerFlags(t *testing.T) {
	t.Setenv("NGODINGMCP_ADDR", "")
	t.Setenv("NGODINGMCP_ROOT", "")
	t.Setenv("NGODINGMCP_TOKEN", "")

	opts, err := parseServerFlags([]string{
		"--addr", "127.0.0.1:9999",
		"--root", "/tmp/ws",
		"--token", "secret",
		"--max-file-bytes", "4096",
	})
	if err != nil {
		t.Fatalf("parseServerFlags: %v", err)
	}
	if opts.addr != "127.0.0.1:9999" {
		t.Errorf("addr = %q", opts.addr)
	}
	if opts.root != "/tmp/ws" {
		t.Errorf("root = %q", opts.root)
	}
	if opts.token != "secret" {
		t.Errorf("token = %q", opts.token)
	}
	if opts.maxFileBytes != 4096 {
		t.Errorf("maxFileBytes = %d", opts.maxFileBytes)
	}
}

func TestParseServerFlagsDefaultsFromEnv(t *testing.T) {
	t.Setenv("NGODINGMCP_ADDR", "0.0.0.0:7000")
	t.Setenv("NGODINGMCP_ROOT", "/tmp/env-root")
	t.Setenv("NGODINGMCP_TOKEN", "env-token")

	opts, err := parseServerFlags(nil)
	if err != nil {
		t.Fatalf("parseServerFlags: %v", err)
	}
	if opts.addr != "0.0.0.0:7000" || opts.root != "/tmp/env-root" || opts.token != "env-token" {
		t.Errorf("env defaults not applied: %+v", opts)
	}
}

func TestParseServerFlagsRejectsExtraArgument(t *testing.T) {
	_, err := parseServerFlags([]string{"serve"})
	if err == nil {
		t.Fatal("expected error for unexpected argument")
	}
}

func TestParseServerFlagsHelp(t *testing.T) {
	_, err := parseServerFlags([]string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
}

func TestParseServerFlagsRunOptions(t *testing.T) {
	opts, err := parseServerFlags([]string{
		"--allow-run",
		"--run-allow", "go build,go test",
		"--run-allow-exact", "bun install --frozen-lockfile,go version",
		"--run-timeout", "30s",
	})
	if err != nil {
		t.Fatalf("parseServerFlags: %v", err)
	}
	if !opts.allowRun {
		t.Error("allowRun = false, want true")
	}
	if opts.runAllow != "go build,go test" {
		t.Errorf("runAllow = %q", opts.runAllow)
	}
	if opts.runExact != "bun install --frozen-lockfile,go version" {
		t.Errorf("runExact = %q", opts.runExact)
	}
	if opts.runTimeout != 30*time.Second {
		t.Errorf("runTimeout = %v", opts.runTimeout)
	}

	got := config.SplitList(opts.runAllow)
	want := []string{"go build", "go test"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("splitList = %v, want %v", got, want)
	}

	exact := config.SplitList(opts.runExact)
	if len(exact) != 2 || exact[0] != "bun install --frozen-lockfile" {
		t.Errorf("splitList exact = %v", exact)
	}
}

func TestParseServerFlagsAllowSensitive(t *testing.T) {
	t.Setenv("NGODINGMCP_ALLOW_SENSITIVE", "")
	opts, err := parseServerFlags(nil)
	if err != nil {
		t.Fatalf("parseServerFlags: %v", err)
	}
	if opts.allowSensitive {
		t.Error("allowSensitive should default to false")
	}
	opts, err = parseServerFlags([]string{"--allow-sensitive"})
	if err != nil {
		t.Fatalf("parseServerFlags: %v", err)
	}
	if !opts.allowSensitive {
		t.Error("--allow-sensitive did not set allowSensitive")
	}
	t.Setenv("NGODINGMCP_ALLOW_SENSITIVE", "true")
	opts, err = parseServerFlags(nil)
	if err != nil {
		t.Fatalf("parseServerFlags: %v", err)
	}
	if !opts.allowSensitive {
		t.Error("NGODINGMCP_ALLOW_SENSITIVE=true did not set allowSensitive")
	}
}
func TestParseServerFlagsDownload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"NGODINGMCP_ALLOW_DOWNLOAD", "NGODINGMCP_DOWNLOAD_ALLOW_HOSTS", "NGODINGMCP_DOWNLOAD_MAX_BYTES", "NGODINGMCP_UPLOAD_MAX_BYTES"} {
		t.Setenv(k, "")
	}
	opts, err := parseServerFlags(nil)
	if err != nil {
		t.Fatalf("parseServerFlags: %v", err)
	}
	if opts.allowDownload || opts.downloadHosts != "" {
		t.Error("download_file must be off by default with no host list")
	}
	if opts.downloadMaxBytes != 20<<20 || opts.uploadMaxBytes != 20<<20 {
		t.Errorf("default limits = %d / %d, want 20 MiB", opts.downloadMaxBytes, opts.uploadMaxBytes)
	}
	opts, err = parseServerFlags([]string{"--allow-download", "--download-allow-host", "a.com,*.b.com", "--download-max-bytes", "1000", "--upload-max-bytes", "2000"})
	if err != nil {
		t.Fatalf("parseServerFlags: %v", err)
	}
	if !opts.allowDownload || opts.downloadMaxBytes != 1000 || opts.uploadMaxBytes != 2000 {
		t.Errorf("flags not applied: %+v", opts)
	}
	if hosts := config.SplitList(opts.downloadHosts); len(hosts) != 2 || hosts[1] != "*.b.com" {
		t.Errorf("hosts = %v", hosts)
	}
	if _, err := parseServerFlags([]string{"--download-max-bytes", "0"}); err == nil {
		t.Error("zero download limit should be rejected")
	}
	t.Setenv("NGODINGMCP_ALLOW_DOWNLOAD", "true")
	t.Setenv("NGODINGMCP_DOWNLOAD_MAX_BYTES", "4096")
	opts, err = parseServerFlags(nil)
	if err != nil {
		t.Fatalf("parseServerFlags: %v", err)
	}
	if !opts.allowDownload || opts.downloadMaxBytes != 4096 {
		t.Errorf("env not applied: allow=%v max=%d", opts.allowDownload, opts.downloadMaxBytes)
	}
}

func TestEnvBoolAndDuration(t *testing.T) {
	t.Setenv("RUNNER_TEST_BOOL", "true")
	b, err := envBool("RUNNER_TEST_BOOL", false)
	if err != nil || !b {
		t.Errorf("envBool(true) = %v, %v", b, err)
	}
	t.Setenv("RUNNER_TEST_BOOL", "not-a-bool")
	if b, err := envBool("RUNNER_TEST_BOOL", false); err == nil {
		t.Errorf("envBool(invalid) = %v, want error", b)
	}
	t.Setenv("RUNNER_TEST_BOOL", "")
	if b, err := envBool("RUNNER_TEST_BOOL", true); err != nil || !b {
		t.Errorf("envBool(unset) = %v, %v, want fallback true", b, err)
	}

	t.Setenv("RUNNER_TEST_DUR", "45s")
	d, err := envDuration("RUNNER_TEST_DUR", time.Minute)
	if err != nil || d != 45*time.Second {
		t.Errorf("envDuration = %v, %v", d, err)
	}
	t.Setenv("RUNNER_TEST_DUR", "bogus")
	if _, err := envDuration("RUNNER_TEST_DUR", time.Minute); err == nil {
		t.Error("envDuration(bogus): want error")
	}
	t.Setenv("RUNNER_TEST_DUR", "-5s")
	if _, err := envDuration("RUNNER_TEST_DUR", time.Minute); err == nil {
		t.Error("envDuration(negative): want error")
	}
}

func TestParseServerFlagsLoadsRunPolicyFromConfig(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NGODINGMCP_ADDR", "")
	t.Setenv("NGODINGMCP_ROOT", "")
	t.Setenv("NGODINGMCP_TOKEN", "")
	t.Setenv("NGODINGMCP_RUN_ALLOWLIST", "")
	t.Setenv("NGODINGMCP_RUN_ALLOWLIST_EXACT", "")
	t.Setenv("NGODINGMCP_RUN_TIMEOUT", "")

	saved := config.Config{
		Root:  "/tmp/from-config",
		Addr:  "127.0.0.1:6060",
		Token: "cfg-token",
		Run:   config.RunConfig{Enabled: true},
	}
	if err := config.Save(saved); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	opts, err := parseServerFlags(nil)
	if err != nil {
		t.Fatalf("parseServerFlags: %v", err)
	}
	if opts.root != saved.Root || opts.addr != saved.Addr || opts.token != saved.Token {
		t.Errorf("config not loaded: %+v", opts)
	}
	if !opts.allowRun {
		t.Error("allowRun = false, want true from config")
	}
	if opts.runAllow != "" || opts.runExact != "" {
		t.Errorf("override lists should be empty; built-in policy is used: %+v", opts)
	}
	if opts.runTimeout != time.Minute {
		t.Errorf("runTimeout = %v, want default", opts.runTimeout)
	}
}

func TestParseTUIFlags(t *testing.T) {
	root, addr, token, err := parseTUIFlags([]string{"--root", "/tmp/r", "--addr", "localhost:1", "--token", "t"})
	if err != nil {
		t.Fatalf("parseTUIFlags: %v", err)
	}
	if root != "/tmp/r" || addr != "localhost:1" || token != "t" {
		t.Errorf("unexpected values: %q %q %q", root, addr, token)
	}
}

func TestParseTUIFlagsEmptyDefaults(t *testing.T) {
	root, addr, token, err := parseTUIFlags(nil)
	if err != nil {
		t.Fatalf("parseTUIFlags: %v", err)
	}
	if root != "" || addr != "" || token != "" {
		t.Errorf("expected empty presets, got %q %q %q", root, addr, token)
	}
}

func TestParseTUIFlagsRejectsExtraArgument(t *testing.T) {
	if _, _, _, err := parseTUIFlags([]string{"extra"}); err == nil {
		t.Fatal("expected error for unexpected argument")
	}
}
