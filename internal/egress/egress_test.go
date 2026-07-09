// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"jul/internal/config"
)

// fakeResolver returns a fixed set of addresses (or an error) so the CIDR
// resolution path and the DNS-rebinding case can be exercised deterministically.
type fakeResolver struct {
	ips []net.IPAddr
	err error
}

func (f fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return f.ips, f.err
}

func mustPolicy(t *testing.T, allow ...string) *Policy {
	t.Helper()
	p, err := New(config.EgressConfig{Enabled: true, Allow: allow})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p == nil {
		t.Fatal("New returned a nil policy for an enabled config")
	}
	return p
}

func serverHostPort(t *testing.T, rawURL string) (string, string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host:port: %v", err)
	}
	return host, port
}

func TestNewDisabledIsNilPolicy(t *testing.T) {
	p, err := New(config.EgressConfig{Enabled: false, Allow: []string{"example.com"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p != nil || p.Enabled() {
		t.Errorf("disabled config must yield the nil (disabled) policy, got %v", p)
	}
}

func TestNewRejectsEmptyAllow(t *testing.T) {
	for _, allow := range [][]string{nil, {}, {"   ", ""}} {
		if _, err := New(config.EgressConfig{Enabled: true, Allow: allow}); err == nil {
			t.Errorf("New(enabled, allow=%v) expected an error", allow)
		}
	}
}

func TestNewParsesEntries(t *testing.T) {
	p := mustPolicy(t, "idp.example.com", ".internal.corp", "10.0.0.0/8", "203.0.113.7", "2001:db8::/32")
	if len(p.hosts) != 2 {
		t.Errorf("hosts = %v, want 2 (host + suffix)", p.hosts)
	}
	if len(p.cidrs) != 3 { // CIDR, bare IPv4 (/32), IPv6 CIDR
		t.Errorf("cidrs = %d, want 3", len(p.cidrs))
	}
}

func TestAllowHost(t *testing.T) {
	p := mustPolicy(t, "idp.example.com", ".internal.corp")
	cases := map[string]bool{
		"idp.example.com":        true,
		"IDP.Example.COM":        true, // case-insensitive
		"api.internal.corp":      true, // suffix
		"deep.api.internal.corp": true,
		"internal.corp":          false, // apex is not matched by ".internal.corp"
		"idp.example.com.evil":   false,
		"example.com":            false,
		"":                       false,
	}
	for host, want := range cases {
		if got := p.allowHost(host); got != want {
			t.Errorf("allowHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestAllowIP(t *testing.T) {
	p := mustPolicy(t, "10.0.0.0/8", "203.0.113.7", "2001:db8::/32")
	cases := map[string]bool{
		"10.1.2.3":     true,
		"11.0.0.1":     false,
		"203.0.113.7":  true, // bare IP stored as /32
		"203.0.113.8":  false,
		"2001:db8::1":  true,
		"2001:dead::1": false,
	}
	for ipStr, want := range cases {
		if got := p.allowIP(net.ParseIP(ipStr)); got != want {
			t.Errorf("allowIP(%q) = %v, want %v", ipStr, got, want)
		}
	}
	if p.allowIP(nil) {
		t.Error("allowIP(nil) must be false")
	}
}

func TestDialIPLiteral(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port := serverHostPort(t, srv.URL) // host is 127.0.0.1

	t.Run("allowed literal dials", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		conn, err := p.DialContext(&net.Dialer{Timeout: 2 * time.Second})(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		_ = conn.Close()
	})

	t.Run("unlisted literal blocked", func(t *testing.T) {
		p := mustPolicy(t, "10.0.0.0/8")
		_, err := p.DialContext(&net.Dialer{Timeout: 2 * time.Second})(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("err = %v, want ErrBlocked", err)
		}
	})
}

func TestDialResolvedCIDR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port := serverHostPort(t, srv.URL)

	t.Run("all resolved IPs in CIDR dials", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		p.resolver = fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
		conn, err := p.DialContext(&net.Dialer{Timeout: 2 * time.Second})(context.Background(), "tcp", net.JoinHostPort("svc.internal", port))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		_ = conn.Close()
	})

	t.Run("resolved IP outside CIDR blocked", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		p.resolver = fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("10.0.0.9")}}}
		_, err := p.DialContext(&net.Dialer{})(context.Background(), "tcp", "svc.internal:443")
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("err = %v, want ErrBlocked", err)
		}
	})

	t.Run("mixed resolved IPs blocked (rebinding)", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		p.resolver = fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("169.254.169.254")}}}
		_, err := p.DialContext(&net.Dialer{})(context.Background(), "tcp", "svc.internal:80")
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("err = %v, want ErrBlocked", err)
		}
	})

	t.Run("no resolved IPs blocked", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		p.resolver = fakeResolver{ips: nil}
		_, err := p.DialContext(&net.Dialer{})(context.Background(), "tcp", "svc.internal:80")
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("err = %v, want ErrBlocked", err)
		}
	})
}

func TestDialNameAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port := serverHostPort(t, srv.URL)

	// A host listed by name is trusted; the base dialer resolves localhost to the
	// loopback the test server listens on.
	p := mustPolicy(t, "localhost")
	conn, err := p.DialContext(&net.Dialer{Timeout: 2 * time.Second})(context.Background(), "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
}

func TestDisabledPolicyDialsAnything(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port := serverHostPort(t, srv.URL)

	var p *Policy // nil == disabled
	conn, err := p.DialContext(&net.Dialer{Timeout: 2 * time.Second})(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatalf("disabled policy must dial freely: %v", err)
	}
	_ = conn.Close()
}

func TestClientEnforcesPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Run("allowed", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		resp, err := p.Client(3 * time.Second).Get(srv.URL)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d", resp.StatusCode)
		}
	})

	t.Run("blocked", func(t *testing.T) {
		p := mustPolicy(t, "10.0.0.0/8")
		_, err := p.Client(3 * time.Second).Get(srv.URL)
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("err = %v, want ErrBlocked", err)
		}
	})
}
