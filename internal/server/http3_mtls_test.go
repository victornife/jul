// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func TestHTTP3RequiresConfiguredClientCertificate(t *testing.T) {
	dir := t.TempDir()
	serverCertPath, serverKeyPath := writeSelfSigned(t, dir, "h3-mtls-server", "localhost")
	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatalf("load server certificate: %v", err)
	}

	clientCA := newCA(t)
	_, validClient := clientCA.clientCert(t, "valid-client", 10, []string{"client.example"}, nil)
	otherCA := newCA(t)
	_, untrustedClient := otherCA.clientCert(t, "untrusted-client", 11, []string{"client.example"}, nil)

	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(clientCA.cert)
	serverTLS := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return &serverCert, nil
		},
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  clientRoots,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "verified client certificate missing", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	h3, err := newStagedHTTP3WithTLS("127.0.0.1:0", serverTLS, handler, nil, logger)
	if err != nil {
		t.Fatalf("newStagedHTTP3WithTLS: %v", err)
	}
	if err := h3.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	defer func() { _ = h3.Close(context.Background()) }()
	addr := h3.(*h3Conn).ln.Addr().String()

	serverRoots := x509.NewCertPool()
	serverPEM, err := os.ReadFile(serverCertPath)
	if err != nil {
		t.Fatalf("read server certificate: %v", err)
	}
	if !serverRoots.AppendCertsFromPEM(serverPEM) {
		t.Fatal("append server certificate")
	}

	tests := []struct {
		name         string
		certificates []tls.Certificate
		wantErr      bool
	}{
		{name: "missing certificate", wantErr: true},
		{name: "valid certificate", certificates: []tls.Certificate{validClient}},
		{name: "untrusted certificate", certificates: []tls.Certificate{untrustedClient}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &http3.Transport{TLSClientConfig: &tls.Config{
				RootCAs:      serverRoots,
				ServerName:   "localhost",
				Certificates: tt.certificates,
			}}
			defer func() { _ = tr.Close() }()
			client := &http.Client{Transport: tr, Timeout: 3 * time.Second}

			resp, err := client.Get("https://" + addr + "/")
			if tt.wantErr {
				if err == nil {
					_ = resp.Body.Close()
					t.Fatal("expected QUIC TLS client-auth failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("GET with valid client certificate: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
			}
			if resp.ProtoMajor != 3 {
				t.Fatalf("protocol = %q, want HTTP/3", resp.Proto)
			}
		})
	}
}

func TestHTTP3RequestClientCertificateAllowsAnonymousButRejectsUntrusted(t *testing.T) {
	dir := t.TempDir()
	serverCertPath, serverKeyPath := writeSelfSigned(t, dir, "h3-mtls-request", "localhost")
	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatalf("load server certificate: %v", err)
	}
	clientCA := newCA(t)
	otherCA := newCA(t)
	_, untrustedClient := otherCA.clientCert(t, "untrusted-client", 20, nil, nil)

	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(clientCA.cert)
	h3, err := newStagedHTTP3WithTLS("127.0.0.1:0", &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return &serverCert, nil
		},
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  clientRoots,
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newStagedHTTP3WithTLS: %v", err)
	}
	if err := h3.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	defer func() { _ = h3.Close(context.Background()) }()
	addr := h3.(*h3Conn).ln.Addr().String()

	serverRoots := x509.NewCertPool()
	serverPEM, _ := os.ReadFile(serverCertPath)
	serverRoots.AppendCertsFromPEM(serverPEM)

	anonymous := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: serverRoots, ServerName: "localhost"}}
	client := &http.Client{Transport: anonymous, Timeout: 3 * time.Second}
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("anonymous request mode GET: %v", err)
	}
	_ = resp.Body.Close()
	_ = anonymous.Close()

	untrusted := &http3.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      serverRoots,
		ServerName:   "localhost",
		Certificates: []tls.Certificate{untrustedClient},
	}}
	defer func() { _ = untrusted.Close() }()
	_, err = (&http.Client{Transport: untrusted, Timeout: 3 * time.Second}).Get("https://" + addr + "/")
	if err == nil {
		t.Fatal("expected an untrusted presented certificate to fail in request mode")
	}
}
