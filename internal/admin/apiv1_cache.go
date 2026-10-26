// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"jul/internal/adminapi"
	"jul/internal/adminoidc"
	"jul/internal/atomicfile"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/rbac"
)

const (
	maxConcurrentCachePurges = 4
	maxRetainedCachePurgeOps = 256
	cachePurgeLedgerMinAge   = 15 * time.Minute
	previewTokenMaxAge       = 15 * time.Minute
	defaultCachePurgeTimeout = 10 * time.Second
	maxCachePurgeTimeout     = 30 * time.Second
	diskPurgeLedgerFilename  = ".jul-cache-purge-ledger"
)

// ScopedPurger is the rich cache invalidation interface implemented by
// *cache.Cache (#621, ADR 0027). Minimal test doubles that implement only
// Purger (Purge and Delete) remain supported via fallback.
// InvalidateScope must invoke opts.CommitFence before installing its fence
// or mutating cache entries when that callback is provided.
type ScopedPurger interface {
	Purger
	PreviewScope(sel cache.NormalizedSelector, opts cache.PreviewOptions) cache.PurgePreview
	InvalidateScope(ctx context.Context, sel cache.NormalizedSelector, opts cache.InvalidateOptions) (cache.InvalidateResult, error)
}

type cachePurgeRecord struct {
	OperationID            string                      `json:"operation_id"`
	Principal              string                      `json:"principal"`
	IdempotencyKey         string                      `json:"idempotency_key,omitempty"`
	IdempotencyFingerprint [32]byte                    `json:"idempotency_fingerprint,omitempty"`
	IdempotencyMethod      string                      `json:"idempotency_method,omitempty"`
	IdempotencyOperation   string                      `json:"idempotency_operation,omitempty"`
	CreatedAt              time.Time                   `json:"created_at"`
	Response               adminapi.CachePurgeResponse `json:"response"`
	MatchedResourceIDs     []string                    `json:"matched_resource_ids"`
	execSelector           *cache.NormalizedSelector   `json:"-"`
}

type cachePurgeLedger struct {
	mu         sync.Mutex
	seq        uint64
	loadedDisk string
	records    []*cachePurgeRecord
	byID       map[string]*cachePurgeRecord
	byIdem     map[string]*cachePurgeRecord
}

func newCachePurgeLedger() *cachePurgeLedger {
	return &cachePurgeLedger{
		byID:   make(map[string]*cachePurgeRecord),
		byIdem: make(map[string]*cachePurgeRecord),
	}
}

func idemMapKey(principal, key string) string {
	return strconv.Itoa(len(principal)) + ":" + principal + "\x00" + key
}

func (s *Server) activeCacheConfig() *config.Config {
	if s.deps.LiveSnapshot != nil {
		if snap := s.deps.LiveSnapshot(); snap.EffectiveConfig != nil {
			return snap.EffectiveConfig
		}
	}
	if s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			return cfg
		}
	}
	return nil
}

func (s *Server) cacheDiskPath() string {
	cfg := s.activeCacheConfig()
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Cache.DiskPath)
}

func (s *Server) evictOneLedgerRecordLocked(now time.Time) (*cachePurgeRecord, int, bool) {
	for i, cand := range s.purgeLedger.records {
		if !cand.Response.Terminal {
			continue
		}
		if cand.IdempotencyKey != "" && now.Sub(cand.CreatedAt) < cachePurgeLedgerMinAge {
			continue
		}
		s.purgeLedger.records = append(s.purgeLedger.records[:i], s.purgeLedger.records[i+1:]...)
		delete(s.purgeLedger.byID, cand.OperationID)
		if cand.IdempotencyKey != "" {
			delete(s.purgeLedger.byIdem, idemMapKey(cand.Principal, cand.IdempotencyKey))
		}
		return cand, i, true
	}
	return nil, -1, false
}

// The restored victim's index lets failed cleanup retain the admitted record
// in that slot without exceeding the ledger bound.
func (s *Server) rollbackAdmittedRecordLocked(opID string, rec, reuseRecord, evictedRec *cachePurgeRecord, prevSnapshot adminapi.CachePurgeResponse, evictIdx int, idemMeta *v1IdempotencyMetadata) int {
	if reuseRecord != nil {
		reuseRecord.Response = prevSnapshot
		return -1
	}
	delete(s.purgeLedger.byID, opID)
	if idemMeta != nil {
		delete(s.purgeLedger.byIdem, idemMapKey(idemMeta.Principal, idemMeta.Key))
	}
	for i := len(s.purgeLedger.records) - 1; i >= 0; i-- {
		if s.purgeLedger.records[i] == rec {
			s.purgeLedger.records = append(s.purgeLedger.records[:i], s.purgeLedger.records[i+1:]...)
			break
		}
	}
	if evictedRec != nil && evictIdx >= 0 && evictIdx <= len(s.purgeLedger.records) {
		if evictedRec.IdempotencyKey != "" {
			if _, exists := s.purgeLedger.byIdem[idemMapKey(evictedRec.Principal, evictedRec.IdempotencyKey)]; exists {
				return -1
			}
		}
		s.purgeLedger.records = append(s.purgeLedger.records[:evictIdx], append([]*cachePurgeRecord{evictedRec}, s.purgeLedger.records[evictIdx:]...)...)
		s.purgeLedger.byID[evictedRec.OperationID] = evictedRec
		if evictedRec.IdempotencyKey != "" {
			s.purgeLedger.byIdem[idemMapKey(evictedRec.Principal, evictedRec.IdempotencyKey)] = evictedRec
		}
		return evictIdx
	}
	return -1
}

func (s *Server) ensurePurgeLedgerLoadedLocked(diskDir string) error {
	if s.purgeLedger == nil {
		s.purgeLedger = newCachePurgeLedger()
	}
	if diskDir == "" || s.purgeLedger.loadedDisk == diskDir {
		return nil
	}
	path := filepath.Join(diskDir, diskPurgeLedgerFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.purgeLedger.loadedDisk = diskDir
			return nil
		}
		return err
	}
	var persisted []*cachePurgeRecord
	if err := json.Unmarshal(data, &persisted); err != nil {
		return err
	}
	for _, rec := range persisted {
		if rec == nil || rec.OperationID == "" {
			continue
		}
		if len(rec.MatchedResourceIDs) > cache.MaxPurgeScanKeys {
			return fmt.Errorf("cache purge ledger resource snapshot exceeds the scan bound")
		}
		for _, id := range rec.MatchedResourceIDs {
			if len(id) != sha256.Size*2 || id != strings.ToLower(id) {
				return fmt.Errorf("cache purge ledger contains an invalid resource snapshot ID")
			}
			if _, err := hex.DecodeString(id); err != nil {
				return fmt.Errorf("cache purge ledger contains an invalid resource snapshot ID: %w", err)
			}
		}
	}
	now := time.Now().UTC()
	changed := false
	for _, rec := range persisted {
		if rec == nil || rec.OperationID == "" {
			continue
		}
		if !rec.Response.Terminal {
			rec.Response.State = "failed"
			rec.Response.Terminal = true
			rec.Response.Outcome = "interrupted"
			rec.Response.InvalidationEffective = "uncertain"
			if rec.Response.FenceSequence > 0 {
				rec.Response.RecoveryGuidance = "The server restarted while this invalidation was in flight; outcome is uncertain. Retry with the same Idempotency-Key or operation_id to complete cleanup."
			} else {
				rec.Response.RecoveryGuidance = "The server restarted before a fence cutoff was recorded for this invalidation; outcome is uncertain. Submit a fresh purge request with a new Idempotency-Key if you intend to invalidate current cache entries."
			}
			changed = true
		}
		if rec.Response.Warnings == nil {
			rec.Response.Warnings = []adminapi.CacheGuidance{}
		}
		if _, exists := s.purgeLedger.byID[rec.OperationID]; exists {
			continue
		}
		s.purgeLedger.records = append(s.purgeLedger.records, rec)
		s.purgeLedger.byID[rec.OperationID] = rec
		if rec.IdempotencyKey != "" && rec.Principal != "" {
			s.purgeLedger.byIdem[idemMapKey(rec.Principal, rec.IdempotencyKey)] = rec
		}
	}
	for len(s.purgeLedger.records) > maxRetainedCachePurgeOps {
		if _, _, ok := s.evictOneLedgerRecordLocked(now); !ok {
			break
		}
		changed = true
	}
	if changed {
		if err := s.persistPurgeLedgerLocked(diskDir); err != nil {
			s.purgeLedger.records = nil
			s.purgeLedger.byID = make(map[string]*cachePurgeRecord)
			s.purgeLedger.byIdem = make(map[string]*cachePurgeRecord)
			return err
		}
	}
	s.purgeLedger.loadedDisk = diskDir
	return nil
}

func (s *Server) persistPurgeLedgerLocked(diskDir string) error {
	if diskDir == "" || s.purgeLedger == nil {
		return nil
	}
	data, err := json.Marshal(s.purgeLedger.records)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(diskDir, diskPurgeLedgerFilename), data, 0o600)
}

func (s *Server) callerPrincipal(r *http.Request) string {
	if id, ok := rbacIdentityFromRequest(r); ok && id.Principal != "" {
		return id.Principal
	}
	return "shared"
}

func (s *Server) previewHMACKey() []byte {
	s.previewKeyOnce.Do(func() {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic("jul: crypto/rand.Read failed: " + err.Error())
		}
		s.previewKey = b[:]
	})
	return s.previewKey
}

func (s *Server) mintPreviewToken(principal, selectorDigest string, broad bool, cacheSeq uint64, now time.Time) string {
	ts := now.UTC().Unix()
	mac := s.computePreviewMAC(principal, selectorDigest, broad, cacheSeq, ts)
	return fmt.Sprintf("cpt1.%d.%d.%s", cacheSeq, ts, hex.EncodeToString(mac))
}

func (s *Server) computePreviewMAC(principal, selectorDigest string, broad bool, cacheSeq uint64, ts int64) []byte {
	h := hmac.New(sha256.New, s.previewHMACKey())
	broadBit := "0"
	if broad {
		broadBit = "1"
	}
	_, _ = fmt.Fprintf(h, "%d:%s\n%s\n%s\n%d\n%d\n", len(principal), principal, selectorDigest, broadBit, cacheSeq, ts)
	return h.Sum(nil)[:16]
}

func (s *Server) verifyPreviewToken(token, principal, selectorDigest string, now time.Time) (cacheSeq uint64, wasBroad bool, ok bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 4 || parts[0] != "cpt1" {
		return 0, false, false
	}
	seq, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, false, false
	}
	ts, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, false, false
	}
	issued := time.Unix(ts, 0)
	if now.Sub(issued) > previewTokenMaxAge || issued.Sub(now) > time.Minute {
		return 0, false, false
	}
	gotMAC, err := hex.DecodeString(parts[3])
	if err != nil || len(gotMAC) != 16 {
		return 0, false, false
	}
	for _, broad := range []bool{false, true} {
		wantMAC := s.computePreviewMAC(principal, selectorDigest, broad, seq, ts)
		if hmac.Equal(gotMAC, wantMAC) {
			return seq, broad, true
		}
	}
	return 0, false, false
}

func toCacheSelector(in adminapi.CacheSelector) cache.Selector {
	return cache.Selector{
		Kind:     cache.SelectorKind(in.Kind),
		Host:     in.Host,
		URL:      in.URL,
		URI:      in.URI,
		Prefix:   in.Prefix,
		Pattern:  in.Pattern,
		Tags:     append([]string(nil), in.Tags...),
		TagMatch: cache.TagMatchMode(in.TagMatch),
		Key:      in.Key,
		RouteID:  in.RouteID,
		All:      in.All,
	}
}

func fromCacheSelector(in cache.Selector) adminapi.CacheSelector {
	return adminapi.CacheSelector{
		Kind:     string(in.Kind),
		Host:     in.Host,
		URL:      in.URL,
		URI:      in.URI,
		Prefix:   in.Prefix,
		Pattern:  in.Pattern,
		Tags:     append([]string(nil), in.Tags...),
		TagMatch: string(in.TagMatch),
		Key:      in.Key,
		RouteID:  in.RouteID,
		All:      in.All,
	}
}

func selectorErrorToAPIError(err error) *adminapi.Error {
	var selErr *cache.SelectorError
	field := "selector"
	if errors.As(err, &selErr) && selErr.Field != "" {
		field = "selector." + selErr.Field
	}
	return adminapi.Errorf(adminapi.CodeInvalidRequest, "%s", err.Error()).
		WithDetails(adminapi.Details{Field: field})
}

func buildCachePurgeWarnings(sel cache.NormalizedSelector, broad, truncated, tagEnabled, cacheEnabled bool) []adminapi.CacheGuidance {
	out := make([]adminapi.CacheGuidance, 0, 2)
	if !cacheEnabled {
		out = append(out, adminapi.CacheGuidance{
			Code:     "cache_disabled",
			Severity: "info",
			Scope:    string(sel.Kind),
			Summary:  "Response caching is currently disabled on this node.",
			Detail:   "Purge previews and invalidations succeed as no-ops while [cache].enabled is false.",
			DocsRef:  "docs/cache.md#configuration",
		})
	}
	if sel.Kind == cache.SelectorTag && !tagEnabled {
		out = append(out, adminapi.CacheGuidance{
			Code:     "tag_ingestion_disabled",
			Severity: "warning",
			Scope:    "cache.tag_headers",
			Summary:  "Origin grouping-tag ingestion is not enabled in [cache].tag_headers.",
			Detail:   "Configure [cache].tag_headers (for example [\"Cache-Tag\"]) so Jul indexes origin grouping tags on newly cached responses.",
			DocsRef:  "docs/cache.md#scoped-cache-invalidation",
		})
	}
	if broad {
		out = append(out, adminapi.CacheGuidance{
			Code:     "broad_scope_origin_load",
			Severity: "warning",
			Scope:    string(sel.Kind),
			Summary:  "Broad cache invalidation will cause subsequent requests in this scope to refetch from the origin.",
			Detail:   "Confirm that upstream backends have capacity for the resulting cache misses, or narrow the selector to a path prefix, glob, or tag.",
			DocsRef:  "docs/cache.md#scoped-cache-invalidation",
		})
	}
	if truncated {
		out = append(out, adminapi.CacheGuidance{
			Code:     "truncated_preview",
			Severity: "warning",
			Scope:    string(sel.Kind),
			Summary:  "Preview counts reached the scan limit and represent a lower bound.",
			Detail:   "Execution will re-evaluate the full selector and fence matching in-flight fills.",
			DocsRef:  "docs/cache.md#scoped-cache-invalidation",
		})
	}
	if sel.ExplicitDefaultPort {
		out = append(out, adminapi.CacheGuidance{
			Code:     "host_port_literal_note",
			Severity: "info",
			Scope:    "selector.host",
			Summary:  "Jul keys cached responses by the literal Host authority.",
			Detail:   "A selector with an explicit :80 or :443 matches only requests that sent that explicit port in Host.",
			DocsRef:  "docs/cache.md#cache-key-and-vary",
		})
	}
	out = append(out,
		adminapi.CacheGuidance{
			Code:     "node_local_scope",
			Severity: "info",
			Scope:    "node",
			Summary:  "Invalidates only this Jul.IA node's memory and disk cache tiers.",
			Detail:   "Downstream browser caches and external CDNs are not purged by this operation.",
			DocsRef:  "docs/cache.md#scoped-cache-invalidation",
		},
		adminapi.CacheGuidance{
			Code:     "admitted_readers_continue",
			Severity: "info",
			Scope:    "readers",
			Summary:  "Already-admitted responses currently streaming to clients finish normally.",
			Detail:   "New requests and pre-invalidation in-flight fills cannot serve or publish invalidated representations after completion.",
			DocsRef:  "docs/cache.md#scoped-cache-invalidation",
		},
	)
	return out
}

// handleV1CachePurgePreview serves POST /api/v1/cache/purge/preview.
func (s *Server) handleV1CachePurgePreview(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	var req adminapi.CachePurgePreviewRequest
	if _, apiErr := readV1JSON(w, r, &req); apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	cfg := s.activeCacheConfig()
	normSel, err := cache.ParseSelector(toCacheSelector(req.Selector), cfg)
	if err != nil {
		writeAPIError(w, r, selectorErrorToAPIError(err))
		return
	}

	now := time.Now().UTC()
	cacheEnabled := s.deps.Cache != nil && (cfg == nil || cfg.Cache.Enabled)
	tagEnabled := cfg != nil && len(cfg.Cache.TagHeaders) > 0

	var preview cache.PurgePreview
	if sp, ok := s.deps.Cache.(ScopedPurger); ok && s.deps.Cache != nil {
		sampleMax := 0
		if req.IncludeSamples {
			sampleMax = 20
		}
		preview = sp.PreviewScope(normSel, cache.PreviewOptions{
			MaxScanEntries: req.MaxScanEntries,
			MaxSampleItems: sampleMax,
		})
		tagEnabled = preview.TagIngestionEnabled || tagEnabled
	} else {
		scanLimit := req.MaxScanEntries
		if scanLimit <= 0 || scanLimit > cache.MaxPreviewScanEntries {
			scanLimit = cache.MaxPreviewScanEntries
		}
		preview = cache.PurgePreview{
			Selector:            normSel,
			CountAccuracy:       "exact",
			ScanLimit:           scanLimit,
			AsOf:                now,
			BroadScope:          normSel.BroadScope,
			TagIngestionEnabled: tagEnabled,
		}
	}

	principal := s.callerPrincipal(r)
	digestHex := normSel.DigestHex()
	token := s.mintPreviewToken(principal, digestHex, preview.BroadScope, preview.CacheSequence, preview.AsOf)
	redactedSel := fromCacheSelector(normSel.RedactedSummary())

	samples := make([]adminapi.CacheResourceSample, 0, len(preview.Samples))
	for _, sm := range preview.Samples {
		methods := sm.Methods
		if methods == nil {
			methods = []string{}
		}
		tiers := sm.Tiers
		if tiers == nil {
			tiers = []string{}
		}
		samples = append(samples, adminapi.CacheResourceSample{
			Host:            sm.Host,
			Path:            sm.Path,
			HasQuery:        sm.HasQuery,
			Methods:         methods,
			Representations: sm.Representations,
			Tiers:           tiers,
			BodyBytes:       sm.BodyBytes,
			VaryFields:      sm.VaryFields,
			Tags:            sm.Tags,
			FreshnessState:  sm.FreshnessState,
			CreatedAt:       adminapi.Timestamp(sm.CreatedAt),
			ExpiresAt:       adminapi.Timestamp(sm.ExpiresAt),
			StaleUntil:      adminapi.Timestamp(sm.StaleUntil),
		})
	}

	resp := adminapi.CachePurgePreviewResponse{
		APIVersion:             adminapi.APIVersion,
		CacheEnabled:           cacheEnabled,
		TagIngestionEnabled:    tagEnabled,
		Selector:               redactedSel,
		SelectorDigest:         digestHex,
		PreviewToken:           token,
		MatchedResources:       preview.MatchedResources,
		MatchedRepresentations: preview.MatchedRepresentations,
		MemoryEntries:          preview.MemoryEntries,
		DiskEntries:            preview.DiskEntries,
		MatchedBytes:           preview.MatchedBytes,
		MemoryBytes:            preview.MemoryBytes,
		DiskBytes:              preview.DiskBytes,
		InflightFillsMatched:   preview.InflightFillsMatched,
		InflightTagUnknown:     preview.InflightTagUnknown,
		CountAccuracy:          preview.CountAccuracy,
		Truncated:              preview.Truncated,
		ScanLimit:              preview.ScanLimit,
		ScannedEntries:         preview.ScannedEntries,
		Source:                 "live_cache_index",
		AsOf:                   adminapi.Timestamp(preview.AsOf),
		CacheSequence:          preview.CacheSequence,
		ExecutionModeContract:  "reevaluate_selector_with_fence",
		BroadScope:             preview.BroadScope,
		Warnings:               buildCachePurgeWarnings(normSel, preview.BroadScope, preview.Truncated, tagEnabled, cacheEnabled),
		Samples:                samples,
		AuthorityState:         s.v1AuthorityState(),
	}

	s.recordAudit(r, "cache.purge.preview", "cache", "success",
		fmt.Sprintf("kind=%s host=%s matched_representations=%d broad=%t",
			normSel.Kind, emptyOrDash(normSel.Host), preview.MatchedRepresentations, preview.BroadScope))
	writeAPIJSON(w, http.StatusOK, resp)
}

// handleV1CachePurge serves POST /api/v1/cache/purge.
func (s *Server) handleV1CachePurge(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	var req adminapi.CachePurgeRequest
	body, apiErr := readV1JSON(w, r, &req)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	idemMeta, apiErr := s.v1IdempotencyMetadata(r, body)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}

	cfg := s.activeCacheConfig()
	normSel, err := cache.ParseSelector(toCacheSelector(req.Selector), cfg)
	if err != nil {
		s.recordAudit(r, "cache.purge", "cache", "failure", "rejected: invalid selector")
		writeAPIError(w, r, selectorErrorToAPIError(err))
		return
	}

	principal := s.callerPrincipal(r)
	digestHex := normSel.DigestHex()
	now := time.Now().UTC()

	var previewSeq uint64
	previewWasBroad := false
	if strings.TrimSpace(req.PreviewToken) != "" {
		seq, wasBroad, ok := s.verifyPreviewToken(req.PreviewToken, principal, digestHex, now)
		if !ok {
			s.recordAudit(r, "cache.purge", "cache", "failure", "rejected: preview_token mismatch or expired")
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest,
				"preview_token does not match the authenticated principal and normalized selector, or has expired").
				WithDetails(adminapi.Details{Field: "preview_token"}))
			return
		}
		previewSeq = seq
		previewWasBroad = wasBroad
	}

	if (normSel.BroadScope || previewWasBroad) && !req.ConfirmBroad {
		s.recordAudit(r, "cache.purge", "cache", "failure", "rejected: broad scope confirmation required")
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest,
			"broad cache invalidation (>= 100 matched resources, truncated scan, host, or all) requires confirm_broad = true").
			WithDetails(adminapi.Details{Field: "confirm_broad"}))
		return
	}

	diskDir := s.cacheDiskPath()
	s.ensurePurgeLedgerInit()

	s.purgeLedger.mu.Lock()
	if err := s.ensurePurgeLedgerLoadedLocked(diskDir); err != nil {
		s.purgeLedger.mu.Unlock()
		s.recordAudit(r, "cache.purge", "cache", "failure", "rejected: durable purge ledger unavailable")
		writeAPIError(w, r, adminapi.New(adminapi.CodeStorageUnavailable).WithDetails(adminapi.Details{}))
		return
	}

	var reuseFenceGen uint64
	var reuseRecord *cachePurgeRecord
	if idemMeta != nil {
		if existing, ok := s.purgeLedger.byIdem[idemMapKey(idemMeta.Principal, idemMeta.Key)]; ok {
			if retryID := strings.TrimSpace(req.RetryOperationID); retryID != "" && retryID != existing.OperationID {
				s.purgeLedger.mu.Unlock()
				writeAPIError(w, r, adminapi.Errorf(adminapi.CodeIdempotencyKeyReused,
					"Idempotency-Key and retry_operation_id refer to different cache purge operations.").
					WithDetails(adminapi.Details{Field: "retry_operation_id"}))
				return
			}
			if existing.Response.SelectorDigest != digestHex {
				s.purgeLedger.mu.Unlock()
				writeAPIError(w, r, adminapi.Errorf(adminapi.CodeIdempotencyKeyReused,
					"Idempotency-Key is already bound to a different request.").WithDetails(adminapi.Details{
					RecordedMethod:    existing.IdempotencyMethod,
					RecordedOperation: existing.IdempotencyOperation,
				}))
				return
			}
			if !existing.Response.Terminal {
				opID := existing.OperationID
				s.purgeLedger.mu.Unlock()
				writeAPIError(w, r, adminapi.Errorf(adminapi.CodeIdempotencyKeyInUse,
					"The idempotent cache purge operation is still in flight; poll its operation_id.").
					WithDetails(adminapi.Details{ApplyID: opID}))
				return
			}
			if existing.Response.State == "succeeded" {
				replayResp := existing.Response
				if replayResp.Warnings == nil {
					replayResp.Warnings = []adminapi.CacheGuidance{}
				}
				replayResp.IdempotentReplay = true
				replayResp.AuthorityState = s.v1AuthorityState()
				s.purgeLedger.mu.Unlock()
				writeAPIJSON(w, http.StatusOK, replayResp)
				return
			}
			if existing.Response.FenceSequence == 0 && (existing.Response.Outcome == "interrupted" || existing.Response.InvalidationEffective == "uncertain") {
				opID := existing.OperationID
				s.purgeLedger.mu.Unlock()
				writeAPIError(w, r, adminapi.Errorf(adminapi.CodeIdempotencyKeyReused,
					"Interrupted cache purge operation has an unknown fence cutoff and cannot be safely continued automatically; submit a fresh purge request with a new Idempotency-Key.").
					WithDetails(adminapi.Details{ApplyID: opID}))
				return
			}
			// Prior attempt terminated in partial/failed/canceled state; reuse its
			// fence sequence and record to complete cleanup without invalidating newer fills.
			reuseFenceGen = existing.Response.FenceSequence
			reuseRecord = existing
		}
	}
	if retryID := strings.TrimSpace(req.RetryOperationID); retryID != "" {
		prior, ok := s.purgeLedger.byID[retryID]
		if !ok || prior.Principal != principal {
			s.purgeLedger.mu.Unlock()
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotFound,
				"No cache purge operation with that retry_operation_id.").
				WithDetails(adminapi.Details{Kind: "cache_purge_operation", ID: retryID}))
			return
		}
		if idemMeta != nil && reuseRecord == nil {
			s.purgeLedger.mu.Unlock()
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest,
				"Idempotency-Key is not bound to retry_operation_id; omit the header or reuse the operation's original key.").
				WithDetails(adminapi.Details{Field: "retry_operation_id"}))
			return
		}
		// If the caller is retrying a looked-up operation whose display selector had
		// a redacted query ("?<redacted>") and the server holds the executable
		// selector in memory, resolve the original executable selector.
		if prior.Response.SelectorDigest != digestHex && prior.execSelector != nil {
			redactedPrior := fromCacheSelector(prior.execSelector.RedactedSummary())
			if req.Selector.Kind == redactedPrior.Kind && req.Selector.Host == redactedPrior.Host &&
				((redactedPrior.URI != "" && req.Selector.URI == redactedPrior.URI) ||
					(redactedPrior.Key != "" && req.Selector.Key == redactedPrior.Key)) {
				normSel = *prior.execSelector
				digestHex = normSel.DigestHex()
			}
		}
		if prior.Response.SelectorDigest != digestHex {
			s.purgeLedger.mu.Unlock()
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest,
				"retry_operation_id selector does not match the requested selector").
				WithDetails(adminapi.Details{Field: "retry_operation_id"}))
			return
		}
		if !prior.Response.Terminal {
			opID := prior.OperationID
			s.purgeLedger.mu.Unlock()
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeIdempotencyKeyInUse,
				"The referenced cache purge operation is still in flight; poll its operation_id.").
				WithDetails(adminapi.Details{ApplyID: opID}))
			return
		}
		if prior.Response.State == "succeeded" {
			replayResp := prior.Response
			if replayResp.Warnings == nil {
				replayResp.Warnings = []adminapi.CacheGuidance{}
			}
			replayResp.IdempotentReplay = true
			replayResp.AuthorityState = s.v1AuthorityState()
			s.purgeLedger.mu.Unlock()
			writeAPIJSON(w, http.StatusOK, replayResp)
			return
		}
		if prior.Response.FenceSequence == 0 && (prior.Response.Outcome == "interrupted" || prior.Response.InvalidationEffective == "uncertain") {
			opID := prior.OperationID
			s.purgeLedger.mu.Unlock()
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest,
				"Interrupted cache purge operation has an unknown fence cutoff and cannot be safely continued automatically; submit a fresh purge request without retry_operation_id.").
				WithDetails(adminapi.Details{Field: "retry_operation_id", ApplyID: opID}))
			return
		}
		reuseFenceGen = prior.Response.FenceSequence
		if reuseRecord == nil {
			reuseRecord = prior
		}
	}

	// Stage concurrency admission and live permission re-verification BEFORE
	// evicting or mutating any retained record in s.purgeLedger (F04, F06).
	if s.cachePurgeInflight.Add(1) > maxConcurrentCachePurges {
		s.cachePurgeInflight.Add(-1)
		s.purgeLedger.mu.Unlock()
		one := 1
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeRateLimited,
			"Too many cache invalidation operations are currently in flight; retry shortly.").
			WithDetails(adminapi.Details{RetryAfterSeconds: &one}))
		return
	}
	defer s.cachePurgeInflight.Add(-1)

	if !s.verifyLivePurgePermission(w, r) {
		s.purgeLedger.mu.Unlock()
		return
	}

	var evictedRec *cachePurgeRecord
	evictIdx := -1
	if reuseRecord == nil && len(s.purgeLedger.records) >= maxRetainedCachePurgeOps {
		var ok bool
		evictedRec, evictIdx, ok = s.evictOneLedgerRecordLocked(now)
		if !ok {
			s.purgeLedger.mu.Unlock()
			one := 1
			w.Header().Set("Retry-After", "1")
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeRateLimited,
				"The cache purge idempotency ledger is at capacity within its retention window; retry shortly.").
				WithDetails(adminapi.Details{RetryAfterSeconds: &one}))
			return
		}
	}

	bootID := s.bootID()
	bootShort := bootID
	if len(bootShort) > 8 {
		bootShort = bootShort[:8]
	}
	redactedSel := fromCacheSelector(normSel.RedactedSummary())
	normSelCopy := normSel

	var opID string
	var rec *cachePurgeRecord
	var prevRecSnapshot adminapi.CachePurgeResponse
	var reuseResourceIDs []string
	crossBootRetry := false
	if reuseRecord != nil {
		opID = reuseRecord.OperationID
		rec = reuseRecord
		prevRecSnapshot = reuseRecord.Response
		reuseResourceIDs = reuseRecord.MatchedResourceIDs
		crossBootRetry = prevRecSnapshot.BootID != "" && prevRecSnapshot.BootID != bootID
		rec.execSelector = &normSelCopy
		rec.Response.State = "running"
		rec.Response.Terminal = false
		rec.Response.Outcome = "running"
		rec.Response.FenceSequence = reuseFenceGen
		if rec.Response.Warnings == nil {
			rec.Response.Warnings = []adminapi.CacheGuidance{}
		}
	} else {
		s.purgeLedger.seq++
		opID = fmt.Sprintf("cp_%d_%s", s.purgeLedger.seq, bootShort)
		rec = &cachePurgeRecord{
			OperationID:  opID,
			Principal:    principal,
			CreatedAt:    now,
			execSelector: &normSelCopy,
			Response: adminapi.CachePurgeResponse{
				APIVersion:            adminapi.APIVersion,
				OperationID:           opID,
				State:                 "running",
				Terminal:              false,
				Outcome:               "running",
				InvalidationEffective: "incomplete",
				Selector:              redactedSel,
				SelectorDigest:        digestHex,
				FenceSequence:         reuseFenceGen,
				StartedAt:             adminapi.Timestamp(now),
				Warnings:              []adminapi.CacheGuidance{},
				BootID:                bootID,
				AuthorityState:        s.v1AuthorityState(),
			},
		}
		if idemMeta != nil {
			rec.IdempotencyKey = idemMeta.Key
			rec.IdempotencyFingerprint = idemMeta.Fingerprint
			rec.IdempotencyMethod = idemMeta.Method
			rec.IdempotencyOperation = idemMeta.Operation
			s.purgeLedger.byIdem[idemMapKey(idemMeta.Principal, idemMeta.Key)] = rec
		}
		s.purgeLedger.records = append(s.purgeLedger.records, rec)
		s.purgeLedger.byID[opID] = rec
	}
	if err := s.persistPurgeLedgerLocked(diskDir); err != nil {
		s.rollbackAdmittedRecordLocked(opID, rec, reuseRecord, evictedRec, prevRecSnapshot, evictIdx, idemMeta)
		s.purgeLedger.mu.Unlock()
		s.recordAudit(r, "cache.purge", "cache", "failure", "rejected: durable purge ledger write failed")
		writeAPIError(w, r, adminapi.New(adminapi.CodeStorageUnavailable).WithDetails(adminapi.Details{}))
		return
	}
	s.purgeLedger.mu.Unlock()

	timeout := defaultCachePurgeTimeout
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
		if timeout > maxCachePurgeTimeout {
			timeout = maxCachePurgeTimeout
		}
	}
	opCtx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	cacheEnabled := s.deps.Cache != nil && (cfg == nil || cfg.Cache.Enabled)
	tagEnabled := cfg != nil && len(cfg.Cache.TagHeaders) > 0

	requireBroadConfirm := !req.ConfirmBroad
	var invRes cache.InvalidateResult
	var invErr error
	fenceCommitted := false
	if s.deps.Cache == nil {
		invRes = cache.InvalidateResult{
			Selector:              normSel,
			State:                 "succeeded",
			Outcome:               "cache_disabled",
			InvalidationEffective: "effective",
			StartedAt:             now,
			CompletedAt:           time.Now().UTC(),
		}
	} else if sp, ok := s.deps.Cache.(ScopedPurger); ok {
		invRes, invErr = sp.InvalidateScope(opCtx, normSel, cache.InvalidateOptions{
			PreviewSequence:     previewSeq,
			ReuseFenceGen:       reuseFenceGen,
			ReuseResourceIDs:    reuseResourceIDs,
			CrossBootRetry:      crossBootRetry,
			RequireBroadConfirm: requireBroadConfirm,
			CommitFence: func(fenceGen uint64, resourceIDs []string) error {
				s.purgeLedger.mu.Lock()
				defer s.purgeLedger.mu.Unlock()
				prevFence := rec.Response.FenceSequence
				prevResources := rec.MatchedResourceIDs
				rec.Response.FenceSequence = fenceGen
				rec.MatchedResourceIDs = resourceIDs
				if err := s.persistPurgeLedgerLocked(diskDir); err != nil {
					rec.Response.FenceSequence = prevFence
					rec.MatchedResourceIDs = prevResources
					return err
				}
				fenceCommitted = true
				return nil
			},
		})
	} else {
		// Minimal Purger fake in legacy tests.
		switch normSel.Kind {
		case cache.SelectorAll:
			s.deps.Cache.Purge()
		case cache.SelectorKey:
			s.deps.Cache.Delete(normSel.Key)
		case cache.SelectorExactURL:
			s.deps.Cache.Delete("GET\n" + normSel.Host + "\n" + normSel.URI)
			s.deps.Cache.Delete("HEAD\n" + normSel.Host + "\n" + normSel.URI)
		default:
			s.deps.Cache.Purge()
		}
		invRes = cache.InvalidateResult{
			Selector:                   normSel,
			State:                      "succeeded",
			Outcome:                    "succeeded",
			InvalidationEffective:      "effective",
			InvalidatedResources:       1,
			InvalidatedRepresentations: 1,
			MemoryEntriesRemoved:       1,
			StartedAt:                  now,
			CompletedAt:                time.Now().UTC(),
		}
	}

	if invErr != nil && invRes.State == "" {
		s.purgeLedger.mu.Lock()
		restoredEvictIdx := s.rollbackAdmittedRecordLocked(opID, rec, reuseRecord, evictedRec, prevRecSnapshot, evictIdx, idemMeta)
		if pErr := s.persistPurgeLedgerLocked(diskDir); pErr != nil {
			rec.Response.State = "failed"
			rec.Response.Terminal = true
			rec.Response.Outcome = "failed"
			rec.Response.InvalidationEffective = "uncertain"
			if reuseRecord == nil {
				if restoredEvictIdx >= 0 {
					s.purgeLedger.records[restoredEvictIdx] = rec
					delete(s.purgeLedger.byID, evictedRec.OperationID)
					if evictedRec.IdempotencyKey != "" {
						delete(s.purgeLedger.byIdem, idemMapKey(evictedRec.Principal, evictedRec.IdempotencyKey))
					}
				} else {
					s.purgeLedger.records = append(s.purgeLedger.records, rec)
				}
			}
			s.purgeLedger.byID[opID] = rec
			if rec.IdempotencyKey != "" {
				s.purgeLedger.byIdem[idemMapKey(rec.Principal, rec.IdempotencyKey)] = rec
			}
			s.purgeLedger.mu.Unlock()
			s.recordAudit(r, "cache.purge", "cache", "failure", "rejected: durable purge ledger cleanup write failed")
			writeAPIError(w, r, adminapi.New(adminapi.CodeStorageUnavailable).WithDetails(adminapi.Details{}))
			return
		}
		s.purgeLedger.mu.Unlock()
		s.recordAudit(r, "cache.purge", "cache", "failure", "rejected: "+invErr.Error())
		if errors.Is(invErr, cache.ErrFenceCommit) {
			writeAPIError(w, r, adminapi.New(adminapi.CodeStorageUnavailable).WithDetails(adminapi.Details{}))
			return
		}
		errField := "selector"
		if errors.Is(invErr, cache.ErrBroadConfirmRequired) {
			errField = "confirm_broad"
		}
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, "%s", invErr.Error()).
			WithDetails(adminapi.Details{Field: errField}))
		return
	}

	finalBroad := normSel.BroadScope || invRes.InvalidatedResources >= cache.BroadScopeResourceThr
	responseBootID := bootID
	if reuseRecord != nil && invRes.State == "canceled" && !fenceCommitted {
		responseBootID = prevRecSnapshot.BootID
	}
	finalResp := adminapi.CachePurgeResponse{
		APIVersion:                 adminapi.APIVersion,
		OperationID:                opID,
		State:                      invRes.State,
		Terminal:                   true,
		Outcome:                    invRes.Outcome,
		InvalidationEffective:      invRes.InvalidationEffective,
		Selector:                   redactedSel,
		SelectorDigest:             digestHex,
		FenceSequence:              invRes.FenceSequence,
		StateChangedSincePreview:   invRes.StateChangedSincePreview,
		InvalidatedResources:       invRes.InvalidatedResources,
		InvalidatedRepresentations: invRes.InvalidatedRepresentations,
		MemoryEntriesRemoved:       invRes.MemoryEntriesRemoved,
		DiskEntriesRemoved:         invRes.DiskEntriesRemoved,
		ActiveReadersRetired:       invRes.ActiveReadersRetired,
		FencedInflightOps:          invRes.FencedInflightOps,
		SkippedEntries:             invRes.SkippedEntries,
		FailedEntries:              invRes.FailedEntries,
		RemainingEntries:           invRes.RemainingEntries,
		StartedAt:                  adminapi.Timestamp(invRes.StartedAt),
		CompletedAt:                adminapi.Timestamp(invRes.CompletedAt),
		RecoveryGuidance:           invRes.RecoveryGuidance,
		Warnings:                   buildCachePurgeWarnings(normSel, finalBroad, false, tagEnabled, cacheEnabled),
		BootID:                     responseBootID,
		AuthorityState:             s.v1AuthorityState(),
	}

	s.purgeLedger.mu.Lock()
	rec.Response = finalResp
	prevResources := rec.MatchedResourceIDs
	if finalResp.State == "succeeded" {
		rec.MatchedResourceIDs = nil
	}
	if err := s.persistPurgeLedgerLocked(diskDir); err != nil {
		rec.MatchedResourceIDs = prevResources
		finalResp.State = "partial"
		finalResp.Outcome = "partial_failure"
		finalResp.InvalidationEffective = "uncertain"
		finalResp.RecoveryGuidance = "Cache entries were invalidated in memory/disk, but persisting the durable operation ledger failed; inspect [cache].disk_path before retrying."
		rec.Response = finalResp
	}
	s.purgeLedger.mu.Unlock()

	auditResult := "success"
	if finalResp.State != "succeeded" {
		auditResult = "failure"
	}
	s.recordAudit(r, "cache.purge", "cache", auditResult,
		fmt.Sprintf("op=%s kind=%s host=%s outcome=%s effective=%s reps=%d mem=%d disk=%d failed=%d",
			opID, normSel.Kind, emptyOrDash(normSel.Host), finalResp.Outcome, finalResp.InvalidationEffective,
			finalResp.InvalidatedRepresentations, finalResp.MemoryEntriesRemoved, finalResp.DiskEntriesRemoved, finalResp.FailedEntries))

	writeAPIJSON(w, http.StatusOK, finalResp)
}

// handleV1CachePurgeGet serves GET /api/v1/cache/purges/{operation_id}.
func (s *Server) handleV1CachePurgeGet(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	opID := strings.TrimSpace(r.PathValue("operation_id"))
	if opID == "" {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, "operation_id is required").
			WithDetails(adminapi.Details{Field: "operation_id"}))
		return
	}
	diskDir := s.cacheDiskPath()
	s.ensurePurgeLedgerInit()

	s.purgeLedger.mu.Lock()
	if err := s.ensurePurgeLedgerLoadedLocked(diskDir); err != nil {
		s.purgeLedger.mu.Unlock()
		writeAPIError(w, r, adminapi.New(adminapi.CodeStorageUnavailable).WithDetails(adminapi.Details{}))
		return
	}
	rec, ok := s.purgeLedger.byID[opID]
	var resp adminapi.CachePurgeResponse
	var owner string
	if ok {
		resp = rec.Response
		if resp.Warnings == nil {
			resp.Warnings = []adminapi.CacheGuidance{}
		}
		owner = rec.Principal
	}
	s.purgeLedger.mu.Unlock()

	if !ok {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotFound, "No cache purge operation with that id.").
			WithDetails(adminapi.Details{Kind: "cache_purge_operation", ID: opID}))
		return
	}

	caller := s.callerPrincipal(r)
	canReadAll := false
	if id, hasID := rbacIdentityFromRequest(r); hasID {
		canReadAll = id.Has(rbac.AdminManage) || id.Has(rbac.AuditRead)
	} else {
		canReadAll = true
	}
	if !canReadAll && owner != caller {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotFound, "No cache purge operation with that id.").
			WithDetails(adminapi.Details{Kind: "cache_purge_operation", ID: opID}))
		return
	}

	resp.AuthorityState = s.v1AuthorityState()
	status := http.StatusOK
	if !resp.Terminal {
		status = http.StatusAccepted
	}
	writeAPIJSON(w, status, resp)
}

func (s *Server) ensurePurgeLedgerInit() {
	s.purgeLedgerOnce.Do(func() {
		if s.purgeLedger == nil {
			s.purgeLedger = newCachePurgeLedger()
		}
	})
}

func (s *Server) v1AuthorityState() adminapi.AuthorityState {
	return authorityState(s.currentAuthority())
}

func emptyOrDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func (s *Server) verifyLivePurgePermission(w http.ResponseWriter, r *http.Request) bool {
	snap := s.currentAuth()
	now := time.Now()
	switch snap.mode {
	case authModeBlocked:
		writeRBACUnavailable(w, r)
		return false
	case authModeRBAC:
		bearer := adminAuthorization(r)
		if bearer != "" || adminoidc.CookiePresented(r) {
			authID, err := authenticateAdminRBAC(r, snap, bearer, now)
			if err == rbac.ErrDisabled {
				s.writeUnauthenticated(w, r, "The principal is disabled or expired.")
				return false
			}
			if err != nil {
				s.writeUnauthenticated(w, r, "")
				return false
			}
			if prevID, ok := rbacIdentityFromRequest(r); ok && prevID.Principal != "" && authID.Principal != prevID.Principal {
				s.writeUnauthenticated(w, r, "")
				return false
			}
			if !snap.policy.Authorize(authID, rbac.CachePurge) {
				s.recordAudit(r, "authz.deny", r.URL.Path, "denied", string(rbac.CachePurge))
				writeForbidden(w, r, rbac.CachePurge, authID)
				return false
			}
			return true
		}
		if id, ok := rbacIdentityFromRequest(r); ok && !id.Legacy {
			if !snap.policy.Authorize(id, rbac.CachePurge) {
				s.recordAudit(r, "authz.deny", r.URL.Path, "denied", string(rbac.CachePurge))
				writeForbidden(w, r, rbac.CachePurge, id)
				return false
			}
			return true
		}
		s.writeUnauthenticated(w, r, "")
		return false
	case authModeLegacy:
		if !checkLegacyToken(r, snap.cfg.Token) {
			s.writeUnauthenticated(w, r, "")
			return false
		}
		return true
	default:
		return true
	}
}
