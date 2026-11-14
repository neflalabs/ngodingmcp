package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ngodingmcp/internal/service"
)

// serviceManager is the part of service.Manager the subcommands use, so the
// output can be tested without systemd.
type serviceManager interface {
	Install(ctx context.Context) (*service.InstallResult, error)
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	Logs(ctx context.Context, lines int, follow bool) error
	Status(ctx context.Context) *service.Status
	Addr() string
	EnvFile() string
}

// newServiceManager is swapped in tests.
var newServiceManager = func() (serviceManager, error) { return service.New() }

// runServiceCommand runs `-d`, stop, restart, status or logs and returns the
// exit code.
func runServiceCommand(cmd string, args []string, out, errOut io.Writer) int {
	lines, follow := 50, true
	if cmd == "logs" {
		fs := flag.NewFlagSet("ngodingmcp logs", flag.ContinueOnError)
		fs.SetOutput(errOut)
		fs.IntVar(&lines, "n", 50, "number of past lines to show")
		noFollow := fs.Bool("no-follow", false, "print the lines and exit instead of following")
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() > 0 {
			fmt.Fprintf(errOut, "error: unexpected argument %q\n", fs.Arg(0))
			return 2
		}
		follow = !*noFollow
	} else if len(args) > 0 {
		fmt.Fprintf(errOut, "error: %s takes no arguments (got %q)\n", cmd, args[0])
		return 2
	}

	m, err := newServiceManager()
	if err != nil {
		fmt.Fprintln(errOut, "error:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "-d", "--daemon":
		return cmdInstall(ctx, m, out, errOut)
	case "stop":
		if err := m.Stop(ctx); err != nil {
			fmt.Fprintln(errOut, "error:", err)
			return 1
		}
		fmt.Fprintln(out, "NgodingMCP service stopped.")
		fmt.Fprintln(out, "It is still enabled, so it starts again at your next login (or boot, with lingering).")
		fmt.Fprintln(out, "Start it now with `ngodingmcp -d`; turn it off for good with `systemctl --user disable ngodingmcp`.")
		return 0
	case "restart":
		if err := m.Restart(ctx); err != nil {
			fmt.Fprintln(errOut, "error:", err)
			return 1
		}
		fmt.Fprintf(out, "NgodingMCP service restarted. Web UI: %s\n", service.UIURL(m.Addr()))
		return 0
	case "status":
		return cmdStatus(ctx, m, out)
	case "logs":
		if err := m.Logs(ctx, lines, follow); err != nil && ctx.Err() == nil {
			fmt.Fprintln(errOut, "error:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(errOut, "error: unknown service command %q\n", cmd)
	return 2
}

func cmdInstall(ctx context.Context, m serviceManager, out, errOut io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := m.Install(ctx)
	if res != nil {
		switch res.Unit {
		case service.UnitCreated:
			fmt.Fprintln(out, "Wrote the systemd user unit ngodingmcp.service.")
		case service.UnitUpdated:
			if res.OldExe != "" {
				fmt.Fprintf(out, "Updated the unit: binary moved from %s.\n", res.OldExe)
			} else {
				fmt.Fprintln(out, "Updated the unit file.")
			}
		}
		if res.EnvCreated {
			fmt.Fprintln(out, "Created the env file with your token (owner-only).")
		}
	}
	if err != nil {
		fmt.Fprintln(errOut, "error:", err)
		return 1
	}

	switch {
	case res.Restarted:
		fmt.Fprintln(out, "NgodingMCP service restarted with the new settings.")
	case res.WasActive:
		fmt.Fprintln(out, "NgodingMCP service is already running.")
	default:
		fmt.Fprintln(out, "NgodingMCP service started and enabled.")
	}
	// Status gets its own short timeout instead of inheriting whatever is
	// left of the 60s install budget.
	statusCtx, statusCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer statusCancel()
	st := m.Status(statusCtx)
	printAddresses(out, res.Addr, st)
	fmt.Fprintf(out, "  token:     NGODINGMCP_TOKEN in %s\n", m.EnvFile())
	for _, w := range st.Warnings {
		fmt.Fprintln(out, "  warning:  ", w)
	}
	if st.LingerKnown && !st.Linger {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "It runs while you are logged in. To keep it running after logout and start it at boot, run once:")
		fmt.Fprintln(out, "  loginctl enable-linger $USER")
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Manage it with: ngodingmcp status | ngodingmcp logs | ngodingmcp restart | ngodingmcp stop")
	return 0
}

func cmdStatus(ctx context.Context, m serviceManager, out io.Writer) int {
	st := m.Status(ctx)
	if !st.Installed {
		fmt.Fprintln(out, "NgodingMCP service: not set up. Run `ngodingmcp -d` to start it in the background.")
		return 3
	}
	state := st.State
	if st.Running() {
		state = "running"
	}
	fmt.Fprintf(out, "NgodingMCP service: %s (%s)\n", state, st.Enabled)
	if st.Running() {
		printAddresses(out, st.Addr, st)
	} else {
		fmt.Fprintf(out, "  address:   %s (not serving)\n", st.Addr)
	}
	if st.UnitExe != "" {
		fmt.Fprintf(out, "  binary:    %s\n", st.UnitExe)
	}
	switch {
	case st.LingerKnown && st.Linger:
		fmt.Fprintln(out, "  linger:    on (keeps running after logout)")
	case st.LingerKnown:
		fmt.Fprintln(out, "  linger:    off (stops at logout; `loginctl enable-linger $USER` to change)")
	}
	for _, w := range st.Warnings {
		fmt.Fprintln(out, "  warning:  ", w)
	}
	if !st.Running() {
		fmt.Fprintln(out, "Start it with `ngodingmcp -d`; see why it stopped with `ngodingmcp logs`.")
		return 3
	}
	return 0
}

func printAddresses(out io.Writer, addr string, st *service.Status) {
	fmt.Fprintf(out, "  web UI:    %s\n", service.UIURL(addr))
	fmt.Fprintf(out, "  MCP:       %s\n", service.MCPURL(addr))
	if st != nil && st.Workspace != "" {
		fmt.Fprintf(out, "  workspace: %s\n", st.Workspace)
	}
}
