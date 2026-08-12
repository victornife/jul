// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"jul/internal/clientaddr"
)

func TestClientAddressInstallsIdentity(t *testing.T) {
	policy, err := clientaddr.NewPolicy([]string{"10.0.0.0/8"}, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	tests := []struct {
		name       string
		remote     string
		xff        string
		wantClient string
		wantResult clientaddr.Result
	}{
		{name: "trusted proxy", remote: "10.1.2.3:5555", xff: "198.51.100.9", wantClient: "198.51.100.9", wantResult: clientaddr.ResultAccepted},
		{name: "untrusted peer", remote: "203.0.113.7:5555", xff: "198.51.100.9", wantClient: "203.0.113.7", wantResult: clientaddr.ResultUntrustedPeer},
		{name: "no header", remote: "10.1.2.3:5555", wantClient: "10.1.2.3", wantResult: clientaddr.ResultAccepted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got clientaddr.Identity
			var remoteAddr string
			h := ClientAddress(policy, nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got, _ = clientaddr.FromContext(r.Context())
				remoteAddr = r.RemoteAddr
			}))

			req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			req.RemoteAddr = tt.remote
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)

			if got.Client.String() != tt.wantClient {
				t.Errorf("client = %s, want %s", got.Client, tt.wantClient)
			}
			if got.Result != tt.wantResult {
				t.Errorf("result = %s, want %s", got.Result, tt.wantResult)
			}
			if remoteAddr != tt.remote {
				t.Errorf("RemoteAddr = %q, want it untouched (%q)", remoteAddr, tt.remote)
			}
		})
	}
}

func TestClientAddressNilPolicyStillInstallsPeerIdentity(t *testing.T) {
	var got clientaddr.Identity
	var ok bool
	h := ClientAddress(nil, nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, ok = clientaddr.FromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !ok {
		t.Fatal("no identity installed for a nil policy")
	}
	if got.Client.String() != "203.0.113.7" || got.Peer.String() != "203.0.113.7" {
		t.Fatalf("identity = %+v, want the peer for both", got)
	}
}

func TestClientAddressLogsAreBoundedAndRateLimited(t *testing.T) {
	policy, err := clientaddr.NewPolicy([]string{"10.0.0.0/8"}, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	h := ClientAddress(policy, log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	const requests = 20
	for range requests {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		req.RemoteAddr = "10.1.2.3:5555"
		req.Header.Set("X-Forwarded-For", "198.51.100.9:1234")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	out := buf.String()
	if n := strings.Count(out, "forwarding header not used"); n != 1 {
		t.Fatalf("%d diagnostics for %d malformed requests, want exactly 1", n, requests)
	}
	if !strings.Contains(out, "result=malformed") || !strings.Contains(out, "peer=10.1.2.3") {
		t.Fatalf("diagnostic is missing its bounded fields: %s", out)
	}
	if strings.Contains(out, "198.51.100.9") {
		t.Fatalf("the untrusted header value leaked into the log: %s", out)
	}
}

func TestClientAddressAcceptedRequestsAreNotLogged(t *testing.T) {
	policy, err := clientaddr.NewPolicy([]string{"10.0.0.0/8"}, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	var buf bytes.Buffer
	h := ClientAddress(policy, slog.New(slog.NewTextHandler(&buf, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = "10.1.2.3:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if buf.Len() != 0 {
		t.Fatalf("an accepted derivation logged: %s", buf.String())
	}
}

func TestClientAddressIsRaceFree(t *testing.T) {
	policy, err := clientaddr.NewPolicy([]string{"10.0.0.0/8"}, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	h := ClientAddress(policy, slog.New(slog.NewTextHandler(&syncWriter{}, nil)))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if _, ok := clientaddr.FromContext(r.Context()); !ok {
			t.Error("identity missing under concurrency")
		}
	}))

	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			req.RemoteAddr = "10.1.2.3:5555"
			if i%2 == 0 {
				req.Header.Set("X-Forwarded-For", "198.51.100.9")
			} else {
				req.Header.Set("X-Forwarded-For", "garbage")
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
}

// syncWriter serialises writes so the race detector reports races in the
// middleware rather than in the test's log sink.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
