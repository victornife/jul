// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corazawaf/coraza/v3/types"

	"jul/internal/config"
)

func TestWAFSeparateBlockingAndDetectionParanoia(t *testing.T) {
	// Paranoia 1 blocks standard SQLi (942100), while Paranoia 2 adds rules
	// such as 920271 (non-printable ASCII in ARGS/URI) that only score in PL2.
	cfgBlock1Detect2 := config.WAFConfig{
		Enabled:           true,
		Mode:              "block",
		BlockStatus:       403,
		CRSEnabled:        true,
		BlockingParanoia:  1,
		DetectionParanoia: 2,
	}
	store := NewInvestigationStore(32, time.Minute)
	var loggedRules atomic.Int32
	fw, err := New(context.Background(), cfgBlock1Detect2, Options{
		Investigation: store,
		Hooks: Hooks{
			OnEvent: func(_, _ string) {
				loggedRules.Add(1)
			},
		},
	})
	if err != nil {
		t.Fatalf("New(block=1, detect=2): %v", err)
	}
	info := fw.Info()
	if info.BlockingParanoia != 1 || info.DetectionParanoia != 2 || info.ParanoiaDefault {
		t.Fatalf("unexpected PolicyInfo paranoia: %+v", info)
	}

	var originHits atomic.Int32
	h := fw.RouteMiddleware(RouteAttribution{RouteID: "rt-paranoia", Scope: "global"}, func() uint64 { return 1 })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			originHits.Add(1)
			w.WriteHeader(http.StatusOK)
		}),
	)

	// 1. PL1 attack (SQLi) is still blocked when blocking_paranoia = 1.
	reqPL1 := httptest.NewRequest(http.MethodGet, "http://example.test/items?id=1%27%20OR%20%271%27=%271", nil)
	reqPL1.Host = "example.test"
	recPL1 := httptest.NewRecorder()
	h.ServeHTTP(recPL1, reqPL1)
	if recPL1.Code != http.StatusForbidden {
		t.Fatalf("PL1 SQLi status = %d, want 403", recPL1.Code)
	}
	if originHits.Load() != 0 {
		t.Fatalf("PL1 SQLi reached origin %d times", originHits.Load())
	}

	// 2. PL2-only payload (URL-encoded BEL control char %07 triggers 920271 at PL2)
	// is logged and recorded as matched_observed, NOT blocked, when blocking=1, detection=2.
	loggedRules.Store(0)
	reqPL2 := httptest.NewRequest(http.MethodGet, "http://example.test/search?q=hello%07world", nil)
	reqPL2.Host = "example.test"
	recPL2 := httptest.NewRecorder()
	h.ServeHTTP(recPL2, reqPL2)
	if recPL2.Code != http.StatusOK {
		t.Fatalf("PL2-only payload with blocking_paranoia=1 status = %d, want 200", recPL2.Code)
	}
	if originHits.Load() != 1 {
		t.Fatalf("PL2-only payload should reach origin once, got %d", originHits.Load())
	}
	if loggedRules.Load() == 0 {
		t.Fatal("expected PL2 detection rule match to be logged when detection_paranoia=2")
	}

	// 3. Promoting blocking_paranoia to 2 blocks the exact same PL2 payload.
	cfgBlock2 := cfgBlock1Detect2
	cfgBlock2.BlockingParanoia = 2
	fw2, err := New(context.Background(), cfgBlock2, Options{})
	if err != nil {
		t.Fatalf("New(block=2, detect=2): %v", err)
	}
	h2 := fw2.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	reqPL2Block := httptest.NewRequest(http.MethodGet, "http://example.test/search?q=hello%07world", nil)
	reqPL2Block.Host = "example.test"
	recPL2Block := httptest.NewRecorder()
	h2.ServeHTTP(recPL2Block, reqPL2Block)
	if recPL2Block.Code != http.StatusForbidden {
		t.Fatalf("PL2 payload with blocking_paranoia=2 status = %d, want 403", recPL2Block.Code)
	}
}

func TestWAFExceptionNarrowFieldPreservesNeighboringProtection(t *testing.T) {
	// Custom deterministic rules + CRS to prove narrow target + method scoping:
	// Rule 100101 checks ARGS for "SELECT_FROM_USERS" (SQLi-like keyword)
	// Rule 100102 checks ARGS for "<script>" (XSS-like keyword)
	cfg := config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		InlineRules: strings.Join([]string{
			`SecRule ARGS "@contains SELECT_FROM_USERS" "id:100101,phase:2,deny,status:403,log,msg:'SQL keyword in arg'"`,
			`SecRule ARGS "@contains <script>" "id:100102,phase:2,deny,status:403,log,msg:'XSS in arg'"`,
		}, "\n"),
		Exceptions: []config.WAFException{
			{
				ID:        "allow-bio-sql-keyword",
				RuleID:    100101,
				Scope:     "narrow_target",
				Target:    "ARGS:profile_bio",
				Methods:   []string{"POST"},
				Rationale: "Allow SQL documentation snippets in profile_bio on POST",
				Owner:     "secops",
				Reference: "SEC-631",
				CreatedAt: "2026-11-01",
				ReviewAt:  "2025-01-01", // past due -> counted in ExceptionsReviewDue
				Fixtures:  []string{"benign-bio"},
			},
		},
	}

	fw, err := New(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("New with narrow exception: %v", err)
	}
	info := fw.Info()
	if info.ExceptionsCount != 1 || info.ExceptionsReviewDue != 1 || info.Rules.Generated != 1 {
		t.Fatalf("unexpected exception counts in PolicyInfo: %+v", info)
	}

	h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 1. Target field (ARGS:profile_bio) with target rule (100101) on allowed method (POST) -> 200 OK.
	bodyAllowed := "profile_bio=SELECT_FROM_USERS"
	req1 := httptest.NewRequest(http.MethodPost, "http://example.test/profile", strings.NewReader(bodyAllowed))
	req1.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req1.Header.Set("Content-Length", "29")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("narrow exception target POST ARGS:profile_bio status = %d, want 200", rec1.Code)
	}

	// 2. Neighboring field (ARGS:other_field) with same rule (100101) on POST -> still 403 Forbidden!
	bodyOtherField := "other_field=SELECT_FROM_USERS"
	req2 := httptest.NewRequest(http.MethodPut, "http://example.test/profile", strings.NewReader(bodyOtherField))
	req2.Method = http.MethodPost
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.Header.Set("Content-Length", "29")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("neighboring field ARGS:other_field status = %d, want 403", rec2.Code)
	}

	// 3. Same field (ARGS:profile_bio) with neighboring rule (100102 XSS) on POST -> still 403 Forbidden!
	bodyOtherRule := "profile_bio=%3Cscript%3Ealert(1)%3C/script%3E"
	req3 := httptest.NewRequest(http.MethodPost, "http://example.test/profile", strings.NewReader(bodyOtherRule))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req3.Header.Set("Content-Length", "45")
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusForbidden {
		t.Fatalf("neighboring rule 100102 on ARGS:profile_bio status = %d, want 403", rec3.Code)
	}

	// 4. Same field (ARGS:profile_bio) and same rule (100101) on disallowed method (GET) -> still 403 Forbidden!
	req4 := httptest.NewRequest(http.MethodGet, "http://example.test/profile?profile_bio=SELECT_FROM_USERS", nil)
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusForbidden {
		t.Fatalf("disallowed method GET on ARGS:profile_bio status = %d, want 403", rec4.Code)
	}
}

func TestWAFExceptionPhase1Phase2AndChainChildren(t *testing.T) {
	// Prove that Jul's pre-rule phase:1 ctl:ruleRemoveTargetById compilation
	// excludes targets from:
	//   (a) Phase-1 header rules
	//   (b) Chained rules where the target variable is on the chain child
	cfg := config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		InlineRules: strings.Join([]string{
			`SecRule REQUEST_HEADERS:X-Scanner-Test "@streq scanner" "id:100201,phase:1,deny,status:403,log"`,
			`SecRule ARGS:mode "@streq rich" "id:100202,phase:2,deny,status:403,log,chain"`,
			`SecRule ARGS:content|ARGS:title "@contains UNION_SELECT" "t:none"`,
		}, "\n"),
		Exceptions: []config.WAFException{
			{
				ID:      "allow-scanner-header",
				RuleID:  100201,
				Scope:   "narrow_target",
				Target:  "REQUEST_HEADERS:X-Scanner-Test",
				Methods: []string{"GET", "POST"},
			},
			{
				ID:     "allow-chain-child-content",
				RuleID: 100202,
				Scope:  "narrow_target",
				Target: "ARGS:content",
			},
		},
	}

	fw, err := New(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// (a) Phase-1 header rule excluded on GET and POST, but NOT on custom/prefix/suffix/case variants.
	lines, _, err := BuildExceptionDirectives(cfg.Exceptions)
	if err != nil || len(lines) != 2 || !strings.Contains(lines[0], `@rx ^(?:GET|POST)$`) {
		t.Fatalf("unexpected BuildExceptionDirectives lines: %v, err=%v", lines, err)
	}
	for _, allowedMethod := range []string{http.MethodGet, http.MethodPost} {
		reqHeader := httptest.NewRequest(allowedMethod, "http://example.test/check", nil)
		reqHeader.Header.Set("X-Scanner-Test", "scanner")
		recHeader := httptest.NewRecorder()
		h.ServeHTTP(recHeader, reqHeader)
		if recHeader.Code != http.StatusOK {
			t.Fatalf("phase-1 header exception on %s status = %d, want 200", allowedMethod, recHeader.Code)
		}
	}
	for _, disallowedMethod := range []string{"XPOST", "POSTX", "GET_EXTRA", "post", "get", http.MethodDelete, http.MethodPut} {
		reqDisallowed := httptest.NewRequest(disallowedMethod, "http://example.test/check", nil)
		reqDisallowed.Header.Set("X-Scanner-Test", "scanner")
		recDisallowed := httptest.NewRecorder()
		h.ServeHTTP(recDisallowed, reqDisallowed)
		if recDisallowed.Code != http.StatusForbidden {
			t.Fatalf("multi-method exception must NOT match %q: got status %d, want 403", disallowedMethod, recDisallowed.Code)
		}
	}

	// (b1) Chain child target ARGS:content is excluded from rule 100202 -> 200 OK.
	reqChainAllowed := httptest.NewRequest(http.MethodGet, "http://example.test/post?mode=rich&content=UNION_SELECT", nil)
	recChainAllowed := httptest.NewRecorder()
	h.ServeHTTP(recChainAllowed, reqChainAllowed)
	if recChainAllowed.Code != http.StatusOK {
		t.Fatalf("chain child ARGS:content exception status = %d, want 200", recChainAllowed.Code)
	}

	// (b2) Sibling target ARGS:title on the same chain child is still enforced -> 403 Forbidden!
	reqChainBlocked := httptest.NewRequest(http.MethodGet, "http://example.test/post?mode=rich&title=UNION_SELECT", nil)
	recChainBlocked := httptest.NewRecorder()
	h.ServeHTTP(recChainBlocked, reqChainBlocked)
	if recChainBlocked.Code != http.StatusForbidden {
		t.Fatalf("chain child sibling ARGS:title status = %d, want 403", recChainBlocked.Code)
	}
}

func TestWAFExceptionMissingOrCollidingRuleIDRejected(t *testing.T) {
	// 1. Exception referencing a nonexistent rule_id must fail compilation.
	_, err := New(context.Background(), config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		InlineRules: `SecRule ARGS:x "@streq bad" "id:100301,phase:2,deny,status:403"`,
		Exceptions: []config.WAFException{
			{
				RuleID: 999999,
				Scope:  "narrow_target",
				Target: "ARGS:x",
			},
		},
	}, Options{})
	if err == nil || !strings.Contains(err.Error(), "999999") {
		t.Fatalf("expected missing rule_id 999999 error, got %v", err)
	}

	// 2. External rule file colliding with Jul reserved generated ID range (890001..900001) must fail.
	dir := t.TempDir()
	collideFile := filepath.Join(dir, "collide.conf")
	if err := os.WriteFile(collideFile, []byte(`SecAction "id:890001,phase:1,nolog,pass"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = New(context.Background(), config.WAFConfig{
		Enabled:         true,
		Mode:            "block",
		BlockStatus:     403,
		DirectivesFiles: []string{collideFile},
	}, Options{})
	if err == nil || !strings.Contains(err.Error(), "890001") {
		t.Fatalf("expected reserved rule ID collision error, got %v", err)
	}

	// 3. Custom tuning directives in inline_rules are flagged on PolicyInfo.HasCustomTuningDirectives.
	fwCustom, err := New(context.Background(), config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		CRSEnabled:  true,
		InlineRules: `SecRuleRemoveById 942100`,
	}, Options{})
	if err != nil {
		t.Fatalf("New with SecRuleRemoveById: %v", err)
	}
	if !fwCustom.Info().HasCustomTuningDirectives {
		t.Fatal("expected HasCustomTuningDirectives = true when inline_rules contains SecRuleRemoveById")
	}
	_ = fwCustom.Close()

	// 4. Item 6: Exception referencing a rule removed via SecRuleRemoveById (single ID or range) must fail compilation.
	_, err = New(context.Background(), config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		CRSEnabled:  true,
		InlineRules: "SecRuleRemoveById 942100 942140-942160\n",
		Exceptions: []config.WAFException{
			{
				RuleID: 942100,
				Scope:  "narrow_target",
				Target: "ARGS:q",
			},
		},
	}, Options{})
	if err == nil || !strings.Contains(err.Error(), "942100") {
		t.Fatalf("expected removed rule_id 942100 to be rejected, got %v", err)
	}
	_, err = New(context.Background(), config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		CRSEnabled:  true,
		InlineRules: "SecRuleRemoveById 942140-942160\n",
		Exceptions: []config.WAFException{
			{
				RuleID: 942150,
				Scope:  "narrow_target",
				Target: "ARGS:q",
			},
		},
	}, Options{})
	if err == nil || !strings.Contains(err.Error(), "942150") {
		t.Fatalf("expected range-removed rule_id 942150 to be rejected, got %v", err)
	}

	// Item 6: Backslash line continuation on SecRuleRemoveById removes 100101 in Coraza
	// and must reject an exception referencing 100101.
	_, err = New(context.Background(), config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		CRSEnabled:  false,
		InlineRules: "SecRule ARGS \"@contains attack\" \"id:100101,phase:2,deny,status:403\"\nSecRuleRemoveById \\\n    100101\n",
		Exceptions: []config.WAFException{
			{
				RuleID: 100101,
				Scope:  "narrow_target",
				Target: "ARGS:q",
			},
		},
	}, Options{})
	if err == nil || !strings.Contains(err.Error(), "100101") {
		t.Fatalf("expected continuation-removed rule_id 100101 to be rejected, got %v", err)
	}

	// Item 6: Range wider than 100,000 (e.g. 100000-350000) removes 250001 in Coraza
	// and must reject an exception referencing 250001.
	_, err = New(context.Background(), config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		CRSEnabled:  false,
		InlineRules: "SecRule ARGS \"@contains attack\" \"id:250001,phase:2,deny,status:403\"\nSecRuleRemoveById 100000-350000\n",
		Exceptions: []config.WAFException{
			{
				RuleID: 250001,
				Scope:  "narrow_target",
				Target: "ARGS:q",
			},
		},
	}, Options{})
	if err == nil || !strings.Contains(err.Error(), "250001") {
		t.Fatalf("expected wide-range-removed rule_id 250001 to be rejected, got %v", err)
	}

	// Item 6: Remove-before-add (SecRuleRemoveById 100102 followed by SecRule id:100102)
	// leaves rule 100102 in Coraza's final RuleGroup, so an exception referencing 100102 must succeed.
	fwAddedAfterRemove, err := New(context.Background(), config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		CRSEnabled:  false,
		InlineRules: "SecRuleRemoveById 100102\nSecRule ARGS \"@contains attack\" \"id:100102,phase:2,deny,status:403\"\n",
		Exceptions: []config.WAFException{
			{
				RuleID: 100102,
				Scope:  "narrow_target",
				Target: "ARGS:q",
			},
		},
	}, Options{})
	if err != nil {
		t.Fatalf("expected remove-before-add rule_id 100102 exception to succeed, got %v", err)
	}
	_ = fwAddedAfterRemove.Close()

	// N1: Verify compiledRuleIDs fails closed with an explicit compatibility error
	// when given an incompatible coraza.WAF implementation.
	if _, compatErr := compiledRuleIDs(incompatibleCorazaWAF{}); compatErr == nil || !strings.Contains(compatErr.Error(), "incompatible coraza.WAF internal layout") {
		t.Fatalf("expected incompatible layout error, got %v", compatErr)
	}
}

type incompatibleCorazaWAF struct{}

func (incompatibleCorazaWAF) NewTransaction() types.Transaction             { return nil }
func (incompatibleCorazaWAF) NewTransactionWithID(string) types.Transaction { return nil }
