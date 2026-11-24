package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ngodingmcp/internal/auth"
)

// restartConfig returns a Config that points at path, so several Server
// instances can share one state file the way a restarted process would.
func restartConfig(path string) Config {
	return Config{
		PublicURL: "http://127.0.0.1:8080",
		Auth:      auth.New(testToken, "ngodingmcp_session"),
		StatePath: path,
	}
}

func mustNew(t *testing.T, path string) *Server {
	t.Helper()
	s, err := New(restartConfig(path))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// issueRefreshToken registers a client and runs the authorization-code flow,
// returning the client ID and the first refresh token.
func issueRefreshToken(t *testing.T, s *Server) (clientID, refresh string) {
	t.Helper()
	client := registerClient(t, s, "http://127.0.0.1:3000/callback")
	clientID, _ = client["client_id"].(string)
	verifier, challenge := pkcePair(t)

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
	code := redirectTarget(t, rec.Body.String()).Query().Get("code")

	tf := url.Values{}
	tf.Set("grant_type", "authorization_code")
	tf.Set("client_id", clientID)
	tf.Set("code", code)
	tf.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	tf.Set("code_verifier", verifier)
	tf.Set("resource", "http://127.0.0.1:8080")
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/token", strings.NewReader(tf.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.handleToken(rec, req)
	m := decodeJSON(t, rec.Result())
	refresh, _ = m["refresh_token"].(string)
	if refresh == "" {
		t.Fatalf("no refresh token: %v", m)
	}
	return clientID, refresh
}

func TestRefreshTokenSurvivesRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "oauth.json")
	s1 := mustNew(t, p)
	clientID, r1 := issueRefreshToken(t, s1)

	// The file must hold only the hash, never the raw token, and stay 0600.
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), r1) {
		t.Fatalf("raw refresh token found in state file")
	}
	if !strings.Contains(string(data), encodeTokenKey(string(hashSecret(r1)))) {
		t.Fatalf("refresh token hash missing from state file")
	}
	if fi, err := os.Stat(p); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %04o, want 0600", fi.Mode().Perm())
	}

	// "Restart": a fresh Server on the same file accepts the token.
	s2 := mustNew(t, p)
	m, code := exchangeRefreshToken(t, s2, clientID, r1)
	if code != http.StatusOK {
		t.Fatalf("refresh after restart: status %d: %v", code, m)
	}
	r2, _ := m["refresh_token"].(string)

	// The rotated token survives a second restart too.
	s3 := mustNew(t, p)
	if m, code := exchangeRefreshToken(t, s3, clientID, r2); code != http.StatusOK {
		t.Fatalf("refresh after second restart: status %d: %v", code, m)
	}
}

func TestRefreshReplayAfterRestartRevokesFamily(t *testing.T) {
	p := filepath.Join(t.TempDir(), "oauth.json")
	s1 := mustNew(t, p)
	clientID, r1 := issueRefreshToken(t, s1)

	// Legitimate rotation before the restart.
	m, code := exchangeRefreshToken(t, s1, clientID, r1)
	if code != http.StatusOK {
		t.Fatalf("rotation: status %d: %v", code, m)
	}
	r2, _ := m["refresh_token"].(string)

	// After a restart r2 is live and r1 is remembered as consumed...
	s2 := mustNew(t, p)
	if !hasLiveToken(s2, r2) {
		t.Fatalf("r2 not loaded after restart")
	}
	s2.mu.Lock()
	_, r1Consumed := s2.consumed[string(hashSecret(r1))]
	s2.mu.Unlock()
	if !r1Consumed {
		t.Fatalf("consumed r1 not loaded after restart")
	}

	// ...so replaying r1 is still detected and kills r2 as well.
	if _, code := exchangeRefreshToken(t, s2, clientID, r1); code != http.StatusBadRequest {
		t.Fatalf("replay after restart: status %d, want 400", code)
	}
	if hasLiveToken(s2, r2) {
		t.Fatalf("r2 still live after replay detection")
	}

	// The revocation itself is persisted: another restart must not bring
	// r2 back.
	s3 := mustNew(t, p)
	if _, code := exchangeRefreshToken(t, s3, clientID, r2); code != http.StatusBadRequest {
		t.Fatalf("successor after revoke+restart: status %d, want 400", code)
	}
}

func TestLateReplayAfterConsumedExpiryStillRevokesFamily(t *testing.T) {
	p := filepath.Join(t.TempDir(), "oauth.json")
	s := mustNew(t, p)
	clientID, r1 := issueRefreshToken(t, s)

	// Emulate a token rotated near its expiry: its successor lives much
	// longer than the presented token did.
	key := hashSecret(r1)
	s.mu.Lock()
	s.refresh[string(key)].expiresAt = time.Now().Add(time.Hour)
	s.mu.Unlock()

	m, code := exchangeRefreshToken(t, s, clientID, r1)
	if code != http.StatusOK {
		t.Fatalf("rotation: status %d: %v", code, m)
	}
	r2, _ := m["refresh_token"].(string)
	if r2 == "" || !hasLiveToken(s, r2) {
		t.Fatalf("successor not live after rotation: %v", m)
	}

	// A sweep after the presented token expired must not forget r1 while
	// its successor is still usable.
	s.mu.Lock()
	s.sweepExpiredLocked(time.Now().Add(2 * time.Hour))
	_, stillConsumed := s.consumed[string(key)]
	s.mu.Unlock()
	if !stillConsumed {
		t.Fatal("consumed marker expired before its successor")
	}

	if _, code := exchangeRefreshToken(t, s, clientID, r1); code != http.StatusBadRequest {
		t.Fatalf("late replay: status %d, want 400", code)
	}
	if hasLiveToken(s, r2) {
		t.Error("successor still live after late replay")
	}
}

// hasLiveToken reports whether raw is currently an exchangeable refresh token
// in s, without consuming it.
func hasLiveToken(s *Server, raw string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.refresh[string(hashSecret(raw))]
	return ok
}

func TestExpiredRefreshTokensNotLoadedOrSaved(t *testing.T) {
	p := filepath.Join(t.TempDir(), "oauth.json")
	mustNew(t, p) // creates the file with a signing key

	st, err := loadState(p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	key := func(raw string) string { return encodeTokenKey(string(hashSecret(raw))) }
	st.RefreshTokens = []persistedRefreshToken{
		{Hash: key("live"), ClientID: "c", Scope: "mcp", Family: "f1", ExpiresAt: now.Add(time.Hour).Unix()},
		{Hash: key("old"), ClientID: "c", Scope: "mcp", Family: "f2", ExpiresAt: now.Add(-time.Hour).Unix()},
		{Hash: "not-a-hash", ClientID: "c", Scope: "mcp", Family: "f3", ExpiresAt: now.Add(time.Hour).Unix()},
	}
	st.ConsumedRefreshTokens = []persistedConsumedToken{
		{Hash: key("used"), Family: "f1", ExpiresAt: now.Add(time.Hour).Unix()},
		{Hash: key("used-old"), Family: "f2", ExpiresAt: now.Add(-time.Hour).Unix()},
	}
	if err := saveState(p, st); err != nil {
		t.Fatal(err)
	}

	s := mustNew(t, p)
	s.mu.Lock()
	_, liveOK := s.refresh[string(hashSecret("live"))]
	_, oldOK := s.refresh[string(hashSecret("old"))]
	nRefresh := len(s.refresh)
	_, usedOK := s.consumed[string(hashSecret("used"))]
	nConsumed := len(s.consumed)
	s.mu.Unlock()
	if !liveOK || oldOK || nRefresh != 1 {
		t.Fatalf("loaded refresh tokens: live=%v old=%v count=%d, want only the live one", liveOK, oldOK, nRefresh)
	}
	if !usedOK || nConsumed != 1 {
		t.Fatalf("loaded consumed tokens: used=%v count=%d, want only the unexpired one", usedOK, nConsumed)
	}

	// An entry that expires while the server runs is left out of the next
	// save.
	s.mu.Lock()
	s.refresh[string(hashSecret("live"))].expiresAt = now.Add(-time.Second)
	err = s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	st, err = loadState(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.RefreshTokens) != 0 {
		t.Fatalf("expired refresh token was saved: %+v", st.RefreshTokens)
	}
	if len(st.ConsumedRefreshTokens) != 1 {
		t.Fatalf("consumed tokens saved = %d, want 1", len(st.ConsumedRefreshTokens))
	}
}

func TestVersion1StateFileStillLoads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "oauth.json")
	s1 := mustNew(t, p)
	registerClient(t, s1, "http://127.0.0.1:3000/callback")

	// Rewrite the file the way an older ngodingmcp left it: version 1, no
	// token lists.
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["version"] = 1
	delete(raw, "refreshTokens")
	delete(raw, "consumedRefreshTokens")
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(p, string(data)); err != nil {
		t.Fatal(err)
	}

	s2 := mustNew(t, p)
	if s2.kid != s1.kid {
		t.Fatalf("signing key changed when loading a version 1 file")
	}
	if s2.ClientCount() != 1 {
		t.Fatalf("client count = %d, want 1", s2.ClientCount())
	}
	if len(s2.refresh) != 0 || len(s2.consumed) != 0 {
		t.Fatalf("version 1 file should load with no refresh tokens")
	}

	// The next save upgrades the file to the current version.
	issueRefreshToken(t, s2)
	st, err := loadState(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != stateVersion || len(st.RefreshTokens) != 1 {
		t.Fatalf("after save: version %d, %d refresh tokens; want %d and 1", st.Version, len(st.RefreshTokens), stateVersion)
	}
}

func TestUnsupportedStateVersionFailsStartup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "oauth.json")
	if err := writeFile(p, `{"version":99}`); err != nil {
		t.Fatal(err)
	}
	if _, err := New(restartConfig(p)); err == nil {
		t.Fatalf("expected version 99 to fail New")
	}
}
