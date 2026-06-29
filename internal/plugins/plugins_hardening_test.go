//go:build wasmplugins

package plugins

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
