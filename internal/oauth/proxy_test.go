package oauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ngodingmcp/internal/auth"
)

func TestParseTrustedProxies(t *testing.T) {
	nets, err := ParseTrustedProxies([]string{"loopback"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 2 {
		t.Fatalf("loopback networks = %v", nets)
	}

	nets, err = ParseTrustedProxies([]string{"private,10.0.0.0/8", "203.0.113.7"})
	if err != nil {
		t.Fatal(err)
	}
	// private expands to 7 + one CIDR + one single-IP network.
	if len(nets) != 9 {
		t.Fatalf("networks = %d, want 9: %v", len(nets), nets)
	}

	if _, err := ParseTrustedProxies([]string{"none"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTrustedProxies([]string{"203.0.113.0/33"}); err == nil {
		t.Fatal("bad CIDR accepted")
	}
	if _, err := ParseTrustedProxies([]string{"not-an-ip"}); err == nil {
		t.Fatal("bad IP accepted")
	}
}

func newIssuerServer(t *testing.T, trusted []string) *Server {
	t.Helper()
	s, err := New(Config{
		PublicURL:      "http://127.0.0.1:8080", // placeholder, overridden per check
		TrustedProxies: trusted,
		Auth:           auth.New(testToken, "ngodingmcp_session"),
		StatePath:      t.TempDir() + "/oauth.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.PublicURL = "" // derive per request
	return s
}

func xfpRequest(remoteAddr, proto string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://mcp.example.test/.well-known/oauth-authorization-server", nil)
	req.RemoteAddr = remoteAddr
	req.Host = "mcp.example.test"
	if proto != "" {
		req.Header.Set("X-Forwarded-Proto", proto)
	}
	return req
}

func TestIssuerUsesTrustedProxyProto(t *testing.T) {
	s := newIssuerServer(t, []string{"loopback"})

	// Trusted peer with https: issuer becomes https.
	if got := s.Issuer(xfpRequest("127.0.0.1:1234", "https")); got != "https://mcp.example.test" {
		t.Fatalf("trusted+https issuer = %s", got)
	}
	// Trusted peer with http: stays http.
	if got := s.Issuer(xfpRequest("127.0.0.1:1234", "http")); got != "http://mcp.example.test" {
		t.Fatalf("trusted+http issuer = %s", got)
	}
	// Untrusted peer: its X-Forwarded-Proto must be ignored.
	s2 := newIssuerServer(t, []string{"none"})
	if got := s2.Issuer(xfpRequest("127.0.0.1:1234", "https")); got != "http://mcp.example.test" {
		t.Fatalf("none+https issuer = %s", got)
	}
	s3 := newIssuerServer(t, []string{"10.0.0.0/8"})
	if got := s3.Issuer(xfpRequest("203.0.113.9:443", "https")); got != "http://mcp.example.test" {
		t.Fatalf("untrusted+https issuer = %s", got)
	}
	if got := s3.Issuer(xfpRequest("10.1.2.3:443", "https")); got != "https://mcp.example.test" {
		t.Fatalf("cidr+https issuer = %s", got)
	}
	// Missing header: stays http even for trusted peers.
	if got := s.Issuer(xfpRequest("127.0.0.1:1234", "")); got != "http://mcp.example.test" {
		t.Fatalf("missing proto issuer = %s", got)
	}
}

func TestIssuerTrustedProxiesValidation(t *testing.T) {
	if _, err := New(Config{
		PublicURL:      "http://127.0.0.1:8080",
		TrustedProxies: []string{"bogus-cidr"},
		Auth:           auth.New(testToken, "ngodingmcp_session"),
		StatePath:      t.TempDir() + "/oauth.json",
	}); err == nil {
		t.Fatal("invalid trusted proxy accepted at startup")
	} else if !strings.Contains(err.Error(), "trusted proxies") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidatePublicURL(t *testing.T) {
	for _, ok := range []string{"", "http://127.0.0.1:8080", "https://mcp.example.com"} {
		if err := ValidatePublicURL(ok); err != nil {
			t.Fatalf("ValidatePublicURL(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"https://", "ftp://x", "https://x/path", "https://x/?y=1", "https://u:p@x/"} {
		if err := ValidatePublicURL(bad); err == nil {
			t.Fatalf("ValidatePublicURL(%q) accepted", bad)
		}
	}
}
