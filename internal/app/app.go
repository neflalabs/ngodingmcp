// Package app wires the workspace, auth, MCP server and web UI into a single
// runnable HTTP server. It is shared by the CLI and the TUI.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/config"
	"ngodingmcp/internal/fetch"
	"ngodingmcp/internal/mcpserver"
	"ngodingmcp/internal/oauth"
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
	// PublicURL is the external base URL of this server, used as the OAuth
	// issuer/resource identifier (e.g. "https://mcp.example.com"). Empty
	// derives it per request from the Host header.
	PublicURL string
	// TrustedProxies names the peers whose X-Forwarded-Proto header is
	// honored when deriving the OAuth issuer. Empty means "loopback".
	TrustedProxies []string
	// OAuthDisabled turns off the embedded OAuth authorization server. The
	// static token still works; MCP clients must then authenticate with it.
	OAuthDisabled bool
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
	logger    *slog.Logger
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
	wsManager := &workspaceManager{
		ws:       ws,
		runner:   cmdRunner,
		procs:    procMgr,
		defaults: webui.WorkspaceDefaults{AllowSensitive: cfg.AllowSensitive, MaxFileBytes: ws.MaxRead()},
		logger:   logger,
	}
	// The start root is usually the workspace last opened in the Web UI
	// (config.Config.Root); bring back its per-workspace settings too.
	if name, changed := wsManager.restoreSaved(config.Load().Workspaces); changed {
		logger.Info("restored saved workspace settings", "name", name)
	}
	ui.SetWorkspaces(wsManager)

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

	var oauthServer *oauth.Server
	var mcpAuth func(http.Handler) http.Handler
	if !cfg.OAuthDisabled {
		oauthServer, err = oauth.New(oauth.Config{
			PublicURL:      cfg.PublicURL,
			TrustedProxies: cfg.TrustedProxies,
			Auth:           authn,
			Logger:         logger,
		})
		if err != nil {
			return nil, fmt.Errorf("init OAuth: %w", err)
		}
		mcpAuth = oauthServer.MCP
		logOAuthIssuer(logger, cfg)
	} else {
		logger.Warn("OAuth disabled; /mcp accepts only the static token")
		mcpAuth = authn.API
	}

	mux := http.NewServeMux()
	mux.Handle("GET /login", http.HandlerFunc(ui.LoginPage))
	mux.Handle("POST /login", http.HandlerFunc(ui.LoginSubmit))
	mux.Handle("POST /logout", http.HandlerFunc(ui.Logout))
	mux.Handle("GET /static/", ui.Static())
	if oauthServer != nil {
		oauthServer.RegisterRoutes(mux)
	}
	mux.Handle("/mcp", mcpAuth(mcpHandler))
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
		logger:    logger,
		http: &http.Server{
			Addr:              cfg.Addr,
			Handler:           oauth.GuardHost(logRequests(logger, mux)),
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
func (s *Server) SensitiveGuard() bool { return !s.ws.AllowSensitive() }

// Start binds the listen address and serves in the background. A serve
// error (other than the listener being closed during shutdown) is logged;
// shut down and inspect the log when that happens, as the process keeps
// running without the HTTP listener.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.listener = ln
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("HTTP listener failed", "addr", s.cfg.Addr, "err", err)
		}
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
	return BaseURLFor(addr)
}

// logOAuthIssuer warns when OAuth is enabled but cannot present a stable
// external identity (local-only derivation from the Host header).
func logOAuthIssuer(logger *slog.Logger, cfg Config) {
	if cfg.PublicURL != "" {
		logger.Info("OAuth enabled", "issuer", cfg.PublicURL)
		return
	}
	logger.Info("OAuth enabled; issuer is derived per request (Host header; X-Forwarded-Proto honored only from trusted proxies)")
	if !isLoopbackAddr(cfg.Addr) {
		logger.Warn("OAuth without -public-url on a non-loopback address; MCP clients need a stable https origin")
	}
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	// An empty host is a wildcard listen (0.0.0.0 / ::), i.e. reachable
	// from the network, not loopback.
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// BaseURLFor is the HTTP origin for a listen address; a wildcard host
// ("0.0.0.0", "::" or empty) becomes 127.0.0.1.
func BaseURLFor(addr string) string {
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

// IsAddrInUse reports whether err comes from binding a listen address that
// another process (often the background service) already uses.
func IsAddrInUse(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "only one usage of each socket address")
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

// statusRecorder captures only the response status code for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	size   int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.size += n
	return n, err
}

func (r *statusRecorder) Flush() {
	// The streaming endpoint (SSE) needs Flush to pass through, otherwise the
	// client never receives events.
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sr, r)
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"status", sr.status,
			"bytes", sr.size,
			"duration", time.Since(start).Round(time.Millisecond),
		)
	})
}

// NewToken returns a fresh random auth token, the same kind New generates
// when Config.Token is empty.
func NewToken() (string, error) { return randomToken() }

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
