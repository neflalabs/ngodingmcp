package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestMCPRejectsUISessionCookieWhenOAuthDisabled verifies that a browser
// session cannot be replayed against /mcp, even with the embedded OAuth
// server switched off. Machine and browser credentials stay separate.
func TestMCPRejectsUISessionCookieWhenOAuthDisabled(t *testing.T) {
	base := liveServer(t)
	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	form := url.Values{"token": {testToken}}
	req, err := http.NewRequest(http.MethodPost, base+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var session *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "ngodingmcp_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("no session cookie in login response: %v", res.Header.Get("Set-Cookie"))
	}

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	mcpReq := func() *http.Request {
		r, err := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		return r
	}

	withCookie := mcpReq()
	withCookie.AddCookie(session)
	res, err = client.Do(withCookie)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/mcp with UI cookie status = %d, want 401", res.StatusCode)
	}

	withToken := mcpReq()
	withToken.Header.Set("Authorization", "Bearer "+testToken)
	res, err = client.Do(withToken)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		t.Fatal("/mcp rejected a valid static token")
	}
}
