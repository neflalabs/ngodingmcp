package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/workspace"
)

// Responsive UI invariants. The layout is plain HTML/CSS/JS with no frontend
// tooling, so these guard the mobile breakpoints and modal behaviour from
// silent regressions.
func TestResponsiveUIAssets(t *testing.T) {
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ui := New(ws, auth.New("tok", "session"), "test")

	get := func(path string) string {
		t.Helper()
		var rec *httptest.ResponseRecorder
		if strings.HasPrefix(path, "/static/") {
			rec = httptest.NewRecorder()
			ui.Static().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		} else {
			rec = httptest.NewRecorder()
			ui.Index(rec, httptest.NewRequest(http.MethodGet, path, nil))
		}
		if rec.Code != http.StatusOK {
			body, _ := io.ReadAll(rec.Body)
			t.Fatalf("GET %s = %d (%s)", path, rec.Code, body)
		}
		return rec.Body.String()
	}

	css := get("/static/style.css")

	// 100vh must appear before 100dvh so dvh wins where supported.
	if i, j := strings.Index(css, "calc(100vh - 46px)"), strings.Index(css, "calc(100dvh - 46px)"); i < 0 || j < 0 || i > j {
		t.Error("mobile .layout must declare 100vh fallback before 100dvh")
	}
	if !strings.Contains(css, "min-height: 100dvh") {
		t.Error("login body missing 100dvh with 100vh fallback")
	}
	for _, want := range []string{
		"@media (max-width: 720px)",
		"@media (max-width: 480px)",
		".sidebar-backdrop { display: none; }",
		".mobile-only { display: none; }",
		".table-scroll",
		"overflow-x: auto",
		":focus-visible",
		"max-height: 85dvh",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css missing %q", want)
		}
	}

	appJS := get("/static/app.js")
	// Stats tables must scroll horizontally inside the modal.
	if strings.Count(appJS, `class="table-scroll"`) < 2 {
		t.Error("stats tables are not wrapped in .table-scroll")
	}
	// Every dialog.modal closes on backdrop click.
	if !strings.Contains(appJS, `querySelectorAll("dialog.modal")`) {
		t.Error("app.js lacks the generic backdrop-click close for dialogs")
	}

	index := get("/")
	for _, want := range []string{
		`name="viewport" content="width=device-width, initial-scale=1"`,
		`name="theme-color"`,
		`id="layout"`,
		`id="sidebarBackdrop"`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}
