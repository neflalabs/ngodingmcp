package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/config"
	"ngodingmcp/internal/workspace"
)

func isolateConfigDir(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestSettingsRoundTrip(t *testing.T) {
	isolateConfigDir(t)
	root := t.TempDir()
	ws, err := workspace.New(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	u := New(ws, auth.New("tok", "session"), "test")

	// Defaults: empty addr/token, OAuth enabled, loopback proxies.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/settings", nil)
	u.handleGetSettings(rec, req)
	var got settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PublicURL != "" || !got.OAuth || len(got.TrustedProxies) != 1 || got.TrustedProxies[0] != "loopback" {
		t.Fatalf("defaults = %+v", got)
	}

	// Put invalid values: public URL, proxies, addr and token are validated.
	for _, body := range []string{
		`{"publicUrl":"https://x/path"}`,
		`{"trustedProxies":["bogus"]}`,
		`{"trustedProxies":["10.0.0.0/33"]}`,
		`{"addr":"not-an-addr"}`,
		`{"addr":"127.0.0.1:99999"}`,
		`{"addr":"127.0.0.1:0"}`,
		`{"token":"has;semicolon"}`,
		`{"token":"with space"}`,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080/api/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		u.handlePutSettings(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status %d, want 400", body, rec.Code)
		}
	}

	// Valid update persists and reports restart required.
	body := `{"publicUrl":"https://mcp.example.com/","oauth":false,"trustedProxies":["10.0.0.0/8","203.0.113.7"],"addr":"0.0.0.0:9000","token":"test-token-for-settings-roundtrip-0123456789","runEnabled":true,"allowSensitive":true,"allowDownload":true}`
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080/api/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	u.handlePutSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: status %d: %s", rec.Code, rec.Body.String())
	}
	var out settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.PublicURL != "https://mcp.example.com" {
		t.Fatalf("publicUrl = %q (trailing slash not normalized)", out.PublicURL)
	}
	if out.OAuth {
		t.Fatal("oauth should be false")
	}
	if out.Addr != "0.0.0.0:9000" || out.Token != "test-token-for-settings-roundtrip-0123456789" {
		t.Fatalf("addr/token = %q/%q", out.Addr, out.Token)
	}
	if !out.RunEnabled || !out.AllowSensitive || !out.AllowDownload {
		t.Fatalf("runEnabled/sensitive/download = %v/%v/%v", out.RunEnabled, out.AllowSensitive, out.AllowDownload)
	}
	if !out.RestartRequired {
		t.Fatal("restartRequired must be true")
	}

	saved := config.Load()
	if saved.PublicURL != "https://mcp.example.com" || saved.OAuth == nil || *saved.OAuth {
		t.Fatalf("saved = %+v", saved)
	}
	if saved.Addr != "0.0.0.0:9000" || saved.Token != "test-token-for-settings-roundtrip-0123456789" {
		t.Fatalf("saved addr/token = %q/%q", saved.Addr, saved.Token)
	}
	if !saved.Run.Enabled || !saved.AllowSensitive || !saved.AllowDownload {
		t.Fatalf("saved run/sensitive/download = %v/%v/%v", saved.Run.Enabled, saved.AllowSensitive, saved.AllowDownload)
	}

	// The sensitive-file guard is applied live, not only at restart.
	if !ws.AllowSensitive() {
		t.Fatal("allowSensitive not applied live to the workspace")
	}
}

func TestSettingsKeepsLegacyShortToken(t *testing.T) {
	isolateConfigDir(t)
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Update(func(c *config.Config) { c.Token = "short" }); err != nil {
		t.Fatal(err)
	}
	u := New(ws, auth.New("short", "session"), "test")

	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080/api/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		u.handlePutSettings(rec, req)
		return rec
	}

	// The Web UI always sends every field, including the unchanged token.
	body := `{"publicUrl":"","oauth":true,"trustedProxies":["loopback"],"addr":"127.0.0.1:9000","token":"short","runEnabled":true,"allowSensitive":true,"allowDownload":true}`
	if rec := put(body); rec.Code != http.StatusOK {
		t.Fatalf("PUT with legacy token: status %d: %s", rec.Code, rec.Body.String())
	}
	if got := config.Load().Token; got != "short" {
		t.Fatalf("saved token = %q, want unchanged %q", got, "short")
	}
	if !ws.AllowSensitive() {
		t.Fatal("unrelated setting was not applied")
	}

	// A newly entered short token must still be rejected.
	if rec := put(`{"token":"brand-new-short"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT with new short token: status %d, want 400", rec.Code)
	}
}

func TestSettingsPutThroughAPIDoesNotDeadlock(t *testing.T) {
	isolateConfigDir(t)
	root := t.TempDir()
	ws, err := workspace.New(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	u := New(ws, auth.New("tok", "session"), "test")

	// API() wraps the file API in holdWorkspace, which pins the workspace
	// gate for the whole request; the settings handler must not try to
	// take that same gate again (it used to deadlock the whole server).
	api := u.API()

	// api is mounted under /api/ with the prefix stripped before it reaches
	// the mux, so paths here are relative to /api.
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		var req *http.Request
		if body != "" {
			req = httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		} else {
			req = httptest.NewRequest(method, "http://127.0.0.1:8080"+path, nil)
		}
		req.Header.Set("Authorization", "Bearer tok")
		api.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(http.MethodGet, "/settings", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPut, "/settings", `{"allowSensitive":true}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT /settings = %d: %s", rec.Code, rec.Body.String())
	}
	if !ws.AllowSensitive() {
		t.Fatal("allowSensitive not applied live through the API")
	}
	if rec := do(http.MethodPut, "/settings", `{"allowSensitive":false}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT /settings restore = %d: %s", rec.Code, rec.Body.String())
	}
	if ws.AllowSensitive() {
		t.Fatal("allowSensitive not restored live through the API")
	}
}

func TestToggleSensitiveAndBackLive(t *testing.T) {
	isolateConfigDir(t)
	root := t.TempDir()
	ws, err := workspace.New(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	u := New(ws, auth.New("tok", "session"), "test")

	setBool := func(key string, v bool) {
		bin, _ := json.Marshal(map[string]bool{key: v})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080/api/settings", bytes.NewReader(bin))
		req.Header.Set("Content-Type", "application/json")
		u.handlePutSettings(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s=%v: status %d: %s", key, v, rec.Code, rec.Body.String())
		}
	}

	if ws.AllowSensitive() {
		t.Fatal("guard should start on")
	}
	setBool("allowSensitive", true)
	if !ws.AllowSensitive() {
		t.Fatal("guard should be off after PUT true")
	}
	setBool("allowSensitive", false)
	if ws.AllowSensitive() {
		t.Fatal("guard should be on after PUT false")
	}
}
