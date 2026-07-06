// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

// caFixture is a throwaway certificate authority used to mint client
// certificates and certificate revocation lists in tests.
type caFixture struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

// newCA creates a self-signed CA suitable for signing client certificates.
func newCA(t testing.TB) *caFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
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
	return &caFixture{
		cert: cert,
		key:  key,
		pem:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// writePEM writes data to a file under dir and returns its path.
func writePEM(t testing.TB, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// clientCert mints a client certificate signed by the CA, with the given
// serial and subject alternative names, and returns the parsed certificate, its
// key, and a tls.Certificate ready for a client handshake.
func (ca *caFixture) clientCert(t testing.TB, cn string, serial int64, dns []string, uris []*url.URL) (*x509.Certificate, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     dns,
		URIs:         uris,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	tlsCert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        cert,
	}
	return cert, tlsCert
}

// writeCRL creates a CRL signed by the CA revoking the given serials and writes
// it to a PEM file under dir, returning its path.
func (ca *caFixture) writeCRL(t testing.TB, dir, name string, serials ...int64) string {
	t.Helper()
	var revoked []x509.RevocationListEntry
	for _, s := range serials {
		revoked = append(revoked, x509.RevocationListEntry{
			SerialNumber:   big.NewInt(s),
			RevocationTime: time.Now(),
		})
	}
	tmpl := &x509.RevocationList{
		Number:                    big.NewInt(1),
		ThisUpdate:                time.Now().Add(-time.Hour),
		NextUpdate:                time.Now().Add(time.Hour),
		RevokedCertificateEntries: revoked,
	}
	der, err := x509.CreateRevocationList(rand.Reader, tmpl, ca.cert, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return writePEM(t, dir, name, pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}))
}

func TestClientAuthMode(t *testing.T) {
	cases := map[string]tls.ClientAuthType{
		"request":   tls.VerifyClientCertIfGiven,
		"require":   tls.RequireAndVerifyClientCert,
		"none":      tls.NoClientCert,
		"":          tls.NoClientCert,
		"bogus":     tls.NoClientCert,
		" require ": tls.RequireAndVerifyClientCert,
	}
	for in, want := range cases {
		if got := clientAuthMode(in); got != want {
			t.Errorf("clientAuthMode(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestClientAuthForAddrDisabled(t *testing.T) {
	servers := []config.ServerConfig{
		{Listen: ":8443", TLS: &config.TLSConfig{Enabled: true}},
	}
	bundle, err := clientAuthForAddr(servers, ":8443", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bundle != nil {
		t.Fatalf("expected nil bundle when client_auth is off, got %+v", bundle)
	}
}

func TestClientAuthForAddrModeAndPool(t *testing.T) {
	dir := t.TempDir()
	ca := newCA(t)
	caFile := writePEM(t, dir, "ca.pem", ca.pem)

	servers := []config.ServerConfig{
		{Listen: ":8443", TLS: &config.TLSConfig{
			Enabled:    true,
			ClientAuth: &config.ClientAuthConfig{Mode: "request", CAFile: caFile},
		}},
		{Listen: ":8443", TLS: &config.TLSConfig{
			Enabled:    true,
			ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: caFile},
		}},
		// Different address; must not influence the :8443 bundle.
		{Listen: ":9443", TLS: &config.TLSConfig{
			Enabled:    true,
			ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: caFile},
		}},
	}
	bundle, err := clientAuthForAddr(servers, ":8443", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bundle == nil {
		t.Fatal("expected a bundle")
	}
	if bundle.mode != tls.RequireAndVerifyClientCert {
		t.Errorf("mode = %v, want strongest (RequireAndVerifyClientCert)", bundle.mode)
	}
	if bundle.pool == nil {
		t.Error("expected a non-nil CA pool")
	}
}

func TestClientAuthForAddrBadCAFile(t *testing.T) {
	servers := []config.ServerConfig{
		{Listen: ":8443", TLS: &config.TLSConfig{
			Enabled:    true,
			ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: "/no/such/ca.pem"},
		}},
	}
	if _, err := clientAuthForAddr(servers, ":8443", nil); err == nil {
		t.Fatal("expected an error for an unreadable ca_file")
	}
}

func TestLoadCABundleMultiCert(t *testing.T) {
	dir := t.TempDir()
	ca1 := newCA(t)
	ca2 := newCA(t)
	bundle := append(append([]byte{}, ca1.pem...), ca2.pem...)
	path := writePEM(t, dir, "bundle.pem", bundle)

	certs, err := loadCABundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 2 {
		t.Fatalf("expected 2 certs, got %d", len(certs))
	}
}

func TestLoadCABundleEmpty(t *testing.T) {
	dir := t.TempDir()
	path := writePEM(t, dir, "empty.pem", []byte("not a certificate"))
	if _, err := loadCABundle(path); err == nil {
		t.Fatal("expected an error for a file with no CERTIFICATE block")
	}
}

func TestLoadCRLVerifiesSignature(t *testing.T) {
	dir := t.TempDir()
	ca := newCA(t)
	crlFile := ca.writeCRL(t, dir, "revoked.crl", 0x2a, 0x2b)

	revoked, err := loadCRL(crlFile, []*x509.Certificate{ca.cert})
	if err != nil {
		t.Fatal(err)
	}
	if !revoked["2a"] || !revoked["2b"] {
		t.Errorf("expected serials 2a and 2b revoked, got %v", revoked)
	}
	if revoked["2c"] {
		t.Error("did not expect serial 2c to be revoked")
	}
}

func TestLoadCRLWrongCA(t *testing.T) {
	dir := t.TempDir()
	signer := newCA(t)
	other := newCA(t)
	crlFile := signer.writeCRL(t, dir, "revoked.crl", 1)

	if _, err := loadCRL(crlFile, []*x509.Certificate{other.cert}); err == nil {
		t.Fatal("expected an error when no configured CA signed the CRL")
	}
}

func TestSANAllowed(t *testing.T) {
	u, _ := url.Parse("spiffe://example.org/svc")
	cert := &x509.Certificate{
		DNSNames: []string{"svc.example.com"},
		URIs:     []*url.URL{u},
	}
	if !sanAllowed(cert, []string{"svc.example.com"}) {
		t.Error("DNS SAN should be allowed")
	}
	if !sanAllowed(cert, []string{"spiffe://example.org/svc"}) {
		t.Error("URI SAN should be allowed")
	}
	if sanAllowed(cert, []string{"other.example.com"}) {
		t.Error("non-matching SAN should not be allowed")
	}
}

func TestMakeClientCertVerifierNoConstraints(t *testing.T) {
	if makeClientCertVerifier(nil, nil, nil) != nil {
		t.Error("verifier should be nil when there is nothing to enforce and no hook")
	}
}

func TestMakeClientCertVerifierSANAndCRL(t *testing.T) {
	ca := newCA(t)
	good, _ := ca.clientCert(t, "good", 0x10, []string{"good.example.com"}, nil)
	revokedCert, _ := ca.clientCert(t, "bad", 0x11, []string{"good.example.com"}, nil)

	var results []string
	record := func(r string) { results = append(results, r) }
	revoked := map[string]bool{"11": true}
	verify := makeClientCertVerifier([]string{"good.example.com"}, revoked, record)

	// Verified: allowed SAN, not revoked.
	if err := verify(nil, [][]*x509.Certificate{{good}}); err != nil {
		t.Errorf("good cert should verify: %v", err)
	}
	// Rejected: revoked serial.
	if err := verify(nil, [][]*x509.Certificate{{revokedCert}}); err == nil {
		t.Error("revoked cert should be rejected")
	}
	// Rejected: SAN not allowed.
	wrongSAN, _ := ca.clientCert(t, "x", 0x12, []string{"nope.example.com"}, nil)
	if err := verify(nil, [][]*x509.Certificate{{wrongSAN}}); err == nil {
		t.Error("disallowed SAN should be rejected")
	}
	want := []string{"verified", "rejected", "rejected"}
	if strings.Join(results, ",") != strings.Join(want, ",") {
		t.Errorf("results = %v, want %v", results, want)
	}
}

func TestMakeClientCertVerifierNoCertAccepted(t *testing.T) {
	var called bool
	verify := makeClientCertVerifier(nil, nil, func(string) { called = true })
	// request mode with no certificate: empty rawCerts and chains.
	if err := verify(nil, nil); err != nil {
		t.Errorf("absent certificate should be accepted by the verifier: %v", err)
	}
	if called {
		t.Error("result hook should not fire when no certificate is presented")
	}
}

// TestMTLSHandshakeEndToEnd exercises a real TLS handshake through a listener
// configured by clientAuthForAddr, covering the valid, revoked, and wrong-CA
// paths and the verified/rejected result hook.
func TestMTLSHandshakeEndToEnd(t *testing.T) {
	dir := t.TempDir()
	ca := newCA(t)
	caFile := writePEM(t, dir, "ca.pem", ca.pem)
	crlFile := ca.writeCRL(t, dir, "revoked.crl", 0x21)

	servers := []config.ServerConfig{
		{Listen: ":0", TLS: &config.TLSConfig{
			Enabled: true,
			ClientAuth: &config.ClientAuthConfig{
				Mode:      "require",
				CAFile:    caFile,
				VerifySAN: []string{"client.example.com"},
				CRLFile:   crlFile,
			},
		}},
	}

	var results []string
	bundle, err := clientAuthForAddr(servers, ":0", func(r string) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if bundle == nil {
		t.Fatal("expected a bundle")
	}

	// Server certificate (self-signed) for the listener.
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	srvDER, _ := x509.CreateCertificate(rand.Reader, srvTmpl, srvTmpl, &srvKey.PublicKey, srvKey)
	srvCert := tls.Certificate{Certificate: [][]byte{srvDER}, PrivateKey: srvKey}

	tlsConf := &tls.Config{
		Certificates:          []tls.Certificate{srvCert},
		ClientAuth:            bundle.mode,
		ClientCAs:             bundle.pool,
		VerifyPeerCertificate: bundle.verify,
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsConf)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
				io.Copy(io.Discard, c)
			}(conn)
		}
	}()

	addr := ln.Addr().String()
	rootPool := x509.NewCertPool()
	srvParsed, _ := x509.ParseCertificate(srvDER)
	rootPool.AddCert(srvParsed)

	dial := func(client tls.Certificate) error {
		conn, err := tls.Dial("tcp", addr, &tls.Config{
			RootCAs:      rootPool,
			Certificates: []tls.Certificate{client},
			ServerName:   "localhost",
		})
		if err != nil {
			return err
		}
		defer conn.Close()
		if err := conn.Handshake(); err != nil {
			return err
		}
		// Under TLS 1.3 the client finishes the handshake in one round trip and
		// a server-side client-auth rejection surfaces as an alert on the first
		// read, not during Dial. Force a read so rejected handshakes report an
		// error here.
		conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buf := make([]byte, 1)
		if _, err := conn.Read(buf); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return nil // accepted: server kept the connection open
			}
			return err
		}
		return nil
	}

	// Valid client: signed by CA, allowed SAN, not revoked.
	_, validCert := ca.clientCert(t, "ok", 0x20, []string{"client.example.com"}, nil)
	if err := dial(validCert); err != nil {
		t.Errorf("valid client should connect: %v", err)
	}

	// Revoked client: signed by CA, allowed SAN, but serial 0x21 is revoked.
	_, revokedCert := ca.clientCert(t, "revoked", 0x21, []string{"client.example.com"}, nil)
	if err := dial(revokedCert); err == nil {
		t.Error("revoked client should be rejected")
	}

	// Wrong CA: a client signed by a different CA fails CA-chain verification
	// before the custom verifier runs.
	otherCA := newCA(t)
	_, wrongCert := otherCA.clientCert(t, "intruder", 0x22, []string{"client.example.com"}, nil)
	if err := dial(wrongCert); err == nil {
		t.Error("client signed by an unknown CA should be rejected")
	}

	if len(results) == 0 || results[0] != "verified" {
		t.Errorf("expected first result 'verified', got %v", results)
	}
	var sawRejected bool
	for _, r := range results {
		if r == "rejected" {
			sawRejected = true
		}
	}
	if !sawRejected {
		t.Errorf("expected a 'rejected' result for the revoked client, got %v", results)
	}
}
