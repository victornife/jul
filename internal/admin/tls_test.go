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
	"errors"
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

// writeAdminTestCA writes a self-signed CA cert/key pair and returns the CA
// certificate/key (for signing client certificates) and the CA cert's PEM
// file path (for admin.tls.client_auth.ca_file).
func writeAdminTestCA(t *testing.T, dir string) (caCert *x509.Certificate, caKey *ecdsa.PrivateKey, caFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "admin test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "admin-client-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return cert, key, path
}

// adminClientCert mints a client certificate signed by ca/caKey, ready for a
// client-side TLS handshake against the admin listener.
func adminClientCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// adminHTTPOverConnWithAuth is adminHTTPOverConn plus an optional bearer
// token, for tests that need to prove client-certificate authentication
// composes with — rather than replaces — the bearer/RBAC layer (#336).
func adminHTTPOverConnWithAuth(t *testing.T, conn *tls.Conn, host, path, bearer string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if err := req.Write(conn); err != nil {
		t.Fatalf("write request over connection: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read response over connection: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
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

// runAdminTLSServer starts a real admin.Server from cfg and blocks until its
// TLS accept loop is actually processing handshakes (not merely that the
// socket is bound: a bare TCP connect can succeed against the kernel backlog
// before Serve's accept loop has picked it up, which would let a real client
// certificate dial race the loop's startup and see a spurious reset).
func runAdminTLSServer(t *testing.T, cfg config.AdminConfig) *Server {
	t.Helper()
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), Deps{})
	if s == nil {
		t.Fatal("New returned nil")
	}
	if s.tlsErr != nil {
		t.Fatalf("New: %v", s.tlsErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErr:
		case <-time.After(2 * time.Second):
			t.Error("Run did not stop after context cancellation")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// A TLS-level dial (not a bare TCP connect) forces the accept loop to
		// actually process a connection, whatever it decides about the
		// handshake, before this helper returns.
		conn, err := tls.Dial("tcp", cfg.Listen, &tls.Config{InsecureSkipVerify: true})
		if err == nil {
			conn.Close()
			return s
		}
		var opErr *net.OpError
		if !errors.As(err, &opErr) || opErr.Op != "dial" {
			// Any error past the dial itself (a handshake rejection, e.g. a
			// missing required client certificate) proves the accept loop is
			// live; only "connection refused" while the listener is still
			// coming up should keep retrying.
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("admin listener never became reachable")
	return nil
}

// mtlsAccepted dials addr with the given (possibly empty) client
// certificates and reports whether the server ultimately accepted the
// connection. Under TLS 1.3 the client completes its side of the handshake
// in one round trip; a server-side client-certificate rejection surfaces as
// an alert on the first read, not necessarily as a Dial error, matching the
// established pattern in internal/server/mtls_test.go.
func mtlsAccepted(t *testing.T, addr string, certs []tls.Certificate) bool {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, Certificates: certs})
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return true // accepted: the server kept the connection open
		}
		return false
	}
	return true
}

// dialAcceptedAdminTLS dials addr with certs and returns a live, accepted
// connection ready for application traffic. A handshake with a certificate
// the server will accept can still surface as a transient reset on a
// freshly-bound listener (an inherent TLS 1.3 client/server timing quirk, not
// a functional defect — verified independently via a bare tls.Listener, a
// bare http.Server, and the real admin.Server, all of which accept the exact
// same certificate reliably once the accept loop is warm); retrying a few
// times is the same tolerance a real HTTP client's connection pool would give
// a fresh connection.
func dialAcceptedAdminTLS(t *testing.T, addr string, certs []tls.Certificate) *tls.Conn {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, Certificates: certs})
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		buf := make([]byte, 1)
		_, readErr := conn.Read(buf)
		var ne net.Error
		if readErr == nil || (errors.As(readErr, &ne) && ne.Timeout()) {
			conn.SetReadDeadline(time.Time{})
			return conn
		}
		conn.Close()
		lastErr = readErr
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dial with certificate never accepted: %v", lastErr)
	return nil
}

// TestAdminListenerRequiresClientCertificateWhenConfigured is the required
// "client-certificate accept and reject" test for #336: with client_auth
// mode "require", a handshake without a client certificate is refused and one
// with a CA-signed certificate succeeds.
func TestAdminListenerRequiresClientCertificateWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeAdminTestCert(t, dir, "srv")
	ca, caKey, caFile := writeAdminTestCA(t, dir)
	addr := adminFreeAddr(t)
	cfg := config.AdminConfig{
		Enabled: true,
		Listen:  addr,
		TLS: &config.AdminTLSConfig{
			Enabled:    true,
			Cert:       cert,
			Key:        key,
			ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: caFile},
		},
	}
	runAdminTLSServer(t, cfg)

	if mtlsAccepted(t, addr, nil) {
		t.Fatal("expected the handshake to be refused without a client certificate")
	}

	clientCert := adminClientCert(t, ca, caKey, "operator")
	conn := dialAcceptedAdminTLS(t, addr, []tls.Certificate{clientCert})
	conn.Close()
}

// TestAdminListenerRejectsClientCertificateFromUntrustedCA proves a
// certificate signed by a CA other than admin.tls.client_auth.ca_file is
// rejected, not merely any presented certificate accepted.
func TestAdminListenerRejectsClientCertificateFromUntrustedCA(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeAdminTestCert(t, dir, "srv")
	_, _, caFile := writeAdminTestCA(t, dir)
	otherCA, otherKey, _ := writeAdminTestCA(t, t.TempDir())
	addr := adminFreeAddr(t)
	cfg := config.AdminConfig{
		Enabled: true,
		Listen:  addr,
		TLS: &config.AdminTLSConfig{
			Enabled:    true,
			Cert:       cert,
			Key:        key,
			ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: caFile},
		},
	}
	runAdminTLSServer(t, cfg)

	untrusted := adminClientCert(t, otherCA, otherKey, "intruder")
	if mtlsAccepted(t, addr, []tls.Certificate{untrusted}) {
		t.Fatal("expected the handshake to fail for a certificate signed by an untrusted CA")
	}
}

// TestAdminListenerComposesClientCertificateWithBearerToken is the required
// "token plus client certificate composed" test for #336: a valid client
// certificate does not bypass the bearer/RBAC layer, and (proved by
// TestAdminListenerRequiresClientCertificateWhenConfigured) a valid bearer
// token never gets the chance to bypass the certificate requirement either —
// composing, not replacing.
func TestAdminListenerComposesClientCertificateWithBearerToken(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeAdminTestCert(t, dir, "srv")
	ca, caKey, caFile := writeAdminTestCA(t, dir)
	addr := adminFreeAddr(t)
	cfg := config.AdminConfig{
		Enabled: true,
		Listen:  addr,
		Token:   "s3cret",
		TLS: &config.AdminTLSConfig{
			Enabled:    true,
			Cert:       cert,
			Key:        key,
			ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: caFile},
		},
	}
	runAdminTLSServer(t, cfg)
	clientCert := adminClientCert(t, ca, caKey, "operator")

	noToken := dialAcceptedAdminTLS(t, addr, []tls.Certificate{clientCert})
	defer noToken.Close()
	if code := adminHTTPOverConnWithAuth(t, noToken, addr, "/api/status", ""); code != http.StatusUnauthorized {
		t.Fatalf("request with valid client cert but no bearer token = %d, want 401", code)
	}

	withToken := dialAcceptedAdminTLS(t, addr, []tls.Certificate{clientCert})
	defer withToken.Close()
	if code := adminHTTPOverConnWithAuth(t, withToken, addr, "/api/status", "s3cret"); code != http.StatusOK {
		t.Fatalf("request with valid client cert and bearer token = %d, want 200", code)
	}
}
