// Package webui serves the embedded editor UI and its small REST API.
package webui

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/workspace"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFS embed.FS

const cookieMaxAge = 7 * 24 * 60 * 60 // seconds

// UI bundles the handlers for the browser interface.
type UI struct {
	ws      *workspace.Workspace
	auth    *auth.Authenticator
	version string
	procs   *procman.Manager
	// workspaces drives the workspace selector; nil hides it.
	workspaces Workspaces

	// uploadMax caps each uploaded file; logger receives upload activity.
	uploadMax int64
	logger    *slog.Logger

	loginMu       sync.Mutex
	loginFailures map[string]*loginAttempts
}

// loginAttempts tracks consecutive failed logins from one IP for throttling.
type loginAttempts struct {
	failures int
	window   time.Time // when the current penalty window expires
}

const (
	loginMaxFailures = 5
	loginWindow      = time.Minute
)

// New returns a UI backed by ws.
func New(ws *workspace.Workspace, a *auth.Authenticator, version string) *UI {
	return &UI{ws: ws, auth: a, version: version, loginFailures: map[string]*loginAttempts{}}
}

// SetProcesses attaches the background process manager. When set, the UI gains
// a process panel and its REST endpoints.
func (u *UI) SetProcesses(p *procman.Manager) { u.procs = p }

// Static serves embedded CSS/JS under /static/. Assets are cached for one
// hour; browsers revalidate afterwards.
func (u *UI) Static() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, r)
	}))
}

// Index serves the main editor page.
func (u *UI) Index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	u.serveFile(w, "index.html")
}

// LoginPage serves the login form.
func (u *UI) LoginPage(w http.ResponseWriter, r *http.Request) {
	if u.auth.Check(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	u.serveFile(w, "login.html")
}

// loginThrottled reports whether ip is in a penalty window for repeated
// failed logins.
func (u *UI) loginThrottled(ip string) bool {
	u.loginMu.Lock()
	defer u.loginMu.Unlock()
	now := time.Now()
	for k, v := range u.loginFailures {
		if now.After(v.window) {
			delete(u.loginFailures, k)
		}
	}
	v, ok := u.loginFailures[ip]
	return ok && now.Before(v.window) && v.failures >= loginMaxFailures
}

func (u *UI) recordLoginFailure(ip string) {
	u.loginMu.Lock()
	defer u.loginMu.Unlock()
	now := time.Now()
	v, ok := u.loginFailures[ip]
	if !ok || now.After(v.window) {
		v = &loginAttempts{window: now.Add(loginWindow)}
		u.loginFailures[ip] = v
	}
	v.failures++
	// Cap the map so it cannot grow without bound under spoofed IPs.
	if len(u.loginFailures) > 4096 {
		u.loginFailures = map[string]*loginAttempts{ip: v}
	}
}

func (u *UI) clearLoginFailures(ip string) {
	u.loginMu.Lock()
	defer u.loginMu.Unlock()
	delete(u.loginFailures, ip)
}

// clientIP returns the apparent client IP, honoring nothing beyond the
// direct peer (proxy headers are not trusted for throttling).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// LoginSubmit validates the submitted token and sets the session cookie.
func (u *UI) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	ip := clientIP(r)
	if u.loginThrottled(ip) {
		http.Error(w, "too many failed logins; try again in a minute", http.StatusTooManyRequests)
		return
	}
	if !u.auth.Validate(strings.TrimSpace(r.PostFormValue("token"))) {
		u.recordLoginFailure(ip)
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}
	u.clearLoginFailures(ip)
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	sessionID, err := u.auth.StartSession()
	if err != nil {
		http.Error(w, "could not start session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     u.auth.CookieName(),
		Value:    sessionID,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		Secure:   secure,
		// Lax, not Strict: the OAuth consent page is reached through a
		// cross-site top-level navigation from the MCP client. Strict
		// withholds the cookie on that GET, so a signed-in user would be
		// shown the token form. Lax still withholds it from cross-site
		// state-changing requests (POST), which the consent handler
		// additionally guards with a CSRF token.
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Logout revokes the UI session server-side and clears the cookie.
func (u *UI) Logout(w http.ResponseWriter, r *http.Request) {
	u.auth.EndSession(r)
	http.SetCookie(w, &http.Cookie{
		Name:     u.auth.CookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (u *UI) serveFile(w http.ResponseWriter, name string) {
	data, err := webFS.ReadFile("web/" + name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// API returns the REST handler mounted at /api/. Every endpoint except the
// workspace selector holds the workspace while it runs, so a switch never
// happens in the middle of a file request.
func (u *UI) API() http.Handler {
	top := http.NewServeMux()
	u.addWorkspaceRoutes(top)
	u.addSettingsRoutes(top)
	top.Handle("/", u.holdWorkspace(u.filesAPI()))
	return top
}

func (u *UI) addSettingsRoutes(mux *http.ServeMux) {
	// Settings only reads/writes config.json (and flips the sensitive
	// guard on the live workspace); it never touches workspace files, so
	// it must NOT be mounted under holdWorkspace — a request that holds
	// the workspace gate and then asks for a write lock deadlocks.
	mux.HandleFunc("GET /settings", u.handleGetSettings)
	mux.HandleFunc("PUT /settings", u.handlePutSettings)
}

func (u *UI) filesAPI() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", u.handleInfo)
	mux.HandleFunc("GET /tree", u.handleTree)
	mux.HandleFunc("GET /file", u.handleGetFile)
	mux.HandleFunc("PUT /file", u.handlePutFile)
	mux.HandleFunc("DELETE /file", u.handleDelete)
	mux.HandleFunc("POST /mkdir", u.handleMkdir)
	mux.HandleFunc("POST /rename", u.handleRename)
	mux.HandleFunc("GET /search", u.handleSearch)
	mux.HandleFunc("GET /stats", u.handleStats)
	mux.HandleFunc("GET /validate", u.handleValidate)
	mux.HandleFunc("POST /search-replace", u.handleSearchReplace)
	mux.HandleFunc("POST /upload", u.handleUpload)
	if u.procs != nil {
		mux.HandleFunc("GET /processes", u.handleProcesses)
		mux.HandleFunc("GET /processes/{name}/logs", u.handleProcessLogs)
		mux.HandleFunc("POST /processes/{name}/stop", u.handleStopProcess)
		mux.HandleFunc("POST /processes/{name}/restart", u.handleRestartProcess)
		mux.HandleFunc("POST /processes/stop-all", u.handleStopAllProcesses)
	}
	return mux
}

// maxLogTail caps how many lines one logs request returns.
const maxLogTail = 2000

func queryInt(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	return strconv.Atoi(s)
}

var errWrongContentType = errStr("Content-Type must be application/json")

// jsonContentType reports whether the request carries a JSON content type
// (application/json, any parameters).
func jsonContentType(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	// strip parameters
	if i := strings.IndexByte(ct, ';'); i != -1 {
		ct = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(ct), "application/json")
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	if !jsonContentType(r) {
		return errWrongContentType
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

// decodeOptionalJSON is decodeJSON for endpoints whose body may be omitted.
func decodeOptionalJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	if !jsonContentType(r) && r.ContentLength != 0 {
		return errWrongContentType
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	writeJSON(w, errStatus(err), map[string]string{"error": err.Error()})
}

// errStatus maps workspace and process errors to HTTP status codes.
func errStatus(err error) int {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, errWrongContentType):
		status = http.StatusUnsupportedMediaType
	case errors.Is(err, workspace.ErrOutsideRoot), errors.Is(err, workspace.ErrSensitive):
		status = http.StatusForbidden
	case errors.Is(err, workspace.ErrExists):
		status = http.StatusConflict
	case errors.Is(err, workspace.ErrStreamTooLarge):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(err, os.ErrNotExist):
		status = http.StatusNotFound
	case errors.Is(err, workspace.ErrIsDir), errors.Is(err, workspace.ErrNotDir):
		status = http.StatusBadRequest
	case errors.Is(err, procman.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, procman.ErrExists), errors.Is(err, procman.ErrTooMany), errors.Is(err, procman.ErrClosed):
		status = http.StatusConflict
	}
	return status
}
