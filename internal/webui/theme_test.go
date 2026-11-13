package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/workspace"
)

func TestThemeAssetsServedAndLinked(t *testing.T) {
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ui := New(ws, auth.New("tok", "session"), "test")

	rec := httptest.NewRecorder()
	ui.Static().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/theme.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/theme.js = %d", rec.Code)
	}
	js := rec.Body.String()
	for _, want := range []string{"prefers-color-scheme", "localStorage", "data-theme-toggle"} {
		if !strings.Contains(js, want) {
			t.Errorf("theme.js missing %q", want)
		}
	}

	rec = httptest.NewRecorder()
	ui.Static().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/style.css", nil))
	if !strings.Contains(rec.Body.String(), `:root[data-theme="light"]`) {
		t.Error("style.css has no light theme block")
	}

	rec = httptest.NewRecorder()
	ui.LoginPage(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if body := rec.Body.String(); !strings.Contains(body, "/static/theme.js") || !strings.Contains(body, "data-theme-toggle") {
		t.Error("login page does not load the theme toggle")
	}
}
