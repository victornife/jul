// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

// backendPKI is a throwaway CA used to give a test backend a private-CA
// certificate, so the proxy's verification is exercised for real rather than
// asserted from struct fields.
type backendPKI struct {
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	caPEM  []byte
	caPath string
}

func newBackendPKI(t *testing.T) *backendPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "backend-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
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
	ca := &backendPKI{cert: cert, key: key, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
	ca.caPath = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca.caPath, ca.caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return ca
}

// issue signs a leaf for the given names, including 127.0.0.1 as an IP SAN so a
// loopback dial can also be verified by address.
func (p *backendPKI) issue(t *testing.T, cn string, dnsNames, uris []string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var parsed []*url.URL
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, u)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     dnsNames,
		URIs:         parsed,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.cert, &key.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

// writePair writes a certificate/key pair to disk for use as client material.
func writePair(t *testing.T, dir string, cert tls.Certificate) (certPath, keyPath string) {
	t.Helper()
	certPath = filepath.Join(dir, "client.pem")
	keyPath = filepath.Join(dir, "client.key")
	leaf := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	key, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, leaf, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: key}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// tlsBackend starts an HTTPS test server with the given TLS config.
func tlsBackend(t *testing.T, cfg *tls.Config, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = cfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func okBackend() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "verified backend")
	})
}

// serveThrough proxies one request through a location and returns the status
// and body the client sees.
func serveThrough(t *testing.T, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig) (int, string) {
	t.Helper()
	h, err := NewProxy(t.Context(), config.ServerConfig{}, loc, upstreams, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge.example/", nil))
	return rec.Code, rec.Body.String()
}

// TestProxyBackendTLSVerification is the matrix that matters: a private-CA
// backend is reachable only when a policy says so, and every way of getting the
// policy wrong fails closed.
func TestProxyBackendTLSVerification(t *testing.T) {
	ca := newBackendPKI(t)
	backend := tlsBackend(t, &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, "backend", []string{"inventory.internal"}, []string{"spiffe://example/inventory"})},
		MinVersion:   tls.VersionTLS12,
	}, okBackend())

	tests := []struct {
		name       string
		policy     *config.BackendTLSConfig
		wantStatus int
	}{
		{
			name:       "no policy cannot verify a private CA",
			policy:     nil,
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "private CA with the right name",
			policy:     &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "private CA with the wrong name",
			policy:     &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "wrong.internal"},
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "system roots do not trust a private CA",
			policy:     &config.BackendTLSConfig{ServerName: "inventory.internal", MinVersion: "1.2"},
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "system_and_file accepts the private CA",
			policy:     &config.BackendTLSConfig{CAMode: "system_and_file", CAFile: ca.caPath, ServerName: "inventory.internal"},
			wantStatus: http.StatusOK,
		},
		{
			name: "matching dns peer identity",
			policy: &config.BackendTLSConfig{
				CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal",
				PeerIdentities: []string{"dns:inventory.internal"},
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "matching uri peer identity",
			policy: &config.BackendTLSConfig{
				CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal",
				PeerIdentities: []string{"uri:spiffe://example/inventory"},
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "non-matching peer identity fails after a valid chain",
			policy: &config.BackendTLSConfig{
				CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal",
				PeerIdentities: []string{"dns:someone.else"},
			},
			wantStatus: http.StatusBadGateway,
		},
		{
			name: "peer identity does not substitute for chain verification",
			policy: &config.BackendTLSConfig{
				ServerName: "inventory.internal", PeerIdentities: []string{"dns:inventory.internal"},
			},
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "insecure bypass reaches the backend",
			policy:     &config.BackendTLSConfig{InsecureSkipVerify: true, ServerName: "anything.invalid"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "tls 1.3 floor is satisfied by the backend",
			policy:     &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal", MinVersion: "1.3"},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc := config.LocationConfig{ProxyPass: backend.URL, BackendTLS: tt.policy}
			code, body := serveThrough(t, loc, nil)
			if code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", code, tt.wantStatus, body)
			}
			if tt.wantStatus == http.StatusOK && !strings.Contains(body, "verified backend") {
				t.Fatalf("body = %q, want the backend response", body)
			}
		})
	}
}

// TestProxyBackendTLSNamedUpstreamMatchesDirectTarget proves the equivalence the
// issue requires: the same policy behaves identically whether it is declared on
// a pool or on a literal target.
func TestProxyBackendTLSNamedUpstreamMatchesDirectTarget(t *testing.T) {
	ca := newBackendPKI(t)
	backend := tlsBackend(t, &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, "backend", []string{"inventory.internal"}, nil)},
		MinVersion:   tls.VersionTLS12,
	}, okBackend())
	host := strings.TrimPrefix(backend.URL, "https://")

	policy := &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal"}

	t.Run("literal target", func(t *testing.T) {
		code, _ := serveThrough(t, config.LocationConfig{ProxyPass: backend.URL, BackendTLS: policy}, nil)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
	})

	t.Run("named upstream", func(t *testing.T) {
		ups := map[string]config.UpstreamConfig{"inventory": {
			Name:       "inventory",
			Servers:    []config.UpstreamServer{{Address: host, Weight: 1}},
			BackendTLS: policy,
		}}
		code, _ := serveThrough(t, config.LocationConfig{ProxyPass: "https://inventory"}, ups)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
	})

	t.Run("location policy overrides the pool", func(t *testing.T) {
		ups := map[string]config.UpstreamConfig{"inventory": {
			Name:    "inventory",
			Servers: []config.UpstreamServer{{Address: host, Weight: 1}},
			// The pool's policy would fail: wrong verified name.
			BackendTLS: &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "wrong.internal"},
		}}
		loc := config.LocationConfig{ProxyPass: "https://inventory", BackendTLS: policy}
		if code, _ := serveThrough(t, loc, ups); code != http.StatusOK {
			t.Fatalf("status = %d, want 200: the location's policy must win", code)
		}
	})
}

// TestProxyBackendTLSVerifiesTheLogicalNameNotTheDialledAddress is the
// discovery-poisoning guard: the pool's selected address is a dial destination,
// while the configured logical name stays the verified identity.
func TestProxyBackendTLSVerifiesTheLogicalNameNotTheDialledAddress(t *testing.T) {
	ca := newBackendPKI(t)
	// The certificate carries only the logical name — no IP SAN for the address
	// the pool will actually dial.
	leaf := ca.issue(t, "backend", []string{"inventory.internal"}, nil)
	leaf.Leaf = nil
	backend := httptest.NewUnstartedServer(okBackend())
	backend.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12}
	backend.StartTLS()
	defer backend.Close()
	host := strings.TrimPrefix(backend.URL, "https://")

	ups := map[string]config.UpstreamConfig{"inventory": {
		Name:    "inventory",
		Servers: []config.UpstreamServer{{Address: host, Weight: 1}},
		BackendTLS: &config.BackendTLSConfig{
			CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal",
		},
	}}

	if code, body := serveThrough(t, config.LocationConfig{ProxyPass: "https://inventory"}, ups); code != http.StatusOK {
		t.Fatalf("status = %d, want 200: the configured name must be verified, not the dialled address (%s)", code, body)
	}

	// Without the override, the address is verified instead — and fails,
	// because the certificate names the service, not the socket.
	upsNoName := map[string]config.UpstreamConfig{"inventory": {
		Name:       "inventory",
		Servers:    []config.UpstreamServer{{Address: host, Weight: 1}},
		BackendTLS: &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory"},
	}}
	if code, _ := serveThrough(t, config.LocationConfig{ProxyPass: "https://inventory"}, upsNoName); code == http.StatusOK {
		t.Fatal("a certificate that does not name the verified identity must not be accepted")
	}
}

// TestProxyBackendMutualTLS covers the client-certificate path against a
// backend that demands one.
func TestProxyBackendMutualTLS(t *testing.T) {
	ca := newBackendPKI(t)
	clientPool := x509.NewCertPool()
	clientPool.AddCert(ca.cert)
	backend := tlsBackend(t, &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, "backend", []string{"inventory.internal"}, nil)},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientPool,
		MinVersion:   tls.VersionTLS12,
	}, okBackend())

	certPath, keyPath := writePair(t, t.TempDir(), ca.issue(t, "jul-client", []string{"jul.client"}, nil))

	t.Run("with a client certificate", func(t *testing.T) {
		loc := config.LocationConfig{ProxyPass: backend.URL, BackendTLS: &config.BackendTLSConfig{
			CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal",
			ClientCert: certPath, ClientKey: keyPath,
		}}
		if code, body := serveThrough(t, loc, nil); code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", code, body)
		}
	})

	t.Run("without one the backend refuses", func(t *testing.T) {
		loc := config.LocationConfig{ProxyPass: backend.URL, BackendTLS: &config.BackendTLSConfig{
			CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal",
		}}
		if code, _ := serveThrough(t, loc, nil); code == http.StatusOK {
			t.Fatal("the proxy reached a client-authenticating backend with no certificate")
		}
	})
}

// TestProxyBackendTLSUnreadableMaterialFailsTheBuild proves the failure lands at
// preparation time, so a bad certificate aborts a reload instead of failing the
// first request.
func TestProxyBackendTLSUnreadableMaterialFailsTheBuild(t *testing.T) {
	loc := config.LocationConfig{
		ProxyPass:  "https://inventory.internal:8443",
		BackendTLS: &config.BackendTLSConfig{CAMode: "file_only", CAFile: filepath.Join(t.TempDir(), "absent.pem")},
	}
	if _, err := NewProxy(t.Context(), config.ServerConfig{}, loc, nil, nil, nil, nil); err == nil {
		t.Fatal("NewProxy accepted unreadable trust material")
	}
}

// TestProxyTransportIsolatedPerGeneration proves that two handler generations
// hold independent connection pools, which is what stops a request admitted
// after a policy change from reusing a connection established under the old
// trust.
func TestProxyTransportIsolatedPerGeneration(t *testing.T) {
	ca := newBackendPKI(t)
	backend := tlsBackend(t, &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, "backend", []string{"inventory.internal"}, nil)},
		MinVersion:   tls.VersionTLS12,
	}, okBackend())

	build := func(policy *config.BackendTLSConfig) http.Handler {
		h, err := NewProxy(t.Context(), config.ServerConfig{}, config.LocationConfig{
			ProxyPass: backend.URL, BackendTLS: policy,
		}, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewProxy: %v", err)
		}
		return h
	}

	trusting := build(&config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal"})
	first, ok := trusting.(*proxyHandler)
	if !ok {
		t.Fatalf("proxy handler = %T, want one that owns its transport", trusting)
	}

	// Warm a keep-alive connection under the trusting policy.
	rec := httptest.NewRecorder()
	trusting.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge.example/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("warm-up status = %d, want 200", rec.Code)
	}

	// A new generation with a stricter policy must not inherit that connection.
	strict := build(&config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "wrong.internal"})
	second := strict.(*proxyHandler)
	if first.transport == second.transport {
		t.Fatal("two generations shared one transport, so they would share pooled connections")
	}
	rec = httptest.NewRecorder()
	strict.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge.example/", nil))
	if rec.Code == http.StatusOK {
		t.Fatal("the stricter generation reused a connection verified under the old policy")
	}

	// Retiring the old generation closes its idle connections exactly once and
	// must be safe to call repeatedly.
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestProxyRefusesToDowngradeToPlaintext pins the no-downgrade rule: a route
// configured for https never dials a plaintext backend, whatever the pool says.
func TestProxyRefusesToDowngradeToPlaintext(t *testing.T) {
	plaintext := httptest.NewServer(okBackend())
	defer plaintext.Close()
	host := strings.TrimPrefix(plaintext.URL, "http://")

	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "inventory",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: host, Weight: 1}},
	}, "http")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	tr := &balancingTransport{pool: pool, base: newProxyTransport(config.LocationConfig{}, nil, 0), tlsBackend: true}

	req := httptest.NewRequest(http.MethodGet, "https://inventory/", nil)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("a TLS route dialled a plaintext backend")
	} else if !strings.Contains(err.Error(), "refusing to downgrade") {
		t.Fatalf("err = %v, want a downgrade refusal", err)
	}
}

func TestTLSFailureCategory(t *testing.T) {
	ca := newBackendPKI(t)
	backend := tlsBackend(t, &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, "backend", []string{"inventory.internal"}, nil)},
		MinVersion:   tls.VersionTLS12,
	}, okBackend())

	dial := func(cfg *tls.Config) error {
		conn, err := tls.Dial("tcp", strings.TrimPrefix(backend.URL, "https://"), cfg)
		if err != nil {
			return err
		}
		_ = conn.Close()
		return nil
	}

	t.Run("unknown authority", func(t *testing.T) {
		err := dial(&tls.Config{ServerName: "inventory.internal", MinVersion: tls.VersionTLS12})
		if got := tlsFailureCategory(err); got != "unknown_authority" {
			t.Fatalf("category = %q for %v", got, err)
		}
	})

	t.Run("hostname mismatch", func(t *testing.T) {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(ca.caPEM)
		err := dial(&tls.Config{ServerName: "wrong.internal", RootCAs: pool, MinVersion: tls.VersionTLS12})
		if got := tlsFailureCategory(err); got != "hostname_mismatch" {
			t.Fatalf("category = %q for %v", got, err)
		}
	})

	t.Run("bounded and secret-free", func(t *testing.T) {
		// Every category is a fixed identifier: nothing derived from the error
		// text, the host or the certificate may appear.
		allowed := map[string]bool{
			"unknown_authority": true, "hostname_mismatch": true, "certificate_expired": true,
			"certificate_invalid": true, "peer_identity_mismatch": true, "client_certificate": true,
			"tls_version": true, "tls_handshake": true, "tls_other": true,
		}
		for _, err := range []error{
			nil,
			io.EOF,
			net.ErrClosed,
		} {
			if got := tlsFailureCategory(err); got != "" && !allowed[got] {
				t.Fatalf("category %q is outside the closed set", got)
			}
		}
	})
}

// TestProxyBackendTLSWebSocketUsesTheSamePolicy proves an upgraded connection
// is not a bypass: the handshake that carries a WebSocket is verified by the
// same policy as an ordinary request, and refused when the policy does not fit.
func TestProxyBackendTLSWebSocketUsesTheSamePolicy(t *testing.T) {
	ca := newBackendPKI(t)
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "not an upgrade", http.StatusBadRequest)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_, _ = buf.WriteString("frame-from-backend")
		_ = buf.Flush()
	})
	backend := tlsBackend(t, &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, "backend", []string{"inventory.internal"}, nil)},
		MinVersion:   tls.VersionTLS12,
	}, echo)

	upgrade := func(policy *config.BackendTLSConfig) (int, string) {
		h, err := NewProxy(t.Context(), config.ServerConfig{}, config.LocationConfig{
			ProxyPass: backend.URL, BackendTLS: policy,
		}, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewProxy: %v", err)
		}
		edge := httptest.NewServer(h)
		defer edge.Close()

		req, err := http.NewRequest(http.MethodGet, edge.URL+"/", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("upgrade request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		// On a 101 the body is the spliced connection; on an error it is the
		// gateway's message.
		body := make([]byte, 128)
		n, _ := resp.Body.Read(body)
		return resp.StatusCode, string(body[:n])
	}

	t.Run("verified policy upgrades", func(t *testing.T) {
		code, response := upgrade(&config.BackendTLSConfig{
			CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal",
		})
		if code != http.StatusSwitchingProtocols {
			t.Fatalf("status = %d, want 101: %q", code, response)
		}
		if !strings.Contains(response, "frame-from-backend") {
			t.Fatalf("the upgraded stream did not carry backend data: %q", response)
		}
	})

	t.Run("an upgrade is not a verification bypass", func(t *testing.T) {
		code, response := upgrade(&config.BackendTLSConfig{
			CAMode: "file_only", CAFile: ca.caPath, ServerName: "wrong.internal",
		})
		if code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: an upgrade must not skip verification: %q", code, response)
		}
	})
}
