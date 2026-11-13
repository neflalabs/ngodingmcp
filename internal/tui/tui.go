// Package tui implements an interactive control panel for the server.
package tui

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ngodingmcp/internal/app"
	"ngodingmcp/internal/buildinfo"
	"ngodingmcp/internal/config"
	"ngodingmcp/internal/procman"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	focusStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	okStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	keyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
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

// sensitiveWarning is shown while the sensitive-file guard is turned off.
const sensitiveWarning = "⚠ guard off: the AI can read and write .env, private keys and .git"

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
// override the saved settings. Empty values fall back to the saved config.
func NewWithPreset(version, root, addr, token string) *Model {
	saved := config.Load()

	m := &Model{version: version, built: buildinfo.Get().Built(), logs: NewLogBuffer(500)}

	defaults := [fieldCount]string{
		firstNonEmpty(root, saved.Root, os.Getenv("NGODINGMCP_ROOT"), defaultRoot()),
		firstNonEmpty(addr, saved.Addr, os.Getenv("NGODINGMCP_ADDR"), "127.0.0.1:8080"),
		firstNonEmpty(token, saved.Token, os.Getenv("NGODINGMCP_TOKEN")),
		"",
		"",
		"",
	}
	m.runEnabled = saved.Run.Enabled
	m.allowSensitive = saved.AllowSensitive
	m.allowDownload = saved.AllowDownload

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

// headerVersion is the version shown in the header, plus the build time when
// known, so every rebuild is visible even when the commit did not change.
func (m *Model) headerVersion() string {
	if m.built == "" {
		return m.version
	}
	return m.version + "  ·  built " + m.built
}

// View implements tea.Model.
func (m *Model) View() string {
	var b strings.Builder
	procs := m.processInfos()

	b.WriteString(titleStyle.Render("NgodingMCP") + dimStyle.Render("  "+m.headerVersion()+"  ·  control panel") + "\n\n")

	for i := range m.inputs {
		focused := i == m.focus
		if focused {
			b.WriteString(focusStyle.Render("▸ "+labels[i]) + "\n")
		} else {
			b.WriteString(labelStyle.Render("  "+labels[i]) + "\n")
		}
		if isToggle(i) {
			on := m.runEnabled
			switch i {
			case fieldAllowSensitive:
				on = m.allowSensitive
			case fieldAllowDownload:
				on = m.allowDownload
			}
			box := "[ ] disabled"
			if on {
				box = "[x] enabled"
			}
			if focused {
				box = focusStyle.Render(box)
			}
			b.WriteString("  " + box + "\n")
			if i == fieldAllowSensitive && m.allowSensitive {
				b.WriteString("  " + errStyle.Render(sensitiveWarning) + "\n")
			}
			b.WriteString("\n")
			continue
		}
		b.WriteString("  " + m.inputs[i].View() + "\n\n")
	}

	b.WriteString(m.helpLine() + "\n")

	// Status line.
	switch {
	case m.errMsg != "":
		b.WriteString(errStyle.Render("✗ "+m.errMsg) + "\n")
	case m.running:
		b.WriteString(okStyle.Render("● running") + "  " + m.srv.UIURL() + "\n")
		b.WriteString(dimStyle.Render("  MCP endpoint: ") + m.srv.MCPURL() + "\n")
		summary := summaryLine(m.srv.ToolCount(), m.srv.RunEnabled(), m.srv.SensitiveGuard())
		if m.srv.DownloadEnabled() {
			summary += dimStyle.Render(" · download: on")
		}
		b.WriteString("  " + summary + "\n")
		if m.srv.TokenGenerated() {
			b.WriteString(dimStyle.Render("  token generated; press ctrl+t to reveal") + "\n")
		}
	default:
		b.WriteString(dimStyle.Render("○ stopped") + "\n")
	}
	if m.statusMsg != "" && m.errMsg == "" {
		b.WriteString(dimStyle.Render("  "+m.statusMsg) + "\n")
	}

	// Background processes.
	if len(procs) > 0 {
		b.WriteString("\n" + labelStyle.Render("Processes") + dimStyle.Render("  (ctrl+x stop all)") + "\n")
		for _, p := range procs {
			status := tuiProcessStatus(p)
			line := "  " + p.Name + "  " + status + "  pid " + itoa(p.PID) + "  " + shortDuration(p.DurationMS)
			if p.Running {
				b.WriteString(okStyle.Render(line) + "\n")
			} else {
				b.WriteString(dimStyle.Render(line) + "\n")
			}
		}
	}

	// Logs.
	b.WriteString("\n" + dimStyle.Render(strings.Repeat("─", max(10, m.width-2))) + "\n")
	b.WriteString(labelStyle.Render("Logs") + dimStyle.Render("  (ctrl+l to clear)") + "\n")

	lines := m.logs.Tail(m.logLineCount(len(procs)))
	if len(lines) == 0 {
		b.WriteString(dimStyle.Render("  no output yet") + "\n")
	}
	for _, line := range lines {
		if act, ok := activityLogLine(line); ok {
			b.WriteString("  " + renderActivity(m.clip(act)) + "\n")
			continue
		}
		b.WriteString("  " + dimStyle.Render(m.clip(line)) + "\n")
	}

	return b.String()
}

func (m *Model) helpLine() string {
	key := func(k, d string) string { return keyStyle.Render(k) + dimStyle.Render(" "+d) }
	return "  " + strings.Join([]string{
		key("[enter]", "start/stop"),
		key("[space]", "toggle option"),
		key("[ctrl+r]", "restart"),
		key("[ctrl+o]", "open UI"),
		key("[ctrl+x]", "stop processes"),
		key("[ctrl+t]", "show token"),
		key("[tab]", "next field"),
		key("[ctrl+q]", "quit"),
	}, "   ")
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

func (m *Model) start() {
	m.errMsg = ""
	name := strings.TrimSpace(m.inputs[fieldRoot].Value())
	if name == "" {
		m.errMsg = "workspace root must not be empty"
		return
	}

	srv, err := app.New(app.Config{
		Addr:           strings.TrimSpace(m.inputs[fieldAddr].Value()),
		Root:           name,
		Token:          strings.TrimSpace(m.inputs[fieldToken].Value()),
		AllowRun:       m.runEnabled,
		AllowSensitive: m.allowSensitive,
		AllowDownload:  m.allowDownload,
		Version:        m.version,
		LogWriter:      m.logs,
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

func (m *Model) logLineCount(nprocs int) int {
	if m.height == 0 {
		return 8
	}
	// Three inputs take ~9 lines, three toggles ~9, plus status, help and
	// headers ≈ 26. The sensitive warning adds one more line.
	n := m.height - 26
	if m.running {
		n-- // tools / run / guard summary line
	}
	if m.allowSensitive {
		n--
	}
	if nprocs > 0 {
		n -= nprocs + 2
	}
	if n < 3 {
		n = 3
	}
	return n
}

func (m *Model) processInfos() []procman.Info {
	if !m.running || m.srv == nil {
		return nil
	}
	return m.srv.Processes()
}

func tuiProcessStatus(p procman.Info) string {
	switch {
	case p.Running && p.Stopping:
		return "stopping"
	case p.Running && !p.Ready:
		return "starting"
	case p.Running:
		return "running "
	default:
		return "exited  "
	}
}

func shortDuration(ms int64) string {
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return strconv.Itoa(int(d.Seconds())) + "s"
	}
	return d.Round(time.Second).String()
}

func itoa(n int) string { return strconv.Itoa(n) }

func (m *Model) clip(s string) string {
	if m.width <= 0 {
		return s
	}
	limit := m.width - 4
	if limit < 10 {
		limit = 10
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
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

func defaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "ngodingmcp-workspace"
	}
	return home + string(os.PathSeparator) + "ngodingmcp-workspace"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
