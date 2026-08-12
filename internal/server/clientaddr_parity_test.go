// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package server

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/clientaddr"
	"jul/internal/middleware"

	"github.com/quic-go/quic-go/http3"
)

// TestClientAddressParityAcrossProtocols proves the ADR-0016 claim that one
// middleware chain serves HTTP/1.1, HTTP/2 and HTTP/3: the same trusted-proxy
// policy must derive the same canonical client over all three, and none of them
// may mutate RemoteAddr.
func TestClientAddressParityAcrossProtocols(t *testing.T) {
	policy, err := clientaddr.NewPolicy([]string{"127.0.0.0/8", "::1/128"}, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	handler := middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := clientaddr.FromContext(r.Context())
		if !ok {
			http.Error(w, "no identity", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Test-Client", id.Client.String())
		w.Header().Set("X-Test-Source", id.Source.String())
		w.Header().Set("X-Test-Result", id.Result.String())
		w.Header().Set("X-Test-Remote-Addr", r.RemoteAddr)
		w.WriteHeader(http.StatusNoContent)
	}), middleware.RequestID(), middleware.ClientAddress(policy, slog.New(slog.NewTextHandler(io.Discard, nil))))

	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "clientaddr-parity", "localhost")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load certificate: %v", err)
	}
	roots := certificatePool(t, certPath)

	// HTTP/1.1 and HTTP/2 over one TLS test server; HTTP/3 over QUIC.
	tcp := httptest.NewUnstartedServer(handler)
	tcp.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	tcp.EnableHTTP2 = true
	tcp.StartTLS()
	defer tcp.Close()

	h3, err := newStagedHTTP3WithTLS("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
	}, handler, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newStagedHTTP3WithTLS: %v", err)
	}
	if err := h3.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	defer func() { _ = h3.Close(context.Background()) }()

	h1Transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost"}}
	h2Transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", NextProtos: []string{"h2"}}, ForceAttemptHTTP2: true}
	h3Transport := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost"}}
	defer func() { _ = h3Transport.Close() }()

	tests := []struct {
		name      string
		url       string
		transport http.RoundTripper
		wantMajor int
	}{
		{name: "http/1.1", url: tcp.URL, transport: h1Transport, wantMajor: 1},
		{name: "http/2", url: tcp.URL, transport: h2Transport, wantMajor: 2},
		{name: "http/3", url: "https://" + h3.(*h3Conn).ln.Addr().String(), transport: h3Transport, wantMajor: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tt.url+"/", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("X-Forwarded-For", "198.51.100.9, 127.0.0.5")
			client := &http.Client{Transport: tt.transport, Timeout: 5 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.ProtoMajor != tt.wantMajor {
				t.Fatalf("protocol = %q, want HTTP/%d", resp.Proto, tt.wantMajor)
			}
			if got := resp.Header.Get("X-Test-Client"); got != "198.51.100.9" {
				t.Errorf("client = %q, want 198.51.100.9", got)
			}
			if got := resp.Header.Get("X-Test-Source"); got != "xff" {
				t.Errorf("source = %q, want xff", got)
			}
			if got := resp.Header.Get("X-Test-Result"); got != "accepted" {
				t.Errorf("result = %q, want accepted", got)
			}
			if got := resp.Header.Get("X-Test-Remote-Addr"); got == "" || got == "198.51.100.9" {
				t.Errorf("RemoteAddr = %q, want the untouched transport peer", got)
			}
		})
	}
}
