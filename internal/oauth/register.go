package oauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxRegisterBody = 16 << 10

// RegisterRequest is the DCR request payload (RFC 7591 §2). Only the fields we
// care about are decoded; unknown ones are tolerated.
type RegisterRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	ApplicationType         string   `json:"application_type"`
}

// RegisterResponse is the DCR response (RFC 7591 §3).
type RegisterResponse struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
}

// handleRegister implements POST /oauth/register.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRegisterBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeRegisterError(w, http.StatusRequestEntityTooLarge,
			"invalid_client_metadata", "registration request too large")
		return
	}
	var req RegisterRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRegisterError(w, http.StatusBadRequest,
			"invalid_client_metadata", "request body must be valid JSON")
		return
	}

	if err := s.validateClientMetadata(req); err != nil {
		writeRegisterError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}

	clientID, err := randomID(16)
	if err != nil {
		writeRegisterError(w, http.StatusInternalServerError, "invalid_client_metadata", "could not generate client_id")
		return
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "MCP client"
	}
	client := registeredClient{
		ClientID:                clientID,
		ClientName:              name,
		RedirectURIs:            req.RedirectURIs,
		GrantTypes:              grantTypesOrDefault(req.GrantTypes),
		ResponseTypes:           responseTypesOrDefault(req.ResponseTypes),
		TokenEndpointAuthMethod: "none",
		CreatedAt:               time.Now().Unix(),
	}

	s.mu.Lock()
	if len(s.clients) >= maxRegisteredClients {
		// Evict the oldest client that has never been used for a token
		// exchange; clients that proved active are always kept.
		victim, ok := pickEvictionLocked(s.clients)
		if !ok {
			s.mu.Unlock()
			writeRegisterError(w, http.StatusBadRequest,
				"invalid_client_metadata",
				fmt.Sprintf("too many registered clients (limit %d) and all of them are in use; ask the administrator to clear ~/.config/ngodingmcp/oauth.json", maxRegisteredClients))
			return
		}
		delete(s.clients, victim)
	}
	s.clients[clientID] = client
	err = s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		s.logger.Error("persist oauth state", "err", err)
		s.mu.Lock()
		delete(s.clients, clientID)
		s.mu.Unlock()
		writeRegisterError(w, http.StatusInternalServerError,
			"invalid_client_metadata", "could not persist client registration")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  client.ClientID,
		"client_name":                client.ClientName,
		"redirect_uris":              client.RedirectURIs,
		"grant_types":                client.GrantTypes,
		"response_types":             client.ResponseTypes,
		"token_endpoint_auth_method": "none",
		"client_id_issued_at":        client.CreatedAt,
	})
}

func writeRegisterError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]any{
		"error":             code,
		"error_description": desc,
	})
}

// validateClientMetadata applies the rules for public (no client secret)
// clients.
func (s *Server) validateClientMetadata(req RegisterRequest) error {
	if len(req.RedirectURIs) == 0 {
		return fmt.Errorf("redirect_uris is required")
	}
	if len(req.RedirectURIs) > maxRedirectURIs {
		return fmt.Errorf("redirect_uris may list at most %d URIs", maxRedirectURIs)
	}
	seenURIs := make(map[string]struct{}, len(req.RedirectURIs))
	for _, raw := range req.RedirectURIs {
		if raw == "" {
			return fmt.Errorf("redirect_uris must not contain empty values")
		}
		if _, dup := seenURIs[raw]; dup {
			return fmt.Errorf("redirect_uris must not contain duplicate URIs")
		}
		seenURIs[raw] = struct{}{}
		if len(raw) > 2048 {
			return fmt.Errorf("redirect uri is too long")
		}
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("redirect_uri %q is invalid", raw)
		}
		if u.Fragment != "" {
			return fmt.Errorf("redirect_uri %q must not contain a fragment", raw)
		}
		switch u.Scheme {
		case "https":
		case "http":
			if !isLoopbackHost(u.Host) {
				return fmt.Errorf("redirect_uri %q must use https unless it is loopback", raw)
			}
		default:
			return fmt.Errorf("redirect_uri %q must use https or loopback http", raw)
		}
		if u.User != nil {
			return fmt.Errorf("redirect_uri %q must not contain userinfo", raw)
		}
	}

	name := strings.TrimSpace(req.ClientName)
	if len(name) > maxClientNameLen {
		return fmt.Errorf("client_name must be at most %d characters", maxClientNameLen)
	}

	// Public clients: no secrets allowed. "none" or an absent method is the
	// only accepted shape.
	if m := strings.TrimSpace(req.TokenEndpointAuthMethod); m != "" && m != "none" {
		return fmt.Errorf("token_endpoint_auth_method must be \"none\" for this server")
	}

	if len(req.GrantTypes) > 0 {
		for _, g := range req.GrantTypes {
			switch g {
			case "authorization_code", "refresh_token":
			default:
				return fmt.Errorf("grant_types must not include %q", g)
			}
		}
		if !contains(req.GrantTypes, "authorization_code") {
			return fmt.Errorf("grant_types must include \"authorization_code\"")
		}
	}

	if len(req.ResponseTypes) > 0 {
		for _, rt := range req.ResponseTypes {
			if rt != "code" {
				return fmt.Errorf("response_types must not include %q", rt)
			}
		}
	}

	return nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// pickEvictionLocked returns the ID of the least valuable client to evict: a
// client that has never been used for a token exchange, oldest registration
// first. Returns false when every registered client has been used at least
// once, in which case eviction would risk dropping an active client and the
// registration is rejected instead.
func pickEvictionLocked(clients map[string]registeredClient) (string, bool) {
	var bestID string
	var bestAt int64 = -1
	for id, c := range clients {
		if c.LastUsedAt != 0 {
			continue
		}
		if bestAt == -1 || c.CreatedAt < bestAt {
			bestAt, bestID = c.CreatedAt, id
		}
	}
	if bestAt == -1 {
		return "", false
	}
	return bestID, true
}

func grantTypesOrDefault(in []string) []string {
	if len(in) == 0 {
		return []string{"authorization_code", "refresh_token"}
	}
	return append([]string{}, in...)
}

func responseTypesOrDefault(in []string) []string {
	if len(in) == 0 {
		return []string{"code"}
	}
	return append([]string{}, in...)
}

func (s *Server) persistLocked() error {
	clients := make([]registeredClient, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	pemBytes, err := marshalSigningKey(s.key)
	if err != nil {
		return err
	}
	st := &persistedState{
		Version:       stateVersion,
		SigningKeyPEM: pemBytes,
		SigningKeyID:  s.kid,
		Clients:       clients,
	}
	return saveState(s.statePath, st)
}
