package oauth

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ParseTrustedProxies compiles the trusted-proxy setting into IP networks.
// Entries may be keywords, CIDR blocks or single IPs:
//
//   - "loopback": 127.0.0.0/8 and ::1/128 (default when the setting is empty)
//   - "private":  loopback + RFC 1918 + ULA (fc00::/7) + fe80::/10
//   - "none":     trust no peer (disables X-Forwarded-Proto entirely)
//   - "10.0.0.0/8", "203.0.113.7", "2001:db8::/32", ...
//
// A peer is trusted only when it matches one of these networks, which is the
// precondition for honoring its X-Forwarded-Proto header.
func ParseTrustedProxies(list []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, raw := range list {
		for _, part := range strings.Split(raw, ",") {
			entry := strings.ToLower(strings.TrimSpace(part))
			if entry == "" {
				continue
			}
			switch entry {
			case "loopback":
				out = append(out, mustParseCIDR("127.0.0.0/8"), mustParseCIDR("::1/128"))
			case "private":
				out = append(out, mustParseCIDR("127.0.0.0/8"), mustParseCIDR("::1/128"),
					mustParseCIDR("10.0.0.0/8"), mustParseCIDR("172.16.0.0/12"), mustParseCIDR("192.168.0.0/16"),
					mustParseCIDR("fc00::/7"), mustParseCIDR("fe80::/10"))
			case "none":
				// Explicitly trust no peer.
			default:
				if strings.Contains(entry, "/") {
					_, ipNet, err := net.ParseCIDR(entry)
					if err != nil {
						return nil, fmt.Errorf("trusted proxy %q is not a valid CIDR", entry)
					}
					out = append(out, ipNet)
					continue
				}
				ip := net.ParseIP(entry)
				if ip == nil {
					return nil, fmt.Errorf("trusted proxy %q is not an IP address, CIDR or keyword (loopback, private, none)", entry)
				}
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			}
		}
	}
	return out, nil
}

// isTrustedProxy reports whether ip (from the connection's RemoteAddr) is one
// of the compiled trusted networks.
func isTrustedProxy(list []*net.IPNet, ip net.IP) bool {
	for _, network := range list {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// peerIP extracts the IP from a RemoteAddr ("host:port" or bare IP). ok is
// false when no IP could be parsed, in which case the peer is never trusted.
func peerIP(remoteAddr string) (net.IP, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip, ip != nil
}

// ValidatePublicURL reports whether raw is usable as an OAuth issuer: an
// absolute http(s) URL with a host and no path, query, fragment or userinfo.
// Empty is valid (issuer is derived per request).
func ValidatePublicURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid public URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("public URL must use http or https")
	}
	if u.Host == "" {
		return errors.New("public URL must have a host")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("public URL must not have a path")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return errors.New("public URL must not have a query, fragment or user")
	}
	return nil
}

func mustParseCIDR(s string) *net.IPNet {
	_, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return ipNet
}

// canonicalResource reports the resource indicator (RFC 8707) when the value
// identifies this server. Clients on different MCP stacks may send either
// the bare issuer origin or the full /mcp endpoint; both are accepted, as a
// trailing slash on either. Returns "" for anything else.
func canonicalResource(issuer, resource string) string {
	r := strings.TrimSpace(resource)
	if r == "" {
		return issuer
	}
	r = strings.TrimSuffix(r, "/")
	if r == issuer || r == issuer+"/mcp" {
		return r
	}
	return ""
}
