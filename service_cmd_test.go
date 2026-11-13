package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"ngodingmcp/internal/service"
)

type fakeManager struct {
	res      *service.InstallResult
	err      error
	status   service.Status
	stopped  bool
	logLines int
	follow   bool
}

func (f *fakeManager) Install(context.Context) (*service.InstallResult, error) {
	return f.res, f.err
}

func (f *fakeManager) Stop(context.Context) error {
	f.stopped = true
	return f.err
}

func (f *fakeManager) Restart(context.Context) error { return f.err }

func (f *fakeManager) Logs(_ context.Context, n int, follow bool) error {
	f.logLines, f.follow = n, follow
	return f.err
}

func (f *fakeManager) Status(context.Context) *service.Status {
	s := f.status
	return &s
}

func (f *fakeManager) Addr() string { return "127.0.0.1:8080" }

func (f *fakeManager) EnvFile() string { return "/home/nefla/.config/ngodingmcp/env" }

func useFakeManager(t *testing.T, f *fakeManager) {
	t.Helper()
	prev := newServiceManager
	newServiceManager = func() (serviceManager, error) { return f, nil }
	t.Cleanup(func() { newServiceManager = prev })
}

func runSvc(cmd string, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := runServiceCommand(cmd, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestDaemonFlagPrintsWebUIAndLingerHint(t *testing.T) {
	f := &fakeManager{
		res:    &service.InstallResult{Unit: service.UnitCreated, EnvCreated: true, Addr: "127.0.0.1:8080"},
		status: service.Status{Installed: true, State: "active", Workspace: "/home/nefla/ngodingmcp", LingerKnown: true},
	}
	useFakeManager(t, f)
	code, out, errOut := runSvc("-d")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	for _, want := range []string{
		"Wrote the systemd user unit",
		"Created the env file",
		"started and enabled",
		"web UI:    http://127.0.0.1:8080/",
		"MCP:       http://127.0.0.1:8080/mcp",
		"workspace: /home/nefla/ngodingmcp",
		"NGODINGMCP_TOKEN in /home/nefla/.config/ngodingmcp/env",
		"loginctl enable-linger",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}

	// With lingering on, no hint; a moved binary is reported.
	f.status.Linger = true
	f.res = &service.InstallResult{Unit: service.UnitUpdated, OldExe: "/old/ngodingmcp", WasActive: true, Restarted: true, Addr: "127.0.0.1:8080"}
	_, out, _ = runSvc("-d")
	if strings.Contains(out, "enable-linger") || !strings.Contains(out, "binary moved from /old/ngodingmcp") ||
		!strings.Contains(out, "restarted") {
		t.Errorf("second -d output:\n%s", out)
	}
}

func TestDaemonFlagError(t *testing.T) {
	useFakeManager(t, &fakeManager{err: &service.PortBusyError{Addr: "127.0.0.1:8080"}})
	code, _, errOut := runSvc("-d")
	if code != 1 || !strings.Contains(errOut, "port 127.0.0.1:8080 is already in use") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}

func TestStatusCommand(t *testing.T) {
	f := &fakeManager{status: service.Status{}}
	useFakeManager(t, f)
	if code, out, _ := runSvc("status"); code != 3 || !strings.Contains(out, "not set up") {
		t.Errorf("not installed: code=%d out=%q", code, out)
	}

	f.status = service.Status{Installed: true, State: "active", Enabled: "enabled", Addr: "127.0.0.1:8080",
		Listening: true, UnitExe: "/home/nefla/bin/ngodingmcp", LingerKnown: true, Linger: true,
		Warnings: []string{"something to fix"}}
	code, out, _ := runSvc("status")
	if code != 0 {
		t.Errorf("running: code=%d", code)
	}
	for _, want := range []string{"service: running (enabled)", "web UI:    http://127.0.0.1:8080/", "linger:    on", "warning:   something to fix"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output misses %q:\n%s", want, out)
		}
	}

	f.status.State = "failed"
	if code, out, _ := runSvc("status"); code != 3 || !strings.Contains(out, "service: failed") || !strings.Contains(out, "ngodingmcp logs") {
		t.Errorf("failed: code=%d out=%q", code, out)
	}
}

func TestStopAndLogsCommands(t *testing.T) {
	f := &fakeManager{}
	useFakeManager(t, f)
	if code, out, _ := runSvc("stop"); code != 0 || !f.stopped || !strings.Contains(out, "stopped") {
		t.Errorf("stop: code=%d out=%q", code, out)
	}
	if code, _, _ := runSvc("logs"); code != 0 || f.logLines != 50 || !f.follow {
		t.Errorf("logs: code=%d lines=%d follow=%v", code, f.logLines, f.follow)
	}
	if code, _, _ := runSvc("logs", "-n", "200", "--no-follow"); code != 0 || f.logLines != 200 || f.follow {
		t.Errorf("logs flags: code=%d lines=%d follow=%v", code, f.logLines, f.follow)
	}
	if code, _, errOut := runSvc("stop", "now"); code != 2 || !strings.Contains(errOut, "takes no arguments") {
		t.Errorf("stop with arg: code=%d err=%q", code, errOut)
	}

	f.err = service.ErrNotInstalled
	if code, _, errOut := runSvc("stop"); code != 1 || !strings.Contains(errOut, "ngodingmcp -d") {
		t.Errorf("stop not installed: code=%d err=%q", code, errOut)
	}
}

func TestServiceCommandUnsupported(t *testing.T) {
	prev := newServiceManager
	newServiceManager = func() (serviceManager, error) { return nil, service.ErrUnsupported }
	t.Cleanup(func() { newServiceManager = prev })
	code, _, errOut := runSvc("status")
	if code != 1 || !strings.Contains(errOut, "systemd") {
		t.Errorf("code=%d err=%q", code, errOut)
	}
}
