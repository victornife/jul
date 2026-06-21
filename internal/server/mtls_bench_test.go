package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"jul/internal/config"
)

// benchTLSServer starts a TLS listener and a handshake-and-discard accept loop.
// When ca is non-nil the listener enforces mutual TLS in "require" mode,
// verifying client certificates against ca (full chain + SAN/CRL pipeline), so
// the benchmark measures the added cost of client-certificate verification over
// a plain server-authenticated handshake. It returns the dial address, the root
// pool a client needs to trust the server certificate, and a stop function.
func benchTLSServer(b *testing.B, ca *caFixture) (addr string, rootPool *x509.CertPool, stop func()) {
	b.Helper()

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
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
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, srvTmpl, &srvKey.PublicKey, srvKey)
	if err != nil {
		b.Fatal(err)
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{srvDER}, PrivateKey: srvKey}},
		MinVersion:   tls.VersionTLS13,
	}

	if ca != nil {
		dir := b.TempDir()
		caFile := writePEM(b, dir, "ca.pem", ca.pem)
		servers := []config.ServerConfig{{Listen: ":0", TLS: &config.TLSConfig{
			Enabled: true,
			ClientAuth: &config.ClientAuthConfig{
				Mode:      "require",
				CAFile:    caFile,
				VerifySAN: []string{"client.example.com"},
			},
		}}}
		bundle, err := clientAuthForAddr(servers, ":0", nil)
		if err != nil {
			b.Fatal(err)
		}
		tlsConf.ClientAuth = bundle.mode
		tlsConf.ClientCAs = bundle.pool
		tlsConf.VerifyPeerCertificate = bundle.verify
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsConf)
	if err != nil {
		b.Fatal(err)
	}
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

	rootPool = x509.NewCertPool()
	srvParsed, err := x509.ParseCertificate(srvDER)
	if err != nil {
		b.Fatal(err)
	}
	rootPool.AddCert(srvParsed)
	return ln.Addr().String(), rootPool, func() { ln.Close() }
}

// BenchmarkTLSHandshakeServerAuth measures a baseline TLS 1.3 handshake with
// server authentication only (no client certificate).
func BenchmarkTLSHandshakeServerAuth(b *testing.B) {
	addr, rootPool, stop := benchTLSServer(b, nil)
	defer stop()
	clientConf := &tls.Config{RootCAs: rootPool, ServerName: "localhost", MinVersion: tls.VersionTLS13}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn, err := tls.Dial("tcp", addr, clientConf)
		if err != nil {
			b.Fatal(err)
		}
		if err := conn.Handshake(); err != nil {
			b.Fatal(err)
		}
		conn.Close()
	}
}

// BenchmarkMTLSHandshake measures a full mutual-TLS handshake: the client
// presents a certificate and the server verifies the chain against the CA and
// runs the SAN allow-list check. The delta against
// BenchmarkTLSHandshakeServerAuth is the cost of client-certificate auth.
func BenchmarkMTLSHandshake(b *testing.B) {
	ca := newCA(b)
	_, clientCert := ca.clientCert(b, "svc", 0x10, []string{"client.example.com"}, nil)
	addr, rootPool, stop := benchTLSServer(b, ca)
	defer stop()
	clientConf := &tls.Config{
		RootCAs:      rootPool,
		Certificates: []tls.Certificate{clientCert},
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS13,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn, err := tls.Dial("tcp", addr, clientConf)
		if err != nil {
			b.Fatal(err)
		}
		if err := conn.Handshake(); err != nil {
			b.Fatal(err)
		}
		conn.Close()
	}
}
