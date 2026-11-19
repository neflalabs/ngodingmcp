// Package tui implements an interactive control panel for the server.
package tui

import (
	"context"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbletea"
	"ngodingmcp/internal/app"
	"ngodingmcp/internal/buildinfo"
	"ngodingmcp/internal/config"
	"ngodingmcp/internal/procman"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	fieldRoot = iota
	fieldAddr
	fieldToken
	fieldRunEnabled
	fieldAllowSensitive
	fieldAllowDownload
	fieldCount
)

var labels = [fieldCount]string{
	"Workspace root",
	"Listen address",
	"Auth token (empty = generate)",
	"Enable run_command & processes (space to toggle)",
	"Allow sensitive files: .env, keys, .git (space to toggle)",
	"Enable download_file: https, public hosts, max 20 MiB (space to toggle)",
}

// isToggle reports whether a field is a checkbox rather than a text input.
func isToggle(i int) bool {
	return i == fieldRunEnabled || i == fieldAllowSensitive || i == fieldAllowDownload
}

type tickMsg struct{}

// Model is the Bubble Tea model for the control panel.
type Model struct {
	version string
	built   string // build time of the binary for the header; empty when unknown
	inputs  [fieldCount]textinput.Model
	focus   int

	runEnabled     bool
	allowSensitive bool
	allowDownload  bool

	// settings holds what config.Resolve read from the environment and
	// config.json (the same source `serve` uses); configErr is its error.
	settings  config.Settings
	configErr error

	srv     *app.Server
	running bool

	showToken bool

	logs *LogBuffer

	statusMsg string
	errMsg    string

	// serviceRunning reports whether the systemd user service is active; it
	// only shapes the message shown when the listen port is taken. Nil means
	// unknown (tests, non-systemd systems).
	serviceRunning func() bool

	width  int
	height int
}

// New builds the TUI model, loading the last-used settings.
func New(version string) *Model {
	return NewWithPreset(version, "", "", "")
}

// NewWithPreset builds the TUI model like New, but lets command-line flags
// override the other settings. The order is the same as for `serve`:
// flag > environment > config.json > default. Empty flag values fall back.
// A malformed NGODINGMCP_* variable is kept in ConfigError and shown, and
// the server refuses to start until it is fixed.
func NewWithPreset(version, root, addr, token string) *Model {
	saved := config.Load()
	s, err := config.Resolve(saved)

	m := &Model{version: version, built: buildinfo.Get().Built(), logs: NewLogBuffer(500)}
	m.settings = s
	m.configErr = err
	if err != nil {
		m.errMsg = "invalid environment: " + err.Error()
	}

	defaults := [fieldCount]string{
		config.FirstNonEmpty(root, s.Root),
		config.FirstNonEmpty(addr, s.Addr),
		config.FirstNonEmpty(token, s.Token),
		"",
		"",
		"",
	}
	m.runEnabled = s.AllowRun
	m.allowDownload = s.AllowDownload
	// Unlike `serve`, the control panel may start from the saved toggle: it
	// is on screen with a warning before anything starts. The environment
	// still wins.
	m.allowSensitive, _ = config.EnvBool("NGODINGMCP_ALLOW_SENSITIVE", saved.AllowSensitive)

	for i := range m.inputs {
		ti := textinput.New()
		ti.Placeholder = "…"
		ti.CharLimit = 4096
		ti.Width = 60
		ti.SetValue(defaults[i])
		m.inputs[i] = ti
	}
	m.inputs[fieldToken].EchoMode = textinput.EchoPassword
	m.inputs[fieldToken].EchoCharacter = '•'
	m.inputs[fieldRoot].Focus()

	return m
}

// SetServiceProbe sets the check used to tell the user that the background
// service (`ngodingmcp -d`) already holds the listen port.
func (m *Model) SetServiceProbe(running func() bool) { m.serviceRunning = running }

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return tickCmd() }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		w := msg.Width - 8
		if w < 20 {
			w = 20
		}
		for i := range m.inputs {
			m.inputs[i].Width = w
		}
		return m, nil

	case tickMsg:
		m.syncRoot()
		return m, tickCmd()

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+q":
			m.stop()
			return m, tea.Quit
		case "tab":
			m.cycleFocus(1)
			return m, nil
		case "shift+tab":
			m.cycleFocus(-1)
			return m, nil
		case "enter":
			if m.running {
				m.stop()
			} else {
				m.start()
			}
			return m, nil
		case " ":
			switch m.focus {
			case fieldRunEnabled:
				m.runEnabled = !m.runEnabled
			case fieldAllowSensitive:
				m.allowSensitive = !m.allowSensitive
			case fieldAllowDownload:
				m.allowDownload = !m.allowDownload
			default:
				// A space typed into a text field is ordinary input.
				var cmd tea.Cmd
				m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
				return m, cmd
			}
			return m, nil
		case "ctrl+r":
			m.stop()
			m.start()
			return m, nil
		case "ctrl+o":
			if m.running {
				if err := openURL(m.srv.UIURL()); err != nil {
					m.errMsg = "cannot open browser: " + err.Error()
				}
			}
			return m, nil
		case "ctrl+l":
			m.logs.Clear()
			return m, nil
		case "ctrl+x":
			if m.running {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				m.srv.StopAllProcesses(ctx)
				cancel()
				m.statusMsg = "stopped all background processes"
				m.logs.Add("stopped all background processes")
			}
			return m, nil
		case "ctrl+t":
			m.showToken = !m.showToken
			if m.showToken {
				m.inputs[fieldToken].EchoMode = textinput.EchoNormal
			} else {
				m.inputs[fieldToken].EchoMode = textinput.EchoPassword
			}
			return m, nil
		}
	}

	if isToggle(m.focus) {
		// The toggles have no text input of their own.
		return m, nil
	}

	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	return m, cmd
}

func (m *Model) cycleFocus(delta int) {
	if !isToggle(m.focus) {
		m.inputs[m.focus].Blur()
	}
	m.focus = (m.focus + delta + fieldCount) % fieldCount
	if !isToggle(m.focus) {
		m.inputs[m.focus].Focus()
	}
}

// ConfigError reports a malformed NGODINGMCP_* variable found when the
// model was built; nil when the environment is fine.
func (m *Model) ConfigError() error { return m.configErr }

func (m *Model) start() {
	if m.configErr != nil {
		m.errMsg = "cannot start: invalid environment: " + m.configErr.Error()
		return
	}
	m.errMsg = ""
	name := strings.TrimSpace(m.inputs[fieldRoot].Value())
	if name == "" {
		m.errMsg = "workspace root must not be empty"
		return
	}

	srv, err := app.New(app.Config{
		Addr:               strings.TrimSpace(m.inputs[fieldAddr].Value()),
		Root:               name,
		Token:              strings.TrimSpace(m.inputs[fieldToken].Value()),
		AllowRun:           m.runEnabled,
		AllowSensitive:     m.allowSensitive,
		RunAllowlist:       config.SplitList(m.settings.RunAllow),
		RunExactAllowlist:  config.SplitList(m.settings.RunExact),
		RunTimeout:         m.settings.RunTimeout,
		AllowDownload:      m.allowDownload,
		DownloadAllowHosts: config.SplitList(m.settings.DownloadHosts),
		DownloadMaxBytes:   m.settings.DownloadMaxBytes,
		UploadMaxBytes:     m.settings.UploadMaxBytes,
		PublicURL:          m.settings.PublicURL,
		OAuthDisabled:      !m.settings.OAuth,
		TrustedProxies:     config.SplitList(m.settings.TrustedProxies),
		Version:            m.version,
		LogWriter:          m.logs,
	})
	if err != nil {
		m.errMsg = err.Error()
		return
	}
	if err := srv.Start(); err != nil {
		m.errMsg = m.startError(strings.TrimSpace(m.inputs[fieldAddr].Value()), err)
		return
	}

	m.srv = srv
	m.running = true
	m.inputs[fieldToken].SetValue(srv.Token())
	m.statusMsg = "started; workspace " + srv.Root()
	m.logs.Add("server listening on " + srv.BaseURL())
	// Update keeps the saved workspaces of the Web UI selector.
	addr := strings.TrimSpace(m.inputs[fieldAddr].Value())
	_ = config.Update(func(c *config.Config) {
		c.Root = srv.Root()
		c.Addr = addr
		c.Token = srv.Token()
		c.Run = config.RunConfig{Enabled: m.runEnabled}
		c.AllowSensitive = m.allowSensitive
		c.AllowDownload = m.allowDownload
	})
}

// syncRoot shows a workspace switched from the Web UI in the root field,
// unless the user is editing that field right now.
func (m *Model) syncRoot() {
	if !m.running || m.srv == nil || m.focus == fieldRoot {
		return
	}
	if root := m.srv.Root(); root != m.inputs[fieldRoot].Value() {
		m.inputs[fieldRoot].SetValue(root)
		m.statusMsg = "workspace switched to " + root
	}
}

func (m *Model) stop() {
	if m.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.srv.Shutdown(ctx); err != nil {
		m.errMsg = "shutdown: " + err.Error()
	}
	m.logs.Add("server stopped")
	m.srv = nil
	m.running = false
	m.statusMsg = "stopped"
}

func (m *Model) processInfos() []procman.Info {
	if !m.running || m.srv == nil {
		return nil
	}
	return m.srv.Processes()
}

func tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}
