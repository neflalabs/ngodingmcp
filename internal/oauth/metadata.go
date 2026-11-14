package oauth

import (
	"encoding/json"
	"net/http"
)

// writeJSON writes a JSON body with no-store headers and the given status.
func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func handleMetadataError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]any{"error": code, "error_description": desc})
}

// handleProtectedResourceMetadata serves RFC 9728 metadata for this resource
// server.
func (s *Server) handleProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	issuer := s.Issuer(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 issuer + "/mcp",
		"authorization_servers":    []string{issuer},
		"scopes_supported":         []string{"mcp"},
		"bearer_methods_supported": []string{"header"},
	})
}

// handleAuthorizationServerMetadata serves RFC 8414 metadata for the embedded
// authorization server.
func (s *Server) handleAuthorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	issuer := s.Issuer(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         issuer,
		"authorization_endpoint":                         issuer + "/oauth/authorize",
		"token_endpoint":                                 issuer + "/oauth/token",
		"jwks_uri":                                       issuer + "/oauth/jwks.json",
		"registration_endpoint":                          issuer + "/oauth/register",
		"scopes_supported":                               []string{"mcp"},
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"code_challenge_methods_supported":               []string{"S256"},
		"authorization_response_iss_parameter_supported": true,
	})
}

// handleJWKS publishes the public key(s) used to sign access tokens.
func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"keys": []map[string]string{jwkWithUse(s.key, s.kid)},
	})
}
