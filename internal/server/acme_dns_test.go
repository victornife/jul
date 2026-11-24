// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"bufio"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"jul/internal/config"
	"jul/internal/egress"
)

func testDNSManager(t *testing.T, dir, ca string) *dnsACMEManager {
	t.Helper()
	m, err := newDNSACMEManager(&config.ACMEConfig{
		Email: "ops@example.test", CA: ca, CacheDir: dir,
		RFC2136: &config.RFC2136Config{
			Server: "127.0.0.1:53", Zone: "example.test",
			TSIGKeyName: "jul.example.test", TSIGSecret: testTSIGSecret,
		},
	}, nil, nil, nil, ACMEOptions{DNSDialContext: (&net.Dialer{}).DialContext})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

type testDNSClosableTransport struct {
	http.RoundTripper
	closed atomic.Int32
}

func (t *testDNSClosableTransport) CloseIdleConnections() { t.closed.Add(1) }

func TestLimitedDNSACMEClientBoundsRequestsAndResponses(t *testing.T) {
	entered := make(chan struct{}, dnsAuthWorkers+1)
	release := make(chan struct{})
	releaseAll := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			_, _ = io.WriteString(w, strings.Repeat("x", maxACMEResponse+1))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		entered <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	defer releaseAll()
	client := limitedDNSACMEClient(server.Client())
	results := make(chan error, dnsAuthWorkers)
	for range dnsAuthWorkers {
		go func() {
			response, err := client.Get(server.URL)
			if err == nil {
				body, readErr := io.ReadAll(response.Body)
				err = errors.Join(readErr, response.Body.Close())
				if err == nil && string(body) != "ok" {
					err = errors.New("unexpected response body")
				}
			}
			results <- err
		}()
	}
	for range dnsAuthWorkers {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("limited requests did not reach the server")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan error, 1)
	go func() {
		_, err := client.Do(req)
		cancelled <- err
	}()
	select {
	case <-entered:
		t.Fatal("more than the permitted requests reached the server")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued request did not cancel: %v", err)
	}
	releaseAll()
	for range dnsAuthWorkers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Get(server.URL + "/large"); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized ACME response was not rejected: %v", err)
	}
	transport := &testDNSClosableTransport{RoundTripper: http.DefaultTransport}
	limitedDNSACMEClient(&http.Client{Transport: transport}).CloseIdleConnections()
	if transport.closed.Load() != 1 {
		t.Fatal("idle-connection cleanup was not forwarded")
	}
}

func dnsTestHello(name string) *tls.ClientHelloInfo {
	return &tls.ClientHelloInfo{
		ServerName: name, SupportedVersions: []uint16{tls.VersionTLS13, tls.VersionTLS12},
		SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
		SupportedCurves:  []tls.CurveID{tls.CurveP256},
		SupportedPoints:  []uint8{0},
		CipherSuites:     []uint16{tls.TLS_AES_128_GCM_SHA256, tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
	}
}

func dnsTestRSAHello(name string) *tls.ClientHelloInfo {
	return &tls.ClientHelloInfo{
		ServerName: name, SupportedVersions: []uint16{tls.VersionTLS12},
		SignatureSchemes: []tls.SignatureScheme{tls.PSSWithSHA256, tls.PKCS1WithSHA256},
		SupportedCurves:  []tls.CurveID{tls.CurveP256},
		SupportedPoints:  []uint8{0},
		CipherSuites:     []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
	}
}

func dnsECDSAIssue(issue func(context.Context, []string) (*tls.Certificate, error)) func(context.Context, []string, dnsKeyAlgorithm) (*tls.Certificate, error) {
	return func(ctx context.Context, names []string, algorithm dnsKeyAlgorithm) (*tls.Certificate, error) {
		if algorithm != dnsECDSA {
			return nil, errors.New("RSA issuance is not part of this test")
		}
		return issue(ctx, names)
	}
}

func dnsTestCertificate(t *testing.T, ca *ocspTestCA, names []string, serial int64, before, after time.Time) *tls.Certificate {
	return dnsTestCertificateForAlgorithm(t, ca, names, dnsECDSA, serial, before, after)
}

func dnsTestCertificateForAlgorithm(t *testing.T, ca *ocspTestCA, names []string, algorithm dnsKeyAlgorithm, serial int64, before, after time.Time) *tls.Certificate {
	t.Helper()
	var key crypto.Signer
	var err error
	switch algorithm {
	case dnsECDSA:
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case dnsRSA:
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	default:
		t.Fatalf("unsupported test certificate algorithm %q", algorithm)
	}
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial), DNSNames: names, NotBefore: before, NotAfter: after,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.issuer, key.Public(), ca.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der, ca.issuerDER}, PrivateKey: key, Leaf: leaf}
}

func TestDNSACMEManagerDeduplicatesAndScopesSNI(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	var metricMu sync.Mutex
	metrics := map[string]bool{}
	m.onIssue = func(name string, _ time.Time) {
		metricMu.Lock()
		metrics[name] = true
		metricMu.Unlock()
	}
	names := []string{"*.example.test", "example.test"}
	ca := newOCSPTestCA(t)
	now := time.Now()
	cert := dnsTestCertificate(t, ca, names, 2, now.Add(-time.Hour), now.Add(12*time.Hour))
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m.issue = dnsECDSAIssue(func(ctx context.Context, got []string) (*tls.Certificate, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return cert, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	provider := m.ProviderForListener(names, []string{"*.example.test"})
	same := m.ProviderForListener([]string{"example.test", "*.example.test"}, []string{"*.example.test"})
	<-started
	const handshakes = 20
	results := make(chan error, handshakes)
	for i := 0; i < handshakes; i++ {
		p := provider
		if i%2 == 0 {
			p = same
		}
		go func() {
			got, err := p.GetCertificate(dnsTestHello("WWW.Example.Test"))
			if err == nil && got != cert {
				err = errors.New("wrong certificate")
			}
			results <- err
		}()
	}
	close(release)
	for range handshakes {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent handshakes started %d orders", calls.Load())
	}
	for _, name := range []string{"", "example.test", "deep.www.example.test", "other.test"} {
		if _, err := provider.GetCertificate(dnsTestHello(name)); err == nil {
			t.Errorf("SNI %q should be rejected", name)
		}
	}
	for _, name := range []string{"one.example.test", "two.example.test"} {
		if _, err := provider.GetCertificate(dnsTestHello(name)); err != nil {
			t.Fatal(err)
		}
	}
	metricMu.Lock()
	defer metricMu.Unlock()
	if len(metrics) != 1 || !metrics["*.example.test"] {
		t.Fatalf("unbounded client SNI appeared in certificate metrics: %v", metrics)
	}
	summary := m.certificateSummary(names, []string{"*.example.test"})
	if summary.State != "available" || summary.Source != "acme" || summary.NotAfter.IsZero() {
		t.Fatalf("live certificate summary: %+v", summary)
	}
}

func TestDNSACMEDualCertificateClientCompatibility(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	names := []string{"example.test"}
	ca := newOCSPTestCA(t)
	now := time.Now()
	certs := map[dnsKeyAlgorithm]*tls.Certificate{
		dnsECDSA: dnsTestCertificateForAlgorithm(t, ca, names, dnsECDSA, 29, now.Add(-time.Hour), now.Add(12*time.Hour)),
		dnsRSA:   dnsTestCertificateForAlgorithm(t, ca, names, dnsRSA, 30, now.Add(-time.Hour), now.Add(12*time.Hour)),
	}
	m.issue = func(_ context.Context, _ []string, algorithm dnsKeyAlgorithm) (*tls.Certificate, error) {
		return certs[algorithm], nil
	}
	provider := m.Provider(names)
	for _, hello := range []*tls.ClientHelloInfo{dnsTestHello(names[0]), dnsTestRSAHello(names[0])} {
		if _, err := provider.GetCertificate(hello); err != nil {
			t.Fatal(err)
		}
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.issuer)
	clientCA := newCA(t)
	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(clientCA.cert)
	_, trusted := clientCA.clientCert(t, "trusted-client", 1, nil, nil)
	for _, requireClient := range []bool{false, true} {
		t.Run(fmt.Sprintf("mTLS=%t", requireClient), func(t *testing.T) {
			s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			s.TLS = &tls.Config{
				MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12,
				NextProtos: []string{"http/1.1"}, GetCertificate: provider.GetCertificate,
			}
			if requireClient {
				s.TLS.ClientAuth = tls.RequireAndVerifyClientCert
				s.TLS.ClientCAs = clientRoots
			}
			s.StartTLS()
			defer s.Close()
			for _, tt := range []struct {
				name  string
				suite []uint16
				want  x509.PublicKeyAlgorithm
			}{
				{"ECDSA", []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}, x509.ECDSA},
				{"RSA-only", []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}, x509.RSA},
				{"both prefer ECDSA", []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}, x509.ECDSA},
			} {
				t.Run(tt.name, func(t *testing.T) {
					cfg := &tls.Config{
						RootCAs: roots, ServerName: names[0], MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12,
						NextProtos: []string{"http/1.1"}, CipherSuites: tt.suite,
					}
					if requireClient {
						cfg.Certificates = []tls.Certificate{trusted}
					}
					conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", s.Listener.Addr().String(), cfg)
					if err != nil {
						t.Fatal(err)
					}
					state := conn.ConnectionState()
					_ = conn.Close()
					if state.Version != tls.VersionTLS12 || state.NegotiatedProtocol != "http/1.1" ||
						len(state.VerifiedChains) == 0 || state.PeerCertificates[0].PublicKeyAlgorithm != tt.want {
						t.Fatalf("wrong negotiated certificate or TLS policy: %+v", state)
					}
				})
			}
			if requireClient {
				_, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", s.Listener.Addr().String(), &tls.Config{
					RootCAs: roots, ServerName: names[0], MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12,
					CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
				})
				if err == nil {
					t.Fatal("RSA-only client without the required client certificate was accepted")
				}
			}
		})
	}
}

func TestDNSACMEVariantRenewalFailuresAreIsolated(t *testing.T) {
	for _, failedAlgorithm := range []dnsKeyAlgorithm{dnsECDSA, dnsRSA} {
		t.Run(string(failedAlgorithm), func(t *testing.T) {
			m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
			names := []string{"example.test"}
			ca := newOCSPTestCA(t)
			now := time.Now()
			certs := map[dnsKeyAlgorithm]*tls.Certificate{
				dnsECDSA: dnsTestCertificateForAlgorithm(t, ca, names, dnsECDSA, 40, now.Add(-time.Hour), now.Add(12*time.Hour)),
				dnsRSA:   dnsTestCertificateForAlgorithm(t, ca, names, dnsRSA, 41, now.Add(-time.Hour), now.Add(12*time.Hour)),
			}
			var fail atomic.Bool
			m.issue = func(_ context.Context, _ []string, algorithm dnsKeyAlgorithm) (*tls.Certificate, error) {
				if algorithm == failedAlgorithm && fail.Load() {
					return nil, errors.New("synthetic renewal failure")
				}
				return certs[algorithm], nil
			}
			provider := m.Provider(names)
			hellos := []*tls.ClientHelloInfo{dnsTestHello(names[0]), dnsTestRSAHello(names[0])}
			for i, hello := range hellos {
				if _, err := provider.GetCertificate(hello); err != nil {
					t.Fatalf("initial variant %d: %v", i, err)
				}
			}
			fail.Store(true)
			m.mu.Lock()
			group := m.groups[dnsNamesKey(names)]
			for i := range group.variants {
				if group.variants[i].algorithm == failedAlgorithm {
					group.variants[i].nextAttempt = time.Time{}
				}
			}
			m.mu.Unlock()
			m.notifyWorker(false)
			deadline := time.Now().Add(5 * time.Second)
			for {
				summary := m.certificateSummary(names, names)
				found := false
				for _, variant := range summary.Variants {
					if variant.Algorithm == string(failedAlgorithm) && variant.Error != "" {
						if variant.State != "available" || summary.State != "available" {
							t.Fatalf("valid certificate became unavailable: %+v", summary)
						}
						found = true
					}
				}
				if found {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("variant renewal failure was not reported: %+v", summary)
				}
				time.Sleep(time.Millisecond)
			}
			for i, hello := range hellos {
				got, err := provider.GetCertificate(hello)
				want := certs[[]dnsKeyAlgorithm{dnsECDSA, dnsRSA}[i]]
				if err != nil || got != want {
					t.Fatalf("variant %d was lost after %s renewal failure: %v", i, failedAlgorithm, err)
				}
			}
		})
	}
}

func TestDNSACMEFallsBackToRSAWhenECDSAIsUnavailable(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	names := []string{"example.test"}
	ca := newOCSPTestCA(t)
	now := time.Now()
	rsaCert := dnsTestCertificateForAlgorithm(t, ca, names, dnsRSA, 42, now.Add(-time.Hour), now.Add(12*time.Hour))
	m.issue = func(_ context.Context, _ []string, algorithm dnsKeyAlgorithm) (*tls.Certificate, error) {
		if algorithm == dnsECDSA {
			return nil, errors.New("synthetic ECDSA issuance failure")
		}
		return rsaCert, nil
	}
	provider := m.Provider(names)
	if _, err := provider.GetCertificate(dnsTestRSAHello(names[0])); err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	s.TLS = &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, GetCertificate: provider.GetCertificate}
	s.StartTLS()
	defer s.Close()
	roots := x509.NewCertPool()
	roots.AddCert(ca.issuer)
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", s.Listener.Addr().String(), &tls.Config{
		RootCAs: roots, ServerName: names[0], MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := conn.ConnectionState()
	_ = conn.Close()
	if state.PeerCertificates[0].PublicKeyAlgorithm != x509.RSA {
		t.Fatal("client supporting both algorithms did not receive the available RSA certificate")
	}
}

func TestDNSACMECacheSeparatesCertificateAlgorithms(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	names := []string{"example.test"}
	now := time.Now()
	cert := dnsTestCertificate(t, newOCSPTestCA(t), names, 43, now.Add(-time.Hour), now.Add(12*time.Hour))
	if err := m.ensureAccountKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.storeCertificate(context.Background(), names, dnsECDSA, cert); err != nil {
		t.Fatal(err)
	}
	ecdsaKey, err := m.certificateCacheKey(names, dnsECDSA)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := m.certificateCacheKey(names, dnsRSA)
	if err != nil || ecdsaKey == rsaKey {
		t.Fatalf("certificate algorithm cache keys collided: %v", err)
	}
	data, err := m.cache.Get(context.Background(), ecdsaKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.cache.Put(context.Background(), rsaKey, data); err != nil {
		t.Fatal(err)
	}
	if _, err := m.loadCertificate(context.Background(), names, dnsRSA); !errors.Is(err, errInvalidCachedCertificate) {
		t.Fatalf("ECDSA material was accepted in the RSA cache: %v", err)
	}
}

type sharedTLSSessionCache struct {
	mu    sync.Mutex
	state *tls.ClientSessionState
}

func (c *sharedTLSSessionCache) Get(string) (*tls.ClientSessionState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state, c.state != nil
}

func (c *sharedTLSSessionCache) Put(_ string, state *tls.ClientSessionState) {
	c.mu.Lock()
	c.state = state
	c.mu.Unlock()
}

func TestDNSACMESessionResumptionEnforcesLiveSNIScope(t *testing.T) {
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		for _, requireClient := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mTLS=%t", tls.VersionName(version), requireClient), func(t *testing.T) {
				m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
				names := []string{"*.example.test"}
				ca := newOCSPTestCA(t)
				now := time.Now()
				cert := dnsTestCertificate(t, ca, names, 22, now.Add(-time.Hour), now.Add(12*time.Hour))
				m.issue = dnsECDSAIssue(func(context.Context, []string) (*tls.Certificate, error) { return cert, nil })
				dynamic := &DynamicCertProvider{}
				dynamic.Set(m.ProviderForListener(names, []string{"first.example.test"}))
				s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				}))
				auth := &dynamicClientAuth{}
				var clientCerts []tls.Certificate
				if requireClient {
					clientCA := newCA(t)
					caFile := writePEM(t, t.TempDir(), "client-ca.pem", clientCA.pem)
					bundle, err := clientAuthForAddr([]config.ServerConfig{{
						Listen: ":443", TLS: &config.TLSConfig{Enabled: true,
							ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: caFile}},
					}}, ":443", nil)
					if err != nil {
						t.Fatal(err)
					}
					auth.set(bundle)
					_, clientCert := clientCA.clientCert(t, "client", 1, nil, nil)
					clientCerts = []tls.Certificate{clientCert}
				}
				s.TLS = &tls.Config{
					MinVersion: version, MaxVersion: version, NextProtos: []string{"http/1.1"},
					GetCertificate: dynamic.GetCertificate,
				}
				s.TLS.GetConfigForClient = dynamic.configForClient(auth.configForClient(s.TLS))
				s.StartTLS()
				defer s.Close()
				roots := x509.NewCertPool()
				roots.AddCert(ca.issuer)
				cache := &sharedTLSSessionCache{}
				connect := func(name string) (bool, error) {
					conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", s.Listener.Addr().String(), &tls.Config{
						RootCAs: roots, ServerName: name, MinVersion: version, MaxVersion: version,
						NextProtos: []string{"http/1.1"}, ClientSessionCache: cache, Certificates: clientCerts,
					})
					if err != nil {
						return false, err
					}
					defer conn.Close()
					resumed := conn.ConnectionState().DidResume
					if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+name+"\r\nConnection: close\r\n\r\n"); err != nil {
						return resumed, err
					}
					response, err := http.ReadResponse(bufio.NewReader(conn), nil)
					if err != nil {
						return resumed, err
					}
					_, err = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					return resumed, err
				}
				if resumed, err := connect("first.example.test"); err != nil || resumed {
					t.Fatalf("initial handshake: resumed=%v err=%v", resumed, err)
				}
				if resumed, err := connect("first.example.test"); err != nil || !resumed {
					t.Fatalf("allowed session did not resume: resumed=%v err=%v", resumed, err)
				}
				if _, err := connect("second.example.test"); err == nil {
					t.Fatal("resumption bypassed the listener SNI scope")
				}
				dynamic.Set(m.ProviderForListener(names, []string{"second.example.test"}))
				if _, err := connect("first.example.test"); err == nil {
					t.Fatal("a removed SNI resumed after the policy was swapped")
				}
				if _, err := connect("second.example.test"); err != nil {
					t.Fatalf("new listener SNI was rejected: %v", err)
				}
			})
		}
	}
}

func TestDNSACMEManagerCacheRestartAndIssuerIsolation(t *testing.T) {
	dir := t.TempDir()
	names := []string{"*.example.test", "example.test"}
	now := time.Now()
	cert := dnsTestCertificate(t, newOCSPTestCA(t), names, 3, now.Add(-time.Hour), now.Add(12*time.Hour))
	var issues atomic.Int32
	first := testDNSManager(t, dir, "https://first.example.test/directory")
	first.issue = dnsECDSAIssue(func(context.Context, []string) (*tls.Certificate, error) {
		issues.Add(1)
		return cert, nil
	})
	p := first.Provider(names)
	if _, err := p.GetCertificate(dnsTestHello("www.example.test")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := testDNSManager(t, dir, "https://first.example.test/directory")
	restarted.issue = dnsECDSAIssue(func(context.Context, []string) (*tls.Certificate, error) {
		issues.Add(1)
		return nil, errors.New("cache was not reused")
	})
	p = restarted.Provider(names)
	loaded, err := p.GetCertificate(dnsTestHello("www.example.test"))
	if err != nil || loaded.Leaf.SerialNumber.Cmp(cert.Leaf.SerialNumber) != 0 {
		t.Fatalf("cached certificate: %v", err)
	}
	if issues.Load() != 1 {
		t.Fatalf("restart unexpectedly ordered %d certificates", issues.Load())
	}

	otherCA := testDNSManager(t, dir, "https://other.example.test/directory")
	otherCA.issue = dnsECDSAIssue(func(context.Context, []string) (*tls.Certificate, error) {
		issues.Add(1)
		return cert, nil
	})
	if _, err := otherCA.Provider(names).GetCertificate(dnsTestHello("www.example.test")); err != nil {
		t.Fatal(err)
	}
	if issues.Load() != 2 {
		t.Fatal("certificate was reused across CA directories")
	}
}

func TestDNSACMEManagerRenewsWithoutDroppingValidCertificate(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	names := []string{"example.test"}
	ca := newOCSPTestCA(t)
	now := time.Now()
	old := dnsTestCertificate(t, ca, names, 4, now.Add(-2*time.Hour), now.Add(15*time.Minute))
	fresh := dnsTestCertificate(t, ca, names, 5, now.Add(-time.Minute), now.Add(12*time.Hour))
	if err := m.ensureAccountKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.storeCertificate(context.Background(), names, dnsECDSA, old); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	m.issue = dnsECDSAIssue(func(ctx context.Context, _ []string) (*tls.Certificate, error) {
		close(started)
		select {
		case <-release:
			return fresh, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	provider := m.Provider(names)
	<-started
	got, err := provider.GetCertificate(dnsTestHello("example.test"))
	if err != nil || got.Leaf.SerialNumber.Cmp(old.Leaf.SerialNumber) != 0 {
		t.Fatalf("valid old certificate during renewal: %v", err)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err = provider.GetCertificate(dnsTestHello("example.test"))
		if err == nil && got.Leaf.SerialNumber.Cmp(fresh.Leaf.SerialNumber) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("renewed certificate was not installed: %v", err)
}

func TestDNSACMEManagerIsolatesStalledIssuance(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	now := time.Now()
	ca := newOCSPTestCA(t)
	cached := dnsTestCertificate(t, ca, []string{"cached.example.test"}, 30, now.Add(-time.Hour), now.Add(12*time.Hour))
	issued := dnsTestCertificate(t, ca, []string{"new.example.test"}, 31, now.Add(-time.Hour), now.Add(12*time.Hour))
	if err := m.ensureAccountKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.storeCertificate(context.Background(), []string{"cached.example.test"}, dnsECDSA, cached); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	m.issue = dnsECDSAIssue(func(ctx context.Context, names []string) (*tls.Certificate, error) {
		switch names[0] {
		case "slow.example.test":
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		case "new.example.test":
			return issued, nil
		default:
			return nil, errors.New("unexpected order")
		}
	})
	m.Provider([]string{"slow.example.test"})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow order did not start")
	}
	for _, tt := range []struct {
		name string
		want *big.Int
	}{{"cached.example.test", cached.Leaf.SerialNumber}, {"new.example.test", issued.Leaf.SerialNumber}} {
		result := make(chan error, 1)
		provider := m.Provider([]string{tt.name})
		go func() {
			got, err := provider.GetCertificate(dnsTestHello(tt.name))
			if err == nil && got.Leaf.SerialNumber.Cmp(tt.want) != 0 {
				err = errors.New("wrong certificate")
			}
			result <- err
		}()
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s was blocked by an unrelated order", tt.name)
		}
	}
}

type blockedDNSCache struct {
	autocert.Cache
	key     string
	entered chan struct{}
	release chan struct{}
}

func (c *blockedDNSCache) Get(ctx context.Context, key string) ([]byte, error) {
	if key == c.key {
		close(c.entered)
		<-c.release
	}
	return c.Cache.Get(ctx, key)
}

func TestDNSACMEManagerBoundsShutdownWithBlockedCache(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	now := time.Now()
	cached := dnsTestCertificate(t, newOCSPTestCA(t), []string{"cached.example.test"}, 32, now.Add(-time.Hour), now.Add(12*time.Hour))
	if err := m.ensureAccountKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.storeCertificate(context.Background(), []string{"cached.example.test"}, dnsECDSA, cached); err != nil {
		t.Fatal(err)
	}
	blockedKey, err := m.certificateCacheKey([]string{"blocked.example.test"}, dnsECDSA)
	if err != nil {
		t.Fatal(err)
	}
	cache := &blockedDNSCache{Cache: m.cache, key: blockedKey, entered: make(chan struct{}), release: make(chan struct{})}
	m.cache = cache
	var release sync.Once
	defer release.Do(func() { close(cache.release) })
	m.Provider([]string{"blocked.example.test"})
	select {
	case <-cache.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cache operation did not start")
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.Provider([]string{"cached.example.test"}).GetCertificate(dnsTestHello("cached.example.test"))
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unrelated cached certificate was blocked by a stuck cache read")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.close(ctx); err == nil {
		t.Fatal("shutdown did not report the blocked cache")
	}
	release.Do(func() { close(cache.release) })
	if err := m.Close(); err != nil {
		t.Fatalf("manager did not stop after the cache recovered: %v", err)
	}
}

func TestDNSACMEExpiredCertificateRemainsVisibleAfterRenewalFailure(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	names := []string{"example.test"}
	now := time.Now()
	cert := dnsTestCertificate(t, newOCSPTestCA(t), names, 23, now.Add(-time.Hour), now.Add(time.Hour))
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	m.now = func() time.Time { return time.Unix(0, clock.Load()) }
	m.issue = dnsECDSAIssue(func(context.Context, []string) (*tls.Certificate, error) { return cert, nil })
	if _, err := m.Provider(names).GetCertificate(dnsTestHello("example.test")); err != nil {
		t.Fatal(err)
	}
	clock.Store(now.Add(2 * time.Hour).UnixNano())
	m.mu.Lock()
	group := m.groups[dnsNamesKey(names)]
	m.mu.Unlock()
	m.finishFailure(group, &group.variants[0], errors.New("synthetic renewal failure"))
	summary := m.certificateSummary(names, names)
	if summary.State != "failed" || !summary.NotAfter.Equal(cert.Leaf.NotAfter) || summary.Error == "" {
		t.Fatalf("expired live certificate disappeared from status: %+v", summary)
	}
}

func TestDNSACMEManagerRepeatedRenewalsStopCleanly(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	names := []string{"example.test"}
	start := time.Now()
	ca := newOCSPTestCA(t)
	const renewals = 8
	certs := make([]*tls.Certificate, renewals+1)
	for i := range certs {
		at := start.Add(time.Duration(i) * 2 * time.Minute)
		certs[i] = dnsTestCertificate(t, ca, names, int64(i+1), at.Add(-time.Second), at.Add(3*time.Minute))
	}
	var clock atomic.Int64
	clock.Store(start.UnixNano())
	m.now = func() time.Time { return time.Unix(0, clock.Load()) }
	var issued atomic.Int32
	m.issue = dnsECDSAIssue(func(context.Context, []string) (*tls.Certificate, error) {
		index := int(issued.Add(1)) - 1
		if index >= len(certs) {
			return nil, errors.New("unexpected additional renewal")
		}
		return certs[index], nil
	})
	provider := m.Provider(names)
	for i, want := range certs {
		if i > 0 {
			clock.Store(start.Add(time.Duration(i) * 2 * time.Minute).UnixNano())
			m.notifyWorker(false)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			got, err := provider.GetCertificate(dnsTestHello("example.test"))
			if err == nil && got.Leaf.SerialNumber.Cmp(want.Leaf.SerialNumber) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("renewal %d was not served: %v", i, err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if got := issued.Load(); got != renewals+1 {
		t.Fatalf("issued %d certificates, want %d", got, renewals+1)
	}
}

func TestDNSACMECertificateCacheRejectsInvalidMaterial(t *testing.T) {
	now := time.Now()
	ca := newOCSPTestCA(t)
	otherCA := newOCSPTestCA(t)
	names := []string{"example.test"}
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		template func(*x509.Certificate)
		cert     func(*tls.Certificate)
	}{
		{name: "missing certificate", cert: func(c *tls.Certificate) { c.Certificate = nil }},
		{name: "missing private key", cert: func(c *tls.Certificate) { c.PrivateKey = nil }},
		{name: "malformed leaf", cert: func(c *tls.Certificate) { c.Certificate[0] = []byte("not DER") }},
		{name: "CA leaf", template: func(c *x509.Certificate) { c.BasicConstraintsValid = true; c.IsCA = true }},
		{name: "not yet valid", template: func(c *x509.Certificate) { c.NotBefore = now.Add(time.Hour) }},
		{name: "expired", template: func(c *x509.Certificate) { c.NotAfter = now.Add(-time.Second) }},
		{name: "unexpected IP SAN", template: func(c *x509.Certificate) { c.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")} }},
		{name: "unexpected email SAN", template: func(c *x509.Certificate) { c.EmailAddresses = []string{"ops@example.test"} }},
		{name: "unexpected URI SAN", template: func(c *x509.Certificate) { c.URIs = []*url.URL{{Scheme: "https", Host: "example.test"}} }},
		{name: "wrong private key", cert: func(c *tls.Certificate) { c.PrivateKey = otherKey }},
		{name: "client-only EKU", template: func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} }},
		{name: "unrelated issuer", cert: func(c *tls.Certificate) { c.Certificate[1] = otherCA.issuerDER }},
		{name: "mismatched parsed leaf", cert: func(c *tls.Certificate) { c.Leaf = otherCA.issuer }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			template := &x509.Certificate{
				SerialNumber: big.NewInt(20), DNSNames: names, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
				KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}
			if tt.template != nil {
				tt.template(template)
			}
			der, err := x509.CreateCertificate(rand.Reader, template, ca.issuer, &key.PublicKey, ca.key)
			if err != nil {
				t.Fatal(err)
			}
			cert := &tls.Certificate{Certificate: [][]byte{der, ca.issuerDER}, PrivateKey: key}
			if tt.cert != nil {
				tt.cert(cert)
			}
			if err := validateDNSCertificate(cert, names, dnsECDSA, now); !errors.Is(err, errInvalidCachedCertificate) {
				t.Fatalf("invalid cached certificate accepted: %v", err)
			}
		})
	}
	if err := validateDNSCertificate(nil, names, dnsECDSA, now); !errors.Is(err, errInvalidCachedCertificate) {
		t.Fatalf("missing cached certificate accepted: %v", err)
	}
	leafOnly := dnsTestCertificate(t, ca, names, 21, now.Add(-time.Hour), now.Add(time.Hour))
	leafOnly.Certificate = leafOnly.Certificate[:1]
	if err := validateDNSCertificate(leafOnly, names, dnsECDSA, now); err != nil {
		t.Fatalf("valid leaf-only CA response rejected: %v", err)
	}
}

func TestDNSACMEAccountCacheRejectsInvalidMaterial(t *testing.T) {
	wrongCurve, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongCurveDER, err := x509.MarshalPKCS8PrivateKey(wrongCurve)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{"oversized", make([]byte, maxCachedCertBytes+1)},
		{"not PEM", []byte("not a private key")},
		{"malformed PKCS8", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("bad DER")})},
		{"wrong curve", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: wrongCurveDER})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
			if err := m.cache.Put(context.Background(), m.accountCacheKey(), tt.data); err != nil {
				t.Fatal(err)
			}
			if err := m.ensureAccountKey(context.Background()); err == nil || m.client.Key != nil {
				t.Fatalf("invalid account key was accepted: %v", err)
			}
		})
	}
}

func TestDNSACMECAFailuresAreBoundedAndRedacted(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			const secret = "synthetic-private-provider-response"
			var ca *httptest.Server
			ca = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Replay-Nonce", "synthetic-nonce")
				switch r.URL.Path {
				case "/directory":
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"newNonce":%q,"newAccount":%q,"newOrder":%q}`,
						ca.URL+"/nonce", ca.URL+"/account", ca.URL+"/order")
				case "/nonce":
					w.WriteHeader(http.StatusNoContent)
				case "/account":
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(code)
					fmt.Fprintf(w, `{"type":"urn:ietf:params:acme:error:serverInternal","detail":%q}`, secret)
				default:
					t.Errorf("unexpected CA request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer ca.Close()
			m := testDNSManager(t, t.TempDir(), ca.URL+"/directory")
			if err := m.ensureAccountKey(context.Background()); err != nil {
				t.Fatal(err)
			}
			m.client.RetryBackoff = func(int, *http.Request, *http.Response) time.Duration { return 0 }
			_, err := m.issueCertificate(context.Background(), []string{"example.test"}, dnsECDSA)
			var acmeErr *acme.Error
			if !errors.As(err, &acmeErr) || acmeErr.StatusCode != code {
				t.Fatalf("CA failure was not retained: %v", err)
			}
			if m.registered || strings.Contains(dnsACMEErrorMessage(err), secret) {
				t.Fatal("failed registration was accepted or its response was disclosed")
			}
		})
	}
}

func TestDNSACMEManagerRejectsInvalidCacheAndRedactsFailures(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	names := []string{"example.test"}
	now := time.Now()
	wrong := dnsTestCertificate(t, newOCSPTestCA(t), []string{"wrong.example.test"}, 6, now.Add(-time.Hour), now.Add(12*time.Hour))
	if err := m.ensureAccountKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	cacheKey, err := m.certificateCacheKey(names, dnsECDSA)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(wrong.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	for _, raw := range wrong.Certificate {
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})...)
	}
	if err := m.cache.Put(context.Background(), cacheKey, data); err != nil {
		t.Fatal(err)
	}
	m.issue = dnsECDSAIssue(func(context.Context, []string) (*tls.Certificate, error) {
		return nil, errors.New("sensitive provider response")
	})
	_, err = m.Provider(names).GetCertificate(dnsTestHello("example.test"))
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unredacted or missing issuance failure: %v", err)
	}
	summary := m.certificateSummary(names, names)
	if summary.State != "failed" || strings.Contains(summary.Error, "sensitive") || summary.Error == "" {
		t.Fatalf("failure status: %+v", summary)
	}
}

func TestDNSACMEManagerReplacesCorruptCachedCertificate(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"malformed", []byte("not a certificate")},
		{"oversized", make([]byte, maxCachedCertBytes+1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
			names := []string{"example.test"}
			if err := m.ensureAccountKey(context.Background()); err != nil {
				t.Fatal(err)
			}
			key, err := m.certificateCacheKey(names, dnsECDSA)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.cache.Put(context.Background(), key, tt.data); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			replacement := dnsTestCertificate(t, newOCSPTestCA(t), names, 23, now.Add(-time.Hour), now.Add(12*time.Hour))
			var issued atomic.Int32
			m.issue = dnsECDSAIssue(func(context.Context, []string) (*tls.Certificate, error) {
				issued.Add(1)
				return replacement, nil
			})
			got, err := m.Provider(names).GetCertificate(dnsTestHello(names[0]))
			if err != nil || got != replacement || issued.Load() != 1 {
				t.Fatalf("corrupt cache was not replaced: cert=%v issued=%d err=%v", got != nil, issued.Load(), err)
			}
			cached, err := m.loadCertificate(context.Background(), names, dnsECDSA)
			if err != nil || cached == nil || cached.Leaf.SerialNumber.Cmp(replacement.Leaf.SerialNumber) != 0 {
				t.Fatalf("replacement was not cached: cert=%v err=%v", cached != nil, err)
			}
		})
	}
}

type failingDNSCache struct {
	autocert.Cache
	readKey  string
	writeKey string
}

func (c *failingDNSCache) Get(ctx context.Context, key string) ([]byte, error) {
	if key == c.readKey {
		return nil, errors.New("sensitive cache read failure")
	}
	return c.Cache.Get(ctx, key)
}

func (c *failingDNSCache) Put(ctx context.Context, key string, data []byte) error {
	if key == c.writeKey {
		return errors.New("sensitive cache write failure")
	}
	return c.Cache.Put(ctx, key, data)
}

func TestDNSACMEManagerSurfacesCacheFailures(t *testing.T) {
	for _, tt := range []struct {
		name       string
		account    bool
		write      bool
		wantIssued int32
	}{
		{name: "account read", account: true},
		{name: "certificate read"},
		{name: "certificate write", write: true, wantIssued: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
			names := []string{"example.test"}
			cache := &failingDNSCache{Cache: m.cache}
			if tt.account {
				cache.readKey = m.accountCacheKey()
			} else {
				if err := m.ensureAccountKey(context.Background()); err != nil {
					t.Fatal(err)
				}
				key, err := m.certificateCacheKey(names, dnsECDSA)
				if err != nil {
					t.Fatal(err)
				}
				if tt.write {
					cache.writeKey = key
				} else {
					cache.readKey = key
				}
			}
			m.cache = cache
			now := time.Now()
			cert := dnsTestCertificate(t, newOCSPTestCA(t), names, 24, now.Add(-time.Hour), now.Add(12*time.Hour))
			var issued atomic.Int32
			m.issue = dnsECDSAIssue(func(context.Context, []string) (*tls.Certificate, error) {
				issued.Add(1)
				return cert, nil
			})
			got, err := m.Provider(names).GetCertificate(dnsTestHello(names[0]))
			if got != nil || err == nil || !strings.Contains(err.Error(), "cache failed") || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("cache failure was hidden or disclosed: cert=%v err=%v", got != nil, err)
			}
			if issued.Load() != tt.wantIssued {
				t.Fatalf("issued %d certificates, want %d", issued.Load(), tt.wantIssued)
			}
			summary := m.certificateSummary(names, names)
			if summary.State != "failed" || summary.Error == "" || strings.Contains(summary.Error, "sensitive") {
				t.Fatalf("cache failure status: %+v", summary)
			}
		})
	}
}

func TestDNSACMEManagerShutdownCancelsIssuance(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	started := make(chan struct{})
	m.issue = dnsECDSAIssue(func(ctx context.Context, _ []string) (*tls.Certificate, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	m.Provider([]string{"example.test"})
	<-started
	done := make(chan struct{})
	go func() { _ = m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel issuance")
	}
}

func TestDNSAuthorizationLocksReleaseOnCancellationAndBound(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	unlock, _, err := m.lockAuthorization(context.Background(), "auth")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := m.lockAuthorization(ctx, "auth"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock wait: %v", err)
	}
	unlock()
	if _, err := m.authorizeDNSOrder(context.Background(), &dnsPendingOrder{order: &acme.Order{AuthzURLs: make([]string, 201)}}, []string{"a.test"}); err == nil {
		t.Fatal("unbounded authorization list was accepted")
	}
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if len(m.authStates) != 0 {
		t.Fatalf("authorization locks were retained: %d", len(m.authStates))
	}
}

func TestDNSAuthorizationStateRespectsExpiryAndCapacity(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	unlock, _, err := m.lockAuthorization(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(5 * time.Minute)
	m.rememberValidDNSAuthorization("shared", "example.test", expires)
	_, valid, err := m.lockAuthorization(context.Background(), "shared")
	if err != nil || valid.name != "example.test" || !valid.until.Equal(expires) {
		t.Fatalf("valid shared authorization: %+v, %v", valid, err)
	}
	m.rememberValidDNSAuthorization("shared", "example.test", time.Now().Add(-time.Second))
	if _, _, err := m.lockAuthorization(context.Background(), "shared"); !errors.Is(err, errDNSAuthorizationBusy) {
		t.Fatalf("expired authorization was shared while cleanup remained active: %v", err)
	}
	unlock()
	m.authMu.Lock()
	for i := 0; i < maxDNSAuthStates; i++ {
		m.authStates[strconv.Itoa(i)] = &dnsAuthorizationState{nextPoll: time.Now().Add(time.Minute)}
	}
	m.authMu.Unlock()
	if _, _, err := m.lockAuthorization(context.Background(), "new"); !errors.Is(err, errDNSAuthorizationStateLimit) {
		t.Fatalf("authorization state capacity was not enforced: %v", err)
	}
	m.authMu.Lock()
	m.authStates["0"].nextPoll = time.Time{}
	m.authMu.Unlock()
	unlock, _, err = m.lockAuthorization(context.Background(), "new")
	if err != nil {
		t.Fatalf("expired authorization state was not pruned: %v", err)
	}
	unlock()
}

func TestDNSACMEDeferredDiscoveryReportsSafeFailure(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	cause := errors.New("private provider response")
	deferred := &dnsAuthorizationDeferredError{cause: cause}
	if !errors.Is(deferred, errDNSAuthorizationBusy) || !errors.Is(deferred, cause) {
		t.Fatal("deferred discovery lost its retry marker or underlying error")
	}
	m.issue = func(context.Context, []string, dnsKeyAlgorithm) (*tls.Certificate, error) {
		return nil, deferred
	}
	group := &dnsCertificateGroup{names: []string{"example.test"}, changed: make(chan struct{})}
	variant := &group.variants[0]
	variant.algorithm = dnsRSA
	m.refresh(group, variant)
	if variant.lastFailure == "" || strings.Contains(variant.lastFailure, cause.Error()) ||
		!variant.nextAttempt.After(time.Now()) {
		t.Fatalf("deferred failure was not safely reported and scheduled: %+v", variant)
	}
	diagnostic := variant.lastFailure
	m.issue = func(context.Context, []string, dnsKeyAlgorithm) (*tls.Certificate, error) {
		return nil, errDNSAuthorizationBusy
	}
	m.refresh(group, variant)
	if variant.lastFailure != diagnostic {
		t.Fatal("busy authorization retry cleared its deferred diagnostic")
	}
	m.mu.Lock()
	m.groups[dnsNamesKey(group.names)] = group
	m.mu.Unlock()
	summary := m.certificateSummary(group.names, group.names)
	if summary.State != "pending" || summary.Error == "" || strings.Contains(summary.Error, cause.Error()) {
		t.Fatalf("deferred status should remain pending with a safe diagnostic: %+v", summary)
	}
	provider := &dnsACMEProvider{manager: m, group: group, patterns: group.names}
	result := make(chan error, 1)
	go func() {
		_, err := provider.GetCertificate(dnsTestRSAHello("example.test"))
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("transient diagnostic prematurely failed the handshake: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	now := time.Now()
	cert := dnsTestCertificateForAlgorithm(t, newOCSPTestCA(t), group.names, dnsRSA, 99, now.Add(-time.Hour), now.Add(time.Hour))
	m.finishSuccess(group, variant, cert)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("handshake did not recover after issuance: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handshake did not resume after issuance")
	}
}

type testDNSChallengeLease struct {
	waitErr       error
	cleanupErr    error
	cleanupCalls  int
	cleanupActive bool
}

func (l *testDNSChallengeLease) Wait(context.Context) error { return l.waitErr }

func (*testDNSChallengeLease) admitCleanup(ctx context.Context) (func(), error) {
	return admitTestDNSCleanup(ctx)
}

func (l *testDNSChallengeLease) cleanupAdmitted(ctx context.Context) error {
	l.cleanupCalls++
	_, bounded := ctx.Deadline()
	l.cleanupActive = bounded && ctx.Err() == nil
	return l.cleanupErr
}

type testDNSChallengeProvider struct {
	lease dnsChallengeLease
	err   error
}

type slowDNSCleanupLease struct {
	active  *atomic.Int32
	peak    *atomic.Int32
	cleaned *atomic.Int32
}

func (*slowDNSCleanupLease) Wait(context.Context) error { return nil }

func (*slowDNSCleanupLease) admitCleanup(ctx context.Context) (func(), error) {
	return admitTestDNSCleanup(ctx)
}

func admitTestDNSCleanup(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return func() {}, nil
}

func (l *slowDNSCleanupLease) cleanupAdmitted(ctx context.Context) error {
	active := l.active.Add(1)
	defer l.active.Add(-1)
	for peak := l.peak.Load(); active > peak && !l.peak.CompareAndSwap(peak, active); peak = l.peak.Load() {
	}
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		l.cleaned.Add(1)
		return nil
	}
}

func TestDNSACMECleanupAttemptsAllLeasesWithBoundedConcurrency(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	var active, peak, cleaned atomic.Int32
	results := make(chan error, 2)
	for range cap(results) {
		var pending []*dnsPendingAuthorization
		for range 8 {
			pending = append(pending, &dnsPendingAuthorization{
				lease: &slowDNSCleanupLease{active: &active, peak: &peak, cleaned: &cleaned},
			})
		}
		go func() { results <- m.cleanupDNSAuthorizations(pending, 500*time.Millisecond) }()
	}
	for range cap(results) {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if cleaned.Load() != 16 || peak.Load() > dnsAuthWorkers {
		t.Fatalf("cleanup attempts=%d peak concurrency=%d", cleaned.Load(), peak.Load())
	}
}

func (p testDNSChallengeProvider) Present(context.Context, string, string) (dnsChallengeLease, error) {
	return p.lease, p.err
}

func TestDNSACMEChallengeCleanupAfterFailure(t *testing.T) {
	tests := []struct {
		name       string
		presentErr error
		waitErr    error
		cleanupErr error
		nilLease   bool
		cancel     bool
	}{
		{name: "propagation timeout", waitErr: context.DeadlineExceeded},
		{name: "ambiguous update cancellation", presentErr: context.Canceled, cancel: true},
		{name: "cleanup failure is retained", waitErr: context.DeadlineExceeded, cleanupErr: errors.New("synthetic private detail")},
		{name: "missing provider lease", nilLease: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
			if err := m.ensureAccountKey(context.Background()); err != nil {
				t.Fatal(err)
			}
			lease := &testDNSChallengeLease{waitErr: tt.waitErr, cleanupErr: tt.cleanupErr}
			provider := testDNSChallengeProvider{err: tt.presentErr}
			if !tt.nilLease {
				provider.lease = lease
			}
			m.provider = provider
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancel {
				cancel()
			}
			auth := &acme.Authorization{
				Identifier: acme.AuthzID{Type: "dns", Value: "example.test"},
				Challenges: []*acme.Challenge{{Type: "dns-01", Status: acme.StatusPending, Token: "synthetic-token"}},
			}
			pending, err := m.prepareDNSAuthorization(ctx, auth)
			if err == nil {
				err = m.startDNSAuthorization(ctx, pending)
			}
			err = errors.Join(err, m.cleanupDNSAuthorizations([]*dnsPendingAuthorization{pending}, 15*time.Second))
			if err == nil {
				t.Fatal("expected challenge failure")
			}
			if tt.presentErr != nil && !errors.Is(err, tt.presentErr) ||
				tt.waitErr != nil && !errors.Is(err, tt.waitErr) {
				t.Fatalf("original failure was lost: %v", err)
			}
			if !tt.nilLease && (lease.cleanupCalls != 1 || !lease.cleanupActive) {
				t.Fatalf("cleanup calls=%d, active bounded context=%v", lease.cleanupCalls, lease.cleanupActive)
			}
			if tt.cleanupErr != nil {
				var cleanupErr *dnsCleanupError
				if !errors.As(err, &cleanupErr) || strings.Contains(dnsACMEErrorMessage(err), "synthetic private detail") {
					t.Fatalf("cleanup failure was lost or disclosed: %v", err)
				}
			}
		})
	}
}

func TestDNSACMEProcessingChallengeRestoresAndWaitsForTXT(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	if err := m.ensureAccountKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease := &testDNSChallengeLease{waitErr: context.DeadlineExceeded}
	m.provider = testDNSChallengeProvider{lease: lease}
	auth := &acme.Authorization{
		Identifier: acme.AuthzID{Type: "dns", Value: "example.test"},
		Challenges: []*acme.Challenge{{Type: "dns-01", Status: acme.StatusProcessing, Token: "synthetic-token"}},
	}
	pending, err := m.prepareDNSAuthorization(context.Background(), auth)
	if err != nil || pending.lease != lease {
		t.Fatalf("processing challenge did not restore its TXT lease: %v", err)
	}
	if err := m.startDNSAuthorization(context.Background(), pending); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("processing challenge did not wait for propagation: %v", err)
	}
	if err := m.cleanupDNSAuthorizations([]*dnsPendingAuthorization{pending}, time.Second); err != nil || lease.cleanupCalls != 1 {
		t.Fatalf("restored lease was not cleaned: %v, calls=%d", err, lease.cleanupCalls)
	}
}

func TestDNSACMERejectsUnusableChallenges(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	for _, tt := range []struct {
		name       string
		challenges []*acme.Challenge
		want       string
	}{
		{"missing DNS challenge", []*acme.Challenge{{Type: "http-01"}}, "does not offer DNS-01"},
		{"invalid DNS challenge", []*acme.Challenge{{Type: "dns-01", Status: acme.StatusInvalid}}, "not pending"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := m.prepareDNSAuthorization(context.Background(), &acme.Authorization{Challenges: tt.challenges})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("unusable challenge was not rejected: %v", err)
			}
		})
	}
}

func TestDNSACMEFailureClassesDoNotDiscloseDetails(t *testing.T) {
	const secret = "synthetic private provider detail"
	tests := []struct {
		err  error
		want string
	}{
		{&dnsCleanupError{cause: errors.New(secret)}, "cleanup failed"},
		{&dnsChallengeError{action: "present", class: "egress", cause: errors.New(secret)}, "present blocked by egress"},
		{errCacheFailure, "cache failed"},
		{errDNSAuthorizationStateLimit, "authorization capacity reached"},
		{egress.ErrBlocked, "ACME request blocked by egress"},
		{context.DeadlineExceeded, "issuance timed out"},
		{context.Canceled, "issuance was cancelled"},
		{&acme.Error{StatusCode: http.StatusTooManyRequests, Detail: secret}, "rate limit"},
		{&dnsACMERetryableResponseError{statusCode: http.StatusTooManyRequests, cause: errors.New(secret)}, "rate limit"},
		{errors.New(secret), "issuance failed"},
	}
	for _, tt := range tests {
		got := dnsACMEErrorMessage(tt.err)
		if !strings.Contains(got, tt.want) || strings.Contains(got, secret) {
			t.Errorf("failure class %q leaked details or omitted its action: %q", tt.want, got)
		}
	}
	cause := errors.New(secret)
	retryable := &dnsACMERetryableResponseError{statusCode: http.StatusTooManyRequests, cause: cause}
	if !errors.Is(retryable, cause) || !retryableDNSChallengeRejection(retryable) {
		t.Fatal("retryable response lost its cause or retry classification")
	}
	if retryableDNSChallengeRejection(errors.Join(retryable, egress.ErrBlocked)) {
		t.Fatal("egress denial was treated as retryable")
	}
}

func TestDNSACMEScopedProviderReloadIsAtomic(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	names := []string{"*.example.test", "example.test"}
	now := time.Now()
	cert := dnsTestCertificate(t, newOCSPTestCA(t), names, 7, now.Add(-time.Hour), now.Add(12*time.Hour))
	var issues atomic.Int32
	m.issue = dnsECDSAIssue(func(context.Context, []string) (*tls.Certificate, error) {
		issues.Add(1)
		return cert, nil
	})
	old := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":443", ServerNames: []string{"first.example.test"},
		TLS: &config.TLSConfig{Enabled: true, ACME: &config.ACMEConfig{Enabled: true, Domains: names}},
	}}}
	dynamic := &DynamicCertProvider{}
	dynamic.Set(m.ProviderForListener(names, old.Servers[0].ServerNames))
	if _, err := dynamic.GetCertificate(dnsTestHello("first.example.test")); err != nil {
		t.Fatal(err)
	}
	entry := &listenerEntry{provider: dynamic, certFingerprint: acmeListenerFingerprint(old.Servers, ":443")}
	s := &Server{ACME: m, listeners: map[string]*listenerEntry{":443": entry}}
	next, err := old.Clone()
	if err != nil {
		t.Fatal(err)
	}
	next.Servers[0].ServerNames = []string{"second.example.test"}
	prepared, err := s.prepareCertRotation(next)
	if err != nil || prepared == nil {
		t.Fatalf("prepare SNI scope update: %v", err)
	}
	prepared.abort()
	if _, err := dynamic.GetCertificate(dnsTestHello("second.example.test")); err == nil {
		t.Fatal("aborted reload published the new SNI scope")
	}
	prepared, err = s.prepareCertRotation(next)
	if err != nil || prepared == nil {
		t.Fatalf("prepare SNI scope update: %v", err)
	}
	prepared.commit()
	if _, err := dynamic.GetCertificate(dnsTestHello("first.example.test")); err == nil {
		t.Fatal("removed SNI remained authorized after publish")
	}
	if _, err := dynamic.GetCertificate(dnsTestHello("second.example.test")); err != nil {
		t.Fatalf("new SNI was not authorized: %v", err)
	}
	if issues.Load() != 1 {
		t.Fatalf("SNI policy reload started %d certificate orders", issues.Load())
	}
	s.stopUnusedACMEManager(nil)
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if !closed {
		t.Fatal("removing ACME did not stop the DNS renewal owner")
	}
}
