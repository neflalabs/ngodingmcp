package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testToken = "test-token-0123456789abcdef012345"

func liveServer(t *testing.T) string {
	t.Helper()
	srv, err := New(Config{Root: t.TempDir(), Addr: "127.0.0.1:0", Version: "test", Token: testToken, OAuthDisabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv.BaseURL()
}

func TestSecurityHeadersPresent(t *testing.T) {
	url := liveServer(t)
	for _, path := range []string{"/login", "/mcp", "/static/style.css", "/"} {
		req, err := http.NewRequest(http.MethodGet, url+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		client := &http.Client{Timeout: 2 * time.Second}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()

		s := res.Header.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: CSP %q missing %q", path, s, want)
			}
		}
		if got := res.Header.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q", path, got)
		}
		if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q", path, got)
		}
		if got := res.Header.Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s: Referrer-Policy = %q", path, got)
		}
	}
}

// TestListenerDeathSurfacesOnErr verifies that when the HTTP listener dies,
// the error reaches Err() so the process can exit with status 1 instead of
// running headless.
func TestListenerDeathSurfacesOnErr(t *testing.T) {
	srv, err := New(Config{Root: t.TempDir(), Addr: "127.0.0.1:0", Version: "test", Token: testToken, OAuthDisabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	ln := srv.listener
	if ln == nil {
		t.Fatal("listener not set")
	}
	// Kill the listener from outside the server.
	_ = ln.Close()

	select {
	case err := <-srv.Err():
		if err == nil {
			t.Fatal("Err() returned nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Err() did not report the listener death")
	}
}

func TestCrossOriginProtectionBlocksStateChangingRequests(t *testing.T) {
	url := liveServer(t)
	client := &http.Client{Timeout: 2 * time.Second}

	// Cross-origin browser POST: rejected before it reaches any handler.
	req, err := http.NewRequest(http.MethodPost, url+"/api/mkdir", strings.NewReader(`{"path":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example.com")
	req.Header.Set("Authorization", "Bearer "+testToken)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		// Go's CrossOriginProtection answers 403 for cross-origin
		// state-changing requests.
		t.Fatalf("cross-origin POST: status = %d, want 403", res.StatusCode)
	}

	// Same request without Origin header (curl-style): allowed to reach auth.
	req2, err := http.NewRequest(http.MethodPost, url+"/api/mkdir", strings.NewReader(`{"path":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+testToken)
	res2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if res2.StatusCode == http.StatusForbidden && strings.Contains(string(body), "cross") {
		t.Fatalf("same-origin-looking request was rejected as cross-origin")
	}
}
