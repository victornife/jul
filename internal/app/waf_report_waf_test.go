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
