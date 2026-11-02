// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"encoding/json"
	"sort"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/waf"
)

// wafReport accumulates one generation's compiled WAF policies (#440).
type wafReport struct {
	out admin.WAFEffectivePolicy
}

func newWAFReport(c *config.Config) *wafReport {
	return &wafReport{out: admin.WAFEffectivePolicy{GlobalEnabled: c.WAF.Enabled}}
}

func (r *wafReport) unprotected(srv config.ServerConfig, loc config.LocationConfig) {
	r.out.UnprotectedRoutes++
	if loc.WAF != nil {
		r.out.DisabledOverrideRoutes++
		r.out.Overrides = append(r.out.Overrides, wafRouteOverride(srv, loc, nil))
	}
}

func (r *wafReport) protected(srv config.ServerConfig, loc config.LocationConfig, info waf.PolicyInfo) {
	summary := wafPolicySummary(info)
	if loc.WAF != nil {
		r.out.OverrideRoutes++
		r.out.Overrides = append(r.out.Overrides, wafRouteOverride(srv, loc, summary))
		return
	}
	r.out.InheritingRoutes++
	if r.out.Global == nil {
		r.out.Global = summary
	}
}

func (r *wafReport) finish(compiledAt time.Time) *admin.WAFEffectivePolicy {
	out := r.out
	out.CompiledAt = compiledAt.UTC().Format(time.RFC3339)
	return &out
}

func wafRouteOverride(srv config.ServerConfig, loc config.LocationConfig, policy *admin.WAFPolicySummary) admin.WAFRouteOverride {
	return admin.WAFRouteOverride{
		Listen:      srv.Listen,
		ServerNames: srv.ServerNames,
		MatchType:   loc.Match.Type,
		Path:        loc.Match.Path,
		Enabled:     loc.WAF.Enabled,
		Policy:      policy,
	}
}

func wafPolicySummary(info waf.PolicyInfo) *admin.WAFPolicySummary {
	return &admin.WAFPolicySummary{
		Mode:                   info.Mode,
		BlockStatus:            info.BlockStatus,
		CRSEnabled:             info.CRSEnabled,
		CRSVersion:             info.CRSVersion,
		Paranoia:               info.Paranoia,
		ParanoiaDefault:        info.ParanoiaDefault,
		RequestBodyLimitBytes:  info.RequestBodyLimitBytes,
		ResponseBodyInspection: info.ResponseBodyInspection,
		RuleFilesConfigured:    info.RuleFilesConfigured,
		ExternalFiles:          info.ExternalFiles,
		ExternalDigest:         info.ExternalDigest,
		PolicyDigest:           info.PolicyDigest,
		InlineRules:            info.InlineRules,
		Rules: admin.WAFRuleCounts{
			Total:     info.Rules.Total,
			Embedded:  info.Rules.Embedded,
			External:  info.Rules.External,
			Inline:    info.Rules.Inline,
			Generated: info.Rules.Generated,
		},
	}
}

// publishWAF records the WAF policy of a generation that just became live.
func (f *HandlerFactory) publishWAF(genID uint64, p *admin.WAFEffectivePolicy) {
	if p == nil {
		return
	}
	cp := *p
	cp.Generation = genID
	f.moduleMu.Lock()
	defer f.moduleMu.Unlock()
	f.liveWAF = &cp
}

func (f *HandlerFactory) publishWAFDigests(genID uint64, global string, byLoc map[[2]int]string) {
	f.moduleMu.Lock()
	defer f.moduleMu.Unlock()
	cp := make(map[[2]int]string, len(byLoc))
	for k, v := range byLoc {
		cp[k] = v
	}
	f.wafDigests = wafDigestRecord{
		genID:  genID,
		global: global,
		byLoc:  cp,
	}
}

// WAFSourceDigests returns the engine-owned external source digests of the WAF
// policies compiled for generation genID (#642, #563).
func (f *HandlerFactory) WAFSourceDigests(genID uint64) (string, map[[2]int]string) {
	f.moduleMu.Lock()
	defer f.moduleMu.Unlock()
	if f.wafDigests.genID != genID {
		return "", nil
	}
	cp := make(map[[2]int]string, len(f.wafDigests.byLoc))
	for k, v := range f.wafDigests.byLoc {
		cp[k] = v
	}
	return f.wafDigests.global, cp
}

// WAFEffective returns the serving generation's compiled WAF policy, with the
// binary's WAF capability. Before the first generation is live only the
// capability is reported.
func (f *HandlerFactory) WAFEffective() *admin.WAFEffectivePolicy {
	f.moduleMu.Lock()
	var out admin.WAFEffectivePolicy
	if f.liveWAF != nil {
		out = *f.liveWAF
		out.Overrides = append([]admin.WAFRouteOverride(nil), f.liveWAF.Overrides...)
	}
	f.moduleMu.Unlock()
	out.Compiled = waf.Compiled
	out.EmbeddedCRSVersion = waf.EmbeddedCRSVersion()
	out.EngineVersion = waf.EngineVersion()
	return &out
}

// wafInvestigationStore returns the factory's bounded WAF investigation ring
// buffer, lazily creating a default instance when nil so unit and integration
// tests share the same collection contract as production (#632).
func (f *HandlerFactory) wafInvestigationStore() *waf.InvestigationStore {
	f.moduleMu.Lock()
	defer f.moduleMu.Unlock()
	if f.WAFInvestigation == nil {
		f.WAFInvestigation = waf.NewInvestigationStore(0, 0)
	}
	return f.WAFInvestigation
}

// WAFInvestigationSnapshot returns a point-in-time snapshot of recent non-clean
// WAF transaction outcomes matching filter (#632).
func (f *HandlerFactory) WAFInvestigationSnapshot(filter waf.InvestigationFilter) waf.InvestigationSnapshot {
	return f.wafInvestigationStore().Snapshot(time.Now().UTC(), filter)
}

func computeWAFRouteOrdinals(c *config.Config) [][]int {
	if c == nil {
		return nil
	}
	type coordinate struct{ server, matchType, path string }
	counts := make(map[coordinate]int)
	ordinals := make([][]int, len(c.Servers))
	for i, srv := range c.Servers {
		names := append([]string(nil), srv.ServerNames...)
		sort.Strings(names)
		framed, _ := json.Marshal(struct {
			Listen string
			Names  []string
		}{srv.Listen, names})
		serverKey := string(framed)
		ordinals[i] = make([]int, len(srv.Locations))
		for j, loc := range srv.Locations {
			key := coordinate{serverKey, matchTypeOrDefault(loc.Match.Type), loc.Match.Path}
			ordinals[i][j] = counts[key]
			counts[key]++
		}
	}
	return ordinals
}
