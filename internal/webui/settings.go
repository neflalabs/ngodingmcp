package webui

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"ngodingmcp/internal/config"
	"ngodingmcp/internal/oauth"
)

// Settings exposed in the Web UI ("⚙ Settings"). Changes are persisted to
// the TUI config file and take effect at the next server start, like every
// other persistent setting; allowSensitive is applied live as well. The
// response always reports restartRequired=true because flags and environment
// variables override the file at start.

type settingsRequest struct {
	PublicURL      *string   `json:"publicUrl"`
	OAuth          *bool     `json:"oauth"`
	TrustedProxies *[]string `json:"trustedProxies"`
	Addr           *string   `json:"addr"`
	Token          *string   `json:"token"`
	RunEnabled     *bool     `json:"runEnabled"`
	AllowSensitive *bool     `json:"allowSensitive"`
	AllowDownload  *bool     `json:"allowDownload"`
}

type settingsResponse struct {
	PublicURL       string   `json:"publicUrl"`
	OAuth           bool     `json:"oauth"`
	TrustedProxies  []string `json:"trustedProxies"`
	Addr            string   `json:"addr"`
	Token           string   `json:"token"`
	RunEnabled      bool     `json:"runEnabled"`
	AllowSensitive  bool     `json:"allowSensitive"`
	AllowDownload   bool     `json:"allowDownload"`
	RestartRequired bool     `json:"restartRequired"`
}

func (u *UI) settingsResponse(c config.Config) settingsResponse {
	oauthEnabled := true
	if c.OAuth != nil {
		oauthEnabled = *c.OAuth
	}
	proxies := c.TrustedProxies
	if len(proxies) == 0 {
		proxies = []string{"loopback"}
	}
	return settingsResponse{
		PublicURL:       c.PublicURL,
		OAuth:           oauthEnabled,
		TrustedProxies:  proxies,
		Addr:            c.Addr,
		Token:           c.Token,
		RunEnabled:      c.Run.Enabled,
		AllowSensitive:  c.AllowSensitive,
		AllowDownload:   c.AllowDownload,
		RestartRequired: true,
	}
}

func (u *UI) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, u.settingsResponse(config.Load()))
}

func (u *UI) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsRequest
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}

	if in.PublicURL != nil {
		v := strings.TrimSpace(*in.PublicURL)
		if err := oauth.ValidatePublicURL(v); err != nil {
			writeErr(w, err)
			return
		}
		var norm string
		if v != "" {
			norm = strings.TrimSuffix(v, "/")
		}
		in.PublicURL = &norm
	}
	if in.TrustedProxies != nil {
		var flat []string
		for _, entry := range *in.TrustedProxies {
			flat = append(flat, splitCSV(entry)...)
		}
		if _, err := oauth.ParseTrustedProxies(flat); err != nil {
			writeErr(w, err)
			return
		}
		norm := make([]string, 0, len(flat))
		for _, entry := range flat {
			if e := strings.TrimSpace(entry); e != "" {
				norm = append(norm, e)
			}
		}
		in.TrustedProxies = &norm
	}
	if in.Addr != nil {
		v := strings.TrimSpace(*in.Addr)
		if err := validateAddr(v); err != nil {
			writeErr(w, err)
			return
		}
		in.Addr = &v
	}
	if in.Token != nil {
		v := strings.TrimSpace(*in.Token)
		// The Web UI sends every field, including an unchanged token from an
		// older config. Grandfather that value so unrelated settings can be
		// saved; only a newly entered token must meet the length rule.
		if v != config.Load().Token {
			if err := validateToken(v); err != nil {
				writeErr(w, err)
				return
			}
		}
		in.Token = &v
	}

	var saved config.Config
	var liveSensitive *bool
	err := config.Update(func(c *config.Config) {
		if in.PublicURL != nil {
			c.PublicURL = *in.PublicURL
		}
		if in.OAuth != nil {
			b := *in.OAuth
			c.OAuth = &b
		}
		if in.TrustedProxies != nil {
			c.TrustedProxies = *in.TrustedProxies
		}
		if in.Addr != nil {
			c.Addr = *in.Addr
		}
		if in.Token != nil {
			c.Token = *in.Token
		}
		if in.RunEnabled != nil {
			c.Run.Enabled = *in.RunEnabled
		}
		if in.AllowSensitive != nil {
			c.AllowSensitive = *in.AllowSensitive
			v := *in.AllowSensitive
			liveSensitive = &v
		}
		if in.AllowDownload != nil {
			c.AllowDownload = *in.AllowDownload
		}
		saved = *c
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// The sensitive-file guard is read on every file operation, so it can be
	// applied live rather than only on restart.
	if liveSensitive != nil && u.ws != nil {
		u.ws.SetAllowSensitive(*liveSensitive)
	}
	writeJSON(w, http.StatusOK, u.settingsResponse(saved))
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// validateAddr checks "host:port" (host optional). The port must be numeric
// and in range; the host must not contain spaces or control characters.
func validateAddr(addr string) error {
	if addr == "" {
		return errEmptyAddr
	}
	for _, c := range addr {
		if c <= ' ' || c == 0x7f {
			return errBadAddr
		}
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errBadAddr
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errBadAddr
	}
	// host may be empty (wildcard), an IPv6 literal, or a name/IP.
	if host != "" && strings.ContainsAny(host, " \t\n/\\\"'") {
		return errBadAddr
	}
	return nil
}

var (
	errEmptyAddr = errStr("addr must not be empty")
	errBadAddr   = errStr("addr must be host:port with a port between 1 and 65535")
)

type errStr string

func (e errStr) Error() string { return string(e) }

// validateToken allows an empty token (meaning: generate one on start) and
// everything a cookie can carry: printable ASCII except the characters that
// break the Cookie header's own syntax. A non-empty custom token must be at
// least 32 characters so it is not trivially guessable. A saved legacy token
// is exempt while unchanged (see handlePutSettings).
func validateToken(token string) error {
	if len(token) > 512 {
		return errStr("token must be at most 512 characters")
	}
	if len(token) > 0 && len(token) < 32 {
		return errStr("token must be at least 32 characters")
	}
	for _, c := range token {
		if c < 0x21 || c > 0x7e {
			return errStr("token must be printable ASCII without spaces")
		}
		switch c {
		case ';', ',', '"', '\\':
			return errStr(`token must not contain any of ; , " \`)
		}
	}
	return nil
}
