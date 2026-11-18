package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxTokenBody = 16 << 10

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

func writeTokenError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]any{
		"error":             code,
		"error_description": desc,
	})
}

// validCodeVerifier enforces RFC 7636 §4.1: 43–128 characters from the
// unreserved set.
func validCodeVerifier(v string) error {
	if len(v) < 43 || len(v) > 128 {
		return fmt.Errorf("code_verifier must be 43-128 characters")
	}
	for _, c := range v {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~'
		if !ok {
			return fmt.Errorf("code_verifier contains invalid characters")
		}
	}
	return nil
}

// handleToken implements POST /oauth/token for the authorization_code and
// refresh_token grants. PKCE (S256) is mandatory for the code exchange.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTokenBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeTokenError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request too large")
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", "request body must be application/x-www-form-urlencoded")
		return
	}

	switch form.Get("grant_type") {
	case "authorization_code":
		s.handleAuthorizationCodeGrant(w, r, form)
	case "refresh_token":
		s.handleRefreshTokenGrant(w, r, form)
	default:
		writeTokenError(w, http.StatusBadRequest, "unsupported_grant_type",
			"grant_type must be authorization_code or refresh_token")
	}
}

// issueTokenPairFor issues an access token and refresh token for issuer.
// grant identifies the grant family the refresh token belongs to; the empty
// string starts a new family (authorization_code grant). resource is bound
// as the access token audience (RFC 8707).
func (s *Server) issueTokenPairFor(issuer, clientID, scope, grant, resource string) (*tokenResponse, error) {
	if grant == "" {
		var err error
		grant, err = randomID(16)
		if err != nil {
			return nil, err
		}
	}
	access, err := issueAccessToken(s.key, s.kid, issuer, scope, resource)
	if err != nil {
		return nil, err
	}
	rawRefresh, err := randomSecret()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s.mu.Lock()
	s.sweepExpiredLocked(now)
	s.refresh[string(hashSecret(rawRefresh))] = &refreshToken{
		clientID:  clientID,
		scope:     scope,
		grant:     grant,
		expiresAt: now.Add(refreshTokenTTL),
	}
	// Mark the client as used so eviction keeps it. Best effort: a failed
	// persist must not fail the token issue.
	if c, ok := s.clients[clientID]; ok {
		c.LastUsedAt = now.Unix()
		s.clients[clientID] = c
		if err := s.persistLocked(); err != nil {
			s.logger.Warn("persist oauth state after token issue", "err", err)
		}
	}
	s.mu.Unlock()

	return &tokenResponse{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int64(accessTokenTTL / time.Second),
		Scope:        scope,
		RefreshToken: rawRefresh,
	}, nil
}

func (s *Server) handleAuthorizationCodeGrant(w http.ResponseWriter, r *http.Request, form url.Values) {
	issuer := s.Issuer(r)

	clientID := strings.TrimSpace(form.Get("client_id"))
	code := strings.TrimSpace(form.Get("code"))
	redirectURI := strings.TrimSpace(form.Get("redirect_uri"))
	codeVerifier := strings.TrimSpace(form.Get("code_verifier"))
	resource := strings.TrimSpace(form.Get("resource"))

	if clientID == "" || code == "" || redirectURI == "" || codeVerifier == "" {
		writeTokenError(w, http.StatusBadRequest, "invalid_request",
			"client_id, code, redirect_uri and code_verifier are required")
		return
	}
	if err := validCodeVerifier(codeVerifier); err != nil {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	s.mu.Lock()
	ac, ok := s.authCodes[string(hashSecret(code))]
	if ok {
		// Single-use: consume whether valid or not.
		delete(s.authCodes, string(hashSecret(code)))
	}
	s.mu.Unlock()

	if !ok {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant",
			"authorization code is invalid or was already used")
		return
	}
	if time.Now().After(ac.expiresAt) {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "authorization code expired")
		return
	}
	if ac.clientID != clientID || ac.redirectURI != redirectURI {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant",
			"authorization code was not issued to this client or redirect_uri")
		return
	}

	// PKCE check: BASE64URL(SHA256(verifier)) == stored challenge.
	sum := sha256.Sum256([]byte(codeVerifier))
	derived := base64.RawURLEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(derived), []byte(ac.codeChallenge)) != 1 {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}

	// Resource validation (RFC 8707): accept the bare issuer origin or the
	// full /mcp endpoint; anything else is invalid_target. Empty defaults to
	// the origin.
	resource = canonicalResource(issuer, resource)
	if resource == "" {
		writeTokenError(w, http.StatusBadRequest, "invalid_target",
			"resource does not identify this server")
		return
	}

	resp, err := s.issueTokenPairFor(issuer, clientID, ac.scope, "", resource)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "server_error", "could not issue token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  resp.AccessToken,
		"token_type":    resp.TokenType,
		"expires_in":    resp.ExpiresIn,
		"scope":         resp.Scope,
		"refresh_token": resp.RefreshToken,
	})
}

func (s *Server) handleRefreshTokenGrant(w http.ResponseWriter, r *http.Request, form url.Values) {
	issuer := s.Issuer(r)

	clientID := strings.TrimSpace(form.Get("client_id"))
	rawRefresh := strings.TrimSpace(form.Get("refresh_token"))
	resource := strings.TrimSpace(form.Get("resource"))

	if clientID == "" || rawRefresh == "" {
		writeTokenError(w, http.StatusBadRequest, "invalid_request",
			"client_id and refresh_token are required")
		return
	}
	resource = canonicalResource(issuer, resource)
	if resource == "" {
		writeTokenError(w, http.StatusBadRequest, "invalid_target",
			"resource does not identify this server")
		return
	}

	key := string(hashSecret(rawRefresh))
	s.mu.Lock()
	rt, ok := s.refresh[key]
	if ok {
		// Rotate: the presented token is consumed whether it is valid or not,
		// so a reused token is evidence of theft, not just an error.
		delete(s.refresh, key)
		s.consumed[key] = consumedGrant{grant: rt.grant, exp: rt.expiresAt}
	}
	s.mu.Unlock()

	if !ok {
		// A refresh token that was already used is evidence of theft
		// (RFC 6819 §5.2.2.3): revoke its whole family, so the attacker's
		// successor tokens die too.
		s.mu.Lock()
		if cg, seen := s.consumed[key]; seen {
			for k, t := range s.refresh {
				if t.grant == cg.grant {
					delete(s.refresh, k)
				}
			}
			s.logger.Warn("refresh token reuse detected; revoked token family", "clientID", clientID)
		}
		s.mu.Unlock()
		writeTokenError(w, http.StatusBadRequest, "invalid_grant",
			"refresh token is invalid or was already used")
		return
	}
	if time.Now().After(rt.expiresAt) {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant", "refresh token expired")
		return
	}
	if rt.clientID != clientID {
		writeTokenError(w, http.StatusBadRequest, "invalid_grant",
			"refresh token was not issued to this client")
		return
	}

	resp, err := s.issueTokenPairFor(issuer, clientID, rt.scope, rt.grant, resource)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "server_error", "could not issue token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  resp.AccessToken,
		"token_type":    resp.TokenType,
		"expires_in":    resp.ExpiresIn,
		"scope":         resp.Scope,
		"refresh_token": resp.RefreshToken,
	})
}
