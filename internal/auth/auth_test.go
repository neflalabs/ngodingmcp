package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func testAuth() *Authenticator {
	return New("test-token-123", "ngodingmcp_session")
}

func TestCheckCredentials(t *testing.T) {
	a := testAuth()

	for _, tc := range []struct {
		name string
		set  func(r *http.Request)
		want bool
	}{
		{"bearer valid", func(r *http.Request) { r.Header.Set("Authorization", "Bearer test-token-123") }, true},
		{"bearer case-insensitive scheme", func(r *http.Request) { r.Header.Set("Authorization", "bearer test-token-123") }, true},
		{"bearer wrong", func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }, false},
		{"bearer empty", func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") }, false},
		{"x-api-key valid", func(r *http.Request) { r.Header.Set("X-API-Key", "test-token-123") }, true},
		{"x-api-key wrong", func(r *http.Request) { r.Header.Set("X-API-Key", "nope") }, false},
		{"cookie valid", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "ngodingmcp_session", Value: "test-token-123"}) }, true},
		{"cookie missing", func(r *http.Request) {}, false},
		{"cookie wrong", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "ngodingmcp_session", Value: "nope"}) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/mcp", nil)
			tc.set(req)
			if got := a.Check(req); got != tc.want {
				t.Fatalf("Check = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckPrefixTrap(t *testing.T) {
	a := testAuth()
	// A shorter prefix of the token must not authenticate.
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/mcp", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	if a.Check(req) {
		t.Fatal("prefix of the token authenticated")
	}
	req.Header.Set("Authorization", "Bearer test-token-1234")
	if a.Check(req) {
		t.Fatal("token with an extra character authenticated")
	}
}

func TestValidateAndCookieName(t *testing.T) {
	a := testAuth()
	if !a.Validate("test-token-123") {
		t.Fatal("Validate(good) = false")
	}
	if a.Validate("nope") {
		t.Fatal("Validate(bad) = true")
	}
	if a.Validate("") {
		t.Fatal("Validate(empty) = true")
	}
	if got := a.CookieName(); got != "ngodingmcp_session" {
		t.Fatalf("CookieName = %q", got)
	}
}

func TestAPIMiddleware(t *testing.T) {
	a := testAuth()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Unauthenticated: 401 + JSON body + Bearer challenge.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/mcp", nil)
	a.API(inner).ServeHTTP(rec, req)
	res := rec.Result()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
	if got := res.Header.Get("WWW-Authenticate"); got == "" {
		t.Fatal("missing WWW-Authenticate challenge")
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}

	// Authenticated: passes through.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/mcp", nil)
	req.Header.Set("Authorization", "Bearer test-token-123")
	a.API(inner).ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Result().StatusCode)
	}
}

func TestPageMiddlewareRedirectsToLogin(t *testing.T) {
	a := testAuth()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/", nil)
	a.Page(inner).ServeHTTP(rec, req)
	res := rec.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/login" {
		t.Fatalf("Location = %q, want /login", loc)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/", nil)
	req.Header.Set("Authorization", "Bearer test-token-123")
	a.Page(inner).ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Result().StatusCode)
	}
}
