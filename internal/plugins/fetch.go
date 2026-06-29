//go:build wasmplugins

package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// errFetchBlocked marks a fetch rejected by a guard (host not allow-listed, or
// resolving to a non-routable/private address). The host function maps it to the
// guest's "blocked" return code, distinct from a transport error.
var errFetchBlocked = errors.New("fetch blocked by guard")

// ipResolver resolves a host to IP addresses; net.Resolver satisfies it. The
// seam lets tests inject addresses to exercise the SSRF guard, including a
// rebinding record that resolves to a private address.
type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// doFetch performs a guarded outbound HTTP request for a plugin holding the
// fetch capability. It enforces the allow-list, an SSRF address guard, a
// per-call timeout, and a response-size cap, and refuses redirects to hosts that
// fall outside the same guards.
func (p *plugin) doFetch(parent context.Context, method, rawURL string, body []byte) (int, []byte, error) {
	if method == "" {
		method = "GET"
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return 0, nil, errFetchBlocked
	}
	if !hostAllowed(p.allowedHosts, u.Hostname()) {
		return 0, nil, errFetchBlocked
	}

	// Backward compat: tests that construct bare plugin structs may not set
	// p.client; use a temporary one in that rare case. Production always sets
	// p.client in compilePlugin.
	client := p.client
	if client == nil {
		client = newFetchClient(p)
	}

	ctx, cancel := context.WithTimeout(parent, p.fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errFetchBlocked) {
			return 0, nil, errFetchBlocked
		}
		return 0, nil, err
	}
	defer resp.Body.Close()

	// Read one byte past the cap so we can detect truncation unambiguously.
	data, _ := io.ReadAll(io.LimitReader(resp.Body, int64(p.maxFetchResp)+1))
	truncated := len(data) > p.maxFetchResp
	if truncated {
		data = data[:p.maxFetchResp]
	}

	inv, _ := parent.Value(invCtxKey{}).(*invocation)
	if inv != nil {
		inv.lastFetch = data
		inv.lastFetchTruncated = truncated
	}

	return resp.StatusCode, data, nil
}

// hostAllowed reports whether host matches the allow-list. An entry may be an
// exact host or a leading-dot suffix ("\u002eexample.com") matching subdomains.
func hostAllowed(allowed []string, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if a == host {
			return true
		}
		if strings.HasPrefix(a, ".") && strings.HasSuffix(host, a) {
			return true
		}
	}
	return false
}

// ipBlocked reports whether dialing ip risks SSRF: loopback, link-local,
// private, multicast, unspecified, or unique-local addresses are all refused.
func ipBlocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	// Unique-local IPv6 (fc00::/7) — IsPrivate covers it, but keep an explicit
	// CGNAT bound (100.64.0.0/10) which IsPrivate does not.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
	}
	return false
}

// newFetchClient builds a temporary http.Client for doFetch when p.client is
// nil (backwards-compat for tests that construct bare plugin structs).
func newFetchClient(p *plugin) *http.Client {
	dialer := &net.Dialer{Timeout: p.fetchTimeout}
	resolver := p.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &http.Client{
		Timeout: p.fetchTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := resolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if ipBlocked(ip.IP) {
						return nil, errFetchBlocked
					}
				}
				if len(ips) == 0 {
					return nil, errFetchBlocked
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !hostAllowed(p.allowedHosts, req.URL.Hostname()) {
				return errFetchBlocked
			}
			return nil
		},
	}
}
