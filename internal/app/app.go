// Package app wires the workspace, auth, MCP server and web UI into a single
// runnable HTTP server. It is shared by the CLI and the TUI.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/fetch"
	"ngodingmcp/internal/mcpserver"
	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/runner"
	"ngodingmcp/internal/webui"
	"ngodingmcp/internal/workspace"
)

const sessionCookie = "ngodingmcp_session"

// Config controls a Server instance.
type Config struct {
	Addr         string
	Root         string
	Token        string
	MaxFileBytes int64
	Version      string
	Logger       *slog.Logger
	// AllowRun enables the opt-in run_command MCP tool. RunAllowlist must list
	// at least one command prefix, e.g. "go test" or "git diff"; RunExactAllowlist
	// lists complete argv values that must match exactly.
	AllowRun          bool
	RunAllowlist      []string
	RunExactAllowlist []string
	RunTimeout        time.Duration
	// AllowSensitive turns off the guard that hides and blocks sensitive files
	// (.env, private keys, .git internals). Off (guarded) by default.
	AllowSensitive bool
	// AllowDownload enables the opt-in download_file MCP tool. With an empty
	// DownloadAllowHosts any public https host is allowed; private and
	// loopback addresses are always refused. DownloadMaxBytes defaults to
	// fetch.DefaultMaxBytes.
	AllowDownload      bool
	DownloadAllowHosts []string
	DownloadMaxBytes   int64
	// UploadMaxBytes caps each file uploaded through the web UI (default
	// webui.DefaultUploadMaxBytes).
	UploadMaxBytes int64
	// LogWriter, when set, receives structured logs instead of stderr. Useful
	// for embedding the server in a TUI.
	LogWriter io.Writer
}

// Server is a configured but not necessarily started HTTP server.
type Server struct {
	ws        *workspace.Workspace
	token     string
	generated bool
	version   string
	cfg       Config
	procs     *procman.Manager
	download  bool
	tools     int
	http      *http.Server
	listener  net.Listener
}

// New prepares a Server. The workspace root is created if missing, and a random
// token is generated when cfg.Token is empty.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8080"
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = 2 << 20
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}

	logger := cfg.Logger
	if cfg.LogWriter != nil {
		logger = slog.New(slog.NewTextHandler(cfg.LogWriter, &slog.HandlerOptions{Level: slog.LevelInfo}))
	} else if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	ws, err := workspace.New(cfg.Root, cfg.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	ws.SetAllowSensitive(cfg.AllowSensitive)
	if cfg.AllowSensitive {
		logger.Warn("sensitive-file protection is disabled (--allow-sensitive)")
	}

	token := strings.TrimSpace(cfg.Token)
	generated := false
	if token == "" {
		token, err = randomToken()
		if err != nil {
			return nil, fmt.Errorf("generate token: %w", err)
		}
		generated = true
	}

	authn := auth.New(token, sessionCookie)
	ui := webui.New(ws, authn, cfg.Version)
	ui.SetUploads(cfg.UploadMaxBytes, logger)

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
		})
		if err != nil {
			return nil, err
		}
		procMgr, err = procman.New(procman.Options{Root: ws.Root(), Runner: cmdRunner})
		if err != nil {
			return nil, err
		}
	}
	ui.SetProcesses(procMgr)

	var downloader *fetch.Client
	if cfg.AllowDownload {
		downloader = fetch.New(fetch.Options{
			MaxBytes:   cfg.DownloadMaxBytes,
			AllowHosts: cfg.DownloadAllowHosts,
		})
	}

	mcpServer := mcpserver.NewWithOptions(ws, cfg.Version, mcpserver.Options{
		Runner:     cmdRunner,
		Processes:  procMgr,
		Downloader: downloader,
		Logger:     logger,
	})
	toolCount, err := mcpserver.CountTools(context.Background(), mcpServer)
	if err != nil {
		// Only the TUI summary uses the count; never fail startup over it.
		logger.Warn("count tools", "err", err)
		toolCount = 0
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{Logger: logger},
	)

	mux := http.NewServeMux()
	mux.Handle("GET /login", http.HandlerFunc(ui.LoginPage))
	mux.Handle("POST /login", http.HandlerFunc(ui.LoginSubmit))
	mux.Handle("POST /logout", http.HandlerFunc(ui.Logout))
	mux.Handle("GET /static/", ui.Static())
	mux.Handle("/mcp", authn.API(mcpHandler))
	mux.Handle("/api/", authn.API(http.StripPrefix("/api", ui.API())))
	mux.Handle("GET /{$}", authn.Page(http.HandlerFunc(ui.Index)))

	return &Server{
		ws:        ws,
		token:     token,
		generated: generated,
		version:   cfg.Version,
		cfg:       cfg,
		procs:     procMgr,
		download:  downloader != nil,
		tools:     toolCount,
		http: &http.Server{
			Addr:              cfg.Addr,
			Handler:           logRequests(logger, mux),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
	}, nil
}

// ToolCount reports how many MCP tools the server exposes (0 if unknown).
func (s *Server) ToolCount() int { return s.tools }

// RunEnabled reports whether run_command and the process tools are on.
func (s *Server) RunEnabled() bool { return s.procs != nil }

// DownloadEnabled reports whether the download_file tool is on.
func (s *Server) DownloadEnabled() bool { return s.download }

// SensitiveGuard reports whether the sensitive-file guard is active.
func (s *Server) SensitiveGuard() bool { return !s.cfg.AllowSensitive }

// Start binds the listen address and serves in the background.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.listener = ln
	go func() {
		_ = s.http.Serve(ln)
	}()
	return nil
}

// Shutdown gracefully stops the server and every background process it owns.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.procs != nil {
		_ = s.procs.Shutdown(ctx)
	}
	return s.http.Shutdown(ctx)
}

// BaseURL is the HTTP origin the server is reachable at.
func (s *Server) BaseURL() string {
	addr := s.cfg.Addr
	if s.listener != nil {
		addr = s.listener.Addr().String()
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// MCPURL is the Streamable HTTP endpoint for MCP clients.
func (s *Server) MCPURL() string { return s.BaseURL() + "/mcp" }

// UIURL is the browser editor URL.
func (s *Server) UIURL() string { return s.BaseURL() + "/" }

// Token returns the active auth token.
func (s *Server) Token() string { return s.token }

// TokenGenerated reports whether the token was generated automatically.
func (s *Server) TokenGenerated() bool { return s.generated }

// Root returns the absolute workspace root.
func (s *Server) Root() string { return s.ws.Root() }

// Processes returns a snapshot of background processes, or nil when the run
// feature is disabled.
func (s *Server) Processes() []procman.Info {
	if s.procs == nil {
		return nil
	}
	return s.procs.List()
}

// StopAllProcesses stops every running background process.
func (s *Server) StopAllProcesses(ctx context.Context) []procman.Info {
	if s.procs == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.procs.StopAll(ctx, 5*time.Second)
}

// Addr returns the actual listen address (useful when cfg.Addr used port 0).
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.cfg.Addr
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"duration", time.Since(start).Round(time.Millisecond),
		)
	})
}

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
