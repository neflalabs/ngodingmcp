package webui

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"

	"ngodingmcp/internal/mcpserver"
	"ngodingmcp/internal/workspace"
)

const (
	// DefaultUploadMaxBytes caps each uploaded file (20 MiB).
	DefaultUploadMaxBytes int64 = 20 << 20
	// maxUploadFiles caps the number of files in one request.
	maxUploadFiles = 20
	// uploadOverhead allows for multipart headers and boundaries.
	uploadOverhead = 1 << 20
)

// errBadFilename is returned for empty, hidden-traversal or path-like names.
var errBadFilename = errors.New("invalid file name: use a plain name without '/', '\\', '..' or control characters")

// SetUploads sets the per-file upload limit (<= 0 keeps the default) and the
// logger that receives one activity line per uploaded file.
func (u *UI) SetUploads(maxBytes int64, logger *slog.Logger) {
	if maxBytes > 0 {
		u.uploadMax = maxBytes
	}
	u.logger = logger
}

func (u *UI) uploadLimit() int64 {
	if u.uploadMax > 0 {
		return u.uploadMax
	}
	return DefaultUploadMaxBytes
}

// uploadResult is the per-file outcome of POST /api/upload.
type uploadResult struct {
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Error  string `json:"error,omitempty"`
	Status int    `json:"status"`
}

// handleUpload streams multipart "file" parts into ?dir= (workspace-relative,
// default root). Each file is written atomically with workspace.WriteStream,
// so the size limit is enforced on the bytes read and a failed file leaves
// nothing behind. Existing files are kept unless ?overwrite=true; a conflict
// answers 409. Every file is attempted; the response lists each outcome and
// the HTTP status is 200 only when all files succeeded, else the status of
// the first failure.
func (u *UI) handleUpload(w http.ResponseWriter, r *http.Request) {
	limit := u.uploadLimit()
	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	overwrite, _ := parseBool(r.URL.Query().Get("overwrite"))

	if strings.HasPrefix(dir, "/") || strings.HasPrefix(dir, "\\") || strings.Contains(dir, ":") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "dir must be a path relative to the workspace root"})
		return
	}
	if dir == "" {
		dir = "."
	}
	cleanDir, err := u.ws.CleanPath(dir)
	if err != nil {
		writeErr(w, u.ws.SafeError(err))
		return
	}

	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected multipart/form-data with one or more \"file\" fields"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxUploadFiles)*limit+uploadOverhead)
	mr, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart body"})
		return
	}

	var results []uploadResult
	status := http.StatusOK
	fail := func(res uploadResult, code int, err error) {
		res.Status = code
		res.Error = u.ws.SafeError(err).Error()
		results = append(results, res)
		if status == http.StatusOK {
			status = code
		}
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				fail(uploadResult{}, http.StatusRequestEntityTooLarge, errors.New("request body too large"))
			} else {
				fail(uploadResult{}, http.StatusBadRequest, errors.New("invalid multipart body"))
			}
			break
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		name := rawFilename(part)
		if len(results) >= maxUploadFiles {
			part.Close()
			fail(uploadResult{Name: name}, http.StatusBadRequest, fmt.Errorf("at most %d files per upload", maxUploadFiles))
			break
		}

		started := time.Now()
		res := uploadResult{Name: name}
		if !safeFilename(name) {
			part.Close()
			fail(res, http.StatusBadRequest, errBadFilename)
			u.logUpload(name, false, "bad name", time.Since(started))
			continue
		}
		dest := path.Join(cleanDir, name)
		out, err := u.ws.WriteStream(dest, part, workspace.StreamOptions{MaxBytes: limit, Overwrite: overwrite})
		part.Close()
		if err != nil {
			code := errStatus(err)
			var mbe *http.MaxBytesError
			bodyTooLarge := errors.As(err, &mbe)
			if bodyTooLarge {
				code, err = http.StatusRequestEntityTooLarge, errors.New("request body too large")
			}
			fail(res, code, err)
			u.logUpload(dest, false, http.StatusText(code), time.Since(started))
			if bodyTooLarge {
				break // the rest of the body cannot be read
			}
			continue
		}
		res.Path, res.Bytes, res.SHA256, res.Status = out.Path, out.Bytes, out.SHA256, http.StatusOK
		results = append(results, res)
		u.logUpload(out.Path, true, humanBytes(out.Bytes), time.Since(started))
	}

	if len(results) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no files in the request (use the \"file\" field)"})
		return
	}
	body := map[string]any{"dir": cleanDir, "files": results}
	if status != http.StatusOK {
		for _, res := range results {
			if res.Error != "" {
				body["error"] = res.Name + ": " + res.Error
				break
			}
		}
	}
	writeJSON(w, status, body)
}

// rawFilename returns the filename exactly as the client sent it.
// multipart.Part.FileName applies filepath.Base, which would silently turn
// "../x" into "x"; a path-like name is refused instead of rewritten.
func rawFilename(p *multipart.Part) string {
	_, params, err := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
	if err != nil {
		return ""
	}
	return params["filename"]
}

// safeFilename accepts a plain base name only.
func safeFilename(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	if strings.ContainsAny(name, "/\\:") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// logUpload writes one activity line in the same format as MCP tool calls.
// Only the destination path, outcome, size and duration are logged.
func (u *UI) logUpload(target string, ok bool, detail string, d time.Duration) {
	if u.logger == nil {
		return
	}
	target = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, target)
	if len(target) > 60 {
		target = "…" + target[len(target)-59:]
	}
	line := mcpserver.ActivityPrefix + mcpserver.ActivityLine("web_upload", target, ok, detail, d)
	if ok {
		u.logger.Info(line)
	} else {
		u.logger.Warn(line)
	}
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "", "0", "false", "no", "off":
		return false, nil
	}
	return false, errors.New("invalid boolean")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
