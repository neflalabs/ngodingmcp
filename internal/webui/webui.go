// Package webui serves the embedded editor UI and its small REST API.
package webui

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/workspace"
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

	// uploadMax caps each uploaded file; logger receives upload activity.
	uploadMax int64
	logger    *slog.Logger
}

// New returns a UI backed by ws.
func New(ws *workspace.Workspace, a *auth.Authenticator, version string) *UI {
	return &UI{ws: ws, auth: a, version: version}
}

// SetProcesses attaches the background process manager. When set, the UI gains
// a process panel and its REST endpoints.
func (u *UI) SetProcesses(p *procman.Manager) { u.procs = p }

// Static serves embedded CSS/JS under /static/.
func (u *UI) Static() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
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

// LoginSubmit validates the submitted token and sets the session cookie.
func (u *UI) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !u.auth.Validate(strings.TrimSpace(r.PostFormValue("token"))) {
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     u.auth.CookieName(),
		Value:    strings.TrimSpace(r.PostFormValue("token")),
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Logout clears the session cookie.
func (u *UI) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     u.auth.CookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
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

// API returns the REST handler mounted at /api/.
func (u *UI) API() http.Handler {
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

func (u *UI) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := u.ws.Stats()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (u *UI) handleValidate(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	res, err := u.ws.ValidateSyntax(p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type searchReplaceReq struct {
	Path        string `json:"path"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Glob        string `json:"glob"`
	DryRun      bool   `json:"dryRun"`
	MaxFiles    int    `json:"maxFiles"`
}

func (u *UI) handleSearchReplace(w http.ResponseWriter, r *http.Request) {
	var req searchReplaceReq
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	res, err := u.ws.SearchReplace(req.Path, req.Pattern, req.Replacement, req.Glob, req.DryRun, req.MaxFiles)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type infoResponse struct {
	Root      string `json:"root"`
	Version   string `json:"version"`
	MaxRead   int64  `json:"maxReadBytes"`
	Processes bool   `json:"processesEnabled"`
	UploadMax int64  `json:"uploadMaxBytes"`
}

func (u *UI) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, infoResponse{
		Root:      u.ws.Root(),
		Version:   u.version,
		MaxRead:   u.ws.MaxRead(),
		Processes: u.procs != nil,
		UploadMax: u.uploadLimit(),
	})
}

// --- background processes --------------------------------------------------

func (u *UI) handleProcesses(w http.ResponseWriter, r *http.Request) {
	procs := u.procs.List()
	if procs == nil {
		procs = []procman.Info{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"processes": procs})
}

type stopProcessRequest struct {
	Wait         *bool `json:"wait"`
	GraceSeconds int   `json:"graceSeconds"`
}

func (u *UI) handleStopProcess(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing process name"})
		return
	}
	var req stopProcessRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	grace := time.Duration(req.GraceSeconds) * time.Second
	wait := req.Wait != nil && *req.Wait
	info, err := u.procs.Stop(name, grace, wait)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// maxLogTail caps how many lines one logs request returns.
const maxLogTail = 2000

// handleProcessLogs returns a process's output. Pass after=<nextSeq> from the
// previous response to receive only newer lines; tail limits the first read.
func (u *UI) handleProcessLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	q := r.URL.Query()
	tail, err := queryInt(q.Get("tail"), 200)
	if err != nil || tail < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tail must be a non-negative integer"})
		return
	}
	if tail > maxLogTail {
		tail = maxLogTail
	}
	after, err := queryInt(q.Get("after"), 0)
	if err != nil || after < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "after must be a non-negative integer"})
		return
	}
	info, logs, err := u.procs.Logs(name, tail, int64(after))
	if err != nil {
		writeErr(w, err)
		return
	}
	if logs.Lines == nil {
		logs.Lines = []procman.LogLine{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"process": info, "logs": logs})
}

func (u *UI) handleRestartProcess(w http.ResponseWriter, r *http.Request) {
	var req stopProcessRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	info, err := u.procs.Restart(r.PathValue("name"), time.Duration(req.GraceSeconds)*time.Second)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func queryInt(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	return strconv.Atoi(s)
}

func (u *UI) handleStopAllProcesses(w http.ResponseWriter, r *http.Request) {
	var req stopProcessRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	grace := time.Duration(req.GraceSeconds) * time.Second
	procs := u.procs.StopAll(r.Context(), grace)
	if procs == nil {
		procs = []procman.Info{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"processes": procs})
}

func (u *UI) handleTree(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	recursive := r.URL.Query().Get("recursive") == "1"
	entries, err := u.ws.ListFiles(dir, recursive)
	if err != nil {
		writeErr(w, err)
		return
	}
	if entries == nil {
		entries = []workspace.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": dir, "entries": entries})
}

func (u *UI) handleGetFile(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	res, err := u.ws.ReadFile(p, 0, 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type putFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (u *UI) handlePutFile(w http.ResponseWriter, r *http.Request) {
	var req putFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p, n, err := u.ws.WriteFile(req.Path, req.Content)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "bytes": n})
}

func (u *UI) handleDelete(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	recursive := r.URL.Query().Get("recursive") == "1"
	if err := u.ws.Delete(p, recursive); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": p})
}

type pathRequest struct {
	Path string `json:"path"`
}

func (u *UI) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var req pathRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p, err := u.ws.Mkdir(req.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": p})
}

type renameRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (u *UI) handleRename(w http.ResponseWriter, r *http.Request) {
	var req renameRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p, err := u.ws.Rename(req.From, req.To)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": p})
}

func (u *UI) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	matches, err := u.ws.SearchText(q.Get("path"), q.Get("q"), q.Get("glob"), 200)
	if err != nil {
		writeErr(w, err)
		return
	}
	if matches == nil {
		matches = []workspace.Match{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": matches})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

// decodeOptionalJSON is decodeJSON for endpoints whose body may be omitted.
func decodeOptionalJSON(r *http.Request, v any) error {
	defer r.Body.Close()
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
