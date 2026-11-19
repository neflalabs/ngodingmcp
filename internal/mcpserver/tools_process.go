package mcpserver

// Background process tools (opt-in, registered only when Options.Processes is
// set): start_process, list_processes, process_logs, stop_process,
// restart_process and stop_all_processes. Commands go through the same
// allowlist as run_command; the lifecycle itself lives in internal/procman.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/runner"
)

// addProcessTools registers every background process tool backed by mgr.
func addProcessTools(s *mcp.Server, mgr *procman.Manager) {
	addStartProcess(s, mgr)
	addListProcesses(s, mgr)
	addProcessLogs(s, mgr)
	addStopProcess(s, mgr)
	addRestartProcess(s, mgr)
	addStopAllProcesses(s, mgr)
}

// --- start_process ---------------------------------------------------------

type startProcessArgs struct {
	Command        string            `json:"command" jsonschema:"allowlisted command, e.g. \"bun run dev\""`
	Args           []string          `json:"args,omitempty" jsonschema:"additional arguments appended to the command"`
	Name           string            `json:"name,omitempty" jsonschema:"process name (letters, digits, _ or -); generated when empty"`
	Cwd            string            `json:"cwd,omitempty" jsonschema:"working directory relative to the workspace root"`
	Env            map[string]string `json:"env,omitempty" jsonschema:"env vars; keys: PORT, HOST, HOSTNAME, NODE_ENV, BUN_ENV, CI"`
	WaitForLog     string            `json:"wait_for_log,omitempty" jsonschema:"regex the output must match before start returns"`
	WaitForPort    int               `json:"wait_for_port,omitempty" jsonschema:"TCP port that must accept connections before start returns"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" jsonschema:"readiness timeout in seconds (default 10, max 60)"`
}

type startProcessOut struct {
	procman.Info
}

func addStartProcess(s *mcp.Server, mgr *procman.Manager) {
	desc := "Start an allowlisted command (no shell) as a background process that outlives the call; " +
		"read it with process_logs, end it with stop_process. "
	prefix, exact := mgr.Allowlist()
	desc += allowlistDescription(prefix, withoutRestart(exact))
	addUntypedTool(s, &mcp.Tool{
		Name:        "start_process",
		Description: desc,
	}, func(_ context.Context, _ *mcp.CallToolRequest, in startProcessArgs) (*mcp.CallToolResult, startProcessOut, error) {
		argv := append(strings.Fields(in.Command), in.Args...)
		if len(argv) == 0 {
			return nil, startProcessOut{}, runner.ErrEmptyCommand
		}
		info, err := mgr.StartWithOptions(procman.StartOptions{
			Name:         in.Name,
			Argv:         argv,
			Dir:          in.Cwd,
			Env:          in.Env,
			ReadyLog:     in.WaitForLog,
			ReadyPort:    in.WaitForPort,
			ReadyTimeout: seconds(in.TimeoutSeconds),
		})
		if err != nil {
			if errors.Is(err, procman.ErrNotReady) && info.Name != "" {
				_, logs, _ := mgr.Logs(info.Name, 20, 0)
				if tail := formatLogLines(logs, true); tail != "" {
					return nil, startProcessOut{}, fmt.Errorf("%w\nrecent output:\n%s\nprocess %q is still running; use stop_process to end it", err, tail, info.Name)
				}
				return nil, startProcessOut{}, fmt.Errorf("%w\nprocess %q is still running; use stop_process to end it", err, info.Name)
			}
			return nil, startProcessOut{}, err
		}
		text := fmt.Sprintf("started %s (pid %d): %s", info.Name, info.PID, info.ArgvString())
		if info.Dir != "" {
			text += "\ncwd: " + info.Dir
		}
		text += fmt.Sprintf("\nready: %t", info.Ready)
		return textResult(text), startProcessOut{Info: info}, nil
	})
}

// --- list_processes --------------------------------------------------------

type listProcessesArgs struct{}

type listProcessesOut struct {
	Processes []procman.Info `json:"processes"`
}

func addListProcesses(s *mcp.Server, mgr *procman.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_processes",
		Description: "List this session's background processes: status, readiness, PID, exit code, runtime.",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listProcessesArgs) (*mcp.CallToolResult, listProcessesOut, error) {
		procs := mgr.List()
		var b strings.Builder
		if len(procs) == 0 {
			b.WriteString("no processes\n")
		} else {
			fmt.Fprintf(&b, "%-14s %-8s %-8s %-7s %-6s %8s  %s\n", "name", "status", "ready", "pid", "exit", "runtime", "command")
			for _, p := range procs {
				exit := "-"
				if !p.Running {
					exit = fmt.Sprintf("%d", p.ExitCode)
				}
				fmt.Fprintf(&b, "%-14s %-8s %-8t %-7d %-6s %8s  %s\n",
					p.Name, processStatus(p), p.Ready, p.PID, exit, formatDuration(p.DurationMS), p.ArgvString())
			}
		}
		return textResult(b.String()), listProcessesOut{Processes: procs}, nil
	})
}

// --- process_logs ----------------------------------------------------------

type processLogsArgs struct {
	Name       string `json:"name" jsonschema:"process name returned by start_process"`
	TailLines  int    `json:"tail_lines,omitempty" jsonschema:"number of trailing lines when after_seq is 0 (default 100, max 1000)"`
	AfterSeq   int64  `json:"after_seq,omitempty" jsonschema:"nextSeq from the previous call"`
	Timestamps *bool  `json:"timestamps,omitempty" jsonschema:"prefix each line with its time (default true)"`
}

type processLogsOut struct {
	procman.Info
	Logs      string            `json:"logs"`
	Lines     []procman.LogLine `json:"lines"`
	NextSeq   int64             `json:"nextSeq"`
	Truncated bool              `json:"truncated"`
	Dropped   bool              `json:"dropped"`
}

func addProcessLogs(s *mcp.Server, mgr *procman.Manager) {
	addTextTool(s, &mcp.Tool{
		Name:        "process_logs",
		Description: "Read process output: after_seq = the last nextSeq for new lines only, else the last tail_lines.",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in processLogsArgs) (*mcp.CallToolResult, processLogsOut, error) {
		info, logs, err := mgr.Logs(in.Name, in.TailLines, in.AfterSeq)
		if err != nil {
			return nil, processLogsOut{}, err
		}
		timestamps := in.Timestamps == nil || *in.Timestamps
		var b strings.Builder
		fmt.Fprintf(&b, "%s (%s, pid %d, %s)\n", info.Name, processStatus(info), info.PID, formatDuration(info.DurationMS))
		if body := formatLogLines(logs, timestamps); body != "" {
			b.WriteString("\n")
			b.WriteString(body)
			b.WriteString("\n")
		} else if in.AfterSeq > 0 {
			b.WriteString("(no new output)\n")
		} else {
			b.WriteString("(no output yet)\n")
		}
		fmt.Fprintf(&b, "next_seq: %d", logs.NextSeq)
		return textResult(b.String()), processLogsOut{
			Info:      info,
			Logs:      formatLogLines(logs, timestamps),
			Lines:     logs.Lines,
			NextSeq:   logs.NextSeq,
			Truncated: logs.Truncated,
			Dropped:   logs.Dropped,
		}, nil
	})
}

// --- stop_process ----------------------------------------------------------

type stopProcessArgs struct {
	Name           string `json:"name" jsonschema:"process name returned by start_process"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"graceful shutdown grace period in seconds (default 5)"`
	Wait           *bool  `json:"wait,omitempty" jsonschema:"wait for exit (default true)"`
}

type stopProcessOut struct {
	procman.Info
}

func addStopProcess(s *mcp.Server, mgr *procman.Manager) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "stop_process",
		Description: "Stop a background process and its children (SIGTERM, then SIGKILL after the grace period).",
		Annotations: destructive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in stopProcessArgs) (*mcp.CallToolResult, stopProcessOut, error) {
		wait := in.Wait == nil || *in.Wait
		info, err := mgr.Stop(in.Name, seconds(in.TimeoutSeconds), wait)
		if err != nil {
			return nil, stopProcessOut{}, err
		}
		return textResult(fmt.Sprintf("%s %s", info.Name, stopState(info, wait))), stopProcessOut{Info: info}, nil
	})
}

// --- restart_process -------------------------------------------------------

type restartProcessArgs struct {
	Name           string `json:"name" jsonschema:"process name returned by start_process"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"graceful shutdown grace period in seconds (default 5)"`
}

type restartProcessOut struct {
	procman.Info
}

func addRestartProcess(s *mcp.Server, mgr *procman.Manager) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "restart_process",
		Description: "Restart a background process with the same command, cwd and env.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in restartProcessArgs) (*mcp.CallToolResult, restartProcessOut, error) {
		info, err := mgr.Restart(in.Name, seconds(in.TimeoutSeconds))
		if err != nil {
			return nil, restartProcessOut{}, err
		}
		return textResult(fmt.Sprintf("restarted %s (pid %d): %s", info.Name, info.PID, info.ArgvString())), restartProcessOut{Info: info}, nil
	})
}

// --- stop_all_processes ----------------------------------------------------

type stopAllProcessesArgs struct {
	TimeoutSeconds int `json:"timeout_seconds,omitempty" jsonschema:"graceful shutdown grace period in seconds (default 5)"`
}

type stopAllProcessesOut struct {
	Processes []procman.Info `json:"processes"`
}

func addStopAllProcesses(s *mcp.Server, mgr *procman.Manager) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "stop_all_processes",
		Description: "Stop every background process.",
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in stopAllProcessesArgs) (*mcp.CallToolResult, stopAllProcessesOut, error) {
		procs := mgr.StopAll(ctx, seconds(in.TimeoutSeconds))
		running := 0
		for _, p := range procs {
			if p.Running {
				running++
			}
		}
		return textResult(fmt.Sprintf("stopped processes; %d still running", running)), stopAllProcessesOut{Processes: procs}, nil
	})
}

// --- formatting helpers ----------------------------------------------------

// processStatus is the one-word lifecycle state shown in listings.
func processStatus(p procman.Info) string {
	switch {
	case p.Running && p.Stopping:
		return "stopping"
	case p.Running && !p.Ready:
		return "starting"
	case p.Running:
		return "running"
	default:
		return "exited"
	}
}

// stopState describes the outcome of a stop request.
func stopState(info procman.Info, wait bool) string {
	switch {
	case info.Running:
		return "stopping"
	case !wait:
		return "stop requested"
	case info.ExitCode != 0:
		return fmt.Sprintf("stopped (exit code %d)", info.ExitCode)
	default:
		return "stopped"
	}
}

// formatLogLines renders captured output, marking gaps in the ring buffer.
func formatLogLines(res procman.LogsResult, timestamps bool) string {
	var b strings.Builder
	if res.Truncated {
		b.WriteString("[... lines dropped since the given cursor ...]\n")
	}
	if res.Dropped {
		b.WriteString("[... older output dropped by ring buffer ...]\n")
	}
	for _, line := range res.Lines {
		if timestamps {
			b.WriteString(line.Time.Format("15:04:05") + "  " + line.Text + "\n")
		} else {
			b.WriteString(line.Text + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatDuration renders a runtime in milliseconds compactly (850ms, 4.2s, 3m10s).
func formatDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}
