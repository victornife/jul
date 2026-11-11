// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/server"
	"jul/internal/upstream"
	"jul/internal/versionid"
	"jul/internal/waf"
)

// wafFactoryConfig serves four routes: two inheriting the global policy, one
// detect-mode override loading an external rule file, and one override that
// turns the WAF off.
func wafFactoryConfig(rules string) *config.Config {
	cfg := config.ProxyTarget("127.0.0.1:9001", "127.0.0.1:0")
	cfg.WAF = config.WAFConfig{Enabled: true, CRSEnabled: true, RequestBodyLimit: 128 << 10}
	base := cfg.Servers[0].Locations[0]
	api := base
	api.Match.Path = "/api"
	api.WAF = &config.WAFConfig{Enabled: true, Mode: "detect", DirectivesFiles: []string{rules}, RequestBodyLimit: 64 << 10}
	health := base
	health.Match.Path = "/health"
	health.WAF = &config.WAFConfig{Enabled: false}
	static := base
	static.Match.Path = "/static"
	cfg.Servers[0].Locations = append(cfg.Servers[0].Locations, api, health, static)
	return cfg
}

func prepareAndCommit(t *testing.T, f *HandlerFactory, cfg *config.Config) uint64 {
	t.Helper()
	_, genID, commit, _, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, retire := commit()
	if retire != nil {
		retire()
	}
	return genID
}

func TestFactoryWAFEffectiveTracksServingGeneration(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	dir := t.TempDir()
	rules := filepath.Join(dir, "api.conf")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(rules, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`SecRule REQUEST_URI "@contains /api/admin" "id:40001,phase:1,deny,status:403"` + "\n")

	gen1 := prepareAndCommit(t, f, wafFactoryConfig(rules))
	p := f.WAFEffective()
	if !p.Compiled || p.Generation != gen1 || p.CompiledAt == "" || !p.GlobalEnabled {
		t.Fatalf("serving policy = %+v", p)
	}
	if p.InheritingRoutes != 2 || p.OverrideRoutes != 1 || p.DisabledOverrideRoutes != 1 || p.UnprotectedRoutes != 1 {
		t.Fatalf("coverage = %+v", p)
	}
	if p.Global == nil || !p.Global.CRSEnabled || p.Global.CRSVersion != p.EmbeddedCRSVersion || p.Global.Rules.Embedded == 0 || p.Global.ExternalFiles != 0 {
		t.Fatalf("global = %+v", p.Global)
	}
	var api = p.Overrides[0].Policy
	if api == nil || api.Mode != "detect" || api.CRSEnabled || api.RequestBodyLimitBytes != 64<<10 || api.ExternalFiles != 1 || api.Rules.External != 1 {
		t.Fatalf("api override = %+v", api)
	}
	digest1 := api.ExternalDigest
	if strings.Contains(fmt.Sprintf("%+v %+v", *p, *api), dir) {
		t.Fatal("effective policy leaks the rule file path")
	}

	// A candidate whose rule file no longer compiles leaves the serving
	// projection on the previous generation (Prepare fails before Publish).
	write("SecRule ARGS \"@notAnOperator x\" \"id:40002,phase:1,deny\"\n")
	if _, _, _, _, err := f.Prepare(context.Background(), wafFactoryConfig(rules)); err == nil {
		t.Fatal("broken rule file compiled")
	}
	if after := f.WAFEffective(); after.Generation != gen1 || after.Overrides[0].Policy.ExternalDigest != digest1 {
		t.Fatalf("failed candidate changed the serving projection: %+v", after)
	}

	// An aborted candidate is not serving either.
	write(`SecRule REQUEST_URI "@contains /api/other" "id:40003,phase:1,deny,status:403"` + "\n")
	_, _, _, abort, err := f.Prepare(context.Background(), wafFactoryConfig(rules))
	if err != nil {
		t.Fatal(err)
	}
	abort()
	if after := f.WAFEffective(); after.Generation != gen1 || after.Overrides[0].Policy.ExternalDigest != digest1 {
		t.Fatalf("aborted candidate changed the serving projection: %+v", after)
	}

	// Same path, changed bytes, committed: a new generation and a new digest.
	gen2 := prepareAndCommit(t, f, wafFactoryConfig(rules))
	after := f.WAFEffective()
	if after.Generation != gen2 || gen2 == gen1 || after.Overrides[0].Policy.ExternalDigest == digest1 {
		t.Fatalf("same-path content change not observable: gen %d->%d digest %s->%s", gen1, gen2, digest1, after.Overrides[0].Policy.ExternalDigest)
	}

	// A global policy change is reflected; an inline-only global loads no file.
	cfg := wafFactoryConfig(rules)
	cfg.WAF = config.WAFConfig{Enabled: true, Mode: "detect", InlineRules: `SecRule ARGS:q "@streq x" "id:40004,phase:1,deny"`, RequestBodyLimit: 128 << 10}
	prepareAndCommit(t, f, cfg)
	if g := f.WAFEffective().Global; g == nil || g.Mode != "detect" || g.CRSEnabled || g.Rules.Inline != 1 || g.ExternalFiles != 0 {
		t.Fatalf("global after change = %+v", g)
	}

	// Test EvaluateServingWAF, HandleAdminWAFEvaluate, and live on-disk asset staleness in WAFEffective() (#630).
	globalRuleFile := filepath.Join(dir, "global.conf")
	initialGlobalRule := `SecRule REQUEST_HEADERS:X-Global-Probe "@streq 1" "id:40005,phase:1,deny,status:403"` + "\n"
	if err := os.WriteFile(globalRuleFile, []byte(initialGlobalRule), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgEval := wafFactoryConfig(rules)
	cfgEval.WAF = config.WAFConfig{
		Enabled:          true,
		Mode:             "block",
		CRSEnabled:       true,
		RequestBodyLimit: 1 << 20,
		DirectivesFiles:  []string{globalRuleFile},
	}
	prepareAndCommit(t, f, provisionedInspection(cfgEval))

	// Before running evaluation, WAFEffective().Evaluation is "unrun".
	if ev := f.WAFEffective().Evaluation; ev == nil || ev.State != "unrun" {
		t.Fatalf("expected unrun evaluation state before EvaluateServingWAF, got %+v", ev)
	}

	// Run server-observed evaluation via HandleAdminWAFEvaluate with empty body.
	sumView, err := f.HandleAdminWAFEvaluate(context.Background(), nil)
	if err != nil || sumView.State != "current" || sumView.Source != "server_observed" || sumView.PassedCases != 10 {
		t.Fatalf("HandleAdminWAFEvaluate = %+v, err=%v", sumView, err)
	}
	if ev := f.WAFEffective().Evaluation; ev == nil || ev.State != "current" {
		t.Fatalf("expected current evaluation state after HandleAdminWAFEvaluate, got %+v", ev)
	}

	// Edit globalRuleFile in-place on disk WITHOUT reloading: WAFEffective().Evaluation immediately reports "stale_asset_changed"!
	if err := os.WriteFile(globalRuleFile, []byte(initialGlobalRule+"# modified on disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ev := f.WAFEffective().Evaluation; ev == nil || ev.State != "stale_asset_changed" {
		t.Fatalf("expected stale_asset_changed after same-path file edit on disk, got %+v", ev)
	}

	// Restore exact original bytes on disk: WAFEffective().Evaluation returns to "current"!
	if err := os.WriteFile(globalRuleFile, []byte(initialGlobalRule), 0o600); err != nil {
		t.Fatal(err)
	}
	if ev := f.WAFEffective().Evaluation; ev == nil || ev.State != "current" {
		t.Fatalf("expected current after restoring exact asset bytes, got %+v", ev)
	}

	// Test supplying an external report via HandleAdminWAFEvaluate -> Source="supplied_report".
	rep, err := f.EvaluateServingWAF(context.Background(), &cfgEval.WAF, nil)
	if err != nil {
		t.Fatalf("EvaluateServingWAF: %v", err)
	}
	repBytes, _ := json.Marshal(rep)
	suppliedView, err := f.HandleAdminWAFEvaluate(context.Background(), repBytes)
	if err != nil || suppliedView.Source != "supplied_report" || suppliedView.State != "current" {
		t.Fatalf("supplied HandleAdminWAFEvaluate = %+v, err=%v", suppliedView, err)
	}

	// Test custom fixture pack via HandleAdminWAFEvaluate.
	packBytes, _ := json.Marshal(waf.DefaultFixturePack())
	customView, err := f.HandleAdminWAFEvaluate(context.Background(), packBytes)
	if err != nil || customView.Source != "server_observed" {
		t.Fatalf("custom pack HandleAdminWAFEvaluate = %+v, err=%v", customView, err)
	}
	if _, err := f.HandleAdminWAFEvaluate(context.Background(), []byte(`{"invalid":true}`)); err == nil {
		t.Fatal("expected error for invalid JSON body in HandleAdminWAFEvaluate")
	}
}

// Every inheriting route shares one compiled global engine, so a generation
// enforces a single global rule set even if a rule file changes mid-build.
func TestFactorySharesTheGlobalFirewall(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	cfg := config.ProxyTarget("127.0.0.1:9001", "127.0.0.1:0")
	cfg.WAF = config.WAFConfig{Enabled: true, InlineRules: `SecRule ARGS:q "@streq x" "id:40010,phase:1,deny"`, RequestBodyLimit: 128 << 10}
	for _, p := range []string{"/a", "/b", "/c"} {
		loc := cfg.Servers[0].Locations[0]
		loc.Match.Path = p
		cfg.Servers[0].Locations = append(cfg.Servers[0].Locations, loc)
	}
	compiles := 0
	orig := newFirewall
	newFirewall = func(ctx context.Context, c config.WAFConfig, o waf.Options) (*waf.Firewall, error) {
		compiles++
		return orig(ctx, c, o)
	}
	t.Cleanup(func() { newFirewall = orig })
	prepareAndCommit(t, f, cfg)
	if compiles != 1 {
		t.Fatalf("global policy compiled %d times for 4 inheriting routes, want 1", compiles)
	}
	if p := f.WAFEffective(); p.InheritingRoutes != 4 || p.Global == nil || p.Global.Rules.Inline != 1 {
		t.Fatalf("shared global = %+v", p)
	}
}

func TestWAFPreparationErrorDoesNotExportMatcherFingerprint(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	cfg := wafFactoryConfig(filepath.Join(t.TempDir(), "missing-rules"))
	private := WAFScope(cfg.Servers[0], cfg.Servers[0].Locations[1])
	_, done, err := f.Build(context.Background(), cfg, false)
	if done != nil {
		defer done()
	}
	if err == nil || !strings.Contains(err.Error(), "servers[0].locations[1]") || strings.Contains(err.Error(), private) {
		t.Fatalf("scope-bearing preparation error: %v", err)
	}
}

// TestSecurityEndpointWAFExternalDigestMustNotConfirmOfflineGuess reproduces
// EX01 (#649) through GET /api/security using a status:read-only principal and
// synthetic low-entropy rule/include/data content, including the provider-overlap
// case where a ${file:...} reference selects or overlaps a Coraza external file.
func TestSecurityEndpointWAFExternalDigestMustNotConfirmOfflineGuess(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Synthetic low-entropy external inputs:
	// 1. A shared secret file used BOTH by a Jul ${file:...} provider and by Coraza @pmFromFile (overlap case).
	// 2. An included SecLang file.
	// 3. A top-level directive file whose path is selected via ${file:...}.
	overlapSecret := "pin-0042\n"
	write("overlap-secret.data", overlapSecret)
	incContent := "SecAction \"id:50002,phase:1,pass,nolog\"\n"
	write("included.conf", incContent)
	mainRuleContent := "SecRule ARGS:pin \"@pmFromFile overlap-secret.data\" \"id:50001,phase:1,deny,status:403\"\nInclude included.conf\n"
	mainRulePath := write("main.conf", mainRuleContent)
	selectorPath := write("selected-rule-path.txt", mainRulePath)
	overlapPath := filepath.Join(dir, "overlap-secret.data")

	overrideRuleContent := "SecRule REQUEST_HEADERS:X-Pin \"@pmFromFile overlap-secret.data\" \"id:50003,phase:1,deny,status:403\"\n"
	overrideRulePath := write("override.conf", overrideRuleContent)

	const statusViewerToken = "status-viewer-synthetic-token-649-ex01"
	t.Setenv("JUL_TEST_649_VIEWER_TOKEN", statusViewerToken)

	cfgPath := filepath.Join(dir, "jul.toml")
	cfg := config.ProxyTarget("http://127.0.0.1:9001", freePort(t))
	cfg.Global.ConfigAuthority = "file_owned"
	// Overlap: the same overlap-secret.data file is read by ${file:...} in the
	// configuration and by Coraza @pmFromFile, and ${file:...} selects main.conf.
	cfg.Servers[0].Name = "srv-${file:" + overlapPath + "}"
	cfg.WAF = config.WAFConfig{
		Enabled:          true,
		Mode:             "block",
		DirectivesFiles:  []string{"${file:" + selectorPath + "}"},
		RequestBodyLimit: 128 << 10,
	}
	baseLoc := cfg.Servers[0].Locations[0]
	baseLoc.ProxyPass = ""
	baseLoc.Return = 200
	overrideLoc := baseLoc
	overrideLoc.Match.Path = "/override"
	overrideLoc.WAF = &config.WAFConfig{
		Enabled:          true,
		Mode:             "detect",
		DirectivesFiles:  []string{overrideRulePath},
		RequestBodyLimit: 64 << 10,
	}
	cfg.Servers[0].Locations = []config.LocationConfig{baseLoc, overrideLoc}
	cfg.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  freePort(t),
		RBAC: config.AdminRBACConfig{
			Enabled: true,
			Roles: []config.AdminRole{{
				Name:        "status-only",
				Permissions: []string{"status:read"},
			}},
			Principals: []config.AdminPrincipal{
				{
					Name:  "viewer",
					Role:  "status-only",
					Token: "${env:JUL_TEST_649_VIEWER_TOKEN}",
				},
				{
					Name:  "root-admin",
					Role:  "admin",
					Token: "admin-user-has-32-char-token--test",
				},
			},
		},
	}
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	legacyUnkeyedDigest := func(files ...string) string {
		var chain strings.Builder
		for _, content := range files {
			sum := sha256.Sum256([]byte(content))
			chain.WriteString("sha256:")
			chain.WriteString(hex.EncodeToString(sum[:]))
			chain.WriteByte('\n')
		}
		outer := sha256.Sum256([]byte(chain.String()))
		return "sha256:" + hex.EncodeToString(outer[:])
	}
	expectedChainBytes := func(files ...string) []byte {
		var chain bytes.Buffer
		for _, content := range files {
			sum := sha256.Sum256([]byte(content))
			chain.WriteString("sha256:")
			chain.WriteString(hex.EncodeToString(sum[:]))
			chain.WriteByte('\n')
		}
		return chain.Bytes()
	}

	versionsA, err := versionid.New(bytes.Repeat([]byte{0x64}, 32))
	if err != nil {
		t.Fatal(err)
	}
	versionsB, err := versionid.New(bytes.Repeat([]byte{0x99}, 32))
	if err != nil {
		t.Fatal(err)
	}

	runServer := func(vctx *versionid.Context) (func() admin.SecurityProjection, chan struct{}, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		signals := make(chan struct{}, 1)
		done := make(chan int, 1)
		parsed, err := config.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		var logBuf bytes.Buffer
		go func() {
			done <- Serve(ctx, signals, config.NewTOMLSource(cfgPath), parsed, "Jul.IA", "649-test", WithEffectiveVersions(vctx), WithLogOutput(&logBuf))
		}()
		stop := func() {
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Fatalf("Serve exit %d: %s", code, logBuf.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Serve shutdown timed out")
			}
		}
		client := &http.Client{Timeout: time.Second}
		fetchSecurity := func() admin.SecurityProjection {
			req, _ := http.NewRequest(http.MethodGet, "http://"+cfg.Admin.Listen+"/api/security", nil)
			req.Header.Set("Authorization", "Bearer "+statusViewerToken)
			res, err := client.Do(req)
			if err != nil {
				return admin.SecurityProjection{}
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				return admin.SecurityProjection{}
			}
			var sp admin.SecurityProjection
			_ = json.NewDecoder(res.Body).Decode(&sp)
			return sp
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if sp := fetchSecurity(); sp.WAFEffective != nil && sp.WAFEffective.Generation > 0 {
				return fetchSecurity, signals, stop
			}
			time.Sleep(15 * time.Millisecond)
		}
		stop()
		t.Fatal("GET /api/security did not become ready")
		return nil, nil, nil
	}

	fetchA, signalsA, stopA := runServer(versionsA)
	sp1 := fetchA()
	eff1 := sp1.WAFEffective
	if eff1 == nil || eff1.Global == nil || len(eff1.Overrides) != 1 || eff1.Overrides[0].Policy == nil {
		stopA()
		t.Fatalf("unexpected waf_effective = %+v", eff1)
	}
	globalDigest1 := eff1.Global.ExternalDigest
	overrideDigest1 := eff1.Overrides[0].Policy.ExternalDigest

	// 1. Neither global nor override digest may equal the legacy unkeyed SHA-256 chain or raw SHA-256.
	unkeyedGlobal := legacyUnkeyedDigest(mainRuleContent, overlapSecret, incContent)
	unkeyedOverride := legacyUnkeyedDigest(overrideRuleContent, overlapSecret)
	if globalDigest1 == unkeyedGlobal || strings.HasPrefix(globalDigest1, "sha256:") {
		stopA()
		t.Fatalf("EX01 unkeyed global digest exposed: got %q, legacy %q", globalDigest1, unkeyedGlobal)
	}
	if overrideDigest1 == unkeyedOverride || strings.HasPrefix(overrideDigest1, "sha256:") {
		stopA()
		t.Fatalf("EX01 unkeyed override digest exposed: got %q, legacy %q", overrideDigest1, unkeyedOverride)
	}

	// 2. Both digests must be valid ev1 identifiers under versionsA's "waf-external-source" domain.
	wantGlobalA := versionsA.Digest("waf-external-source", expectedChainBytes(mainRuleContent, overlapSecret, incContent))
	wantOverrideA := versionsA.Digest("waf-external-source", expectedChainBytes(overrideRuleContent, overlapSecret))
	if globalDigest1 != wantGlobalA || overrideDigest1 != wantOverrideA {
		stopA()
		t.Fatalf("digests = (%q, %q), want (%q, %q)", globalDigest1, overrideDigest1, wantGlobalA, wantOverrideA)
	}

	// 3. Replacing the shared low-entropy file in place updates both digests on reload.
	newSecret := "pin-9999\n"
	write("overlap-secret.data", newSecret)
	signalsA <- struct{}{}
	wantGlobalA2 := versionsA.Digest("waf-external-source", expectedChainBytes(mainRuleContent, newSecret, incContent))
	wantOverrideA2 := versionsA.Digest("waf-external-source", expectedChainBytes(overrideRuleContent, newSecret))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cur := fetchA().WAFEffective; cur != nil && cur.Generation > eff1.Generation {
			if cur.Global.ExternalDigest != wantGlobalA2 || cur.Overrides[0].Policy.ExternalDigest != wantOverrideA2 {
				stopA()
				t.Fatalf("post-reload digests = (%q, %q), want (%q, %q)", cur.Global.ExternalDigest, cur.Overrides[0].Policy.ExternalDigest, wantGlobalA2, wantOverrideA2)
			}
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	stopA()

	// 4. Restarting with the SAME durable version context preserves digest equality,
	// whereas restarting with an INDEPENDENT version context produces different_context digests.
	fetchA2, _, stopA2 := runServer(versionsA)
	effA2 := fetchA2().WAFEffective
	stopA2()
	if effA2.Global.ExternalDigest != wantGlobalA2 || versionid.Comparison(effA2.Global.ExternalDigest, wantGlobalA2) != "same_context" {
		t.Fatalf("same-key restart changed digest: got %q, want %q", effA2.Global.ExternalDigest, wantGlobalA2)
	}

	fetchB, _, stopB := runServer(versionsB)
	effB := fetchB().WAFEffective
	stopB()
	if effB.Global.ExternalDigest == wantGlobalA2 || versionid.Comparison(effB.Global.ExternalDigest, wantGlobalA2) != "different_context" {
		t.Fatalf("independent-key restart did not isolate context: got %q vs %q", effB.Global.ExternalDigest, wantGlobalA2)
	}
}

func TestThird653PreparedWAFIdentitySurvivesFileReplacement(t *testing.T) {
	backendCanceled := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") != "1" {
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: start\n\n")
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
		close(backendCanceled)
	}))
	defer backend.Close()
	f, cleanup := minimalFactory(t)
	defer cleanup()
	dir := t.TempDir()
	path := filepath.Join(dir, "blocked clients.txt")
	rulePath := filepath.Join(dir, "rules.conf")
	if err := os.WriteFile(rulePath, []byte(`SecRule REMOTE_ADDR "@ipMatchFromFile blocked clients.txt" "id:10953,phase:1,deny,status:403,nolog"`), 0o600); err != nil {
		t.Fatal(err)
	}
	both := []byte("203.0.113.0/24\n")
	onlyAlice := []byte("127.0.0.0/8\n")
	if err := os.WriteFile(path, both, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.ProxyTarget(backend.URL, freePort(t))
	rid := "private"
	cfg.Servers[0].Locations[0].RouteID = &rid
	cfg.WAF = config.WAFConfig{Enabled: true, Mode: "block", DirectivesFiles: []string{rulePath}}
	initial, err := config.NewCandidate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var replaceAfterPrepare atomic.Bool
	factory := func(ctx context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		handlers, gen, commit, abort, err := f.Prepare(ctx, c)
		if err == nil && replaceAfterPrepare.Swap(false) {
			if writeErr := os.WriteFile(path, both, 0o600); writeErr != nil {
				abort()
				return nil, 0, nil, nil, writeErr
			}
		}
		return handlers, gen, commit, abort, err
	}
	srv := server.New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), f.Log, factory, config.NewTOMLSource(""), func(context.Context, *config.Config) error { return nil })
	srv.WAFSourceDigests = f.WAFSourceDigests
	ready := make(chan struct{})
	srv.OnInitialGenerationReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	reload := make(chan server.ReloadRequest, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	defer func() { cancel(); <-done }()
	<-ready

	client := &http.Client{Timeout: 5 * time.Second}
	request := func(query string) (*http.Response, error) {
		r, _ := http.NewRequest("GET", "http://"+cfg.Servers[0].Listen+"/"+query, nil)
		r.SetBasicAuth("bob", "pw")
		return client.Do(r)
	}
	oldStream, err := request("?stream=1")
	if err != nil {
		t.Fatal(err)
	}
	defer oldStream.Body.Close()
	if oldStream.StatusCode != 200 {
		t.Fatalf("before=%d", oldStream.StatusCode)
	}
	if err = os.WriteFile(path, onlyAlice, 0o600); err != nil {
		t.Fatal(err)
	}
	next, _ := cfg.Clone()
	next.Global.LogLevel = "debug"
	cand, err := config.NewCandidate(next)
	if err != nil {
		t.Fatal(err)
	}
	replaceAfterPrepare.Store(true)
	result := make(chan server.ReloadResult, 1)
	reload <- server.ReloadRequest{Source: server.ReloadSourceAdmin, Candidate: cand, Result: result}
	rel := <-result
	if rel.Outcome != server.ReloadAppliedLive {
		t.Fatalf("reload=%+v", rel)
	}
	after, err := request("")
	if err != nil {
		t.Fatal(err)
	}
	_ = after.Body.Close()
	if after.StatusCode != 403 {
		t.Fatalf("fixture: newly loaded WAF must block the current client, got %d", after.StatusCode)
	}
	select {
	case <-backendCanceled:
	case <-time.After(2200 * time.Millisecond):
		t.Fatalf("prepared WAF changed, but its old stream survived the 2s bound despite wired engine identity; retirements=%+v", rel.EntryRetirements)
	}
}

func TestWAFEvaluationPerScopeOverridesOnlyAndServingBinding(t *testing.T) {
	// Item 10 Subcases 2, 3, 4:
	// 1. Overrides-only deployment: importing a "global" report must NOT bind as "current".
	// 2. Per-route scope evaluation and import binds to the specific route override and rolls up to WAFEffective.Evaluation.
	// 3. POST /api/security/waf/evaluate after same-path external rule file replacement without reload
	//    returns "stale_asset_changed", matching subsequent GET /api/security, and becomes "current" after reload.
	ctx := context.Background()
	tmpDir := t.TempDir()
	ruleFile := filepath.Join(tmpDir, "override-rules.conf")
	ruleV1 := `SecRule REQUEST_HEADERS:X-Probe "@streq v1" "id:109801,phase:1,deny,status:403,log"` + "\n"
	ruleV2 := `SecRule REQUEST_HEADERS:X-Probe "@streq v2" "id:109801,phase:1,deny,status:403,log"` + "\n"
	if err := os.WriteFile(ruleFile, []byte(ruleV1), 0o600); err != nil {
		t.Fatal(err)
	}

	ridAPI := "rt-api-override"
	cfg := &config.Config{
		WAF: config.WAFConfig{Enabled: false},
		Servers: []config.ServerConfig{
			{
				Listen:      "127.0.0.1:0",
				ServerNames: []string{"api.example.test"},
				Locations: []config.LocationConfig{
					{
						RouteID: &ridAPI,
						Match:   config.MatchConfig{Type: "prefix", Path: "/api"},
						Return:  200,
						WAF: &config.WAFConfig{
							Enabled:          true,
							Mode:             "block",
							BlockStatus:      403,
							CRSEnabled:       true,
							RequestBodyLimit: config.Size(1 << 20),
							DirectivesFiles:  []string{ruleFile},
						},
					},
				},
			},
		},
	}

	f, cleanup := minimalFactory(t)
	defer cleanup()
	vctx, err := versionid.New(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.Versions = vctx

	_, _, commit, abort, err := f.Prepare(ctx, provisionedInspection(cfg))
	if err != nil {
		t.Fatalf("Prepare initial: %v", err)
	}
	defer abort()
	_, retire := commit()
	if retire != nil {
		defer retire()
	}

	// Before any evaluation runs, overrides-only node reports "unrun" (never nil).
	effInit := f.WAFEffective()
	if effInit.Global != nil || len(effInit.Overrides) != 1 {
		t.Fatalf("expected overrides-only WAFEffective, got %+v", effInit)
	}
	if effInit.Evaluation == nil || effInit.Evaluation.State != "unrun" {
		t.Fatalf("expected initial overrides-only evaluation state unrun, got %+v", effInit.Evaluation)
	}

	// Generate a valid CLI report under the same durable vctx for the route scope,
	// then tamper its scope to "global" and import it onto the overrides-only node.
	validRep, err := waf.EvaluatePolicy(ctx, ridAPI, *cfg.Servers[0].Locations[0].WAF, nil, nil, vctx)
	if err != nil {
		t.Fatalf("EvaluatePolicy: %v", err)
	}
	globalAttempt := *validRep
	globalAttempt.Summary.Scope = "global"
	globalAttemptBytes, _ := json.Marshal(globalAttempt)
	gotGlobalAttempt, err := f.HandleAdminWAFEvaluate(ctx, globalAttemptBytes)
	if err != nil {
		t.Fatalf("HandleAdminWAFEvaluate globalAttempt: %v", err)
	}
	if gotGlobalAttempt.State == "current" {
		t.Fatalf("overrides-only node falsely bound a global-scope report as current: %+v", gotGlobalAttempt)
	}
	if f.WAFEffective().Evaluation.State != "unrun" {
		t.Fatalf("unmatched global import must not overwrite overrides-only evaluation state, got %+v", f.WAFEffective().Evaluation)
	}

	// Mismatched digest on the valid route scope must also report stale_policy_changed and not bind as current.
	mismatchAttempt := *validRep
	mismatchAttempt.Summary.PolicyDigest = "ev1:wrongctx:deadbeef"
	mismatchBytes, _ := json.Marshal(mismatchAttempt)
	gotMismatch, err := f.HandleAdminWAFEvaluate(ctx, mismatchBytes)
	if err != nil {
		t.Fatalf("HandleAdminWAFEvaluate mismatch: %v", err)
	}
	if gotMismatch.State != "stale_policy_changed" {
		t.Fatalf("mismatched digest import state = %q, want stale_policy_changed", gotMismatch.State)
	}

	// Genuine separate-process report (with same durable vctx) for scope "rt-api-override" binds as "current".
	validBytes, _ := json.Marshal(validRep)
	gotValidImport, err := f.HandleAdminWAFEvaluate(ctx, validBytes)
	if err != nil {
		t.Fatalf("HandleAdminWAFEvaluate validRep: %v", err)
	}
	if gotValidImport.State != "current" || gotValidImport.Source != "supplied_report" {
		t.Fatalf("expected current supplied_report on route override, got %+v", gotValidImport)
	}
	effBound := f.WAFEffective()
	if effBound.Evaluation.State != "current" || effBound.Overrides[0].Evaluation == nil || effBound.Overrides[0].Evaluation.State != "current" {
		t.Fatalf("expected bound current evaluation on override and rollup, got top=%+v ov=%+v", effBound.Evaluation, effBound.Overrides[0].Evaluation)
	}

	// Subcase 3: Replace ruleFile on disk at the SAME path WITHOUT reloading HandlerFactory.
	// A fresh POST /api/security/waf/evaluate must evaluate the new candidate bytes on disk
	// AND rebind against the still-serving v1 generation, returning "stale_asset_changed"
	// consistently on both POST and subsequent GET!
	if err := os.WriteFile(ruleFile, []byte(ruleV2), 0o600); err != nil {
		t.Fatal(err)
	}
	postBodyAfterEdit, _ := json.Marshal(map[string]string{"scope": ridAPI})
	gotPostAfterEdit, err := f.HandleAdminWAFEvaluate(ctx, postBodyAfterEdit)
	if err != nil {
		t.Fatalf("HandleAdminWAFEvaluate after same-path asset edit: %v", err)
	}
	if gotPostAfterEdit.State != "stale_asset_changed" {
		t.Fatalf("POST evaluate after same-path edit without reload = %q, want stale_asset_changed", gotPostAfterEdit.State)
	}
	effAfterEdit := f.WAFEffective()
	if effAfterEdit.Evaluation.State != "stale_asset_changed" || effAfterEdit.Overrides[0].Evaluation.State != "stale_asset_changed" {
		t.Fatalf("GET after same-path edit without reload = top:%q ov:%q, want stale_asset_changed", effAfterEdit.Evaluation.State, effAfterEdit.Overrides[0].Evaluation.State)
	}

	// Now reload HandlerFactory so the serving generation compiles ruleV2 -> evaluation becomes "current"!
	_, _, commit2, abort2, err := f.Prepare(ctx, cfg)
	if err != nil {
		t.Fatalf("Prepare reload: %v", err)
	}
	defer abort2()
	_, retire2 := commit2()
	if retire2 != nil {
		defer retire2()
	}
	effAfterReload := f.WAFEffective()
	if effAfterReload.Evaluation.State != "current" || effAfterReload.Overrides[0].Evaluation.State != "current" {
		t.Fatalf("after reload to evaluated asset bytes, state = top:%q ov:%q, want current", effAfterReload.Evaluation.State, effAfterReload.Overrides[0].Evaluation.State)
	}
}

func TestWAFEvidenceMetricsTransitionOnScrapeWithoutAdminReads(t *testing.T) {
	// Finding 5, Item 4, and Item 5:
	// 1. After a successful reload/evaluation with an external rule file, modifying
	//    that file on disk and scraping ONLY /metrics transitions waf_effective to "stale".
	// 2. Audit sink health transitions (disabled -> ok -> degraded -> ok) refresh on
	//    /metrics scrapes without calling /api/v1/security/posture.
	// 3. Explicit raw/effective provenance survives >64 unrelated candidate builds
	//    and aborted previews without any global ring buffer.
	f, cleanup := minimalFactory(t)
	defer cleanup()

	var auditConfigured atomic.Bool
	var auditFailing atomic.Bool
	f.SetAuditHealthSource(func() (uint64, bool, bool) {
		return 0, auditFailing.Load(), auditConfigured.Load()
	})

	dir := t.TempDir()
	rulePath := filepath.Join(dir, "external.conf")
	initialRule := `SecRule ARGS:probe "@streq bad" "id:107501,phase:1,deny,status:403,log"` + "\n"
	if err := os.WriteFile(rulePath, []byte(initialRule), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		WAF: config.WAFConfig{
			Enabled:         true,
			Mode:            "block",
			BlockStatus:     403,
			DirectivesFiles: []string{rulePath},
		},
		Servers: []config.ServerConfig{
			{
				Listen:    ":8080",
				Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
			},
		},
	}

	_, _, commit, _, err := f.Prepare(context.Background(), provisionedInspection(cfg))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, retire := commit()
	defer retire()

	scrapeMetrics := func() string {
		rec := httptest.NewRecorder()
		f.Metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return rec.Body.String()
	}

	// 1. Initial scrape reports producer="waf_effective",state="ok" 1 and audit_sink state="disabled" 1.
	body1 := scrapeMetrics()
	if !strings.Contains(body1, `jul_security_evidence_state{producer="waf_effective",state="ok"} 1`) {
		t.Fatalf("initial /metrics missing waf_effective ok=1:\n%s", body1)
	}
	if !strings.Contains(body1, `jul_security_evidence_state{producer="audit_sink",state="disabled"} 1`) {
		t.Fatalf("initial /metrics missing audit_sink disabled=1:\n%s", body1)
	}

	// Item 4: Audit sink transitions ok -> degraded -> ok on /metrics scrapes with zero posture reads!
	auditConfigured.Store(true)
	auditFailing.Store(false)
	if !strings.Contains(scrapeMetrics(), `jul_security_evidence_state{producer="audit_sink",state="ok"} 1`) {
		t.Fatal("expected audit_sink state=ok=1 on scrape")
	}
	auditFailing.Store(true)
	if !strings.Contains(scrapeMetrics(), `jul_security_evidence_state{producer="audit_sink",state="degraded"} 1`) {
		t.Fatal("expected audit_sink state=degraded=1 on scrape after failure")
	}
	auditFailing.Store(false)
	if !strings.Contains(scrapeMetrics(), `jul_security_evidence_state{producer="audit_sink",state="ok"} 1`) {
		t.Fatal("expected audit_sink state=ok=1 on scrape after recovery")
	}

	// 2. Modify the external rule file on disk WITHOUT calling any admin/Console endpoint.
	modifiedRule := `SecRule ARGS:probe "@streq worse" "id:107501,phase:1,deny,status:403,log"` + "\n"
	if err := os.WriteFile(rulePath, []byte(modifiedRule), 0o600); err != nil {
		t.Fatal(err)
	}

	// 3. Scraping /metrics alone detects the on-disk asset change and flips state="stale" to 1!
	body2 := scrapeMetrics()
	if !strings.Contains(body2, `jul_security_evidence_state{producer="waf_effective",state="stale"} 1`) {
		t.Fatalf("scrape after external file edit without admin read did not flip waf_effective to stale:\n%s", body2)
	}
	if !strings.Contains(body2, `jul_security_evidence_state{producer="waf_effective",state="ok"} 0`) {
		t.Fatalf("scrape after external file edit left waf_effective ok!=0:\n%s", body2)
	}

	// 4. Disabling or degrading waf_investigation must be reflected on /metrics scrapes.
	f.WAFInvestigation.SetEnabled(false)
	bodyDisabled := scrapeMetrics()
	if !strings.Contains(bodyDisabled, `jul_security_evidence_state{producer="waf_investigation",state="disabled"} 1`) {
		t.Fatalf("scrape with disabled investigation store did not report state=disabled=1:\n%s", bodyDisabled)
	}
	f.WAFInvestigation.SetEnabled(true)
	f.WAFInvestigation.Record(waf.TransactionEvent{
		Outcome:        waf.OutcomeInterruptedRequest,
		Mode:           "block",
		EventTruncated: true,
	})
	bodyDegraded := scrapeMetrics()
	if !strings.Contains(bodyDegraded, `jul_security_evidence_state{producer="waf_investigation",state="degraded"} 1`) {
		t.Fatalf("scrape with truncated investigation event did not report state=degraded=1:\n%s", bodyDegraded)
	}

	// 5. Item 5: Explicit raw/effective provenance through config.WithRawConfig across
	// aborted candidates and >64 unrelated candidate creations before commit.
	t.Setenv("JUL_TEST_POSTURE_ADMIN_SECRET", "resolved-secret-value-9876543210")
	rawSecretCfg := *cfg
	rawSecretCfg.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		Token:   "${env:JUL_TEST_POSTURE_ADMIN_SECRET}",
	}
	candSecret, err := config.NewCandidate(&rawSecretCfg)
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}

	// Aborted preview candidate with literal secret must not affect live posture metrics.
	literalCfg := *cfg
	literalCfg.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		Token:   "literal-secret-in-aborted-candidate",
	}
	candAborted, err := config.NewCandidate(&literalCfg)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, abortPreview, err := f.Prepare(config.WithRawConfig(context.Background(), candAborted.Raw), candAborted.Effective)
	if err != nil {
		t.Fatalf("Prepare aborted candidate: %v", err)
	}
	abortPreview()

	// Stage candSecret, then create 100 (>64) unrelated candidates before committing candSecret.
	_, _, commitSecret, abortSecret, err := f.Prepare(config.WithRawConfig(context.Background(), candSecret.Raw), candSecret.Effective)
	if err != nil {
		t.Fatalf("Prepare candSecret.Effective: %v", err)
	}
	defer abortSecret()
	for i := 0; i < 100; i++ {
		if _, err := config.NewCandidate(&literalCfg); err != nil {
			t.Fatal(err)
		}
	}
	_, retireSecret := commitSecret()
	defer retireSecret()

	bodySecret := scrapeMetrics()
	if !strings.Contains(bodySecret, `jul_security_posture_findings{area="certificates",severity="warning"} 0`) {
		t.Fatalf("secret-backed config published false certificates warning on /metrics after >64 candidates:\n%s", bodySecret)
	}
}

// #697/#698 through the real preparation/publication path: global
// inheritance, per-location overrides and an unaffected sibling, the
// projected compiled state, a response-inspection change and its rollback,
// and a failed compile that leaves the previous policy serving.
func TestWAFArgumentGuardAndResponseInspectionAcrossRoutes(t *testing.T) {
	ctx := context.Background()
	const marker = "ROUTE-LEAK-MARKER"
	var hits sync.Map
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := hits.LoadOrStore(r.URL.Path, new(atomic.Int64))
		n.(*atomic.Int64).Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "payload "+marker)
	}))
	defer origin.Close()
	hitCount := func(p string) int64 {
		if n, ok := hits.Load(p); ok {
			return n.(*atomic.Int64).Load()
		}
		return 0
	}
	leak := `SecRule RESPONSE_BODY "@contains ` + marker + `" "id:1698101,phase:4,deny,status:403,log"`
	build := func(responseCheck bool, raisedRules string) *config.Config {
		loc := func(path string, w *config.WAFConfig) config.LocationConfig {
			return config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: path}, ProxyPass: "http://origin_up", WAF: w}
		}
		return &config.Config{
			WAF:       config.WAFConfig{Enabled: true, Mode: "block", BlockStatus: 403, CRSEnabled: true, ResponseBodyCheck: responseCheck, InlineRules: leak, RequestBodyLimit: config.Size(128 << 10)},
			Upstreams: []config.UpstreamConfig{{Name: "origin_up", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: origin.Listener.Addr().String(), Weight: 1}}}},
			Servers: []config.ServerConfig{{Name: "s", Listen: ":8080", ServerNames: []string{"app.example.test"}, Locations: []config.LocationConfig{
				loc("/inherit", nil),
				loc("/raised", &config.WAFConfig{Enabled: true, Mode: "block", BlockStatus: 403, CRSEnabled: true, InlineRules: raisedRules, RequestBodyLimit: config.Size(128 << 10)}),
				loc("/detect", &config.WAFConfig{Enabled: true, Mode: "detect", BlockStatus: 403, CRSEnabled: true, RequestBodyLimit: config.Size(128 << 10)}),
				loc("/sibling", &config.WAFConfig{Enabled: true, Mode: "block", BlockStatus: 403, InlineRules: `SecRule ARGS:probe "@streq 1" "id:1698102,phase:2,deny,status:403"`, RequestBodyLimit: config.Size(128 << 10)}),
			}}},
		}
	}
	f, cleanup := minimalFactory(t)
	defer cleanup()
	serve := func(cfg *config.Config) (http.Handler, func()) {
		t.Helper()
		hMap, _, commit, _, err := f.Prepare(ctx, provisionedInspection(cfg))
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		_, retire := commit()
		return hMap[":8080"], retire
	}
	form := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf("a%d=v", i)
		}
		return strings.Join(parts, "&")
	}
	post := func(h http.Handler, path string, n int) int {
		body := form(n)
		req := httptest.NewRequest(http.MethodPost, "http://app.example.test"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Content-Length", fmt.Sprint(len(body)))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	get := func(h http.Handler, path string) int {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://app.example.test"+path, nil))
		return rr.Code
	}

	h, retire := serve(build(false, "SecArgumentsLimit 2000"))
	eff := f.WAFEffective()
	if g := eff.Global; g == nil || !g.ArgumentLimitGuard || g.ArgumentLimit != 1000 || g.ResponseBodyInspection || g.ResponseBodyMimeTypes != nil {
		t.Fatalf("global summary = %+v", eff.Global)
	}
	byPath := map[string]*admin.WAFPolicySummary{}
	for _, o := range eff.Overrides {
		byPath[o.Path] = o.Policy
	}
	if p := byPath["/raised"]; p == nil || p.ArgumentLimit != 2000 || !p.ArgumentLimitGuard {
		t.Fatalf("/raised summary = %+v", p)
	}
	if p := byPath["/sibling"]; p == nil || p.ArgumentLimitGuard || p.CRSEnabled {
		t.Fatalf("/sibling summary = %+v", p)
	}

	for path, tc := range map[string]struct {
		n, status int
		forwarded bool
	}{
		"/inherit": {1001, 413, false},
		"/raised":  {1500, 200, true},
		"/detect":  {1001, 200, true},
		"/sibling": {1001, 200, true},
	} {
		before := hitCount(path)
		if got := post(h, path, tc.n); got != tc.status || (hitCount(path) > before) != tc.forwarded {
			t.Fatalf("%s with %d args: status %d forwarded %v, want %d %v", path, tc.n, got, hitCount(path) > before, tc.status, tc.forwarded)
		}
	}
	if got := post(h, "/raised", 2001); got != 413 {
		t.Fatalf("/raised over its own limit: %d", got)
	}
	if got := get(h, "/inherit/page"); got != 200 {
		t.Fatalf("response inspection off: %d", got)
	}
	gen1 := eff.Generation
	digest1 := eff.Global.PolicyDigest

	// Enabling response inspection is a new, published policy.
	h2, retire2 := serve(build(true, "SecArgumentsLimit 2000"))
	eff2 := f.WAFEffective()
	if !eff2.Global.ResponseBodyInspection || len(eff2.Global.ResponseBodyMimeTypes) != 3 || eff2.Global.PolicyDigest == digest1 {
		t.Fatalf("after enabling response inspection: %+v", eff2.Global)
	}
	if got := get(h2, "/inherit/page"); got != 403 {
		t.Fatalf("response inspection on: %d", got)
	}
	if got := get(h2, "/sibling/page"); got != 200 {
		t.Fatalf("sibling route inspected the global response rule: %d", got)
	}

	// A candidate that fails to compile leaves the previous policy serving.
	if _, _, _, _, err := f.Prepare(ctx, build(false, "SecRule ARGS \"@notAnOperator x\" \"id:1698103,phase:2,deny\"")); err == nil {
		t.Fatal("broken override compiled")
	}
	if after := f.WAFEffective(); after.Generation != eff2.Generation || !after.Global.ResponseBodyInspection {
		t.Fatalf("failed candidate changed the serving projection: gen %d", after.Generation)
	}
	if got := get(h2, "/inherit/page"); got != 403 {
		t.Fatalf("serving handler after failed candidate: %d", got)
	}

	// Rolling back the field restores the original compiled policy identity.
	h3, retire3 := serve(build(false, "SecArgumentsLimit 2000"))
	eff3 := f.WAFEffective()
	if eff3.Global.ResponseBodyInspection || eff3.Global.PolicyDigest != digest1 || eff3.Generation == gen1 {
		t.Fatalf("rollback: %+v gen %d", eff3.Global, eff3.Generation)
	}
	if got := get(h3, "/inherit/page"); got != 200 {
		t.Fatalf("after rollback: %d", got)
	}
	for _, r := range []func(){retire, retire2, retire3} {
		if r != nil {
			r()
		}
	}
}
