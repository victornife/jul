// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/backendtls"
	"jul/internal/config"
)

// probePKI issues a private-CA certificate for a probe backend.
type probePKI struct {
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	caPEM  []byte
	caPath string
}

func newProbePKI(t *testing.T) *probePKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "probe-ca"},
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
	p := &probePKI{cert: cert, key: key, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
	p.caPath = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p.caPath, p.caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *probePKI) issue(t *testing.T, dnsNames []string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 1),
		Subject:      pkix.Name{CommonName: "probe-backend"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
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

// TestProbeUsesTheSameTrustAsLiveTraffic is the ADR 0016 §9 contract: a backend
// is never reported healthy under weaker verification than the requests Jul
// sends it — and, just as importantly, a backend that live traffic can verify
// is not reported unhealthy because the probe could not.
func TestProbeUsesTheSameTrustAsLiveTraffic(t *testing.T) {
	ca := newProbePKI(t)
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	backend.TLS = &tls.Config{Certificates: []tls.Certificate{ca.issue(t, []string{"probe.internal"})}, MinVersion: tls.VersionTLS12}
	backend.StartTLS()
	defer backend.Close()
	addr := backend.Listener.Addr().String()

	probe := func(policy *backendtls.Policy) bool {
		pool, err := NewPool(config.UpstreamConfig{
			Name:     "probed",
			Servers:  []config.UpstreamServer{{Address: addr, Weight: 1}},
			MaxFails: 1,
		}, "https")
		if err != nil {
			t.Fatalf("NewPool: %v", err)
		}
		defer pool.Close()
		hc := &healthChecker{
			pool:   pool,
			params: healthParamsFrom(config.HealthCheckConfig{Enabled: true, Type: "http", Path: "/healthz"}),
			states: map[*Backend]*probeState{},
			client: &http.Client{Timeout: 2 * time.Second, Transport: probeTransport(2*time.Second, policy)},
		}
		b := pool.Backends()[0]
		return hc.probeHTTP(t.Context(), b)
	}

	t.Run("no policy cannot verify a private CA", func(t *testing.T) {
		if probe(nil) {
			t.Fatal("a probe with no policy verified a private-CA backend")
		}
	})

	t.Run("the pool's policy makes the backend probeable", func(t *testing.T) {
		policy, err := backendtls.Resolve(backendtls.Options{
			CAMode: backendtls.CAModeFileOnly, CAFile: ca.caPath, ServerName: "probe.internal",
		}, "probed")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if !probe(policy) {
			t.Fatal("a backend live traffic can verify was reported unprobeable")
		}
	})

	t.Run("a policy that names the wrong identity still fails", func(t *testing.T) {
		policy, err := backendtls.Resolve(backendtls.Options{
			CAMode: backendtls.CAModeFileOnly, CAFile: ca.caPath, ServerName: "wrong.internal",
		}, "probed")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if probe(policy) {
			t.Fatal("the probe accepted a backend whose certificate names something else")
		}
	})
}

// TestProbeAndLiveTrafficFailIdentically closes the direction the other parity
// tests leave open.
//
// Proving that both succeed against a good certificate does not prove they
// share trust: two independently built configurations would also both succeed.
// The *failing* case is what distinguishes them, because that is where a
// divergence shows up first — and it is the case that matters, since a probe
// which accepts what live traffic rejects is exactly the "healthy under weaker
// verification" state ADR 0016 §9 forbids.
func TestProbeAndLiveTrafficFailIdentically(t *testing.T) {
	ca := newProbePKI(t)
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	backend.TLS = &tls.Config{Certificates: []tls.Certificate{ca.issue(t, []string{"probe.internal"})}, MinVersion: tls.VersionTLS12}
	backend.StartTLS()
	defer backend.Close()
	addr := backend.Listener.Addr().String()

	probeAccepts := func(policy *backendtls.Policy) bool {
		pool, err := NewPool(config.UpstreamConfig{
			Name:     "probed",
			Servers:  []config.UpstreamServer{{Address: addr, Weight: 1}},
			MaxFails: 1,
		}, "https")
		if err != nil {
			t.Fatalf("NewPool: %v", err)
		}
		defer pool.Close()
		hc := &healthChecker{
			pool:   pool,
			params: healthParamsFrom(config.HealthCheckConfig{Enabled: true, Type: "http", Path: "/healthz"}),
			states: map[*Backend]*probeState{},
			client: &http.Client{Timeout: 2 * time.Second, Transport: probeTransport(2*time.Second, policy)},
		}
		return hc.probeHTTP(t.Context(), pool.Backends()[0])
	}

	// The same resolved policy a route's transport would carry.
	liveAccepts := func(policy *backendtls.Policy) bool {
		client := &http.Client{
			Timeout:   2 * time.Second,
			Transport: &http.Transport{TLSClientConfig: policy.ClientConfig()},
		}
		resp, err := client.Get("https://" + addr + "/")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	}

	resolve := func(serverName string) *backendtls.Policy {
		t.Helper()
		p, err := backendtls.Resolve(backendtls.Options{
			CAMode: backendtls.CAModeFileOnly, CAFile: ca.caPath, ServerName: serverName,
		}, "probed")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		return p
	}

	for _, tt := range []struct {
		name       string
		serverName string
		want       bool
	}{
		{name: "the identity the certificate carries", serverName: "probe.internal", want: true},
		{name: "an identity it does not carry", serverName: "wrong.internal", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			policy := resolve(tt.serverName)
			probe, live := probeAccepts(policy), liveAccepts(policy)
			if probe != live {
				t.Fatalf("probe accepted = %v but live traffic accepted = %v; health and live traffic disagree about trust", probe, live)
			}
			if probe != tt.want {
				t.Fatalf("both accepted = %v, want %v", probe, tt.want)
			}
		})
	}
}

// TestPoolIdentityIncludesTheBackendTLSPolicy proves the mechanism that makes
// the field hot-reloadable: a changed policy — including a certificate rotated
// in place — changes the pool's identity, so the pool and its probe client are
// rebuilt rather than reused.
func TestPoolIdentityIncludesTheBackendTLSPolicy(t *testing.T) {
	ca := newProbePKI(t)
	base := config.UpstreamConfig{
		Name:    "probed",
		Servers: []config.UpstreamServer{{Address: "127.0.0.1:9443", Weight: 1}},
		BackendTLS: &config.BackendTLSConfig{
			CAMode: "file_only", CAFile: ca.caPath, ServerName: "probe.internal",
		},
	}

	same := metaOf(base, "https")
	if !same.equal(metaOf(base, "https")) {
		t.Fatal("an unchanged policy must keep the pool identity stable")
	}

	changed := base
	renamed := *base.BackendTLS
	renamed.ServerName = "moved.internal"
	changed.BackendTLS = &renamed
	if same.equal(metaOf(changed, "https")) {
		t.Fatal("changing the verified name must rebuild the pool")
	}

	// Rotate the CA file in place, leaving the configured path untouched: the
	// fingerprint digests content, so the pool identity changes.
	rotated := newProbePKI(t)
	if err := os.WriteFile(ca.caPath, rotated.caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if same.equal(metaOf(base, "https")) {
		t.Fatal("an in-place certificate rotation must rebuild the pool")
	}

	// A pool with no policy is unaffected.
	noPolicy := base
	noPolicy.BackendTLS = nil
	if !metaOf(noPolicy, "https").equal(metaOf(noPolicy, "https")) {
		t.Fatal("a pool without a policy must keep a stable identity")
	}
}

// TestRegistryRejectsUnresolvablePolicy proves a malformed policy fails the
// staged build, so the reload aborts instead of a backend turning unhealthy.
func TestRegistryRejectsUnresolvablePolicy(t *testing.T) {
	reg := NewRegistry(RegistryOptions{DialContext: (&net.Dialer{}).DialContext})
	defer reg.CloseAll()

	_, err := reg.For(t.Context(), config.UpstreamConfig{
		Name:       "broken",
		Servers:    []config.UpstreamServer{{Address: "127.0.0.1:9443", Weight: 1}},
		BackendTLS: &config.BackendTLSConfig{CAMode: "file_only", CAFile: filepath.Join(t.TempDir(), "absent.pem")},
	}, "https")
	if err == nil {
		t.Fatal("the registry staged a pool with unreadable trust material")
	}
}

// TestPrivateCABackendIsBothReachableAndHealthy is the lane's closing
// assertion: with one pool-level policy, the same private-CA backend both
// serves live traffic and passes its active health probe. Before this tranche a
// deployment could have one but not the other.
func TestPrivateCABackendIsBothReachableAndHealthy(t *testing.T) {
	ca := newProbePKI(t)
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	backend.TLS = &tls.Config{Certificates: []tls.Certificate{ca.issue(t, []string{"probe.internal"})}, MinVersion: tls.VersionTLS12}
	backend.StartTLS()
	defer backend.Close()
	addr := backend.Listener.Addr().String()

	up := config.UpstreamConfig{
		Name:     "probed",
		Servers:  []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails: 1,
		HealthCheck: &config.HealthCheckConfig{
			Enabled: true, Type: "http", Path: "/healthz",
			Interval: config.Duration(20 * time.Millisecond),
			Timeout:  config.Duration(2 * time.Second),
			// One successful probe is enough for the transition, so the test
			// asserts on the probe's verdict rather than on wall-clock timing.
			HealthyThreshold: 1, UnhealthyThreshold: 1,
		},
		BackendTLS: &config.BackendTLSConfig{
			CAMode: "file_only", CAFile: ca.caPath, ServerName: "probe.internal",
		},
	}

	// The probe verdict is observed through the registry's own hook, which is
	// the same seam metrics use, rather than by poking backend state.
	probes := make(chan bool, 8)
	reg := NewRegistry(RegistryOptions{
		DialContext: (&net.Dialer{}).DialContext,
		OnProbe: func(_, _ string, ok bool, _ time.Duration) {
			select {
			case probes <- ok:
			default:
			}
		},
	})
	defer reg.CloseAll()

	if _, err := reg.For(t.Context(), up, "https"); err != nil {
		t.Fatalf("registry.For: %v", err)
	}
	reg.Commit()
	reg.Activate()

	// The live traffic path: a request through the same policy must verify.
	policy, err := backendtls.Resolve(up.BackendTLS.Options(), up.Name)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: probeTransport(3*time.Second, policy)}
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("live traffic to a private-CA backend failed: %v", err)
	}
	_ = resp.Body.Close()

	// The health path must reach the same verdict. Jitter means the first probe
	// can be a transient failure, so we keep waiting until we observe a
	// successful probe or time out.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ok := <-probes:
			if ok {
				return
			}
		case <-deadline:
			t.Fatal("no successful health probe was observed for a private-CA backend that live traffic verifies")
		}
	}
}
