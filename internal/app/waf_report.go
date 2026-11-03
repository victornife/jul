// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/waf"
)

// wafReport accumulates one generation's compiled WAF policies (#440).
type wafReport struct {
	out    admin.WAFEffectivePolicy
	counts map[wafRouteCoordinate]int
}

type wafRouteCoordinate struct {
	server, matchType, path string
}

func newWAFReport(c *config.Config) *wafReport {
	return &wafReport{
		out: admin.WAFEffectivePolicy{
			GlobalEnabled:   c.WAF.Enabled,
			RouteProtection: make(map[string]string),
		},
		counts: make(map[wafRouteCoordinate]int),
	}
}

func (r *wafReport) nextOrdinal(srv config.ServerConfig, loc config.LocationConfig) int {
	names := append([]string(nil), srv.ServerNames...)
	sort.Strings(names)
	framed, _ := json.Marshal(struct {
		Listen string
		Names  []string
	}{srv.Listen, names})
	key := wafRouteCoordinate{string(framed), matchTypeOrDefault(loc.Match.Type), loc.Match.Path}
	ord := r.counts[key]
	r.counts[key]++
	return ord
}

func wafRouteIdentityKey(srv config.ServerConfig, loc config.LocationConfig, ordinal int) string {
	if loc.RouteID != nil && strings.TrimSpace(*loc.RouteID) != "" {
		return "id:" + strings.TrimSpace(*loc.RouteID)
	}
	names := append([]string(nil), srv.ServerNames...)
	sort.Strings(names)
	framed, _ := json.Marshal(struct {
		Listen    string
		Names     []string
		MatchType string
		Path      string
		Ordinal   int
	}{srv.Listen, names, matchTypeOrDefault(loc.Match.Type), loc.Match.Path, ordinal})
	return "tuple:" + string(framed)
}

func wafOverrideIdentityKey(ov admin.WAFRouteOverride) string {
	if strings.TrimSpace(ov.RouteID) != "" {
		return "id:" + strings.TrimSpace(ov.RouteID)
	}
	names := append([]string(nil), ov.ServerNames...)
	sort.Strings(names)
	framed, _ := json.Marshal(struct {
		Listen    string
		Names     []string
		MatchType string
		Path      string
		Ordinal   int
	}{ov.Listen, names, matchTypeOrDefault(ov.MatchType), ov.Path, ov.MatchOrdinal})
	return "tuple:" + string(framed)
}

func (r *wafReport) unprotected(srv config.ServerConfig, loc config.LocationConfig) {
	ord := r.nextOrdinal(srv, loc)
	if !loc.Withdrawn {
		r.out.RouteProtection[wafRouteIdentityKey(srv, loc, ord)] = "unprotected"
	}
	r.out.UnprotectedRoutes++
	if loc.WAF != nil {
		r.out.DisabledOverrideRoutes++
		r.out.Overrides = append(r.out.Overrides, wafRouteOverride(srv, loc, ord, nil))
	}
}

func (r *wafReport) protected(srv config.ServerConfig, loc config.LocationConfig, info waf.PolicyInfo) {
	ord := r.nextOrdinal(srv, loc)
	summary := wafPolicySummary(info)
	mode := "block"
	if info.Mode == "detect" {
		mode = "detect"
		r.out.DetectRoutes++
	} else {
		r.out.BlockRoutes++
	}
	if !loc.Withdrawn {
		r.out.RouteProtection[wafRouteIdentityKey(srv, loc, ord)] = mode
	}
	if loc.WAF != nil {
		r.out.OverrideRoutes++
		r.out.Overrides = append(r.out.Overrides, wafRouteOverride(srv, loc, ord, summary))
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

func wafRouteOverride(srv config.ServerConfig, loc config.LocationConfig, ordinal int, policy *admin.WAFPolicySummary) admin.WAFRouteOverride {
	var routeID string
	if loc.RouteID != nil {
		routeID = *loc.RouteID
	}
	return admin.WAFRouteOverride{
		RouteID:      routeID,
		Listen:       srv.Listen,
		ServerNames:  srv.ServerNames,
		MatchType:    loc.Match.Type,
		Path:         loc.Match.Path,
		MatchOrdinal: ordinal,
		Enabled:      loc.WAF.Enabled,
		Policy:       policy,
	}
}

func wafPolicySummary(info waf.PolicyInfo) *admin.WAFPolicySummary {
	return &admin.WAFPolicySummary{
		Mode:                      info.Mode,
		BlockStatus:               info.BlockStatus,
		CRSEnabled:                info.CRSEnabled,
		CRSVersion:                info.CRSVersion,
		Paranoia:                  info.Paranoia,
		ParanoiaDefault:           info.ParanoiaDefault,
		BlockingParanoia:          info.BlockingParanoia,
		DetectionParanoia:         info.DetectionParanoia,
		ExceptionsCount:           info.ExceptionsCount,
		ExceptionsReviewDue:       info.ExceptionsReviewDue,
		HasCustomTuningDirectives: info.HasCustomTuningDirectives,
		RequestBodyLimitBytes:     info.RequestBodyLimitBytes,
		ResponseBodyInspection:    info.ResponseBodyInspection,
		RuleFilesConfigured:       info.RuleFilesConfigured,
		ExternalFiles:             info.ExternalFiles,
		ExternalDigest:            info.ExternalDigest,
		PolicyDigest:              info.PolicyDigest,
		InlineRules:               info.InlineRules,
		Rules: admin.WAFRuleCounts{
			Total:     info.Rules.Total,
			Embedded:  info.Rules.Embedded,
			External:  info.Rules.External,
			Inline:    info.Rules.Inline,
			Generated: info.Rules.Generated,
		},
	}
}

// publishWAF records the WAF policy of a generation that just became live and
// emits bounded protection-state and transition signals (#440, #630, #628-B).
func (f *HandlerFactory) publishWAF(genID uint64, p *admin.WAFEffectivePolicy) {
	if p == nil {
		return
	}
	cp := *p
	cp.Generation = genID
	f.moduleMu.Lock()
	prev := f.liveWAF
	f.liveWAF = &cp
	f.pruneScopedEvaluationsLocked(&cp)
	f.moduleMu.Unlock()

	transition := classifyWAFProtectionTransition(prev, &cp)
	f.Metrics.ObserveWAFServingRoutes(cp.BlockRoutes, cp.DetectRoutes, cp.DisabledOverrideRoutes, cp.UnprotectedRoutes)
	f.Metrics.ObserveWAFProtectionChange(transition)
}

func (f *HandlerFactory) pruneScopedEvaluationsLocked(p *admin.WAFEffectivePolicy) {
	if len(f.lastWAFEvalByScope) == 0 {
		f.lastWAFEval = nil
		return
	}
	next := make(map[string]*waf.WAFEvaluationReport)
	if p != nil {
		if p.Global != nil {
			if r := f.lastWAFEvalByScope["global"]; r != nil {
				next["global"] = r
			}
		}
		for _, ov := range p.Overrides {
			if !ov.Enabled || ov.Policy == nil {
				continue
			}
			ovKey := wafOverrideIdentityKey(ov)
			if r := f.lastWAFEvalByScope[ovKey]; r != nil {
				next[ovKey] = r
			} else if ov.RouteID != "" && f.lastWAFEvalByScope[ov.RouteID] != nil {
				next[ovKey] = f.lastWAFEvalByScope[ov.RouteID]
			} else if ov.Path != "" && f.lastWAFEvalByScope[ov.Path] != nil {
				next[ovKey] = f.lastWAFEvalByScope[ov.Path]
			}
		}
	}
	f.lastWAFEvalByScope = next
	if r := next["global"]; r != nil {
		f.lastWAFEval = r
	} else {
		f.lastWAFEval = nil
		for _, r := range next {
			f.lastWAFEval = r
			break
		}
	}
}

func wafProtectionRank(mode string) int {
	switch mode {
	case "block":
		return 2
	case "detect":
		return 1
	default:
		return 0
	}
}

func effectiveRouteModes(p *admin.WAFEffectivePolicy) map[string]string {
	out := make(map[string]string)
	if p == nil {
		return out
	}
	for k, v := range p.RouteProtection {
		out[k] = v
	}
	for _, ov := range p.Overrides {
		k := wafOverrideIdentityKey(ov)
		if _, exists := out[k]; exists {
			continue
		}
		if !ov.Enabled || ov.Policy == nil {
			out[k] = "unprotected"
		} else if ov.Policy.Mode == "detect" {
			out[k] = "detect"
		} else {
			out[k] = "block"
		}
	}
	return out
}

func classifyWAFProtectionTransition(prev, curr *admin.WAFEffectivePolicy) string {
	if prev == nil {
		return "initial"
	}
	prevModes := effectiveRouteModes(prev)
	currModes := effectiveRouteModes(curr)
	var anyWeakened, anyTightened bool
	for k, prevMode := range prevModes {
		currMode, ok := currModes[k]
		if !ok {
			continue
		}
		pr, cr := wafProtectionRank(prevMode), wafProtectionRank(currMode)
		if cr < pr {
			anyWeakened = true
		} else if cr > pr {
			anyTightened = true
		}
	}
	if anyWeakened || curr.BlockRoutes < prev.BlockRoutes || curr.UnprotectedRoutes > prev.UnprotectedRoutes || curr.DisabledOverrideRoutes > prev.DisabledOverrideRoutes || (prev.BlockRoutes == 0 && curr.DetectRoutes < prev.DetectRoutes) {
		return "weakened"
	}
	if anyTightened || curr.BlockRoutes > prev.BlockRoutes || (prev.BlockRoutes == 0 && curr.DetectRoutes > prev.DetectRoutes) {
		return "tightened"
	}
	prevDig := ""
	currDig := ""
	if prev.Global != nil {
		prevDig = prev.Global.PolicyDigest
	}
	if curr.Global != nil {
		currDig = curr.Global.PolicyDigest
	}
	if prevDig != currDig || len(prev.Overrides) != len(curr.Overrides) {
		return "reloaded"
	}
	for i := range curr.Overrides {
		var pd, cd string
		if prev.Overrides[i].Policy != nil {
			pd = prev.Overrides[i].Policy.PolicyDigest
		}
		if curr.Overrides[i].Policy != nil {
			cd = curr.Overrides[i].Policy.PolicyDigest
		}
		if pd != cd || prev.Overrides[i].Enabled != curr.Overrides[i].Enabled {
			return "reloaded"
		}
	}
	return "unchanged"
}

func (f *HandlerFactory) publishWAFAssets(
	policySHA string,
	assets []waf.ExternalAssetRecord,
	overridePolicySHA map[string]string,
	overrideExtSHA map[string]string,
	overrideAssets map[string][]waf.ExternalAssetRecord,
) {
	f.moduleMu.Lock()
	defer f.moduleMu.Unlock()
	f.liveGlobalPolicySHA = policySHA
	f.liveGlobalAssets = append([]waf.ExternalAssetRecord(nil), assets...)
	f.liveOverridePolicySHA = make(map[string]string, len(overridePolicySHA))
	for k, v := range overridePolicySHA {
		f.liveOverridePolicySHA[k] = v
	}
	f.liveOverrideExtSHA = make(map[string]string, len(overrideExtSHA))
	for k, v := range overrideExtSHA {
		f.liveOverrideExtSHA[k] = v
	}
	f.liveOverrideAssets = make(map[string][]waf.ExternalAssetRecord, len(overrideAssets))
	for k, v := range overrideAssets {
		f.liveOverrideAssets[k] = append([]waf.ExternalAssetRecord(nil), v...)
	}
}

// RecordWAFEvaluation stores a validated WAFEvaluationReport on the factory so
// WAFEffective() can project its live binding state (#630).
func (f *HandlerFactory) RecordWAFEvaluation(rep *waf.WAFEvaluationReport) {
	if rep == nil {
		return
	}
	scope := strings.TrimSpace(rep.Summary.Scope)
	if scope == "" {
		scope = "global"
	}
	f.RecordWAFEvaluationForScope(scope, rep)
}

// RecordWAFEvaluationForScope stores a validated WAFEvaluationReport under a
// canonical serving scope key ("global" or wafOverrideIdentityKey). When a
// serving WAF policy is active, reports for unknown, disabled, or retired
// scopes are rejected so lastWAFEvalByScope remains bounded to active policies.
func (f *HandlerFactory) RecordWAFEvaluationForScope(scopeKey string, rep *waf.WAFEvaluationReport) {
	if rep == nil {
		return
	}
	if scopeKey == "" {
		scopeKey = "global"
	}
	cp := *rep
	cp.Summary.Warnings = append([]string(nil), rep.Summary.Warnings...)
	cp.Cases = append([]waf.WAFFixtureCaseResult(nil), rep.Cases...)
	f.moduleMu.Lock()
	defer f.moduleMu.Unlock()
	if f.liveWAF != nil {
		cKey, targetSummary, amb, found := matchEffectiveWAFScopeWithConfig(f.liveConfig, f.liveWAF, scopeKey)
		if amb || !found || targetSummary == nil {
			return
		}
		scopeKey = cKey
	}
	if f.lastWAFEvalByScope == nil {
		f.lastWAFEvalByScope = make(map[string]*waf.WAFEvaluationReport)
	}
	f.lastWAFEvalByScope[scopeKey] = &cp
	if scopeKey == "global" || f.lastWAFEval == nil {
		f.lastWAFEval = &cp
	}
}

// EvaluateServingWAF evaluates the candidate or currently serving WAF policy
// against pack (defaulting to waf.DefaultFixturePack) and records the resulting
// report (#630).
func (f *HandlerFactory) EvaluateServingWAF(ctx context.Context, candidate *config.WAFConfig, pack *waf.WAFFixturePack) (*waf.WAFEvaluationReport, error) {
	rep, _, _, _, err := f.evaluateServingWAFScope(ctx, "", candidate, pack)
	if err == nil && rep != nil && candidate == nil {
		f.RecordWAFEvaluation(rep)
	}
	return rep, err
}

func (f *HandlerFactory) evaluateServingWAFScope(
	ctx context.Context,
	requestedScope string,
	candidate *config.WAFConfig,
	pack *waf.WAFFixturePack,
) (*waf.WAFEvaluationReport, string, string, string, error) {
	if !waf.Compiled {
		return nil, "", "", "", admin.ErrWAFEvaluationUnavailable
	}
	f.moduleMu.Lock()
	liveCfg := f.liveConfig
	liveWAF := f.liveWAF
	f.moduleMu.Unlock()

	scope := strings.TrimSpace(requestedScope)
	if candidate != nil {
		if !candidate.Enabled {
			return nil, "", "", "", admin.ErrWAFEvaluationUnavailable
		}
		if scope == "" {
			scope = "global"
		}
		var baseline *config.WAFConfig
		if liveCfg != nil && liveCfg.WAF.Enabled {
			b := liveCfg.WAF
			baseline = &b
		}
		rep, err := waf.EvaluatePolicy(ctx, scope, *candidate, baseline, pack, f.Versions)
		if err != nil {
			return nil, "", "", "", err
		}
		f.RecordWAFEvaluationForScope(scope, rep)
		return rep, scope, scope, "", nil
	}

	if liveCfg == nil {
		return nil, "", "", "", admin.ErrWAFEvaluationUnavailable
	}
	targetCfg, canonicalKey, displayScope, err := resolveConfigWAFScope(liveCfg, liveWAF, scope)
	if err != nil {
		return nil, "", "", "", err
	}
	var startPolicyDigest string
	if _, sum, _, ok := matchEffectiveWAFScopeWithConfig(liveCfg, liveWAF, scope); ok && sum != nil {
		startPolicyDigest = sum.PolicyDigest
	}
	rep, err := waf.EvaluatePolicy(ctx, displayScope, targetCfg, nil, pack, f.Versions)
	if err != nil {
		return nil, "", "", "", err
	}
	return rep, canonicalKey, displayScope, startPolicyDigest, nil
}

func resolveConfigWAFScope(
	cfg *config.Config,
	liveWAF *admin.WAFEffectivePolicy,
	scope string,
) (target config.WAFConfig, canonicalKey string, displayScope string, err error) {
	scope = strings.TrimSpace(scope)
	if scope == "" || scope == "global" {
		if cfg.WAF.Enabled {
			return cfg.WAF, "global", "global", nil
		}
		// On an overrides-only deployment with no explicit scope, allow default
		// evaluation only when there is a single enabled route override.
		if scope == "" {
			ordinals := computeWAFRouteOrdinals(cfg)
			var matches []struct {
				wcfg  config.WAFConfig
				key   string
				label string
			}
			for i, srv := range cfg.Servers {
				for j, loc := range srv.Locations {
					if loc.WAF != nil && loc.WAF.Enabled {
						key := wafRouteIdentityKey(srv, loc, ordinals[i][j])
						label := loc.Match.Path
						if loc.RouteID != nil && strings.TrimSpace(*loc.RouteID) != "" {
							label = strings.TrimSpace(*loc.RouteID)
						}
						matches = append(matches, struct {
							wcfg  config.WAFConfig
							key   string
							label string
						}{*loc.WAF, key, label})
					}
				}
			}
			if len(matches) == 1 {
				return matches[0].wcfg, matches[0].key, matches[0].label, nil
			}
		}
		return config.WAFConfig{}, "", "", admin.ErrWAFEvaluationUnavailable
	}

	ordinals := computeWAFRouteOrdinals(cfg)
	// 1. Explicit route_id precedence.
	for i, srv := range cfg.Servers {
		for j, loc := range srv.Locations {
			if loc.RouteID != nil && strings.TrimSpace(*loc.RouteID) == scope {
				wcfg, ok := EffectiveWAF(cfg, loc)
				if !ok {
					return config.WAFConfig{}, "", "", fmt.Errorf("waf is not enabled on route %q", scope)
				}
				key := "global"
				if loc.WAF != nil {
					key = wafRouteIdentityKey(srv, loc, ordinals[i][j])
				}
				return wcfg, key, scope, nil
			}
		}
	}
	// 2. Unambiguous path / <listen><path> / <server_name><path> / tuple key.
	type candMatch struct {
		wcfg  config.WAFConfig
		key   string
		label string
	}
	var matches []candMatch
	for i, srv := range cfg.Servers {
		for j, loc := range srv.Locations {
			ord := ordinals[i][j]
			rKey := wafRouteIdentityKey(srv, loc, ord)
			matched := rKey == scope || loc.Match.Path == scope || (srv.Listen != "" && srv.Listen+loc.Match.Path == scope)
			if !matched {
				for _, sn := range srv.ServerNames {
					if sn != "" && sn+loc.Match.Path == scope {
						matched = true
						break
					}
				}
			}
			if matched {
				wcfg, ok := EffectiveWAF(cfg, loc)
				if !ok {
					continue
				}
				key := "global"
				if loc.WAF != nil {
					key = rKey
				}
				label := scope
				if loc.RouteID != nil && strings.TrimSpace(*loc.RouteID) != "" {
					label = strings.TrimSpace(*loc.RouteID)
				}
				matches = append(matches, candMatch{wcfg: wcfg, key: key, label: label})
			}
		}
	}
	if len(matches) > 1 {
		return config.WAFConfig{}, "", "", fmt.Errorf("waf evaluation scope %q is ambiguous across %d routes; specify a unique route_id", scope, len(matches))
	}
	if len(matches) == 1 {
		return matches[0].wcfg, matches[0].key, matches[0].label, nil
	}
	_ = liveWAF
	return config.WAFConfig{}, "", "", fmt.Errorf("waf evaluation scope %q not found or not enabled", scope)
}

func matchEffectiveWAFScope(eff *admin.WAFEffectivePolicy, requestedScope string) (canonicalKey string, summary *admin.WAFPolicySummary, ambiguous bool, found bool) {
	return matchEffectiveWAFScopeWithConfig(nil, eff, requestedScope)
}

func matchEffectiveWAFScopeWithConfig(cfg *config.Config, eff *admin.WAFEffectivePolicy, requestedScope string) (canonicalKey string, summary *admin.WAFPolicySummary, ambiguous bool, found bool) {
	if eff == nil {
		return "", nil, false, false
	}
	scope := strings.TrimSpace(requestedScope)
	if cfg != nil && scope != "" && scope != "global" {
		// Check if scope is already a canonical override key.
		for _, ov := range eff.Overrides {
			if wafOverrideIdentityKey(ov) == scope {
				if !ov.Enabled || ov.Policy == nil {
					return "", nil, false, false
				}
				return scope, ov.Policy, false, true
			}
		}
		_, cKey, _, err := resolveConfigWAFScope(cfg, eff, scope)
		if err != nil {
			if strings.Contains(err.Error(), "ambiguous") {
				return "", nil, true, false
			}
			return "", nil, false, false
		}
		if cKey == "global" {
			if eff.Global != nil {
				return "global", eff.Global, false, true
			}
			return "", nil, false, false
		}
		for _, ov := range eff.Overrides {
			if wafOverrideIdentityKey(ov) == cKey && ov.Enabled && ov.Policy != nil {
				return cKey, ov.Policy, false, true
			}
		}
		return "", nil, false, false
	}
	if scope == "" || scope == "global" {
		if eff.Global != nil {
			return "global", eff.Global, false, true
		}
		if scope == "" {
			var enabled []admin.WAFRouteOverride
			for _, ov := range eff.Overrides {
				if ov.Enabled && ov.Policy != nil {
					enabled = append(enabled, ov)
				}
			}
			if len(enabled) == 1 {
				return wafOverrideIdentityKey(enabled[0]), enabled[0].Policy, false, true
			}
		}
		return "global", nil, false, false
	}
	// 1. Durable RouteID match on Overrides.
	for _, ov := range eff.Overrides {
		if strings.TrimSpace(ov.RouteID) == scope {
			if !ov.Enabled || ov.Policy == nil {
				return "", nil, false, false
			}
			return wafOverrideIdentityKey(ov), ov.Policy, false, true
		}
	}
	// 2. Unambiguous coordinate/path match on Overrides.
	var matches []admin.WAFRouteOverride
	for _, ov := range eff.Overrides {
		ovKey := wafOverrideIdentityKey(ov)
		matched := ovKey == scope || ov.Path == scope || (ov.Listen != "" && ov.Listen+ov.Path == scope)
		if !matched {
			for _, sn := range ov.ServerNames {
				if sn != "" && sn+ov.Path == scope {
					matched = true
					break
				}
			}
		}
		if matched {
			matches = append(matches, ov)
		}
	}
	if len(matches) > 1 {
		return "", nil, true, false
	}
	if len(matches) == 1 && matches[0].Enabled && matches[0].Policy != nil {
		return wafOverrideIdentityKey(matches[0]), matches[0].Policy, false, true
	}
	return "", nil, false, false
}

func (f *HandlerFactory) commitAndRebindScopedEvaluation(
	canonicalKey string,
	requestedScope string,
	displayScope string,
	startPolicyDigest string,
	isSuppliedReport bool,
	rep *waf.WAFEvaluationReport,
) *admin.WAFEvaluationSummaryView {
	f.moduleMu.Lock()
	hook := f.onBeforeEvalCommit
	f.moduleMu.Unlock()
	if hook != nil {
		hook(canonicalKey)
	}

	f.moduleMu.Lock()
	liveCfg := f.liveConfig
	f.moduleMu.Unlock()
	eff := f.WAFEffective()

	staleView := func(reason string) *admin.WAFEvaluationSummaryView {
		unbound := rep.Summary
		if displayScope != "" {
			unbound.Scope = displayScope
		}
		unbound.State = "stale_policy_changed"
		unbound.Warnings = append(append([]string(nil), unbound.Warnings...), reason)
		return toAdminEvaluationSummaryView(unbound)
	}

	if eff == nil || (eff.Global == nil && eff.OverrideRoutes == 0) {
		return staleView("serving WAF policy was disabled or removed during evaluation")
	}

	lookupScope := strings.TrimSpace(requestedScope)
	if lookupScope == "" {
		lookupScope = displayScope
	}
	curKey, targetSummary, ambiguous, found := matchEffectiveWAFScopeWithConfig(liveCfg, eff, lookupScope)
	if ambiguous || !found || targetSummary == nil || curKey != canonicalKey {
		return staleView(fmt.Sprintf("target WAF scope %q is no longer active or was reassigned during evaluation", displayScope))
	}

	if !isSuppliedReport && startPolicyDigest != "" && targetSummary.PolicyDigest != startPolicyDigest {
		return staleView(fmt.Sprintf("target WAF scope %q policy changed during evaluation", displayScope))
	}

	curInfo := waf.PolicyInfo{
		Mode:           targetSummary.Mode,
		CRSEnabled:     targetSummary.CRSEnabled,
		CRSVersion:     targetSummary.CRSVersion,
		PolicyDigest:   targetSummary.PolicyDigest,
		ExternalDigest: targetSummary.ExternalDigest,
	}
	bound := waf.VerifyEvaluationBinding(&rep.Summary, "", "", curInfo, "", "")
	if !isSuppliedReport || bound.State == "current" || bound.State == "failed" {
		f.RecordWAFEvaluationForScope(canonicalKey, rep)
		effAfter := f.WAFEffective()
		if v := findScopedEvaluationView(effAfter, canonicalKey); v != nil {
			cp := *v
			if displayScope != "" {
				cp.Scope = displayScope
			}
			return &cp
		}
		return staleView(fmt.Sprintf("target WAF scope %q was retired before evaluation could bind", displayScope))
	}
	if displayScope != "" {
		bound.Scope = displayScope
	}
	return toAdminEvaluationSummaryView(bound)
}

// HandleAdminWAFEvaluate handles POST /api/security/waf/evaluate requests from
// the Console or API (#630). If the request body carries a valid supplied
// WAFEvaluationReport, it is verified against the serving policy and marked
// "supplied_report"; otherwise a fresh server-observed evaluation is run.
func (f *HandlerFactory) HandleAdminWAFEvaluate(ctx context.Context, body []byte) (*admin.WAFEvaluationSummaryView, error) {
	if !waf.Compiled {
		return nil, admin.ErrWAFEvaluationUnavailable
	}
	trimmed := strings.TrimSpace(string(body))
	var requestedScope string
	if trimmed != "" && trimmed != "{}" {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
			return nil, fmt.Errorf("waf evaluate: decode JSON request: %w", err)
		}
		if rawScope, ok := probe["scope"]; ok {
			if err := json.Unmarshal(rawScope, &requestedScope); err != nil {
				return nil, fmt.Errorf("waf evaluate: invalid scope: %w", err)
			}
		}
		// 1. If the payload looks like a WAFEvaluationReport (contains "summary" or "cases"),
		// validate it strictly as an evaluation report and never fall through to fixture-pack parsing.
		if _, hasSummary := probe["summary"]; hasSummary || probe["cases"] != nil {
			delete(probe, "scope")
			repBytes, _ := json.Marshal(probe)
			rep, err := waf.ParseEvaluationReport(repBytes)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(rep.Summary.Scope) == "" && requestedScope != "" {
				rep.Summary.Scope = requestedScope
			}
			// Strip private unkeyed raw digests on admission (#649); external
			// reports bind via context-keyed ev1: digests only.
			rep.RawPolicySHA256 = ""
			rep.RawExternalSHA256 = ""
			rep.Summary.Source = "supplied_report"

			f.moduleMu.Lock()
			liveCfg := f.liveConfig
			f.moduleMu.Unlock()
			eff := f.WAFEffective()
			if eff.Global == nil && eff.OverrideRoutes == 0 {
				return nil, admin.ErrWAFEvaluationUnavailable
			}
			canonicalKey, targetSummary, ambiguous, found := matchEffectiveWAFScopeWithConfig(liveCfg, eff, rep.Summary.Scope)
			if ambiguous {
				return nil, fmt.Errorf("waf evaluation report scope %q is ambiguous across multiple routes", rep.Summary.Scope)
			}
			if !found || targetSummary == nil {
				// Scope is not active on this node (e.g., a "global" report imported
				// onto an overrides-only node, or an unknown route_id). Return a
				// truthful stale_policy_changed view without binding it as current.
				unbound := rep.Summary
				unbound.State = "stale_policy_changed"
				unbound.Warnings = append(append([]string(nil), unbound.Warnings...),
					fmt.Sprintf("supplied evaluation scope %q does not match an active serving WAF policy on this node", rep.Summary.Scope))
				return toAdminEvaluationSummaryView(unbound), nil
			}
			displayScope := strings.TrimSpace(rep.Summary.Scope)
			if displayScope == "" {
				displayScope = "global"
			}
			return f.commitAndRebindScopedEvaluation(canonicalKey, rep.Summary.Scope, displayScope, targetSummary.PolicyDigest, true, rep), nil
		}

		// 2. Check for {"scope": "..."} wrapper (with optional embedded "fixtures").
		if _, ok := probe["scope"]; ok && probe["fixtures"] == nil {
			// Scope-only request; proceed to default fixture pack below.
		} else {
			delete(probe, "scope")
			packBytes, _ := json.Marshal(probe)
			pack, err := waf.ParseFixturePack(packBytes)
			if err != nil {
				return nil, err
			}
			rep, canonicalKey, displayScope, startPolicyDigest, err := f.evaluateServingWAFScope(ctx, requestedScope, nil, pack)
			if err != nil {
				return nil, err
			}
			return f.commitAndRebindScopedEvaluation(canonicalKey, requestedScope, displayScope, startPolicyDigest, false, rep), nil
		}
	}

	rep, canonicalKey, displayScope, startPolicyDigest, err := f.evaluateServingWAFScope(ctx, requestedScope, nil, nil)
	if err != nil {
		return nil, err
	}
	return f.commitAndRebindScopedEvaluation(canonicalKey, requestedScope, displayScope, startPolicyDigest, false, rep), nil
}

func findScopedEvaluationView(eff *admin.WAFEffectivePolicy, canonicalKey string) *admin.WAFEvaluationSummaryView {
	if eff == nil {
		return nil
	}
	if canonicalKey == "" || canonicalKey == "global" {
		if eff.Global != nil && eff.Evaluation != nil {
			return eff.Evaluation
		}
		return nil
	}
	for _, ov := range eff.Overrides {
		if ov.Enabled && ov.Policy != nil && wafOverrideIdentityKey(ov) == canonicalKey && ov.Evaluation != nil {
			return ov.Evaluation
		}
	}
	return nil
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
	var evalRep *waf.WAFEvaluationReport
	evalByScope := make(map[string]*waf.WAFEvaluationReport, len(f.lastWAFEvalByScope))
	for k, v := range f.lastWAFEvalByScope {
		if v != nil {
			cp := *v
			evalByScope[k] = &cp
		}
	}
	var globalPolicySHA string
	var globalExtSHA string
	var globalAssets []waf.ExternalAssetRecord
	overridePolicySHA := make(map[string]string, len(f.liveOverridePolicySHA))
	for k, v := range f.liveOverridePolicySHA {
		overridePolicySHA[k] = v
	}
	overrideExtSHA := make(map[string]string, len(f.liveOverrideExtSHA))
	for k, v := range f.liveOverrideExtSHA {
		overrideExtSHA[k] = v
	}
	overrideAssets := make(map[string][]waf.ExternalAssetRecord, len(f.liveOverrideAssets))
	var allOverrideAssets []waf.ExternalAssetRecord
	for k, v := range f.liveOverrideAssets {
		overrideAssets[k] = append([]waf.ExternalAssetRecord(nil), v...)
		allOverrideAssets = append(allOverrideAssets, v...)
	}
	if f.liveWAF != nil {
		out = *f.liveWAF
		out.Overrides = append([]admin.WAFRouteOverride(nil), f.liveWAF.Overrides...)
	}
	if f.lastWAFEval != nil {
		cp := *f.lastWAFEval
		evalRep = &cp
	}
	globalPolicySHA = f.liveGlobalPolicySHA
	globalExtSHA = f.wafDigests.global
	globalAssets = append([]waf.ExternalAssetRecord(nil), f.liveGlobalAssets...)
	f.moduleMu.Unlock()
	out.Compiled = waf.Compiled
	out.EmbeddedCRSVersion = waf.EmbeddedCRSVersion()
	out.EngineVersion = waf.EngineVersion()

	allAssets := append(append([]waf.ExternalAssetRecord(nil), globalAssets...), allOverrideAssets...)
	assetsMatch := len(allAssets) == 0 || diskAssetsMatchRecorded(allAssets)

	bindSummary := func(
		rep *waf.WAFEvaluationReport,
		pol *admin.WAFPolicySummary,
		polSHA string,
		extSHA string,
		assets []waf.ExternalAssetRecord,
	) *admin.WAFEvaluationSummaryView {
		curInfo := waf.PolicyInfo{
			Mode:           pol.Mode,
			CRSEnabled:     pol.CRSEnabled,
			CRSVersion:     pol.CRSVersion,
			PolicyDigest:   pol.PolicyDigest,
			ExternalDigest: pol.ExternalDigest,
		}
		var baseSummary *waf.WAFEvaluationSummary
		var repPolSHA, repExtSHA string
		if rep != nil {
			baseSummary = &rep.Summary
			repPolSHA = rep.RawPolicySHA256
			repExtSHA = rep.RawExternalSHA256
		}
		bound := waf.VerifyEvaluationBinding(baseSummary, repPolSHA, repExtSHA, curInfo, polSHA, extSHA)
		if (bound.State == "current" || bound.State == "failed" || bound.State == "unrun" || bound.State == "stale_policy_changed") && len(assets) > 0 && !diskAssetsMatchRecorded(assets) {
			bound.State = "stale_asset_changed"
		}
		return toAdminEvaluationSummaryView(bound)
	}

	if out.Global != nil {
		gRep := evalByScope["global"]
		if gRep == nil && evalRep != nil && (evalRep.Summary.Scope == "" || evalRep.Summary.Scope == "global") {
			gRep = evalRep
		}
		out.Evaluation = bindSummary(gRep, out.Global, globalPolicySHA, globalExtSHA, globalAssets)
	}

	var overrideViews []*admin.WAFEvaluationSummaryView
	for i := range out.Overrides {
		ov := &out.Overrides[i]
		if !ov.Enabled || ov.Policy == nil {
			continue
		}
		ovKey := wafOverrideIdentityKey(*ov)
		ovRep := evalByScope[ovKey]
		if ovRep == nil && ov.RouteID != "" {
			ovRep = evalByScope[ov.RouteID]
		}
		if ovRep == nil && ov.Path != "" {
			ovRep = evalByScope[ov.Path]
		}
		ov.Evaluation = bindSummary(ovRep, ov.Policy, overridePolicySHA[ovKey], overrideExtSHA[ovKey], overrideAssets[ovKey])
		overrideViews = append(overrideViews, ov.Evaluation)
	}

	if out.Global == nil && len(overrideViews) > 0 {
		best := overrideViews[0]
		for _, v := range overrideViews[1:] {
			if evalStatePriority(v.State) > evalStatePriority(best.State) {
				best = v
			}
		}
		cp := *best
		if !assetsMatch && (cp.State == "current" || cp.State == "failed" || cp.State == "unrun") {
			cp.State = "stale_asset_changed"
		}
		out.Evaluation = &cp
	}
	return &out
}

func evalStatePriority(state string) int {
	switch state {
	case "failed":
		return 6
	case "stale_asset_changed":
		return 5
	case "stale_policy_changed":
		return 4
	case "stale_engine_or_crs_changed":
		return 3
	case "current":
		return 2
	case "unrun":
		return 1
	default:
		return 0
	}
}

func diskAssetsMatchRecorded(assets []waf.ExternalAssetRecord) bool {
	for _, a := range assets {
		f, err := os.Open(a.Path)
		if err != nil {
			return false
		}
		data, err := io.ReadAll(io.LimitReader(f, waf.MaxExternalRuleFileBytes+1))
		_ = f.Close()
		if err != nil || int64(len(data)) != a.SizeBytes {
			return false
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != a.SHA256 {
			return false
		}
	}
	return true
}

func toAdminEvaluationSummaryView(s waf.WAFEvaluationSummary) *admin.WAFEvaluationSummaryView {
	return &admin.WAFEvaluationSummaryView{
		State:                s.State,
		Source:               s.Source,
		EvaluatedAt:          s.EvaluatedAt,
		PackID:               s.PackID,
		Scope:                s.Scope,
		PolicyDigest:         s.PolicyDigest,
		ExternalDigest:       s.ExternalDigest,
		EngineVersion:        s.EngineVersion,
		EmbeddedCRSVersion:   s.EmbeddedCRSVersion,
		AnalysisCompleteness: s.AnalysisCompleteness,
		Warnings:             append([]string(nil), s.Warnings...),
		TotalCases:           s.TotalCases,
		PassedCases:          s.PassedCases,
		FailedCases:          s.FailedCases,
		LegitimateTotal:      s.LegitimateTotal,
		LegitimateBlocked:    s.LegitimateBlocked,
		MaliciousTotal:       s.MaliciousTotal,
		MaliciousAllowed:     s.MaliciousAllowed,
		ChangedFromBaseline:  s.ChangedFromBaseline,
		CompileDurationMs:    s.CompileDurationMs,
		EvalDurationMs:       s.EvalDurationMs,
	}
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
