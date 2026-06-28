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

// doFetch performs a guarded outbound HTTP request for a plugin holding the
// fetch capability. It enforces the allow-list, an SSRF address guard, a
// per-call timeout, and a response-size cap, and refuses redirects to hosts that
// fall outside the same guards.
func (p *plugin) doFetch(parent context.Context, method, rawURL string, body []byte) (int, []byte, error) {
	if method == "" {
		method = http.MethodGet
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

	dialer := &net.Dialer{Timeout: p.fetchTimeout}
	client := &http.Client{
		Timeout: p.fetchTimeout,
		Transport: &http.Transport{
			// guardedDial blocks connections to non-routable/private addresses,
			// closing the SSRF window even when DNS resolves to such an address.
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if ipBlocked(ip.IP) {
						return nil, errFetchBlocked
					}
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
			},
		},
		// Re-validate the allow-list on every redirect hop so a permitted host
		// cannot bounce the call to a private or non-allow-listed target.
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
	data, _ := io.ReadAll(io.LimitReader(resp.Body, int64(p.maxFetchResp)))
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
