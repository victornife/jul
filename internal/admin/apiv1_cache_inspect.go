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
	"jul/internal/cachemeta"
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

func applyRoutePolicyToEffective(cfg *config.Config, loc config.LocationConfig, pol cachemeta.EffectivePolicy) (cachemeta.EffectivePolicy, bool) {
	pol.RouteOverridesSupported = true
	pol.GatewayFreshnessCapSupported = true
	if cfg != nil {
		pol.DefaultTTLSeconds = cfg.Cache.DefaultTTL.Std().Seconds()
		pol.SWRSeconds = cfg.Cache.StaleWhileRevalidate.Std().Seconds()
		pol.SIESeconds = cfg.Cache.StaleIfError.Std().Seconds()
	}
	pol.MaxTTLSeconds = nil
	pol.BypassCookies = nil
	pol.BypassAnyCookie = false
	hasOverride := false
	if cp := loc.CachePolicy; cp != nil {
		if cp.DefaultTTL != nil {
			pol.DefaultTTLSeconds = cp.DefaultTTL.Std().Seconds()
			hasOverride = true
		}
		if cp.MaxTTL != nil {
			v := cp.MaxTTL.Std().Seconds()
			pol.MaxTTLSeconds = &v
			hasOverride = true
		}
		if cp.StaleWhileRevalidate != nil {
			pol.SWRSeconds = cp.StaleWhileRevalidate.Std().Seconds()
			hasOverride = true
		}
		if cp.StaleIfError != nil {
			pol.SIESeconds = cp.StaleIfError.Std().Seconds()
			hasOverride = true
		}
		if len(cp.BypassCookies) > 0 {
			pol.BypassCookies = append([]string(nil), cp.BypassCookies...)
			hasOverride = true
		}
		if cp.BypassAnyCookie {
			pol.BypassAnyCookie = true
			hasOverride = true
		}
	}
	return pol, hasOverride
}

func locationHasPredicates(loc config.LocationConfig) bool {
	return loc.Match.HasPredicates()
}

func locationMatchesInspectionSelector(loc config.LocationConfig, sel cache.NormalizedSelector) bool {
	targetPath := ""
	switch sel.Kind {
	case cache.SelectorExactURL:
		targetPath = sel.URI
		if q := strings.IndexByte(targetPath, '?'); q >= 0 {
			targetPath = targetPath[:q]
		}
	case cache.SelectorKey:
		parts := strings.Split(sel.Key, "\n")
		if len(parts) == 3 {
			targetPath = parts[2]
			if nl := strings.IndexByte(targetPath, 0x00); nl >= 0 {
				targetPath = targetPath[:nl]
			}
			if q := strings.IndexByte(targetPath, '?'); q >= 0 {
				targetPath = targetPath[:q]
			}
		}
	case cache.SelectorPrefix:
		switch loc.Match.Type {
		case "exact":
			return strings.HasPrefix(loc.Match.Path, sel.Prefix)
		case "prefix", "":
			return strings.HasPrefix(loc.Match.Path, sel.Prefix) || strings.HasPrefix(sel.Prefix, loc.Match.Path)
		default:
			return true
		}
	default:
		return true
	}
	if targetPath == "" {
		return true
	}
	switch loc.Match.Type {
	case "exact":
		return targetPath == loc.Match.Path
	case "prefix", "":
		return strings.HasPrefix(targetPath, loc.Match.Path)
	default:
		return true
	}
}

func resolveInspectionRoutePolicy(sel cache.NormalizedSelector, cfg *config.Config, base cachemeta.EffectivePolicy) (cachemeta.EffectivePolicy, string, string, []string) {
	base.RouteOverridesSupported = true
	base.GatewayFreshnessCapSupported = true
	defaultSource := "global_inherited_origin_precedence"
	defaultRouteState := "host_scope_may_include_disabled_routes"
	if cfg == nil {
		return base, defaultSource, defaultRouteState, nil
	}
	if sel.RouteID != "" {
		for _, srv := range cfg.Servers {
			for _, loc := range srv.Locations {
				if loc.RouteID != nil && *loc.RouteID == sel.RouteID {
					routeState := "disabled_retained_entries_may_exist"
					if loc.Cache {
						routeState = "enabled"
					}
					pol, hasOverride := applyRoutePolicyToEffective(cfg, loc, base)
					if hasOverride {
						return pol, "route_override_with_global_inheritance", routeState, nil
					}
					return pol, defaultSource, routeState, nil
				}
			}
		}
		return base, defaultSource, defaultRouteState, nil
	}
	host := sel.Host
	if sel.Kind == cache.SelectorKey {
		parts := strings.Split(sel.Key, "\n")
		if len(parts) == 3 {
			host = parts[1]
		}
	}
	bare := middleware.CanonicalHTTPHost(host)
	var candidates []config.LocationConfig
	for _, srv := range cfg.Servers {
		matchedServer := false
		for _, name := range srv.ServerNames {
			norm := config.NormalizeHostPattern(name)
			if norm == host || norm == bare {
				matchedServer = true
				break
			}
		}
		if !matchedServer {
			continue
		}
		for _, loc := range srv.Locations {
			if locationMatchesInspectionSelector(loc, sel) {
				candidates = append(candidates, loc)
			}
		}
	}
	if len(candidates) == 0 {
		return base, defaultSource, defaultRouteState, nil
	}
	if len(candidates) == 1 && !locationHasPredicates(candidates[0]) {
		pol, hasOverride := applyRoutePolicyToEffective(cfg, candidates[0], base)
		source := defaultSource
		if hasOverride {
			source = "route_override_with_global_inheritance"
		}
		routeState := defaultRouteState
		if sel.Kind == cache.SelectorExactURL || sel.Kind == cache.SelectorKey {
			if candidates[0].Cache {
				routeState = "enabled"
			} else {
				routeState = "disabled_retained_entries_may_exist"
			}
		}
		return pol, source, routeState, nil
	}
	// Multiple candidate routes (or a predicate-scoped route) match this selector without route_id.
	// Check whether any candidate uses route cache overrides, predicates, or differing cache toggles.
	firstPol, firstHasOverride := applyRoutePolicyToEffective(cfg, candidates[0], base)
	ambiguous := locationHasPredicates(candidates[0]) && firstHasOverride
	for i := 1; i < len(candidates); i++ {
		nextPol, nextHasOverride := applyRoutePolicyToEffective(cfg, candidates[i], base)
		if locationHasPredicates(candidates[i]) || candidates[i].Cache != candidates[0].Cache || nextHasOverride != firstHasOverride ||
			nextPol.DefaultTTLSeconds != firstPol.DefaultTTLSeconds || nextPol.SWRSeconds != firstPol.SWRSeconds || nextPol.SIESeconds != firstPol.SIESeconds ||
			(nextPol.MaxTTLSeconds == nil) != (firstPol.MaxTTLSeconds == nil) ||
			(nextPol.MaxTTLSeconds != nil && firstPol.MaxTTLSeconds != nil && *nextPol.MaxTTLSeconds != *firstPol.MaxTTLSeconds) ||
			nextPol.BypassAnyCookie != firstPol.BypassAnyCookie || strings.Join(nextPol.BypassCookies, ",") != strings.Join(firstPol.BypassCookies, ",") {
			ambiguous = true
			break
		}
	}
	if ambiguous {
		return base, "ambiguous_multiple_routes_use_route_id", defaultRouteState, []string{"ambiguous_route_policy_use_route_id"}
	}
	if firstHasOverride {
		return firstPol, "route_override_with_global_inheritance", defaultRouteState, nil
	}
	return firstPol, defaultSource, defaultRouteState, nil
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
		effPol, _, _, extraWarnings := resolveInspectionRoutePolicy(sel, cfg, data.Policy)
		data.Policy = effPol
		data.Warnings = append(data.Warnings, extraWarnings...)
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
	_, policySource, routeCacheState, _ := resolveInspectionRoutePolicy(sel, cfg, data.Policy)
	resp := adminapi.CacheInspectResponse{ConcurrentChange: change, APIVersion: adminapi.APIVersion, Selector: fromCacheSelector(sel.RedactedSummary()), Inspection: data, ExpiresAt: adminapi.Timestamp(snap.expires), Consistency: "bounded_metadata_capture_not_global_snapshot", PolicySource: policySource, RouteCacheState: routeCacheState, AuthorityState: s.v1AuthorityState()}
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
