package app

// Core is the reusable MCP runtime: a workspace, the MCP server and the
// optional process/download helpers. It deliberately knows nothing about
// HTTP routing, authentication, the Web UI, the TUI or the systemd service,
// so the same construction can be reused by another front end or template.

import (
	"context"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	mayamcpapi "github.com/neflalabs/mayamcp/api"

	"ngodingmcp/internal/config"
	"ngodingmcp/internal/fetch"
	"ngodingmcp/internal/mcpserver"
	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/runner"
	"ngodingmcp/internal/selfrestart"
	"ngodingmcp/internal/workspace"
)

type Core struct {
	Workspace  *workspace.Workspace
	MCPServer  *mcp.Server
	Runner     *runner.Runner
	Processes  *procman.Manager
	Downloader *fetch.Client
	ToolCount  int
	Logger     *slog.Logger
	Disabled   []mcpserver.Module
}

// withDefaults resolves the empty fields shared by New and NewCore.
func (cfg Config) withDefaults() Config {
	if cfg.Addr == "" {
		cfg.Addr = config.DefaultAddr
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = 2 << 20
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	return cfg
}

// NewCore prepares an MCP runtime without starting a listener or wiring any
// UI/auth layer. The caller can serve the returned MCPServer over any transport.
func NewCore(cfg Config) (*Core, error) {
	cfg = cfg.withDefaults()

	// The logger is now built by mayamcp's level-B helper; behaviour is the
	// same as before (text handler, info level, stderr by default).
	logger := mayamcpapi.NewLogger(mayamcpapi.LogOptions{
		Writer: cfg.LogWriter,
		Logger: cfg.Logger,
	})

	disabled, err := mcpserver.ParseModules(cfg.DisableTools)
	if err != nil {
		return nil, err
	}
	ws, err := workspace.New(cfg.Root, cfg.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	ws.SetAllowSensitive(cfg.AllowSensitive)
	if cfg.AllowSensitive {
		logger.Warn("sensitive-file protection is disabled (--allow-sensitive)")
	}

	var cmdRunner *runner.Runner
	var procMgr *procman.Manager
	if cfg.AllowRun {
		allow, exact := cfg.RunAllowlist, cfg.RunExactAllowlist
		if len(allow) == 0 && len(exact) == 0 {
			allow, exact = runner.DefaultPolicy()
		}
		cmdRunner, err = runner.New(runner.Options{
			Root:           ws.Root(),
			Allowlist:      allow,
			ExactAllowlist: exact,
			Timeout:        cfg.RunTimeout,
			Restart:        selfrestart.New(logger).Schedule,
		})
		if err != nil {
			return nil, err
		}
		procMgr, err = procman.New(procman.Options{Root: ws.Root(), Runner: cmdRunner})
		if err != nil {
			return nil, err
		}
	}

	var downloader *fetch.Client
	if cfg.AllowDownload {
		downloader = fetch.New(fetch.Options{
			MaxBytes:   cfg.DownloadMaxBytes,
			AllowHosts: cfg.DownloadAllowHosts,
		})
	}

	toolOpts := mcpserver.Options{
		Runner:     cmdRunner,
		Processes:  procMgr,
		Downloader: downloader,
		Disabled:   disabled,
		Logger:     logger,
	}
	name, title, _, _, instructions := mcpserver.ResolveDefaults(toolOpts)
	_, _, _, namespace, _ := mcpserver.ResolveDefaults(toolOpts)

	// C-lite adoption: mayamcp owns identity, logging and the hook lifecycle;
	// ngodingmcp supplies domain tools, resources and middleware.
	mayamcpCore := mayamcpapi.NewCore(mayamcpapi.Config{
		Name:         name,
		Title:        title,
		Version:      cfg.Version,
		Instructions: instructions,
		Logger:       logger,
		// ngodingmcp's activity middleware understands its _meta exit codes.
		DisableActivityMiddleware: true,
		Register: func(s *mcp.Server) {
			mcpserver.RegisterTools(s, ws, toolOpts)
		},
		RegisterResources: func(s *mcp.Server) {
			mcpserver.RegisterResources(s, ws, toolOpts)
		},
		Middleware: []mcp.Middleware{
			mcpserver.SafeErrorMiddleware(ws),
			mcpserver.HoldWorkspaceMiddleware(ws),
			mcpserver.ActivityMiddleware(logger, namespace),
		},
	})
	mcpServer := mayamcpCore.Server
	if len(disabled) > 0 {
		logger.Info("tool modules disabled", "modules", cfg.DisableTools)
	}
	toolCount, err := mayamcpapi.CountTools(context.Background(), mcpServer)
	if err != nil {
		// Only the TUI summary uses the count; never fail startup over it.
		logger.Warn("count tools", "err", err)
		toolCount = 0
	}

	return &Core{
		Workspace:  ws,
		MCPServer:  mcpServer,
		Runner:     cmdRunner,
		Processes:  procMgr,
		Downloader: downloader,
		ToolCount:  toolCount,
		Logger:     logger,
		Disabled:   disabled,
	}, nil
}
