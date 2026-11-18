package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/workspace"
)

func TestLoginCreatesOpaqueSessionAndLogoutRevokes(t *testing.T) {
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ui := New(ws, auth.New("test-token-0123456789abcdef012345", "ngodingmcp_session"), "test")

	post := func(path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+path,
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "127.0.0.1:12345"
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		switch path {
		case "/login":
			ui.LoginSubmit(rec, req)
		case "/logout":
			ui.Logout(rec, req)
		}
		return rec
	}

	// Login with the raw token mints an opaque session, not the token.
	form := url.Values{"token": {"test-token-0123456789abcdef012345"}}
	rec := post("/login", form, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d", len(cookies))
	}
	sessionCookie := cookies[0]
	if strings.EqualFold(sessionCookie.Value, "test-token-0123456789abcdef012345") {
		t.Fatal("cookie holds the raw token instead of an opaque session ID")
	}
	if !ui.auth.Sessions().Valid(sessionCookie.Value) {
		t.Fatal("minted session is not valid server-side")
	}

	// Logout revokes the session server-side and expires the cookie.
	rec = post("/logout", url.Values{}, sessionCookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout status = %d", rec.Code)
	}
	if ui.auth.Sessions().Valid(sessionCookie.Value) {
		t.Fatal("logout did not revoke the session server-side")
	}
}

func TestPageSessionCookieAuthenticates(t *testing.T) {
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ui := New(ws, auth.New("test-token-0123456789abcdef012345", "ngodingmcp_session"), "test")
	sessionID, err := ui.auth.StartSession()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/", nil)
	req.AddCookie(&http.Cookie{Name: "ngodingmcp_session", Value: sessionID})
	if !ui.auth.Check(req) {
		t.Fatal("valid session cookie rejected by auth.Check")
	}
	ui.auth.EndSession(req)
	if ui.auth.Check(req) {
		t.Fatal("revoked session cookie still authenticates")
	}
}

func TestCheckHeaderIgnoresCookie(t *testing.T) {
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ui := New(ws, auth.New("test-token-0123456789abcdef012345", "ngodingmcp_session"), "test")
	sessionID, err := ui.auth.StartSession()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/mcp", nil)
	req.AddCookie(&http.Cookie{Name: "ngodingmcp_session", Value: sessionID})
	if ui.auth.CheckHeader(req) {
		t.Fatal("CheckHeader accepted the UI session cookie")
	}
	if ui.auth.Check(req) {
		// Check does accept it (for the UI), which is expected
		t.Log("Check accepts the session cookie, as designed for the UI")
	}
}
