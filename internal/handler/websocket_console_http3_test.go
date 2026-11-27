// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package handler

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"github.com/quic-go/quic-go/http3"
)

// The isolated Playwright project owns this fixture through stdin and the
// readiness file. Normal Go test runs never start a long-lived backend.
func TestWebSocketConsoleHTTP3Fixture(t *testing.T) {
	readyPath := os.Getenv("JUL_WEBSOCKET_E2E_READY")
	if readyPath == "" {
		t.Skip("started by the websocket-real-server Playwright project")
	}
	pki := newBackendPKI(t)
	serverCert := pki.issue(t, "inventory", []string{"inventory.internal"}, nil)
	clientCert := pki.issue(t, "jul", []string{"jul.internal"}, nil)
	certPath, keyPath := writePair(t, t.TempDir(), clientCert)
	clients := x509.NewCertPool()
	clients.AddCert(pki.cert)
	var accepted atomic.Int64
	server := &http3.Server{
		TLSConfig: http3.ConfigureTLSConfig(&tls.Config{
			Certificates: []tls.Certificate{serverCert},
			ClientAuth:   tls.RequireAndVerifyClientCert, ClientCAs: clients,
		}),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor != 3 || r.Method != http.MethodConnect || r.Proto != "websocket" ||
				r.TLS == nil || r.TLS.Version != tls.VersionTLS13 || r.TLS.NegotiatedProtocol != http3.NextProtoH3 ||
				len(r.TLS.PeerCertificates) != 1 || r.Header.Get("Sec-WebSocket-Protocol") != "chat" ||
				r.Header.Get("Upgrade") != "" || r.Header.Get("Connection") != "" || r.Header.Get("Sec-WebSocket-Key") != "" {
				t.Error("Console tunnel did not use a valid native H3/mTLS handshake")
				http.Error(w, "invalid native H3 handshake", http.StatusBadRequest)
				return
			}
			accepted.Add(1)
			w.Header().Set("Sec-WebSocket-Protocol", "chat")
			echoHTTP3WebSocket(w, r)
		}),
	}
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(packet) }()
	defer func() {
		_ = server.Close()
		_ = packet.Close()
		<-done
	}()
	ready, err := json.Marshal(map[string]string{
		"address": packet.LocalAddr().String(), "ca": pki.caPath,
		"client_cert": certPath, "client_key": keyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readyPath, ready, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	if accepted.Load() == 0 {
		t.Error("Console journey never established an H3 backend tunnel")
	}
}
