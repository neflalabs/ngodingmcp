package oauth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// MCP protects the MCP endpoint. Static machine credentials (Bearer /
// X-API-Key) are accepted exactly as before; otherwise a verifiable ES256
// access JWT issued by this server is required. The UI session cookie is not
// accepted. Requests without either receive a 401 with an RFC 9728
// resource_metadata challenge so MCP clients can discover and use OAuth.
func (s *Server) MCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch s.acceptMCP(r) {
		case mcpAccepted:
			next.ServeHTTP(w, r)
		case mcpRejectedInvalid:
			s.unauthorized(w, r, "invalid_token", "access token is invalid or expired")
		default:
			s.unauthorized(w, r, "", "")
		}
	})
}

type mcpOutcome int

const (
	mcpUnauthenticated mcpOutcome = iota
	mcpAccepted
	mcpRejectedInvalid
)

func (s *Server) acceptMCP(r *http.Request) mcpOutcome {
	// Machine credentials only: the UI session cookie is not accepted,
	// so a leaked browser cookie cannot be replayed against /mcp.
	if s.auth != nil && s.auth.CheckHeader(r) {
		return mcpAccepted
	}
	h := r.Header.Get("Authorization")
	if h == "" {
		return mcpUnauthenticated
	}
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		token := strings.TrimSpace(h[7:])
		if token == "" {
			return mcpRejectedInvalid
		}
		if _, err := verifyAccessToken(token, &s.key.PublicKey, s.Issuer(r)); err != nil {
			return mcpRejectedInvalid
		}
		return mcpAccepted
	}
	return mcpUnauthenticated
}

// unauthorized writes the 401 JSON body with the RFC 9728 / RFC 6750 challenge
// headers. When errCode is non-empty it is included as error=.
func (s *Server) unauthorized(w http.ResponseWriter, r *http.Request, errCode, errDesc string) {
	challenge := `Bearer realm="ngodingmcp", resource_metadata="` + s.resourceMetadataURL(r) + `", scope="mcp"`
	if errCode != "" {
		challenge += `, error="` + errCode + `"`
		if errDesc != "" {
			challenge += `, error_description="` + errDesc + `"`
		}
	}
	w.Header().Set("WWW-Authenticate", challenge)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}
