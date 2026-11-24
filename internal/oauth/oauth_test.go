package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ngodingmcp/internal/auth"
)

const testToken = "0123456789abcdef0123456789abcdef01234567"

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		PublicURL: "http://127.0.0.1:8080",
		Auth:      auth.New(testToken, "ngodingmcp_session"),
		StatePath: t.TempDir() + "/oauth.json",
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(testConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func decodeJSON(t *testing.T, r *http.Response) map[string]any {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode body %q: %v", body, err)
	}
	return m
}

// redirectTarget extracts the destination from the interstitial page the
// authorize handler renders after a consent decision.
func redirectTarget(t *testing.T, body string) *url.URL {
	t.Helper()
	const marker = `<meta http-equiv="refresh" content="0;url=`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("interstitial missing meta refresh:\n%s", body)
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, `">`)
	if j < 0 {
		t.Fatalf("interstitial meta refresh malformed:\n%s", body)
	}
	u, err := url.Parse(html.UnescapeString(rest[:j]))
	if err != nil {
		t.Fatalf("interstitial redirect URL: %v", err)
	}
	return u
}

func registerClient(t *testing.T, s *Server, redirectURI string) map[string]any {
	t.Helper()
	body := `{"client_name":"Test Client","redirect_uris":["` + redirectURI + `"]}`
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleRegister(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		m := decodeJSON(t, res)
		t.Fatalf("register: status %d: %v", res.StatusCode, m)
	}
	return decodeJSON(t, res)
}

func pkcePair(t *testing.T) (verifier, challenge string) {
	t.Helper()
	buf := make([]byte, 32)
	for i := range buf {
		buf[i] = byte(i+1) ^ byte(i+7)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

func TestMetadataDerivedFromPublicURL(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/.well-known/oauth-protected-resource", nil)
	rec := httptest.NewRecorder()
	s.handleProtectedResourceMetadata(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	m := decodeJSON(t, res)
	if m["resource"] != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("resource = %v", m["resource"])
	}
	servers, _ := m["authorization_servers"].([]any)
	if len(servers) != 1 || servers[0] != "http://127.0.0.1:8080" {
		t.Fatalf("authorization_servers = %v", m["authorization_servers"])
	}
}

func TestMetadataDerivedFromRequestHost(t *testing.T) {
	s := newTestServer(t)
	s.cfg.PublicURL = "" // derive per request

	req := httptest.NewRequest(http.MethodGet, "http://example.com/.well-known/oauth-authorization-server", nil)
	req.Host = "mcp.example.test"
	rec := httptest.NewRecorder()
	s.handleAuthorizationServerMetadata(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	m := decodeJSON(t, res)
	if m["issuer"] != "http://mcp.example.test" {
		t.Fatalf("issuer = %v", m["issuer"])
	}
	if m["authorization_endpoint"] != "http://mcp.example.test/oauth/authorize" {
		t.Fatalf("authorization_endpoint = %v", m["authorization_endpoint"])
	}
	if m["code_challenge_methods_supported"].([]any)[0] != "S256" {
		t.Fatalf("code_challenge_methods_supported = %v", m["code_challenge_methods_supported"])
	}
	if m["authorization_response_iss_parameter_supported"] != true {
		t.Fatalf("authorization_response_iss_parameter_supported missing")
	}
}

func TestDCRRejectsNonLoopbackHTTP(t *testing.T) {
	s := newTestServer(t)
	cases := []string{
		`{"redirect_uris":["http://evil.com/cb"]}`,
		`{"redirect_uris":["http://localhost:1234/cb#frag"]}`,
		`{"redirect_uris":["ftp://127.0.0.1/cb"]}`,
		`{"redirect_uris":["https://example.com/cb"],"token_endpoint_auth_method":"client_secret_basic"}`,
		`{"redirect_uris":["https://example.com/cb"],"grant_types":["authorization_code","implicit"]}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/register", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleRegister(rec, req)
		res := rec.Result()
		defer res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s: status %d, want 400", body, res.StatusCode)
		}
		if m := decodeJSON(t, res); m["error"] != "invalid_client_metadata" {
			t.Fatalf("body %s: error = %v", body, m["error"])
		}
	}
}

func TestFullAuthorizationFlow(t *testing.T) {
	s := newTestServer(t)
	client := registerClient(t, s, "http://127.0.0.1:3000/callback")
	clientID, _ := client["client_id"].(string)
	verifier, challenge := pkcePair(t)

	// Authorization request.
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	q.Set("response_type", "code")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", "xyz")
	q.Set("scope", "mcp")
	q.Set("resource", "http://127.0.0.1:8080")

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/oauth/authorize?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	s.handleAuthorize(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize GET: status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Approve") {
		t.Fatalf("consent page missing approve button")
	}

	// Approve (with token, not session cookie).
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	form.Set("code_challenge", challenge)
	form.Set("state", "xyz")
	form.Set("scope", "mcp")
	form.Set("resource", "http://127.0.0.1:8080")
	form.Set("token", testToken)
	form.Set("decision", "approve")
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.handleAuthorizeSubmit(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("approve: status %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	loc := redirectTarget(t, string(body))
	if loc.Query().Get("iss") != "http://127.0.0.1:8080" {
		t.Fatalf("iss in redirect = %q", loc.Query().Get("iss"))
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in redirect: %s", loc)
	}

	// Exchange code for tokens.
	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("client_id", clientID)
	tokenForm.Set("code", code)
	tokenForm.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	tokenForm.Set("code_verifier", verifier)
	tokenForm.Set("resource", "http://127.0.0.1:8080")
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/token", strings.NewReader(tokenForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.handleToken(rec, req)
	res = rec.Result()
	defer res.Body.Close()
	m := decodeJSON(t, res)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("token: status %d: %v", res.StatusCode, m)
	}

	// Verify the access token against the published JWKS.
	access, _ := m["access_token"].(string)
	claims, err := verifyAccessToken(access, &s.key.PublicKey, "http://127.0.0.1:8080")
	if err != nil {
		t.Fatalf("verify access token: %v", err)
	}
	if claims.Audience != "http://127.0.0.1:8080" {
		t.Fatalf("aud = %q", claims.Audience)
	}
	if claims.Scope != "mcp" {
		t.Fatalf("scope = %q", claims.Scope)
	}

	// Code replay must fail.
	rec = httptest.NewRecorder()
	s.handleToken(rec, req)
	res = rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("code replay: status %d, want 400", res.StatusCode)
	}

	// Refresh rotation.
	refresh, _ := m["refresh_token"].(string)
	rf := url.Values{}
	rf.Set("grant_type", "refresh_token")
	rf.Set("client_id", clientID)
	rf.Set("refresh_token", refresh)
	rf.Set("resource", "http://127.0.0.1:8080")
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/token", strings.NewReader(rf.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.handleToken(rec, req)
	res = rec.Result()
	defer res.Body.Close()
	m2 := decodeJSON(t, res)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("refresh: status %d: %v", res.StatusCode, m2)
	}
	newRefresh, _ := m2["refresh_token"].(string)
	if newRefresh == "" || newRefresh == refresh {
		t.Fatalf("refresh token was not rotated")
	}

	// Reusing the old refresh token must fail.
	rf.Set("refresh_token", refresh)
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/token", strings.NewReader(rf.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.handleToken(rec, req)
	res = rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("refresh reuse: status %d, want 400", res.StatusCode)
	}
}

func TestTokenEndpointRejectsWrongResource(t *testing.T) {
	s := newTestServer(t)
	client := registerClient(t, s, "http://127.0.0.1:3000/callback")
	clientID, _ := client["client_id"].(string)
	verifier, challenge := pkcePair(t)

	// Forge an auth code directly to isolate the resource check at token time.
	s.mu.Lock()
	now := timeNow()
	s.authCodes[string(hashSecret("forged"))] = &authCode{
		clientID:      clientID,
		redirectURI:   "http://127.0.0.1:3000/callback",
		codeChallenge: challenge,
		scope:         "mcp",
		issuedAt:      now,
		expiresAt:     now.Add(authCodeTTL),
	}
	s.mu.Unlock()

	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("client_id", clientID)
	tokenForm.Set("code", "forged")
	tokenForm.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	tokenForm.Set("code_verifier", verifier)
	tokenForm.Set("resource", "http://evil.example")
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/token", strings.NewReader(tokenForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleToken(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("resource mismatch: status %d, want 400", res.StatusCode)
	}
	if m := decodeJSON(t, res); m["error"] != "invalid_target" {
		t.Fatalf("error = %v, want invalid_target", m["error"])
	}
}

func TestPKCEFailure(t *testing.T) {
	s := newTestServer(t)
	client := registerClient(t, s, "http://127.0.0.1:3000/callback")
	clientID, _ := client["client_id"].(string)
	_, challenge := pkcePair(t)

	now := timeNow()
	s.mu.Lock()
	s.authCodes[string(hashSecret("forgedpkce"))] = &authCode{
		clientID:      clientID,
		redirectURI:   "http://127.0.0.1:3000/callback",
		codeChallenge: challenge,
		scope:         "mcp",
		issuedAt:      now,
		expiresAt:     now.Add(authCodeTTL),
	}
	s.mu.Unlock()

	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("client_id", clientID)
	tokenForm.Set("code", "forgedpkce")
	tokenForm.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	tokenForm.Set("code_verifier", "9"+strings.Repeat("A", 42))
	tokenForm.Set("resource", "http://127.0.0.1:8080")
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/token", strings.NewReader(tokenForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleToken(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("pkce failure: status %d, want 400", res.StatusCode)
	}
	if m := decodeJSON(t, res); m["error"] != "invalid_grant" {
		t.Fatalf("error = %v, want invalid_grant", m["error"])
	}
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

func timeNow() time.Time { return time.Now() }

func TestCorruptStateFailsStartup(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/oauth.json"
	if err := writeFile(p, "{not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{PublicURL: "http://127.0.0.1:8080", StatePath: p}); err == nil {
		t.Fatalf("expected corrupt state to fail New")
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/oauth.json"
	s1, err := New(Config{PublicURL: "http://127.0.0.1:8080", StatePath: p, Auth: auth.New(testToken, "ngodingmcp_session")})
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	registerClient(t, s1, "http://127.0.0.1:3000/callback")

	s2, err := New(Config{PublicURL: "http://127.0.0.1:8080", StatePath: p, Auth: auth.New(testToken, "ngodingmcp_session")})
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	if s2.ClientCount() != 1 {
		t.Fatalf("client count after reload = %d, want 1", s2.ClientCount())
	}
	if s1.kid != s2.kid {
		t.Fatalf("signing key changed across restart")
	}
}

func TestMCPRejectsUISessionCookie(t *testing.T) {
	s := newTestServer(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := s.MCP(next)

	// A valid UI session cookie must not authenticate /mcp: browser
	// credentials and machine credentials are different.
	sessionID, err := s.auth.Sessions().Create()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/mcp", nil)
	req.AddCookie(&http.Cookie{Name: "ngodingmcp_session", Value: sessionID})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("UI session cookie accepted by /mcp: status %d", rec.Result().StatusCode)
	}
}

func TestMCPUnauthorizedChallenge(t *testing.T) {
	s := newTestServer(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := s.MCP(next)

	// No credentials: 401 with resource_metadata challenge.
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	challenge := res.Header.Get("WWW-Authenticate")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.StatusCode)
	}
	for _, want := range []string{`resource_metadata="http://127.0.0.1:8080/.well-known/oauth-protected-resource"`, `scope="mcp"`} {
		if !strings.Contains(challenge, want) {
			t.Fatalf("challenge %q missing %q", challenge, want)
		}
	}

	// Static token: passes.
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusNoContent {
		t.Fatalf("static token rejected")
	}

	// OAuth JWT: passes.
	issuer := "http://127.0.0.1:8080"
	access, err := issueAccessToken(s.key, s.kid, issuer, "mcp", "http://127.0.0.1:8080/mcp")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusNoContent {
		t.Fatalf("oauth jwt rejected")
	}

	// Garbage bearer: 401 with invalid_token.
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/mcp", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res = rec.Result()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("garbage bearer: status %d", res.StatusCode)
	}
	if !strings.Contains(res.Header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("garbage bearer challenge = %q", res.Header.Get("WWW-Authenticate"))
	}
}

func TestSessionConsentRequiresCSRF(t *testing.T) {
	s := newTestServer(t)
	client := registerClient(t, s, "http://127.0.0.1:3000/callback")
	clientID, _ := client["client_id"].(string)
	verifier, challenge := pkcePair(t)

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	q.Set("response_type", "code")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", "s")
	q.Set("scope", "mcp")
	q.Set("resource", "http://127.0.0.1:8080")

	// The consent page is only shown to signed-in browser sessions, which
	// are now opaque server-side session IDs (the raw token is not a
	// session). Mint one through the same authenticator.
	sessionID, err := s.auth.Sessions().Create()
	if err != nil {
		t.Fatal(err)
	}
	session := &http.Cookie{Name: "ngodingmcp_session", Value: sessionID}

	// Render the consent page with a session cookie; it must carry a CSRF token.
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	s.handleAuthorize(rec, req)
	first := csrfFromConsent(t, rec.Body.String())

	approve := func(csrfToken string) int {
		form := url.Values{}
		form.Set("client_id", clientID)
		form.Set("redirect_uri", "http://127.0.0.1:3000/callback")
		form.Set("code_challenge", challenge)
		form.Set("state", "s")
		form.Set("scope", "mcp")
		form.Set("resource", "http://127.0.0.1:8080")
		form.Set("decision", "approve")
		if csrfToken != "" {
			form.Set("csrf_token", csrfToken)
		}
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/authorize", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(session)
		rec := httptest.NewRecorder()
		s.handleAuthorizeSubmit(rec, req)
		return rec.Result().StatusCode
	}

	// A stale/missing CSRF token re-renders the consent with a fresh
	// token and an inline error (200), not a dead-end 403.
	for _, stale := range []string{"", "bogus-token"} {
		code := approve(stale)
		if code != http.StatusOK {
			t.Fatalf("approve with stale csrf: status %d, want 200", code)
		}
	}

	// The re-rendered page carries a fresh CSRF the user can approve with.
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	s.handleAuthorize(rec, req)
	csrf := csrfFromConsent(t, rec.Body.String())
	if csrf == first {
		t.Fatalf("re-rendered consent reused the old csrf token")
	}
	if code := approve(csrf); code != http.StatusOK {
		t.Fatalf("approve with fresh csrf: status %d, want 200", code)
	}

	// The consumed CSRF is single-use; replaying it re-renders the
	// consent (200) instead of a dead-end 403.
	if code := approve(csrf); code != http.StatusOK {
		t.Fatalf("replayed csrf: status %d, want 200", code)
	}
	_ = verifier
}

// csrfFromConsent returns the csrf_token value of a rendered consent page and
// fails the test when the page has none.
func csrfFromConsent(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="csrf_token" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("consent page missing csrf_token:\n%s", body)
	}
	rest := body[i+len(marker):]
	end := strings.Index(rest, `"`)
	if end <= 0 {
		t.Fatalf("consent page has an empty or unterminated csrf_token:\n%s", body)
	}
	return rest[:end]
}

// TestConsentTokenFormAcceptsSessionOnPost covers the SameSite asymmetry: the
// consent page is reached by a cross-site top-level GET (which with a Strict
// cookie carries no session, so the token form is rendered), while the form
// POST is same-site and does carry the cookie. The handler must not demand a
// CSRF token the rendered page could never have included.
func TestConsentTokenFormAcceptsSessionOnPost(t *testing.T) {
	s := newTestServer(t)
	client := registerClient(t, s, "http://127.0.0.1:3000/callback")
	clientID, _ := client["client_id"].(string)
	_, challenge := pkcePair(t)

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	q.Set("response_type", "code")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", "s")
	q.Set("scope", "mcp")
	q.Set("resource", "http://127.0.0.1:8080")

	// Cross-site GET without the session cookie: token form, no CSRF.
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/oauth/authorize?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	s.handleAuthorize(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `name="csrf_token"`) {
		t.Fatalf("token-mode page unexpectedly carries a CSRF token:\n%s", body)
	}
	if !strings.Contains(body, `name="token"`) {
		t.Fatalf("token-mode page missing token field:\n%s", body)
	}

	// Same-site POST: the browser now attaches a valid session cookie.
	sessionID, err := s.auth.Sessions().Create()
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	form.Set("code_challenge", challenge)
	form.Set("state", "s")
	form.Set("scope", "mcp")
	form.Set("resource", "http://127.0.0.1:8080")
	form.Set("token", testToken)
	form.Set("decision", "approve")
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "ngodingmcp_session", Value: sessionID})
	rec = httptest.NewRecorder()
	s.handleAuthorizeSubmit(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: status %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `http-equiv="refresh"`) || !strings.Contains(body, `href=`) {
		t.Fatalf("approve: interstitial missing meta refresh or Continue link:\n%s", body)
	}
}

// TestConsentExpiredSessionFallsBackToTokenForm covers the opposite mismatch:
// the page was rendered with a session and a CSRF token, but the session is
// gone by the time the form is submitted. The user gets the token form back
// instead of a dead end.
func TestConsentExpiredSessionFallsBackToTokenForm(t *testing.T) {
	s := newTestServer(t)
	client := registerClient(t, s, "http://127.0.0.1:3000/callback")
	clientID, _ := client["client_id"].(string)
	_, challenge := pkcePair(t)

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	q.Set("response_type", "code")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", "s")
	q.Set("scope", "mcp")
	q.Set("resource", "http://127.0.0.1:8080")

	sessionID, err := s.auth.Sessions().Create()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "ngodingmcp_session", Value: sessionID})
	rec := httptest.NewRecorder()
	s.handleAuthorize(rec, req)
	body := rec.Body.String()
	marker := `name="csrf_token" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("session-mode page missing CSRF token:\n%s", body)
	}
	csrf := body[i+len(marker):]
	csrf = csrf[:strings.Index(csrf, `"`)]

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	form.Set("code_challenge", challenge)
	form.Set("state", "s")
	form.Set("scope", "mcp")
	form.Set("resource", "http://127.0.0.1:8080")
	form.Set("csrf_token", csrf)
	form.Set("decision", "approve")
	// No session cookie: the session is gone.
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.handleAuthorizeSubmit(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stale session: status %d, want 200", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, `name="token"`) || !strings.Contains(body, "Session expired") {
		t.Fatalf("stale session did not fall back to the token form:\n%s", body)
	}
}

func TestConsentCSPAllowsRegisteredRedirectOrigin(t *testing.T) {
	// Chrome enforces form-action on the redirect that follows the consent
	// form submission. The policy must therefore name the client's
	// redirect origin, otherwise the browser cancels the hop back to the
	// client and the consent page appears to do nothing.
	s := newTestServer(t)
	_, challenge := pkcePair(t)

	cases := []struct {
		name        string
		redirectURI string
		want        string
	}{
		{
			name:        "google",
			redirectURI: "https://oauth-redirect.googleusercontent.com/r/user_bound_custom-mcp-abc",
			want:        "form-action 'self' https://oauth-redirect.googleusercontent.com",
		},
		{
			name:        "loopback with port",
			redirectURI: "http://127.0.0.1:3000/callback",
			want:        "form-action 'self' http://127.0.0.1:3000",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := registerClient(t, s, tc.redirectURI)
			clientID, _ := client["client_id"].(string)

			q := url.Values{}
			q.Set("client_id", clientID)
			q.Set("redirect_uri", tc.redirectURI)
			q.Set("response_type", "code")
			q.Set("code_challenge", challenge)
			q.Set("code_challenge_method", "S256")
			q.Set("scope", "mcp")
			q.Set("resource", "http://127.0.0.1:8080")

			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/oauth/authorize?"+q.Encode(), nil)
			rec := httptest.NewRecorder()
			s.handleAuthorize(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("authorize: status %d", rec.Code)
			}
			csp := rec.Result().Header.Get("Content-Security-Policy")
			if !strings.Contains(csp, tc.want) {
				t.Fatalf("CSP %q missing %q", csp, tc.want)
			}
			// Everything else from the hardened policy must still be there.
			for _, directive := range []string{"default-src 'self'", "frame-ancestors 'none'", "base-uri 'self'"} {
				if !strings.Contains(csp, directive) {
					t.Fatalf("CSP %q missing %q", csp, directive)
				}
			}
		})
	}
}

func exchangeRefreshToken(t *testing.T, s *Server, clientID, refresh string) (map[string]any, int) {
	t.Helper()
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refresh)
	form.Set("resource", "http://127.0.0.1:8080")
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleToken(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	return decodeJSON(t, res), res.StatusCode
}

func TestRefreshTokenReplayRevokesFamily(t *testing.T) {
	s := newTestServer(t)
	client := registerClient(t, s, "http://127.0.0.1:3000/callback")
	clientID, _ := client["client_id"].(string)
	verifier, challenge := pkcePair(t)

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	q.Set("response_type", "code")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("scope", "mcp")
	q.Set("resource", "http://127.0.0.1:8080")

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	form.Set("response_type", "code")
	form.Set("code_challenge", challenge)
	form.Set("code_challenge_method", "S256")
	form.Set("scope", "mcp")
	form.Set("resource", "http://127.0.0.1:8080")
	form.Set("token", testToken)
	form.Set("decision", "approve")
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleAuthorizeSubmit(rec, req)
	loc := redirectTarget(t, rec.Body.String())

	code := loc.Query().Get("code")
	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("client_id", clientID)
	tokenForm.Set("code", code)
	tokenForm.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	tokenForm.Set("code_verifier", verifier)
	tokenForm.Set("resource", "http://127.0.0.1:8080")
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/token", strings.NewReader(tokenForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.handleToken(rec, req)
	m := decodeJSON(t, rec.Result())
	r1, _ := m["refresh_token"].(string)
	if r1 == "" {
		t.Fatalf("no refresh token: %v", m)
	}

	// Legitimate rotation.
	m2, code2 := exchangeRefreshToken(t, s, clientID, r1)
	if code2 != http.StatusOK {
		t.Fatalf("rotation: status %d: %v", code2, m2)
	}
	r2, _ := m2["refresh_token"].(string)

	// Attacker replays the old token: denied, and the whole family dies.
	_, codeReplay := exchangeRefreshToken(t, s, clientID, r1)
	if codeReplay != http.StatusBadRequest {
		t.Fatalf("replay: status %d, want 400", codeReplay)
	}
	_, codeR2 := exchangeRefreshToken(t, s, clientID, r2)
	if codeR2 != http.StatusBadRequest {
		t.Fatalf("successor after replay detection: status %d, want 400 (family revoked)", codeR2)
	}
}

// replayWriter signals when the token endpoint has answered with an error.
type replayWriter struct {
	*httptest.ResponseRecorder
	done chan struct{}
	once sync.Once
}

func (w *replayWriter) WriteHeader(code int) {
	w.ResponseRecorder.WriteHeader(code)
	if code >= http.StatusBadRequest {
		w.once.Do(func() { close(w.done) })
	}
}

func TestConcurrentRefreshReplayRevokesFamily(t *testing.T) {
	s := newTestServer(t)
	clientID, r1 := issueRefreshToken(t, s)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.testRotateGap = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}

	type result struct {
		code int
		m    map[string]any
	}
	rotated := make(chan result, 1)
	go func() {
		m, code := exchangeRefreshToken(t, s, clientID, r1)
		rotated <- result{code: code, m: m}
	}()
	<-entered // the old token is consumed; its successor is not stored yet

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", r1)
	form.Set("resource", "http://127.0.0.1:8080")
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := &replayWriter{ResponseRecorder: httptest.NewRecorder(), done: make(chan struct{})}
	s.handleToken(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("replay: status %d, want 400", rec.Code)
	}
	<-rec.done

	close(release)
	res := <-rotated
	if res.code != http.StatusOK {
		t.Fatalf("rotation: status %d: %v", res.code, res.m)
	}
	next, _ := res.m["refresh_token"].(string)
	if next == "" {
		t.Fatalf("rotation returned no refresh token: %v", res.m)
	}
	if hasLiveToken(s, r1) {
		t.Error("presented token still live after rotation")
	}
	if hasLiveToken(s, next) {
		t.Error("successor still live after replay detected between consume and issue")
	}
}

func TestFullFlowWithEndpointResource(t *testing.T) {
	// Spec-compliant clients (e.g. Gemini) send resource = the MCP endpoint
	// URL, not the bare origin. That must work end to end and yield a token
	// whose audience is the endpoint.
	s := newTestServer(t)
	client := registerClient(t, s, "http://127.0.0.1:3000/callback")
	clientID, _ := client["client_id"].(string)
	verifier, challenge := pkcePair(t)

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	form.Set("code_challenge", challenge)
	form.Set("code_challenge_method", "S256")
	form.Set("scope", "mcp")
	form.Set("resource", "http://127.0.0.1:8080/mcp")
	form.Set("code", "") // placeholder to be safe: authorize endpoint never sees it
	form.Set("response_type", "code")
	form.Set("token", testToken)
	form.Set("decision", "approve")
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleAuthorizeSubmit(rec, req)
	loc := redirectTarget(t, rec.Body.String())
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in redirect: %s", loc)
	}

	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("client_id", clientID)
	tokenForm.Set("code", code)
	tokenForm.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	tokenForm.Set("code_verifier", verifier)
	tokenForm.Set("resource", "http://127.0.0.1:8080/mcp")
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/token", strings.NewReader(tokenForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.handleToken(rec, req)
	m := decodeJSON(t, rec.Result())
	if rec.Code != http.StatusOK {
		t.Fatalf("token: %d: %v", rec.Code, m)
	}
	access, _ := m["access_token"].(string)
	claims, err := verifyAccessToken(access, &s.key.PublicKey, "http://127.0.0.1:8080")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Audience != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("aud = %q", claims.Audience)
	}
	// And it must be accepted by the /mcp middleware.
	h := s.MCP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("/mcp with endpoint-aud token: %d", rec.Code)
	}
}
