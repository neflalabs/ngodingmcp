package webui

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/workspace"
)

type uploadAPI struct {
	handler http.Handler
	root    string
	logs    *bytes.Buffer
}

func newUploadAPI(t *testing.T, maxBytes int64) uploadAPI {
	t.Helper()
	root := t.TempDir()
	ws, err := workspace.New(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	authn := auth.New(testToken, "session")
	ui := New(ws, authn, "test")
	ui.SetUploads(maxBytes, slog.New(slog.NewTextHandler(logs, nil)))
	mux := http.NewServeMux()
	mux.Handle("/api/", authn.API(http.StripPrefix("/api", ui.API())))
	return uploadAPI{handler: mux, root: root, logs: logs}
}

type uploadFile struct {
	name, content string
}

func (a uploadAPI) post(t *testing.T, query string, authed bool, files ...uploadFile) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, f := range files {
		// CreateFormFile escapes quotes only, so a name like "../x" reaches
		// the server unchanged, as a hostile client would send it.
		fw, err := mw.CreateFormFile("file", f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(f.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload"+query, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if authed {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func (a uploadAPI) assertNoTemp(t *testing.T) {
	t.Helper()
	_ = filepath.Walk(a.root, func(p string, info os.FileInfo, err error) error {
		if err == nil && strings.HasPrefix(info.Name(), ".ngodingmcp-") {
			t.Errorf("temp file left behind: %s", p)
		}
		return nil
	})
}

func TestUploadRequiresAuth(t *testing.T) {
	a := newUploadAPI(t, 1024)
	rec, _ := a.post(t, "?dir=.", false, uploadFile{"a.txt", "hi"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(a.root, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("file written without auth")
	}
}

func TestUploadSuccessAndActivityLog(t *testing.T) {
	a := newUploadAPI(t, 1024)
	const secret = "SECRET-CONTENT-42"
	rec, out := a.post(t, "?dir=static/images", true, uploadFile{"hero.webp", secret})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	got, err := os.ReadFile(filepath.Join(a.root, "static", "images", "hero.webp"))
	if err != nil || string(got) != secret {
		t.Fatalf("file = %q, %v", got, err)
	}
	files := out["files"].([]any)
	f := files[0].(map[string]any)
	if f["path"] != "static/images/hero.webp" || f["bytes"].(float64) != float64(len(secret)) {
		t.Fatalf("result = %v", f)
	}
	log := a.logs.String()
	if !strings.Contains(log, "web_upload") || !strings.Contains(log, "static/images/hero.webp") {
		t.Fatalf("activity log missing upload line: %s", log)
	}
	if strings.Contains(log, secret) || strings.Contains(log, a.root) {
		t.Fatalf("activity log leaks content or host path: %s", log)
	}
}

func TestUploadMultipleFiles(t *testing.T) {
	a := newUploadAPI(t, 1024)
	rec, out := a.post(t, "", true, uploadFile{"one.txt", "1"}, uploadFile{"two.bin", "\x00\x01\x02"}, uploadFile{"three.txt", "333"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	if n := len(out["files"].([]any)); n != 3 {
		t.Fatalf("files = %d, want 3", n)
	}
	for _, name := range []string{"one.txt", "two.bin", "three.txt"} {
		if _, err := os.Stat(filepath.Join(a.root, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	bin, _ := os.ReadFile(filepath.Join(a.root, "two.bin"))
	if !bytes.Equal(bin, []byte{0, 1, 2}) {
		t.Fatalf("binary content changed: %v", bin)
	}
}

func TestUploadTooLargeLeavesNothing(t *testing.T) {
	a := newUploadAPI(t, 10)
	rec, _ := a.post(t, "", true, uploadFile{"big.bin", strings.Repeat("x", 11)})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(a.root, "big.bin")); !os.IsNotExist(err) {
		t.Fatal("partial file left behind")
	}
	a.assertNoTemp(t)
	// Exactly at the limit is fine.
	rec, _ = a.post(t, "", true, uploadFile{"ok.bin", strings.Repeat("x", 10)})
	if rec.Code != http.StatusOK {
		t.Fatalf("at-limit status = %d", rec.Code)
	}
}

func TestUploadTraversalRejected(t *testing.T) {
	a := newUploadAPI(t, 1024)
	cases := []struct {
		query string
		name  string
	}{
		{"?dir=..", "x.txt"},
		{"?dir=../outside", "x.txt"},
		{"?dir=/etc", "x.txt"},
		{"", "../x.txt"},
		{"", "sub/x.txt"},
		{"", `..\x.txt`},
		{"", ".."},
	}
	for _, c := range cases {
		rec, out := a.post(t, c.query, true, uploadFile{c.name, "x"})
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusForbidden {
			t.Errorf("dir %q name %q: status = %d, want 400/403", c.query, c.name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), a.root) {
			t.Errorf("error leaks host path: %v", out)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(a.root), "x.txt")); !os.IsNotExist(err) {
		t.Fatal("file escaped the workspace")
	}
}

func TestUploadSensitiveDestination(t *testing.T) {
	a := newUploadAPI(t, 1024)
	for _, c := range []struct{ query, name string }{
		{"", ".env"},
		{"", "id_rsa"},
		{"", "server.pem"},
		{"?dir=.git", "config"},
	} {
		rec, _ := a.post(t, c.query, true, uploadFile{c.name, "SECRET=1"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s%s: status = %d, want 403; body=%s", c.query, c.name, rec.Code, rec.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(a.root, ".env")); !os.IsNotExist(err) {
		t.Fatal(".env was written")
	}
	// Templates stay allowed.
	if rec, _ := a.post(t, "", true, uploadFile{".env.example", "A="}); rec.Code != http.StatusOK {
		t.Fatalf(".env.example status = %d", rec.Code)
	}
}

func TestUploadConflictWithoutOverwrite(t *testing.T) {
	a := newUploadAPI(t, 1024)
	dest := filepath.Join(a.root, "a.txt")
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, out := a.post(t, "", true, uploadFile{"a.txt", "new"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(out["error"].(string), "overwrite") {
		t.Fatalf("conflict error should mention overwrite: %v", out)
	}
	if got, _ := os.ReadFile(dest); string(got) != "old" {
		t.Fatalf("file replaced without overwrite: %q", got)
	}
	rec, _ = a.post(t, "?overwrite=true", true, uploadFile{"a.txt", "new"})
	if rec.Code != http.StatusOK {
		t.Fatalf("overwrite status = %d", rec.Code)
	}
	if got, _ := os.ReadFile(dest); string(got) != "new" {
		t.Fatalf("overwrite did not replace: %q", got)
	}
}

func TestUploadMixedResultsKeepGoodFiles(t *testing.T) {
	a := newUploadAPI(t, 1024)
	rec, out := a.post(t, "", true, uploadFile{"good.txt", "ok"}, uploadFile{".env", "S=1"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want first failure (403)", rec.Code)
	}
	files := out["files"].([]any)
	if len(files) != 2 || files[0].(map[string]any)["status"].(float64) != 200 {
		t.Fatalf("results = %v", files)
	}
	if _, err := os.Stat(filepath.Join(a.root, "good.txt")); err != nil {
		t.Fatal("good file not written")
	}
}
