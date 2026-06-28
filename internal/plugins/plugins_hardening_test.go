//go:build wasmplugins

package plugins

import (
	"context"
	"io"
	"log/slog"
	"net"
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
