package tui

import (
	"github.com/charmbracelet/lipgloss"
	"ngodingmcp/internal/procman"
	"strconv"
	"strings"
	"time"
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

// sensitiveWarning is shown while the sensitive-file guard is turned off.
const sensitiveWarning = "⚠ guard off: the AI can read and write .env, private keys and .git"

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
