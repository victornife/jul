// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/egress"
)

func TestHostAllowed(t *testing.T) {
	allowed := []string{"api.example.com", ".trusted.net"}
	cases := map[string]bool{
		"api.example.com": true,
		"x.trusted.net":   true,
		"trusted.net":     false,
		"evil.com":        false,
		"":                false,
	}
	for host, want := range cases {
		if got := hostAllowed(allowed, host); got != want {
			t.Errorf("hostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestIPBlocked(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.1.1", "100.64.0.1", "0.0.0.0", "::1"}
	for _, s := range blocked {
		if !ipBlocked(net.ParseIP(s)) {
			t.Errorf("ipBlocked(%s) = false, want true", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1"}
	for _, s := range allowed {
		if ipBlocked(net.ParseIP(s)) {
			t.Errorf("ipBlocked(%s) = true, want false (public)", s)
		}
	}
}

func TestFetchBlocksDisallowedHost(t *testing.T) {
	p := &plugin{capFetch: true, allowedHosts: []string{"api.example.com"}, fetchTimeout: time.Second, maxFetchResp: 1 << 10}
	if _, _, err := p.doFetch(context.Background(), "GET", "https://evil.com/", nil); err != errFetchBlocked {
		t.Fatalf("doFetch disallowed host err = %v, want errFetchBlocked", err)
	}
}

func TestFetchBlocksLoopbackEvenIfAllowed(t *testing.T) {
	srv := httptest.NewServer(nil)
	defer srv.Close()
	p := &plugin{capFetch: true, allowedHosts: []string{"127.0.0.1"}, fetchTimeout: time.Second, maxFetchResp: 1 << 10}
	if _, _, err := p.doFetch(context.Background(), "GET", srv.URL, nil); err == nil {
		t.Fatal("doFetch to loopback succeeded, want SSRF guard rejection")
	}
}

// rebindResolver resolves any host to a fixed address, simulating a DNS record
// whose value the attacker controls (the rebinding to a private IP).
type rebindResolver struct{ ip string }

func (r rebindResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP(r.ip)}}, nil
}

func TestFetchBlocksDNSRebinding(t *testing.T) {
	// Allow-listed host that resolves to a private address: the dialer must
	// validate and dial the resolved IP, so the call is blocked, not connected.
	p := &plugin{
		capFetch:     true,
		allowedHosts: []string{"api.example.com"},
		fetchTimeout: time.Second,
		maxFetchResp: 1 << 10,
		resolver:     rebindResolver{ip: "127.0.0.1"},
	}
	if _, _, err := p.doFetch(context.Background(), "GET", "https://api.example.com/", nil); err == nil {
		t.Fatal("doFetch to rebound private IP succeeded, want SSRF guard rejection")
	}
}

// dialerFunc adapts a function to the dialer interface so tests can observe the
// address the fetch chain would connect to without a real network dial.
type dialerFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func (f dialerFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

// TestFetchDialIntersection proves the WASM plugin fetch enforces both its own
// SSRF guard and the global [egress] allow-list: a destination must satisfy both
// to connect, a global-policy refusal wraps egress.ErrBlocked (distinct from the
// local errFetchBlocked), and the SSRF guard still refuses a private address
// even when the global policy would allow it.
func TestFetchDialIntersection(t *testing.T) {
	pluginWrap := func(allow ...string) (func(base DialFunc) DialFunc, ipResolver) {
		res := rebindResolver{ip: "8.8.8.8"} // public, passes the SSRF guard
		pol, err := egress.New(config.EgressConfig{Enabled: true, Allow: allow}, egress.WithResolver(res))
		if err != nil {
			t.Fatalf("egress.New: %v", err)
		}
		return pol.For(egress.SubsystemPlugin).DialContextWith, res
	}

	var dialed string
	base := dialerFunc(func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = addr
		c, _ := net.Pipe()
		return c, nil
	})

	t.Run("both allowed reaches the dialer", func(t *testing.T) {
		dialed = ""
		wrap, res := pluginWrap("8.0.0.0/8")
		p := &plugin{resolver: res, egressWrap: wrap}
		conn, err := p.fetchDial(base, res)(context.Background(), "tcp", "api.example.com:443")
		if err != nil {
			t.Fatalf("both allowed: %v", err)
		}
		_ = conn.Close()
		if dialed != "8.8.8.8:443" {
			t.Errorf("dialed = %q, want 8.8.8.8:443", dialed)
		}
	})

	t.Run("global policy blocks before the dialer", func(t *testing.T) {
		dialed = ""
		wrap, res := pluginWrap("10.0.0.0/8") // 8.8.8.8 is outside
		p := &plugin{resolver: res, egressWrap: wrap}
		_, err := p.fetchDial(base, res)(context.Background(), "tcp", "api.example.com:443")
		if !errors.Is(err, egress.ErrBlocked) {
			t.Fatalf("global blocked: err = %v, want egress.ErrBlocked", err)
		}
		if errors.Is(err, errFetchBlocked) {
			t.Error("a global block must not be reported as a local block")
		}
		if dialed != "" {
			t.Error("base dialer must not run when the global policy blocks")
		}
	})

	t.Run("SSRF guard blocks a globally allowed private address", func(t *testing.T) {
		loop := rebindResolver{ip: "127.0.0.1"}
		pol, err := egress.New(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.0/8"}}, egress.WithResolver(loop))
		if err != nil {
			t.Fatalf("egress.New: %v", err)
		}
		p := &plugin{resolver: loop, egressWrap: pol.For(egress.SubsystemPlugin).DialContextWith}
		_, err = p.fetchDial(base, loop)(context.Background(), "tcp", "api.example.com:443")
		if !errors.Is(err, errFetchBlocked) {
			t.Fatalf("ssrf blocked: err = %v, want errFetchBlocked", err)
		}
	})
}

// TestFetchGlobalEgressBlocksDoFetch proves the intersection end-to-end through
// doFetch: a host permitted by the plugin's allowed_hosts is still refused by
// the global policy, and the error wraps egress.ErrBlocked so the host maps it
// to the distinct guest code.
func TestFetchGlobalEgressBlocksDoFetch(t *testing.T) {
	res := rebindResolver{ip: "8.8.8.8"}
	pol, err := egress.New(config.EgressConfig{Enabled: true, Allow: []string{"10.0.0.0/8"}}, egress.WithResolver(res))
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	p := &plugin{
		capFetch:     true,
		allowedHosts: []string{"api.example.com"}, // local rule allows
		fetchTimeout: time.Second,
		maxFetchResp: 1 << 10,
		resolver:     res,
		egressWrap:   pol.For(egress.SubsystemPlugin).DialContextWith,
	}
	_, _, err = p.doFetch(context.Background(), "GET", "https://api.example.com/", nil)
	if !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("err = %v, want wrapping egress.ErrBlocked", err)
	}
	if errors.Is(err, errFetchBlocked) {
		t.Error("global egress block must not be reported as a local block")
	}
}

func TestKVSetEnforcesBounds(t *testing.T) {
	p := &plugin{kv: newMemKV(), kvKeys: map[string]int{}, kvMaxEntries: 2, kvMaxBytes: 100}
	if !p.kvSet("a", []byte("x")) || !p.kvSet("b", []byte("y")) {
		t.Fatal("first two keys should fit")
	}
	if p.kvSet("c", []byte("z")) {
		t.Fatal("third distinct key should exceed kv_max_entries")
	}
	if !p.kvSet("a", []byte("updated")) {
		t.Fatal("updating an existing key should be allowed")
	}
	big := make([]byte, 200)
	if p.kvSet("a", big) {
		t.Fatal("value over kv_max_bytes should be rejected")
	}
}

func TestFlushRejectsInvalidStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	inv := &invocation{w: rec, log: slog.New(slog.NewTextHandler(io.Discard, nil)), status: 700}
	inv.flush()
	if rec.Code != 500 {
		t.Fatalf("flush status = %d, want 500 for out-of-range guest status", rec.Code)
	}
}

func TestFetchTruncationDetected(t *testing.T) {
	// A response larger than maxFetchResp should be capped and flagged truncated.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 200))
	}))
	defer srv.Close()

	inv := &invocation{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := withInvocation(context.Background(), inv)

	// Custom client that dials the test server directly, bypassing SSRF guards
	// so this test focuses on truncation detection only.
	testClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", srv.Listener.Addr().String())
			},
		},
	}

	p := &plugin{capFetch: true, allowedHosts: []string{"127.0.0.1"}, fetchTimeout: time.Second, maxFetchResp: 100, client: testClient}
	status, data, err := p.doFetch(ctx, "GET", srv.URL, nil)
	if err != nil {
		t.Fatalf("doFetch err = %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(data) != 100 {
		t.Fatalf("len(data) = %d, want 100 (capped)", len(data))
	}
	if !inv.lastFetchTruncated {
		t.Fatal("lastFetchTruncated = false, want true")
	}
	if !bytes.Equal(inv.lastFetch, data) {
		t.Fatal("lastFetch mismatch")
	}
}

func TestFetchNotTruncated(t *testing.T) {
	// A response smaller than maxFetchResp should not be flagged truncated.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("small"))
	}))
	defer srv.Close()

	inv := &invocation{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := withInvocation(context.Background(), inv)

	testClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", srv.Listener.Addr().String())
			},
		},
	}

	p := &plugin{capFetch: true, allowedHosts: []string{"127.0.0.1"}, fetchTimeout: time.Second, maxFetchResp: 100, client: testClient}
	status, data, err := p.doFetch(ctx, "GET", srv.URL, nil)
	if err != nil {
		t.Fatalf("doFetch err = %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if string(data) != "small" {
		t.Fatalf("data = %q, want \"small\"", string(data))
	}
	if inv.lastFetchTruncated {
		t.Fatal("lastFetchTruncated = true, want false")
	}
}

func TestFetchClearsLastFetchOnError(t *testing.T) {
	// A successful fetch stores a response; a subsequent failed fetch must
	// clear it so fetch_read does not expose stale data.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("live"))
	}))
	defer live.Close()

	inv := &invocation{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := withInvocation(context.Background(), inv)

	// First client succeeds.
	okClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", live.Listener.Addr().String())
			},
		},
	}
	p := &plugin{capFetch: true, allowedHosts: []string{"127.0.0.1"}, fetchTimeout: time.Second, maxFetchResp: 100, client: okClient}
	if _, _, err := p.doFetch(ctx, "GET", live.URL, nil); err != nil {
		t.Fatalf("first fetch err = %v", err)
	}
	if inv.lastFetch == nil {
		t.Fatal("lastFetch nil after success, want data")
	}

	// Second client fails (dial refused).
	failClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", "127.0.0.1:1") // port 1 is almost certainly refused
			},
		},
	}
	p.client = failClient
	if _, _, err := p.doFetch(ctx, "GET", "http://127.0.0.1/", nil); err == nil {
		t.Fatal("second fetch succeeded, want error")
	}
	if inv.lastFetch != nil {
		t.Fatalf("lastFetch not cleared after failure, got %q", inv.lastFetch)
	}
	if inv.lastFetchTruncated {
		t.Fatal("lastFetchTruncated true after failure, want false")
	}
}

// errRoundTripper returns a fixed *http.Response; used to inject a body reader
// that fails mid-read without involving real network I/O.
type errRoundTripper struct{ resp *http.Response }

func (e *errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return e.resp, nil }

// errorReader always returns a read error, simulating a connection reset.
type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestFetchBodyReadErrorReturnsTransportError(t *testing.T) {
	// If the upstream connects but the response body read fails mid-way,
	// doFetch must return the read error (mapped to guest code -4) and
	// clear lastFetch so the guest cannot act on partial data.
	inv := &invocation{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := withInvocation(context.Background(), inv)

	p := &plugin{
		capFetch:     true,
		allowedHosts: []string{"example.com"},
		fetchTimeout: time.Second,
		maxFetchResp: 1 << 10,
		client: &http.Client{
			Transport: &errRoundTripper{
				resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/plain"}},
					Body:       io.NopCloser(errorReader{}),
				},
			},
		},
	}

	_, _, err := p.doFetch(ctx, "GET", "http://example.com/", nil)
	if err == nil {
		t.Fatal("doFetch with body read error succeeded, want error")
	}
	if inv.lastFetch != nil {
		t.Fatalf("lastFetch not cleared after body read error, got %q", inv.lastFetch)
	}
	if inv.lastFetchTruncated {
		t.Fatal("lastFetchTruncated true after body read error, want false")
	}
}
func TestPluginCloseClosesIdleConnections(t *testing.T) {
	// close() must be nil-safe and tolerate missing runtime/client without panic.
	var nilP *plugin
	nilP.close()

	pNoClient := &plugin{}
	pNoClient.close()

	pWithClient := &plugin{client: &http.Client{}}
	pWithClient.close() // runtime is nil so it returns early; still must not panic
}

// multiIPResolver returns a fixed list of IP addresses in order.
type multiIPResolver struct{ ips []string }

func (r multiIPResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	var addrs []net.IPAddr
	for _, s := range r.ips {
		addrs = append(addrs, net.IPAddr{IP: net.ParseIP(s)})
	}
	return addrs, nil
}

// mockDialer fails on the first N calls, then returns a pipe connection.
type mockDialer struct {
	failCount int
	attempted []string
	lastErr   error
}

func (m *mockDialer) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	m.attempted = append(m.attempted, address)
	if m.failCount > 0 {
		m.failCount--
		m.lastErr = errors.New("connection refused")
		return nil, m.lastErr
	}
	c, _ := net.Pipe()
	_ = network
	return c, nil
}

func TestFetchTriesMultipleValidatedIPs(t *testing.T) {
	// Use public-range IPs so the SSRF guard does not block them.
	resolver := multiIPResolver{ips: []string{"8.8.8.8", "8.8.4.4"}}
	md := &mockDialer{failCount: 1}

	conn, err := dialValidatedIPs(context.Background(), md, resolver, "tcp", "example.com", "443")
	if err != nil {
		t.Fatalf("dialValidatedIPs err = %v, want nil", err)
	}
	defer conn.Close()

	// Should have attempted two IP addresses.
	if len(md.attempted) != 2 {
		t.Fatalf("attempted %d addrs, want 2: %v", len(md.attempted), md.attempted)
	}
	if md.attempted[0] != "8.8.8.8:443" {
		t.Fatalf("first attempt = %q, want 8.8.8.8:443", md.attempted[0])
	}
	if md.attempted[1] != "8.8.4.4:443" {
		t.Fatalf("second attempt = %q, want 8.8.4.4:443", md.attempted[1])
	}
}

func TestFetchReturnsTransportErrorWhenAllIPsFail(t *testing.T) {
	// Every validated public IP fails to dial: the error must be a transport
	// failure, not errFetchBlocked, so guests get code -4 instead of -3.
	resolver := multiIPResolver{ips: []string{"8.8.8.8", "8.8.4.4"}}
	md := &mockDialer{failCount: 2}

	_, err := dialValidatedIPs(context.Background(), md, resolver, "tcp", "example.com", "443")
	if err == nil {
		t.Fatal("dialValidatedIPs err = nil, want a transport error")
	}
	if errors.Is(err, errFetchBlocked) {
		t.Fatalf("dialValidatedIPs err = %v, must not be errFetchBlocked", err)
	}
	if len(md.attempted) != 2 {
		t.Fatalf("attempted %d addrs, want 2: %v", len(md.attempted), md.attempted)
	}
}
