package oauth

import (
	"encoding/base64"
	"sort"
	"time"
)

// Refresh tokens are kept in oauth.json so a server restart does not force
// every MCP client to reconnect. Only the SHA-256 hash of each token is
// stored, never the raw token: whoever reads the file still cannot present a
// valid refresh token.
//
// Two lists are stored:
//   - refreshTokens: tokens that can still be exchanged (s.refresh).
//   - consumedRefreshTokens: tokens that were already rotated (s.consumed).
//     They are kept so that replaying an old token after a restart still
//     revokes its whole family (RFC 6819 §5.2.2.3).
//
// Expired entries are dropped when the file is loaded and when it is saved.

// persistedRefreshToken is one live refresh token as stored on disk.
type persistedRefreshToken struct {
	Hash      string `json:"hash"` // base64url(SHA-256(raw token))
	ClientID  string `json:"client_id"`
	Scope     string `json:"scope"`
	Family    string `json:"family"`     // grant family shared by rotated tokens
	ExpiresAt int64  `json:"expires_at"` // Unix seconds
}

// persistedConsumedToken is one already-rotated refresh token as stored on
// disk. It only needs its family to revoke the family on replay.
type persistedConsumedToken struct {
	Hash      string `json:"hash"`
	Family    string `json:"family"`
	ExpiresAt int64  `json:"expires_at"`
}

// encodeTokenKey turns an in-memory map key (raw SHA-256 bytes held in a
// string) into text that fits in JSON.
func encodeTokenKey(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

// decodeTokenKey reverses encodeTokenKey. It reports false for anything that
// is not a 32-byte SHA-256 hash, so a hand-edited or damaged entry is skipped
// instead of becoming a bogus map key.
func decodeTokenKey(s string) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return "", false
	}
	return string(b), true
}

// snapshotRefreshLocked copies the live and consumed refresh tokens into
// their on-disk form, leaving out expired entries. The result is sorted so
// the file content is stable. Caller must hold s.mu.
func (s *Server) snapshotRefreshLocked(now time.Time) ([]persistedRefreshToken, []persistedConsumedToken) {
	live := make([]persistedRefreshToken, 0, len(s.refresh))
	for key, rt := range s.refresh {
		if now.After(rt.expiresAt) {
			continue
		}
		live = append(live, persistedRefreshToken{
			Hash:      encodeTokenKey(key),
			ClientID:  rt.clientID,
			Scope:     rt.scope,
			Family:    rt.grant,
			ExpiresAt: rt.expiresAt.Unix(),
		})
	}
	consumed := make([]persistedConsumedToken, 0, len(s.consumed))
	for key, cg := range s.consumed {
		if now.After(cg.exp) {
			continue
		}
		consumed = append(consumed, persistedConsumedToken{
			Hash:      encodeTokenKey(key),
			Family:    cg.grant,
			ExpiresAt: cg.exp.Unix(),
		})
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Hash < live[j].Hash })
	sort.Slice(consumed, func(i, j int) bool { return consumed[i].Hash < consumed[j].Hash })
	return live, consumed
}

// restoreRefreshTokens rebuilds the in-memory maps from the state file,
// skipping expired and malformed entries. skipped counts the malformed ones
// (expired entries are normal and not counted). An old (version 1) file has
// no token lists, which simply yields empty maps.
func restoreRefreshTokens(st *persistedState, now time.Time) (live map[string]*refreshToken, consumed map[string]consumedGrant, skipped int) {
	live = make(map[string]*refreshToken, len(st.RefreshTokens))
	for _, p := range st.RefreshTokens {
		exp := time.Unix(p.ExpiresAt, 0)
		if now.After(exp) {
			continue
		}
		key, ok := decodeTokenKey(p.Hash)
		if !ok || p.ClientID == "" || p.Family == "" {
			skipped++
			continue
		}
		live[key] = &refreshToken{
			clientID:  p.ClientID,
			scope:     p.Scope,
			grant:     p.Family,
			expiresAt: exp,
		}
	}
	consumed = make(map[string]consumedGrant, len(st.ConsumedRefreshTokens))
	for _, p := range st.ConsumedRefreshTokens {
		exp := time.Unix(p.ExpiresAt, 0)
		if now.After(exp) {
			continue
		}
		key, ok := decodeTokenKey(p.Hash)
		if !ok || p.Family == "" {
			skipped++
			continue
		}
		consumed[key] = consumedGrant{grant: p.Family, exp: exp}
	}
	return live, consumed, skipped
}

// saveTokensLocked writes the state file after a refresh-token change
// (issue, rotate, revoke). It is best effort: the change already happened in
// memory, so a failed write is logged and the token keeps working until the
// next restart. Caller must hold s.mu.
func (s *Server) saveTokensLocked(event string) {
	if err := s.persistLocked(); err != nil {
		s.logger.Warn("persist oauth state", "event", event, "err", err)
	}
}
