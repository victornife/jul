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
	"sync"
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

// guard is a convenience for tests: a discovery-scoped handle over p.
func guard(p *Policy) *Guard { return p.For(SubsystemDiscovery) }

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
	// Every scoped handle over a nil policy is a no-op.
	if guard(p).Enabled() {
		t.Error("guard over a nil policy must be disabled")
	}
}

func TestNewRejectsEmptyAllow(t *testing.T) {
	for _, allow := range [][]string{nil, {}, {"   ", ""}} {
		if _, err := New(config.EgressConfig{Enabled: true, Allow: allow}); err == nil {
			t.Errorf("New(enabled, allow=%v) expected an error", allow)
		}
	}
}

func TestNewRejectsAmbiguousEntries(t *testing.T) {
	for _, entry := range []string{
		"https://idp.example.com", // a URL
		"idp.example.com/path",    // a path
		"10.0.0.0/33",             // invalid CIDR
		"host name",               // space
		"idp.example.com:443",     // explicit port
		"user@idp.example.com",    // userinfo
		"a..b.example.com",        // empty label
	} {
		if _, err := New(config.EgressConfig{Enabled: true, Allow: []string{entry}}); err == nil {
			t.Errorf("New(allow=%q) expected an error, got nil", entry)
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

func TestNewDeduplicatesEntries(t *testing.T) {
	p := mustPolicy(t,
		"idp.example.com", "IDP.Example.com", "idp.example.com.", // one host rule
		"10.0.0.0/8", "10.1.2.3/8", // same masked network
		"203.0.113.7", "203.0.113.7", // one bare IP
	)
	if len(p.hosts) != 1 {
		t.Errorf("hosts = %v, want 1 after dedup", p.hosts)
	}
	if len(p.cidrs) != 2 {
		t.Errorf("cidrs = %d, want 2 after dedup", len(p.cidrs))
	}
}

func TestNewNormalizesIDNA(t *testing.T) {
	p := mustPolicy(t, "bücher.example") // Unicode IDN → punycode
	if got := p.hosts[0].value; got != "xn--bcher-kva.example" {
		t.Errorf("IDN host = %q, want punycode xn--bcher-kva.example", got)
	}
	if !p.allowHost("xn--bcher-kva.example") {
		t.Error("normalized IDN host should match its ASCII form")
	}
}

func TestAllowHost(t *testing.T) {
	p := mustPolicy(t, "idp.example.com", ".internal.corp")
	cases := map[string]bool{
		"idp.example.com":        true,
		"api.internal.corp":      true, // suffix
		"deep.api.internal.corp": true,
		"internal.corp":          false, // apex is not matched by ".internal.corp"
		"evilinternal.corp":      false, // suffix must be on a label boundary
		"idp.example.com.evil":   false,
		"example.com":            false,
		"":                       false,
	}
	for host, want := range cases {
		if got := p.allowHost(normalizeDialHost(host)); got != want {
			t.Errorf("allowHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestAllowHostNormalizesTarget(t *testing.T) {
	p := mustPolicy(t, "idp.example.com")
	for _, h := range []string{"IDP.Example.COM", "idp.example.com."} {
		if !p.allowHost(normalizeDialHost(h)) {
			t.Errorf("allowHost(normalize(%q)) = false, want true", h)
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
		conn, err := guard(p).DialContext(&net.Dialer{Timeout: 2 * time.Second})(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		_ = conn.Close()
	})

	t.Run("unlisted literal blocked with typed reason", func(t *testing.T) {
		p := mustPolicy(t, "10.0.0.0/8")
		_, err := guard(p).DialContext(&net.Dialer{Timeout: 2 * time.Second})(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("err = %v, want ErrBlocked", err)
		}
		var be *BlockError
		if !errors.As(err, &be) {
			t.Fatalf("err = %v, want *BlockError", err)
		}
		if be.Reason != ReasonIPNotAllowed {
			t.Errorf("reason = %q, want %q", be.Reason, ReasonIPNotAllowed)
		}
		if be.Subsystem != SubsystemDiscovery {
			t.Errorf("subsystem = %q, want %q", be.Subsystem, SubsystemDiscovery)
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
		conn, err := guard(p).DialContext(&net.Dialer{Timeout: 2 * time.Second})(context.Background(), "tcp", net.JoinHostPort("svc.internal", port))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		_ = conn.Close()
	})

	t.Run("all resolved IPs outside CIDR blocked (host_not_allowed)", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		p.resolver = fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("10.0.0.9")}}}
		_, err := guard(p).DialContext(&net.Dialer{})(context.Background(), "tcp", "svc.internal:443")
		var be *BlockError
		if !errors.As(err, &be) || be.Reason != ReasonHostNotAllowed {
			t.Fatalf("err = %v, want host_not_allowed BlockError", err)
		}
	})

	t.Run("mixed resolved IPs blocked (rebinding)", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		p.resolver = fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("169.254.169.254")}}}
		_, err := guard(p).DialContext(&net.Dialer{})(context.Background(), "tcp", "svc.internal:80")
		var be *BlockError
		if !errors.As(err, &be) || be.Reason != ReasonMixedDNS {
			t.Fatalf("err = %v, want mixed_dns_answers BlockError", err)
		}
	})

	t.Run("no resolved IPs blocked", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		p.resolver = fakeResolver{ips: nil}
		_, err := guard(p).DialContext(&net.Dialer{})(context.Background(), "tcp", "svc.internal:80")
		var be *BlockError
		if !errors.As(err, &be) || be.Reason != ReasonNoDNSAnswers {
			t.Fatalf("err = %v, want no_dns_answers BlockError", err)
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
	conn, err := guard(p).DialContext(&net.Dialer{Timeout: 2 * time.Second})(context.Background(), "tcp", net.JoinHostPort("localhost", port))
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
	conn, err := guard(p).DialContext(&net.Dialer{Timeout: 2 * time.Second})(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatalf("disabled policy must dial freely: %v", err)
	}
	_ = conn.Close()
}

func TestObserverReportsDecisions(t *testing.T) {
	var mu sync.Mutex
	var decisions []Decision
	p, err := New(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.0/8"}},
		WithObserver(func(d Decision) {
			mu.Lock()
			decisions = append(decisions, d)
			mu.Unlock()
		}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port := serverHostPort(t, srv.URL)

	// Allowed dial → one allow decision.
	conn, err := p.For(SubsystemAuth).DialContext(&net.Dialer{Timeout: 2 * time.Second})(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	// Blocked dial → one block decision.
	_, _ = p.For(SubsystemACME).DialContext(&net.Dialer{})(context.Background(), "tcp", "10.0.0.1:80")

	mu.Lock()
	defer mu.Unlock()
	if len(decisions) != 2 {
		t.Fatalf("decisions = %d, want 2 (%v)", len(decisions), decisions)
	}
	if decisions[0].Result != ResultAllow || decisions[0].Subsystem != SubsystemAuth {
		t.Errorf("first decision = %+v, want allow/auth", decisions[0])
	}
	if decisions[1].Result != ResultBlock || decisions[1].Subsystem != SubsystemACME || decisions[1].Reason != ReasonIPNotAllowed {
		t.Errorf("second decision = %+v, want block/acme/ip_not_allowed", decisions[1])
	}
}

func TestClientEnforcesPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Run("allowed", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		resp, err := guard(p).Client(3 * time.Second).Get(srv.URL)
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
		_, err := guard(p).Client(3 * time.Second).Get(srv.URL)
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("err = %v, want ErrBlocked", err)
		}
	})
}

func TestClientIgnoresProxyEnv(t *testing.T) {
	// A guarded client must pin Proxy=nil so HTTP(S)_PROXY cannot hide the target.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1") // would fail if honoured
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := mustPolicy(t, "127.0.0.0/8")
	resp, err := guard(p).Client(3 * time.Second).Get(srv.URL)
	if err != nil {
		t.Fatalf("guarded client honoured a proxy: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestClientRedirectToBlockedHost(t *testing.T) {
	// The upstream redirects to a host that resolves to a disallowed address.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://blocked.internal/next", http.StatusFound)
	}))
	defer upstream.Close()
	_, port := serverHostPort(t, upstream.URL)

	p := mustPolicy(t, "127.0.0.0/8")
	p.resolver = fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("10.0.0.9")}}}
	// Rewrite the redirect target's port onto the fake resolver via the same
	// blocked host; the dial resolves blocked.internal → 10.0.0.9 (outside CIDR).
	_ = port
	_, err := guard(p).Client(3 * time.Second).Get(upstream.URL)
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("redirect to blocked host: err = %v, want ErrBlocked", err)
	}
}

func TestDialWithComposesUnderPolicy(t *testing.T) {
	// DialContextWith wraps a base DialFunc: the policy decides first, then the
	// base runs. A blocked host never reaches the base dialer.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port := serverHostPort(t, srv.URL)

	var baseCalled bool
	base := func(ctx context.Context, network, addr string) (net.Conn, error) {
		baseCalled = true
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, addr)
	}

	p := mustPolicy(t, "10.0.0.0/8") // 127.0.0.1 is not allowed
	_, err := p.For(SubsystemPlugin).DialContextWith(base)(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	if baseCalled {
		t.Error("base dialer must not run for a blocked destination")
	}

	// An allowed destination reaches the base dialer.
	p2 := mustPolicy(t, "127.0.0.0/8")
	conn, err := p2.For(SubsystemPlugin).DialContextWith(base)(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatalf("allowed dial: %v", err)
	}
	_ = conn.Close()
	if !baseCalled {
		t.Error("base dialer should run for an allowed destination")
	}
}

func TestConcurrentDialsRace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port := serverHostPort(t, srv.URL)

	p := mustPolicy(t, "127.0.0.0/8")
	p.resolver = fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	g := guard(p)
	dial := g.DialContext(&net.Dialer{Timeout: 2 * time.Second})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := dial(context.Background(), "tcp", net.JoinHostPort("svc.internal", port))
			if err == nil {
				_ = conn.Close()
			}
		}()
	}
	wg.Wait()
}
