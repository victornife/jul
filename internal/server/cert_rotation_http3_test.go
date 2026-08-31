// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

// h3ClientTrusting returns an http3.Transport whose root pool trusts exactly
// the certificate at certPath, so a successful request proves the server
// presented that certificate rather than merely that some cert was accepted.
func h3ClientTrusting(t *testing.T, certPath string) *http3.Transport {
	t.Helper()
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("failed to add cert to pool")
	}
	return &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "a.example.com"}}
}

// getH3 issues an HTTP/3 GET against addr using tr, retrying with a short
// per-attempt timeout over a generous overall deadline. Unlike TCP, QUIC has
// no immediate refusal for "nothing is listening yet": a client dialing
// before the accept loop has started just gets silence until its own
// handshake timeout fires, so a single long-timeout attempt can consume the
// entire retry budget by itself (observed on Windows CI, where listener
// startup is measurably slower than on Linux). Many short attempts survive
// that; a long per-attempt timeout with only a handful of retries does not.
func getH3(t *testing.T, tr *http3.Transport, addr string) (*http.Response, error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var resp *http.Response
	var err error
	for {
		client := &http.Client{Transport: tr, Timeout: 500 * time.Millisecond}
		resp, err = client.Get("https://" + addr + "/")
		if err == nil || time.Now().After(deadline) {
			return resp, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// getH3Once issues exactly one bounded HTTP/3 GET, for an assertion that
// expects a deterministic, fast failure (a TLS handshake the client's own
// certificate verification rejects) rather than a "listener not ready yet"
// condition that would need retrying.
func getH3Once(t *testing.T, tr *http3.Transport, addr string) (*http.Response, error) {
	t.Helper()
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	return client.Get("https://" + addr + "/")
}

// TestReloadRotatesHTTP3CertificateWithoutRebind (#100 acceptance criterion:
// "New HTTP/3 handshakes after Publish observe candidate certificate in full
// build") proves the same live rotation reaches HTTP/3, not merely the TCP
// TLS path: the QUIC listener shares the exact same dynamicCertProvider
// object buildListenerEntry installs on the TCP listener, so no HTTP/3-
// specific code is needed — this test is the real-network confirmation of
// that structural sharing.
func TestReloadRotatesHTTP3CertificateWithoutRebind(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := writeSelfSigned(t, dir, "h3-a", "a.example.com")
	certB, keyB := writeSelfSigned(t, dir, "h3-b", "a.example.com")
	addr := freePort(t)

	withH3 := func(cert, key string) *config.Config {
		c := tlsCfgFor(addr, cert, key, "a.example.com")
		c.Servers[0].HTTP3 = &config.HTTP3Config{Enabled: true, AltSvcMaxAge: 86400}
		return c
	}

	initial := withH3(certA, keyA)
	src := &stubSource{}
	src.set(initial, nil)
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
		return map[string]http.Handler{addr: h}, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(initial, nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan ReloadRequest, 2)
	go func() { _ = srv.Run(ctx, reload, redact.EmptyState()) }()

	trA := h3ClientTrusting(t, certA)
	defer func() { _ = trA.Close() }()
	respA, err := getH3(t, trA, addr)
	if err != nil {
		t.Fatalf("HTTP/3 request against certificate A never succeeded: %v", err)
	}
	_ = respA.Body.Close()
	if respA.ProtoMajor != 3 {
		t.Fatalf("proto = %q, want HTTP/3", respA.Proto)
	}

	next := withH3(certB, keyB)
	src.set(next, nil)
	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{ID: "rotate-h3", Source: ReloadSourceFileWatch, Result: resultCh}
	if result := <-resultCh; result.Outcome != ReloadAppliedLive {
		t.Fatalf("reload outcome = %+v", result)
	}

	// A client that only trusts A must now be refused: the live provider no
	// longer serves A on new handshakes. The listener has been up and serving
	// since before rotation, so this is a deterministic certificate-mismatch
	// rejection, not a "not ready yet" condition — one bounded attempt proves it.
	trAAfter := h3ClientTrusting(t, certA)
	defer func() { _ = trAAfter.Close() }()
	if _, err := getH3Once(t, trAAfter, addr); err == nil {
		t.Fatal("expected a client trusting only the old certificate to fail after rotation")
	}

	// A client trusting B must succeed, over HTTP/3, without any rebind.
	trB := h3ClientTrusting(t, certB)
	defer func() { _ = trB.Close() }()
	respB, err := getH3(t, trB, addr)
	if err != nil {
		t.Fatalf("HTTP/3 request against certificate B never succeeded after rotation: %v", err)
	}
	defer func() { _ = respB.Body.Close() }()
	if respB.ProtoMajor != 3 {
		t.Fatalf("proto = %q, want HTTP/3", respB.Proto)
	}
}
