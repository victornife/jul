// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
	"jul/internal/waf"
)

func TestSecurityPostureMetricsRefreshWithoutPostureReads(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	certPath, keyPath := filepath.Join(t.TempDir(), "leaf.pem"), filepath.Join(t.TempDir(), "key.pem")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	writeLeaf := func(until time.Time) {
		t.Helper()
		leaf := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"app.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: until, KeyUsage: x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeLeaf(time.Now().Add(90 * 24 * time.Hour))
	const token = "metrics-freshness-synthetic-token-12345"
	cfg := &config.Config{Admin: config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: token}, Servers: []config.ServerConfig{{Listen: "127.0.0.1:8443", ServerNames: []string{"app.example.test"}, TLS: &config.TLSConfig{Enabled: true, Cert: certPath, Key: keyPath}, Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}}}}}
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	changed.Servers[0].Locations[0].Return = 201
	changedRaw, err := config.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	var unavailable, drift atomic.Bool
	var rawReads, loaderReads atomic.Int64
	a := admin.New(cfg.Admin, f.Log, admin.Deps{
		ReadConfigRaw: func() ([]byte, error) {
			rawReads.Add(1)
			if unavailable.Load() {
				return nil, errors.New("private-source-canary")
			}
			if drift.Load() {
				return changedRaw, nil
			}
			return raw, nil
		},
		LoadConfig: func() (*config.Config, error) {
			loaderReads.Add(1)
			return nil, errors.New("must use live raw/effective snapshot")
		},
		LiveSnapshot: func() server.LiveSnapshot { return server.LiveSnapshot{RawConfig: cfg, EffectiveConfig: cfg} },
		WAFCompiled:  waf.Compiled, WAFEffective: f.WAFEffective,
		ObserveSecurityPosture:       f.Metrics.ObserveSecurityPostureFindings,
		ObserveSecurityEvidenceState: f.Metrics.ObserveSecurityEvidenceState,
		Metrics:                      f.Metrics.Handler(),
	})
	f.SetSecurityPostureSource(a.RefreshSecurityPostureMetrics)
	f.SetAuditHealthSource(a.AuditWriteHealth)
	readsBefore := rawReads.Load()
	_, _, commit, abort, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	_, retire := commit()
	defer retire()
	if rawReads.Load() != readsBefore {
		t.Fatal("publication must not call the admin snapshot evaluator before server publication")
	}
	handler := a.Handler()
	scrape := func() string {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/metrics", nil)
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("metrics status %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	assertMetric := func(body, name string, want float64) {
		t.Helper()
		if got, ok := parsePromExposition(body)[name]; !ok || got != want {
			t.Fatalf("%s=%v want %v", name, got, want)
		}
	}
	errorsMetric := `jul_security_posture_findings{area="certificates",severity="error"}`
	assertMetric(scrape(), errorsMetric, 0)
	writeLeaf(time.Now().Add(-time.Minute))
	assertMetric(scrape(), errorsMetric, 1)
	writeLeaf(time.Now().Add(90 * 24 * time.Hour))
	recovered := scrape()
	assertMetric(recovered, errorsMetric, 0)
	baselineMaintenance := parsePromExposition(recovered)[`jul_security_posture_findings{area="maintenance",severity="warning"}`]
	drift.Store(true)
	drifted := scrape()
	assertMetric(drifted, `jul_security_posture_findings{area="maintenance",severity="warning"}`, baselineMaintenance+1)
	unavailable.Store(true)
	stale := scrape()
	assertMetric(stale, `jul_security_evidence_state{producer="posture_evaluator",state="stale"}`, 1)
	assertMetric(stale, `jul_security_evidence_state{producer="posture_evaluator",state="ok"}`, 0)
	if strings.Contains(stale, "private-source-canary") {
		t.Fatal("source error leaked into metrics")
	}
	unavailable.Store(false)
	assertMetric(scrape(), `jul_security_evidence_state{producer="posture_evaluator",state="ok"}`, 1)
	if loaderReads.Load() != 0 {
		t.Fatal("scraping reloaded provider-bearing config instead of using the live snapshot")
	}
	// Exercise concurrent readers through the same authenticated transport.
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); scrape() }()
	}
	wg.Wait()
}

func TestSecurityMetricsAvailabilityAndOfflineFallback(t *testing.T) {
	var absent *HandlerFactory
	absent.SetSecurityPostureSource(nil)
	absent.RefreshSecurityMetrics()
	(&HandlerFactory{}).RefreshSecurityMetrics()
	f, cleanup := minimalFactory(t)
	defer cleanup()
	f.RefreshSecurityMetrics()
	families, _ := f.Metrics.Gather()
	if len(families) == 0 {
		t.Fatal("metrics unavailable")
	}
	a := admin.New(config.AdminConfig{Enabled: true}, f.Log, admin.Deps{
		ReadConfigRaw:                func() ([]byte, error) { return nil, errors.New("unavailable") },
		ObserveSecurityEvidenceState: f.Metrics.ObserveSecurityEvidenceState,
	})
	f.SetSecurityPostureSource(a.RefreshSecurityPostureMetrics)
	f.RefreshSecurityMetrics()
	body := httptest.NewRecorder()
	f.Metrics.Handler().ServeHTTP(body, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(body.Body.String(), `jul_security_evidence_state{producer="posture_evaluator",state="unavailable"} 1`) {
		t.Fatal("missing snapshot must not look healthy")
	}
	f.SetSecurityPostureSource(nil)
	cfg := &config.Config{Servers: []config.ServerConfig{{Listen: ":8080", Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}}}}}
	_, _, commit, abort, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	_, retire := commit()
	defer retire()
	f.RefreshSecurityMetrics()
	body = httptest.NewRecorder()
	f.Metrics.Handler().ServeHTTP(body, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(body.Body.String(), `jul_security_evidence_state{producer="posture_evaluator",state="ok"} 1`) {
		t.Fatal("standalone factory must refresh offline findings")
	}
}
