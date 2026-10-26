// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/handler"
	"jul/internal/rbac"
	"jul/internal/router"
	"jul/internal/server"
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
	// In-flight idempotency conflict (409 CodeIdempotencyKeyInUse), retry_operation_id conflict (409), and 202 Accepted status lookup.
	inflightRetryRR := postV1JSON(t, sLegacy, "/api/v1/cache/purge", adminToken, "", adminapi.CachePurgeRequest{
		Selector:         tagSelector,
		RetryOperationID: "cp_inflight_1",
	})
	if inflightRetryRR.Code != http.StatusConflict {
		t.Fatalf("expected 409 for retry_operation_id referencing in-flight operation, got %d", inflightRetryRR.Code)
	}
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

type countingScopedPurger struct {
	executions   atomic.Int32
	beforeCommit func()
	onExec       func()
}

func (p *countingScopedPurger) Purge()            {}
func (p *countingScopedPurger) Delete(key string) {}
func (p *countingScopedPurger) PreviewScope(sel cache.NormalizedSelector, opts cache.PreviewOptions) cache.PurgePreview {
	return cache.PurgePreview{
		Selector:      sel,
		CountAccuracy: "exact",
		ScanLimit:     10000,
		AsOf:          time.Now().UTC(),
		BroadScope:    sel.BroadScope,
		CacheSequence: 1,
		Samples:       nil,
	}
}
func (p *countingScopedPurger) InvalidateScope(ctx context.Context, sel cache.NormalizedSelector, opts cache.InvalidateOptions) (cache.InvalidateResult, error) {
	if p.beforeCommit != nil {
		p.beforeCommit()
	}
	fenceGen := opts.ReuseFenceGen
	if fenceGen == 0 {
		fenceGen = 1
	}
	if opts.CommitFence != nil {
		if err := opts.CommitFence(fenceGen, nil); err != nil {
			return cache.InvalidateResult{}, fmt.Errorf("%w: %w", cache.ErrFenceCommit, err)
		}
	}
	if p.onExec != nil {
		p.onExec()
	}
	p.executions.Add(1)
	now := time.Now().UTC()
	return cache.InvalidateResult{
		Selector:                   sel,
		State:                      "succeeded",
		Outcome:                    "succeeded",
		InvalidationEffective:      "effective",
		FenceSequence:              fenceGen,
		InvalidatedResources:       1,
		InvalidatedRepresentations: 1,
		MemoryEntriesRemoved:       1,
		StartedAt:                  now,
		CompletedAt:                now,
	}, nil
}

func TestAuditR5_LedgerWriteAndLoadFailuresFailClosed(t *testing.T) {
	t.Parallel()

	diskDir := t.TempDir()
	// Make the ledger pathname a directory to force an actual read/write failure (F03).
	ledgerPath := filepath.Join(diskDir, diskPurgeLedgerFilename)
	if err := os.MkdirAll(ledgerPath, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	eng := &countingScopedPurger{}
	const tok = "admin-secret-token-621-f03"
	s1 := newTestServer(t, config.AdminConfig{Token: tok}, Deps{
		Cache: eng,
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{
				Cache: config.CacheConfig{Enabled: true, DiskPath: diskDir},
			}, nil
		},
	})

	sel := adminapi.CacheSelector{
		Kind: "exact_url",
		URL:  "https://portal.example.com/articles/42",
	}
	rr1 := postV1JSON(t, s1, "/api/v1/cache/purge", tok, "idem-f03-write-fail", adminapi.CachePurgeRequest{
		Selector: sel,
	})
	if rr1.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when ledger path is a directory, got %d: %s", rr1.Code, rr1.Body.String())
	}
	if got := eng.executions.Load(); got != 0 {
		t.Fatalf("expected 0 engine executions when ledger persistence fails pre-execution, got %d", got)
	}

	// Also verify post-execution persistence failure downgrades response to partial/uncertain.
	_ = os.RemoveAll(ledgerPath)
	s2 := newTestServer(t, config.AdminConfig{Token: tok}, Deps{
		Cache: &countingScopedPurger{
			onExec: func() {
				// Break the ledger path during execution so post-execution persistence fails.
				_ = os.Remove(ledgerPath)
				_ = os.MkdirAll(ledgerPath, 0o700)
			},
		},
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{
				Cache: config.CacheConfig{Enabled: true, DiskPath: diskDir},
			}, nil
		},
	})
	rr2 := postV1JSON(t, s2, "/api/v1/cache/purge", tok, "idem-f03-post-fail", adminapi.CachePurgeRequest{
		Selector: sel,
	})
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 with degraded state on post-execution ledger failure, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var postResp adminapi.CachePurgeResponse
	if err := json.Unmarshal(rr2.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("unmarshal postResp: %v", err)
	}
	if postResp.State == "succeeded" || postResp.InvalidationEffective != "uncertain" {
		t.Fatalf("expected post-execution ledger write failure to report partial/uncertain, got %+v", postResp)
	}

	// Verify startup recovery of a persisted "running" record transitions it to interrupted/uncertain AND persists that terminal state to disk (Round 4).
	_ = os.RemoveAll(ledgerPath)
	runningJSON := `[{"operation_id":"cp_1_crash","principal":"shared","idempotency_key":"idem-crash-1","created_at":"2026-10-25T10:00:00Z","response":{"api_version":"v1","operation_id":"cp_1_crash","state":"running","terminal":false,"outcome":"running","invalidation_effective":"incomplete","selector":{"kind":"exact_url","host":"portal.example.com","uri":"/articles/42"},"selector_digest":"deadbeef","started_at":"2026-10-25T10:00:00Z","warnings":[],"boot_id":"boot-old"}}]`
	if err := os.WriteFile(ledgerPath, []byte(runningJSON), 0o600); err != nil {
		t.Fatalf("WriteFile runningJSON: %v", err)
	}
	s3 := newTestServer(t, config.AdminConfig{Token: tok}, Deps{
		Cache: &countingScopedPurger{},
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{
				Cache: config.CacheConfig{Enabled: true, DiskPath: diskDir},
			}, nil
		},
	})
	lookupRR := getV1(t, s3, "/api/v1/cache/purges/cp_1_crash", tok)
	if lookupRR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for recovered interrupted operation, got %d: %s", lookupRR.Code, lookupRR.Body.String())
	}
	diskAfterBytes, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("ReadFile ledgerPath: %v", err)
	}
	if !strings.Contains(string(diskAfterBytes), `"outcome":"interrupted"`) || !strings.Contains(string(diskAfterBytes), `"terminal":true`) {
		t.Fatalf("expected startup recovery to persist interrupted/terminal state to disk, got %s", string(diskAfterBytes))
	}

	// Verify corrupt ledger JSON fails closed on GET /api/v1/cache/purges/{id}, and LiveSnapshot + rollbackAdmittedRecordLocked with evictedRec.
	_ = os.WriteFile(ledgerPath, []byte("{corrupt-json"), 0o600)
	sCorrupt := newTestServer(t, config.AdminConfig{Token: tok}, Deps{
		Cache: &countingScopedPurger{},
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{
				EffectiveConfig: &config.Config{
					Cache: config.CacheConfig{Enabled: true, DiskPath: diskDir},
				},
			}
		},
	})
	if getCorruptRR := getV1(t, sCorrupt, "/api/v1/cache/purges/cp_1_crash", tok); getCorruptRR.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on corrupt ledger GET lookup, got %d", getCorruptRR.Code)
	}

	// Exercise rollbackAdmittedRecordLocked restoring evictedRec when ledger is at 256 capacity and pre-execution write fails.
	_ = os.RemoveAll(ledgerPath)
	_ = os.MkdirAll(ledgerPath, 0o700)
	sCorrupt.ensurePurgeLedgerInit()
	sCorrupt.purgeLedger.mu.Lock()
	sCorrupt.purgeLedger.loadedDisk = diskDir
	sCorrupt.purgeLedger.records = nil
	for i := 0; i < maxRetainedCachePurgeOps; i++ {
		id := fmt.Sprintf("cp_evict_%d", i)
		rec := &cachePurgeRecord{
			OperationID:    id,
			Principal:      "shared",
			IdempotencyKey: id,
			CreatedAt:      time.Now().Add(-2 * cachePurgeLedgerMinAge),
			Response:       adminapi.CachePurgeResponse{OperationID: id, State: "succeeded", Terminal: true},
		}
		sCorrupt.purgeLedger.records = append(sCorrupt.purgeLedger.records, rec)
		sCorrupt.purgeLedger.byID[id] = rec
		sCorrupt.purgeLedger.byIdem[idemMapKey("shared", id)] = rec
	}
	sCorrupt.purgeLedger.mu.Unlock()
	evictRollbackRR := postV1JSON(t, sCorrupt, "/api/v1/cache/purge", tok, "idem-rollback-evict", adminapi.CachePurgeRequest{
		Selector: sel,
	})
	if evictRollbackRR.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on pre-execution write failure at capacity, got %d", evictRollbackRR.Code)
	}
	sCorrupt.purgeLedger.mu.Lock()
	_, restored := sCorrupt.purgeLedger.byID["cp_evict_0"]
	sCorrupt.purgeLedger.mu.Unlock()
	if !restored {
		t.Fatalf("expected rollbackAdmittedRecordLocked to restore evicted cp_evict_0")
	}
}

func TestAuditR5_ProtectedReplayBindingNeverEvictedByRejectedOrUnkeyedTraffic(t *testing.T) {
	t.Parallel()

	const tok = "admin-secret-token-621-f04"
	blockCh := make(chan struct{})
	slotEntered := make(chan struct{}, maxConcurrentCachePurges)
	var shouldBlock atomic.Bool

	eng := &countingScopedPurger{
		onExec: func() {
			if shouldBlock.Load() {
				slotEntered <- struct{}{}
				<-blockCh
			}
		},
	}
	s := newTestServer(t, config.AdminConfig{Token: tok}, Deps{Cache: eng})

	// 1. Execute the original keyed operation (#1) and fill the remaining 255 ledger slots with young keyed records.
	origSel := adminapi.CacheSelector{
		Kind: "exact_url",
		URL:  "https://portal.example.com/articles/orig",
	}
	rrOrig := postV1JSON(t, s, "/api/v1/cache/purge", tok, "idem-orig-protected-key", adminapi.CachePurgeRequest{
		Selector: origSel,
	})
	if rrOrig.Code != http.StatusOK || eng.executions.Load() != 1 {
		t.Fatalf("expected initial keyed operation to succeed (executions=1), got code=%d execs=%d", rrOrig.Code, eng.executions.Load())
	}

	s.purgeLedger.mu.Lock()
	for i := len(s.purgeLedger.records); i < maxRetainedCachePurgeOps; i++ {
		id := fmt.Sprintf("cp_young_%d", i)
		ik := fmt.Sprintf("idem-young-%d", i)
		rec := &cachePurgeRecord{
			OperationID:    id,
			Principal:      "shared",
			IdempotencyKey: ik,
			CreatedAt:      time.Now().UTC(),
			Response: adminapi.CachePurgeResponse{
				OperationID: id,
				State:       "succeeded",
				Terminal:    true,
				Warnings:    []adminapi.CacheGuidance{},
			},
		}
		s.purgeLedger.records = append(s.purgeLedger.records, rec)
		s.purgeLedger.byID[id] = rec
		s.purgeLedger.byIdem[idemMapKey("shared", ik)] = rec
	}
	s.purgeLedger.mu.Unlock()

	// 2. Occupy all 4 execution slots and attempt an unkeyed operation: must return 429 WITHOUT evicting idem-orig-protected-key!
	s.cachePurgeInflight.Store(maxConcurrentCachePurges)
	unkeyedBlockedRR := postV1JSON(t, s, "/api/v1/cache/purge", tok, "", adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{Kind: "exact_url", URL: "https://portal.example.com/articles/unkeyed"},
	})
	if unkeyedBlockedRR.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when all slots occupied, got %d", unkeyedBlockedRR.Code)
	}
	s.cachePurgeInflight.Store(0)

	// 3. Even with a free slot, an unkeyed request when all 256 records are young (<15m) keyed records must return 429 without evicting!
	unkeyedCapRR := postV1JSON(t, s, "/api/v1/cache/purge", tok, "", adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{Kind: "exact_url", URL: "https://portal.example.com/articles/unkeyed-2"},
	})
	if unkeyedCapRR.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when all 256 ledger slots hold protected <15m keyed records, got %d", unkeyedCapRR.Code)
	}

	// 4. Retry the original key: must return 200 with IdempotentReplay=true and engine executions still == 1!
	close(blockCh)
	retryOrigRR := postV1JSON(t, s, "/api/v1/cache/purge", tok, "idem-orig-protected-key", adminapi.CachePurgeRequest{
		Selector: origSel,
	})
	if retryOrigRR.Code != http.StatusOK {
		t.Fatalf("expected 200 on replay of protected key, got %d: %s", retryOrigRR.Code, retryOrigRR.Body.String())
	}
	var replayResp adminapi.CachePurgeResponse
	if err := json.Unmarshal(retryOrigRR.Body.Bytes(), &replayResp); err != nil {
		t.Fatalf("unmarshal replayResp: %v", err)
	}
	if !replayResp.IdempotentReplay || eng.executions.Load() != 1 {
		t.Fatalf("expected IdempotentReplay=true and executions=1, got replay=%v executions=%d", replayResp.IdempotentReplay, eng.executions.Load())
	}
}

type blockingBodyReader struct {
	admitted chan struct{}
	release  chan struct{}
	once     sync.Once
	inner    *strings.Reader
}

func (b *blockingBodyReader) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.admitted)
		<-b.release
	})
	return b.inner.Read(p)
}

func TestAuditR5_LiveRevocationDuringAdmissionBlocksExecution(t *testing.T) {
	t.Parallel()

	const rootTok = "root-admin-token-32-chars-padded-621"
	const opTok = "rbac-live-revoke-token-32-chars-621"
	pol1, err := rbac.Build(true, rbac.RoleAdmin, nil, []rbac.PrincipalDef{
		{Name: "root", Role: rbac.RoleAdmin, Token: rootTok},
		{Name: "ops-1", Role: rbac.RoleOperator, Token: opTok},
	}, "", time.Now())
	if err != nil {
		t.Fatalf("rbac.Build pol1: %v", err)
	}
	polViewer, err := rbac.Build(true, rbac.RoleAdmin, nil, []rbac.PrincipalDef{
		{Name: "root", Role: rbac.RoleAdmin, Token: rootTok},
		{Name: "ops-1", Role: rbac.RoleViewer, Token: opTok},
	}, "", time.Now())
	if err != nil {
		t.Fatalf("rbac.Build polViewer: %v", err)
	}

	eng := &countingScopedPurger{}
	s := newTestServer(t, config.AdminConfig{}, Deps{Cache: eng})
	s.UpdatePolicy(pol1)

	bodyReader := &blockingBodyReader{
		admitted: make(chan struct{}),
		release:  make(chan struct{}),
		inner:    strings.NewReader(`{"selector":{"kind":"exact_url","url":"https://portal.example.com/articles/42"}}`),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cache/purge", bodyReader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+opTok)
	rr := httptest.NewRecorder()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.routes().ServeHTTP(rr, req)
	}()

	// Wait until the request has passed outer RBAC middleware and is reading the body.
	<-bodyReader.admitted
	// Hot-reload the same principal/token to viewer!
	s.UpdatePolicy(polViewer)
	close(bodyReader.release)
	wg.Wait()

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when principal is demoted to viewer before execution, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := eng.executions.Load(); got != 0 {
		t.Fatalf("expected 0 engine executions after live revocation, got %d", got)
	}
}

func TestAuditR5_R6_QueryRoundtripWireArraysAndCountBasedBroadGuard(t *testing.T) {
	t.Parallel()

	const tok = "admin-secret-token-621-f07-f08-f14"
	realCache, err := cache.New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(4 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	}, nil)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	h := realCache.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = fmt.Fprintf(w, "item-%s", r.URL.RawQuery)
	}))

	s := newTestServer(t, config.AdminConfig{Token: tok}, Deps{
		Cache: realCache,
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{
				Cache: config.CacheConfig{Enabled: true},
			}, nil
		},
	})

	// 1. Preview a no-match queried URL and verify raw JSON includes "samples":[] and "warnings":[...] (F07, F08).
	querySel := adminapi.CacheSelector{
		Kind: "exact_url",
		URL:  "https://portal.example.com/articles/42?lang=es",
	}
	prevRR := postV1JSON(t, s, "/api/v1/cache/purge/preview", tok, "", adminapi.CachePurgePreviewRequest{
		Selector:       querySel,
		IncludeSamples: true,
	})
	if prevRR.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", prevRR.Code, prevRR.Body.String())
	}
	rawPrevJSON := prevRR.Body.String()
	if !strings.Contains(rawPrevJSON, `"samples":[]`) || !strings.Contains(rawPrevJSON, `"warnings":`) {
		t.Fatalf("expected raw preview JSON to contain non-null samples and warnings arrays, got %s", rawPrevJSON)
	}
	var prevResp adminapi.CachePurgePreviewResponse
	if err := json.Unmarshal(prevRR.Body.Bytes(), &prevResp); err != nil {
		t.Fatalf("unmarshal prevResp: %v", err)
	}
	if prevResp.Selector.URI != "/articles/42?<redacted>" {
		t.Fatalf("expected redacted URI in preview response, got %q", prevResp.Selector.URI)
	}

	// Execute with the original unredacted selector and preview_token: must succeed (200 OK).
	execQueryRR := postV1JSON(t, s, "/api/v1/cache/purge", tok, "", adminapi.CachePurgeRequest{
		Selector:     querySel,
		PreviewToken: prevResp.PreviewToken,
	})
	if execQueryRR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK executing queried URL with preview_token, got %d: %s", execQueryRR.Code, execQueryRR.Body.String())
	}

	// 2. Warm 100 resources under /articles/item-* and verify count-based broad confirmation is enforced at execution (F14).
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://portal.example.com/articles/item-%d", i), nil)
		req.Host = "portal.example.com"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	pfx100 := adminapi.CacheSelector{
		Kind:   "prefix",
		Host:   "portal.example.com",
		Prefix: "/articles/",
	}
	prev100RR := postV1JSON(t, s, "/api/v1/cache/purge/preview", tok, "", adminapi.CachePurgePreviewRequest{
		Selector: pfx100,
	})
	var prev100 adminapi.CachePurgePreviewResponse
	_ = json.Unmarshal(prev100RR.Body.Bytes(), &prev100)
	if !prev100.BroadScope || prev100.MatchedResources != 100 {
		t.Fatalf("expected BroadScope=true and MatchedResources=100, got %+v", prev100)
	}

	// Execute without confirm_broad: must fail with 400 confirm_broad and remove 0 entries!
	noConfirmRR := postV1JSON(t, s, "/api/v1/cache/purge", tok, "", adminapi.CachePurgeRequest{
		Selector: pfx100,
	})
	if noConfirmRR.Code != http.StatusBadRequest || !strings.Contains(noConfirmRR.Body.String(), "confirm_broad") {
		t.Fatalf("expected 400 confirm_broad for 100-resource prefix without confirm_broad, got %d: %s", noConfirmRR.Code, noConfirmRR.Body.String())
	}

	// Execute with confirm_broad=true: must succeed and remove all 100 resources.
	withConfirmRR := postV1JSON(t, s, "/api/v1/cache/purge", tok, "", adminapi.CachePurgeRequest{
		Selector:     pfx100,
		ConfirmBroad: true,
	})
	if withConfirmRR.Code != http.StatusOK {
		t.Fatalf("expected 200 with confirm_broad=true, got %d: %s", withConfirmRR.Code, withConfirmRR.Body.String())
	}

	// 3. Also test retry_operation_id resolution when the caller sends the redacted display selector (Round 5).
	var execQueryResp adminapi.CachePurgeResponse
	_ = json.Unmarshal(execQueryRR.Body.Bytes(), &execQueryResp)
	s.purgeLedger.mu.Lock()
	if rec, ok := s.purgeLedger.byID[execQueryResp.OperationID]; ok {
		rec.Response.State = "partial"
	}
	s.purgeLedger.mu.Unlock()
	retryRedactedRR := postV1JSON(t, s, "/api/v1/cache/purge", tok, "", adminapi.CachePurgeRequest{
		Selector:         execQueryResp.Selector, // uri: "/articles/42?<redacted>"
		RetryOperationID: execQueryResp.OperationID,
	})
	if retryRedactedRR.Code != http.StatusOK {
		t.Fatalf("expected 200 retrying with redacted selector via execSelector resolution, got %d: %s", retryRedactedRR.Code, retryRedactedRR.Body.String())
	}

	// Exercise verifyLivePurgePermission across authModeBlocked and authModeLegacy mismatch.
	reqLegacyBad := httptest.NewRequest(http.MethodPost, "/api/v1/cache/purge", nil)
	reqLegacyBad.Header.Set("Authorization", "Bearer wrong-legacy-token")
	if s.verifyLivePurgePermission(httptest.NewRecorder(), reqLegacyBad) {
		t.Fatalf("expected verifyLivePurgePermission to reject wrong legacy token")
	}
	s.UpdateLiveAdminConfig(config.AdminConfig{
		RBAC: config.AdminRBACConfig{Enabled: true},
	})
	s.UpdatePolicy(nil)
	if s.verifyLivePurgePermission(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/cache/purge", nil)) {
		t.Fatalf("expected verifyLivePurgePermission to reject authModeBlocked")
	}

	// Exercise verifyLivePurgePermission with disabled RBAC principal and missing credential.
	disabledPol, err := rbac.Build(true, rbac.RoleAdmin, nil, []rbac.PrincipalDef{
		{Name: "root", Role: rbac.RoleAdmin, Token: "root-admin-token-32-chars-padded-621"},
		{Name: "disabled-op", Role: rbac.RoleOperator, Token: "disabled-op-token-32-chars-padded-621", Disabled: true},
	}, "", time.Now())
	if err != nil {
		t.Fatalf("rbac.Build disabledPol: %v", err)
	}
	s.UpdatePolicy(disabledPol)
	reqDisabled := httptest.NewRequest(http.MethodPost, "/api/v1/cache/purge", nil)
	reqDisabled.Header.Set("Authorization", "Bearer disabled-op-token-32-chars-padded-621")
	if s.verifyLivePurgePermission(httptest.NewRecorder(), reqDisabled) {
		t.Fatalf("expected verifyLivePurgePermission to reject disabled RBAC principal")
	}
	if s.verifyLivePurgePermission(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/cache/purge", nil)) {
		t.Fatalf("expected verifyLivePurgePermission to reject missing credential")
	}
}

func TestReauditR01_InterruptedOperationRetryPreservesFreshPostRestartFills(t *testing.T) {
	t.Parallel()

	const tok = "admin-secret-token-621-r01"
	diskDir := t.TempDir()
	crashDir := t.TempDir()

	makeCacheCfg := func(dir string) config.CacheConfig {
		return config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: config.Size(2 << 20),
			DiskPath:      dir,
			DiskMaxSize:   config.Size(16 << 20),
			MaxEntrySize:  config.Size(4 << 20),
			DefaultTTL:    config.Duration(5 * time.Minute),
		}
	}

	c1, err := cache.New(makeCacheCfg(diskDir), nil)
	if err != nil {
		t.Fatalf("cache.New c1: %v", err)
	}
	effCfg1 := &config.Config{Cache: makeCacheCfg(diskDir)}
	s1 := newTestServer(t, config.AdminConfig{Token: tok}, Deps{
		Cache: c1,
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{EffectiveConfig: effCfg1}
		},
	})

	originGen := 0
	serveItem := func(c *cache.Cache) (*httptest.ResponseRecorder, string) {
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			originGen++
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "payload-v%d", originGen)
		}))
		req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/r01-item", nil)
		req.Host = "portal.example.com"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr, rr.Body.String()
	}

	// Warm v1 in c1.
	rrWarm, bodyWarm := serveItem(c1)
	if rrWarm.Header().Get("X-Cache") != "MISS" || bodyWarm != "payload-v1" {
		t.Fatalf("expected initial MISS payload-v1, got %q %q", rrWarm.Header().Get("X-Cache"), bodyWarm)
	}

	// 1. Execute purge on s1; after the engine finishes invalidating disk/memory,
	// snapshot the reachable crash state in diskDir BEFORE the final API ledger commit
	// by intercepting the post-execution ledger write via a read-only crash snapshot copy.
	sel := adminapi.CacheSelector{
		Kind: "exact_url",
		Host: "portal.example.com",
		URI:  "/r01-item",
	}

	// Capture the pre-execution durable running record right after s1 writes it by
	// wrapping s1.deps.Cache with a proxy that delegates to c1 and copies diskDir -> crashDir
	// immediately after c1.InvalidateScope completes (before handleV1CachePurge writes finalResp).
	snapPurger := &postInvalidateSnapshotPurger{
		ScopedPurger: c1,
		afterInvalidate: func() {
			entries, rErr := os.ReadDir(diskDir)
			if rErr != nil {
				t.Errorf("ReadDir diskDir: %v", rErr)
				return
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				data, fErr := os.ReadFile(filepath.Join(diskDir, e.Name()))
				if fErr == nil {
					_ = os.WriteFile(filepath.Join(crashDir, e.Name()), data, 0o600)
				}
			}
		},
	}
	s1.deps.Cache = snapPurger

	rr1 := postV1JSON(t, s1, "/api/v1/cache/purge", tok, "idem-r01-crash-retry", adminapi.CachePurgeRequest{
		Selector: sel,
	})
	if rr1.Code != http.StatusOK {
		t.Fatalf("expected 200 on initial purge, got %d: %s", rr1.Code, rr1.Body.String())
	}

	// Verify the crash snapshot in crashDir captured a "running" record with FenceSequence > 0.
	rawCrashLedger, err := os.ReadFile(filepath.Join(crashDir, diskPurgeLedgerFilename))
	if err != nil {
		t.Fatalf("ReadFile crashDir ledger: %v", err)
	}
	var crashRecs []cachePurgeRecord
	if err := json.Unmarshal(rawCrashLedger, &crashRecs); err != nil || len(crashRecs) != 1 {
		t.Fatalf("Unmarshal crashRecs: err=%v len=%d", err, len(crashRecs))
	}
	if crashRecs[0].Response.State != "running" || crashRecs[0].Response.FenceSequence == 0 {
		t.Fatalf("expected crash snapshot running record to have FenceSequence > 0, got state=%q fence=%d",
			crashRecs[0].Response.State, crashRecs[0].Response.FenceSequence)
	}
	origFence := crashRecs[0].Response.FenceSequence

	// 2. Reopen crashDir on a new cache (c2) and server (s2).
	c2, err := cache.New(makeCacheCfg(crashDir), nil)
	if err != nil {
		t.Fatalf("cache.New c2: %v", err)
	}
	effCfg2 := &config.Config{Cache: makeCacheCfg(crashDir)}
	s2 := newTestServer(t, config.AdminConfig{Token: tok}, Deps{
		Cache: c2,
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{EffectiveConfig: effCfg2}
		},
	})

	// 3. Populate new matching content on c2; verify it is a warm HIT.
	rrMiss2, body2 := serveItem(c2)
	if rrMiss2.Header().Get("X-Cache") != "MISS" || body2 != "payload-v2" {
		t.Fatalf("expected MISS payload-v2 on c2, got %q %q", rrMiss2.Header().Get("X-Cache"), body2)
	}
	rrHit2, bodyHit2 := serveItem(c2)
	if rrHit2.Header().Get("X-Cache") != "HIT" || bodyHit2 != "payload-v2" {
		t.Fatalf("expected warm HIT payload-v2 before retry, got %q %q", rrHit2.Header().Get("X-Cache"), bodyHit2)
	}

	// 4. Retry the same Idempotency-Key on s2: must complete at origFence and keep payload-v2 as a warm HIT.
	rrRetry := postV1JSON(t, s2, "/api/v1/cache/purge", tok, "idem-r01-crash-retry", adminapi.CachePurgeRequest{
		Selector: sel,
	})
	if rrRetry.Code != http.StatusOK {
		t.Fatalf("expected 200 on interrupted retry, got %d: %s", rrRetry.Code, rrRetry.Body.String())
	}
	var retryResp adminapi.CachePurgeResponse
	_ = json.Unmarshal(rrRetry.Body.Bytes(), &retryResp)
	if retryResp.FenceSequence != origFence {
		t.Fatalf("expected retry to preserve original FenceSequence %d, got %d", origFence, retryResp.FenceSequence)
	}

	// 5. Verify fresh post-restart content remains a warm HIT!
	rrAfter, bodyAfter := serveItem(c2)
	if rrAfter.Header().Get("X-Cache") != "HIT" || bodyAfter != "payload-v2" {
		t.Fatalf("R01 regression: interrupted retry wiped post-restart warm content (X-Cache=%q body=%q)",
			rrAfter.Header().Get("X-Cache"), bodyAfter)
	}

	// 6. Verify that an interrupted record with an unknown cutoff (FenceSequence == 0) refuses automatic continuation.
	unknownDir := t.TempDir()
	unknownLedger := []*cachePurgeRecord{
		{
			OperationID:    "cp_unknown_1",
			Principal:      "shared",
			IdempotencyKey: "idem-r01-unknown-cutoff",
			CreatedAt:      time.Now().UTC(),
			Response: adminapi.CachePurgeResponse{
				APIVersion:            adminapi.APIVersion,
				OperationID:           "cp_unknown_1",
				State:                 "running",
				Terminal:              false,
				Outcome:               "running",
				InvalidationEffective: "incomplete",
				Selector:              sel,
				SelectorDigest:        retryResp.SelectorDigest,
				FenceSequence:         0,
			},
		},
	}
	rawUnknown, _ := json.Marshal(unknownLedger)
	_ = os.WriteFile(filepath.Join(unknownDir, diskPurgeLedgerFilename), rawUnknown, 0o600)
	c3, err := cache.New(makeCacheCfg(unknownDir), nil)
	if err != nil {
		t.Fatalf("cache.New c3: %v", err)
	}
	effCfg3 := &config.Config{Cache: makeCacheCfg(unknownDir)}
	s3 := newTestServer(t, config.AdminConfig{Token: tok}, Deps{
		Cache: c3,
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{EffectiveConfig: effCfg3}
		},
	})
	_, _ = serveItem(c3)
	rrUnknownIdem := postV1JSON(t, s3, "/api/v1/cache/purge", tok, "idem-r01-unknown-cutoff", adminapi.CachePurgeRequest{
		Selector: sel,
	})
	if rrUnknownIdem.Code != http.StatusConflict {
		t.Fatalf("expected 409 refusing unknown-cutoff Idempotency-Key continuation, got %d: %s", rrUnknownIdem.Code, rrUnknownIdem.Body.String())
	}
	rrUnknownOp := postV1JSON(t, s3, "/api/v1/cache/purge", tok, "", adminapi.CachePurgeRequest{
		Selector:         sel,
		RetryOperationID: "cp_unknown_1",
	})
	if rrUnknownOp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 refusing unknown-cutoff retry_operation_id continuation, got %d: %s", rrUnknownOp.Code, rrUnknownOp.Body.String())
	}
	rrStillWarm, _ := serveItem(c3)
	if rrStillWarm.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected c3 warm entry to remain HIT after refused unknown-cutoff retry, got %q", rrStillWarm.Header().Get("X-Cache"))
	}
}

type postInvalidateSnapshotPurger struct {
	ScopedPurger
	afterInvalidate func()
}

func (p *postInvalidateSnapshotPurger) InvalidateScope(ctx context.Context, sel cache.NormalizedSelector, opts cache.InvalidateOptions) (cache.InvalidateResult, error) {
	res, err := p.ScopedPurger.InvalidateScope(ctx, sel, opts)
	if p.afterInvalidate != nil {
		p.afterInvalidate()
	}
	return res, err
}

func TestReauditR02_OpenToTokenAndOpenToRBACAuthTighteningDeniesAdmittedCredentiallessRequest(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"open_to_legacy_token", "open_to_rbac"} {
		t.Run(mode, func(t *testing.T) {
			var execCount atomic.Int32
			eng := &countingScopedPurger{
				onExec: func() {
					execCount.Add(1)
				},
			}
			// Start with open administration (empty token, RBAC disabled).
			s := newTestServer(t, config.AdminConfig{}, Deps{Cache: eng})

			payloadBytes, _ := json.Marshal(adminapi.CachePurgeRequest{
				Selector: adminapi.CacheSelector{
					Kind: "exact_url",
					Host: "portal.example.com",
					URI:  "/r02-open-tighten",
				},
			})

			bodyReader := &blockingBodyReader{
				admitted: make(chan struct{}),
				release:  make(chan struct{}),
				inner:    strings.NewReader(string(payloadBytes)),
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/cache/purge", bodyReader)
			req.RemoteAddr = "127.0.0.1:54321"
			req.Header.Set("Content-Type", "application/json")
			// Credentialless request admitted while administration is open.
			rr := httptest.NewRecorder()
			doneCh := make(chan struct{})
			go func() {
				defer close(doneCh)
				s.routes().ServeHTTP(rr, req)
			}()

			<-bodyReader.admitted
			if mode == "open_to_legacy_token" {
				s.UpdateLiveAdminConfig(config.AdminConfig{
					Token: "newly-required-legacy-token-621",
				})
			} else {
				pol, err := rbac.Build(true, rbac.RoleAdmin, nil, []rbac.PrincipalDef{
					{Name: "admin-user", Role: rbac.RoleAdmin, Token: "rbac-admin-token-32-chars-padded-621"},
				}, "", time.Now())
				if err != nil {
					t.Fatalf("rbac.Build: %v", err)
				}
				s.UpdateLiveAdminConfig(config.AdminConfig{
					RBAC: config.AdminRBACConfig{Enabled: true},
				})
				s.UpdatePolicy(pol)
			}
			close(bodyReader.release)
			<-doneCh

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("%s: expected 401 Unauthorized after live auth tightening on credentialless request, got %d: %s",
					mode, rr.Code, rr.Body.String())
			}
			if execCount.Load() != 0 {
				t.Fatalf("%s: expected 0 engine executions after live auth tightening, got %d", mode, execCount.Load())
			}
		})
	}
}

type delayedScopedPurger struct {
	*cache.Cache
	kind    cache.SelectorKind
	entered chan cache.InvalidateOptions
	release <-chan struct{}
}

func (p *delayedScopedPurger) InvalidateScope(ctx context.Context, sel cache.NormalizedSelector, opts cache.InvalidateOptions) (cache.InvalidateResult, error) {
	if sel.Kind == p.kind {
		p.entered <- opts
		select {
		case <-p.release:
		case <-ctx.Done():
			return cache.InvalidateResult{}, ctx.Err()
		}
	}
	return p.Cache.InvalidateScope(ctx, sel, opts)
}

func TestV1CachePurgeOlderFullRetryPreservesNewerScopedFence(t *testing.T) {
	t.Parallel()

	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			t.Parallel()
			const token = "purge-ordering-admin-token"
			cfg := config.CacheConfig{
				Enabled:       true,
				MemoryMaxSize: config.Size(2 << 20),
				DefaultTTL:    config.Duration(5 * time.Minute),
			}
			if disk {
				cfg.DiskPath = t.TempDir()
				cfg.DiskMaxSize = config.Size(8 << 20)
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			c, err := cache.New(cfg, log)
			if err != nil {
				t.Fatal(err)
			}
			fullRelease := make(chan struct{})
			originEntered := make(chan struct{})
			originRelease := make(chan struct{})
			releaseFull := sync.OnceFunc(func() { close(fullRelease) })
			releaseOrigin := sync.OnceFunc(func() { close(originRelease) })
			purger := &delayedScopedPurger{
				Cache: c, kind: cache.SelectorAll, entered: make(chan cache.InvalidateOptions, 1), release: fullRelease,
			}
			s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
				Cache: c,
				LoadConfig: func() (*config.Config, error) {
					return &config.Config{Cache: cfg}, nil
				},
			})
			initialRR := postV1JSON(t, s, "/api/v1/cache/purge", token, "", adminapi.CachePurgeRequest{
				Selector: adminapi.CacheSelector{Kind: "all", All: true}, ConfirmBroad: true,
			})
			if initialRR.Code != http.StatusOK {
				t.Fatalf("initial full purge: HTTP %d: %s", initialRR.Code, initialRR.Body.String())
			}
			initial := decodeInto[adminapi.CachePurgeResponse](t, initialRR)
			s.purgeLedger.mu.Lock()
			s.purgeLedger.byID[initial.OperationID].Response.State = "partial"
			s.purgeLedger.mu.Unlock()
			s.deps.Cache = purger
			var calls atomic.Int64
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=300")
				if calls.Add(1) == 1 {
					close(originEntered)
					select {
					case <-originRelease:
					case <-r.Context().Done():
						return
					}
					_, _ = io.WriteString(w, "stale-pre-purge")
					return
				}
				_, _ = io.WriteString(w, "fresh-post-purge")
			}))
			t.Cleanup(origin.Close)
			var wg sync.WaitGroup
			t.Cleanup(func() {
				releaseFull()
				releaseOrigin()
				wg.Wait()
			})
			rt, err := router.New(&config.Config{Servers: []config.ServerConfig{{
				Listen: "purge-test", ServerNames: []string{"portal.example.com"},
				Locations: []config.LocationConfig{{
					Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: origin.URL, Cache: true,
				}},
			}}}, map[string]router.Builder{
				router.ActionProxy: func(srv config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
					proxy, err := handler.NewProxy(context.Background(), srv, loc, nil, nil, log, nil)
					if err != nil {
						return nil, err
					}
					return c.Handler(proxy), nil
				},
			}, nil, nil, log)
			if err != nil {
				t.Fatal(err)
			}
			serve := func() *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				rt.For("purge-test").ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://portal.example.com/article", nil))
				return rr
			}
			fullDone := make(chan *httptest.ResponseRecorder, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				fullDone <- postV1JSON(t, s, "/api/v1/cache/purge", token, "", adminapi.CachePurgeRequest{
					Selector: adminapi.CacheSelector{Kind: "all", All: true}, ConfirmBroad: true,
					RetryOperationID: initial.OperationID,
				})
			}()
			var fullOpts cache.InvalidateOptions
			select {
			case fullOpts = <-purger.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("full purge never reached engine entry")
			}
			if fullOpts.ReuseFenceGen != initial.FenceSequence || fullOpts.CommitFence == nil {
				t.Fatalf("full purge retry did not preserve its cutoff: %+v", fullOpts)
			}
			fillDone := make(chan *httptest.ResponseRecorder, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				fillDone <- serve()
			}()
			select {
			case <-originEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("cold fill never reached origin")
			}
			scopedRR := postV1JSON(t, s, "/api/v1/cache/purge", token, "", adminapi.CachePurgeRequest{
				Selector: adminapi.CacheSelector{Kind: "exact_url", Host: "portal.example.com", URI: "/article"},
			})
			if scopedRR.Code != http.StatusOK {
				t.Fatalf("scoped purge: HTTP %d: %s", scopedRR.Code, scopedRR.Body.String())
			}
			scoped := decodeInto[adminapi.CachePurgeResponse](t, scopedRR)
			if scoped.State != "succeeded" || scoped.InvalidationEffective != "effective" || scoped.FenceSequence <= fullOpts.ReuseFenceGen {
				t.Fatalf("unexpected scoped purge result: %+v", scoped)
			}
			if stats := c.IndexStats(); stats.ActiveFences != 1 || stats.InflightOriginOps != 1 {
				t.Fatalf("expected retained scoped fence and blocked fill: %+v", stats)
			}
			releaseFull()
			select {
			case rr := <-fullDone:
				if rr.Code != http.StatusOK {
					t.Fatalf("full purge: HTTP %d: %s", rr.Code, rr.Body.String())
				}
				full := decodeInto[adminapi.CachePurgeResponse](t, rr)
				if full.State != "succeeded" || full.InvalidationEffective != "effective" || full.FenceSequence != fullOpts.ReuseFenceGen {
					t.Fatalf("unexpected full purge result: %+v", full)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("full purge did not finish")
			}
			if stats := c.IndexStats(); stats.ActiveFences != 1 || stats.InflightOriginOps != 1 {
				t.Fatalf("older full purge cleared the newer scoped fence: %+v", stats)
			}
			releaseOrigin()
			select {
			case rr := <-fillDone:
				if rr.Code != http.StatusOK || rr.Body.String() != "stale-pre-purge" {
					t.Fatalf("unexpected original response: HTTP %d body=%q", rr.Code, rr.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original fill did not finish")
			}
			after := serve()
			if after.Header().Get("X-Cache") != "MISS" || after.Body.String() != "fresh-post-purge" {
				t.Fatalf("stale response was cached: X-Cache=%q body=%q", after.Header().Get("X-Cache"), after.Body.String())
			}
			hit := serve()
			if hit.Header().Get("X-Cache") != "HIT" || hit.Body.String() != "fresh-post-purge" || calls.Load() != 2 {
				t.Fatalf("fresh replacement was not cached: X-Cache=%q body=%q calls=%d", hit.Header().Get("X-Cache"), hit.Body.String(), calls.Load())
			}
			if stats := c.IndexStats(); stats.ActiveFences != 0 || stats.InflightOriginOps != 0 {
				t.Fatalf("completed origin work did not reclaim fences: %+v", stats)
			}
		})
	}
}

func TestV1CachePurgeRetiresFillCompletedBeforeEngineEntry(t *testing.T) {
	t.Parallel()

	for _, kind := range []cache.SelectorKind{cache.SelectorAll, cache.SelectorExactURL, cache.SelectorPrefix, cache.SelectorGlob, cache.SelectorTag, cache.SelectorHost, cache.SelectorKey} {
		for _, disk := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/disk=%t", kind, disk), func(t *testing.T) {
				t.Parallel()
				cfg := config.CacheConfig{
					Enabled: true, MemoryMaxSize: config.Size(2 << 20),
					DefaultTTL: config.Duration(5 * time.Minute), TagHeaders: []string{"Cache-Tag"},
				}
				if disk {
					cfg.DiskPath = t.TempDir()
					cfg.DiskMaxSize = config.Size(8 << 20)
				}
				c, err := cache.New(cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				purgeRelease := make(chan struct{})
				originEntered := make(chan struct{})
				originRelease := make(chan struct{})
				releasePurge := sync.OnceFunc(func() { close(purgeRelease) })
				releaseOrigin := sync.OnceFunc(func() { close(originRelease) })
				var wg sync.WaitGroup
				purger := &delayedScopedPurger{
					Cache: c, kind: kind, entered: make(chan cache.InvalidateOptions, 1), release: purgeRelease,
				}
				const token = "pre-engine-purge-admin-token"
				s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
					Cache: purger,
					LoadConfig: func() (*config.Config, error) {
						return &config.Config{Cache: cfg}, nil
					},
				})
				t.Cleanup(func() {
					releaseOrigin()
					releasePurge()
					wg.Wait()
				})
				var calls atomic.Int64
				h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=300")
					w.Header().Set("Cache-Tag", "article:42")
					if calls.Add(1) == 1 {
						close(originEntered)
						<-originRelease
						_, _ = io.WriteString(w, "stale-pre-cutoff")
						return
					}
					_, _ = io.WriteString(w, "fresh-post-cutoff")
				}))
				serve := func() *httptest.ResponseRecorder {
					rr := httptest.NewRecorder()
					h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://portal.example.com/article", nil))
					return rr
				}
				fillDone := make(chan *httptest.ResponseRecorder, 1)
				wg.Add(1)
				go func() {
					defer wg.Done()
					fillDone <- serve()
				}()
				<-originEntered
				sel := adminapi.CacheSelector{Kind: string(kind), Host: "portal.example.com"}
				switch kind {
				case cache.SelectorAll:
					sel.Host, sel.All = "", true
				case cache.SelectorExactURL:
					sel.URI = "/article"
				case cache.SelectorPrefix:
					sel.Prefix = "/article"
				case cache.SelectorGlob:
					sel.Pattern = "/art*"
				case cache.SelectorTag:
					sel.Tags = []string{"article:42"}
				case cache.SelectorKey:
					sel.Host, sel.Key = "", "GET\nportal.example.com\n/article"
				}
				purgeDone := make(chan *httptest.ResponseRecorder, 1)
				wg.Add(1)
				go func() {
					defer wg.Done()
					purgeDone <- postV1JSON(t, s, "/api/v1/cache/purge", token, "", adminapi.CachePurgeRequest{
						Selector: sel, ConfirmBroad: true,
					})
				}()
				opts := <-purger.entered
				if opts.ReuseFenceGen != 0 || opts.CommitFence == nil {
					t.Fatalf("new operation reserved its cutoff before engine entry: %+v", opts)
				}
				releaseOrigin()
				if initial := <-fillDone; initial.Body.String() != "stale-pre-cutoff" {
					t.Fatalf("unexpected original response: %q", initial.Body.String())
				}
				if warm := serve(); warm.Header().Get("X-Cache") != "HIT" {
					t.Fatal("pre-cutoff fill did not finish publication before engine entry")
				}
				releasePurge()
				rr := <-purgeDone
				if rr.Code != http.StatusOK {
					t.Fatalf("purge: HTTP %d: %s", rr.Code, rr.Body.String())
				}
				res := decodeInto[adminapi.CachePurgeResponse](t, rr)
				if res.State != "succeeded" || res.InvalidationEffective != "effective" || res.FenceSequence == 0 || res.InvalidatedRepresentations != 1 {
					t.Fatalf("unexpected purge result: %+v", res)
				}
				after := serve()
				if after.Header().Get("X-Cache") != "MISS" || after.Body.String() != "fresh-post-cutoff" {
					t.Fatalf("stale entry survived purge: X-Cache=%q body=%q", after.Header().Get("X-Cache"), after.Body.String())
				}
			})
		}
	}
}

func TestV1CachePurgeFenceCommitFailureKeepsUnknownCutoff(t *testing.T) {
	t.Parallel()

	diskDir := t.TempDir()
	ledgerPath := filepath.Join(diskDir, diskPurgeLedgerFilename)
	eng := &countingScopedPurger{
		beforeCommit: func() {
			if err := os.Remove(ledgerPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(ledgerPath, 0o700); err != nil {
				t.Fatal(err)
			}
		},
	}
	const token = "fence-commit-failure-admin-token"
	s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
		Cache: eng,
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{Cache: config.CacheConfig{Enabled: true, DiskPath: diskDir}}, nil
		},
	})
	req := adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{Kind: "exact_url", Host: "portal.example.com", URI: "/article"},
	}
	rr := postV1JSON(t, s, "/api/v1/cache/purge", token, "idem-fence-commit-fail", req)
	if rr.Code != http.StatusServiceUnavailable || eng.executions.Load() != 0 {
		t.Fatalf("failed intent commit reached execution: HTTP %d executions=%d", rr.Code, eng.executions.Load())
	}
	s.purgeLedger.mu.Lock()
	rec := s.purgeLedger.records[0]
	fence, state, effective := rec.Response.FenceSequence, rec.Response.State, rec.Response.InvalidationEffective
	s.purgeLedger.mu.Unlock()
	if fence != 0 || state != "failed" || effective != "uncertain" {
		t.Fatalf("uncommitted cutoff was retained as usable: fence=%d state=%s effective=%s", fence, state, effective)
	}
	retry := postV1JSON(t, s, "/api/v1/cache/purge", token, "idem-fence-commit-fail", req)
	if retry.Code != http.StatusConflict || eng.executions.Load() != 0 {
		t.Fatalf("unknown-cutoff retry was accepted: HTTP %d executions=%d", retry.Code, eng.executions.Load())
	}
}

func TestV1CachePurgeFenceCommitFailurePreservesLedgerBoundsAndBindings(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"at_capacity", "retry"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			diskDir := t.TempDir()
			ledgerPath := filepath.Join(diskDir, diskPurgeLedgerFilename)
			eng := &countingScopedPurger{beforeCommit: func() {
				if err := os.Remove(ledgerPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(ledgerPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}}
			const token = "ledger-bounds-failure-admin-token"
			const idemKey = "idem-commit-failure-bounds"
			s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
				Cache: eng,
				LoadConfig: func() (*config.Config, error) {
					return &config.Config{Cache: config.CacheConfig{Enabled: true, DiskPath: diskDir}}, nil
				},
			})
			s.ensurePurgeLedgerInit()
			sel := adminapi.CacheSelector{Kind: "exact_url", Host: "portal.example.com", URI: "/article"}
			norm, err := cache.ParseSelector(toCacheSelector(sel), nil)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := maxRetainedCachePurgeOps
			wantFence := uint64(0)
			if mode == "retry" {
				wantCount, wantFence = 1, 7
			}
			for i := 0; i < wantCount; i++ {
				rec := &cachePurgeRecord{
					OperationID: fmt.Sprintf("prior_%d", i), Principal: "shared", CreatedAt: time.Now().UTC(),
					Response: adminapi.CachePurgeResponse{
						State: "succeeded", Terminal: true, SelectorDigest: norm.DigestHex(),
					},
				}
				if mode == "retry" {
					rec.IdempotencyKey = idemKey
					rec.Response.State = "partial"
					rec.Response.FenceSequence = wantFence
					s.purgeLedger.byIdem[idemMapKey("shared", idemKey)] = rec
				}
				s.purgeLedger.records = append(s.purgeLedger.records, rec)
				s.purgeLedger.byID[rec.OperationID] = rec
			}
			rr := postV1JSON(t, s, "/api/v1/cache/purge", token, idemKey, adminapi.CachePurgeRequest{Selector: sel})
			if rr.Code != http.StatusServiceUnavailable || eng.executions.Load() != 0 {
				t.Fatalf("intent failure: HTTP %d executions=%d", rr.Code, eng.executions.Load())
			}
			s.purgeLedger.mu.Lock()
			count, indexed := len(s.purgeLedger.records), len(s.purgeLedger.byID)
			rec := s.purgeLedger.byIdem[idemMapKey("shared", idemKey)]
			s.purgeLedger.mu.Unlock()
			if count != wantCount || indexed != wantCount {
				t.Fatalf("failed admission duplicated or exceeded ledger bounds: records=%d indexed=%d want=%d", count, indexed, wantCount)
			}
			if rec == nil || rec.Response.FenceSequence != wantFence || !rec.Response.Terminal || rec.Response.InvalidationEffective != "uncertain" {
				t.Fatalf("failed admission lost its idempotency binding: %+v", rec)
			}
		})
	}
}

func TestV1CachePurgeCanceledRetryPreservesCutoffAndBoot(t *testing.T) {
	t.Parallel()

	for _, unsafeCrossBoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("unsafe_cross_boot=%t", unsafeCrossBoot), func(t *testing.T) {
			t.Parallel()
			cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: config.Size(2 << 20), DefaultTTL: config.Duration(5 * time.Minute)}
			c, err := cache.New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			const token = "canceled-retry-purge-admin-token"
			const idemKey = "idem-canceled-purge-retry"
			s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
				Cache: c,
				LoadConfig: func() (*config.Config, error) {
					return &config.Config{Cache: cfg}, nil
				},
			})
			req := adminapi.CachePurgeRequest{Selector: adminapi.CacheSelector{Kind: "all", All: true}, ConfirmBroad: true}
			initialRR := postV1JSON(t, s, "/api/v1/cache/purge", token, idemKey, req)
			if initialRR.Code != http.StatusOK {
				t.Fatalf("initial purge: HTTP %d: %s", initialRR.Code, initialRR.Body.String())
			}
			initial := decodeInto[adminapi.CachePurgeResponse](t, initialRR)
			cutoff, originalBoot := initial.FenceSequence, initial.BootID
			if unsafeCrossBoot {
				cutoff, originalBoot = c.StartupHighWaterGen()+100, "prior-boot"
			}
			s.purgeLedger.mu.Lock()
			rec := s.purgeLedger.byID[initial.OperationID]
			rec.Response.State = "partial"
			rec.Response.FenceSequence = cutoff
			rec.Response.BootID = originalBoot
			s.purgeLedger.mu.Unlock()
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=300")
				_, _ = io.WriteString(w, "fresh-post-cutoff")
			}))
			serve := func() *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://portal.example.com/fresh", nil))
				return rr
			}
			serve()
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			canceledReq := httptest.NewRequest(http.MethodPost, "/api/v1/cache/purge", bytes.NewReader(body)).WithContext(ctx)
			canceledReq.Header.Set("Content-Type", "application/json")
			canceledReq.Header.Set("Authorization", "Bearer "+token)
			canceledReq.Header.Set("Idempotency-Key", idemKey)
			canceledRR := httptest.NewRecorder()
			s.routes().ServeHTTP(canceledRR, canceledReq)
			if canceledRR.Code != http.StatusOK {
				t.Fatalf("canceled retry: HTTP %d: %s", canceledRR.Code, canceledRR.Body.String())
			}
			canceled := decodeInto[adminapi.CachePurgeResponse](t, canceledRR)
			if canceled.State != "canceled" || canceled.FenceSequence != cutoff || canceled.BootID != originalBoot {
				t.Fatalf("canceled retry lost its original boundary: %+v", canceled)
			}
			retry := postV1JSON(t, s, "/api/v1/cache/purge", token, idemKey, req)
			wantStatus := http.StatusOK
			if unsafeCrossBoot {
				wantStatus = http.StatusBadRequest
			}
			if retry.Code != wantStatus {
				t.Fatalf("subsequent retry: HTTP %d want %d: %s", retry.Code, wantStatus, retry.Body.String())
			}
			fresh := serve()
			if fresh.Header().Get("X-Cache") != "HIT" || fresh.Body.String() != "fresh-post-cutoff" {
				t.Fatalf("canceled retry expanded its cutoff: X-Cache=%q body=%q", fresh.Header().Get("X-Cache"), fresh.Body.String())
			}
		})
	}
}

func TestV1CachePurgeRollbackPreservesReusedExpiredIdempotencyKey(t *testing.T) {
	t.Parallel()

	for _, cleanupFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup_fails=%t", cleanupFails), func(t *testing.T) {
			t.Parallel()
			cfg := config.CacheConfig{
				Enabled: true, MemoryMaxSize: config.Size(2 << 20),
				DefaultTTL: config.Duration(5 * time.Minute),
			}
			if cleanupFails {
				cfg.DiskPath, cfg.DiskMaxSize = t.TempDir(), config.Size(8<<20)
			}
			c, err := cache.New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=300")
				_, _ = io.WriteString(w, "warm")
			}))
			for i := 0; i < cache.BroadScopeResourceThr; i++ {
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://portal.example.com/many/%d", i), nil))
			}
			release := make(chan struct{})
			releasePurge := sync.OnceFunc(func() { close(release) })
			purger := &delayedScopedPurger{
				Cache: c, kind: cache.SelectorPrefix, entered: make(chan cache.InvalidateOptions, 1), release: release,
			}
			const token = "rollback-collision-admin-token"
			const reusedKey = "idem-expired-key-reused"
			s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
				Cache: purger,
				LoadConfig: func() (*config.Config, error) {
					return &config.Config{Cache: cfg}, nil
				},
			})
			var wg sync.WaitGroup
			t.Cleanup(func() {
				releasePurge()
				wg.Wait()
			})
			s.ensurePurgeLedgerInit()
			for i := 0; i < maxRetainedCachePurgeOps; i++ {
				rec := &cachePurgeRecord{
					OperationID: fmt.Sprintf("expired_%d", i), Principal: "shared", CreatedAt: time.Now().UTC(),
					Response: adminapi.CachePurgeResponse{State: "succeeded", Terminal: true, SelectorDigest: "expired-selector"},
				}
				if i == 0 {
					rec.IdempotencyKey = reusedKey
					rec.CreatedAt = time.Now().Add(-2 * cachePurgeLedgerMinAge)
					s.purgeLedger.byIdem[idemMapKey("shared", reusedKey)] = rec
				}
				s.purgeLedger.records = append(s.purgeLedger.records, rec)
				s.purgeLedger.byID[rec.OperationID] = rec
			}
			failedDone := make(chan *httptest.ResponseRecorder, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				failedDone <- postV1JSON(t, s, "/api/v1/cache/purge", token, "idem-rollback-broad-denied", adminapi.CachePurgeRequest{
					Selector: adminapi.CacheSelector{Kind: "prefix", Host: "portal.example.com", Prefix: "/many"},
				})
			}()
			<-purger.entered
			newReq := adminapi.CachePurgeRequest{
				Selector: adminapi.CacheSelector{Kind: "exact_url", Host: "portal.example.com", URI: "/unrelated"},
			}
			newRR := postV1JSON(t, s, "/api/v1/cache/purge", token, reusedKey, newReq)
			if newRR.Code != http.StatusOK {
				t.Fatalf("new use of expired key: HTTP %d: %s", newRR.Code, newRR.Body.String())
			}
			newOp := decodeInto[adminapi.CachePurgeResponse](t, newRR)
			if cleanupFails {
				path := filepath.Join(cfg.DiskPath, diskPurgeLedgerFilename)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			releasePurge()
			failed := <-failedDone
			wantStatus, wantCount := http.StatusBadRequest, maxRetainedCachePurgeOps-1
			if cleanupFails {
				wantStatus, wantCount = http.StatusServiceUnavailable, maxRetainedCachePurgeOps
			}
			if failed.Code != wantStatus {
				t.Fatalf("rejected broad purge: HTTP %d want %d: %s", failed.Code, wantStatus, failed.Body.String())
			}
			replayRR := postV1JSON(t, s, "/api/v1/cache/purge", token, reusedKey, newReq)
			if replayRR.Code != http.StatusOK {
				t.Fatalf("rollback overwrote the newer key binding: HTTP %d: %s", replayRR.Code, replayRR.Body.String())
			}
			replay := decodeInto[adminapi.CachePurgeResponse](t, replayRR)
			if !replay.IdempotentReplay || replay.OperationID != newOp.OperationID {
				t.Fatalf("rollback replaced a newer operation: got %+v want operation %s", replay, newOp.OperationID)
			}
			s.purgeLedger.mu.Lock()
			count, indexed := len(s.purgeLedger.records), len(s.purgeLedger.byID)
			s.purgeLedger.mu.Unlock()
			if count != wantCount || indexed != wantCount {
				t.Fatalf("rollback ledger size: records=%d indexed=%d want=%d", count, indexed, wantCount)
			}
		})
	}
}
