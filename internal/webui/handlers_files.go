package webui

import (
	"net/http"
	"ngodingmcp/internal/buildinfo"
	"ngodingmcp/internal/workspace"
	"time"
)

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
		writeErr(w, err)
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
	Root       string `json:"root"`
	Version    string `json:"version"`
	Built      string `json:"built,omitempty"` // RFC 3339 build time of the binary
	MaxRead    int64  `json:"maxReadBytes"`
	Processes  bool   `json:"processesEnabled"`
	UploadMax  int64  `json:"uploadMaxBytes"`
	Workspaces bool   `json:"workspacesEnabled"`
}

func (u *UI) handleInfo(w http.ResponseWriter, r *http.Request) {
	var built string
	if t := buildinfo.Get().BuildTime; !t.IsZero() {
		built = t.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, infoResponse{
		Root:       u.ws.Root(),
		Version:    u.version,
		Built:      built,
		MaxRead:    u.ws.MaxRead(),
		Processes:  u.procs != nil,
		UploadMax:  u.uploadLimit(),
		Workspaces: u.workspaces != nil,
	})
}

// --- background processes --------------------------------------------------

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
		writeErr(w, err)
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
		writeErr(w, err)
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
		writeErr(w, err)
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
