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
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"jul/internal/config"
)

// TestHTTP3FollowsSwappedClientAuth (#486): the QUIC listener reads the same
// dynamic client-auth holder as TCP TLS, so a swapped CRL or a disabled policy
// applies to new HTTP/3 handshakes, and the per-handshake config keeps TLS 1.3
// even though it is cloned from a TCP policy with a lower floor.
func TestHTTP3FollowsSwappedClientAuth(t *testing.T) {
	dir := t.TempDir()
	serverCertPath, serverKeyPath := writeSelfSigned(t, dir, "h3-dyn", "localhost")
	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	ca := newCA(t)
	caFile := writePEM(t, dir, "ca.pem", ca.pem)
	_, client := ca.clientCert(t, "c", 10, nil, nil)
	bundle := func(revoked ...int64) *clientAuthBundle {
		t.Helper()
		b, err := clientAuthForAddr([]config.ServerConfig{{Listen: "x", TLS: &config.TLSConfig{Enabled: true, ClientAuth: &config.ClientAuthConfig{
			Mode: "require", CAFile: caFile, CRLFile: ca.writeCRL(t, dir, "c.crl", revoked...),
		}}}}, "x", nil)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	dyn := &dynamicClientAuth{}
	dyn.set(bundle())
	base := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &serverCert, nil },
		MinVersion:     tls.VersionTLS12,
	}
	base.GetConfigForClient = dyn.configForClient(base)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h3, err := newStagedHTTP3WithTLS("127.0.0.1:0", base, handler, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := h3.Activate(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h3.Close(context.Background()) }()
	addr := h3.(*h3Conn).ln.Addr().String()
	roots := certificatePool(t, serverCertPath)

	get := func(certs []tls.Certificate) error {
		tr := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", Certificates: certs}}
		defer func() { _ = tr.Close() }()
		resp, err := (&http.Client{Transport: tr, Timeout: 3 * time.Second}).Get("https://" + addr + "/")
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		return nil
	}

	if err := get([]tls.Certificate{client}); err != nil {
		t.Fatalf("valid client over HTTP/3: %v", err)
	}
	if err := get(nil); err == nil {
		t.Fatal("anonymous client accepted under require")
	}
	dyn.set(bundle(10))
	if err := get([]tls.Certificate{client}); err == nil {
		t.Fatal("client revoked by the swapped CRL still accepted over HTTP/3")
	}
	dyn.set(nil)
	if err := get(nil); err != nil {
		t.Fatalf("anonymous client rejected after client auth was switched off: %v", err)
	}
}
