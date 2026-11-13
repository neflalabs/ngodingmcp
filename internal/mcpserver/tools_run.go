package mcpserver

// Command tool (opt-in, registered only when Options.Runner is set):
// run_command executes one allowlisted command without a shell. The allowlist,
// timeout and output limits are enforced by internal/runner.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/runner"
)

type runCommandArgs struct {
	Command        string   `json:"command" jsonschema:"command to run; must match an allowlisted prefix such as \"go test\""`
	Args           []string `json:"args,omitempty" jsonschema:"additional arguments appended to the command"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty" jsonschema:"optional timeout in seconds, capped by the server maximum"`
}

type runCommandOut struct {
	Command    string   `json:"command"`
	Argv       []string `json:"argv"`
	ExitCode   int      `json:"exitCode"`
	Output     string   `json:"output"`
	Truncated  bool     `json:"truncated"`
	TimedOut   bool     `json:"timedOut"`
	DurationMS int64    `json:"durationMs"`
}

func addRunCommand(s *mcp.Server, run *runner.Runner) {
	desc := "Run an allowlisted command inside the workspace root without a shell. " +
		allowlistDescription(run.Allowlist(), run.ExactAllowlist()) +
		"Non-zero exit codes are reported in the result."
	mcp.AddTool(s, &mcp.Tool{
		Name:        "run_command",
		Description: desc,
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in runCommandArgs) (*mcp.CallToolResult, runCommandOut, error) {
		argv := append(strings.Fields(in.Command), in.Args...)
		if len(argv) == 0 {
			return nil, runCommandOut{}, runner.ErrEmptyCommand
		}

		if in.TimeoutSeconds > 0 {
			d := min(seconds(in.TimeoutSeconds), run.Timeout())
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}

		res, err := run.Run(ctx, argv)
		if err != nil {
			return nil, runCommandOut{}, err
		}
		out := runCommandOut{
			Command:    in.Command,
			Argv:       res.Argv,
			ExitCode:   res.ExitCode,
			Output:     res.Output,
			Truncated:  res.Truncated,
			TimedOut:   res.TimedOut,
			DurationMS: res.DurationMS,
		}
		return textResult(formatRunResult(res, run.Timeout())), out, nil
	})
}

// formatRunResult renders a finished command as a terminal-style transcript.
func formatRunResult(res runner.Result, timeout time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "$ %s\n", runner.FormatArgv(res.Argv))
	if res.TimedOut {
		fmt.Fprintf(&b, "timed out after %s\n", timeout)
	} else {
		fmt.Fprintf(&b, "exit code: %d\n", res.ExitCode)
	}
	fmt.Fprintf(&b, "duration: %dms\n", res.DurationMS)
	if res.Output != "" {
		b.WriteString("\n")
		b.WriteString(res.Output)
		if !strings.HasSuffix(res.Output, "\n") {
			b.WriteString("\n")
		}
	}
	if res.Truncated {
		b.WriteString("[output truncated]\n")
	}
	return b.String()
}
