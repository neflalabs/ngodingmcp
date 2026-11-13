// Command ngodingmcp runs a sandboxed text-editor MCP server over HTTP, with an
// embedded web UI and an optional terminal control panel.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"ngodingmcp/internal/app"
	"ngodingmcp/internal/config"
	"ngodingmcp/internal/tui"
)

// version can be overridden at build time:
//
//	go build -ldflags "-X main.version=1.2.3"
var version = "v2026.11.4"

const usageText = `NgodingMCP — sandboxed text-editor MCP server

Usage:
  ngodingmcp                    Start the server, or open the control panel in a terminal
  ngodingmcp serve [flags]      Start the HTTP server
  ngodingmcp tui [flags]        Open the terminal control panel
  ngodingmcp version            Print the version
  ngodingmcp help               Show this help

Server flags:
  -addr string             HTTP listen address (default "127.0.0.1:8080")
  -root string             workspace root directory, created if missing
  -token string            shared auth token; generated when empty
  -max-file-bytes int      maximum file size read by tools, in bytes (default 2097152)
  -allow-run               enable the opt-in run_command and background process tools (built-in policy)
  -run-allow string        override built-in command prefixes (comma-separated)
  -run-allow-exact string  override built-in exact commands with no extra arguments
  -run-timeout duration    per-command timeout for run_command (default 1m0s)
  -allow-sensitive         let tools read, list and edit sensitive files such as .env,
                           private keys and .git internals (blocked by default)
  -allow-download          enable the opt-in download_file tool (https, public hosts only)
  -download-allow-host string
                           limit download_file to these hosts (comma-separated,
                           "*.example.com" for subdomains); empty = any public host
  -download-max-bytes int  maximum size per download_file call (default 20971520)
  -upload-max-bytes int    maximum size per file uploaded in the web UI (default 20971520)

Control panel flags:
  -root string             preset workspace root
  -addr string             preset listen address
  -token string            preset auth token

Environment: NGODINGMCP_ADDR, NGODINGMCP_ROOT, NGODINGMCP_TOKEN,
             NGODINGMCP_ALLOW_RUN, NGODINGMCP_RUN_ALLOWLIST,
             NGODINGMCP_RUN_ALLOWLIST_EXACT, NGODINGMCP_RUN_TIMEOUT,
             NGODINGMCP_ALLOW_SENSITIVE, NGODINGMCP_ALLOW_DOWNLOAD,
             NGODINGMCP_DOWNLOAD_ALLOW_HOSTS, NGODINGMCP_DOWNLOAD_MAX_BYTES,
             NGODINGMCP_UPLOAD_MAX_BYTES

Settings are also loaded from the TUI config file (see "ngodingmcp tui");
flags and environment variables override it.

Examples:
  ngodingmcp serve --root ~/project --addr 127.0.0.1:8080
  ngodingmcp serve --allow-run
  ngodingmcp serve --allow-run --run-allow "go build,go test"
  ngodingmcp serve --allow-download --download-max-bytes 52428800
  ngodingmcp tui --root ~/project
  NGODINGMCP_TOKEN=secret ngodingmcp serve
`

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		// No arguments in an interactive terminal: open the control panel.
		if stdinIsTerminal() {
			runTUIOrDie(nil)
			return
		}
		runServerOrDie(nil)
		return
	}

	switch args[0] {
	case "serve":
		runServerOrDie(args[1:])
	case "tui":
		runTUIOrDie(args[1:])
	case "version", "-version", "--version", "-v":
		fmt.Println("ngodingmcp " + version)
	case "help", "-h", "--help":
		fmt.Print(usageText)
	default:
		if strings.HasPrefix(args[0], "-") {
			// Backward compatibility: `ngodingmcp --addr ...` starts the server.
			runServerOrDie(args)
			return
		}
		fmt.Fprintf(os.Stderr, "error: unknown command %q\n\n%s", args[0], usageText)
		os.Exit(2)
	}
}

func runTUIOrDie(args []string) {
	root, addr, token, err := parseTUIFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if err := runTUI(root, addr, token); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func runTUI(root, addr, token string) error {
	p := tea.NewProgram(tui.NewWithPreset(version, root, addr, token), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// serverOptions holds the parsed server configuration.
type serverOptions struct {
	addr         string
	root         string
	token        string
	maxFileBytes int64
	allowRun     bool
	runAllow     string
	runExact     string
	runTimeout   time.Duration
	// allowSensitive disables the sensitive-file guard (.env, keys, .git).
	allowSensitive bool
	// download_file (opt-in) and web UI upload limits.
	allowDownload    bool
	downloadHosts    string
	downloadMaxBytes int64
	uploadMaxBytes   int64
}

func parseServerFlags(args []string) (*serverOptions, error) {
	opts := &serverOptions{}
	fs := flag.NewFlagSet("ngodingmcp serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	saved := config.Load()

	fs.StringVar(&opts.addr, "addr", envOr("NGODINGMCP_ADDR", firstNonEmpty(saved.Addr, "127.0.0.1:8080")), "HTTP listen address")
	fs.StringVar(&opts.root, "root", envOr("NGODINGMCP_ROOT", firstNonEmpty(saved.Root, filepath.Join(homeDir(), "ngodingmcp-workspace"))), "workspace root directory (created if missing)")
	fs.StringVar(&opts.token, "token", envOr("NGODINGMCP_TOKEN", saved.Token), "shared auth token (generated if empty)")
	fs.Int64Var(&opts.maxFileBytes, "max-file-bytes", 2<<20, "maximum file size read by tools, in bytes")
	fs.BoolVar(&opts.allowRun, "allow-run", envBoolFrom("NGODINGMCP_ALLOW_RUN", saved.Run.Enabled), "enable the opt-in run_command and background process tools")
	fs.StringVar(&opts.runAllow, "run-allow", os.Getenv("NGODINGMCP_RUN_ALLOWLIST"), "override the built-in command prefixes allowed for run_command")
	fs.StringVar(&opts.runExact, "run-allow-exact", os.Getenv("NGODINGMCP_RUN_ALLOWLIST_EXACT"), "override the built-in exact commands allowed for run_command")
	fs.DurationVar(&opts.runTimeout, "run-timeout", envDuration("NGODINGMCP_RUN_TIMEOUT", time.Minute), "per-command timeout for run_command")
	fs.BoolVar(&opts.allowSensitive, "allow-sensitive", envBoolFrom("NGODINGMCP_ALLOW_SENSITIVE", false), "allow tools to read, list and edit sensitive files (.env, keys, .git internals)")

	fs.BoolVar(&opts.allowDownload, "allow-download", envBoolFrom("NGODINGMCP_ALLOW_DOWNLOAD", saved.AllowDownload), "enable the opt-in download_file tool (https, public hosts only)")
	fs.StringVar(&opts.downloadHosts, "download-allow-host", os.Getenv("NGODINGMCP_DOWNLOAD_ALLOW_HOSTS"), "limit download_file to these hosts (comma-separated); empty means any public host")
	fs.Int64Var(&opts.downloadMaxBytes, "download-max-bytes", envInt64("NGODINGMCP_DOWNLOAD_MAX_BYTES", 20<<20), "maximum size per download_file call, in bytes")
	fs.Int64Var(&opts.uploadMaxBytes, "upload-max-bytes", envInt64("NGODINGMCP_UPLOAD_MAX_BYTES", 20<<20), "maximum size per file uploaded through the web UI, in bytes")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if opts.downloadMaxBytes <= 0 || opts.uploadMaxBytes <= 0 {
		return nil, errors.New("-download-max-bytes and -upload-max-bytes must be positive")
	}
	return opts, nil
}

func parseTUIFlags(args []string) (root, addr, token string, err error) {
	fs := flag.NewFlagSet("ngodingmcp tui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	fs.StringVar(&root, "root", "", "preset workspace root")
	fs.StringVar(&addr, "addr", "", "preset listen address")
	fs.StringVar(&token, "token", "", "preset auth token")

	if err := fs.Parse(args); err != nil {
		return "", "", "", err
	}
	if fs.NArg() > 0 {
		return "", "", "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return root, addr, token, nil
}

func runServerOrDie(args []string) {
	opts, err := parseServerFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if err := runServer(opts); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runServer(opts *serverOptions) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	srv, err := app.New(app.Config{
		Addr:               opts.addr,
		Root:               opts.root,
		Token:              opts.token,
		MaxFileBytes:       opts.maxFileBytes,
		AllowRun:           opts.allowRun,
		AllowSensitive:     opts.allowSensitive,
		RunAllowlist:       splitList(opts.runAllow),
		RunExactAllowlist:  splitList(opts.runExact),
		RunTimeout:         opts.runTimeout,
		AllowDownload:      opts.allowDownload,
		DownloadAllowHosts: splitList(opts.downloadHosts),
		DownloadMaxBytes:   opts.downloadMaxBytes,
		UploadMaxBytes:     opts.uploadMaxBytes,
		Version:            version,
		Logger:             logger,
	})
	if err != nil {
		return err
	}
	if err := srv.Start(); err != nil {
		return err
	}

	printBanner(srv)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func printBanner(srv *app.Server) {
	fmt.Fprintf(os.Stderr, "\nNgodingMCP %s\n", version)
	fmt.Fprintf(os.Stderr, "  workspace: %s\n", srv.Root())
	fmt.Fprintf(os.Stderr, "  web UI:    %s\n", srv.UIURL())
	fmt.Fprintf(os.Stderr, "  MCP:       %s\n", srv.MCPURL())
	if srv.DownloadEnabled() {
		fmt.Fprintf(os.Stderr, "  download:  download_file enabled (https, public hosts only)\n")
	}
	if srv.TokenGenerated() {
		fmt.Fprintf(os.Stderr, "  token:     %s  (generated for this run)\n", srv.Token())
	} else {
		fmt.Fprintf(os.Stderr, "  token:     (from flag/env)\n")
	}
	fmt.Fprintf(os.Stderr, "\n  Tip: run `ngodingmcp tui` for an interactive control panel.\n\n")
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return err == nil && v
}

// envBoolFrom uses the environment when set, otherwise fallback.
func envBoolFrom(key string, fallback bool) bool {
	if _, ok := os.LookupEnv(key); !ok {
		return fallback
	}
	return envBool(key)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func envInt64(key string, fallback int64) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}
