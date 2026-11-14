// Package service runs NgodingMCP as a systemd user service: `ngodingmcp -d`
// writes ~/.config/systemd/user/ngodingmcp.service, enables and starts it, and
// the stop, restart, status and logs subcommands manage it afterwards. No root
// or sudo is involved. Every systemctl and journalctl call goes through
// Commander, so tests never reach the real systemd.
package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ngodingmcp/internal/app"
	"ngodingmcp/internal/config"
)

const (
	// Name is the unit name without the .service suffix.
	Name = "ngodingmcp"
	// Unit is the full unit name.
	Unit = Name + ".service"
	// DefaultAddr is the listen address when neither the env file nor the
	// config sets one (same default as `ngodingmcp serve`).
	DefaultAddr = "127.0.0.1:8080"
)

var (
	// ErrUnsupported is returned off Linux, where there is no systemd.
	ErrUnsupported = errors.New("the background service needs Linux with systemd")
	// ErrNotInstalled is returned by stop, restart and logs before `-d` ran.
	ErrNotInstalled = errors.New("the background service is not set up yet; run `ngodingmcp -d` first")
)

// PortBusyError means another program listens on the service's address.
type PortBusyError struct{ Addr string }

func (e *PortBusyError) Error() string {
	return fmt.Sprintf("port %s is already in use by another program (the TUI or a manual `ngodingmcp serve`?); "+
		"stop it first, or set another NGODINGMCP_ADDR in the env file", e.Addr)
}

// Manager installs and controls the user service. Fields are exported so
// tests can point everything at temporary paths and a fake Commander.
type Manager struct {
	Cmd        Commander
	Exe        string // absolute path of the running binary
	UnitPath   string // ~/.config/systemd/user/ngodingmcp.service
	EnvPath    string // ~/.config/ngodingmcp/env
	Home       string // home directory, for the %h specifier
	LingerPath string // /var/lib/systemd/linger/<user>; empty when unknown

	// Listening reports whether something accepts TCP connections on addr.
	Listening func(addr string) bool
	// Sleep waits between readiness checks.
	Sleep func(time.Duration)
	// NewToken makes the token for a new env file when the config has none.
	NewToken func() (string, error)
	// ReadyTimeout bounds the wait for the service to come up after start.
	ReadyTimeout time.Duration
}

// New returns a Manager for the current user and binary.
func New() (*Manager, error) {
	if runtime.GOOS != "linux" {
		return nil, ErrUnsupported
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find own binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, fmt.Errorf("find home directory: %w", err)
	}
	// systemd reads user units from ~/.config/systemd/user (or
	// $XDG_CONFIG_HOME/systemd/user); the env file sits next to config.json.
	cfgDir, err := os.UserConfigDir()
	if err != nil || cfgDir == "" {
		cfgDir = filepath.Join(home, ".config")
	}
	envPath := filepath.Join(cfgDir, "ngodingmcp", "env")
	if p, err := config.Path(); err == nil {
		envPath = filepath.Join(filepath.Dir(p), "env")
	}
	m := &Manager{
		Cmd:          ExecCommander{},
		Exe:          exe,
		UnitPath:     filepath.Join(cfgDir, "systemd", "user", Unit),
		EnvPath:      envPath,
		Home:         home,
		Listening:    dialable,
		Sleep:        time.Sleep,
		NewToken:     app.NewToken,
		ReadyTimeout: 8 * time.Second,
	}
	if name := currentUser(); name != "" {
		m.LingerPath = filepath.Join("/var/lib/systemd/linger", name)
	}
	return m, nil
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

func dialable(addr string) bool {
	conn, err := net.DialTimeout("tcp", dialAddr(addr), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// dialAddr turns a wildcard listen address into one that can be dialled.
func dialAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// Addr is the address the service listens on, resolved like `serve` does it:
// NGODINGMCP_ADDR from the env file, then the config file, then the default.
func (m *Manager) Addr() string {
	env, _ := readEnvFile(m.EnvPath)
	if v := strings.TrimSpace(env["NGODINGMCP_ADDR"]); v != "" {
		return v
	}
	if v := strings.TrimSpace(config.Load().Addr); v != "" {
		return v
	}
	return DefaultAddr
}

// EnvFile is the env file the unit loads (token and other settings).
func (m *Manager) EnvFile() string { return m.EnvPath }

// UIURL is the Web UI address for a listen address.
func UIURL(addr string) string { return app.BaseURLFor(addr) + "/" }

// MCPURL is the MCP endpoint for a listen address.
func MCPURL(addr string) string { return app.BaseURLFor(addr) + "/mcp" }

func (m *Manager) systemctl(ctx context.Context, args ...string) (string, error) {
	return m.Cmd.Output(ctx, "systemctl", append([]string{"--user"}, args...)...)
}

// State is the unit's ActiveState as `systemctl --user is-active` prints it:
// active, inactive, failed, activating, deactivating... or "unknown" when
// systemctl cannot answer (not installed, no user session bus).
func (m *Manager) State(ctx context.Context) string {
	out, _ := m.systemctl(ctx, "is-active", Unit)
	// is-active exits non-zero for anything but active and still prints it.
	if s := strings.TrimSpace(out); s != "" && !strings.ContainsAny(s, " \n") {
		return s
	}
	return "unknown"
}

// Running reports whether the service is active. The TUI uses it to explain
// a busy port.
func (m *Manager) Running(ctx context.Context) bool { return m.State(ctx) == "active" }

// Installed reports whether the unit file exists.
func (m *Manager) Installed() bool {
	_, err := os.Stat(m.UnitPath)
	return err == nil
}

// UnitChange says what Install did to the unit file.
type UnitChange string

const (
	UnitCreated   UnitChange = "created"
	UnitUpdated   UnitChange = "updated"
	UnitUnchanged UnitChange = "unchanged"
)

// InstallResult reports what `-d` did.
type InstallResult struct {
	Unit       UnitChange
	OldExe     string // previous ExecStart binary when the unit was updated
	EnvCreated bool
	WasActive  bool
	Restarted  bool
	Addr       string
}

// Install writes or refreshes the unit file and the env file, then runs
// `systemctl --user daemon-reload` and `enable --now`, and waits until the
// service answers on its port. A service that was already running is
// restarted when its unit or env file changed, so it picks up the new binary
// path or token.
func (m *Manager) Install(ctx context.Context) (*InstallResult, error) {
	if strings.Contains(m.Exe, string(filepath.Separator)+"go-build") {
		return nil, errors.New("this binary was started by `go run` from a temporary folder; " +
			"build it first (go build -o ngodingmcp .) and run ./ngodingmcp -d")
	}
	res := &InstallResult{Addr: m.Addr()}
	res.WasActive = m.State(ctx) == "active"
	if !res.WasActive && m.Listening(res.Addr) {
		return res, &PortBusyError{Addr: res.Addr}
	}

	var err error
	if res.EnvCreated, err = m.ensureEnvFile(); err != nil {
		return res, err
	}
	if res.Unit, res.OldExe, err = m.writeUnit(); err != nil {
		return res, err
	}

	if _, err := m.systemctl(ctx, "daemon-reload"); err != nil {
		return res, fmt.Errorf("reload systemd user units: %w", err)
	}
	if _, err := m.systemctl(ctx, "enable", "--now", Unit); err != nil {
		return res, fmt.Errorf("enable and start the service: %w", err)
	}
	if res.WasActive && (res.Unit != UnitUnchanged || res.EnvCreated) {
		if _, err := m.systemctl(ctx, "restart", Unit); err != nil {
			return res, fmt.Errorf("restart the service: %w", err)
		}
		res.Restarted = true
	}
	return res, m.waitReady(ctx, res.Addr)
}

// waitReady polls until the unit is active and its port answers.
func (m *Manager) waitReady(ctx context.Context, addr string) error {
	const step = 250 * time.Millisecond
	tries := int(m.ReadyTimeout/step) + 1
	state := ""
	for i := 0; i < tries; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i > 0 {
			m.Sleep(step)
		}
		state = m.State(ctx)
		if state == "failed" {
			return errors.New("the service failed to start; see `ngodingmcp logs` for why")
		}
		if state == "active" && m.Listening(addr) {
			return nil
		}
	}
	if state == "active" {
		return fmt.Errorf("the service runs but %s does not answer yet; check `ngodingmcp logs`", addr)
	}
	return fmt.Errorf("the service did not start (state: %s); see `ngodingmcp logs`", state)
}

// writeUnit creates or refreshes the unit file. The existing ExecStart binary
// is returned when the path changed, for example after the binary moved.
func (m *Manager) writeUnit() (UnitChange, string, error) {
	want := renderUnit(m.Exe, envRef(m.EnvPath, m.Home))
	old, err := os.ReadFile(m.UnitPath)
	switch {
	case err == nil && string(old) == want:
		return UnitUnchanged, "", nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", "", fmt.Errorf("read %s: %w", m.UnitPath, err)
	}
	change, oldExe := UnitCreated, ""
	if err == nil {
		change = UnitUpdated
		if prev := execPathFromUnit(string(old)); prev != m.Exe {
			oldExe = prev
		}
	}
	if err := writeFileAtomic(m.UnitPath, []byte(want), 0o644); err != nil {
		return "", "", fmt.Errorf("write %s: %w", m.UnitPath, err)
	}
	return change, oldExe, nil
}

// ensureEnvFile creates the env file with the token when it is missing. The
// token is the one the TUI saved in config.json, so the control panel and the
// service share it; with none saved, a new token is made and saved to both.
// An existing file is never rewritten, only tightened to 0600.
func (m *Manager) ensureEnvFile() (bool, error) {
	if info, err := os.Lstat(m.EnvPath); err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("secure %s: not a regular file", m.EnvPath)
		}
		if info.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(m.EnvPath, 0o600); err != nil {
				return false, fmt.Errorf("secure %s: %w", m.EnvPath, err)
			}
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	token := strings.TrimSpace(config.Load().Token)
	if token == "" {
		var err error
		if token, err = m.NewToken(); err != nil {
			return false, fmt.Errorf("generate token: %w", err)
		}
		if err := config.Update(func(c *config.Config) {
			if strings.TrimSpace(c.Token) == "" {
				c.Token = token
			}
		}); err != nil {
			return false, fmt.Errorf("save token to config: %w", err)
		}
	}

	dir := filepath.Dir(m.EnvPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(m.EnvPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("create %s: %w", m.EnvPath, err)
	}
	if _, err := f.WriteString(envHeader + "\n" + envLine("NGODINGMCP_TOKEN", token)); err != nil {
		_ = f.Close()
		_ = os.Remove(m.EnvPath)
		return false, fmt.Errorf("write %s: %w", m.EnvPath, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(m.EnvPath)
		return false, fmt.Errorf("write %s: %w", m.EnvPath, err)
	}
	return true, f.Close()
}

// Stop stops the service. It stays enabled, so it starts again at the next
// login (or boot, with lingering).
func (m *Manager) Stop(ctx context.Context) error {
	if !m.Installed() {
		return ErrNotInstalled
	}
	_, err := m.systemctl(ctx, "stop", Unit)
	return err
}

// Restart restarts the service, for example after a rebuild or an edit of
// the env file, and waits until it answers again.
func (m *Manager) Restart(ctx context.Context) error {
	if !m.Installed() {
		return ErrNotInstalled
	}
	if _, err := m.systemctl(ctx, "restart", Unit); err != nil {
		return err
	}
	return m.waitReady(ctx, m.Addr())
}

// Logs shows the service journal, following new lines when follow is set
// (like `journalctl --user -u ngodingmcp -f`).
func (m *Manager) Logs(ctx context.Context, lines int, follow bool) error {
	if !m.Installed() {
		return ErrNotInstalled
	}
	if lines <= 0 {
		lines = 50
	}
	args := []string{"--user", "-u", Unit, "-n", fmt.Sprint(lines), "--no-pager"}
	if follow {
		args = append(args, "-f")
	}
	return m.Cmd.Attach(ctx, "journalctl", args...)
}

// Status is what `ngodingmcp status` shows.
type Status struct {
	Installed bool
	State     string // see Manager.State
	Enabled   string // enabled, disabled, ... or "unknown"
	Addr      string
	Listening bool
	Workspace string // the workspace the service opens (config.json root)
	UnitExe   string // binary in the unit's ExecStart
	Exe       string // this binary
	// LingerKnown is false when the linger state could not be read.
	LingerKnown bool
	Linger      bool
	// Warnings are problems worth a line each, such as a unit that points at
	// another binary.
	Warnings []string
}

// Running reports whether the service is active.
func (s *Status) Running() bool { return s.State == "active" }

// Status collects the service state without changing anything.
func (m *Manager) Status(ctx context.Context) *Status {
	st := &Status{Installed: m.Installed(), Addr: m.Addr(), Exe: m.Exe, State: "inactive", Enabled: "disabled"}
	cfg := config.Load()
	st.Workspace = cfg.Root
	if m.LingerPath != "" {
		if _, err := os.Stat(m.LingerPath); err == nil {
			st.LingerKnown, st.Linger = true, true
		} else if errors.Is(err, os.ErrNotExist) {
			st.LingerKnown = true
		}
	}
	if !st.Installed {
		return st
	}
	st.State = m.State(ctx)
	st.Enabled = "unknown"
	if out, _ := m.systemctl(ctx, "is-enabled", Unit); strings.TrimSpace(out) != "" {
		st.Enabled = strings.TrimSpace(out)
	}
	if st.Running() {
		st.Listening = m.Listening(st.Addr)
	}
	if data, err := os.ReadFile(m.UnitPath); err == nil {
		st.UnitExe = execPathFromUnit(string(data))
	}
	if st.UnitExe != "" && st.UnitExe != m.Exe {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"the service runs %s, not this binary (%s); run `%s -d` to switch it", st.UnitExe, m.Exe, m.Exe))
	}
	if st.UnitExe != "" {
		if _, err := os.Stat(st.UnitExe); err != nil {
			st.Warnings = append(st.Warnings, fmt.Sprintf("the binary in the unit (%s) is missing; run `ngodingmcp -d` again from the new location", st.UnitExe))
		}
	}
	env, _ := readEnvFile(m.EnvPath)
	if strings.TrimSpace(env["NGODINGMCP_ROOT"]) != "" {
		st.Warnings = append(st.Warnings, "NGODINGMCP_ROOT is set in "+m.EnvPath+
			"; it overrides the workspace chosen in the Web UI on every restart")
		st.Workspace = strings.TrimSpace(env["NGODINGMCP_ROOT"])
	}
	if tok, saved := strings.TrimSpace(env["NGODINGMCP_TOKEN"]), strings.TrimSpace(cfg.Token); tok != "" && saved != "" && tok != saved {
		st.Warnings = append(st.Warnings, "the token in "+m.EnvPath+
			" differs from the one saved by the control panel; the service uses the env file")
	}
	if st.Running() && !st.Listening {
		st.Warnings = append(st.Warnings, st.Addr+" does not answer; check `ngodingmcp logs`")
	}
	return st
}
