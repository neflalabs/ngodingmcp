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

func TestLoginSubmitWrongTokenThrottles(t *testing.T) {
	ws, err := workspace.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ui := New(ws, auth.New("tok", "session"), "test")

	post := func(ip, token string) *httptest.ResponseRecorder {
		body := url.Values{"token": {token}}.Encode()
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		req.RemoteAddr = ip + ":12345"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		ui.LoginSubmit(rec, req)
		return rec
	}

	ip := "203.0.113.7"

	// Failed logins redirect to /login?error=1, never panic.
	for i := 0; i < loginMaxFailures; i++ {
		rec := post(ip, "wrong")
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, rec.Code, http.StatusSeeOther)
		}
		if loc := rec.Header().Get("Location"); loc != "/login?error=1" {
			t.Fatalf("attempt %d: Location = %q, want /login?error=1", i+1, loc)
		}
	}

	// The next failure is throttled before the token is checked.
	if rec := post(ip, "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("throttled status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}

	// A correct token from another address still works and sets a cookie.
	rec := post("203.0.113.8", "tok")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("valid login: status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if cookies := rec.Result().Cookies(); len(cookies) == 0 {
		t.Fatal("valid login did not set a session cookie")
	}
}
