package oauth

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// registerClientRaw registers a client by name and returns its client_id.
func registerClientRaw(t *testing.T, s *Server, name string) string {
	t.Helper()
	body := `{"client_name":"` + name + `","redirect_uris":["https://app.example/callback"]}`
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleRegister(rec, req)
	res := rec.Result()
	if res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("register %q: status %d body %s", name, res.StatusCode, b)
	}
	return decodeJSON(t, res)["client_id"].(string)
}

func TestRegisterEvictsOldestUnused(t *testing.T) {
	s := newTestServer(t)

	// Fill the store with 200 never-used clients; index 0 is the oldest.
	// CreatedAt is set explicitly because registrations may land in the
	// same second.
	ids := make([]string, 0, maxRegisteredClients)
	for i := 0; i < maxRegisteredClients; i++ {
		ids = append(ids, registerClientRaw(t, s, fmt.Sprintf("client-%03d", i)))
	}
	for i, id := range ids {
		c := s.clients[id]
		c.CreatedAt = int64(1700000000 + i)
		s.clients[id] = c
	}
	// Mark a recent client as used: it must survive even the 201st
	// registration, while the oldest never-used client is evicted.
	used := ids[maxRegisteredClients-1]
	c := s.clients[used]
	c.LastUsedAt = 1700000000
	s.clients[used] = c

	id := registerClientRaw(t, s, "client-200")
	if id == "" {
		t.Fatal("eviction registration failed")
	}
	if _, ok := s.clients[ids[0]]; ok {
		t.Fatal("the oldest never-used client was not evicted")
	}
	if _, ok := s.clients[used]; !ok {
		t.Fatal("a client marked as used was evicted")
	}
	if got := len(s.clients); got != maxRegisteredClients {
		t.Fatalf("clients = %d after eviction", got)
	}
}

func TestRegisterRejectsWhenAllClientsUsed(t *testing.T) {
	s := newTestServer(t)
	for i := 0; i < maxRegisteredClients; i++ {
		registerClientRaw(t, s, fmt.Sprintf("used-%03d", i))
	}
	for k, c := range s.clients {
		c.LastUsedAt = 1700000000
		s.clients[k] = c
	}
	body := `{"client_name":"new-client","redirect_uris":["https://app.example/callback"]}`
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleRegister(rec, req)
	if rec.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when every client is in use", rec.Result().StatusCode)
	}
}
