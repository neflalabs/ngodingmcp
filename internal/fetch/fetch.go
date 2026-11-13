// Package fetch downloads a file from an HTTPS URL into the workspace for the
// opt-in download_file tool.
//
// The guards are deliberately strict because the URL comes from an AI agent:
//   - only https:// URLs, without user:password@;
//   - every connection, including each redirect hop and every address a host
//     name resolves to, is checked at dial time, so loopback, private, link-local
//     (cloud metadata), CGNAT and other non-public addresses are refused even
//     after DNS rebinding or a redirect;
//   - environment proxies are ignored, so the dial check sees the real target;
//   - redirects are capped and each hop is re-checked (scheme and host list);
//   - the size limit is enforced on the bytes read, never on Content-Length;
//   - the whole download has a timeout;
//   - the file is written atomically through workspace.WriteStream, which also
//     applies the path and sensitive-file guards.
//
// Errors name only the host, never the full URL, because signed URLs carry
// their token in the query string.
package fetch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	"ngodingmcp/internal/workspace"
)

const (
	// DefaultMaxBytes is the default download size limit (20 MiB).
	DefaultMaxBytes int64 = 20 << 20
	// DefaultTimeout bounds one whole download, body included.
	DefaultTimeout = 2 * time.Minute
	// DefaultMaxRedirects caps the redirect chain.
	DefaultMaxRedirects = 5
)

var (
	// ErrNotHTTPS is returned for any scheme other than https.
	ErrNotHTTPS = errors.New("only https:// URLs are allowed")
	// ErrHostNotAllowed is returned when a host is not on the allowlist.
	ErrHostNotAllowed = errors.New("host is not in the download allowlist")
	// ErrBlockedAddress is returned when a host resolves to a non-public address.
	ErrBlockedAddress = errors.New("address is not public (loopback, private, link-local and similar ranges are blocked)")
	// ErrChecksum is returned when the downloaded data does not match sha256.
	ErrChecksum = errors.New("sha256 mismatch; nothing was written")
)

// Options configures a Client.
type Options struct {
	// MaxBytes caps the downloaded size (default DefaultMaxBytes).
	MaxBytes int64
	// AllowHosts, when non-empty, limits downloads (and every redirect hop)
	// to these hosts. "example.com" matches exactly; "*.example.com" matches
	// any subdomain. Empty means any public host.
	AllowHosts []string
	// Timeout bounds one download (default DefaultTimeout).
	Timeout time.Duration
	// MaxRedirects caps redirects (default DefaultMaxRedirects).
	MaxRedirects int

	// Testing loosens the guards for unit tests only. It is never set from
	// the CLI, the environment or the config file.
	Testing TestingOptions
}

// TestingOptions exist so tests can use httptest.NewTLSServer on 127.0.0.1.
type TestingOptions struct {
	// AllowLoopback permits 127.0.0.0/8 and ::1. All other blocks stay.
	AllowLoopback bool
	// RootCAs trusts the test server's certificate.
	RootCAs *x509.CertPool
}

// Client downloads files with the guards described in the package comment.
type Client struct {
	opts Options
	http *http.Client
}

// Result describes a finished download.
type Result struct {
	Path        string `json:"path"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"contentType,omitempty"`
	Host        string `json:"host"`
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// New builds a Client.
func New(opts Options) *Client {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxRedirects <= 0 {
		opts.MaxRedirects = DefaultMaxRedirects
	}
	for i, h := range opts.AllowHosts {
		opts.AllowHosts[i] = strings.ToLower(strings.TrimSpace(h))
	}
	c := &Client{opts: opts}

	dialer := &net.Dialer{
		Timeout: 15 * time.Second,
		// Control runs for every connection attempt after DNS resolution,
		// right before connect: the only place a rebinding answer or a
		// redirect target cannot slip past.
		Control: func(_, address string, _ syscall.RawConn) error {
			return c.checkDialAddress(address)
		},
	}
	transport := &http.Transport{
		Proxy:                 nil, // never route through an environment proxy
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opts.Testing.RootCAs},
	}
	c.http = &http.Client{
		Transport: transport,
		Timeout:   opts.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > c.opts.MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", c.opts.MaxRedirects)
			}
			return c.checkURL(req.URL)
		},
	}
	return c
}

// MaxBytes returns the effective size limit.
func (c *Client) MaxBytes() int64 { return c.opts.MaxBytes }

// AllowHosts returns the host allowlist (empty means any public host).
func (c *Client) AllowHosts() []string { return c.opts.AllowHosts }

// Download fetches rawURL and stores it at dest inside ws. When wantSHA256 is
// set (64 hex characters), the data must match it or nothing is written.
func (c *Client) Download(ctx context.Context, ws *workspace.Workspace, rawURL, dest string, overwrite bool, wantSHA256 string) (Result, error) {
	wantSHA256 = strings.ToLower(strings.TrimSpace(wantSHA256))
	if wantSHA256 != "" && !sha256Hex.MatchString(wantSHA256) {
		return Result{}, errors.New("sha256 must be 64 hexadecimal characters")
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Result{}, errors.New("source_url is not a valid URL")
	}
	if err := c.checkURL(u); err != nil {
		return Result{}, err
	}
	host := u.Hostname()

	// Validate the destination before any network traffic.
	if _, err := ws.CheckWritable(dest, overwrite); err != nil {
		return Result{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Result{}, errors.New("source_url is not a valid URL")
	}
	req.Header.Set("User-Agent", "ngodingmcp-download")
	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("download from %s: %w", host, stripURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("download from %s: HTTP %d", host, resp.StatusCode)
	}
	if resp.ContentLength > c.opts.MaxBytes {
		return Result{}, fmt.Errorf("download from %s: %w of %d bytes", host, workspace.ErrStreamTooLarge, c.opts.MaxBytes)
	}

	var verify func(string) error
	if wantSHA256 != "" {
		verify = func(sum string) error {
			if sum != wantSHA256 {
				return ErrChecksum
			}
			return nil
		}
	}
	res, err := ws.WriteStream(dest, resp.Body, workspace.StreamOptions{
		MaxBytes:  c.opts.MaxBytes,
		Overwrite: overwrite,
		Verify:    verify,
	})
	if err != nil {
		return Result{}, fmt.Errorf("download from %s: %w", host, stripURL(err))
	}
	return Result{
		Path:        res.Path,
		Bytes:       res.Bytes,
		SHA256:      res.SHA256,
		ContentType: resp.Header.Get("Content-Type"),
		Host:        host,
	}, nil
}

// checkURL applies the scheme, credentials and host-allowlist rules to the
// first URL and to every redirect target.
func (c *Client) checkURL(u *url.URL) error {
	if !strings.EqualFold(u.Scheme, "https") {
		return ErrNotHTTPS
	}
	if u.User != nil {
		return errors.New("URLs with user:password@ are not allowed")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return errors.New("source_url has no host")
	}
	if len(c.opts.AllowHosts) > 0 && !hostAllowed(host, c.opts.AllowHosts) {
		return fmt.Errorf("%w: %s", ErrHostNotAllowed, host)
	}
	return nil
}

func hostAllowed(host string, allow []string) bool {
	for _, a := range allow {
		if suffix, ok := strings.CutPrefix(a, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == a {
			return true
		}
	}
	return false
}

// checkDialAddress refuses any IP that is not a plain public unicast address.
func (c *Client) checkDialAddress(address string) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, address)
	}
	ip := ap.Addr().Unmap()
	if c.opts.Testing.AllowLoopback && ip.IsLoopback() {
		return nil
	}
	if !IsPublicIP(ip) {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
	}
	return nil
}

// nonPublic lists ranges that the netip helpers do not already cover.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT, also Tailscale
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, broadcast
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
}

// IsPublicIP reports whether ip is a public unicast address. Loopback,
// private (RFC 1918, fc00::/7), link-local (169.254.0.0/16 including the
// cloud metadata address, fe80::/10), multicast, unspecified, CGNAT and
// reserved ranges are not public.
func IsPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// stripURL removes the request URL that *url.Error puts in its message, so a
// signed URL's query token never reaches an error or a log.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if errors.Is(ue.Err, context.DeadlineExceeded) || ue.Timeout() {
			return errors.New("timed out")
		}
		return ue.Err
	}
	return err
}
