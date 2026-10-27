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
	"net/http"
	"strconv"
	"strings"
	"time"

	"jul/internal/adminapi"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/rbac"
)

const maxInspectionSnapshots = 16
const maxInspectionSnapshotBytes = 8 << 20
const maxInspectionPageBytes = 2 << 20 // Below the shared client's 4 MiB safety limit.
const inspectionSnapshotTTL = time.Minute

type cacheInspector interface {
	InspectScope(context.Context, cache.NormalizedSelector) (cache.Inspection, error)
}
type inspectionSnapshot struct {
	principal string
	scope     string
	auth      *authSnapshot
	configID  [32]byte
	expires   time.Time
	data      cache.Inspection
	bytes     int
	timer     *time.Timer
}

func inspectionConfigID(cfg *config.Config) [32]byte {
	b, _ := json.Marshal(cfg)
	return sha256.Sum256(b)
}

func configuredInspectionScope(sel cache.NormalizedSelector, cfg *config.Config) bool {
	if cfg == nil || sel.Kind == cache.SelectorAll {
		return false
	}
	host := sel.Host
	if sel.Kind == cache.SelectorKey {
		parts := strings.Split(sel.Key, "\n")
		if len(parts) != 3 {
			return false
		}
		host = parts[1]
	}
	bare := middleware.CanonicalHTTPHost(host)
	for _, srv := range cfg.Servers {
		for _, name := range srv.ServerNames {
			name = config.NormalizeHostPattern(name)
			if name == host || name == bare {
				return true
			}
		}
	}
	// Unnamed/wildcard hosts need an explicit named configuration before generic
	// enumeration; this does not widen purge or router wildcard semantics.
	return false
}

func (s *Server) handleV1CacheInspect(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	var req adminapi.CacheInspectRequest
	if _, e := readV1JSON(w, r, &req); e != nil {
		writeAPIError(w, r, adminapi.Errorf(e.Code, "Invalid inspection request body."))
		return
	}
	fail := func(code adminapi.Code, msg string) { writeAPIError(w, r, adminapi.Errorf(code, "%s", msg)) }
	if req.PageSize == 0 {
		req.PageSize = 50
	}
	if req.PageSize < 1 || req.PageSize > 50 || len(req.Cursor) > 160 {
		fail(adminapi.CodeInvalidRequest, "page_size must be 1..50 and cursor must be bounded")
		return
	}
	if !s.verifyLiveCachePermission(w, r, rbac.CacheInspect) {
		return
	}
	cfg := s.activeCacheConfig()
	sel, err := cache.ParseInspectionSelector(toCacheSelector(req.Selector), cfg)
	if err != nil {
		fail(adminapi.CodeInvalidRequest, "Invalid inspection selector; check host, route, path or tag syntax.")
		return
	}
	if !configuredInspectionScope(sel, cfg) {
		fail(adminapi.CodeInvalidRequest, "inspection requires an explicitly configured host or named route; whole-node inspection is unsupported")
		return
	}
	if !s.verifyLiveCachePermission(w, r, rbac.CacheInspect) {
		return
	}
	auth := s.currentAuth()
	principal := s.callerPrincipal(r)
	cfgID := inspectionConfigID(cfg)
	now := time.Now().UTC()
	var id string
	var snap *inspectionSnapshot
	retained, published := false, false
	defer func() {
		if retained && (!published || r.Context().Err() != nil) {
			s.inspectionMu.Lock()
			if s.inspectionSnapshots[id] == snap {
				delete(s.inspectionSnapshots, id)
			}
			if snap.timer != nil {
				snap.timer.Stop()
			}
			s.inspectionMu.Unlock()
		}
	}()
	offset := 0
	if req.Cursor != "" {
		parts := strings.Split(req.Cursor, ".")
		if len(parts) != 3 {
			fail(adminapi.CodeInvalidRequest, "invalid inspection cursor")
			return
		}
		id = parts[0]
		offset, err = strconv.Atoi(parts[1])
		if err != nil || offset < 1 || offset > cache.MaxInspectionResources {
			fail(adminapi.CodeInvalidRequest, "invalid inspection cursor")
			return
		}
		s.inspectionMu.Lock()
		snap = s.inspectionSnapshots[id]
		s.inspectionMu.Unlock()
		if snap == nil || !hmac.Equal([]byte(parts[2]), []byte(s.inspectionCursorMAC(id, offset, snap))) || !now.Before(snap.expires) || snap.principal != principal || snap.scope != sel.DigestHex() || snap.auth != auth || snap.configID != cfgID || offset >= len(snap.data.Resources) {
			fail(adminapi.CodeInvalidRequest, "inspection cursor expired or does not match current authorization and scope; restart inspection")
			return
		}
	} else {
		if s.inspectionBuilders.Add(1) > 2 {
			s.inspectionBuilders.Add(-1)
			w.Header().Set("Retry-After", "1")
			fail(adminapi.CodeRateLimited, "inspection builders are busy; retry later")
			return
		}
		defer s.inspectionBuilders.Add(-1)
		data := cache.Inspection{State: "unavailable", Resources: []cache.SafeResourceSummary{}, Decisions: []cache.CacheDecision{}, Warnings: []string{"cache_inspector_unavailable"}, EvidenceState: "unavailable", CapturedAt: now}
		if cfg != nil && !cfg.Cache.Enabled {
			data.State = "disabled"
		} else if inspector, ok := s.deps.Cache.(cacheInspector); ok {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			data, err = inspector.InspectScope(ctx, sel)
			if err != nil {
				fail(adminapi.CodeStorageUnavailable, "inspection canceled or unavailable; retry with a new inspection")
				return
			}
		}
		if r.Context().Err() != nil {
			return
		}
		b, e := json.Marshal(data)
		if e != nil {
			fail(adminapi.CodeStorageUnavailable, "inspection metadata could not be encoded")
			return
		}
		if len(b) > maxInspectionSnapshotBytes {
			fail(adminapi.CodeStorageUnavailable, "inspection exceeded the metadata byte limit; narrow the scope")
			return
		}
		random := make([]byte, 16)
		if _, e = rand.Read(random); e != nil {
			fail(adminapi.CodeStorageUnavailable, "inspection cursor unavailable")
			return
		}
		id = hex.EncodeToString(random)
		expires := now.Add(inspectionSnapshotTTL)
		for _, d := range data.Decisions {
			if d.ExpiresAt.Before(expires) {
				expires = d.ExpiresAt
			}
		}
		snap = &inspectionSnapshot{principal: principal, scope: sel.DigestHex(), auth: auth, configID: cfgID, expires: expires, data: data, bytes: len(b)}
		if !s.verifyLiveCachePermission(w, r, rbac.CacheInspect) {
			return
		}
		if s.currentAuth() != auth || inspectionConfigID(s.activeCacheConfig()) != cfgID {
			fail(adminapi.CodeInvalidRequest, "configuration or authorization changed; restart inspection")
			return
		}
		if r.Context().Err() != nil {
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	if !s.verifyLiveCachePermission(w, r, rbac.CacheInspect) {
		return
	}
	if s.currentAuth() != auth || inspectionConfigID(s.activeCacheConfig()) != cfgID {
		fail(adminapi.CodeInvalidRequest, "configuration or authorization changed; restart inspection")
		return
	}
	if !time.Now().Before(snap.expires) {
		fail(adminapi.CodeInvalidRequest, "inspection evidence expired during capture; restart inspection")
		return
	}
	data := snap.data
	end := min(offset+req.PageSize, len(data.Resources))
	data.Resources = data.Resources[offset:end]
	change := "unknown_after_capture"
	if seq, ok := s.deps.Cache.(interface{ InspectionSequence() uint64 }); ok {
		change = "no_sequence_change_other_churn_unknown"
		if seq.InspectionSequence() != snap.data.CacheSequence {
			change = "observed_change_since_capture"
		}
	}
	resp := adminapi.CacheInspectResponse{ConcurrentChange: change, APIVersion: adminapi.APIVersion, Selector: fromCacheSelector(sel.RedactedSummary()), Inspection: data, ExpiresAt: adminapi.Timestamp(snap.expires), Consistency: "bounded_metadata_capture_not_global_snapshot", PolicySource: "global_inherited_origin_precedence_route_overrides_unsupported", RouteCacheState: "host_scope_may_include_disabled_routes", AuthorityState: s.v1AuthorityState()}
	if sel.RouteID != "" {
		for _, srv := range cfg.Servers {
			for _, loc := range srv.Locations {
				if loc.RouteID != nil && *loc.RouteID == sel.RouteID {
					if loc.Cache {
						resp.RouteCacheState = "enabled"
					} else {
						resp.RouteCacheState = "disabled_retained_entries_may_exist"
					}
				}
			}
		}
	}
	// page_size is an upper bound. Wide representations must remain usable by
	// the shared client without raising its response safety limit. Include the
	// cursor and complete envelope when measuring the actual encoded response.
	for {
		resp.Inspection.Resources = snap.data.Resources[offset:end]
		resp.NextCursor = ""
		if end < len(snap.data.Resources) {
			resp.NextCursor = id + "." + strconv.Itoa(end) + "." + s.inspectionCursorMAC(id, end, snap)
		}
		encoded, e := json.Marshal(resp)
		if e != nil || (len(encoded)+1 > maxInspectionPageBytes && end-offset <= 1) {
			fail(adminapi.CodeStorageUnavailable, "inspection page exceeded the metadata limit; narrow the scope")
			return
		}
		if len(encoded)+1 <= maxInspectionPageBytes {
			break
		}
		end = offset + (end-offset)/2
	}
	if req.Cursor == "" && resp.NextCursor != "" {
		s.inspectionMu.Lock()
		if s.inspectionSnapshots == nil {
			s.inspectionSnapshots = make(map[string]*inspectionSnapshot)
		}
		total := snap.bytes
		for _, p := range s.inspectionSnapshots {
			total += p.bytes
		}
		if len(s.inspectionSnapshots) >= maxInspectionSnapshots || total > maxInspectionSnapshotBytes {
			s.inspectionMu.Unlock()
			w.Header().Set("Retry-After", "1")
			fail(adminapi.CodeRateLimited, "inspection retention capacity reached; retry later")
			return
		}
		s.inspectionSnapshots[id] = snap
		retained = true
		s.inspectionMu.Unlock()
		snap.timer = time.AfterFunc(time.Until(snap.expires), func() { s.inspectionMu.Lock(); delete(s.inspectionSnapshots, id); s.inspectionMu.Unlock() })
	}

	if r.Context().Err() != nil {
		return
	}
	if !s.verifyLiveCachePermission(w, r, rbac.CacheInspect) {
		return
	}
	if s.currentAuth() != auth || inspectionConfigID(s.activeCacheConfig()) != cfgID || !time.Now().Before(snap.expires) {
		fail(adminapi.CodeInvalidRequest, "inspection authorization, configuration or evidence changed; restart inspection")
		return
	}
	published = true
	s.recordAudit(r, "cache.inspect", "cache", "success", "bounded metadata inspection")
	writeAPIJSON(w, http.StatusOK, resp)
}

func (s *Server) inspectionCursorMAC(id string, offset int, p *inspectionSnapshot) string {
	return hex.EncodeToString(s.computePreviewMAC(p.principal, "inspect:"+id+":"+p.scope, false, uint64(offset), p.expires.Unix()))
}
