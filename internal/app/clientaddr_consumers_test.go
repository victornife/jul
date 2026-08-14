// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/auth"
	"jul/internal/clientaddr"
	"jul/internal/config"
	"jul/internal/middleware"
)

// identityChain builds the production per-listener chain for a policy trusting
// trusted, ending in h. It is the real chain, so what these tests observe is
// what a served request observes.
func identityChain(t *testing.T, f *HandlerFactory, trusted []string, h http.Handler) http.Handler {
	t.Helper()
	policy, err := clientaddr.NewPolicy(trusted, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	return middleware.Chain(h, f.globalChain(policy, nil)...)
}

func request(remoteAddr, xff string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	return req
}

// TestCIDRAuthUsesCanonicalClient is the spoofing matrix for CIDR
// authentication: a forwarding header only moves a client into or out of a
// range when the immediate peer is a trusted proxy.
func TestCIDRAuthUsesCanonicalClient(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	tests := []struct {
		name       string
		allow      []string
		deny       []string
		trusted    []string
		remoteAddr string
		xff        string
		wantStatus int
	}{
		{
			name:       "direct client inside the allow list",
			allow:      []string{"198.51.100.0/24"},
			remoteAddr: "198.51.100.9:5555",
			wantStatus: http.StatusOK,
		},
		{
			name:       "untrusted peer cannot spoof its way into the allow list",
			allow:      []string{"198.51.100.0/24"},
			remoteAddr: "203.0.113.7:5555",
			xff:        "198.51.100.9",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "untrusted peer cannot spoof its way out of the deny list",
			deny:       []string{"203.0.113.0/24"},
			remoteAddr: "203.0.113.7:5555",
			xff:        "198.51.100.9",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "trusted proxy carries the client into the allow list",
			allow:      []string{"198.51.100.0/24"},
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			xff:        "198.51.100.9",
			wantStatus: http.StatusOK,
		},
		{
			name:       "the proxy's own address is not the client",
			allow:      []string{"10.0.0.0/8"},
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			xff:        "198.51.100.9",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "trusted proxy carries the client into the deny list",
			deny:       []string{"198.51.100.0/24"},
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			xff:        "198.51.100.9",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "malformed chain from a trusted proxy falls back to the peer",
			allow:      []string{"198.51.100.0/24"},
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			xff:        "not-an-address",
			wantStatus: http.StatusForbidden,
		},
		{
			// The peer is a proxy, and an allow list naming the proxy network
			// must not admit a request whose client could not be resolved.
			name:       "malformed chain is not attributed to a trusted proxy inside the allow list",
			allow:      []string{"10.0.0.0/8"},
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			xff:        "not-an-address",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "over-hop chain is not attributed to a trusted proxy inside the allow list",
			allow:      []string{"10.0.0.0/8"},
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			xff:        strings.TrimSuffix(strings.Repeat("198.51.100.9, ", 20), ", "),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "oversized chain is not attributed to a trusted proxy inside the allow list",
			allow:      []string{"10.0.0.0/8"},
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			xff:        strings.TrimSuffix(strings.Repeat("198.51.100.9, ", 700), ", "),
			wantStatus: http.StatusForbidden,
		},
		{
			// An untrusted sender is the client, so its own address still
			// decides: only a resolvable client is judged, not a proxy hop.
			name:       "an untrusted peer is still judged on its own address",
			allow:      []string{"203.0.113.0/24"},
			remoteAddr: "203.0.113.7:5555",
			xff:        "198.51.100.9",
			wantStatus: http.StatusOK,
		},
		{
			name:       "ipv6 client through an ipv6 proxy",
			allow:      []string{"2001:db8:900::/48"},
			trusted:    []string{"2001:db8:100::/48"},
			remoteAddr: "[2001:db8:100::5]:443",
			xff:        "2001:db8:900::1",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := auth.New(t.Context(), config.AuthConfig{Allow: tt.allow, Deny: tt.deny}, auth.Options{})
			if err != nil {
				t.Fatalf("auth.New: %v", err)
			}
			protected := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			rr := httptest.NewRecorder()
			identityChain(t, f, tt.trusted, protected).ServeHTTP(rr, request(tt.remoteAddr, tt.xff))

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tt.wantStatus)
			}
		})
	}
}

// TestRateLimitKeyUsesCanonicalClient proves that two clients behind one
// trusted proxy get separate buckets, while an untrusted peer cannot escape
// its own bucket by asserting an address.
func TestRateLimitKeyUsesCanonicalClient(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	keyOf := func(trusted []string, remoteAddr, xff string) string {
		var key string
		probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			key = middleware.RateKeyFunc("ip")(r)
		})
		identityChain(t, f, trusted, probe).ServeHTTP(httptest.NewRecorder(), request(remoteAddr, xff))
		return key
	}

	trusted := []string{"10.0.0.0/8"}
	if got := keyOf(trusted, "10.1.2.3:5555", "198.51.100.9"); got != "198.51.100.9" {
		t.Errorf("key behind a trusted proxy = %q, want the canonical client", got)
	}
	if got := keyOf(trusted, "10.1.2.3:6666", "203.0.113.4"); got != "203.0.113.4" {
		t.Errorf("second client behind the same proxy = %q, want its own bucket", got)
	}
	if got := keyOf(trusted, "203.0.113.7:5555", "198.51.100.9"); got != "203.0.113.7" {
		t.Errorf("untrusted peer key = %q, want its own address", got)
	}
	if got := keyOf(nil, "203.0.113.7:5555", "198.51.100.9"); got != "203.0.113.7" {
		t.Errorf("direct deployment key = %q, want the peer", got)
	}
	// One normalization for both families: the key is netip's canonical text.
	if got := keyOf(nil, "[::ffff:203.0.113.7]:5555", ""); got != "203.0.113.7" {
		t.Errorf("ipv4-mapped key = %q, want the unmapped form", got)
	}
	if got := keyOf(nil, "[2001:db8::1]:443", ""); got != "2001:db8::1" {
		t.Errorf("ipv6 key = %q, want the bare address", got)
	}

	// A degraded chain from a trusted proxy names no client, so it must not land
	// in the bucket the proxy's correctly attributed traffic uses.
	attributed := keyOf(trusted, "10.1.2.3:5555", "198.51.100.9")
	for _, xff := range []string{
		"not-an-address",
		strings.TrimSuffix(strings.Repeat("198.51.100.9, ", 20), ", "),
		strings.TrimSuffix(strings.Repeat("198.51.100.9, ", 700), ", "),
	} {
		degraded := keyOf(trusted, "10.1.2.3:5555", xff)
		if degraded == attributed {
			t.Errorf("degraded key = %q, want a bucket separate from the attributed client", degraded)
		}
		if degraded == "10.1.2.3" {
			t.Errorf("degraded key = %q, want it separate from the proxy's own address too", degraded)
		}
		if !strings.HasPrefix(degraded, "unattributed:") {
			t.Errorf("degraded key = %q, want the unattributed namespace", degraded)
		}
	}
}

// TestRateLimitHeaderAndJWTKeysFallBackToCanonicalClient checks the fallbacks:
// an operator-selected header key is untrusted by construction, but when it is
// absent the limiter still keys on the canonical client rather than the peer.
func TestRateLimitHeaderAndJWTKeysFallBackToCanonicalClient(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	for _, spec := range []string{"header:X-Tenant", "jwt:sub"} {
		var key string
		probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			key = middleware.RateKeyFunc(spec)(r)
		})
		identityChain(t, f, []string{"10.0.0.0/8"}, probe).
			ServeHTTP(httptest.NewRecorder(), request("10.1.2.3:5555", "198.51.100.9"))
		if key != "198.51.100.9" {
			t.Errorf("%s fallback key = %q, want the canonical client", spec, key)
		}
	}
}

// TestAccessLogRecordsClientAndPeer covers the field contract: client_ip is
// always present, peer_ip appears only when a trusted proxy changed the answer,
// and the removed "remote" field is gone.
func TestAccessLogRecordsClientAndPeer(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	tests := []struct {
		name       string
		trusted    []string
		remoteAddr string
		xff        string
		wantClient string
		wantPeer   string
		wantSource string
		wantResult string
	}{
		{
			name:       "direct client omits peer_ip",
			remoteAddr: "203.0.113.7:5555",
			wantClient: "203.0.113.7",
			wantSource: "peer",
			wantResult: "accepted",
		},
		{
			name:       "proxied client records both",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			xff:        "198.51.100.9",
			wantClient: "198.51.100.9",
			wantPeer:   "10.1.2.3",
			wantSource: "xff",
			wantResult: "accepted",
		},
		{
			name:       "untrusted sender records only its own address",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "203.0.113.7:5555",
			xff:        "198.51.100.9",
			wantClient: "203.0.113.7",
			wantSource: "peer",
			wantResult: "untrusted_peer",
		},
		{
			// Without the result field this record is byte-identical to a
			// request that genuinely originated at the proxy, because peer_ip
			// is omitted when it equals client_ip.
			name:       "degraded chain is distinguishable from proxy-originated traffic",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			xff:        "not-an-address",
			wantClient: "10.1.2.3",
			wantSource: "peer",
			wantResult: "malformed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &recordingSink{}
			chain := middleware.Chain(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
				mustPolicyChain(t, f, tt.trusted, sink)...,
			)
			chain.ServeHTTP(httptest.NewRecorder(), request(tt.remoteAddr, tt.xff))

			if len(sink.records) != 1 {
				t.Fatalf("recorded %d access records, want 1", len(sink.records))
			}
			rec := sink.records[0]
			if rec.Client != tt.wantClient {
				t.Errorf("client_ip = %q, want %q", rec.Client, tt.wantClient)
			}
			wantPeer := tt.wantPeer
			if wantPeer == "" {
				wantPeer = tt.wantClient
			}
			if rec.Peer != wantPeer {
				t.Errorf("peer = %q, want %q", rec.Peer, wantPeer)
			}
			if rec.ClientSource != tt.wantSource {
				t.Errorf("client_addr_source = %q, want %q", rec.ClientSource, tt.wantSource)
			}
			if rec.ClientResult != tt.wantResult {
				t.Errorf("client_addr_result = %q, want %q", rec.ClientResult, tt.wantResult)
			}
		})
	}
}

// mustPolicyChain builds the production chain with sink installed as the only
// access-log sink.
func mustPolicyChain(t *testing.T, f *HandlerFactory, trusted []string, sink middleware.AccessSink) []middleware.Middleware {
	t.Helper()
	policy, err := clientaddr.NewPolicy(trusted, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	saved := f.AccessSinks
	t.Cleanup(func() { f.AccessSinks = saved })
	f.AccessSinks = []middleware.AccessSink{sink}
	return f.globalChain(policy, nil)
}

type recordingSink struct{ records []middleware.AccessRecord }

func (s *recordingSink) Log(rec middleware.AccessRecord) { s.records = append(s.records, rec) }

// TestAccessLogLineFields pins the emitted field names: client_ip present,
// peer_ip conditional, and no legacy remote field.
func TestAccessLogLineFields(t *testing.T) {
	tests := []struct {
		name       string
		record     middleware.AccessRecord
		wantFields []string
		notFields  []string
	}{
		{
			name:       "direct client",
			record:     middleware.AccessRecord{Client: "203.0.113.7", Peer: "203.0.113.7"},
			wantFields: []string{"client_ip=203.0.113.7"},
			notFields:  []string{"peer_ip", "remote="},
		},
		{
			name:       "proxied client",
			record:     middleware.AccessRecord{Client: "198.51.100.9", Peer: "10.1.2.3"},
			wantFields: []string{"client_ip=198.51.100.9", "peer_ip=10.1.2.3"},
			notFields:  []string{"remote="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			middleware.NewSlogSink(newTextLogger(&buf)).Log(tt.record)
			line := buf.String()
			for _, want := range tt.wantFields {
				if !strings.Contains(line, want) {
					t.Errorf("access line %q is missing %q", line, want)
				}
			}
			for _, unwanted := range tt.notFields {
				if strings.Contains(line, unwanted) {
					t.Errorf("access line %q still contains %q", line, unwanted)
				}
			}
		})
	}
}

// newTextLogger returns a text-format slog logger writing to w with no time or
// level noise, so a test can assert on the attribute list alone.
func newTextLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey || a.Key == slog.LevelKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}
