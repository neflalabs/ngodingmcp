// Package auth implements token authentication for both API/MCP clients
// (Authorization: Bearer or X-API-Key) and the browser UI (signed-in cookie).
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// Authenticator validates a shared secret.
type Authenticator struct {
	token      string
	cookieName string
}

// New returns an Authenticator for token. cookieName is used by the web UI.
func New(token, cookieName string) *Authenticator {
	return &Authenticator{token: token, cookieName: cookieName}
}

// CookieName returns the name of the UI session cookie.
func (a *Authenticator) CookieName() string { return a.cookieName }

// Validate reports whether the given token is correct.
func (a *Authenticator) Validate(token string) bool { return a.equal(token) }

// Check reports whether the request carries a valid credential.
func (a *Authenticator) Check(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
			if a.equal(strings.TrimSpace(h[7:])) {
				return true
			}
		}
	}
	if k := r.Header.Get("X-API-Key"); a.equal(k) {
		return true
	}
	if c, err := r.Cookie(a.cookieName); err == nil && a.equal(c.Value) {
		return true
	}
	return false
}

func (a *Authenticator) equal(got string) bool {
	if got == "" || a.token == "" {
		return false
	}
	// Compare digests so the length of the secret is not leaked by the
	// constant-time comparison's early return on length mismatch.
	gh := sha256.Sum256([]byte(got))
	th := sha256.Sum256([]byte(a.token))
	return subtle.ConstantTimeCompare(gh[:], th[:]) == 1
}

// API protects machine endpoints (MCP and REST). Unauthorized requests get a
// JSON 401 with a Bearer challenge.
func (a *Authenticator) API(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Check(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="ngodingmcp"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
	})
}

// Page protects HTML pages and redirects unauthenticated browsers to /login.
func (a *Authenticator) Page(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Check(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}
