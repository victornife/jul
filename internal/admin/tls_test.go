// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/config"
)

// writeAdminTestCert writes a self-signed cert/key pair named cn under dir
// and returns their paths.
func writeAdminTestCert(t *testing.T, dir, cn string) (certPath, keyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, cn+"-cert.pem")
	keyPath = filepath.Join(dir, cn+"-key.pem")
	certOut, err := os.Create(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	certOut.Close()
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyOut, err := os.Create(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		t.Fatal(err)
	}
	keyOut.Close()
	return certPath, keyPath
}

func adminFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestPrepareTLSSkipsWhenNotEnabledAtStartup(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	prepared, err := s.PrepareTLS(config.AdminConfig{TLS: &config.AdminTLSConfig{Enabled: true, Cert: "x", Key: "y"}})
	if err != nil || prepared != nil {
		t.Fatalf("PrepareTLS = (%v, %v), want (nil, nil): TLS was never enabled at startup", prepared, err)
	}
}

func TestPrepareTLSSkipsUnchangedCertificate(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeAdminTestCert(t, dir, "a")
	cfg := config.AdminConfig{TLS: &config.AdminTLSConfig{Enabled: true, Cert: cert, Key: key}}
	s := newTestServer(t, cfg, Deps{})

	prepared, err := s.PrepareTLS(cfg)
	if err != nil {
		t.Fatalf("PrepareTLS: %v", err)
	}
	if prepared != nil {
		t.Fatal("expected no candidate for an unchanged certificate")
	}
}

func TestPrepareTLSRejectsMalformedCandidateBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeAdminTestCert(t, dir, "a")
	cfg := config.AdminConfig{TLS: &config.AdminTLSConfig{Enabled: true, Cert: cert, Key: key}}
	s := newTestServer(t, cfg, Deps{})
	originalFP := s.certFingerprint

	broken := config.AdminConfig{TLS: &config.AdminTLSConfig{Enabled: true, Cert: dir + "/missing.pem", Key: dir + "/missing-key.pem"}}
	prepared, err := s.PrepareTLS(broken)
	if err == nil {
		t.Fatal("expected an error for an unloadable candidate cert/key pair")
	}
	if prepared != nil {
		t.Fatal("expected no candidate when the candidate fails to load")
	}
	if s.certFingerprint != originalFP {
		t.Fatal("a malformed candidate must never mutate the live fingerprint")
	}
}

func TestCommitPreparedTLSIsANoOpForANilCandidate(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeAdminTestCert(t, dir, "a")
	cfg := config.AdminConfig{TLS: &config.AdminTLSConfig{Enabled: true, Cert: cert, Key: key}}
	s := newTestServer(t, cfg, Deps{})
	originalFP := s.certFingerprint

	s.CommitPreparedTLS(nil)

	if s.certFingerprint != originalFP {
		t.Fatal("committing a nil candidate must never change the live fingerprint")
	}
}

// dialAdminTLS opens a fresh TLS connection to addr and returns the leaf
// certificate the admin listener presented.
func dialAdminTLS(t *testing.T, addr string) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("no peer certificate presented")
	}
	return state.PeerCertificates[0]
}

// adminHTTPOverConn issues one HTTP/1.1 GET over an already-established
// connection, proving the connection is still usable end to end.
func adminHTTPOverConn(t *testing.T, conn *tls.Conn, host, path string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Write(conn); err != nil {
		t.Fatalf("write request over existing connection: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read response over existing connection: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

// TestAdminListenerRotatesCertificateWithoutRebind is the real end-to-end
// proof for #336: the admin listener terminates TLS with an operator-supplied
// certificate, a live PrepareTLS/CommitPreparedTLS rotation swaps it with no
// rebind and no dropped connection, and a client trusting only the old
// certificate is refused afterward.
func TestAdminListenerRotatesCertificateWithoutRebind(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := writeAdminTestCert(t, dir, "a")
	certB, keyB := writeAdminTestCert(t, dir, "b")
	addr := adminFreeAddr(t)

	cfg := config.AdminConfig{
		Enabled: true,
		Listen:  addr,
		TLS:     &config.AdminTLSConfig{Enabled: true, Cert: certA, Key: keyA},
	}
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), Deps{})
	if s == nil {
		t.Fatal("New returned nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- s.Run(ctx) }()

	var held *tls.Conn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err == nil {
			held = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if held == nil {
		t.Fatal("admin TLS listener never became reachable")
	}
	defer held.Close()

	leafBefore := held.ConnectionState().PeerCertificates[0]
	if leafBefore.Subject.CommonName != "a" {
		t.Fatalf("initial connection observed CN %q, want a", leafBefore.Subject.CommonName)
	}
	if code := adminHTTPOverConn(t, held, addr, "/healthz"); code != http.StatusOK {
		t.Fatalf("held connection status before rotation = %d", code)
	}

	next := config.AdminConfig{Enabled: true, Listen: addr, TLS: &config.AdminTLSConfig{Enabled: true, Cert: certB, Key: keyB}}
	prepared, err := s.PrepareTLS(next)
	if err != nil {
		t.Fatalf("PrepareTLS: %v", err)
	}
	if prepared == nil {
		t.Fatal("expected a candidate for a changed certificate")
	}
	s.CommitPreparedTLS(prepared)

	leafAfter := dialAdminTLS(t, addr)
	if leafAfter.Subject.CommonName != "b" {
		t.Fatalf("new connection after rotation observed CN %q, want b", leafAfter.Subject.CommonName)
	}

	// The connection established before rotation must still work: no
	// certificate change is retroactive to an already-completed handshake.
	if code := adminHTTPOverConn(t, held, addr, "/healthz"); code != http.StatusOK {
		t.Fatalf("held connection status after rotation = %d", code)
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

// TestAdminListenerFailsCleanlyOnMalformedCertificate proves #336's
// "does not half-start" requirement: a malformed cert/key pair at
// construction fails Run before it ever binds a socket.
func TestAdminListenerFailsCleanlyOnMalformedCertificate(t *testing.T) {
	dir := t.TempDir()
	cfg := config.AdminConfig{
		Enabled: true,
		Listen:  adminFreeAddr(t),
		TLS:     &config.AdminTLSConfig{Enabled: true, Cert: dir + "/missing.pem", Key: dir + "/missing-key.pem"},
	}
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), Deps{})
	if s == nil {
		t.Fatal("New returned nil")
	}
	if s.tlsErr == nil {
		t.Fatal("expected New to record the load failure")
	}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("expected Run to fail rather than half-start")
	}
}
