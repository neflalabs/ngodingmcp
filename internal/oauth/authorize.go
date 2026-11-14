package oauth

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type consentData struct {
	ClientName    string
	ClientID      string
	RedirectURI   string
	State         string
	Scope         string
	CodeChallenge string
	Resource      string
	NeedsToken    bool
	Error         bool
	CSRFToken     string
}

// authorizeInput is the validated subset of the authorization request.
type authorizeInput struct {
	client        registeredClient
	clientID      string
	redirectURI   string
	state         string
	scope         string
	codeChallenge string
	resource      string
}

// parseAuthorize validates an incoming authorization request. The returned
// input is only valid when err is nil.
func (s *Server) parseAuthorize(r *http.Request, q url.Values) (*authorizeInput, error) {
	clientID := strings.TrimSpace(q.Get("client_id"))
	redirectURI := strings.TrimSpace(q.Get("redirect_uri"))
	responseType := strings.TrimSpace(q.Get("response_type"))
	codeChallenge := strings.TrimSpace(q.Get("code_challenge"))
	method := strings.TrimSpace(q.Get("code_challenge_method"))
	state := q.Get("state")
	scope := strings.TrimSpace(q.Get("scope"))
	resource := strings.TrimSpace(q.Get("resource"))

	if responseType != "code" {
		return nil, fmt.Errorf("response_type must be \"code\"")
	}
	if clientID == "" {
		return nil, fmt.Errorf("client_id is required")
	}
	s.mu.Lock()
	client, ok := s.clients[clientID]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown client_id")
	}

	// Exact redirect URI match against the registered list.
	if redirectURI == "" {
		return nil, fmt.Errorf("redirect_uri is required")
	}
	matched := false
	for _, uri := range client.RedirectURIs {
		if uri == redirectURI {
			matched = true
			break
		}
	}
	if !matched {
		return nil, fmt.Errorf("redirect_uri does not exactly match a registered URI")
	}

	if codeChallenge == "" {
		return nil, fmt.Errorf("code_challenge is required")
	}
	if method == "" || method == "S256" {
		method = "S256"
	} else {
		return nil, fmt.Errorf("code_challenge_method must be \"S256\"")
	}
	if len(codeChallenge) < 43 || len(codeChallenge) > 128 {
		return nil, fmt.Errorf("code_challenge is malformed")
	}

	if scope == "" {
		scope = "mcp"
	}
	for _, sc := range strings.Fields(scope) {
		if sc != "mcp" {
			return nil, fmt.Errorf("unsupported scope %q", sc)
		}
	}

	issuer := s.Issuer(r)
	resource = canonicalResource(issuer, resource)
	if resource == "" {
		return nil, fmt.Errorf("resource does not match this server")
	}

	return &authorizeInput{
		client:        client,
		clientID:      clientID,
		redirectURI:   redirectURI,
		state:         state,
		scope:         scope,
		codeChallenge: codeChallenge,
		resource:      resource,
	}, nil
}

// redirectError redirects the agent to the client's redirect_uri with an OAuth
// error, carrying state and iss when available.
func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, desc string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("error", code)
	if desc != "" {
		q.Set("error_description", desc)
	}
	if state != "" {
		q.Set("state", state)
	}
	q.Set("iss", s.Issuer(r))
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// handleAuthorize renders the consent page.
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.sweepExpiredLocked(time.Now())
	s.mu.Unlock()
	q := r.URL.Query()

	in, err := s.parseAuthorize(r, q)
	if err != nil {
		// Invalid requests must not redirect to unvalidated URIs.
		http.Error(w, "authorization request invalid: "+err.Error(), http.StatusBadRequest)
		return
	}

	data := consentData{
		ClientName:    in.client.ClientName,
		ClientID:      in.clientID,
		RedirectURI:   in.redirectURI,
		State:         in.state,
		Scope:         in.scope,
		CodeChallenge: in.codeChallenge,
		Resource:      in.resource,
		NeedsToken:    !s.hasSession(r),
	}
	if s.hasSession(r) {
		// Bind a single-use CSRF token to the session for the consent
		// form. Without a session the shared token in the form is the
		// secret instead.
		if csrf, err := randomID(16); err == nil {
			s.mu.Lock()
			s.csrfTokens[csrf] = csrfEntry{session: s.sessionKey(r), exp: time.Now().Add(10 * time.Minute)}
			s.mu.Unlock()
			data.CSRFToken = csrf
		}
	}
	s.renderConsent(w, data, nil)
}

// handleAuthorizeSubmit approves or denies an authorization request. Approval
// requires either a valid session cookie plus its CSRF token, or the shared
// token in the form.
func (s *Server) handleAuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.sweepExpiredLocked(time.Now())
	s.mu.Unlock()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return
	}
	if s.hasSession(r) {
		tok := r.PostForm.Get("csrf_token")
		s.mu.Lock()
		entry, ok := s.csrfTokens[tok]
		if ok {
			delete(s.csrfTokens, tok)
		}
		s.mu.Unlock()
		if !ok || time.Now().After(entry.exp) || entry.session != s.sessionKey(r) {
			http.Error(w, "invalid or expired CSRF token; go back and try again", http.StatusForbidden)
			return
		}
	}
	q := url.Values{
		"client_id":             {r.PostForm.Get("client_id")},
		"redirect_uri":          {r.PostForm.Get("redirect_uri")},
		"response_type":         {"code"},
		"code_challenge":        {r.PostForm.Get("code_challenge")},
		"code_challenge_method": {"S256"},
		"state":                 {r.PostForm.Get("state")},
		"scope":                 {r.PostForm.Get("scope")},
		"resource":              {r.PostForm.Get("resource")},
	}
	in, err := s.parseAuthorize(r, q)
	if err != nil {
		http.Error(w, "authorization request invalid: "+err.Error(), http.StatusBadRequest)
		return
	}

	decision := r.PostForm.Get("decision")
	if decision == "deny" {
		s.redirectError(w, r, in.redirectURI, in.state, "access_denied", "user denied the request")
		return
	}
	if decision != "approve" {
		http.Error(w, "unknown decision", http.StatusBadRequest)
		return
	}

	// Approval requires authentication.
	if !s.hasSession(r) {
		token := strings.TrimSpace(r.PostForm.Get("token"))
		if s.auth == nil || !s.auth.Validate(token) {
			s.renderConsent(w, consentData{
				ClientName:    in.client.ClientName,
				ClientID:      in.clientID,
				RedirectURI:   in.redirectURI,
				State:         in.state,
				Scope:         in.scope,
				CodeChallenge: in.codeChallenge,
				Resource:      in.resource,
				NeedsToken:    true,
				Error:         true,
			}, nil)
			return
		}
	}

	code, err := randomID(32)
	if err != nil {
		http.Error(w, "could not issue code", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	now := time.Now()
	s.authCodes[string(hashSecret(code))] = &authCode{
		clientID:      in.clientID,
		redirectURI:   in.redirectURI,
		codeChallenge: in.codeChallenge,
		scope:         in.scope,
		issuedAt:      now,
		expiresAt:     now.Add(authCodeTTL),
	}
	s.mu.Unlock()

	u, err := url.Parse(in.redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	qq := u.Query()
	qq.Set("code", code)
	if in.state != "" {
		qq.Set("state", in.state)
	}
	qq.Set("iss", s.Issuer(r))
	u.RawQuery = qq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// hasSession reports whether the request carries a valid login session.
func (s *Server) hasSession(r *http.Request) bool {
	return s.auth != nil && s.auth.Check(r)
}

// renderConsent renders the consent page.
func (s *Server) renderConsent(w http.ResponseWriter, data consentData, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	var buf bytes.Buffer
	if err := consentTemplate().Execute(&buf, data); err != nil {
		http.Error(w, "could not render consent page", http.StatusInternalServerError)
		return
	}
	_, _ = buf.WriteTo(w)
}
