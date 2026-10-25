// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/rbac"
)

type errScopedPurger struct{}

func (errScopedPurger) Purge()            {}
func (errScopedPurger) Delete(key string) {}
func (errScopedPurger) PreviewScope(sel cache.NormalizedSelector, opts cache.PreviewOptions) cache.PurgePreview {
	return cache.PurgePreview{Selector: sel, CountAccuracy: "exact"}
}
func (errScopedPurger) InvalidateScope(ctx context.Context, sel cache.NormalizedSelector, opts cache.InvalidateOptions) (cache.InvalidateResult, error) {
	return cache.InvalidateResult{}, errors.New("simulated engine scan limit exceeded")
}

func postV1JSON(t *testing.T, s *Server, path, token, idemKey string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	s.routes().ServeHTTP(rr, req)
	return rr
}

func TestV1CachePurgeWorkflowRBACBindingIdempotencyAndSecretSafety(t *testing.T) {
	t.Parallel()

	diskDir := t.TempDir()
	stripTags := true
	cacheCfg := config.CacheConfig{
		Enabled:         true,
		MemoryMaxSize:   config.Size(2 << 20),
		DiskPath:        diskDir,
		DiskMaxSize:     config.Size(8 << 20),
		DefaultTTL:      config.Duration(5 * time.Minute),
		TagHeaders:      []string{"Cache-Tag"},
		StripTagHeaders: &stripTags,
	}
	c, err := cache.New(cacheCfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}

	origin := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Cache-Tag", "article:42")
		_, _ = io.WriteString(w, "cached-article-42")
	}))

	// Warm an entry carrying a sensitive query parameter and grouping tag `article:42`.
	warmReq := httptest.NewRequest(http.MethodGet, "http://portal.example.com/articles/42?token=TOP_SECRET_QUERY_999", nil)
	warmReq.Host = "portal.example.com"
	warmRec := httptest.NewRecorder()
	origin.ServeHTTP(warmRec, warmReq)
	if warmRec.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected warm MISS, got %q", warmRec.Header().Get("X-Cache"))
	}

	const (
		adminToken  = "admin-token-32-chars-padded-621-aa"
		opToken     = "operator-token-32-chars-padded-621"
		op2Token    = "operator2-token-32-chars-padded-62"
		viewerToken = "viewer-token-32-chars-padded-621-v"
	)

	s := newTestServer(t, config.AdminConfig{}, Deps{
		Cache: c,
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{Cache: cacheCfg}, nil
		},
		Authority: func() ConfigAuthorityStatus {
			return ConfigAuthorityStatus{
				Mode:   "file_owned",
				Source: "explicit",
			}
		},
	})

	policy, err := rbac.Build(true, rbac.RoleAdmin, nil, []rbac.PrincipalDef{
		{Name: "root", Role: rbac.RoleAdmin, Token: adminToken},
		{Name: "ops-1", Role: rbac.RoleOperator, Token: opToken},
		{Name: "ops-2", Role: rbac.RoleOperator, Token: op2Token},
		{Name: "auditor-view", Role: rbac.RoleViewer, Token: viewerToken},
	}, "", time.Now())
	if err != nil {
		t.Fatalf("rbac.Build: %v", err)
	}
	s.UpdatePolicy(policy)

	tagSelector := adminapi.CacheSelector{
		Kind: "tag",
		Host: "portal.example.com",
		Tags: []string{"article:42"},
	}

	// 1. Unauthenticated request is rejected with 401.
	unauthRR := postV1JSON(t, s, "/api/v1/cache/purge/preview", "", "", adminapi.CachePurgePreviewRequest{
		Selector: tagSelector,
	})
	if unauthRR.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", unauthRR.Code)
	}

	// 2. Viewer (status:read, no cache:purge) CANNOT preview or execute cache purge (403 FORBIDDEN, ADR 0027 §5.1).
	viewerPrevRR := postV1JSON(t, s, "/api/v1/cache/purge/preview", viewerToken, "", adminapi.CachePurgePreviewRequest{
		Selector:       tagSelector,
		IncludeSamples: true,
	})
	if viewerPrevRR.Code != http.StatusForbidden {
		t.Fatalf("viewer preview status = %d, want 403: %s", viewerPrevRR.Code, viewerPrevRR.Body.String())
	}
	viewerExecRR := postV1JSON(t, s, "/api/v1/cache/purge", viewerToken, "", adminapi.CachePurgeRequest{
		Selector: tagSelector,
	})
	if viewerExecRR.Code != http.StatusForbidden {
		t.Fatalf("viewer execute status = %d, want 403: %s", viewerExecRR.Code, viewerExecRR.Body.String())
	}

	// 3. Operator previews tag `article:42` in file_owned mode.
	prevRR := postV1JSON(t, s, "/api/v1/cache/purge/preview", opToken, "", adminapi.CachePurgePreviewRequest{
		Selector:       tagSelector,
		IncludeSamples: true,
	})
	if prevRR.Code != http.StatusOK {
		t.Fatalf("operator preview status = %d: %s", prevRR.Code, prevRR.Body.String())
	}
	prevResp := decodeInto[adminapi.CachePurgePreviewResponse](t, prevRR)
	if prevResp.MatchedResources != 1 || prevResp.MatchedRepresentations != 1 {
		t.Fatalf("unexpected preview counts: %+v", prevResp)
	}
	if prevResp.ConfigAuthority != "file_owned" {
		t.Fatalf("expected file_owned authority, got %q", prevResp.ConfigAuthority)
	}
	// Secret-safety check: raw query secret TOP_SECRET_QUERY_999 must never appear in preview response!
	if strings.Contains(prevRR.Body.String(), "TOP_SECRET_QUERY_999") {
		t.Fatalf("preview response leaked query secret: %s", prevRR.Body.String())
	}

	// 4. Scope binding checks:
	//    - Using op-1's preview_token with a different selector must fail with 400.
	//    - Using op-1's preview_token as op-2 must fail with 400.
	mismatchRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{
			Kind: "tag",
			Host: "portal.example.com",
			Tags: []string{"article:43"},
		},
		PreviewToken: prevResp.PreviewToken,
	})
	if mismatchRR.Code != http.StatusBadRequest {
		t.Fatalf("selector mismatch status = %d, want 400", mismatchRR.Code)
	}

	crossPrincipalRR := postV1JSON(t, s, "/api/v1/cache/purge", op2Token, "", adminapi.CachePurgeRequest{
		Selector:     tagSelector,
		PreviewToken: prevResp.PreviewToken,
	})
	if crossPrincipalRR.Code != http.StatusBadRequest {
		t.Fatalf("cross-principal preview_token status = %d, want 400", crossPrincipalRR.Code)
	}

	// 5. Permission revocation while confirmation is open:
	//    Revoke cache:purge from ops-1 via hot UpdatePolicy while ops-1 holds a valid preview_token!
	revokedPolicy, err := rbac.Build(true, rbac.RoleAdmin, nil, []rbac.PrincipalDef{
		{Name: "root", Role: rbac.RoleAdmin, Token: adminToken},
		{Name: "ops-1", Role: rbac.RoleViewer, Token: opToken}, // downgraded to viewer!
		{Name: "ops-2", Role: rbac.RoleOperator, Token: op2Token},
		{Name: "auditor-view", Role: rbac.RoleViewer, Token: viewerToken},
	}, "", time.Now())
	if err != nil {
		t.Fatalf("rbac.Build revokedPolicy: %v", err)
	}
	s.UpdatePolicy(revokedPolicy)

	revokedExecRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
		Selector:     tagSelector,
		PreviewToken: prevResp.PreviewToken,
	})
	if revokedExecRR.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when cache:purge revoked while confirmation open, got %d: %s", revokedExecRR.Code, revokedExecRR.Body.String())
	}

	// Restore operator policy for ops-1.
	s.UpdatePolicy(policy)

	// 6. Execute invalidation with Idempotency-Key, then verify idempotent replay and conflict.
	execRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "idem-621-1", adminapi.CachePurgeRequest{
		Selector:     tagSelector,
		PreviewToken: prevResp.PreviewToken,
	})
	if execRR.Code != http.StatusOK {
		t.Fatalf("execute status = %d: %s", execRR.Code, execRR.Body.String())
	}
	execResp := decodeInto[adminapi.CachePurgeResponse](t, execRR)
	if execResp.State != "succeeded" || execResp.InvalidatedRepresentations != 1 || execResp.IdempotentReplay {
		t.Fatalf("unexpected execute response: %+v", execResp)
	}

	// Replay with identical Idempotency-Key and selector returns idempotent_replay=true.
	replayRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "idem-621-1", adminapi.CachePurgeRequest{
		Selector: tagSelector,
	})
	if replayRR.Code != http.StatusOK {
		t.Fatalf("replay status = %d: %s", replayRR.Code, replayRR.Body.String())
	}
	replayResp := decodeInto[adminapi.CachePurgeResponse](t, replayRR)
	if !replayResp.IdempotentReplay || replayResp.OperationID != execResp.OperationID {
		t.Fatalf("expected idempotent replay of %s, got %+v", execResp.OperationID, replayResp)
	}

	// Reusing the same Idempotency-Key with a different selector returns 409 IDEMPOTENCY_KEY_REUSED.
	conflictRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "idem-621-1", adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{
			Kind:   "prefix",
			Host:   "portal.example.com",
			Prefix: "/articles/",
		},
	})
	if conflictRR.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict status = %d, want 409: %s", conflictRR.Code, conflictRR.Body.String())
	}

	// 7. Operation status lookup GET /api/v1/cache/purges/{operation_id}.
	statusRR := getV1(t, s, "/api/v1/cache/purges/"+execResp.OperationID, opToken)
	if statusRR.Code != http.StatusOK {
		t.Fatalf("status lookup = %d: %s", statusRR.Code, statusRR.Body.String())
	}

	// Broad scope without confirm_broad=true is rejected with 400; with confirm_broad=true succeeds.
	broadRejectRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{Kind: "all", All: true},
	})
	if broadRejectRR.Code != http.StatusBadRequest {
		t.Fatalf("broad scope without confirm_broad status = %d, want 400", broadRejectRR.Code)
	}
	broadOKRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
		Selector:     adminapi.CacheSelector{Kind: "all", All: true},
		ConfirmBroad: true,
	})
	if broadOKRR.Code != http.StatusOK {
		t.Fatalf("broad scope with confirm_broad status = %d: %s", broadOKRR.Code, broadOKRR.Body.String())
	}

	// 8. Principal isolation on GET /api/v1/cache/purges/{operation_id}:
	//    ops-2 (without audit:read or admin:manage) cannot read ops-1's operation; root (admin) can!
	//    Let's create a narrow role with only cache:purge (no audit:read) for isolation check.
	isolatedPolicy, err := rbac.Build(true, rbac.RoleAdmin, map[string][]string{
		"purge-only": {"cache:purge", "status:read"},
	}, []rbac.PrincipalDef{
		{Name: "root", Role: rbac.RoleAdmin, Token: adminToken},
		{Name: "ops-1", Role: "purge-only", Token: opToken},
		{Name: "ops-2", Role: "purge-only", Token: op2Token},
	}, "", time.Now())
	if err != nil {
		t.Fatalf("rbac.Build isolatedPolicy: %v", err)
	}
	s.UpdatePolicy(isolatedPolicy)

	otherOpLookup := getV1(t, s, "/api/v1/cache/purges/"+execResp.OperationID, op2Token)
	if otherOpLookup.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when another purge-only principal looks up ops-1 operation, got %d", otherOpLookup.Code)
	}
	adminOpLookup := getV1(t, s, "/api/v1/cache/purges/"+execResp.OperationID, adminToken)
	if adminOpLookup.Code != http.StatusOK {
		t.Fatalf("expected 200 when admin looks up ops-1 operation, got %d", adminOpLookup.Code)
	}

	// 9. retry_operation_id validation (unknown ID -> 404, mismatched selector -> 400, succeeded ID -> idempotent replay).
	unknownRetryRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
		Selector:         tagSelector,
		RetryOperationID: "cp_99999_missing",
	})
	if unknownRetryRR.Code != http.StatusNotFound {
		t.Fatalf("unknown retry_operation_id status = %d, want 404", unknownRetryRR.Code)
	}
	mismatchRetryRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{
			Kind:   "prefix",
			Host:   "portal.example.com",
			Prefix: "/articles/",
		},
		RetryOperationID: execResp.OperationID,
	})
	if mismatchRetryRR.Code != http.StatusBadRequest {
		t.Fatalf("mismatched retry_operation_id status = %d, want 400", mismatchRetryRR.Code)
	}
	succeededRetryRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
		Selector:         tagSelector,
		RetryOperationID: execResp.OperationID,
	})
	if succeededRetryRR.Code != http.StatusOK {
		t.Fatalf("succeeded retry_operation_id status = %d, want 200", succeededRetryRR.Code)
	}

	// 10. Durable .jul-cache-purge-ledger reload on a new Server instance sharing diskDir,
	//     plus disabled cache (Cache: nil) preview and execute behavior.
	s2 := newTestServer(t, config.AdminConfig{}, Deps{
		Cache: nil,
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{Cache: cacheCfg}, nil
		},
	})
	s2.UpdatePolicy(isolatedPolicy)

	persistedLookup := getV1(t, s2, "/api/v1/cache/purges/"+execResp.OperationID, opToken)
	if persistedLookup.Code != http.StatusOK {
		t.Fatalf("expected persisted operation %s to reload from disk ledger, got %d: %s", execResp.OperationID, persistedLookup.Code, persistedLookup.Body.String())
	}

	// Preview with explicit default port :443 on disabled cache emits warnings.
	disabledPrevRR := postV1JSON(t, s2, "/api/v1/cache/purge/preview", opToken, "", adminapi.CachePurgePreviewRequest{
		Selector: adminapi.CacheSelector{
			Kind: "exact_url",
			URL:  "https://portal.example.com:443/articles/42",
		},
	})
	if disabledPrevRR.Code != http.StatusOK {
		t.Fatalf("disabled cache preview status = %d: %s", disabledPrevRR.Code, disabledPrevRR.Body.String())
	}
	disabledExecRR := postV1JSON(t, s2, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{
			Kind: "exact_url",
			URL:  "https://portal.example.com:443/articles/42",
		},
	})
	if disabledExecRR.Code != http.StatusOK {
		t.Fatalf("disabled cache execute status = %d: %s", disabledExecRR.Code, disabledExecRR.Body.String())
	}
	disabledExec := decodeInto[adminapi.CachePurgeResponse](t, disabledExecRR)
	if disabledExec.Outcome != "cache_disabled" {
		t.Fatalf("expected cache_disabled outcome when Cache is nil, got %q", disabledExec.Outcome)
	}

	// 11. Token verification edge cases, TimeoutMS, truncated/tag-disabled warnings, and concurrency limit (429).
	for _, badTok := range []string{
		"malformed",
		"cpt1.notnum.100.abcdef",
		"cpt1.1.notnum.abcdef",
		"cpt1.1.100.nothex",
		"cpt1.1.100.00112233445566778899aabbccddeeff", // wrong MAC
	} {
		rr := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
			Selector:     tagSelector,
			PreviewToken: badTok,
		})
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for bad preview_token %q, got %d", badTok, rr.Code)
		}
	}

	// Warnings coverage (truncated, tag_ingestion_disabled, broad_scope, explicit_default_port).
	ws := buildCachePurgeWarnings(
		cache.NormalizedSelector{Kind: cache.SelectorTag, Host: "portal.example.com:443", ExplicitDefaultPort: true},
		true, true, false, false,
	)
	if len(ws) < 5 {
		t.Fatalf("expected at least 5 warnings when all warning conditions are active, got %d", len(ws))
	}

	// Concurrency backpressure (maxConcurrentCachePurges -> 429 RateLimited).
	s.cachePurgeInflight.Store(maxConcurrentCachePurges)
	rateLimRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
		Selector:     adminapi.CacheSelector{Kind: "all", All: true},
		ConfirmBroad: true,
	})
	s.cachePurgeInflight.Store(0)
	if rateLimRR.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when maxConcurrentCachePurges reached, got %d", rateLimRR.Code)
	}

	// TimeoutMS execution path.
	timeoutOKRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{
			Kind:   "prefix",
			Host:   "portal.example.com",
			Prefix: "/articles/",
		},
		TimeoutMS: 5000,
	})
	if timeoutOKRR.Code != http.StatusOK {
		t.Fatalf("TimeoutMS purge status = %d: %s", timeoutOKRR.Code, timeoutOKRR.Body.String())
	}

	// 12. Legacy minimal Purger fallback, in-flight idempotency conflict (409), and ledger capacity eviction.
	sLegacy := newTestServer(t, config.AdminConfig{Token: adminToken}, Deps{
		Cache: &fakePurger{},
	})
	for _, sel := range []adminapi.CacheSelector{
		{Kind: "all", All: true},
		{Kind: "key", Key: "GET\nportal.example.com\n/a"},
		{Kind: "exact_url", URL: "https://portal.example.com/a"},
		{Kind: "prefix", Host: "portal.example.com", Prefix: "/a"},
	} {
		rr := postV1JSON(t, sLegacy, "/api/v1/cache/purge", adminToken, "", adminapi.CachePurgeRequest{
			Selector:     sel,
			ConfirmBroad: true,
		})
		if rr.Code != http.StatusOK {
			t.Fatalf("legacy purger fallback for %s failed: %d %s", sel.Kind, rr.Code, rr.Body.String())
		}
	}

	// In-flight idempotency conflict (409 CodeIdempotencyKeyInUse) and 202 Accepted status lookup.
	sLegacy.ensurePurgeLedgerInit()
	sLegacy.purgeLedger.mu.Lock()
	runningRec := &cachePurgeRecord{
		OperationID:    "cp_inflight_1",
		Principal:      "shared",
		IdempotencyKey: "idem-inflight-1",
		CreatedAt:      time.Now().UTC(),
		Response: adminapi.CachePurgeResponse{
			OperationID:    "cp_inflight_1",
			State:          "running",
			Terminal:       false,
			SelectorDigest: prevResp.SelectorDigest,
		},
	}
	sLegacy.purgeLedger.byID["cp_inflight_1"] = runningRec
	sLegacy.purgeLedger.byIdem[idemMapKey("shared", "idem-inflight-1")] = runningRec
	sLegacy.purgeLedger.mu.Unlock()

	inflightRR := postV1JSON(t, sLegacy, "/api/v1/cache/purge", adminToken, "idem-inflight-1", adminapi.CachePurgeRequest{
		Selector: tagSelector,
	})
	if inflightRR.Code != http.StatusConflict {
		t.Fatalf("expected 409 for in-flight idempotency key, got %d", inflightRR.Code)
	}
	inflightLookup := getV1(t, sLegacy, "/api/v1/cache/purges/cp_inflight_1", adminToken)
	if inflightLookup.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for running operation lookup, got %d", inflightLookup.Code)
	}

	// Fill ledger to maxRetainedCachePurgeOps with old records and verify oldest eviction.
	sLegacy.purgeLedger.mu.Lock()
	sLegacy.purgeLedger.records = nil
	for i := 0; i < maxRetainedCachePurgeOps; i++ {
		id := "cp_old_" + string(rune('a'+(i%26)))
		r := &cachePurgeRecord{
			OperationID:    id,
			Principal:      "legacy",
			IdempotencyKey: id,
			CreatedAt:      time.Now().Add(-2 * cachePurgeLedgerMinAge),
			Response:       adminapi.CachePurgeResponse{OperationID: id, State: "succeeded", Terminal: true},
		}
		sLegacy.purgeLedger.records = append(sLegacy.purgeLedger.records, r)
	}
	sLegacy.purgeLedger.mu.Unlock()
	evictRR := postV1JSON(t, sLegacy, "/api/v1/cache/purge", adminToken, "idem-after-evict", adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{Kind: "exact_url", URL: "https://portal.example.com/a"},
	})
	if evictRR.Code != http.StatusOK {
		t.Fatalf("expected 200 after evicting aged ledger record, got %d", evictRR.Code)
	}

	// Retry of a partial operation using the same Idempotency-Key (reuseRecord != nil).
	sLegacy.purgeLedger.mu.Lock()
	partialRec := &cachePurgeRecord{
		OperationID:    "cp_partial_idem",
		Principal:      "shared",
		IdempotencyKey: "idem-partial-1",
		CreatedAt:      time.Now().UTC(),
		Response: adminapi.CachePurgeResponse{
			OperationID:    "cp_partial_idem",
			State:          "partial",
			Terminal:       true,
			SelectorDigest: prevResp.SelectorDigest,
			FenceSequence:  7,
		},
	}
	sLegacy.purgeLedger.byID["cp_partial_idem"] = partialRec
	sLegacy.purgeLedger.byIdem[idemMapKey("shared", "idem-partial-1")] = partialRec
	sLegacy.purgeLedger.mu.Unlock()

	retryIdemRR := postV1JSON(t, sLegacy, "/api/v1/cache/purge", adminToken, "idem-partial-1", adminapi.CachePurgeRequest{
		Selector: tagSelector,
	})
	if retryIdemRR.Code != http.StatusOK {
		t.Fatalf("expected 200 when retrying partial operation with same Idempotency-Key, got %d: %s", retryIdemRR.Code, retryIdemRR.Body.String())
	}

	// Ledger at capacity within retention window -> 429 RateLimited.
	sLegacy.purgeLedger.mu.Lock()
	sLegacy.purgeLedger.records = nil
	for i := 0; i < maxRetainedCachePurgeOps; i++ {
		id := "cp_fresh_" + string(rune('a'+(i%26)))
		r := &cachePurgeRecord{
			OperationID:    id,
			Principal:      "shared",
			IdempotencyKey: id,
			CreatedAt:      time.Now().UTC(),
			Response:       adminapi.CachePurgeResponse{OperationID: id, State: "succeeded", Terminal: true},
		}
		sLegacy.purgeLedger.records = append(sLegacy.purgeLedger.records, r)
	}
	sLegacy.purgeLedger.mu.Unlock()
	capRR := postV1JSON(t, sLegacy, "/api/v1/cache/purge", adminToken, "idem-cap-12345", adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{Kind: "exact_url", URL: "https://portal.example.com/a"},
	})
	if capRR.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when ledger at capacity within retention window, got %d", capRR.Code)
	}

	// Invalid selector & invalid Idempotency-Key error branches on preview and purge.
	badPrevSelRR := postV1JSON(t, s, "/api/v1/cache/purge/preview", opToken, "", adminapi.CachePurgePreviewRequest{
		Selector: adminapi.CacheSelector{Kind: "prefix", Host: "portal.example.com", Prefix: "not-starting-with-slash"},
	})
	if badPrevSelRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid selector on preview, got %d", badPrevSelRR.Code)
	}
	badExecSelRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "", adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{Kind: "prefix", Host: "portal.example.com", Prefix: "not-starting-with-slash"},
	})
	if badExecSelRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid selector on purge, got %d", badExecSelRR.Code)
	}
	badIdemRR := postV1JSON(t, s, "/api/v1/cache/purge", opToken, "short", adminapi.CachePurgeRequest{
		Selector: tagSelector,
	})
	if badIdemRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for short Idempotency-Key, got %d", badIdemRR.Code)
	}

	// Malformed JSON body on preview and purge, plus engine-error rollback in handleV1CachePurge.
	for _, path := range []string{"/api/v1/cache/purge/preview", "/api/v1/cache/purge"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{bad-json"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+opToken)
		s.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for malformed JSON on %s, got %d", path, rr.Code)
		}
	}

	sErr := newTestServer(t, config.AdminConfig{Token: adminToken}, Deps{Cache: errScopedPurger{}})
	errPurgeRR := postV1JSON(t, sErr, "/api/v1/cache/purge", adminToken, "idem-err-1234", adminapi.CachePurgeRequest{
		Selector: tagSelector,
	})
	if errPurgeRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when ScopedPurger returns scan error, got %d", errPurgeRR.Code)
	}
}
