// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/rbac"
	"jul/internal/server"
	"jul/internal/upstream"
)

// TestManagedApplyLifecycle_202PendingThen200Terminal is the WS01 vertical
// lifecycle test (§2.9). It drives a REAL managed apply through the actual
// ConfigApplyCoordinator and the actual admin HTTP handler — not a hand-seeded
// registry — and proves the exact production sequence the workstream exists to
// guarantee (AC-02):
//
//	persist candidate
//	→ enqueue reload
//	→ register exact-ID pending record
//	→ synchronous HTTP path returns a real 202 saved_not_live
//	→ GET /api/config/applies/<id> observes 202 + state=pending (never a 404)
//	→ terminal reload completion is released
//	→ GET the same endpoint observes 200 + state=terminal with matching result
//
// The terminal reload result is held behind a channel so the coordinator's
// bounded synchronous wait expires and returns the provisional saved_not_live
// exactly as it does in production when a reload outlives reload_timeout. No
// sleeps approximate the interleaving: the 202 is observed while the reload is
// demonstrably still in flight (its result is still buffered in the test), and
// the 200 is observed only after the terminal result is delivered and the
// finalizer signals completion.
func TestManagedApplyLifecycle_202PendingThen200Terminal(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	// reqCh captures the enqueued reload so the test controls exactly when the
	// terminal result is delivered. Withholding it forces the synchronous
	// ApplyRaw wait to expire and return the provisional saved_not_live 202,
	// while the exact-ID pending ledger record is already registered.
	reqCh := make(chan server.ReloadRequest, 1)
	clock := newSavedNotLiveClock()
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			reqCh <- req
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			// A short reload_timeout bounds the synchronous wait so the 202 is
			// returned promptly without any test sleep.
			cfg.Global.ReloadTimeout = config.Duration(savedNotLiveBudget)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		waitMargin:     10 * time.Millisecond,
		PlannedRestart: &PlannedRestartStore{},
		clock:          clock,
	}

	// Wire the coordinator to a real admin ManagedApplyRegistry using the EXACT
	// field mapping serve.go installs in production, and observe the terminal
	// finalization through a channel so the test never races the finalizer.
	registry, _, completedCh := wireProductionLedger(c)

	// Build the REAL admin HTTP handler over that same registry. The GET path is
	// served through admin.Server.Handler(), the exact handler the listener
	// serves in production, including route authorization.
	adminSrv := admin.New(config.AdminConfig{Enabled: true, Listen: "127.0.0.1:0"}, nil, admin.Deps{
		ManagedApplies: registry,
	})
	if adminSrv == nil {
		t.Fatal("admin.New returned nil")
	}
	h := adminSrv.Handler()

	// 1. Submit a managed apply. The reload is withheld, so ApplyRaw returns the
	//    provisional saved_not_live 202 after its bounded wait expires.
	startedAt := clock.Now().UTC()
	deadline := startedAt.Add(savedNotLiveBudget)
	reqCtx := admin.ApplyRequestContext{
		Operation: admin.ApplyOperationConfigApply,
		StartedAt: startedAt,
		Deadline:  deadline,
		TokenID:   "tok-owner-vertical",
	}
	res := applyRawAwaitingSavedNotLive(t, c, clock, reqCtx, validConfigRaw(t, ":8081"), ApplyHot)
	if res.Reload == nil || res.Reload.Outcome != server.ReloadSavedNotLive {
		t.Fatalf("result = %+v, want provisional saved_not_live", res.Reload)
	}
	applyID := res.ApplyID
	if applyID == "" {
		t.Fatal("saved_not_live result carried no apply id")
	}
	// The generated ID must be boot-scoped (rl_<12-hex>_<seq>), not a legacy
	// rl_<seq>, so it cannot collide across process restarts.
	if !bootScopedIDPattern.MatchString(applyID) {
		t.Fatalf("apply id %q is not boot-scoped (rl_<12-hex>_<seq>)", applyID)
	}

	// 2-5. GET /api/config/applies/<id> through the real admin handler MUST see
	//      202 + state=pending: a real 202 is never followed by a 404. This is
	//      the exact window the workstream closes.
	pendingCode, pendingBody := getApply(t, h, applyID)
	if pendingCode != http.StatusAccepted {
		t.Fatalf("pending GET code = %d, want 202 (body %+v)", pendingCode, pendingBody)
	}
	if pendingBody["state"] != string(admin.ManagedApplyPending) {
		t.Errorf("pending state = %v, want pending", pendingBody["state"])
	}
	if pendingBody["id"] != applyID {
		t.Errorf("pending id = %v, want %q", pendingBody["id"], applyID)
	}
	if pendingBody["operation"] != string(admin.ApplyOperationConfigApply) {
		t.Errorf("pending operation = %v, want %s", pendingBody["operation"], admin.ApplyOperationConfigApply)
	}
	// The pending response exposes the absolute deadline for deadline-aware
	// polling (AC-08), and never leaks actor/source IP/token digest.
	if _, ok := pendingBody["deadline"]; !ok {
		t.Error("pending response missing deadline for deadline-aware polling")
	}
	for _, forbidden := range []string{"actor", "source_ip", "token_digest", "owner_token_id"} {
		if _, present := pendingBody[forbidden]; present {
			t.Errorf("pending response leaked %q", forbidden)
		}
	}

	// 6. Release the terminal reload completion.
	req := <-reqCh
	req.Result <- server.ReloadResult{
		ID:             applyID,
		Source:         server.ReloadSourceAdmin,
		Outcome:        server.ReloadAppliedLive,
		Published:      true,
		DesiredVersion: server.CanonicalVersion(mustParse(t, validConfigRaw(t, ":8081"))),
		ServingVersion: "v2",
	}
	// Wait for the finalizer to durably terminalize the ledger record before
	// polling (no sleep-based interleaving).
	select {
	case fin := <-completedCh:
		if fin.FinalizationError != "" {
			t.Fatalf("unexpected finalization error: %q", fin.FinalizationError)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal finalization callback was not called")
	}

	// 7-9. Poll the same endpoint: it MUST now be 200 + state=terminal with the
	//      IDs, operation, and result matching the accepted apply.
	termCode, termBody := getApply(t, h, applyID)
	if termCode != http.StatusOK {
		t.Fatalf("terminal GET code = %d, want 200 (body %+v)", termCode, termBody)
	}
	if termBody["state"] != string(admin.ManagedApplyTerminal) {
		t.Errorf("terminal state = %v, want terminal", termBody["state"])
	}
	if termBody["id"] != applyID {
		t.Errorf("terminal id = %v, want %q", termBody["id"], applyID)
	}
	if termBody["operation"] != string(admin.ApplyOperationConfigApply) {
		t.Errorf("terminal operation = %v, want %s", termBody["operation"], admin.ApplyOperationConfigApply)
	}
	result, ok := termBody["result"].(map[string]any)
	if !ok {
		t.Fatalf("terminal result missing or not an object: %v", termBody["result"])
	}
	if result["apply_id"] != applyID {
		t.Errorf("terminal result apply_id = %v, want %q", result["apply_id"], applyID)
	}
	if result["ok"] != true {
		t.Errorf("terminal result ok = %v, want true", result["ok"])
	}
	// The terminal projection still never leaks actor/source IP/token digest.
	for _, forbidden := range []string{"actor", "source_ip", "token_digest", "owner_token_id"} {
		if _, present := termBody[forbidden]; present {
			t.Errorf("terminal response leaked %q", forbidden)
		}
	}
}

func TestManagedApplyLateReloadFailureDoesNotDiscloseSecret(t *testing.T) {
	const secret = "http://late-reload-secret.example.test:9000"
	t.Setenv("JUL_LATE_RELOAD_SECRET", secret)
	path := filepath.Join(t.TempDir(), "server.toml")
	addr := freePort(t)
	cfg := config.ProxyTarget("http://127.0.0.1:9000", addr)
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	candidate, err := config.NewCandidate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f, cleanup := minimalFactory(t)
	defer cleanup()
	srv := server.New(candidate.Effective, candidate.Raw, lifecycle.ComputeFingerprint(candidate.Effective), f.Log,
		func(ctx context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
			if c.Servers[0].Locations[0].ProxyPass == secret {
				return nil, 0, nil, nil, fmt.Errorf("late preparation failed for %s", c.Servers[0].Locations[0].ProxyPass)
			}
			return f.Prepare(ctx, c)
		}, config.NewTOMLSource(path), func(context.Context, *config.Config) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan server.ReloadRequest)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, candidate.Redaction) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	coordinator := &ConfigApplyCoordinator{
		BaseCtx: ctx, Path: path, Preflight: testPreflight(), LiveSnapshot: srv.LiveSnapshot,
		PlannedRestart: &PlannedRestartStore{},
		SubmitReload: func(req server.ReloadRequest) error {
			reload <- req
			return nil
		},
	}
	registry, _, completed := wireProductionLedger(coordinator)
	adminSrv := admin.New(config.AdminConfig{Enabled: true, Listen: "127.0.0.1:0"}, nil, admin.Deps{
		ManagedApplies: registry,
		ReadConfigRaw:  func() ([]byte, error) { return os.ReadFile(path) },
		LoadConfig:     func() (*config.Config, error) { return config.NewTOMLSource(path).Load() },
		LiveSnapshot:   srv.LiveSnapshot,
		ApplyConfigRaw: func(ctx admin.ApplyRequestContext, data []byte, mode string) (admin.ConfigApplyResult, error) {
			result, err := coordinator.ApplyRaw(ctx, data, ApplyMode(mode))
			return toAdminConfigApplyResult(result), err
		},
	})
	cfg.Servers[0].Locations[0].ProxyPass = "${env:JUL_LATE_RELOAD_SECRET}"
	raw, err = config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(raw))
	req.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	adminSrv.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("apply status = %d: %s", response.Code, response.Body.String())
	}
	var result admin.ConfigApplyResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Reload == nil || result.Reload.Outcome != server.ReloadNotApplied || result.Reload.FailedPhase != "prepare" || !result.Restored {
		t.Fatalf("late failure classification lost: %+v", result)
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatal("immediate response disclosed the resolved secret")
	}
	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal record was not completed")
	}
	code, record := getApply(t, adminSrv.Handler(), result.ApplyID)
	recordJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK || strings.Contains(string(recordJSON), secret) {
		t.Fatalf("terminal record status %d or disclosed secret", code)
	}
	var terminal struct {
		State  admin.ManagedApplyState `json:"state"`
		Result admin.ConfigApplyResult `json:"result"`
	}
	if err := json.Unmarshal(recordJSON, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.State != admin.ManagedApplyTerminal || terminal.Result.Reload == nil ||
		terminal.Result.Reload.Outcome != server.ReloadNotApplied ||
		terminal.Result.Reload.FailedPhase != "prepare" || !terminal.Result.Restored {
		t.Fatalf("terminal failure classification lost: %+v", terminal)
	}
}

// getApply issues GET /api/config/applies/<id> through the admin handler and
// returns the status code and decoded JSON body. It asserts the Cache-Control:
// no-store contract on every response so a lifecycle poll is never cached.
func getApply(t *testing.T, h http.Handler, id string) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config/applies/"+id, nil)
	h.ServeHTTP(rr, req)
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var body map[string]any
	if rr.Body.Len() > 0 {
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal body: %v (raw %s)", err, rr.Body.String())
		}
	}
	return rr.Code, body
}

// mustParse parses config raw for a canonical-version comparison in the test.
func mustParse(t *testing.T, raw []byte) *config.Config {
	t.Helper()
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

func TestManagedApplyDNSValidationDoesNotDiscloseResolvedSecrets(t *testing.T) {
	const adminSecret = "dns01-admin-credential-canary-123"
	const operatorToken = "dns01-config-apply-token-fixture"
	t.Setenv("JUL_DNS_REVIEW_ADMIN_SECRET", adminSecret)
	t.Setenv("JUL_DNS_REVIEW_TSIG", "MDEyMzQ1Njc4OWFiY2RlZg==")
	t.Setenv("JUL_DNS_REVIEW_MULTILINE_SECRET", "private-value\ncredentialCanary[123]:suffix")

	path := filepath.Join(t.TempDir(), "server.toml")
	seed := config.ProxyTarget("127.0.0.1:9000", ":8080")
	seed.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{
			Enabled:     true,
			DefaultRole: "admin",
			Roles:       []config.AdminRole{{Name: "writer", Permissions: []string{"config:apply"}}},
			Principals: []config.AdminPrincipal{
				{Name: "writer", Role: "writer", Token: operatorToken},
				{Name: "admin", Role: "admin", Token: "${env:JUL_DNS_REVIEW_ADMIN_SECRET}"},
			},
		},
	}
	raw, err := config.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	baseline := mustParse(t, raw)
	resolved, err := config.NewCandidate(baseline)
	if err != nil {
		t.Fatal(err)
	}
	liveSnapshot := func() server.LiveSnapshot {
		return server.LiveSnapshot{EffectiveConfig: resolved.Effective}
	}
	coordinator := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		LiveSnapshot:   liveSnapshot,
		PlannedRestart: &PlannedRestartStore{},
	}
	entered := false
	adminServer := admin.New(resolved.Effective.Admin, nil, admin.Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(path) },
		LoadConfig:    func() (*config.Config, error) { return config.Parse(raw) },
		LiveSnapshot:  liveSnapshot,
		ApplyConfigRaw: func(ctx admin.ApplyRequestContext, data []byte, mode string) (admin.ConfigApplyResult, error) {
			entered = true
			result, err := coordinator.ApplyRaw(ctx, data, ApplyMode(mode))
			return toAdminConfigApplyResult(result), err
		},
	})
	if adminServer == nil {
		t.Fatal("admin server is disabled")
	}
	policy, err := rbac.Build(true, "admin", map[string][]string{"writer": {"config:apply"}}, []rbac.PrincipalDef{
		{Name: "writer", Role: "writer", Token: operatorToken},
		{Name: "admin", Role: "admin", Token: adminSecret},
	}, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	adminServer.UpdatePolicy(policy)

	for _, tc := range []struct {
		name string
		edit func(*config.Config)
	}{
		{"invalid domain", func(c *config.Config) {
			c.Servers[0].TLS.ACME.Domains = []string{"${env:JUL_DNS_REVIEW_ADMIN_SECRET}!"}
		}},
		{"domain outside zone", func(c *config.Config) {
			c.Servers[0].TLS.ACME.Domains = []string{"${env:JUL_DNS_REVIEW_ADMIN_SECRET}.outside.test"}
		}},
		{"uncovered server name", func(c *config.Config) {
			c.Servers[0].ServerNames = []string{"${env:JUL_DNS_REVIEW_ADMIN_SECRET}.example.test"}
			c.Servers[0].TLS.ACME.Domains = []string{"other.example.test"}
		}},
		{"invalid CA", func(c *config.Config) {
			c.Servers[0].TLS.ACME.CA = "${env:JUL_DNS_REVIEW_ADMIN_SECRET}!"
		}},
		{"missing TLS certificate", func(c *config.Config) {
			c.Servers[0].TLS = &config.TLSConfig{
				Enabled: true,
				Cert:    "/missing/${env:JUL_DNS_REVIEW_ADMIN_SECRET}.pem",
				Key:     "/missing/key.pem",
			}
		}},
		{"multiline TLS certificate", func(c *config.Config) {
			c.Servers[0].TLS = &config.TLSConfig{
				Enabled: true,
				Cert:    "/missing/${env:JUL_DNS_REVIEW_MULTILINE_SECRET}.pem",
				Key:     "/missing/key.pem",
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, err := baseline.Clone()
			if err != nil {
				t.Fatal(err)
			}
			candidate.Servers[0].ServerNames = []string{"example.test"}
			candidate.Servers[0].TLS = &config.TLSConfig{Enabled: true, ACME: &config.ACMEConfig{
				Enabled: true, Email: "ops@example.test", CA: "letsencrypt-staging",
				Challenge: "dns-01", DNSProvider: "rfc2136", Domains: []string{"example.test"},
				RFC2136: &config.RFC2136Config{
					Server: "127.0.0.1:53", Observers: []string{"127.0.0.1:53"},
					Zone: "example.test", TSIGKeyName: "jul.example.test",
					TSIGSecret: "${env:JUL_DNS_REVIEW_TSIG}",
				},
			}}
			tc.edit(candidate)
			candidateRaw, err := config.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			for _, endpoint := range []struct {
				path         string
				wantStatus   int
				managedApply bool
			}{
				{path: "/api/config/apply", wantStatus: http.StatusBadRequest, managedApply: true},
				{path: "/api/config/apply?mode=stage_restart", wantStatus: http.StatusBadRequest, managedApply: true},
				{path: "/api/config/raw", wantStatus: http.StatusBadRequest, managedApply: true},
			} {
				t.Run(endpoint.path, func(t *testing.T) {
					entered = false
					request := httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewReader(candidateRaw))
					request.RemoteAddr = "127.0.0.1:1234"
					request.Header.Set("Authorization", "Bearer "+operatorToken)
					response := httptest.NewRecorder()
					adminServer.Handler().ServeHTTP(response, request)
					if endpoint.managedApply && !entered {
						t.Fatalf("request did not reach managed apply: status=%d", response.Code)
					}
					if response.Code != endpoint.wantStatus {
						t.Fatalf("status = %d, want %d", response.Code, endpoint.wantStatus)
					}
					for _, secret := range []string{adminSecret, "private-value", "credentialCanary[123]"} {
						if strings.Contains(response.Body.String(), secret) {
							t.Fatal("managed apply response disclosed a resolved credential")
						}
					}
				})
			}
		})
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, raw) {
		t.Fatal("invalid candidate changed the persisted configuration")
	}
}
