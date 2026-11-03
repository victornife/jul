// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"jul/internal/admin"
	"jul/internal/background"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/waf"
)

func writeTempHTPasswd(t *testing.T, dir, user, pass string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	p := filepath.Join(dir, "htpasswd")
	if err := os.WriteFile(p, []byte(user+":"+string(hash)+"\n"), 0o600); err != nil {
		t.Fatalf("write htpasswd: %v", err)
	}
	return p
}

var (
	executedIsolationMu      sync.Mutex
	executedIsolationResults = map[string]SecurityIsolationCaseResult{}
)

func recordExecutedIsolationCase(caseID, property, status, boundary string) {
	executedIsolationMu.Lock()
	defer executedIsolationMu.Unlock()
	executedIsolationResults[caseID] = SecurityIsolationCaseResult{
		CaseID:           caseID,
		Property:         property,
		Status:           status,
		ObservedBoundary: boundary,
	}
}

func generateTestPKI(t *testing.T, dir string) (caFile, srvCertFile, srvKeyFile string, caPool *x509.CertPool, clientCert tls.Certificate, wrongCert tls.Certificate) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa ca: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Jul Isolation Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create ca: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caFile = filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	caPool = x509.NewCertPool()
	caPool.AddCert(caCert)

	makeLeaf := func(cn string, dns []string, ips []net.IP, parent *x509.Certificate, parentKey *rsa.PrivateKey, eku x509.ExtKeyUsage) ([]byte, []byte, tls.Certificate) {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("rsa leaf: %v", err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: cn},
			DNSNames:     dns,
			IPAddresses:  ips,
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		}
		signer := parent
		signerKey := parentKey
		if signer == nil {
			signer = tmpl
			signerKey = k
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &k.PublicKey, signerKey)
		if err != nil {
			t.Fatalf("create leaf: %v", err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatalf("keypair: %v", err)
		}
		return certPEM, keyPEM, pair
	}

	sCert, sKey, _ := makeLeaf("app.example.test", []string{"app.example.test", "backend.internal"}, []net.IP{net.ParseIP("127.0.0.1")}, caCert, caKey, x509.ExtKeyUsageServerAuth)
	srvCertFile = filepath.Join(dir, "srv.pem")
	srvKeyFile = filepath.Join(dir, "srv.key")
	if err := os.WriteFile(srvCertFile, sCert, 0o600); err != nil {
		t.Fatalf("write srv cert: %v", err)
	}
	if err := os.WriteFile(srvKeyFile, sKey, 0o600); err != nil {
		t.Fatalf("write srv key: %v", err)
	}

	_, _, clientCert = makeLeaf("alice", []string{"alice.client.test"}, nil, caCert, caKey, x509.ExtKeyUsageClientAuth)
	_, _, wrongCert = makeLeaf("untrusted", []string{"untrusted.client.test"}, nil, nil, nil, x509.ExtKeyUsageClientAuth)
	return
}

// TestSecurityIsolationMatrixManifestValid verifies the committed
// docs/security-isolation-matrix.json manifest and exercises negative
// validation and report-verification failure cases (#634).
func TestSecurityIsolationMatrixManifestValid(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "docs", "security-isolation-matrix.json")
	m, err := LoadSecurityIsolationManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadSecurityIsolationManifest(%s): %v", manifestPath, err)
	}
	if len(m.Cases) < 8 {
		t.Fatalf("len(m.Cases) = %d, want >= 8", len(m.Cases))
	}

	// Build a valid synthetic report matching the manifest and verify it passes.
	results := make([]SecurityIsolationCaseResult, 0, len(m.Cases))
	for _, c := range m.Cases {
		results = append(results, SecurityIsolationCaseResult{
			CaseID:           c.ID,
			Property:         c.Property,
			Status:           "pass",
			ObservedBoundary: c.ExpectedBoundary,
		})
	}
	rep := &SecurityIsolationReport{
		SchemaVersion: SecurityIsolationMatrixVersion,
		MatrixID:      m.MatrixID,
		SourceSHA:     "70277d9886131b3e88927b150eb43af97ad7c312",
		BuildProfile:  "full",
		WAFCompiled:   waf.Compiled,
		Results:       results,
	}
	if err := VerifySecurityIsolationReport(m, rep, "70277d9886131b3e88927b150eb43af97ad7c312"); err != nil {
		t.Fatalf("VerifySecurityIsolationReport valid: %v", err)
	}

	// Lean build profile allows full-only cases to report "unavailable" honestly.
	leanRep := *rep
	leanRep.BuildProfile = "lean"
	leanResults := append([]SecurityIsolationCaseResult(nil), results...)
	for i, c := range m.Cases {
		if c.BuildProfile == "full" {
			leanResults[i].Status = "unavailable"
		}
	}
	leanRep.Results = leanResults
	if err := VerifySecurityIsolationReport(m, &leanRep, "70277d9886131b3e88927b150eb43af97ad7c312"); err != nil {
		t.Fatalf("VerifySecurityIsolationReport lean unavailable: %v", err)
	}

	// Negative test: even an unavailable result must match the declared expected_boundary.
	wrongBoundary := append([]SecurityIsolationCaseResult(nil), leanResults...)
	wrongBoundary[len(wrongBoundary)-1].ObservedBoundary = "wrong_boundary_name"
	badLeanRep := leanRep
	badLeanRep.Results = wrongBoundary
	if err := VerifySecurityIsolationReport(m, &badLeanRep, "70277d9886131b3e88927b150eb43af97ad7c312"); err == nil {
		t.Fatal("expected error when unavailable result has mismatched observed_boundary")
	}

	// Negative manifest cases: missing file, unknown field, trailing data, invalid fields.
	if _, err := LoadSecurityIsolationManifest(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("expected error loading missing manifest file")
	}
	if _, err := ParseSecurityIsolationManifest([]byte(`{"schema_version":1,"unknown_field":true}`)); err == nil {
		t.Error("expected error for unknown field in manifest")
	}
	if _, err := ParseSecurityIsolationManifest([]byte(`{"schema_version":1}{"extra":1}`)); err == nil {
		t.Error("expected error for trailing JSON data")
	}
	if _, err := ParseSecurityIsolationManifest([]byte(`{"schema_version":99}`)); err == nil {
		t.Error("expected error from ParseSecurityIsolationManifest when ValidateSecurityIsolationManifest fails")
	}

	badManifests := []func(*SecurityIsolationManifest){
		func(x *SecurityIsolationManifest) { x.SchemaVersion = 99 },
		func(x *SecurityIsolationManifest) { x.MatrixID = "" },
		func(x *SecurityIsolationManifest) { x.Owner = "" },
		func(x *SecurityIsolationManifest) { x.RevalidationTriggers = nil },
		func(x *SecurityIsolationManifest) { x.Cases = nil },
		func(x *SecurityIsolationManifest) { x.Cases[0].ID = "" },
		func(x *SecurityIsolationManifest) { x.Cases[1].ID = x.Cases[0].ID },
		func(x *SecurityIsolationManifest) { x.Cases[0].Property = "invalid_prop" },
		func(x *SecurityIsolationManifest) { x.Cases[0].Scenario = "" },
		func(x *SecurityIsolationManifest) { x.Cases[0].RouteScope = "" },
		func(x *SecurityIsolationManifest) { x.Cases[0].Protocols = nil },
		func(x *SecurityIsolationManifest) { x.Cases[0].BuildProfile = "invalid" },
		func(x *SecurityIsolationManifest) { x.Cases[0].ExpectedBoundary = "" },
		func(x *SecurityIsolationManifest) { x.Cases[0].OwnerTest = "no_test_func" },
		func(x *SecurityIsolationManifest) { x.Cases[0].Limitations = "" },
		func(x *SecurityIsolationManifest) {
			// Remove all cases of one required property.
			var filtered []SecurityIsolationCase
			for _, c := range x.Cases {
				if c.Property != "cache_isolation" {
					filtered = append(filtered, c)
				}
			}
			x.Cases = filtered
		},
	}
	for i, mutate := range badManifests {
		cp := *m
		cp.Cases = append([]SecurityIsolationCase(nil), m.Cases...)
		mutate(&cp)
		if err := ValidateSecurityIsolationManifest(&cp); err == nil {
			t.Errorf("badManifests[%d] unexpectedly passed validation", i)
		}
	}

	// Negative report verification cases: nil, stale SHA, missing case, skipped required case, duplicate result.
	if err := VerifySecurityIsolationReport(m, nil, ""); err == nil {
		t.Error("expected error for nil report")
	}
	badReports := []func(*SecurityIsolationReport){
		func(r *SecurityIsolationReport) { r.SchemaVersion = 2 },
		func(r *SecurityIsolationReport) { r.MatrixID = "wrong" },
		func(r *SecurityIsolationReport) { r.SourceSHA = "" },
		func(r *SecurityIsolationReport) { r.SourceSHA = "stale-sha" },
		func(r *SecurityIsolationReport) { r.BuildProfile = "unknown" },
		func(r *SecurityIsolationReport) { r.Results = nil },
		func(r *SecurityIsolationReport) { r.Results[0].Status = "invalid_status" },
		func(r *SecurityIsolationReport) { r.Results = append(r.Results, r.Results[0]) },
		func(r *SecurityIsolationReport) { r.Results = r.Results[1:] },
		func(r *SecurityIsolationReport) { r.Results[0].Property = "wrong_prop" },
		func(r *SecurityIsolationReport) { r.Results[0].ObservedBoundary = "" },
		func(r *SecurityIsolationReport) { r.Results[0].Status = "skipped" },
		func(r *SecurityIsolationReport) { r.Results[0].Status = "fail" },
	}
	for i, mutate := range badReports {
		cp := *rep
		cp.Results = append([]SecurityIsolationCaseResult(nil), rep.Results...)
		mutate(&cp)
		if err := VerifySecurityIsolationReport(m, &cp, "70277d9886131b3e88927b150eb43af97ad7c312"); err == nil {
			t.Errorf("badReports[%d] unexpectedly passed verification", i)
		}
	}
}

// TestSecurityIsolationPublicRestrictedAdminAndIdentity verifies ISO-ROUTE-001,
// ISO-ROUTE-002, ISO-TRUST-001, and ISO-TRUST-002 using real handlers, origin
// counters, and TLS/client-address boundaries (#634).
func TestSecurityIsolationPublicRestrictedAdminAndIdentity(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	htpasswdPath := writeTempHTPasswd(t, tmpDir, "family", "correct-horse-battery")

	publicRoot := filepath.Join(tmpDir, "public")
	if err := os.MkdirAll(publicRoot, 0o750); err != nil {
		t.Fatalf("mkdir public: %v", err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, "index.html"), []byte("public-portal-welcome"), 0o600); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	var (
		restrictedHits atomic.Int64
		cidrOnlyHits   atomic.Int64
		lastForwarded  atomic.Value
	)
	restrictedOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		restrictedHits.Add(1)
		if r.URL.Path == "/dashboard/origin-forbidden" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("origin-generated-403"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("restricted-dashboard-secret"))
	}))
	defer restrictedOrigin.Close()

	cidrOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cidrOnlyHits.Add(1)
		lastForwarded.Store(r.Header.Get("X-Forwarded-For"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("cidr-protected-ok"))
	}))
	defer cidrOrigin.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamConfig{
			{
				Name:     "dash",
				Strategy: "round_robin",
				Servers:  []config.UpstreamServer{{Address: restrictedOrigin.Listener.Addr().String(), Weight: 1}},
			},
			{
				Name:     "cidr_app",
				Strategy: "round_robin",
				Servers:  []config.UpstreamServer{{Address: cidrOrigin.Listener.Addr().String(), Weight: 1}},
			},
		},
		Servers: []config.ServerConfig{
			{
				Name:        "public-portal",
				Listen:      ":8080",
				ServerNames: []string{"portal.example.test"},
				ClientAddress: &config.ClientAddressConfig{
					TrustedProxies:   []string{"10.0.0.0/8"},
					ForwardedHeaders: []string{"X-Forwarded-For"},
					MaxHops:          1,
				},
				Locations: []config.LocationConfig{
					{
						RouteID: config.String("portal-home"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/"},
						Root:    publicRoot,
						Sharing: &config.SharingConfig{Mode: "public"},
					},
				},
			},
			{
				Name:        "restricted-app",
				Listen:      ":8080",
				ServerNames: []string{"app.example.test"},
				ClientAddress: &config.ClientAddressConfig{
					TrustedProxies:   []string{"10.0.0.0/8"},
					ForwardedHeaders: []string{"X-Forwarded-For"},
					MaxHops:          1,
				},
				Locations: []config.LocationConfig{
					{
						RouteID:   config.String("app-dashboard-post-admin"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/dashboard/admin", Methods: []string{"POST"}},
						ProxyPass: "http://dash",
						Auth: &config.AuthConfig{
							Basic: &config.BasicAuthConfig{File: htpasswdPath, Realm: "Dash"},
						},
					},
					{
						RouteID:   config.String("app-dashboard"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/dashboard/"},
						ProxyPass: "http://dash",
						Sharing:   &config.SharingConfig{Mode: "restricted"},
						Auth: &config.AuthConfig{
							Basic: &config.BasicAuthConfig{File: htpasswdPath, Realm: "Dash"},
						},
					},
					{
						RouteID:   config.String("app-cidr"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/cidr-only/"},
						ProxyPass: "http://cidr_app",
						Auth: &config.AuthConfig{
							Allow: []string{"203.0.113.10/32"},
						},
					},
					{
						RouteID: config.String("app-default-deny"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/"},
						Deny:    true,
					},
				},
			},
		},
	}

	factory, cleanup := minimalFactory(t)
	defer cleanup()
	handlers, _, commit, _, err := factory.Prepare(ctx, cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, retire := commit()
	defer retire()
	untrustedHandler := handlers[":8080"]

	t.Run("public_vs_restricted_and_admin", func(t *testing.T) {
		// 1. Public portal succeeds without credentials and never hits restricted origin.
		req := httptest.NewRequest(http.MethodGet, "http://portal.example.test/index.html", nil)
		req.RemoteAddr = "198.51.100.25:54321"
		rr := httptest.NewRecorder()
		untrustedHandler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "public-portal-welcome") {
			t.Fatalf("public portal = %d %q", rr.Code, rr.Body.String())
		}
		if restrictedHits.Load() != 0 {
			t.Fatalf("restrictedHits after public request = %d, want 0", restrictedHits.Load())
		}

		// 2. Anonymous request to restricted dashboard is denied before origin (0 origin hits).
		req = httptest.NewRequest(http.MethodGet, "http://app.example.test/dashboard/index", nil)
		req.RemoteAddr = "198.51.100.25:54321"
		rr = httptest.NewRecorder()
		untrustedHandler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous dashboard status = %d, want 401", rr.Code)
		}
		if restrictedHits.Load() != 0 {
			t.Fatalf("restrictedHits after anonymous 401 = %d, want 0", restrictedHits.Load())
		}

		// 3. Authenticated request to restricted dashboard reaches origin (+1 hit).
		req = httptest.NewRequest(http.MethodGet, "http://app.example.test/dashboard/index", nil)
		req.RemoteAddr = "198.51.100.25:54321"
		req.SetBasicAuth("family", "correct-horse-battery")
		rr = httptest.NewRecorder()
		untrustedHandler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || rr.Body.String() != "restricted-dashboard-secret" {
			t.Fatalf("authenticated dashboard = %d %q", rr.Code, rr.Body.String())
		}
		if restrictedHits.Load() != 1 {
			t.Fatalf("restrictedHits = %d, want 1", restrictedHits.Load())
		}

		// 4. Origin-generated 403 is distinguishable from gateway denial via origin counter.
		req = httptest.NewRequest(http.MethodGet, "http://app.example.test/dashboard/origin-forbidden", nil)
		req.RemoteAddr = "198.51.100.25:54321"
		req.SetBasicAuth("family", "correct-horse-battery")
		rr = httptest.NewRecorder()
		untrustedHandler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden || rr.Body.String() != "origin-generated-403" {
			t.Fatalf("origin 403 = %d %q", rr.Code, rr.Body.String())
		}
		if restrictedHits.Load() != 2 {
			t.Fatalf("restrictedHits after origin 403 = %d, want 2", restrictedHits.Load())
		}

		// 5. Private admin API refuses unauthenticated loopback (401) and non-loopback plaintext (403).
		offLoopbackAdmin := admin.New(config.AdminConfig{Enabled: true, Listen: "192.0.2.10:9090", Token: "secret-admin-token-1234567890"}, factory.Log, admin.Deps{
			LoadConfig: func() (*config.Config, error) { return cfg, nil },
		}).Handler()

		nonLoopbackReq := httptest.NewRequest(http.MethodGet, "http://192.0.2.10:9090/api/v1/status", nil)
		nonLoopbackReq.RemoteAddr = "198.51.100.25:54321"
		nonLoopbackReq.Header.Set("Authorization", "Bearer secret-admin-token-1234567890")
		nlRR := httptest.NewRecorder()
		offLoopbackAdmin.ServeHTTP(nlRR, nonLoopbackReq)
		if nlRR.Code != http.StatusForbidden {
			t.Fatalf("non-loopback plaintext admin status = %d, want 403", nlRR.Code)
		}

		loopbackAdmin := admin.New(config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: "secret-admin-token-1234567890"}, factory.Log, admin.Deps{
			LoadConfig: func() (*config.Config, error) { return cfg, nil },
		}).Handler()

		unauthAdminReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9090/api/v1/status", nil)
		unauthAdminReq.RemoteAddr = "127.0.0.1:54321"
		uaRR := httptest.NewRecorder()
		loopbackAdmin.ServeHTTP(uaRR, unauthAdminReq)
		if uaRR.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated loopback admin status = %d, want 401", uaRR.Code)
		}
		recordExecutedIsolationCase("ISO-ROUTE-001", "public_private_isolation", "pass", "denied_before_origin_on_restricted_allowed_on_public")
	})

	t.Run("alternate_host_encoded_path_and_predicates", func(t *testing.T) {
		beforeHits := restrictedHits.Load()

		// Unmatched Host falls back to first server block (portal.example.test static root),
		// never reaching app.example.test's restricted backend.
		req := httptest.NewRequest(http.MethodGet, "http://unknown.example.test/dashboard/index", nil)
		req.RemoteAddr = "198.51.100.25:54321"
		rr := httptest.NewRecorder()
		untrustedHandler.ServeHTTP(rr, req)
		if rr.Code == http.StatusOK && strings.Contains(rr.Body.String(), "restricted-dashboard-secret") {
			t.Fatal("unmatched Host unexpectedly reached restricted backend")
		}

		// Dot-segment / encoded traversal (/public/../dashboard/index) on app.example.test
		// still normalizes into /dashboard/index and requires Basic auth.
		for _, rawTarget := range []string{
			"/public/../dashboard/index",
			"/%64ashboard/index",
			"/dashboard/admin",
		} {
			req = httptest.NewRequest(http.MethodPost, "http://app.example.test"+rawTarget, nil)
			req.RemoteAddr = "198.51.100.25:54321"
			rr = httptest.NewRecorder()
			untrustedHandler.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusForbidden {
				t.Fatalf("unauthenticated %s status = %d, want 401/403", rawTarget, rr.Code)
			}
		}
		if restrictedHits.Load() != beforeHits {
			t.Fatalf("restrictedHits changed from %d to %d on rejected traversal/predicate requests", beforeHits, restrictedHits.Load())
		}
		recordExecutedIsolationCase("ISO-ROUTE-002", "public_private_isolation", "pass", "denied_before_origin_zero_origin_hits")
	})

	t.Run("forged_forwarding_and_identity_headers", func(t *testing.T) {
		beforeHits := cidrOnlyHits.Load()

		// Untrusted peer 198.51.100.99 forging X-Forwarded-For: 203.0.113.10 is denied by CIDR auth.
		req := httptest.NewRequest(http.MethodGet, "http://app.example.test/cidr-only/resource", nil)
		req.RemoteAddr = "198.51.100.99:40000"
		req.Header.Set("X-Forwarded-For", "203.0.113.10")
		req.Header.Set("Forwarded", "for=203.0.113.10")
		req.Header.Set("X-Real-IP", "203.0.113.10")
		rr := httptest.NewRecorder()
		untrustedHandler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("forged XFF from untrusted peer status = %d, want 403", rr.Code)
		}
		if cidrOnlyHits.Load() != beforeHits {
			t.Fatalf("cidrOnlyHits incremented on forged XFF from untrusted peer")
		}

		// Trusted proxy 10.0.0.1 asserting 203.0.113.10 succeeds when trusted_proxies includes 10.0.0.0/8.
		req = httptest.NewRequest(http.MethodGet, "http://app.example.test/cidr-only/resource", nil)
		req.RemoteAddr = "10.0.0.1:40000"
		req.Header.Set("X-Forwarded-For", "203.0.113.10")
		rr = httptest.NewRecorder()
		untrustedHandler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("trusted proxy XFF status = %d, want 200", rr.Code)
		}
		if cidrOnlyHits.Load() != beforeHits+1 {
			t.Fatalf("cidrOnlyHits = %d, want %d", cidrOnlyHits.Load(), beforeHits+1)
		}
		recordExecutedIsolationCase("ISO-TRUST-001", "trusted_identity", "pass", "untrusted_peer_headers_ignored_trusted_proxy_accepted")
	})

	t.Run("mtls_and_backend_tls_boundaries", func(t *testing.T) {
		caFile, srvCertFile, srvKeyFile, caPool, validClientCert, wrongClientCert := generateTestPKI(t, tmpDir)
		_ = srvCertFile
		_ = srvKeyFile

		var mtlsOriginHits atomic.Int64
		tlsOrigin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mtlsOriginHits.Add(1)
			w.Header().Set("X-Verified-Client-CN", r.Header.Get("X-Client-CN"))
			w.WriteHeader(http.StatusOK)
		}))
		tlsCert, err := tls.LoadX509KeyPair(srvCertFile, srvKeyFile)
		if err != nil {
			t.Fatalf("LoadX509KeyPair: %v", err)
		}
		tlsOrigin.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
		tlsOrigin.StartTLS()
		defer tlsOrigin.Close()

		mtlsCfg := &config.Config{
			Upstreams: []config.UpstreamConfig{
				{
					Name:     "secure_backend",
					Strategy: "round_robin",
					Servers:  []config.UpstreamServer{{Address: tlsOrigin.Listener.Addr().String(), Weight: 1}},
					BackendTLS: &config.BackendTLSConfig{
						CAMode:         "file_only",
						CAFile:         caFile,
						ServerName:     "backend.internal",
						PeerIdentities: []string{"dns:backend.internal"},
					},
				},
				{
					Name:     "wrong_san_backend",
					Strategy: "round_robin",
					Servers:  []config.UpstreamServer{{Address: tlsOrigin.Listener.Addr().String(), Weight: 1}},
					BackendTLS: &config.BackendTLSConfig{
						CAMode:         "file_only",
						CAFile:         caFile,
						ServerName:     "backend.internal",
						PeerIdentities: []string{"dns:other-service.internal"},
					},
				},
			},
			Servers: []config.ServerConfig{{
				Name:        "mtls-server",
				Listen:      ":8443",
				ServerNames: []string{"app.example.test"},
				TLS: &config.TLSConfig{
					Enabled: true,
					Cert:    srvCertFile,
					Key:     srvKeyFile,
					ClientAuth: &config.ClientAuthConfig{
						Mode:      "request",
						CAFile:    caFile,
						VerifySAN: []string{"alice.client.test"},
					},
				},
				Locations: []config.LocationConfig{
					{
						RouteID:           config.String("mtls-route"),
						Match:             config.MatchConfig{Type: "prefix", Path: "/mtls/"},
						ProxyPass:         "https://secure_backend",
						RequireClientCert: true,
						Headers:           map[string]string{"X-Client-CN": "$ssl_client_s_dn"},
					},
					{
						RouteID:   config.String("wrong-san-route"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/wrong-san/"},
						ProxyPass: "https://wrong_san_backend",
					},
				},
			}},
		}

		f2, clean2 := minimalFactory(t)
		defer clean2()
		hMap, _, commit2, _, err := f2.Prepare(ctx, mtlsCfg)
		if err != nil {
			t.Fatalf("Prepare mtlsCfg: %v", err)
		}
		_, retire2 := commit2()
		defer retire2()
		mtlsHandler := hMap[":8443"]

		// Without client cert -> 403 before origin.
		req := httptest.NewRequest(http.MethodGet, "https://app.example.test/mtls/check", nil)
		req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
		rr := httptest.NewRecorder()
		mtlsHandler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden || mtlsOriginHits.Load() != 0 {
			t.Fatalf("missing client cert = %d, hits=%d; want 403, 0", rr.Code, mtlsOriginHits.Load())
		}

		// Untrusted client cert -> 403 before origin.
		untrustedLeaf, _ := x509.ParseCertificate(wrongClientCert.Certificate[0])
		req = httptest.NewRequest(http.MethodGet, "https://app.example.test/mtls/check", nil)
		req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{untrustedLeaf}}
		rr = httptest.NewRecorder()
		mtlsHandler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden || mtlsOriginHits.Load() != 0 {
			t.Fatalf("untrusted client cert = %d, hits=%d; want 403, 0", rr.Code, mtlsOriginHits.Load())
		}

		// Valid client cert + verified backend TLS SAN -> 200 and forwards verified CN (ignoring forged client header).
		validLeaf, _ := x509.ParseCertificate(validClientCert.Certificate[0])
		_ = caPool
		req = httptest.NewRequest(http.MethodGet, "https://app.example.test/mtls/check", nil)
		req.Header.Set("X-Client-CN", "forged-admin")
		req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{validLeaf}}
		rr = httptest.NewRecorder()
		mtlsHandler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || rr.Header().Get("X-Verified-Client-CN") != "CN=alice" {
			t.Fatalf("valid mTLS = %d CN=%q, want 200 CN=CN=alice", rr.Code, rr.Header().Get("X-Verified-Client-CN"))
		}

		// Backend TLS peer identity mismatch -> 502 Bad Gateway.
		req = httptest.NewRequest(http.MethodGet, "https://app.example.test/wrong-san/check", nil)
		req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
		rr = httptest.NewRecorder()
		mtlsHandler.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("wrong backend SAN status = %d, want 502", rr.Code)
		}
		recordExecutedIsolationCase("ISO-TRUST-002", "trusted_identity", "pass", "tls_peer_and_client_cert_verified_before_origin")
	})
}

// TestSecurityIsolationCacheAuthAndWAFBoundaries verifies ISO-CACHE-001 and
// ISO-CACHE-002 across warm/cold cache hits, Vary, Authorization/Cookie bypass,
// correlation header non-replay, and WAF policy tightening (#634).
func TestSecurityIsolationCacheAuthAndWAFBoundaries(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	var originHits atomic.Int64
	var revalHits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cached/reval" {
			hit := revalHits.Add(1)
			if hit == 1 {
				// Age=2 exceeds max-age=1 while remaining inside stale-while-revalidate=30,
				// so the next request immediately exercises STALE serving + background revalidation.
				w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=30")
				w.Header().Set("Age", "2")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=30")
			}
			w.Header().Set("ETag", `"reval-v1"`)
			w.Header().Set("Vary", "Accept-Language")
			if reqID := r.Header.Get("X-Request-ID"); reqID != "" {
				w.Header().Set("X-Upstream-Saw-ReqID", reqID)
			}
			if r.Header.Get("If-None-Match") == `"reval-v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = fmt.Fprintf(w, "reval-body-%s", r.Header.Get("Accept-Language"))
			return
		}
		n := originHits.Add(1)
		w.Header().Set("Cache-Control", "max-age=300")
		w.Header().Set("ETag", `"item-v1"`)
		w.Header().Set("Vary", "Accept-Language")
		w.Header().Set("Accept-Ranges", "bytes")
		// Simulate an upstream trying to echo X-Request-ID; Jul's cache must never
		// replay a previous client's X-Request-ID to a later client.
		if reqID := r.Header.Get("X-Request-ID"); reqID != "" {
			w.Header().Set("X-Upstream-Saw-ReqID", reqID)
		}
		_, _ = fmt.Fprintf(w, "cached-payload-%d-lang-%s", n, r.Header.Get("Accept-Language"))
	}))
	defer origin.Close()

	factory, cleanup := minimalFactory(t)
	defer cleanup()
	c, err := cache.New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: 8 << 20,
		DiskPath:      tmpDir,
		DiskMaxSize:   16 << 20,
		DefaultTTL:    config.Duration(5 * time.Minute),
		RangeEnabled:  true,
	}, factory.Log)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	factory.Cache = c

	baseCfg := func(wafEnabled bool) *config.Config {
		var wcfg *config.WAFConfig
		if wafEnabled && waf.Compiled {
			wcfg = &config.WAFConfig{
				Enabled:          true,
				Mode:             "block",
				BlockStatus:      403,
				CRSEnabled:       true,
				RequestBodyLimit: config.Size(128 << 10),
			}
		}
		return &config.Config{
			Cache: config.CacheConfig{
				Enabled:      true,
				DefaultTTL:   config.Duration(5 * time.Minute),
				RangeEnabled: true,
			},
			Upstreams: []config.UpstreamConfig{
				{
					Name:     "portal_up",
					Strategy: "round_robin",
					Servers:  []config.UpstreamServer{{Address: origin.Listener.Addr().String(), Weight: 1}},
				},
			},
			Servers: []config.ServerConfig{{
				Name:        "portal",
				Listen:      ":8080",
				ServerNames: []string{"portal.example.test"},
				Locations: []config.LocationConfig{{
					RouteID:   config.String("portal-cached"),
					Match:     config.MatchConfig{Type: "prefix", Path: "/cached/"},
					ProxyPass: "http://portal_up",
					Cache:     true,
					CachePolicy: &config.LocationCachePolicy{
						BypassCookies: []string{"session_id"},
					},
					WAF: wcfg,
				}},
			}},
		}
	}

	hMap, _, commit, _, err := factory.Prepare(ctx, baseCfg(false))
	if err != nil {
		t.Fatalf("Prepare baseCfg(false): %v", err)
	}
	_, retire := commit()
	defer retire()
	handler := middleware.RequestID()(hMap[":8080"])

	t.Run("warm_cold_vary_auth_and_correlation_headers", func(t *testing.T) {
		// 1. Cold miss with client-supplied X-Request-ID "req-client-1".
		req1 := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		req1.Header.Set("Accept-Language", "en")
		req1.Header.Set("X-Request-ID", "req-client-1")
		rr1 := httptest.NewRecorder()
		handler.ServeHTTP(rr1, req1)
		if rr1.Code != http.StatusOK || rr1.Header().Get("X-Cache") != "MISS" {
			t.Fatalf("req1 = %d X-Cache=%q", rr1.Code, rr1.Header().Get("X-Cache"))
		}
		if rr1.Header().Get("X-Request-ID") != "req-client-1" {
			t.Fatalf("req1 X-Request-ID = %q, want req-client-1", rr1.Header().Get("X-Request-ID"))
		}

		// 2. Warm hit from a second client with X-Request-ID "req-client-2" must receive
		// its own X-Request-ID ("req-client-2"), never the cached "req-client-1".
		req2 := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		req2.Header.Set("Accept-Language", "en")
		req2.Header.Set("X-Request-ID", "req-client-2")
		rr2 := httptest.NewRecorder()
		handler.ServeHTTP(rr2, req2)
		if rr2.Code != http.StatusOK || rr2.Header().Get("X-Cache") != "HIT" {
			t.Fatalf("req2 = %d X-Cache=%q", rr2.Code, rr2.Header().Get("X-Cache"))
		}
		if rr2.Header().Get("X-Request-ID") != "req-client-2" {
			t.Fatalf("warm hit replayed wrong X-Request-ID: %q", rr2.Header().Get("X-Request-ID"))
		}

		// 3. Request with session_id cookie bypasses the shared cache.
		before := originHits.Load()
		reqCookie := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		reqCookie.Header.Set("Accept-Language", "en")
		reqCookie.Header.Set("Cookie", "session_id=secret-user-session")
		rrCookie := httptest.NewRecorder()
		handler.ServeHTTP(rrCookie, reqCookie)
		if rrCookie.Header().Get("X-Cache") == "HIT" || originHits.Load() != before+1 {
			t.Fatalf("cookie bypass failed: X-Cache=%q hits=%d->%d", rrCookie.Header().Get("X-Cache"), before, originHits.Load())
		}

		// 4. Request with Authorization header bypasses shared cache reuse.
		before = originHits.Load()
		reqAuth := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		reqAuth.Header.Set("Accept-Language", "en")
		reqAuth.Header.Set("Authorization", "Bearer user-token")
		rrAuth := httptest.NewRecorder()
		handler.ServeHTTP(rrAuth, reqAuth)
		if rrAuth.Header().Get("X-Cache") == "HIT" || originHits.Load() != before+1 {
			t.Fatalf("Authorization bypass failed: X-Cache=%q hits=%d->%d", rrAuth.Header().Get("X-Cache"), before, originHits.Load())
		}

		// 5. Warm HEAD request and Vary isolation (Accept-Language: fr is a cold MISS).
		reqHead := httptest.NewRequest(http.MethodHead, "http://portal.example.test/cached/item", nil)
		reqHead.Header.Set("Accept-Language", "en")
		reqHead.Header.Set("X-Request-ID", "req-head-client")
		rrHead := httptest.NewRecorder()
		handler.ServeHTTP(rrHead, reqHead)
		if rrHead.Code != http.StatusOK || rrHead.Body.Len() != 0 || rrHead.Header().Get("X-Request-ID") != "req-head-client" {
			t.Fatalf("warm HEAD = %d len=%d reqID=%q", rrHead.Code, rrHead.Body.Len(), rrHead.Header().Get("X-Request-ID"))
		}

		reqVaryFR := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		reqVaryFR.Header.Set("Accept-Language", "fr")
		rrVaryFR := httptest.NewRecorder()
		handler.ServeHTTP(rrVaryFR, reqVaryFR)
		if rrVaryFR.Code != http.StatusOK || rrVaryFR.Header().Get("X-Cache") != "MISS" || !strings.HasSuffix(rrVaryFR.Body.String(), "-lang-fr") {
			t.Fatalf("Vary fr = %d X-Cache=%q body=%q", rrVaryFR.Code, rrVaryFR.Header().Get("X-Cache"), rrVaryFR.Body.String())
		}

		// 6. Range request against warm cached representation returns 206 Partial Content without correlation header leakage.
		reqRange := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		reqRange.Header.Set("Accept-Language", "en")
		reqRange.Header.Set("Range", "bytes=0-5")
		reqRange.Header.Set("X-Request-ID", "req-range-client")
		rrRange := httptest.NewRecorder()
		handler.ServeHTTP(rrRange, reqRange)
		if rrRange.Code != http.StatusPartialContent || rrRange.Body.String() != "cached" || rrRange.Header().Get("X-Request-ID") != "req-range-client" {
			t.Fatalf("warm Range = %d body=%q reqID=%q", rrRange.Code, rrRange.Body.String(), rrRange.Header().Get("X-Request-ID"))
		}

		// 7. Stale-while-revalidate (SWR) serves STALE immediately, triggers background
		// conditional 304 origin revalidation, refreshes the entry to HIT, and preserves Vary/correlation isolation.
		revalDone := make(chan string, 4)
		c.SetRevalidationObserver(func(outcome string) {
			select {
			case revalDone <- outcome:
			default:
			}
		})
		defer c.SetRevalidationObserver(nil)

		reqReval1 := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/reval", nil)
		reqReval1.Header.Set("Accept-Language", "en")
		reqReval1.Header.Set("X-Request-ID", "req-reval-1")
		rrReval1 := httptest.NewRecorder()
		handler.ServeHTTP(rrReval1, reqReval1)
		if rrReval1.Code != http.StatusOK || rrReval1.Header().Get("ETag") != `"reval-v1"` {
			t.Fatalf("reval1 = %d ETag=%q", rrReval1.Code, rrReval1.Header().Get("ETag"))
		}

		bgGroup := background.NewGroup(ctx, background.GroupOptions{Generation: 1})
		reqSWR := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/reval", nil)
		reqSWR = reqSWR.WithContext(background.WithLease(reqSWR.Context(), bgGroup))
		reqSWR.Header.Set("Accept-Language", "en")
		reqSWR.Header.Set("X-Request-ID", "req-swr-client")
		rrSWR := httptest.NewRecorder()
		handler.ServeHTTP(rrSWR, reqSWR)
		if rrSWR.Code != http.StatusOK || rrSWR.Header().Get("X-Cache") != "STALE" || rrSWR.Header().Get("X-Request-ID") != "req-swr-client" {
			t.Fatalf("SWR hit = %d X-Cache=%q reqID=%q", rrSWR.Code, rrSWR.Header().Get("X-Cache"), rrSWR.Header().Get("X-Request-ID"))
		}
		select {
		case out := <-revalDone:
			if out != "not_modified" {
				t.Fatalf("background SWR revalidation outcome = %q, want not_modified", out)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for background SWR revalidation")
		}
		if revalHits.Load() < 2 {
			t.Fatalf("expected background SWR to contact origin (revalHits=%d, want >= 2)", revalHits.Load())
		}

		// After background 304 refresh, the entry is fresh again (X-Cache: HIT) and serves client 304s cleanly.
		reqAfterSWR := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/reval", nil)
		reqAfterSWR.Header.Set("Accept-Language", "en")
		reqAfterSWR.Header.Set("X-Request-ID", "req-after-swr")
		rrAfterSWR := httptest.NewRecorder()
		handler.ServeHTTP(rrAfterSWR, reqAfterSWR)
		if rrAfterSWR.Code != http.StatusOK || rrAfterSWR.Header().Get("X-Cache") != "HIT" || rrAfterSWR.Header().Get("X-Request-ID") != "req-after-swr" {
			t.Fatalf("post-SWR hit = %d X-Cache=%q reqID=%q", rrAfterSWR.Code, rrAfterSWR.Header().Get("X-Cache"), rrAfterSWR.Header().Get("X-Request-ID"))
		}

		reqCond304 := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/reval", nil)
		reqCond304.Header.Set("Accept-Language", "en")
		reqCond304.Header.Set("If-None-Match", `"reval-v1"`)
		reqCond304.Header.Set("X-Request-ID", "req-reval-304")
		rrCond304 := httptest.NewRecorder()
		handler.ServeHTTP(rrCond304, reqCond304)
		if rrCond304.Code != http.StatusNotModified || rrCond304.Header().Get("X-Request-ID") != "req-reval-304" {
			t.Fatalf("cond 304 = %d reqID=%q", rrCond304.Code, rrCond304.Header().Get("X-Request-ID"))
		}

		recordExecutedIsolationCase("ISO-CACHE-001", "cache_isolation", "pass", "no_cross_user_cache_hit_and_no_correlation_header_replay")
	})

	t.Run("waf_tightening_with_warm_cache_and_reload", func(t *testing.T) {
		if !waf.Compiled {
			recordExecutedIsolationCase("ISO-CACHE-002", "cache_isolation", "unavailable", "waf_executes_before_cache_hit_and_cutover_fences_stale_fills")
			t.Skip("WAF inspection before warm cache requires -tags waf")
		}
		// Reload with WAF enabled in block mode while /cached/item is warm in cache.
		hMap2, _, commit2, _, err := factory.Prepare(ctx, baseCfg(true))
		if err != nil {
			t.Fatalf("Prepare baseCfg(true): %v", err)
		}
		_, retire2 := commit2()
		defer retire2()
		wafHandler := middleware.RequestID()(hMap2[":8080"])

		// Clean request to warm cached URL still succeeds from cache.
		cleanReq := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		cleanReq.Header.Set("Accept-Language", "en")
		cleanRR := httptest.NewRecorder()
		wafHandler.ServeHTTP(cleanRR, cleanReq)
		if cleanRR.Code != http.StatusOK {
			t.Fatalf("clean warm request after WAF tightening = %d, want 200", cleanRR.Code)
		}

		// Attack request (SQLi in header/URI) against the warm cached resource is blocked
		// by WAF BEFORE the cache can serve the warm entry!
		before := originHits.Load()
		attackReq := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		attackReq.Header.Set("Accept-Language", "en")
		attackReq.Header.Set("User-Agent", "sqlmap/1.7")
		attackRR := httptest.NewRecorder()
		wafHandler.ServeHTTP(attackRR, attackReq)
		if attackRR.Code != http.StatusForbidden {
			t.Fatalf("attack against warm cached URL = %d, want 403", attackRR.Code)
		}
		if originHits.Load() != before {
			t.Fatalf("originHits changed on WAF-blocked request: %d -> %d", before, originHits.Load())
		}

		// Reload with explicit cache invalidation cutover policy (reload_policy = "invalidate")
		// and verify WAF still executes before cache lookup.
		invCfg := baseCfg(true)
		invCfg.Servers[0].Locations[0].CachePolicy.ReloadPolicy = "invalidate"
		hMapInv, _, commitInv, _, err := factory.Prepare(ctx, invCfg)
		if err != nil {
			t.Fatalf("Prepare invCfg: %v", err)
		}
		_, retireInv := commitInv()
		defer retireInv()
		invHandler := middleware.RequestID()(hMapInv[":8080"])
		attackAfterCutover := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		attackAfterCutover.Header.Set("Accept-Language", "en")
		attackAfterCutover.Header.Set("User-Agent", "sqlmap/1.7")
		rrAfterCutover := httptest.NewRecorder()
		invHandler.ServeHTTP(rrAfterCutover, attackAfterCutover)
		if rrAfterCutover.Code != http.StatusForbidden || originHits.Load() != before {
			t.Fatalf("attack after invalidate cutover = %d hits=%d->%d", rrAfterCutover.Code, before, originHits.Load())
		}

		// Simulated process restart with cache reopen from disk (brand-new Cache instance on tmpDir):
		// first re-warm /cached/item on disk under retain policy, then reopen a fresh Cache on tmpDir
		// with cold memory, verify the reopened disk entry serves X-Cache: HIT on clean traffic, and
		// verify WAF blocks attack traffic BEFORE the reopened disk cache hit!
		retainCfg := baseCfg(true)
		retainCfg.Servers[0].Locations[0].CachePolicy.ReloadPolicy = "retain"
		hMapRetain, _, commitRetain, _, err := factory.Prepare(ctx, retainCfg)
		if err != nil {
			t.Fatalf("Prepare retainCfg: %v", err)
		}
		_, retireRetain := commitRetain()
		defer retireRetain()
		retainHandler := middleware.RequestID()(hMapRetain[":8080"])
		warmDiskReq := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		warmDiskReq.Header.Set("Accept-Language", "en")
		warmDiskRR := httptest.NewRecorder()
		retainHandler.ServeHTTP(warmDiskRR, warmDiskReq)
		if warmDiskRR.Code != http.StatusOK {
			t.Fatalf("warmDiskReq status = %d", warmDiskRR.Code)
		}

		factoryRestart, cleanupRestart := minimalFactory(t)
		defer cleanupRestart()
		cReopened, err := cache.New(config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: 8 << 20,
			DiskPath:      tmpDir,
			DiskMaxSize:   16 << 20,
			DefaultTTL:    config.Duration(5 * time.Minute),
			RangeEnabled:  true,
		}, factoryRestart.Log)
		if err != nil {
			t.Fatalf("cache.New reopen: %v", err)
		}
		factoryRestart.Cache = cReopened
		hMapRestart, _, commitRestart, _, err := factoryRestart.Prepare(ctx, retainCfg)
		if err != nil {
			t.Fatalf("Prepare restart: %v", err)
		}
		_, retireRestart := commitRestart()
		defer retireRestart()
		restartHandler := middleware.RequestID()(hMapRestart[":8080"])

		beforeReopenHits := originHits.Load()
		cleanReopenReq := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		cleanReopenReq.Header.Set("Accept-Language", "en")
		cleanReopenRR := httptest.NewRecorder()
		restartHandler.ServeHTTP(cleanReopenRR, cleanReopenReq)
		if cleanReopenRR.Code != http.StatusOK || cleanReopenRR.Header().Get("X-Cache") != "HIT" || originHits.Load() != beforeReopenHits {
			t.Fatalf("reopened disk cache clean request = %d X-Cache=%q hits=%d->%d", cleanReopenRR.Code, cleanReopenRR.Header().Get("X-Cache"), beforeReopenHits, originHits.Load())
		}

		attackAfterRestart := httptest.NewRequest(http.MethodGet, "http://portal.example.test/cached/item", nil)
		attackAfterRestart.Header.Set("Accept-Language", "en")
		attackAfterRestart.Header.Set("User-Agent", "sqlmap/1.7")
		rrAfterRestart := httptest.NewRecorder()
		restartHandler.ServeHTTP(rrAfterRestart, attackAfterRestart)
		if rrAfterRestart.Code != http.StatusForbidden || originHits.Load() != beforeReopenHits {
			t.Fatalf("attack after restart against reopened disk cache = %d hits=%d->%d", rrAfterRestart.Code, beforeReopenHits, originHits.Load())
		}

		recordExecutedIsolationCase("ISO-CACHE-002", "cache_isolation", "pass", "waf_executes_before_cache_hit_and_cutover_fences_stale_fills")
	})
}

// TestSecurityIsolationLifecycleAndProtocolBoundaries verifies ISO-LIFE-001 and
// ISO-PROTO-001 across failed Prepare/reload, withdrawal gate, concurrent
// traffic, WebSocket upgrade, and SSE streaming (#634).
func TestSecurityIsolationLifecycleAndProtocolBoundaries(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	htpasswdPath := writeTempHTPasswd(t, tmpDir, "op", "secret-pass-123")

	var wsUpgrades atomic.Int64
	var sseRequests atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws":
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				wsUpgrades.Add(1)
				hj, ok := w.(http.Hijacker)
				if !ok {
					http.Error(w, "no hijacker", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Upgrade", "websocket")
				w.Header().Set("Connection", "Upgrade")
				w.WriteHeader(http.StatusSwitchingProtocols)
				conn, rw, err := hj.Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				line, _ := rw.ReadString('\n')
				_, _ = rw.WriteString("echo:" + line)
				_ = rw.Flush()
				return
			}
			w.WriteHeader(http.StatusBadRequest)
		case "/sse":
			sseRequests.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: event-1\n\n"))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer backend.Close()

	makeCfg := func(withdrawn bool, inlineWAF string) *config.Config {
		var wcfg *config.WAFConfig
		if waf.Compiled && inlineWAF != "" {
			wcfg = &config.WAFConfig{
				Enabled:          true,
				Mode:             "block",
				BlockStatus:      403,
				InlineRules:      inlineWAF,
				RequestBodyLimit: config.Size(64 << 10),
			}
		}
		return &config.Config{
			Upstreams: []config.UpstreamConfig{
				{
					Name:     "app",
					Strategy: "round_robin",
					Servers:  []config.UpstreamServer{{Address: backend.Listener.Addr().String(), Weight: 1}},
				},
			},
			Servers: []config.ServerConfig{{
				Name:        "main",
				Listen:      ":8080",
				ServerNames: []string{"app.example.test"},
				Locations: []config.LocationConfig{
					{
						RouteID:   config.String("ws-route"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/ws"},
						ProxyPass: "http://app",
						Withdrawn: withdrawn,
						Auth: &config.AuthConfig{
							Basic: &config.BasicAuthConfig{File: htpasswdPath, Realm: "WS"},
						},
						WAF: wcfg,
					},
					{
						RouteID:   config.String("sse-route"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/sse"},
						ProxyPass: "http://app",
						WAF:       wcfg,
					},
				},
			}},
		}
	}

	factory, cleanup := minimalFactory(t)
	defer cleanup()

	goodRule := `SecRule ARGS:attack "@streq 1" "id:19001,phase:1,deny,status:403,log,msg:'blocked'"`
	hMap, _, commit, _, err := factory.Prepare(ctx, makeCfg(false, goodRule))
	if err != nil {
		t.Fatalf("Prepare initial: %v", err)
	}
	_, retire := commit()
	defer retire()

	var currentHandler atomic.Value
	currentHandler.Store(hMap[":8080"])

	t.Run("failed_prepare_tightening_rollback_and_concurrency", func(t *testing.T) {
		if waf.Compiled {
			// A broken WAF directive fails Prepare and leaves generation 1 active.
			if _, _, _, _, err := factory.Prepare(ctx, makeCfg(false, `SecRule BROKEN_SYNTAX`)); err == nil {
				t.Fatal("expected broken WAF rule to fail Prepare")
			}
			if got := factory.WAFEffective().Generation; got != 1 {
				t.Fatalf("serving WAF generation after failed Prepare = %d, want 1", got)
			}
		}

		// Concurrent traffic during withdrawal tightening and rollback.
		var wg sync.WaitGroup
		stop := make(chan struct{})
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					h := currentHandler.Load().(http.Handler)
					req := httptest.NewRequest(http.MethodGet, "http://app.example.test/sse", nil)
					rr := httptest.NewRecorder()
					h.ServeHTTP(rr, req)
					if rr.Code != http.StatusOK {
						t.Errorf("concurrent /sse status = %d, want 200", rr.Code)
						return
					}
				}
			}()
		}

		// Activate withdrawal on /ws while /sse continues serving 200.
		hMap2, _, commit2, _, err := factory.Prepare(ctx, makeCfg(true, goodRule))
		if err != nil {
			t.Fatalf("Prepare withdrawn: %v", err)
		}
		_, retire2 := commit2()
		currentHandler.Store(hMap2[":8080"])

		reqWS := httptest.NewRequest(http.MethodGet, "http://app.example.test/ws", nil)
		reqWS.SetBasicAuth("op", "secret-pass-123")
		rrWS := httptest.NewRecorder()
		currentHandler.Load().(http.Handler).ServeHTTP(rrWS, reqWS)
		if rrWS.Code != http.StatusForbidden {
			t.Fatalf("withdrawn /ws status = %d, want 403", rrWS.Code)
		}

		// Roll back withdrawal and restore /ws.
		hMap3, _, commit3, _, err := factory.Prepare(ctx, makeCfg(false, goodRule))
		if err != nil {
			t.Fatalf("Prepare rollback: %v", err)
		}
		_, retire3 := commit3()
		currentHandler.Store(hMap3[":8080"])
		close(stop)
		wg.Wait()
		retire2()
		defer retire3()
		status := "pass"
		if !waf.Compiled {
			status = "unavailable"
		}
		recordExecutedIsolationCase("ISO-LIFE-001", "policy_lifecycle", status, "failed_prepare_no_side_effect_and_atomic_generation_cutover")
	})

	t.Run("websocket_sse_grpc_and_stream_boundaries", func(t *testing.T) {
		srv := httptest.NewServer(currentHandler.Load().(http.Handler))
		defer srv.Close()

		// 1. Unauthenticated WebSocket handshake is rejected with 401 before upgrade.
		beforeWS := wsUpgrades.Load()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/ws", nil)
		req.Host = "app.example.test"
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unauth ws: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || wsUpgrades.Load() != beforeWS {
			t.Fatalf("unauth ws = %d upgrades=%d", resp.StatusCode, wsUpgrades.Load())
		}

		// 2. If WAF is compiled, an attack in the WebSocket handshake query is blocked with 403 before upgrade.
		if waf.Compiled {
			reqAttack, _ := http.NewRequest(http.MethodGet, srv.URL+"/ws?attack=1", nil)
			reqAttack.Host = "app.example.test"
			reqAttack.SetBasicAuth("op", "secret-pass-123")
			reqAttack.Header.Set("Connection", "Upgrade")
			reqAttack.Header.Set("Upgrade", "websocket")
			respAttack, err := http.DefaultClient.Do(reqAttack)
			if err != nil {
				t.Fatalf("waf ws: %v", err)
			}
			_ = respAttack.Body.Close()
			if respAttack.StatusCode != http.StatusForbidden || wsUpgrades.Load() != beforeWS {
				t.Fatalf("waf ws = %d upgrades=%d, want 403", respAttack.StatusCode, wsUpgrades.Load())
			}
		}

		// 3. Authenticated clean WebSocket upgrade succeeds (101 Switching Protocols) and streams frames.
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatalf("dial ws: %v", err)
		}
		defer conn.Close()
		authReq, _ := http.NewRequest(http.MethodGet, "http://app.example.test/ws", nil)
		authReq.SetBasicAuth("op", "secret-pass-123")
		authReq.Header.Set("Connection", "Upgrade")
		authReq.Header.Set("Upgrade", "websocket")
		if err := authReq.Write(conn); err != nil {
			t.Fatalf("write ws req: %v", err)
		}
		br := bufio.NewReader(conn)
		wsResp, err := http.ReadResponse(br, authReq)
		if err != nil {
			t.Fatalf("read ws resp: %v", err)
		}
		if wsResp.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("ws upgrade status = %d, want 101", wsResp.StatusCode)
		}
		_, _ = io.WriteString(conn, "hello-ws\n")
		reply, err := br.ReadString('\n')
		if err != nil || reply != "echo:hello-ws\n" {
			t.Fatalf("ws frame reply = %q, err=%v", reply, err)
		}

		// 4. SSE streaming succeeds and flushes event-stream content.
		sseReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/sse", nil)
		sseReq.Host = "app.example.test"
		sseResp, err := http.DefaultClient.Do(sseReq)
		if err != nil {
			t.Fatalf("sse request: %v", err)
		}
		sseBody, _ := io.ReadAll(sseResp.Body)
		_ = sseResp.Body.Close()
		if sseResp.StatusCode != http.StatusOK || !strings.Contains(string(sseBody), "data: event-1") {
			t.Fatalf("sse = %d %q", sseResp.StatusCode, string(sseBody))
		}

		// 5. Real HTTP/2 over TLS: verify H2 negotiation and WAF request-phase blocking before origin.
		h2Srv := httptest.NewUnstartedServer(currentHandler.Load().(http.Handler))
		h2Srv.EnableHTTP2 = true
		h2Srv.StartTLS()
		defer h2Srv.Close()

		h2Req, _ := http.NewRequest(http.MethodGet, h2Srv.URL+"/sse", nil)
		h2Req.Host = "app.example.test"
		h2Resp, err := h2Srv.Client().Do(h2Req)
		if err != nil {
			t.Fatalf("h2 sse request: %v", err)
		}
		_, _ = io.ReadAll(h2Resp.Body)
		_ = h2Resp.Body.Close()
		if h2Resp.ProtoMajor != 2 || h2Resp.StatusCode != http.StatusOK {
			t.Fatalf("h2 sse proto=%d status=%d, want 2, 200", h2Resp.ProtoMajor, h2Resp.StatusCode)
		}
		if waf.Compiled {
			h2Attack, _ := http.NewRequest(http.MethodGet, h2Srv.URL+"/sse?attack=1", nil)
			h2Attack.Host = "app.example.test"
			h2AttackResp, err := h2Srv.Client().Do(h2Attack)
			if err != nil {
				t.Fatalf("h2 waf attack: %v", err)
			}
			_ = h2AttackResp.Body.Close()
			if h2AttackResp.ProtoMajor != 2 || h2AttackResp.StatusCode != http.StatusForbidden {
				t.Fatalf("h2 waf attack proto=%d status=%d, want 2, 403", h2AttackResp.ProtoMajor, h2AttackResp.StatusCode)
			}
		}

		// 6. Real Jul native gRPC passthrough (GRPC: true), gRPC-JSON transcoding
		// (ActionGRPCTranscode), and L4 TCP stream proxy (stream.NewServer) boundaries.
		fullProtocolsExecuted := verifyFullProtocolIsolationBoundaries(t, factory, htpasswdPath)

		status := "pass"
		if !waf.Compiled || !fullProtocolsExecuted {
			status = "unavailable"
		}
		recordExecutedIsolationCase("ISO-PROTO-001", "protocol_applicability", status, "explicit_protocol_inspection_and_session_boundaries")
	})
}

func resolveCurrentGitHEAD(t *testing.T) string {
	t.Helper()
	if envSHA := strings.TrimSpace(os.Getenv("GITHUB_SHA")); len(envSHA) == 40 {
		return envSHA
	}
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		if sha := strings.TrimSpace(string(out)); len(sha) == 40 {
			return sha
		}
	}
	return "70277d9886131b3e88927b150eb43af97ad7c312"
}

// TestSecurityIsolationMatrixEndToEndExecutionGate connects the real executed
// security isolation test cases to VerifySecurityIsolationReport (#634).
func TestSecurityIsolationMatrixEndToEndExecutionGate(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "docs", "security-isolation-matrix.json")
	m, err := LoadSecurityIsolationManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadSecurityIsolationManifest: %v", err)
	}

	// Ensure all three execution suites have run in this test binary invocation.
	executedIsolationMu.Lock()
	count := len(executedIsolationResults)
	executedIsolationMu.Unlock()
	if count < len(m.Cases) {
		t.Run("exec_public_restricted_admin_identity", TestSecurityIsolationPublicRestrictedAdminAndIdentity)
		t.Run("exec_cache_auth_waf", TestSecurityIsolationCacheAuthAndWAFBoundaries)
		t.Run("exec_lifecycle_protocols", TestSecurityIsolationLifecycleAndProtocolBoundaries)
	}

	executedIsolationMu.Lock()
	results := make([]SecurityIsolationCaseResult, 0, len(m.Cases))
	for _, c := range m.Cases {
		if r, ok := executedIsolationResults[c.ID]; ok {
			results = append(results, r)
		}
	}
	executedIsolationMu.Unlock()

	profile := "lean"
	if waf.Compiled && fullProtocolCapabilitiesCompiled {
		profile = "full"
	}
	headSHA := resolveCurrentGitHEAD(t)
	rep := &SecurityIsolationReport{
		SchemaVersion: SecurityIsolationMatrixVersion,
		MatrixID:      m.MatrixID,
		SourceSHA:     headSHA,
		BuildProfile:  profile,
		WAFCompiled:   waf.Compiled,
		Results:       results,
	}
	if err := VerifySecurityIsolationReport(m, rep, headSHA); err != nil {
		t.Fatalf("VerifySecurityIsolationReport on executed results failed: %v (results=%+v)", err, results)
	}
}
