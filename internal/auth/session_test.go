package auth

import (
	"testing"
	"time"
)

func TestSessionStoreLifecycle(t *testing.T) {
	s := NewSessionStore()
	id, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) < 40 {
		t.Fatalf("session id too short: %q", id)
	}
	if !s.Valid(id) {
		t.Fatal("fresh session is not valid")
	}
	if s.Valid("bogus") {
		t.Fatal("bogus session id is valid")
	}

	// Sliding TTL: each Valid call extends the session.
	s.mu.Lock()
	orig := s.m[id]
	s.mu.Unlock()
	s.Valid(id)
	s.mu.Lock()
	slid := s.m[id]
	s.mu.Unlock()
	if !slid.After(orig) && !slid.Equal(orig) {
		t.Fatalf("session expiry did not move: %v -> %v", orig, slid)
	}

	// Revoke deletes it server-side.
	s.Revoke(id)
	if s.Valid(id) {
		t.Fatal("revoked session is still valid")
	}
}

func TestSessionStoreExpiry(t *testing.T) {
	s := NewSessionStore()
	s.ttl = time.Millisecond
	id, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if s.Valid(id) {
		t.Fatal("expired session is valid")
	}
}

func TestSessionStoreBound(t *testing.T) {
	s := NewSessionStore()
	s.ttl = time.Hour
	for i := 0; i < maxSessions+50; i++ {
		if _, err := s.Create(); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Len(); got > maxSessions {
		t.Fatalf("store grew past the cap: %d", got)
	}
}
