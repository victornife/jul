// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

// mapResolver resolves by hostname so a test can map several names to different
// addresses (unlike fakeResolver, which returns one fixed set for any host). It
// honors context cancellation so the timeout/cancellation path can be exercised.
type mapResolver struct {
	byHost map[string][]net.IPAddr
	err    error
}

func (m mapResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.byHost[host], nil
}

func ipAddrs(ss ...string) []net.IPAddr {
	out := make([]net.IPAddr, 0, len(ss))
	for _, s := range ss {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out
}

// TestClientRedirectToAllowedHost is the allowed-redirect counterpart to
// TestClientRedirectToBlockedHost: a redirect whose target is inside the
// allow-list is followed successfully.
func TestClientRedirectToAllowedHost(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer final.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirector.Close()

	// Both servers listen on 127.0.0.1, which is inside the allow-list, so the
	// redirect target is re-checked and permitted.
	p := mustPolicy(t, "127.0.0.0/8")
	resp, err := guard(p).Client(3 * time.Second).Get(redirector.URL)
	if err != nil {
		t.Fatalf("get through allowed redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("final status = %d, want %d (redirect not followed to allowed host)", resp.StatusCode, http.StatusTeapot)
	}
}

// TestTLSSNIAndHostPreservedWhenDialingIP proves the guard substitutes only the
// dial target, never the name the TLS/HTTP layer uses: when a CIDR-only hostname
// is dialed by its resolved IP, the TLS SNI and the HTTP Host header still carry
// the original hostname.
func TestTLSSNIAndHostPreservedWhenDialingIP(t *testing.T) {
	var mu sync.Mutex
	var gotSNI, gotHost string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.TLS != nil {
			gotSNI = r.TLS.ServerName
		}
		gotHost = r.Host
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port := serverHostPort(t, srv.URL) // 127.0.0.1

	// "svc.internal" is not listed by name; it resolves to the loopback address
	// the TLS server listens on, which is inside the allowed CIDR, so the guard
	// dials the IP directly.
	p := mustPolicy(t, "127.0.0.0/8")
	p.resolver = mapResolver{byHost: map[string][]net.IPAddr{"svc.internal": ipAddrs("127.0.0.1")}}

	tr := &http.Transport{
		DialContext:     guard(p).DialContext(&net.Dialer{Timeout: 2 * time.Second}),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only; asserting SNI, not verifying the cert name
		Proxy:           nil,
	}
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	resp, err := client.Get("https://svc.internal:" + port + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	mu.Lock()
	defer mu.Unlock()
	if gotSNI != "svc.internal" {
		t.Errorf("TLS SNI = %q, want %q (guard must not rewrite the name to the IP)", gotSNI, "svc.internal")
	}
	if gotHost != "svc.internal:"+port && gotHost != "svc.internal" {
		t.Errorf("HTTP Host = %q, want svc.internal[:port]", gotHost)
	}
}

// TestConnectionReuseDoesNotBypassCheck proves a pooled keep-alive connection to
// an allowed host is not reused to reach a different, disallowed host: each new
// host is re-evaluated by the guard.
func TestConnectionReuseDoesNotBypassCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port := serverHostPort(t, srv.URL)

	p := mustPolicy(t, "127.0.0.0/8")
	p.resolver = mapResolver{byHost: map[string][]net.IPAddr{
		"allowed.svc": ipAddrs("127.0.0.1"), // inside the CIDR
		"blocked.svc": ipAddrs("10.0.0.9"),  // outside the CIDR
	}}
	client := guard(p).Client(3 * time.Second)

	// First request to an allowed host establishes a pooled connection.
	resp, err := client.Get("http://allowed.svc:" + port + "/")
	if err != nil {
		t.Fatalf("allowed request: %v", err)
	}
	_ = resp.Body.Close()

	// A follow-up request to a different, disallowed host must not ride the
	// pooled connection; it is blocked after resolution.
	_, err = client.Get("http://blocked.svc:" + port + "/")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("second host err = %v, want ErrBlocked (pool must not bypass the check)", err)
	}
}

// TestContextCancellationPropagates proves context cancellation and deadlines
// flow through the guarded dial on both the IP-literal and the resolved-name
// paths, rather than being swallowed or turned into a policy block.
func TestContextCancellationPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port := serverHostPort(t, srv.URL)

	t.Run("cancelled context on IP-literal dial", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already cancelled before dialing
		_, err := guard(p).DialContext(&net.Dialer{Timeout: 2 * time.Second})(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrBlocked) {
			t.Error("cancellation must not be reported as a policy block")
		}
	})

	t.Run("cancelled context on resolved-name dial", func(t *testing.T) {
		p := mustPolicy(t, "127.0.0.0/8")
		p.resolver = mapResolver{byHost: map[string][]net.IPAddr{"svc.internal": ipAddrs("127.0.0.1")}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := guard(p).DialContext(&net.Dialer{})(ctx, "tcp", net.JoinHostPort("svc.internal", port))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled from the resolver seam", err)
		}
	})
}

// TestResolverSeamRace exercises the DNS resolver seam and the decision observer
// concurrently so the race detector can prove the resolve/observe path holds no
// unsynchronized shared state.
func TestResolverSeamRace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port := serverHostPort(t, srv.URL)

	var count int64
	var mu sync.Mutex
	p, err := New(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.0/8"}},
		WithResolver(mapResolver{byHost: map[string][]net.IPAddr{"svc.internal": ipAddrs("127.0.0.1")}}),
		WithObserver(func(Decision) { mu.Lock(); count++; mu.Unlock() }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dial := p.For(SubsystemDiscovery).DialContext(&net.Dialer{Timeout: 2 * time.Second})

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if conn, err := dial(context.Background(), "tcp", net.JoinHostPort("svc.internal", port)); err == nil {
				_ = conn.Close()
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if count != 40 {
		t.Errorf("observer saw %d decisions, want 40", count)
	}
}

// TestBlockedRequestsDoNotLeak proves a refused destination opens no connection
// and leaks no goroutine: repeated blocked dials never reach the base dialer and
// leave the goroutine count stable.
func TestBlockedRequestsDoNotLeak(t *testing.T) {
	p := mustPolicy(t, "127.0.0.0/8")
	p.resolver = mapResolver{byHost: map[string][]net.IPAddr{"blocked.svc": ipAddrs("10.0.0.9")}}

	var dials int64
	base := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials++
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	dial := guard(p).DialContextWith(base)

	before := runtime.NumGoroutine()
	for i := 0; i < 300; i++ {
		if _, err := dial(context.Background(), "tcp", "blocked.svc:443"); !errors.Is(err, ErrBlocked) {
			t.Fatalf("iter %d: err = %v, want ErrBlocked", i, err)
		}
	}
	if dials != 0 {
		t.Errorf("base dialer called %d times for blocked destinations, want 0 (no connection opened)", dials)
	}

	// Let any transient goroutines settle, then confirm the count did not grow.
	after := settleGoroutines(before)
	if after > before+5 {
		t.Errorf("goroutines grew from %d to %d after repeated blocks (possible leak)", before, after)
	}
}

// settleGoroutines waits briefly for goroutines to return to at most baseline,
// returning the observed count. It avoids flakiness from unrelated runtime
// goroutines by polling a few times.
func settleGoroutines(baseline int) int {
	n := runtime.NumGoroutine()
	for i := 0; i < 20 && n > baseline; i++ {
		time.Sleep(10 * time.Millisecond)
		runtime.GC()
		n = runtime.NumGoroutine()
	}
	return n
}
