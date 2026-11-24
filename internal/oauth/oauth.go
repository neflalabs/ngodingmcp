// Package oauth implements an embedded OAuth 2.1 authorization server and
// resource-server middleware for ngodingmcp's MCP endpoint. It follows the
// MCP authorization specification:
//   - OAuth 2.0 Protected Resource Metadata (RFC 9728) served at
//     /.well-known/oauth-protected-resource
//   - OAuth 2.0 Authorization Server Metadata (RFC 8414) served at
//     /.well-known/oauth-authorization-server
//   - OAuth 2.1 authorization-code + PKCE (S256) with resource indicators
//     (RFC 8707) and issuer in the authorization response (RFC 9207)
//   - OAuth 2.0 Dynamic Client Registration (RFC 7591)
//   - ES256 access tokens bound to this server via the "aud" claim
//
// The shared static token remains an accepted credential on /mcp; OAuth is an
// additional path for spec-compliant MCP clients.
package oauth

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"ngodingmcp/internal/auth"
)

// Config controls the OAuth server. The zero value is unusable; use New.
type Config struct {
	// PublicURL, when set, is used as the issuer and resource identifier
	// instead of deriving one from each request. It must be an absolute
	// http(s) URL with no path and no trailing slash, e.g.
	// "https://mcp.example.com".
	PublicURL string
	// Auth validates the shared static token (used to approve consent and to
	// accept static credentials on /mcp). Required: without it the consent
	// page has no way to approve a request.
	Auth *auth.Authenticator
	// TrustedProxies names the peers whose X-Forwarded-Proto header is
	// honored when deriving the issuer from the request (e.g. "loopback",
	// "private", or CIDRs of a TLS-terminating proxy). Empty means
	// "loopback". "none" disables forwarded-header trust entirely. Ignored
	// when PublicURL is set.
	TrustedProxies []string
	// Logger for structured logs. When nil, logs go to stderr-discard.
	Logger *slog.Logger
	// StatePath is the location of oauth.json (signing key, registered
	// clients and refresh token hashes). When empty,
	// ~/.config/ngodingmcp/oauth.json is used. Tests should point it at a
	// temporary directory.
	StatePath string
}

// Server is the embedded authorization server and resource-server helper.
type Server struct {
	cfg    Config
	logger *slog.Logger
	auth   *auth.Authenticator

	key     *ecdsa.PrivateKey
	kid     string
	clients map[string]registeredClient
	trusted []*net.IPNet

	mu         sync.Mutex
	authCodes  map[string]*authCode
	refresh    map[string]*refreshToken
	csrfTokens map[string]csrfEntry
	consumed   map[string]consumedGrant

	statePath string

	// testRotateGap pauses a rotation after the old token is consumed.
	// Test-only: lets tests force the replay/rotation interleaving.
	testRotateGap func()
}

// challengeForToken holds the PKCE inputs for a pending authorize request.
type authCode struct {
	clientID      string
	redirectURI   string
	codeChallenge string
	scope         string
	issuedAt      time.Time
	expiresAt     time.Time
}

type refreshToken struct {
	clientID  string
	scope     string
	grant     string // family shared by the original and all rotated tokens
	expiresAt time.Time
}

// csrfEntry is a single-use CSRF token bound to a session cookie.
type csrfEntry struct {
	session string
	exp     time.Time
}

// consumedGrant records a rotated (already-used) refresh token's family so a
// replayed old token can revoke the whole family.
type consumedGrant struct {
	grant string
	exp   time.Time
}

// New loads or creates the persisted OAuth state. A missing state file is the
// first-run case and is not an error.
func New(cfg Config) (*Server, error) {
	if cfg.Auth == nil {
		return nil, errors.New("oauth: Config.Auth is required (the consent page has no other way to approve requests)")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	p := cfg.StatePath
	if p == "" {
		var err error
		p, err = defaultStatePath()
		if err != nil {
			return nil, fmt.Errorf("resolve oauth state path: %w", err)
		}
	}
	if cfg.PublicURL != "" {
		if err := ValidatePublicURL(cfg.PublicURL); err != nil {
			return nil, err
		}
		cfg.PublicURL = strings.TrimSuffix(cfg.PublicURL, "/")
	}
	trustedInput := cfg.TrustedProxies
	if len(trustedInput) == 0 {
		// Default: a same-host or intranet reverse proxy keeps working
		// without any configuration; "none" opts out explicitly.
		trustedInput = []string{"loopback"}
	}
	trusted, err := ParseTrustedProxies(trustedInput)
	if err != nil {
		return nil, fmt.Errorf("trusted proxies: %w", err)
	}
	cfg.TrustedProxies = trustedInput

	persisted, err := loadState(p)
	if err != nil {
		return nil, err
	}
	if persisted == nil {
		persisted = &persistedState{Version: stateVersion}
	}

	priv, err := parseSigningKey(persisted.SigningKeyPEM)
	if err != nil {
		return nil, err
	}
	freshKey := persisted.SigningKeyPEM == ""
	kid := persisted.SigningKeyID
	if kid == "" {
		kid = signingKeyID(priv)
	}
	if freshKey {
		pemBytes, err := marshalSigningKey(priv)
		if err != nil {
			return nil, err
		}
		persisted.Version = stateVersion
		persisted.SigningKeyPEM = pemBytes
		persisted.SigningKeyID = kid
		if err := saveState(p, persisted); err != nil {
			return nil, err
		}
	}

	// Refresh tokens survive restarts (hashes only); auth codes and CSRF
	// tokens are short-lived and stay in memory.
	refresh, consumed, skipped := restoreRefreshTokens(persisted, time.Now())
	if skipped > 0 {
		cfg.Logger.Warn("ignored malformed refresh token entries in oauth state", "count", skipped)
	}

	s := &Server{
		cfg:        cfg,
		logger:     cfg.Logger,
		auth:       cfg.Auth,
		key:        priv,
		kid:        kid,
		clients:    map[string]registeredClient{},
		statePath:  p,
		trusted:    trusted,
		authCodes:  map[string]*authCode{},
		refresh:    refresh,
		csrfTokens: map[string]csrfEntry{},
		consumed:   consumed,
	}
	for _, c := range persisted.Clients {
		s.clients[c.ClientID] = c
	}
	return s, nil
}

// Issuer returns the authorization-server issuer and resource identifier for
// the request. When Config.PublicURL is set, it always wins. Otherwise the
// issuer is derived from the request's Host header, which must be syntactically
// valid (no userinfo, no control characters, no quotes); an unusable Host
// yields a loopback fallback rather than an attacker-chosen origin. The
// scheme is https only when the connection itself is TLS, or when the peer is
// a trusted proxy that sets a valid X-Forwarded-Proto header
// (Config.TrustedProxies).
func (s *Server) Issuer(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if p := r.Header.Get("X-Forwarded-Proto"); (p == "http" || p == "https") && s.peerTrusted(r) {
		scheme = p
	}
	host := r.Host
	if !validRequestHost(host) {
		host = "127.0.0.1"
	}
	return scheme + "://" + host
}

// peerTrusted reports whether the direct peer of the connection is one of the
// configured trusted proxies. Unparsable or missing RemoteAddr is untrusted.
func (s *Server) peerTrusted(r *http.Request) bool {
	ip, ok := peerIP(r.RemoteAddr)
	return ok && isTrustedProxy(s.trusted, ip)
}

// GuardHost rejects requests with a Host header that cannot be embedded
// safely in URLs, headers or JSON. Wrap a mux with it so every handler in
// the process rejects such requests uniformly.
func GuardHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validRequestHost(r.Host) {
			http.Error(w, "invalid Host header", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validRequestHost reports whether host (with optional port) is a syntactically
// valid Host header: a DNS name, IPv4 literal or bracketed IPv6 literal with
// digits-only port, containing no characters that could break out of a URL,
// header value or JSON string.
func validRequestHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if strings.HasPrefix(host, "[") {
		end := strings.Index(host, "]")
		if end < 0 {
			return false
		}
		if ip := net.ParseIP(host[1:end]); ip == nil {
			return false
		}
		rest := host[end+1:]
		if rest == "" {
			return true
		}
		if !strings.HasPrefix(rest, ":") {
			return false
		}
		return isPort(rest[1:])
	}
	if strings.Contains(host, ":") {
		h, port, err := net.SplitHostPort(host)
		if err != nil || !isPort(port) {
			return false
		}
		host = h
	}
	for _, c := range host {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_'
		if !ok {
			return false
		}
	}
	return host != ""
}

func isPort(s string) bool {
	if len(s) == 0 || len(s) > 5 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// resourceMetadataURL is the absolute URL clients use to find this server's
// protected-resource metadata.
func (s *Server) resourceMetadataURL(r *http.Request) string {
	return s.Issuer(r) + "/.well-known/oauth-protected-resource"
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.handleProtectedResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleAuthorizationServerMetadata)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.handleAuthorizationServerMetadata)
	mux.HandleFunc("GET /oauth/jwks.json", s.handleJWKS)
	mux.HandleFunc("POST /oauth/register", s.handleRegister)
	mux.HandleFunc("GET /oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("POST /oauth/authorize", s.handleAuthorizeSubmit)
	mux.HandleFunc("POST /oauth/token", s.handleToken)
}

// ClientCount reports how many clients have registered. Exposed for tests and
// diagnostics; unused by the HTTP layer.
func (s *Server) ClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// randomID returns a base64url-encoded random identifier of nbytes.
func randomID(nbytes int) (string, error) {
	buf := make([]byte, nbytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// randomSecret returns the same, but encoded for opaque-token usage.
func randomSecret() (string, error) { return randomID(32) }

// hashSecret is a stable lookup key for opaque tokens held in memory.
func hashSecret(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// isLoopbackHost reports whether a redirect host is loopback-only.
func isLoopbackHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// sessionKey returns a stable, non-reversible identifier for the caller's
// UI session cookie, used to bind CSRF tokens to a session.
func (s *Server) sessionKey(r *http.Request) string {
	if s.auth == nil {
		return ""
	}
	c, err := r.Cookie(s.auth.CookieName())
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(c.Value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// sweepExpiredLocked evicts expired authorization codes, refresh tokens, CSRF
// tokens and consumed-grant records. Caller must hold s.mu.
func (s *Server) sweepExpiredLocked(now time.Time) {
	for k, ac := range s.authCodes {
		if now.After(ac.expiresAt) {
			delete(s.authCodes, k)
		}
	}
	for k, rt := range s.refresh {
		if now.After(rt.expiresAt) {
			delete(s.refresh, k)
		}
	}
	for k, exp := range s.csrfTokens {
		if now.After(exp.exp) {
			delete(s.csrfTokens, k)
		}
	}
	for k, cg := range s.consumed {
		if now.After(cg.exp) {
			delete(s.consumed, k)
		}
	}
}
