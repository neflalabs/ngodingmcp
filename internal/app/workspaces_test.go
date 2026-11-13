package app

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/config"
	"ngodingmcp/internal/webui"
	"ngodingmcp/internal/workspace"
)

const wsTestToken = "test-token-0123456789abcdef"

type wsHarness struct {
	t       *testing.T
	ws      *workspace.Workspace
	handler http.Handler
	a, b    string // resolved roots of two project folders
}

func newWSHarness(t *testing.T) *wsHarness {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, b := t.TempDir(), t.TempDir()
	for dir, name := range map[string]string{a: "a.txt", b: "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(b, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.New(a, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ra, _ := workspace.ResolveRoot(a)
	rb, _ := workspace.ResolveRoot(b)

	authn := auth.New(wsTestToken, "session")
	ui := webui.New(ws, authn, "test")
	ui.SetWorkspaces(&workspaceManager{
		ws:       ws,
		defaults: webui.WorkspaceDefaults{AllowSensitive: false, MaxFileBytes: 1 << 20},
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	mux := http.NewServeMux()
	mux.Handle("/api/", authn.API(http.StripPrefix("/api", ui.API())))
	return &wsHarness{t: t, ws: ws, handler: mux, a: ra, b: rb}
}

func (h *wsHarness) do(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, "/api"+path, r)
	req.Header.Set("Authorization", "Bearer "+wsTestToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestWorkspaceSelectorFlow(t *testing.T) {
	h := newWSHarness(t)

	// Empty list; active root is the start folder, not saved yet.
	code, out := h.do("GET", "/workspaces", nil)
	if code != 200 || out["activeRoot"] != h.a || out["activeName"] != "" {
		t.Fatalf("initial list: %d %v", code, out)
	}

	// Save both folders; the first one is the active one.
	if code, out = h.do("POST", "/workspaces", map[string]any{"name": "mcp", "path": h.a}); code != 201 {
		t.Fatalf("save mcp: %d %v", code, out)
	}
	if out["activeName"] != "mcp" {
		t.Errorf("activeName = %v, want mcp", out["activeName"])
	}
	if code, out = h.do("POST", "/workspaces", map[string]any{"name": "sekolah", "path": h.b, "maxFileBytes": 4096}); code != 201 {
		t.Fatalf("save sekolah: %d %v", code, out)
	}
	if code, _ = h.do("POST", "/workspaces", map[string]any{"name": "sekolah", "path": h.a}); code != 409 {
		t.Errorf("duplicate name: code %d, want 409", code)
	}

	// Switch to sekolah: file API now sees the other folder with its settings.
	code, out = h.do("POST", "/workspaces/sekolah/activate", nil)
	if code != 200 || out["root"] != h.b {
		t.Fatalf("activate: %d %v", code, out)
	}
	if h.ws.Root() != h.b || h.ws.MaxRead() != 4096 {
		t.Errorf("after switch root=%q maxRead=%d", h.ws.Root(), h.ws.MaxRead())
	}
	if code, _ = h.do("GET", "/file?path=b.txt", nil); code != 200 {
		t.Errorf("read b.txt in new workspace: %d", code)
	}
	if code, _ = h.do("GET", "/file?path=a.txt", nil); code == 200 {
		t.Error("a.txt from the old workspace is still readable")
	}
	// The sensitive guard is inherited (on) for the new workspace.
	if code, _ = h.do("GET", "/file?path=.env", nil); code == 200 {
		t.Error(".env readable although the guard is inherited as on")
	}
	// The active workspace is remembered for the next start.
	if got := config.Load().Root; got != h.b {
		t.Errorf("config root = %q, want %q", got, h.b)
	}

	// Editing the active workspace applies its settings at once.
	if code, out = h.do("PUT", "/workspaces/sekolah", map[string]any{"name": "sekolah", "path": h.b, "allowSensitive": true}); code != 200 {
		t.Fatalf("update active: %d %v", code, out)
	}
	if !h.ws.AllowSensitive() {
		t.Error("allowSensitive override not applied to the active workspace")
	}
	// maxFileBytes was cleared, so it inherits the global default (1 MiB here).
	if h.ws.MaxRead() != 1<<20 {
		t.Errorf("maxRead = %d, want inherited 1 MiB", h.ws.MaxRead())
	}
	if code, _ = h.do("GET", "/file?path=.env", nil); code != 200 {
		t.Errorf(".env with per-workspace allow: code %d", code)
	}
	// The folder of the active workspace cannot be changed in place.
	if code, _ = h.do("PUT", "/workspaces/sekolah", map[string]any{"name": "sekolah", "path": h.a}); code != 400 {
		t.Errorf("change folder of active workspace: code %d, want 400", code)
	}

	// Rename + delete.
	if code, out = h.do("PUT", "/workspaces/mcp", map[string]any{"name": "ngodingmcp", "path": h.a}); code != 200 {
		t.Fatalf("rename: %d %v", code, out)
	}
	if code, _ = h.do("POST", "/workspaces/mcp/activate", nil); code != 404 {
		t.Errorf("activate old name: %d, want 404", code)
	}
	if code, _ = h.do("POST", "/workspaces/"+url.PathEscape("ngodingmcp")+"/activate", nil); code != 200 {
		t.Errorf("activate renamed: %d", code)
	}
	if h.ws.Root() != h.a {
		t.Errorf("root after switching back = %q", h.ws.Root())
	}
	if code, out = h.do("DELETE", "/workspaces/sekolah", nil); code != 200 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if list, _ := out["workspaces"].([]any); len(list) != 1 {
		t.Errorf("workspaces after delete = %v", out["workspaces"])
	}
	if _, err := os.Stat(filepath.Join(h.b, "b.txt")); err != nil {
		t.Error("deleting a saved workspace touched its folder")
	}
}

func TestWorkspaceSelectorValidation(t *testing.T) {
	h := newWSHarness(t)
	file := filepath.Join(h.a, "a.txt")
	cases := map[string]map[string]any{
		"empty name":    {"name": " ", "path": h.b},
		"slash in name": {"name": "a/b", "path": h.b},
		"relative path": {"name": "x", "path": "rel/dir"},
		"missing path":  {"name": "x", "path": filepath.Join(h.b, "nope")},
		"file path":     {"name": "x", "path": file},
		"fs root":       {"name": "x", "path": "/"},
		"negative max":  {"name": "x", "path": h.b, "maxFileBytes": -1},
	}
	for name, body := range cases {
		if code, out := h.do("POST", "/workspaces", body); code != 400 {
			t.Errorf("%s: code %d (%v), want 400", name, code, out)
		}
	}
	if code, _ := h.do("POST", "/workspaces/nope/activate", nil); code != 404 {
		t.Errorf("activate unknown: %d, want 404", code)
	}

	// A saved folder that disappears is flagged and cannot be opened.
	gone := t.TempDir()
	if code, _ := h.do("POST", "/workspaces", map[string]any{"name": "gone", "path": gone}); code != 201 {
		t.Fatal("save gone")
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	_, out := h.do("GET", "/workspaces", nil)
	list, _ := out["workspaces"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["missing"] != true {
		t.Errorf("missing flag: %v", out["workspaces"])
	}
	before := h.ws.Root()
	if code, _ := h.do("POST", "/workspaces/gone/activate", nil); code != 400 {
		t.Errorf("activate missing folder: %d, want 400", code)
	}
	if h.ws.Root() != before {
		t.Error("root changed on a failed switch")
	}
}

// A restarted server (for example the systemd service after a reboot) opens
// the workspace last chosen in the Web UI, with that workspace's own settings.
func TestRestartRestoresActiveWorkspace(t *testing.T) {
	h := newWSHarness(t)
	body := map[string]any{"name": "sekolah", "path": h.b, "maxFileBytes": 4096, "allowSensitive": true}
	if code, out := h.do("POST", "/workspaces", body); code != 201 {
		t.Fatalf("save: %d %v", code, out)
	}
	if code, out := h.do("POST", "/workspaces/sekolah/activate", nil); code != 200 {
		t.Fatalf("activate: %d %v", code, out)
	}

	// What `ngodingmcp serve` does on start: the root comes from the config.
	srv, err := New(Config{
		Addr:         "127.0.0.1:0",
		Root:         config.Load().Root,
		Token:        wsTestToken,
		MaxFileBytes: 1 << 20,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if srv.Root() != h.b {
		t.Errorf("root after restart = %q, want %q", srv.Root(), h.b)
	}
	if srv.ws.MaxRead() != 4096 || !srv.ws.AllowSensitive() {
		t.Errorf("settings after restart: maxRead=%d allowSensitive=%v, want 4096 true", srv.ws.MaxRead(), srv.ws.AllowSensitive())
	}

	// A root that is not a saved workspace keeps the start settings.
	other, err := New(Config{Addr: "127.0.0.1:0", Root: h.a, Token: wsTestToken, MaxFileBytes: 1 << 20,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if other.ws.MaxRead() != 1<<20 || other.ws.AllowSensitive() {
		t.Errorf("unsaved root got overrides: maxRead=%d allowSensitive=%v", other.ws.MaxRead(), other.ws.AllowSensitive())
	}
}

func TestIsAddrInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, err = net.Listen("tcp", ln.Addr().String())
	if !IsAddrInUse(err) {
		t.Errorf("IsAddrInUse(%v) = false", err)
	}
	if IsAddrInUse(nil) || IsAddrInUse(os.ErrNotExist) {
		t.Error("IsAddrInUse true for an unrelated error")
	}
	if got := BaseURLFor("0.0.0.0:9000"); got != "http://127.0.0.1:9000" {
		t.Errorf("BaseURLFor wildcard = %q", got)
	}
}

func TestWorkspaceSelectorNeedsAuth(t *testing.T) {
	h := newWSHarness(t)
	req := httptest.NewRequest("GET", "/api/workspaces", nil)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list: %d, want 401", rec.Code)
	}
}
