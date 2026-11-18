package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

const (
	// DefaultSessionTTL is the idle timeout for UI sessions; accessing a
	// session refreshes it (sliding expiration).
	DefaultSessionTTL = 7 * 24 * time.Hour
	maxSessions       = 4096
	sessionIDBytes    = 32
)

// SessionStore issues and validates opaque session IDs for the web UI.
// It is deliberately in-memory: restarting the server signs everyone out,
// which is the safe default for a credential that guards a file editor.
type SessionStore struct {
	mu  sync.Mutex
	m   map[string]time.Time
	ttl time.Duration
}

// NewSessionStore returns an empty session store.
func NewSessionStore() *SessionStore {
	return &SessionStore{m: map[string]time.Time{}, ttl: DefaultSessionTTL}
}

// Create mints a new session ID and records it.
func (s *SessionStore) Create() (string, error) {
	b := make([]byte, sessionIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	if len(s.m) >= maxSessions {
		// Evict the oldest entries rather than grow without bound.
		oldest, oldestExp := "", time.Time{}
		for k, exp := range s.m {
			if oldest == "" || exp.Before(oldestExp) {
				oldest, oldestExp = k, exp
			}
		}
		delete(s.m, oldest)
	}
	s.m[id] = time.Now().Add(s.ttl)
	return id, nil
}

// Valid reports whether id is a live session, extending its idle timeout.
func (s *SessionStore) Valid(id string) bool {
	if id == "" {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[id]
	if !ok || now.After(exp) {
		delete(s.m, id)
		return false
	}
	s.m[id] = now.Add(s.ttl)
	return true
}

// Revoke permanently invalidates id.
func (s *SessionStore) Revoke(id string) {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
}

// Len returns the number of live sessions.
func (s *SessionStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	return len(s.m)
}

func (s *SessionStore) sweepLocked(now time.Time) {
	for k, exp := range s.m {
		if now.After(exp) {
			delete(s.m, k)
		}
	}
}
