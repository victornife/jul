// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/versionid"
)

// Regressions for #697 (fail closed when argument collection stops at
// SecArgumentsLimit) and #698 (response_body_check governs response-body
// inspection, with deliberate operator overrides winning and reported).

const xssPayload = "<script>alert(document.cookie)</script>"

// formBody returns an application/x-www-form-urlencoded body of exactly n
// arguments, optionally with an XSS payload in the last one.
func formBody(n int, attackLast bool) []byte {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte('&')
		}
		v := "benign"
		if attackLast && i == n-1 {
			v = xssPayload
		}
		fmt.Fprintf(&b, "a%d=%s", i, url.QueryEscape(v))
	}
	return []byte(b.String())
}

// flatJSONBody returns a flat JSON object of exactly n members.
func flatJSONBody(n int, attackLast bool) []byte {
	var b strings.Builder
	b.WriteByte('{')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		v := "benign"
		if attackLast && i == n-1 {
			v = xssPayload
		}
		fmt.Fprintf(&b, "%q:%q", fmt.Sprintf("k%d", i), v)
	}
	b.WriteByte('}')
	return []byte(b.String())
}

var argFormats = []struct {
	name, contentType string
	body              func(int, bool) []byte
}{
	{"urlencoded", "application/x-www-form-urlencoded", formBody},
	{"json", "application/json", flatJSONBody},
}

// assertArgsPostCount proves a fixture's effective argument count with the
// engine itself (no limit in the way), so boundary tests do not rest on an
// assumed parser expansion.
func assertArgsPostCount(t *testing.T, contentType string, body []byte, want int) {
	t.Helper()
	// Without CRS no rule selects the JSON body processor, so the probe does.
	fw := assessmentFirewall(t, config.WAFConfig{InlineRules: "SecArgumentsLimit 100000\n" +
		`SecRule REQUEST_HEADERS:Content-Type "@rx ^application/json" "id:1697000,phase:1,pass,nolog,ctl:requestBodyProcessor=JSON"` + "\n" +
		fmt.Sprintf(`SecRule &ARGS_POST "@eq %d" "id:1697001,phase:2,deny,status:299,nolog"`, want)})
	h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest(http.MethodPost, "http://app.example.test/count", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 299 {
		t.Fatalf("fixture %s does not have exactly %d ARGS_POST", contentType, want)
	}
}

type guardResult struct {
	status, hits int
	tx           []string
	ruleID       int
	would        bool
	source       string
	origin       bool
}

func runGuardRequest(t *testing.T, cfg config.WAFConfig, method, target, contentType string, body []byte) guardResult {
	t.Helper()
	th := newOutcomeTestHarness(t, cfg)
	t.Cleanup(func() { _ = th.fw.Close() })
	var hits atomic.Int64
	h := th.fw.RouteMiddleware(RouteAttribution{RouteID: "r-args", Scope: "global"}, func() uint64 { return 1 })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		}))
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if body != nil {
		// A real client sends Content-Length; CRS 920180 flags a POST without it.
		req.Header.Set("Content-Length", fmt.Sprint(len(body)))
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	_, tx, _, snap := th.snapshot()
	res := guardResult{status: rr.Code, hits: int(hits.Load()), tx: tx}
	if len(snap.Events) > 0 {
		ev := snap.Events[0]
		res.ruleID, res.would, res.origin = ev.InterruptedRuleID, ev.WouldInterrupt, ev.OriginReached
		for _, m := range ev.MatchedRules {
			if m.RuleID == config.WAFArgumentLimitGuardRuleID {
				res.source = m.SourceClass
			}
		}
	}
	return res
}

func assertGuardRejected(t *testing.T, r guardResult) {
	t.Helper()
	if r.status != http.StatusRequestEntityTooLarge || r.hits != 0 || r.origin {
		t.Fatalf("status=%d origin hits=%d reached=%v, want 413 with no origin", r.status, r.hits, r.origin)
	}
	if !slices.Equal(r.tx, []string{"block:interrupted_request"}) || r.ruleID != config.WAFArgumentLimitGuardRuleID || r.source != "generated" {
		t.Fatalf("outcome=%v rule=%d source=%q, want interrupted_request by generated rule 900001", r.tx, r.ruleID, r.source)
	}
}

func assertForwardedClean(t *testing.T, r guardResult) {
	t.Helper()
	if r.status != http.StatusOK || r.hits != 1 || !slices.Equal(r.tx, []string{"block:clean"}) {
		t.Fatalf("status=%d hits=%d outcome=%v, want a clean forwarded request", r.status, r.hits, r.tx)
	}
}

func TestArgumentLimitGuardDefaultBoundaries(t *testing.T) {
	crs := config.WAFConfig{Enabled: true, CRSEnabled: true}
	for _, f := range argFormats {
		t.Run(f.name, func(t *testing.T) {
			assertArgsPostCount(t, f.contentType, f.body(1000, false), 1000)
			assertArgsPostCount(t, f.contentType, f.body(1001, false), 1001)
			assertArgsPostCount(t, f.contentType, f.body(1201, true), 1201)

			assertForwardedClean(t, runGuardRequest(t, crs, http.MethodPost, "http://app.example.test/api", f.contentType, f.body(1000, false)))
			assertGuardRejected(t, runGuardRequest(t, crs, http.MethodPost, "http://app.example.test/api", f.contentType, f.body(1001, false)))
			// The #695 bypass: the attack sits beyond the collected set.
			assertGuardRejected(t, runGuardRequest(t, crs, http.MethodPost, "http://app.example.test/api", f.contentType, f.body(1201, true)))
			// Below the limit CRS itself still sees and blocks the attack.
			below := runGuardRequest(t, crs, http.MethodPost, "http://app.example.test/api", f.contentType, f.body(999, true))
			if below.status != http.StatusForbidden || below.hits != 0 || below.ruleID == config.WAFArgumentLimitGuardRuleID {
				t.Fatalf("below-limit attack: status=%d hits=%d rule=%d, want CRS 403", below.status, below.hits, below.ruleID)
			}
		})
	}
	t.Run("query string", func(t *testing.T) {
		q := string(formBody(1001, false))
		assertGuardRejected(t, runGuardRequest(t, crs, http.MethodGet, "http://app.example.test/search?"+q, "", nil))
		q = string(formBody(1000, false))
		assertForwardedClean(t, runGuardRequest(t, crs, http.MethodGet, "http://app.example.test/search?"+q, "", nil))
	})
}

func TestArgumentLimitGuardModesRemovalAndLimits(t *testing.T) {
	form := "application/x-www-form-urlencoded"
	post := func(t *testing.T, cfg config.WAFConfig, n int, attack bool) guardResult {
		t.Helper()
		return runGuardRequest(t, cfg, http.MethodPost, "http://app.example.test/api", form, formBody(n, attack))
	}

	t.Run("detect mode forwards and records the finding", func(t *testing.T) {
		r := post(t, config.WAFConfig{Enabled: true, CRSEnabled: true, Mode: "detect"}, 1201, false)
		if r.status != http.StatusOK || r.hits != 1 || !slices.Equal(r.tx, []string{"detect:matched_observed"}) {
			t.Fatalf("detect: status=%d hits=%d outcome=%v", r.status, r.hits, r.tx)
		}
		if !r.would || r.ruleID != config.WAFArgumentLimitGuardRuleID || r.source != "generated" {
			t.Fatalf("detect finding: would=%v rule=%d source=%q", r.would, r.ruleID, r.source)
		}
	})

	t.Run("deliberate removal is honoured and reported", func(t *testing.T) {
		cfg := config.WAFConfig{Enabled: true, CRSEnabled: true, InlineRules: "SecRuleRemoveById 900001"}
		fw := assessmentFirewall(t, cfg)
		if info := fw.Info(); info.ArgumentLimitGuard || info.Rules.Generated != 1 || info.Rules.Inline != 0 {
			t.Fatalf("removed guard reported as guard=%v rules=%+v", info.ArgumentLimitGuard, info.Rules)
		}
		// Removal restores the engine's partial inspection: documented, not hidden.
		r := post(t, cfg, 1201, true)
		if r.status != http.StatusOK || r.hits != 1 {
			t.Fatalf("removed guard: status=%d hits=%d", r.status, r.hits)
		}
	})

	t.Run("interim operator guard compiles and is superseded", func(t *testing.T) {
		interim := `SecRule ARGUMENTS_LIMIT_REACHED "@eq 1" "id:1695190,phase:2,deny,status:413,log"`
		cfg := config.WAFConfig{Enabled: true, CRSEnabled: true, InlineRules: interim}
		if !assessmentFirewall(t, cfg).Info().ArgumentLimitGuard {
			t.Fatal("generated guard not active alongside the interim guard")
		}
		assertGuardRejected(t, post(t, cfg, 1201, true))
		cfg.InlineRules = "SecRuleRemoveById 900001\n" + interim
		r := post(t, cfg, 1201, true)
		if r.status != http.StatusRequestEntityTooLarge || r.hits != 0 || r.ruleID != 1695190 {
			t.Fatalf("interim-only: status=%d hits=%d rule=%d", r.status, r.hits, r.ruleID)
		}
	})

	t.Run("custom SecArgumentsLimit is respected", func(t *testing.T) {
		low := config.WAFConfig{Enabled: true, CRSEnabled: true, InlineRules: "SecArgumentsLimit 500"}
		if got := assessmentFirewall(t, low).Info().ArgumentLimit; got != 500 {
			t.Fatalf("compiled argument limit = %d, want 500", got)
		}
		assertForwardedClean(t, post(t, low, 500, false))
		assertGuardRejected(t, post(t, low, 501, false))

		high := config.WAFConfig{Enabled: true, CRSEnabled: true, InlineRules: "SecArgumentsLimit 2000"}
		assertForwardedClean(t, post(t, high, 1500, false))
		if r := post(t, high, 1500, true); r.status != http.StatusForbidden || r.hits != 0 {
			t.Fatalf("raised limit must let CRS see argument 1500: status=%d hits=%d", r.status, r.hits)
		}
		assertGuardRejected(t, post(t, high, 2001, false))
	})

	t.Run("argument count and request bytes stay distinct", func(t *testing.T) {
		cfg := config.WAFConfig{Enabled: true, CRSEnabled: true, RequestBodyLimit: config.Size(4 << 10)}
		r := runGuardRequest(t, cfg, http.MethodPost, "http://app.example.test/api", form, []byte("a="+strings.Repeat("b", 8<<10)))
		if r.status != http.StatusRequestEntityTooLarge || !slices.Equal(r.tx, []string{"block:rejected_body_limit"}) || r.ruleID != 0 {
			t.Fatalf("byte limit: status=%d outcome=%v rule=%d", r.status, r.tx, r.ruleID)
		}
	})

	t.Run("non-CRS policy has no generated guard", func(t *testing.T) {
		cfg := config.WAFConfig{Enabled: true, InlineRules: `SecRule ARGS "@contains <script>" "id:1697002,phase:2,deny,status:403"`}
		info := assessmentFirewall(t, cfg).Info()
		if info.ArgumentLimitGuard || info.Rules.Generated != 0 || info.ArgumentLimit != 1000 {
			t.Fatalf("non-CRS policy info = %+v", info)
		}
	})
}

func TestBuildDirectivesGeneratedOrder(t *testing.T) {
	cfg := config.WAFConfig{Enabled: true, Mode: "detect", CRSEnabled: true, Paranoia: 2, DirectivesFiles: []string{"/rules/a.conf"}, InlineRules: "SecAction \"id:1,phase:1,pass,nolog\""}
	d, err := buildDirectives(cfg)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"Include @crs-setup.conf.example", "id:900000", "Include @owasp_crs/*.conf", "id:900001", "SecResponseBodyAccess Off", "SecResponseBodyMimeType text/plain text/html text/xml", "Include /rules/a.conf", "id:1,phase:1", "SecRuleEngine DetectionOnly"}
	last := -1
	for _, want := range order {
		i := strings.Index(d, want)
		if i <= last {
			t.Fatalf("%q out of order in:\n%s", want, d)
		}
		last = i
	}
	cfg.CRSEnabled, cfg.ResponseBodyCheck = false, true
	if d, _ = buildDirectives(cfg); strings.Contains(d, "900001") || !strings.Contains(d, "SecResponseBodyAccess On\n") {
		t.Fatalf("non-CRS directives:\n%s", d)
	}
}

// --- #698 response-body inspection -------------------------------------

const leakMarker = "RESPONSE-LEAK-MARKER"

var leakRule = `SecRule RESPONSE_BODY "@contains ` + leakMarker + `" "id:1698001,phase:4,deny,status:403,log"`

func responseStatus(t *testing.T, fw *Firewall, contentType string, knownLength bool) int {
	t.Helper()
	body := strings.Repeat("x", 512) + leakMarker
	h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		if knownLength {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		}
		_, _ = io.WriteString(w, body)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://app.example.test/page", nil))
	return rr.Code
}

func TestResponseBodyInspectionMatrix(t *testing.T) {
	for _, crs := range []bool{false, true} {
		for _, check := range []bool{false, true} {
			t.Run(fmt.Sprintf("crs=%v check=%v", crs, check), func(t *testing.T) {
				fw := assessmentFirewall(t, config.WAFConfig{CRSEnabled: crs, ResponseBodyCheck: check, InlineRules: leakRule})
				info := fw.Info()
				if info.ResponseBodyInspection != check || info.ResponseBodyOperatorOverride {
					t.Fatalf("info: inspection=%v override=%v", info.ResponseBodyInspection, info.ResponseBodyOperatorOverride)
				}
				if check != slices.Equal(info.ResponseBodyMimeTypes, DefaultResponseBodyMimeTypes) {
					t.Fatalf("reported MIME = %v", info.ResponseBodyMimeTypes)
				}
				want := http.StatusOK
				if check {
					want = http.StatusForbidden
				}
				for _, ct := range []string{"text/plain", "text/html; charset=utf-8", "text/xml"} {
					for _, known := range []bool{true, false} {
						if got := responseStatus(t, fw, ct, known); got != want {
							t.Fatalf("%s known-length=%v: status %d, want %d", ct, known, got, want)
						}
					}
				}
				for _, ct := range []string{"application/json", "application/octet-stream", "text/event-stream"} {
					if got := responseStatus(t, fw, ct, true); got != http.StatusOK {
						t.Fatalf("out-of-scope %s inspected: status %d", ct, got)
					}
				}
			})
		}
	}
}

func TestResponseBodyInspectionOperatorOverrides(t *testing.T) {
	dir := t.TempDir()
	file := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	offFile := file("off.conf", "SecResponseBodyAccess Off\n")
	onFile := file("on.conf", "SecResponseBodyAccess On\n")
	jsonFile := file("json.conf", "SecResponseBodyMimeType application/json\n")

	cases := []struct {
		name      string
		check     bool
		files     []string
		inline    string
		access    bool
		mimes     []string
		override  bool
		statusFor map[string]int
	}{
		{"inline Off beats check=true", true, nil, "SecResponseBodyAccess Off", false, nil, true, map[string]int{"text/plain": 200}},
		{"inline On beats check=false", false, nil, "SecResponseBodyAccess On", true, DefaultResponseBodyMimeTypes, true, map[string]int{"text/plain": 403, "application/json": 200}},
		{"file Off beats check=true", true, []string{offFile}, "", false, nil, true, map[string]int{"text/plain": 200}},
		{"file On beats check=false", false, []string{onFile}, "", true, DefaultResponseBodyMimeTypes, true, map[string]int{"text/plain": 403}},
		{"inline MIME change", true, nil, "SecResponseBodyMimeType application/json", true, []string{"application/json"}, true, map[string]int{"application/json": 403, "text/plain": 200}},
		{"inline MIME clear", true, nil, "SecResponseBodyMimeTypesClear", true, nil, true, map[string]int{"text/plain": 200, "application/json": 200}},
		{"inline after included file wins", true, []string{jsonFile}, "SecResponseBodyMimeType text/html", true, []string{"text/html"}, true, map[string]int{"text/html": 403, "application/json": 200}},
		{"included file alone applies", true, []string{jsonFile}, "", true, []string{"application/json"}, true, map[string]int{"application/json": 403, "text/plain": 200}},
	}
	for _, crs := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("crs=%v %s", crs, tc.name), func(t *testing.T) {
				fw := assessmentFirewall(t, config.WAFConfig{CRSEnabled: crs, ResponseBodyCheck: tc.check, DirectivesFiles: tc.files, InlineRules: tc.inline + "\n" + leakRule})
				info := fw.Info()
				if info.ResponseBodyInspection != tc.access || !slices.Equal(info.ResponseBodyMimeTypes, tc.mimes) || info.ResponseBodyOperatorOverride != tc.override {
					t.Fatalf("info: access=%v mimes=%v override=%v", info.ResponseBodyInspection, info.ResponseBodyMimeTypes, info.ResponseBodyOperatorOverride)
				}
				for ct, want := range tc.statusFor {
					if got := responseStatus(t, fw, ct, true); got != want {
						t.Fatalf("%s: status %d, want %d", ct, got, want)
					}
				}
			})
		}
	}
}

func TestResponseInspectionOffKeepsRequestAndHeaderInspection(t *testing.T) {
	fw := assessmentFirewall(t, config.WAFConfig{CRSEnabled: true, InlineRules: leakRule + "\n" +
		`SecRule RESPONSE_HEADERS:X-Leak "@streq yes" "id:1698002,phase:3,deny,status:403,log"`})
	hits := 0
	h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/plain")
		if r.URL.Query().Get("hdr") == "1" {
			w.Header().Set("X-Leak", "yes")
		}
		_, _ = io.WriteString(w, leakMarker)
	}))
	do := func(target string) int {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		return rr.Code
	}
	if got := do("http://app.example.test/page"); got != http.StatusOK {
		t.Fatalf("body with marker and inspection off: %d", got)
	}
	if got := do("http://app.example.test/page?hdr=1"); got != http.StatusForbidden {
		t.Fatalf("response-header rule with body inspection off: %d", got)
	}
	before := hits
	if got := do("http://app.example.test/page?q=" + url.QueryEscape(xssPayload)); got != http.StatusForbidden || hits != before {
		t.Fatalf("request attack with response inspection off: status %d, origin hits %d->%d", got, before, hits)
	}
}

// With response inspection off, a flushed chunk reaches a real TCP client
// while the handler is still blocked: the response is streamed, not held in
// an inspection buffer.
func TestResponseInspectionOffStreamsWithoutBuffering(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.WAFConfig
	}{
		{"crs check=false", config.WAFConfig{CRSEnabled: true}},
		{"check=true operator Off", config.WAFConfig{CRSEnabled: true, ResponseBodyCheck: true, InlineRules: "SecResponseBodyAccess Off"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fw := assessmentFirewall(t, tc.cfg)
			release := make(chan struct{})
			srv := httptest.NewServer(fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, "FIRST_CHUNK\n")
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, "TAIL\n")
			})))
			defer srv.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			resp, err := srv.Client().Get(srv.URL + "/stream")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			first := make([]byte, len("FIRST_CHUNK\n"))
			done := make(chan error, 1)
			go func() { _, err := io.ReadFull(resp.Body, first); done <- err }()
			select {
			case err := <-done:
				if err != nil || string(first) != "FIRST_CHUNK\n" {
					t.Fatalf("first chunk = %q err=%v", first, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("first chunk withheld while the handler is blocked: response was buffered")
			}
			close(release)
			if rest, _ := io.ReadAll(resp.Body); string(rest) != "TAIL\n" {
				t.Fatalf("tail = %q", rest)
			}
		})
	}
}

// --- policy identity and evaluation staleness ---------------------------

func TestGeneratedPolicyChangeMakesEvaluationStale(t *testing.T) {
	vctx := versionid.Process()
	build := func(cfg config.WAFConfig) *Firewall {
		t.Helper()
		cfg.Enabled = true
		applyTestDefaults(&cfg)
		fw, err := New(context.Background(), cfg, Options{Versions: vctx})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = fw.Close() })
		return fw
	}
	base := build(config.WAFConfig{CRSEnabled: true})
	same := build(config.WAFConfig{CRSEnabled: true})
	if base.Info().PolicyDigest != same.Info().PolicyDigest || base.RawPolicyDigest() != same.RawPolicyDigest() {
		t.Fatal("identical policies have different identities")
	}
	if !strings.HasPrefix(base.Info().PolicyDigest, "ev1:") {
		t.Fatalf("policy digest is not keyed: %q", base.Info().PolicyDigest)
	}
	summary := &WAFEvaluationSummary{State: "current", PolicyDigest: base.Info().PolicyDigest, EngineVersion: EngineVersion(), EmbeddedCRSVersion: EmbeddedCRSVersion(), TotalCases: 1, PassedCases: 1}
	if got := VerifyEvaluationBinding(summary, base.RawPolicyDigest(), "", same.Info(), same.RawPolicyDigest(), ""); got.State != "current" {
		t.Fatalf("same policy evaluation = %q", got.State)
	}

	// The pre-fix canonical policy (no generated-directives version, guard or
	// MIME framing) with an unchanged TOML and engine is no longer current.
	legacy := canonicalPolicyBytes(config.WAFConfig{Enabled: true, CRSEnabled: true}, base.Info(), [32]byte{})
	var kept []string
	for _, line := range strings.SplitAfter(string(legacy), "\n") {
		if !strings.HasPrefix(line, "generated_directives=") && !strings.HasPrefix(line, "response_body_mime_types=") &&
			!strings.HasPrefix(line, "argument_limit=") && !strings.HasPrefix(line, "argument_limit_guard=") {
			kept = append(kept, line)
		}
	}
	legacySum := sha256.Sum256([]byte(strings.Join(kept, "")))
	if got := VerifyEvaluationBinding(summary, hex.EncodeToString(legacySum[:]), "", base.Info(), base.RawPolicyDigest(), ""); got.State != "stale_policy_changed" {
		t.Fatalf("pre-fix evaluation = %q, want stale_policy_changed", got.State)
	}

	for name, cfg := range map[string]config.WAFConfig{
		"response_body_check": {CRSEnabled: true, ResponseBodyCheck: true},
		"guard removed":       {CRSEnabled: true, InlineRules: "SecRuleRemoveById 900001"},
		"argument limit":      {CRSEnabled: true, InlineRules: "SecArgumentsLimit 2000"},
	} {
		other := build(cfg)
		if other.Info().PolicyDigest == base.Info().PolicyDigest {
			t.Fatalf("%s: policy digest unchanged", name)
		}
		if got := VerifyEvaluationBinding(summary, base.RawPolicyDigest(), "", other.Info(), other.RawPolicyDigest(), ""); got.State != "stale_policy_changed" {
			t.Fatalf("%s: evaluation = %q, want stale_policy_changed", name, got.State)
		}
	}
}

func TestCompiledEngineStateAdapter(t *testing.T) {
	if _, err := compiledEngineState(nil); err == nil || !strings.Contains(err.Error(), "incompatible coraza.WAF internal layout") {
		t.Fatalf("nil WAF: %v", err)
	}
	if _, err := compiledEngineState(incompatibleCorazaWAF{}); err == nil || !strings.Contains(err.Error(), "incompatible coraza.WAF internal layout") {
		t.Fatalf("incompatible WAF: %v", err)
	}
	if _, err := compiledEngineState((*incompatibleCorazaWAF)(nil)); err == nil || !strings.Contains(err.Error(), "nil wrapper") {
		t.Fatalf("typed nil WAF: %v", err)
	}
	if _, err := compiledEngineState(engineWithoutSettings{waf: &struct{ Rules struct{} }{}}); err == nil || !strings.Contains(err.Error(), "missing response-body or argument-limit settings") {
		t.Fatalf("engine without settings: %v", err)
	}
	long := strings.Repeat("a", 150)
	in := make([]string, 20)
	for i := range in {
		in[i] = long
	}
	out := boundedMimeTypes(in)
	if len(out) != maxReportedMimeTypes || len(out[0]) != maxReportedMimeTypeLen {
		t.Fatalf("bounded = %d entries of %d", len(out), len(out[0]))
	}
	if boundedMimeTypes(nil) != nil {
		t.Fatal("empty MIME list not nil")
	}
}

// Concurrent transactions on one compiled policy and a rebuilt policy do not
// share the per-transaction ARGUMENTS_LIMIT_REACHED flag.
func TestArgumentLimitGuardConcurrentTransactions(t *testing.T) {
	fw := assessmentFirewall(t, config.WAFConfig{CRSEnabled: true})
	next := assessmentFirewall(t, config.WAFConfig{CRSEnabled: true, ResponseBodyCheck: true})
	var hits atomic.Int64
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	handlers := []http.Handler{fw.Middleware()(origin), next.Middleware()(origin)}
	var wg sync.WaitGroup
	var bad atomic.Int64
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			over := i%2 == 0
			n := 10
			if over {
				n = 1001
			}
			req := httptest.NewRequest(http.MethodPost, "http://app.example.test/api", bytes.NewReader(formBody(n, false)))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			handlers[i%4/2].ServeHTTP(rr, req)
			if (over && rr.Code != http.StatusRequestEntityTooLarge) || (!over && rr.Code != http.StatusOK) {
				bad.Add(1)
			}
		}()
	}
	wg.Wait()
	if bad.Load() != 0 || hits.Load() != 20 {
		t.Fatalf("mismatched outcomes=%d origin hits=%d, want 0 and 20", bad.Load(), hits.Load())
	}
}

// engineWithoutSettings mimics the wrapper layout with an engine struct that
// lacks the response-body and argument-limit fields.
type engineWithoutSettings struct {
	incompatibleCorazaWAF
	waf *struct{ Rules struct{} }
}
