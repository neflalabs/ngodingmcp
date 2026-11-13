package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ngodingmcp/internal/config"
)

// fakeSystemd stands in for systemctl and journalctl. It records every call
// and keeps a tiny state machine: enable --now and restart make the unit
// active, stop makes it inactive.
type fakeSystemd struct {
	calls    []string
	active   bool
	enabled  bool
	failNext map[string]error // first word of the systemctl verb -> error
	startsAs string           // state after start; default "active"
}

func (f *fakeSystemd) Output(_ context.Context, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	if name != "systemctl" || len(args) < 2 || args[0] != "--user" {
		return "", errors.New("unexpected command: " + line)
	}
	verb := args[1]
	if err := f.failNext[verb]; err != nil {
		delete(f.failNext, verb)
		return "", err
	}
	switch verb {
	case "is-active":
		if f.active {
			return f.state() + "\n", nil
		}
		return "inactive\n", errors.New("exit status 3")
	case "is-enabled":
		if f.enabled {
			return "enabled\n", nil
		}
		return "disabled\n", errors.New("exit status 1")
	case "daemon-reload":
	case "enable":
		f.enabled = true
		if len(args) > 2 && args[2] == "--now" {
			f.active = true
		}
	case "restart":
		f.active = true
	case "stop":
		f.active = false
	default:
		return "", errors.New("unexpected verb: " + line)
	}
	return "", nil
}

func (f *fakeSystemd) state() string {
	if f.startsAs != "" {
		return f.startsAs
	}
	return "active"
}

func (f *fakeSystemd) Attach(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, "attach "+name+" "+strings.Join(args, " "))
	return nil
}

func (f *fakeSystemd) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

type harness struct {
	m      *Manager
	sd     *fakeSystemd
	home   string
	listen map[string]bool // addresses that answer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	exe := filepath.Join(home, "bin", "ngodingmcp")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &harness{sd: &fakeSystemd{}, home: home, listen: map[string]bool{}}
	h.m = &Manager{
		Cmd:          h.sd,
		Exe:          exe,
		UnitPath:     filepath.Join(cfgDir, "systemd", "user", Unit),
		EnvPath:      filepath.Join(cfgDir, "ngodingmcp", "env"),
		Home:         home,
		LingerPath:   filepath.Join(home, "linger", "nefla"),
		Sleep:        func(time.Duration) {},
		NewToken:     func() (string, error) { return "generated-token", nil },
		ReadyTimeout: time.Second,
	}
	// The port answers only while the fake unit is active, unless the test
	// marks it as taken by another program.
	h.m.Listening = func(addr string) bool { return h.listen[addr] || h.sd.active }
	return h
}

func (h *harness) unit(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(h.m.UnitPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInstallWritesUnitAndEnvAndStarts(t *testing.T) {
	h := newHarness(t)
	res, err := h.m.Install(context.Background())
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Unit != UnitCreated || !res.EnvCreated || res.WasActive || res.Restarted {
		t.Errorf("result = %+v", res)
	}
	if res.Addr != DefaultAddr {
		t.Errorf("addr = %q, want default", res.Addr)
	}

	unit := h.unit(t)
	for _, want := range []string{
		`ExecStart="` + h.m.Exe + `" serve`,
		"EnvironmentFile=-%h/.config/ngodingmcp/env",
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit misses %q:\n%s", want, unit)
		}
	}

	// The order matters: reload before enable, enable with --now.
	want := []string{
		"systemctl --user is-active ngodingmcp.service",
		"systemctl --user daemon-reload",
		"systemctl --user enable --now ngodingmcp.service",
	}
	for i, w := range want {
		if i >= len(h.sd.calls) || h.sd.calls[i] != w {
			t.Fatalf("calls = %q, want prefix %q", h.sd.calls, want)
		}
	}
	if h.sd.called("systemctl --user restart") {
		t.Error("a fresh install must not restart")
	}

	// The env file holds the token, owner-only, and the token is saved to
	// config.json so the TUI uses the same one.
	info, err := os.Stat(h.m.EnvPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("env file mode = %v, want 0600", info.Mode().Perm())
	}
	env, _ := readEnvFile(h.m.EnvPath)
	if env["NGODINGMCP_TOKEN"] != "generated-token" {
		t.Errorf("env token = %q", env["NGODINGMCP_TOKEN"])
	}
	if _, ok := env["NGODINGMCP_ROOT"]; ok {
		t.Error("env file must not set NGODINGMCP_ROOT")
	}
	if got := config.Load().Token; got != "generated-token" {
		t.Errorf("config token = %q", got)
	}
}

func TestInstallReusesTokenFromConfig(t *testing.T) {
	h := newHarness(t)
	if err := config.Save(config.Config{Token: `tui "token" with space`, Addr: "127.0.0.1:9123"}); err != nil {
		t.Fatal(err)
	}
	res, err := h.m.Install(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Addr != "127.0.0.1:9123" {
		t.Errorf("addr = %q, want the config address", res.Addr)
	}
	env, _ := readEnvFile(h.m.EnvPath)
	if env["NGODINGMCP_TOKEN"] != `tui "token" with space` {
		t.Errorf("env token = %q, want the config token round-tripped", env["NGODINGMCP_TOKEN"])
	}
}

func TestInstallAgainIsIdempotent(t *testing.T) {
	h := newHarness(t)
	if _, err := h.m.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.m.EnvPath, []byte("NGODINGMCP_TOKEN=mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.sd.calls = nil
	res, err := h.m.Install(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Unit != UnitUnchanged || res.EnvCreated || !res.WasActive || res.Restarted {
		t.Errorf("result = %+v", res)
	}
	if h.sd.called("systemctl --user restart") {
		t.Error("nothing changed, so no restart")
	}
	// An edited env file is kept, only tightened.
	env, _ := readEnvFile(h.m.EnvPath)
	if env["NGODINGMCP_TOKEN"] != "mine" {
		t.Errorf("env file was rewritten: %v", env)
	}
	if info, _ := os.Stat(h.m.EnvPath); info.Mode().Perm() != 0o600 {
		t.Errorf("env mode = %v, want 0600", info.Mode().Perm())
	}
}

// Moving the binary and running -d again points the unit at the new path and
// restarts the running service.
func TestInstallAfterBinaryMovedUpdatesUnit(t *testing.T) {
	h := newHarness(t)
	if _, err := h.m.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := h.m.Exe
	h.m.Exe = filepath.Join(h.home, "apps", "with space", "ngodingmcp")
	h.sd.calls = nil
	res, err := h.m.Install(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Unit != UnitUpdated || res.OldExe != old || !res.Restarted {
		t.Errorf("result = %+v", res)
	}
	if got := execPathFromUnit(h.unit(t)); got != h.m.Exe {
		t.Errorf("unit ExecStart path = %q, want %q", got, h.m.Exe)
	}
	if !h.sd.called("systemctl --user daemon-reload") || !h.sd.called("systemctl --user restart ngodingmcp.service") {
		t.Errorf("calls = %q", h.sd.calls)
	}
}

func TestInstallRefusesBusyPort(t *testing.T) {
	h := newHarness(t)
	h.listen[DefaultAddr] = true // the TUI or a manual serve holds the port
	_, err := h.m.Install(context.Background())
	var busy *PortBusyError
	if !errors.As(err, &busy) || busy.Addr != DefaultAddr {
		t.Fatalf("err = %v, want PortBusyError", err)
	}
	if h.m.Installed() || h.sd.called("systemctl --user enable") {
		t.Error("nothing must be written or started when the port is busy")
	}
}

func TestInstallRefusesGoRunBinary(t *testing.T) {
	h := newHarness(t)
	h.m.Exe = "/tmp/go-build123/b001/exe/ngodingmcp"
	if _, err := h.m.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "go run") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallReportsFailedStart(t *testing.T) {
	h := newHarness(t)
	h.sd.startsAs = "failed"
	_, err := h.m.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ngodingmcp logs") {
		t.Fatalf("err = %v, want a pointer to the logs", err)
	}
}

func TestInstallSurfacesSystemctlError(t *testing.T) {
	h := newHarness(t)
	h.sd.failNext = map[string]error{"daemon-reload": errors.New("Failed to connect to bus: No medium found")}
	_, err := h.m.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("err = %v", err)
	}
}

func TestStopRestartLogs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.m.Stop(ctx); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Stop before install = %v", err)
	}
	if err := h.m.Logs(ctx, 0, true); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Logs before install = %v", err)
	}
	if _, err := h.m.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.m.Stop(ctx); err != nil || h.sd.active {
		t.Fatalf("Stop: %v active=%v", err, h.sd.active)
	}
	if err := h.m.Restart(ctx); err != nil || !h.sd.active {
		t.Fatalf("Restart: %v active=%v", err, h.sd.active)
	}
	if err := h.m.Logs(ctx, 0, true); err != nil {
		t.Fatal(err)
	}
	if !h.sd.called("attach journalctl --user -u ngodingmcp.service -n 50 --no-pager -f") {
		t.Errorf("calls = %q", h.sd.calls)
	}
}

func TestStatus(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	st := h.m.Status(ctx)
	if st.Installed || st.Running() || !st.LingerKnown || st.Linger {
		t.Errorf("before install: %+v", st)
	}

	if err := config.Save(config.Config{Root: "/home/nefla/sekolah", Token: "tui"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.Install(ctx); err != nil {
		t.Fatal(err)
	}
	st = h.m.Status(ctx)
	if !st.Installed || !st.Running() || !st.Listening || st.Enabled != "enabled" {
		t.Errorf("after install: %+v", st)
	}
	if st.Workspace != "/home/nefla/sekolah" || st.UnitExe != h.m.Exe || len(st.Warnings) != 0 {
		t.Errorf("after install: %+v", st)
	}

	// Lingering on, a different binary, NGODINGMCP_ROOT and a diverging token
	// each get noticed.
	if err := os.MkdirAll(filepath.Dir(h.m.LingerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.m.LingerPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.m.EnvPath, []byte("NGODINGMCP_TOKEN=other\nNGODINGMCP_ROOT=/srv/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.m.Exe = filepath.Join(h.home, "elsewhere", "ngodingmcp")
	st = h.m.Status(ctx)
	if !st.Linger {
		t.Error("linger file not detected")
	}
	joined := strings.Join(st.Warnings, "\n")
	for _, want := range []string{"not this binary", "NGODINGMCP_ROOT", "token"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings miss %q: %q", want, st.Warnings)
		}
	}
	if st.Workspace != "/srv/x" {
		t.Errorf("workspace = %q, want the env override", st.Workspace)
	}

	if err := h.m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if st = h.m.Status(ctx); st.Running() || st.State != "inactive" {
		t.Errorf("after stop: %+v", st)
	}
}

func TestStateWithoutSystemctl(t *testing.T) {
	h := newHarness(t)
	h.sd.failNext = map[string]error{"is-active": errors.New("exec: \"systemctl\": executable file not found in $PATH")}
	if got := h.m.State(context.Background()); got != "unknown" {
		t.Errorf("State = %q, want unknown", got)
	}
}

func TestEnvRefAndQuoting(t *testing.T) {
	if got := envRef("/home/a/.config/ngodingmcp/env", "/home/a"); got != "%h/.config/ngodingmcp/env" {
		t.Errorf("envRef in home = %q", got)
	}
	if got := envRef("/etc/x/env", "/home/a"); got != "/etc/x/env" {
		t.Errorf("envRef outside home = %q", got)
	}
	exe := `/opt/my "odd" 100%$dir/ngodingmcp`
	unit := renderUnit(exe, "/opt/50%/env")
	if !strings.Contains(unit, `ExecStart="/opt/my \"odd\" 100%%$$dir/ngodingmcp" serve`) {
		t.Errorf("ExecStart not escaped:\n%s", unit)
	}
	if !strings.Contains(unit, "EnvironmentFile=-/opt/50%%/env") {
		t.Errorf("EnvironmentFile not escaped:\n%s", unit)
	}
	if got := execPathFromUnit(unit); got != exe {
		t.Errorf("execPathFromUnit = %q, want %q", got, exe)
	}
	if got := dialAddr("0.0.0.0:8080"); got != "127.0.0.1:8080" {
		t.Errorf("dialAddr = %q", got)
	}
	if got := UIURL("127.0.0.1:8080"); got != "http://127.0.0.1:8080/" {
		t.Errorf("UIURL = %q", got)
	}
}
