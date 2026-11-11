// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/server"
	"jul/internal/versionid"
	"jul/internal/waf"
)

// TestWAFSecretPredicateSecurityWorkflow uses the production raw loader,
// resolved Candidate, real routing/Coraza, authenticated admin handler and
// actual Console matcher. No canned serving attribution or effective loader
// can hide a disagreement between the three predicate-identity producers.
func TestWAFSecretPredicateSecurityWorkflow(t *testing.T) {
	const envSecret = "env-predicate-canary-719331"
	const fileSecret = "file-predicate-canary-823342"
	const operatorToken = "operator-snapshot-synthetic-token-12345"
	const adminToken = "admin-snapshot-synthetic-token-12345678"
	t.Setenv("JUL_WAF_SNAPSHOT_KEY", envSecret)
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "predicate-key")
	if err := os.WriteFile(secretPath, []byte(fileSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	adminCfg := config.AdminConfig{
		Enabled: true, Listen: "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{Enabled: true, Principals: []config.AdminPrincipal{
			{Name: "operator", Role: "operator", Token: operatorToken},
			{Name: "admin", Role: "admin", Token: adminToken},
		}},
	}
	cfg := &config.Config{
		Admin: adminCfg,
		WAF: config.WAFConfig{Enabled: true, Mode: "block", BlockStatus: 403,
			InlineRules: `SecRule ARGS:bad "@streq attack" "id:111421,phase:1,deny,status:403,log"`},
		Servers: []config.ServerConfig{{Listen: ":8080", ServerNames: []string{"app.example.test"}}},
	}
	refs := []string{"${env:JUL_WAF_SNAPSHOT_KEY}", "${file:" + filepath.ToSlash(secretPath) + "}"}
	for _, kind := range []string{"header", "query"} {
		for _, ref := range refs {
			match := config.MatchConfig{Type: "prefix", Path: "/api"}
			if kind == "header" {
				match.Methods = []string{"GET"}
				match.Headers = []config.HeaderMatch{{Name: "X-App-Key", Op: "exact", Value: config.String(ref)}}
			} else {
				match.Methods = []string{"POST"}
				match.Query = []config.QueryMatch{{Name: "access_key", Op: "exact", Value: config.String(ref)}}
			}
			cfg.Servers[0].Locations = append(cfg.Servers[0].Locations, config.LocationConfig{Match: match, Return: 200})
		}
	}
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "jul.toml")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	src := config.NewTOMLSource(configPath)
	parsed, err := src.Load()
	if err != nil {
		t.Fatal(err)
	}
	cand, err := config.NewCandidate(parsed)
	if err != nil {
		t.Fatal(err)
	}
	f, cleanup := minimalFactory(t)
	defer cleanup()
	f.Versions = versionid.Process()
	handlers, _, commit, abort, err := f.Prepare(config.WithRawConfig(context.Background(), cand.Raw), cand.Effective)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	_, retire := commit()
	defer retire()

	a := admin.New(cand.Effective.Admin, f.Log, admin.Deps{
		LoadConfig:        src.Load,
		ReadConfigRaw:     func() ([]byte, error) { return os.ReadFile(configPath) },
		EffectiveVersions: f.Versions,
		WAFCompiled:       waf.Compiled,
		WAFEffective:      f.WAFEffective,
		WAFInvestigation:  f.WAFInvestigationSnapshot,
	})
	policy, err := buildRBACPolicy(cand.Effective.Admin)
	if err != nil {
		t.Fatal(err)
	}
	a.UpdatePolicy(policy)
	api := httptest.NewServer(a.Handler())
	defer api.Close()
	call := func(method, path string, body []byte, tokens ...string) (int, []byte) {
		t.Helper()
		r, err := http.NewRequest(method, api.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		token := operatorToken
		if len(tokens) > 0 {
			token = tokens[0]
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		resp, err := api.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), envSecret) || strings.Contains(string(b), fileSecret) {
			t.Fatalf("%s disclosed a predicate secret", path)
		}
		return resp.StatusCode, b
	}
	for i, secret := range []string{envSecret, fileSecret, envSecret, fileSecret} {
		method, target := http.MethodGet, "http://app.example.test/api?bad=attack"
		if i >= 2 {
			method = http.MethodPost
			target += "&access_key=" + secret
		}
		r := httptest.NewRequest(method, target, nil)
		if i < 2 {
			r.Header.Set("X-App-Key", secret)
		}
		w := httptest.NewRecorder()
		handlers[":8080"].ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || w.Header().Get(waf.HeaderWAFEvent) == "" {
			t.Fatalf("route %d did not produce a real WAF rejection: %d", i, w.Code)
		}
	}
	// Reads must use the retained declaration; changing provider contents must
	// neither re-resolve credentials nor prevent investigation of serving events.
	t.Setenv("JUL_WAF_SNAPSHOT_KEY", "not-loaded-env-replacement")
	if err := os.WriteFile(secretPath, []byte("not-loaded-file-replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, securityJSON := call(http.MethodGet, "/api/security", nil)
	if status != http.StatusOK {
		t.Fatalf("security status=%d: %s", status, securityJSON)
	}
	status, eventsJSON := call(http.MethodGet, "/api/v1/waf/events", nil)
	if status != http.StatusOK {
		t.Fatalf("events status=%d: %s", status, eventsJSON)
	}
	var projection admin.SecurityProjection
	var events adminapi.WAFEventsResponse
	if err := json.Unmarshal(securityJSON, &projection); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(eventsJSON, &events); err != nil {
		t.Fatal(err)
	}
	if len(projection.WAFRoutes) != 4 || projection.WAFEffective == nil || len(projection.WAFEffective.ServingRoutes) != 4 || len(events.Events) != 4 {
		t.Fatalf("missing route/event evidence: routes=%d events=%d", len(projection.WAFRoutes), len(events.Events))
	}
	if projection.BaseVersion != server.CanonicalVersion(cand.Raw) || projection.BaseVersion != projection.WAFEffective.ServingBaseVersion {
		t.Fatal("editable and serving revision are inconsistent")
	}
	seen := make(map[string]bool)
	for i, route := range projection.WAFRoutes {
		want := f.Versions.RoutePredicates(cand.Raw.Servers[0].Locations[i].Match)
		if _, ok := versionid.Parse(route.CanonicalPredicates); !ok || route.CanonicalPredicates != want || seen[want] {
			t.Fatalf("route %d identity is not a distinct keyed declaration", i)
		}
		seen[want] = true
		if projection.WAFEffective.ServingRoutes[i].CanonicalPredicates != want {
			t.Fatalf("route %d editable and serving identities disagree", i)
		}
	}
	for _, ev := range events.Events {
		if ev.StalePolicy || ev.Route.CanonicalPredicates != projection.WAFRoutes[ev.Route.MatchOrdinal].CanonicalPredicates {
			t.Fatalf("event for ordinal %d does not bind to its editable declaration", ev.Route.MatchOrdinal)
		}
	}
	// Restore providers only for a new preview candidate, which legitimately
	// resolves its own immutable inputs through the existing lifecycle owner.
	t.Setenv("JUL_WAF_SNAPSHOT_KEY", envSecret)
	if err := os.WriteFile(secretPath, []byte(fileSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("console handoff and preview", func(t *testing.T) {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Skip("Console handoff requires Node; backend identity assertions ran above")
		}
		payload, err := json.Marshal(map[string]any{"security": projection, "events": events.Events})
		if err != nil {
			t.Fatal(err)
		}
		// The deadline only guards against a hung Node process. Node's start-up
		// with type stripping can take well over 15 s on a contended Windows
		// runner, so it is not a latency assertion.
		const handoffTimeout = 2 * time.Minute
		ctx, cancel := context.WithTimeout(context.Background(), handoffTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, node, "--experimental-strip-types", "--input-type=module", "-e", wafSnapshotConsoleHandoff)
		cmd.WaitDelay = 5 * time.Second
		cmd.Stdin = bytes.NewReader(payload)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		patches, err := cmd.Output()
		if ctx.Err() != nil {
			t.Fatalf("actual Console handoff timed out after %s: %v: %s", handoffTimeout, err, stderr.String())
		}
		if err != nil {
			t.Fatalf("actual Console handoff failed: %v: %s", err, stderr.String())
		}
		var bodies []json.RawMessage
		if err := json.Unmarshal(patches, &bodies); err != nil || len(bodies) != 4 {
			t.Fatalf("invalid Console patch output: %v", err)
		}
		if status, _ := call(http.MethodPost, "/api/config/patch/preview", bodies[0]); status != http.StatusForbidden {
			t.Fatalf("operator without config:raw preview status=%d, want 403", status)
		}
		for _, body := range bodies {
			status, out := call(http.MethodPost, "/api/config/patch/preview", body, adminToken)
			if status != http.StatusOK || !bytes.Contains(out, []byte(`"valid":true`)) {
				t.Fatalf("Console patch preview failed: %d %s", status, out)
			}
		}
		// A later persisted reorder leaves the old generation serving. The
		// already-created patch must now fail CAS, never silently change siblings.
		staged, err := cand.Raw.Clone()
		if err != nil {
			t.Fatal(err)
		}
		locs := staged.Servers[0].Locations
		locs[0], locs[1] = locs[1], locs[0]
		stagedBytes, err := config.Marshal(staged)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, stagedBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		status, out := call(http.MethodPost, "/api/config/patch/preview", bodies[0], adminToken)
		if status != http.StatusConflict {
			t.Fatalf("stale Console patch escaped CAS: %d %s", status, out)
		}
		status, out = call(http.MethodGet, "/api/security", nil)
		var changed admin.SecurityProjection
		if status != http.StatusOK || json.Unmarshal(out, &changed) != nil || changed.BaseVersion == projection.BaseVersion || changed.WAFRoutes[0].CanonicalPredicates != projection.WAFRoutes[1].CanonicalPredicates {
			t.Fatal("staged projection did not move selectors and version together")
		}
	})
}

// Imports the actual production Console helper; only UI-independent type
// annotations are stripped by Node. No translated copy of its matching logic.
const wafSnapshotConsoleHandoff = `
import {readFileSync} from 'node:fs';
import {matchLocationWAFForEvent} from '../admin/ui/src/lib/wafEffective.ts';
const {security,events} = JSON.parse(readFileSync(0,'utf8'));
const patches = events.map(ev => {
  const result = matchLocationWAFForEvent(ev, security.location_wafs ?? [], security);
  if (result.kind !== 'matched' || result.target.match_ordinal !== ev.route.match_ordinal)
    throw new Error('current secret-backed route was not matched: '+JSON.stringify(result));
  const r = result.target;
  const staged = {...security,base_version:'changed-after-event'};
  if (matchLocationWAFForEvent(ev,security.location_wafs ?? [],staged).kind !== 'rejected')
    throw new Error('staged revision was not rejected');
  const changed = structuredClone(security);
  changed.waf_routes[r.match_ordinal].canonical_predicates += 'changed';
  if (matchLocationWAFForEvent(ev,security.location_wafs ?? [],changed).kind !== 'rejected')
    throw new Error('changed predicate was not rejected');
  return {base_version:security.base_version,ops:[{
    op:'location_waf_set',listen:r.listen,server_names:r.server_names ?? [],
    match_type:r.match_type,path:r.path,match_ordinal:r.match_ordinal,
    selector_version:r.selector_version,
    waf:{enabled:true,mode:r.mode,crs_enabled:r.crs_enabled,inline_rules:r.inline_rules,
      exceptions:[{id:'snapshot-tune',rule_id:111421,scope:'narrow_target',target:'ARGS:bad'}]}
  }]};
});
process.stdout.write(JSON.stringify(patches));
`

func TestDeclaredWAFRouteMatchEmbeddingFallback(t *testing.T) {
	m := config.MatchConfig{Methods: []string{"POST"}}
	for _, raw := range []*config.Config{nil, {}, {Servers: []config.ServerConfig{{}}}} {
		if got := declaredWAFRouteMatch(raw, 0, 0, m); fmt.Sprint(got.Methods) != "[POST]" {
			t.Fatalf("embedding fallback changed match: %+v", got)
		}
	}
}
