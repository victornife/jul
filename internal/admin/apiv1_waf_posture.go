// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/rbac"
	"jul/internal/redact"
	"jul/internal/versionid"
	"jul/internal/waf"
)

func v1WAFEventParameters() []ExternalParameter {
	return []ExternalParameter{
		{Name: "event_id", In: "query", Type: "string", Description: "Filter by exact server-owned event_id (wev_...)."},
		{Name: "request_id", In: "query", Type: "string", Description: "Filter by untrusted client X-Request-ID hint."},
		{Name: "trace_id", In: "query", Type: "string", Description: "Filter by untrusted trace correlation hint."},
		{Name: "route_id", In: "query", Type: "string", Description: "Filter by durable route_id."},
		{Name: "path", In: "query", Type: "string", Description: "Filter by sanitized path prefix or exact match."},
		{Name: "outcome", In: "query", Type: "string", Description: "Filter by transaction outcome (matched_observed, interrupted_request, interrupted_response, rejected_body_limit, inspection_error)."},
		{Name: "rule_id", In: "query", Type: "integer", Description: "Filter by matched or interrupting rule_id."},
		{Name: "limit", In: "query", Type: "integer", Description: "Maximum number of events to return (1..256, default 50)."},
	}
}

// handleV1WAFEvents serves GET /api/v1/waf/events (#632-B).
func (s *Server) handleV1WAFEvents(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	q := r.URL.Query()
	filter := waf.InvestigationFilter{
		EventID:    strings.TrimSpace(q.Get("event_id")),
		RequestID:  strings.TrimSpace(q.Get("request_id")),
		TraceID:    strings.TrimSpace(q.Get("trace_id")),
		RouteID:    strings.TrimSpace(q.Get("route_id")),
		PathPrefix: strings.TrimSpace(q.Get("path")),
		Outcome:    strings.TrimSpace(q.Get("outcome")),
		Limit:      50,
	}
	if filter.Outcome != "" {
		switch filter.Outcome {
		case waf.OutcomeClean, waf.OutcomeMatchedObserved, waf.OutcomeInterruptedRequest,
			waf.OutcomeInterruptedResponse, waf.OutcomeRejectedBodyLimit, waf.OutcomeInspectionError:
		default:
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest,
				"Unsupported WAF transaction outcome %q.", filter.Outcome))
			return
		}
	}
	if rawRule := strings.TrimSpace(q.Get("rule_id")); rawRule != "" {
		n, err := strconv.Atoi(rawRule)
		if err != nil || n <= 0 {
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest,
				"Query parameter rule_id must be a positive integer."))
			return
		}
		filter.RuleID = n
	}
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n <= 0 || n > waf.MaxInvestigationEvents {
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest,
				"Query parameter limit must be an integer between 1 and %d.", waf.MaxInvestigationEvents))
			return
		}
		filter.Limit = n
	}

	var eff *WAFEffectivePolicy
	var servingGen uint64
	var servingDigest string
	if s.deps.WAFEffective != nil {
		eff = s.deps.WAFEffective()
		if eff != nil {
			servingGen = eff.Generation
			if eff.Global != nil {
				servingDigest = eff.Global.PolicyDigest
			} else if len(eff.Overrides) == 1 && eff.Overrides[0].Policy != nil {
				servingDigest = eff.Overrides[0].Policy.PolicyDigest
			}
		}
	}

	out := adminapi.WAFEventsResponse{
		APIVersion:          adminapi.APIVersion,
		CollectionState:     "unavailable",
		ServingGeneration:   servingGen,
		ServingPolicyDigest: servingDigest,
		Retention: adminapi.WAFRetentionSpec{
			MaxEvents:     waf.MaxInvestigationEvents,
			MaxEventBytes: waf.MaxEventBytes,
			TTLSeconds:    int(waf.InvestigationEventTTL.Seconds()),
		},
		Events: []adminapi.WAFEventRecord{},
	}
	if !s.deps.WAFCompiled {
		writeAPIJSON(w, http.StatusOK, out)
		return
	}
	if s.deps.WAFInvestigation == nil {
		out.CollectionState = "disabled"
		writeAPIJSON(w, http.StatusOK, out)
		return
	}

	snap := s.deps.WAFInvestigation(filter)
	if snap.Enabled {
		out.CollectionState = "active"
	} else {
		out.CollectionState = "disabled"
	}
	out.Retention = adminapi.WAFRetentionSpec{
		MaxEvents:     snap.Capacity,
		MaxEventBytes: snap.MaxEventBytes,
		TTLSeconds:    snap.TTLSeconds,
	}
	out.Stats = adminapi.WAFCollectionStats{
		RetainedEvents: snap.Retained,
		RecordedTotal:  snap.RecordedTotal,
		TruncatedTotal: snap.TruncatedTotal,
		DroppedTotal:   snap.DroppedTotal,
	}

	canReadConfig := true
	if id, ok := rbac.IdentityFromContext(r.Context()); ok && !id.Has(rbac.ConfigRead) {
		canReadConfig = false
	}

	out.Events = make([]adminapi.WAFEventRecord, 0, len(snap.Events))
	for _, ev := range snap.Events {
		rules := make([]adminapi.WAFMatchedRuleRecord, 0, len(ev.MatchedRules))
		for _, mr := range ev.MatchedRules {
			rules = append(rules, adminapi.WAFMatchedRuleRecord{
				RuleID:         mr.RuleID,
				Phase:          mr.Phase,
				Severity:       mr.Severity,
				SourceClass:    mr.SourceClass,
				Disruptive:     mr.Disruptive,
				Logged:         mr.Logged,
				Tags:           append([]string(nil), mr.Tags...),
				MatchedTargets: append([]string(nil), mr.MatchedTargets...),
			})
		}
		expectedDigest := servingPolicyDigestForEvent(eff, ev)
		stale := (servingGen > 0 && ev.Generation > 0 && ev.Generation != servingGen) ||
			(expectedDigest != "" && ev.PolicyDigest != "" && ev.PolicyDigest != expectedDigest)
		canonPred := ev.Route.CanonicalPredicates
		if canonPred != "" {
			if _, ok := versionid.Parse(canonPred); !ok {
				canonPred = s.deps.EffectiveVersions.Digest("waf-route-predicates-v1", []byte(canonPred))
			}
		}
		routeRef := adminapi.WAFRouteRef{
			RouteID:             ev.Route.RouteID,
			Listen:              ev.Route.Listen,
			ServerNames:         append([]string(nil), ev.Route.ServerNames...),
			MatchType:           ev.Route.MatchType,
			PathPattern:         ev.Route.PathPattern,
			MatchOrdinal:        ev.Route.MatchOrdinal,
			Scope:               ev.Route.Scope,
			Methods:             append([]string(nil), ev.Route.Methods...),
			Predicates:          redact.Apply(ev.Route.Predicates),
			CanonicalPredicates: canonPred,
		}
		if !canReadConfig {
			// Observability-only callers without config:read still get durable
			// route_id and scope without raw route match patterns.
			routeRef.PathPattern = ""
			routeRef.ServerNames = nil
			routeRef.Methods = nil
			routeRef.Predicates = ""
			routeRef.CanonicalPredicates = ""
		}
		out.Events = append(out.Events, adminapi.WAFEventRecord{
			EventID:                  ev.EventID,
			Time:                     adminapi.Timestamp(ev.Time),
			Outcome:                  ev.Outcome,
			Mode:                     ev.Mode,
			WouldInterrupt:           ev.WouldInterrupt,
			InterruptedAction:        ev.InterruptedAction,
			InterruptedRuleID:        ev.InterruptedRuleID,
			ClientStatus:             ev.ClientStatus,
			OriginReached:            ev.OriginReached,
			OriginStatus:             ev.OriginStatus,
			PartialResponseDelivered: ev.PartialResponseDelivered,
			Hijacked:                 ev.Hijacked,
			PhasesEvaluated:          append([]string(nil), ev.PhasesEvaluated...),
			Method:                   ev.Method,
			Host:                     ev.Host,
			Path:                     ev.Path,
			QueryOmitted:             ev.QueryOmitted,
			Route:                    routeRef,
			Generation:               ev.Generation,
			PolicyDigest:             ev.PolicyDigest,
			ExternalDigest:           ev.ExternalDigest,
			CRSVersion:               ev.CRSVersion,
			Paranoia:                 ev.Paranoia,
			StalePolicy:              stale,
			MatchedRules:             rules,
			TotalMatchedRules:        ev.TotalMatchedRules,
			MatchesTruncated:         ev.MatchesTruncated,
			EventTruncated:           ev.EventTruncated,
			SupportHeaderEmitted:     ev.SupportHeaderEmitted,
			RequestIDHint:            ev.RequestIDHint,
			TraceIDHint:              ev.TraceIDHint,
			ErrorCategory:            ev.ErrorCategory,
		})
	}
	writeAPIJSON(w, http.StatusOK, out)
}

func canonicalPostureNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		t := strings.ToLower(strings.TrimSpace(n))
		if t != "" {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func slicesEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func servingPolicyDigestForEvent(eff *WAFEffectivePolicy, ev waf.TransactionEvent) string {
	if eff == nil {
		return ""
	}
	if ev.Route.Scope == "override" {
		if rid := strings.TrimSpace(ev.Route.RouteID); rid != "" {
			for _, ov := range eff.Overrides {
				if strings.TrimSpace(ov.RouteID) == rid {
					if !ov.Enabled || ov.Policy == nil {
						return "__disabled__"
					}
					return ov.Policy.PolicyDigest
				}
			}
		}
		evNames := canonicalPostureNames(ev.Route.ServerNames)
		evMT := strings.TrimSpace(ev.Route.MatchType)
		if evMT == "" {
			evMT = "prefix"
		}
		for _, ov := range eff.Overrides {
			ovMT := strings.TrimSpace(ov.MatchType)
			if ovMT == "" {
				ovMT = "prefix"
			}
			if ov.Listen == ev.Route.Listen &&
				ovMT == evMT &&
				ov.Path == ev.Route.PathPattern &&
				ov.MatchOrdinal == ev.Route.MatchOrdinal &&
				slicesEqualStrings(canonicalPostureNames(ov.ServerNames), evNames) {
				if !ov.Enabled || ov.Policy == nil {
					return "__disabled__"
				}
				return ov.Policy.PolicyDigest
			}
		}
		// Fallback when one side omits server_names in a single-vhost fixture.
		var matches []WAFRouteOverride
		for _, ov := range eff.Overrides {
			ovMT := strings.TrimSpace(ov.MatchType)
			if ovMT == "" {
				ovMT = "prefix"
			}
			if ov.Listen == ev.Route.Listen &&
				ovMT == evMT &&
				ov.Path == ev.Route.PathPattern &&
				ov.MatchOrdinal == ev.Route.MatchOrdinal {
				matches = append(matches, ov)
			}
		}
		if len(matches) == 1 {
			if !matches[0].Enabled || matches[0].Policy == nil {
				return "__disabled__"
			}
			return matches[0].Policy.PolicyDigest
		}
		return "__missing_override__"
	}
	if eff.Global != nil {
		return eff.Global.PolicyDigest
	}
	if len(eff.Overrides) > 0 {
		return "__missing_global__"
	}
	return ""
}

// handleV1SecurityPosture serves GET /api/v1/security/posture (#633).
func (s *Server) handleV1SecurityPosture(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	includeConfigDetails := true
	if id, ok := rbac.IdentityFromContext(r.Context()); ok && !id.Has(rbac.ConfigRead) {
		includeConfigDetails = false
	}
	resp, apiErr := s.securityPostureSnapshot(includeConfigDetails)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

// RefreshSecurityPostureMetrics uses the same read-only evaluator as the API.
// The metrics pre-gather hook calls it so time-dependent findings and persisted
// drift update without an operator opening the Console. No provider resolution,
// network probing, or configuration mutation is performed.
func (s *Server) RefreshSecurityPostureMetrics() {
	_, _ = s.securityPostureSnapshot(false)
}

func (s *Server) securityPostureSnapshot(includeConfigDetails bool) (adminapi.SecurityPostureResponse, *adminapi.Error) {
	// Serialize scrapes and interactive evaluations so an older observation
	// cannot overwrite a newer observation's metrics after it completes.
	s.postureMu.Lock()
	defer s.postureMu.Unlock()
	state, apiErr := s.v1ConfigState()
	var persistedCfg *config.Config
	var baseVersion string
	if apiErr == nil {
		persistedCfg = state.Config
		baseVersion = state.Version
	}

	var rawServingCfg, effServingCfg *config.Config
	if s.deps.LiveSnapshot != nil {
		snap := s.deps.LiveSnapshot()
		if snap.EffectiveConfig != nil {
			effServingCfg = snap.EffectiveConfig
			rawServingCfg = snap.RawConfig
			if rawServingCfg == nil {
				rawServingCfg = effServingCfg
			}
		}
	}
	if effServingCfg == nil && s.deps.LoadConfig != nil {
		if liveCfg, err := s.deps.LoadConfig(); err == nil && liveCfg != nil {
			effServingCfg = liveCfg
			rawServingCfg = liveCfg
		}
	}
	if effServingCfg == nil {
		if apiErr != nil {
			if s.deps.ObserveSecurityEvidenceState != nil {
				s.deps.ObserveSecurityEvidenceState("posture_evaluator", "unavailable")
			}
			return adminapi.SecurityPostureResponse{}, apiErr
		}
		effServingCfg = persistedCfg
		rawServingCfg = persistedCfg
	}

	resp := s.evaluateSecurityPostureWithPersisted(rawServingCfg, effServingCfg, persistedCfg, baseVersion, includeConfigDetails)
	if apiErr != nil && s.deps.ObserveSecurityEvidenceState != nil {
		// The serving generation remains inspectable, but persisted drift is
		// unknown. Do not advertise complete fresh evidence after a read failure.
		s.deps.ObserveSecurityEvidenceState("posture_evaluator", "stale")
	}
	return resp, nil
}

// EvaluateSecurityPostureOffline evaluates the 7-area gateway security posture
// for a standalone configuration (used by `jul posture` and `jul doctor`;
// read-only and network-free by default, #633).
func EvaluateSecurityPostureOffline(rawCfg, effCfg *config.Config, wafCompiled bool) adminapi.SecurityPostureResponse {
	if effCfg == nil {
		effCfg = rawCfg
	}
	if rawCfg == nil {
		rawCfg = effCfg
	}
	s := &Server{
		deps: Deps{
			WAFCompiled: wafCompiled,
		},
	}
	return s.evaluateSecurityPostureWithPersisted(rawCfg, effCfg, nil, "", true)
}

var postureAreaOrder = []string{
	"admin",
	"trust",
	"waf",
	"cache",
	"certificates",
	"intent",
	"maintenance",
}

func (s *Server) evaluateSecurityPostureWithPersisted(rawCfg, effCfg, persistedCfg *config.Config, baseVersion string, includeConfigDetails bool) adminapi.SecurityPostureResponse {
	now := time.Now().UTC()
	var findings []adminapi.SecurityPostureFinding

	var servingGen uint64
	var servingPolicyDigest string
	var wafEff *WAFEffectivePolicy
	if s.deps.WAFEffective != nil {
		wafEff = s.deps.WAFEffective()
		if wafEff != nil {
			servingGen = wafEff.Generation
			if wafEff.Global != nil {
				servingPolicyDigest = wafEff.Global.PolicyDigest
			}
		}
	}

	addFinding := func(f adminapi.SecurityPostureFinding) {
		f.Scope = sanitizePostureToken(f.Scope)
		f.Summary = sanitizePostureToken(f.Summary)
		f.Remediation = sanitizePostureToken(f.Remediation)
		if !includeConfigDetails {
			f.Scope = f.Area
			f.Summary = redactPostureSummaryForStatusRead(f.Code, f.Area, f.Severity)
		}
		if f.ServingGeneration == 0 && servingGen > 0 {
			f.ServingGeneration = servingGen
		}
		if f.PolicyDigest == "" && servingPolicyDigest != "" && f.Area == "waf" {
			f.PolicyDigest = servingPolicyDigest
		}
		findings = append(findings, f)
	}

	rawListen := func(srvIdx int, fallback string) string {
		if rawCfg != nil && srvIdx >= 0 && srvIdx < len(rawCfg.Servers) && strings.TrimSpace(rawCfg.Servers[srvIdx].Listen) != "" {
			return rawCfg.Servers[srvIdx].Listen
		}
		return fallback
	}
	rawPath := func(srvIdx, locIdx int, fallback string) string {
		if rawCfg != nil && srvIdx >= 0 && srvIdx < len(rawCfg.Servers) && locIdx >= 0 && locIdx < len(rawCfg.Servers[srvIdx].Locations) && rawCfg.Servers[srvIdx].Locations[locIdx].Match.Path != "" {
			return rawCfg.Servers[srvIdx].Locations[locIdx].Match.Path
		}
		return fallback
	}
	rawServerName := func(srvIdx, snIdx int, fallback string) string {
		if rawCfg != nil && srvIdx >= 0 && srvIdx < len(rawCfg.Servers) && snIdx >= 0 && snIdx < len(rawCfg.Servers[srvIdx].ServerNames) && strings.TrimSpace(rawCfg.Servers[srvIdx].ServerNames[snIdx]) != "" {
			return rawCfg.Servers[srvIdx].ServerNames[snIdx]
		}
		return fallback
	}
	rawExternalEnforcer := func(srvIdx, locIdx int, fallback string) string {
		if rawCfg != nil && srvIdx >= 0 && srvIdx < len(rawCfg.Servers) && locIdx >= 0 && locIdx < len(rawCfg.Servers[srvIdx].Locations) {
			if sh := rawCfg.Servers[srvIdx].Locations[locIdx].Sharing; sh != nil && sh.ExternalEnforcer != nil && strings.TrimSpace(*sh.ExternalEnforcer) != "" {
				return strings.TrimSpace(*sh.ExternalEnforcer)
			}
		}
		return fallback
	}

	// ── Area 1: admin ────────────────────────────────────────────────────────
	if effCfg != nil {
		if effCfg.Admin.Enabled && !config.IsLoopbackListen(effCfg.Admin.Listen) {
			if effCfg.Admin.TLS == nil || !effCfg.Admin.TLS.Enabled {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "admin_off_loopback_no_tls",
					Area:           "admin",
					Severity:       "error",
					EpistemicState: "configured",
					Scope:          "admin.listen",
					Summary:        "Admin listener is bound off-loopback without TLS.",
					Remediation:    "Configure admin.tls or bind admin.listen to 127.0.0.1.",
				})
			}
			if strings.TrimSpace(effCfg.Admin.Token) == "" && !effCfg.Admin.RBAC.Enabled {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "admin_off_loopback_unauthenticated",
					Area:           "admin",
					Severity:       "error",
					EpistemicState: "configured",
					Scope:          "admin",
					Summary:        "Admin listener is bound off-loopback without a bearer token or RBAC.",
					Remediation:    "Enable admin.rbac with named principals or set admin.token.",
				})
			}
		}
		if effCfg.Admin.RBAC.Enabled && strings.TrimSpace(effCfg.Admin.Token) != "" {
			addFinding(adminapi.SecurityPostureFinding{
				Code:           "admin_legacy_token_active_with_rbac",
				Area:           "admin",
				Severity:       "warning",
				EpistemicState: "configured",
				Scope:          "admin.token",
				Summary:        "Legacy shared admin.token remains active alongside named-principal RBAC.",
				Remediation:    "Remove admin.token once all automation uses named RBAC principals.",
			})
		}
		if s.audit != nil {
			if st := s.audit.statusReport(); st != nil && !st.Healthy {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "admin_audit_sink_degraded",
					Area:           "admin",
					Severity:       "warning",
					EpistemicState: "observed",
					Scope:          "admin.audit_log",
					Summary:        "Admin audit log sink reported a write degradation.",
					Remediation:    "Check disk headroom and permissions for the configured audit log path.",
				})
			}
		}
	}

	// ── Area 2: trust ────────────────────────────────────────────────────────
	if effCfg != nil {
		sp := projectSecurity(rawCfg, s.deps.WAFCompiled)
		if sp.BackendTLSInsecure > 0 {
			addFinding(adminapi.SecurityPostureFinding{
				Code:           "trust_backend_tls_insecure_skip_verify",
				Area:           "trust",
				Severity:       "warning",
				EpistemicState: "configured",
				Scope:          "backend_tls",
				Summary:        fmt.Sprintf("%d backend_tls policy block(s) disable upstream certificate verification.", sp.BackendTLSInsecure),
				Remediation:    "Configure ca_file or system roots and set insecure_skip_verify = false.",
			})
		}
		if sp.DiscoveryInsecure > 0 {
			addFinding(adminapi.SecurityPostureFinding{
				Code:           "trust_discovery_tls_insecure",
				Area:           "trust",
				Severity:       "warning",
				EpistemicState: "configured",
				Scope:          "upstreams.discovery",
				Summary:        fmt.Sprintf("%d service-discovery provider(s) disable registry TLS verification.", sp.DiscoveryInsecure),
				Remediation:    "Enable TLS verification on Consul/Kubernetes service discovery.",
			})
		}
		if !effCfg.Egress.Enabled {
			addFinding(adminapi.SecurityPostureFinding{
				Code:           "trust_egress_allowlist_disabled",
				Area:           "trust",
				Severity:       "info",
				EpistemicState: "configured",
				Scope:          "egress",
				Summary:        "Outbound auxiliary egress allow-list (JWKS, forward-auth, discovery, ACME) is not enabled.",
				Remediation:    "Enable egress.enabled and list trusted outbound CIDRs/hosts when outbound isolation is required.",
			})
		}
		for i, srv := range effCfg.Servers {
			if srv.ClientAddress == nil {
				continue
			}
			for _, cidr := range srv.ClientAddress.TrustedProxies {
				trimmed := strings.TrimSpace(cidr)
				if trimmed == "0.0.0.0/0" || trimmed == "::/0" {
					addFinding(adminapi.SecurityPostureFinding{
						Code:           "trust_wide_open_trusted_proxies",
						Area:           "trust",
						Severity:       "warning",
						EpistemicState: "configured",
						Scope:          fmt.Sprintf("servers.%d.client_address", i),
						Summary:        fmt.Sprintf("Listener %s trusts all peers (%s) to assert forwarding headers.", rawListen(i, srv.Listen), trimmed),
						Remediation:    "Restrict trusted_proxies to the exact proxy CIDRs you operate.",
					})
				}
			}
		}
	}

	// ── Area 3: waf ──────────────────────────────────────────────────────────
	if effCfg != nil {
		d := wafDistribution(effCfg)
		if !s.deps.WAFCompiled {
			sev := "info"
			if d.Locations > 0 || effCfg.WAF.Enabled {
				sev = "error"
			}
			addFinding(adminapi.SecurityPostureFinding{
				Code:           "waf_engine_not_compiled",
				Area:           "waf",
				Severity:       sev,
				EpistemicState: "unavailable",
				Scope:          "waf",
				Summary:        "This binary was built without the waf build tag; Coraza WAF inspection is unavailable.",
				Remediation:    "Use a binary built with -tags waf to enforce WAF policies.",
			})
		} else {
			unprotected := 0
			for _, srv := range effCfg.Servers {
				if srv.RedirectHTTPS != 0 && len(srv.Locations) == 0 {
					continue
				}
				for _, loc := range srv.Locations {
					if loc.Withdrawn {
						continue
					}
					w := effCfg.WAF
					if loc.WAF != nil {
						w = *loc.WAF
					}
					if !w.Enabled {
						unprotected++
					}
				}
			}
			if unprotected > 0 {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "waf_routes_unprotected",
					Area:           "waf",
					Severity:       "warning",
					EpistemicState: "configured",
					Scope:          "waf",
					Summary:        fmt.Sprintf("%d active HTTP route(s) have WAF inspection disabled.", unprotected),
					Remediation:    "Enable global waf.enabled or configure per-location WAF overrides for exposed routes.",
				})
			}
			if d.DetectLocs > 0 {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "waf_detect_only_routes",
					Area:           "waf",
					Severity:       "info",
					EpistemicState: "configured",
					Scope:          "waf.mode",
					Summary:        fmt.Sprintf("%d route(s) run WAF in detect-only mode (matches are logged but not blocked).", d.DetectLocs),
					Remediation:    "Evaluate synthetic fixtures and switch mode to block once tuning is complete.",
				})
			}
			if waf.DetectCustomTuningDirectives(effCfg.WAF.InlineRules) || (wafEff != nil && wafEff.Global != nil && wafEff.Global.HasCustomTuningDirectives) {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "waf_custom_seclang_unanalysed",
					Area:           "waf",
					Severity:       "info",
					EpistemicState: "unanalysed_custom",
					Scope:          "waf.inline_rules",
					Summary:        "Policy includes manual SecLang rule-removal or target-mutation directives outside structured exceptions.",
					Remediation:    "Prefer structured waf.exceptions entries and run jul waf evaluate against synthetic fixtures.",
				})
			}
			if wafEff != nil {
				var allEvals []*WAFEvaluationSummaryView
				if wafEff.Evaluation != nil {
					allEvals = append(allEvals, wafEff.Evaluation)
				}
				for _, ov := range wafEff.Overrides {
					if ov.Evaluation != nil {
						allEvals = append(allEvals, ov.Evaluation)
					}
				}
				var staleEval, failedEval, unrunEval *WAFEvaluationSummaryView
				for _, ev := range allEvals {
					switch {
					case ev.State == "failed" && failedEval == nil:
						failedEval = ev
					case strings.HasPrefix(ev.State, "stale") && staleEval == nil:
						staleEval = ev
					case ev.State == "unrun" && unrunEval == nil:
						unrunEval = ev
					}
				}
				if failedEval != nil {
					addFinding(adminapi.SecurityPostureFinding{
						Code:           "waf_evaluation_failed",
						Area:           "waf",
						Severity:       "error",
						EpistemicState: "observed",
						Scope:          "waf.evaluation",
						Summary:        fmt.Sprintf("Serving WAF policy failed %d synthetic fixture check(s).", failedEval.FailedCases),
						Remediation:    "Inspect failed fixtures in jul waf evaluate and adjust narrow exceptions or paranoia.",
					})
				} else if staleEval != nil {
					addFinding(adminapi.SecurityPostureFinding{
						Code:           "waf_evaluation_stale",
						Area:           "waf",
						Severity:       "warning",
						EpistemicState: "observed",
						Scope:          "waf.evaluation",
						Summary:        fmt.Sprintf("Serving WAF fixture evaluation is stale (%s).", staleEval.State),
						Remediation:    "Re-run jul waf evaluate or POST /api/security/waf/evaluate to verify the current policy.",
					})
				} else if unrunEval != nil && wafEff.Evaluation != nil && wafEff.Evaluation.State == "unrun" {
					addFinding(adminapi.SecurityPostureFinding{
						Code:           "waf_evaluation_unrun",
						Area:           "waf",
						Severity:       "info",
						EpistemicState: "observed",
						Scope:          "waf.evaluation",
						Summary:        "Serving WAF policy has not yet been evaluated against synthetic regression fixtures.",
						Remediation:    "Run jul waf evaluate or POST /api/security/waf/evaluate to verify the current policy against regression fixtures.",
					})
				}
			}
		}
	}

	// ── Area 4: cache ────────────────────────────────────────────────────────
	if effCfg != nil && effCfg.Cache.Enabled {
		for i, srv := range effCfg.Servers {
			for j, loc := range srv.Locations {
				if !loc.Cache || loc.Auth == nil {
					continue
				}
				bypassConfigured := loc.CachePolicy != nil && (loc.CachePolicy.BypassAnyCookie || len(loc.CachePolicy.BypassCookies) > 0)
				dispListen := rawListen(i, srv.Listen)
				dispPath := rawPath(i, j, loc.Match.Path)
				if bypassConfigured {
					addFinding(adminapi.SecurityPostureFinding{
						Code:           "cache_authenticated_route_cookie_bypass_configured",
						Area:           "cache",
						Severity:       "info",
						EpistemicState: "configured",
						Scope:          fmt.Sprintf("servers.%d.locations.%d.cache_policy", i, j),
						Summary:        fmt.Sprintf("Route %s %s combines authentication and caching with explicit cookie bypass.", dispListen, dispPath),
						Remediation:    "Verify upstream responses set Cache-Control: private or Vary: Authorization when user-specific.",
					})
				} else {
					addFinding(adminapi.SecurityPostureFinding{
						Code:           "cache_authenticated_route_review",
						Area:           "cache",
						Severity:       "info",
						EpistemicState: "inferred",
						Scope:          fmt.Sprintf("servers.%d.locations.%d.cache", i, j),
						Summary:        fmt.Sprintf("Route %s %s enables both authentication and response caching (safe for shared public assets, requires Cache-Control/Vary discipline for personalized responses).", dispListen, dispPath),
						Remediation:    "Configure cache_policy.bypass_any_cookie = true if the backend uses session cookies without Cache-Control: private.",
					})
				}
			}
		}
	}

	// ── Area 5: certificates & credentials ───────────────────────────────────
	if rawCfg != nil {
		for _, d := range config.LintWithRaw(rawCfg, effCfg, false) {
			switch {
			case d.Code == "waf_exception_review_due":
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "waf_exception_review_due",
					Area:           "waf",
					Severity:       "warning",
					EpistemicState: "configured",
					Scope:          d.Field,
					Summary:        d.Message,
					Remediation:    d.Hint,
				})
			case strings.Contains(d.Message, "literal value"):
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "certificates_literal_secret_in_config",
					Area:           "certificates",
					Severity:       "warning",
					EpistemicState: "configured",
					Scope:          d.Field,
					Summary:        d.Message,
					Remediation:    d.Hint,
				})
			case strings.Contains(d.Message, "native gRPC route is covered by the WAF"):
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "waf_native_grpc_incompatible",
					Area:           "waf",
					Severity:       "warning",
					EpistemicState: "configured",
					Scope:          d.Field,
					Summary:        d.Message,
					Remediation:    d.Hint,
				})
			case strings.Contains(d.Message, "request_body_limit"):
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "waf_body_limit_below_route_max",
					Area:           "waf",
					Severity:       "warning",
					EpistemicState: "configured",
					Scope:          d.Field,
					Summary:        d.Message,
					Remediation:    d.Hint,
				})
			case d.Code == config.FindingIntentPreviewNotLoopback,
				d.Code == config.FindingIntentPrivateBindTooWide,
				d.Code == config.FindingIntentChecksBypassed,
				d.Code == config.FindingIntentRestrictedCached:
				addFinding(adminapi.SecurityPostureFinding{
					Code:           d.Code,
					Area:           "intent",
					Severity:       "warning",
					EpistemicState: "configured",
					Scope:          d.Field,
					Summary:        d.Message,
					Remediation:    d.Hint,
				})
			case d.Code == config.FindingIntentPublicHasChecks:
				addFinding(adminapi.SecurityPostureFinding{
					Code:           d.Code,
					Area:           "intent",
					Severity:       "info",
					EpistemicState: "configured",
					Scope:          d.Field,
					Summary:        d.Message,
					Remediation:    d.Hint,
				})
			case d.Code == config.FindingCacheSharedAcrossEntries:
				addFinding(adminapi.SecurityPostureFinding{
					Code:           d.Code,
					Area:           "cache",
					Severity:       "warning",
					EpistemicState: "configured",
					Scope:          d.Field,
					Summary:        d.Message,
					Remediation:    d.Hint,
				})
			}
		}
	}
	if effCfg != nil {
		for i, srv := range effCfg.Servers {
			if srv.TLS == nil || !srv.TLS.Enabled {
				continue
			}
			certPath := strings.TrimSpace(srv.TLS.Cert)
			if certPath == "" {
				continue
			}
			scope := fmt.Sprintf("servers.%d.tls.cert", i)
			dispListen := rawListen(i, srv.Listen)
			pemBytes, err := os.ReadFile(certPath)
			if err != nil {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "certificates_tls_cert_unreadable",
					Area:           "certificates",
					Severity:       "warning",
					EpistemicState: "observed",
					Scope:          scope,
					Summary:        fmt.Sprintf("Listener %s TLS certificate file cannot be read for local posture inspection.", dispListen),
					Remediation:    "Verify the TLS certificate path and file permissions.",
				})
				continue
			}
			block, _ := pem.Decode(pemBytes)
			if block == nil {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "certificates_tls_cert_unreadable",
					Area:           "certificates",
					Severity:       "warning",
					EpistemicState: "observed",
					Scope:          scope,
					Summary:        fmt.Sprintf("Listener %s TLS certificate file contains no PEM block.", dispListen),
					Remediation:    "Replace the TLS certificate file with a valid PEM-encoded X.509 certificate.",
				})
				continue
			}
			leaf, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "certificates_tls_cert_unreadable",
					Area:           "certificates",
					Severity:       "warning",
					EpistemicState: "observed",
					Scope:          scope,
					Summary:        fmt.Sprintf("Listener %s TLS leaf certificate could not be parsed.", dispListen),
					Remediation:    "Replace the TLS certificate file with a valid X.509 certificate.",
				})
				continue
			}
			if !now.Before(leaf.NotAfter) {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "certificates_tls_cert_expired",
					Area:           "certificates",
					Severity:       "error",
					EpistemicState: "observed",
					Scope:          scope,
					Summary:        fmt.Sprintf("Listener %s TLS leaf certificate expired at %s.", dispListen, leaf.NotAfter.UTC().Format(time.RFC3339)),
					Remediation:    "Renew and reload the listener TLS certificate immediately.",
				})
			} else if now.Before(leaf.NotBefore) {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "certificates_tls_cert_not_yet_valid",
					Area:           "certificates",
					Severity:       "error",
					EpistemicState: "observed",
					Scope:          scope,
					Summary:        fmt.Sprintf("Listener %s TLS leaf certificate is not valid until %s.", dispListen, leaf.NotBefore.UTC().Format(time.RFC3339)),
					Remediation:    "Check system clock synchronization or replace the certificate.",
				})
			} else if now.Add(30 * 24 * time.Hour).After(leaf.NotAfter) {
				addFinding(adminapi.SecurityPostureFinding{
					Code:           "certificates_tls_cert_expiring_soon",
					Area:           "certificates",
					Severity:       "warning",
					EpistemicState: "observed",
					Scope:          scope,
					Summary:        fmt.Sprintf("Listener %s TLS leaf certificate expires within 30 days (%s).", dispListen, leaf.NotAfter.UTC().Format(time.RFC3339)),
					Remediation:    "Renew the TLS certificate before expiration.",
				})
			}
			for snIdx, sn := range srv.ServerNames {
				host := strings.TrimSpace(sn)
				if host == "" || strings.Contains(host, "*") {
					continue
				}
				if err := leaf.VerifyHostname(host); err != nil {
					addFinding(adminapi.SecurityPostureFinding{
						Code:           "certificates_tls_san_mismatch",
						Area:           "certificates",
						Severity:       "warning",
						EpistemicState: "observed",
						Scope:          scope,
						Summary:        fmt.Sprintf("Listener %s server_name %q is not covered by the leaf certificate SANs.", dispListen, rawServerName(i, snIdx, host)),
						Remediation:    "Reissue the certificate with matching Subject Alternative Names for all configured server_names.",
					})
				}
			}
		}
	}

	// ── Area 6: intent (application sharing & isolation) ─────────────────────
	if effCfg != nil {
		for i, srv := range effCfg.Servers {
			dispListen := rawListen(i, srv.Listen)
			for j, loc := range srv.Locations {
				dispPath := rawPath(i, j, loc.Match.Path)
				scope := fmt.Sprintf("%s %s", dispListen, dispPath)
				if loc.Withdrawn {
					addFinding(adminapi.SecurityPostureFinding{
						Code:           "sharing_route_withdrawn",
						Area:           "intent",
						Severity:       "info",
						EpistemicState: "configured",
						Scope:          scope,
						Summary:        fmt.Sprintf("Route %s is intentionally withdrawn (returns 503 before upstream or auth).", scope),
						Remediation:    "Set withdrawn = false when ready to re-expose the route.",
					})
					continue
				}
				if loc.Sharing == nil {
					continue
				}
				mode := strings.ToLower(strings.TrimSpace(loc.Sharing.Mode))
				var extEnf string
				if loc.Sharing.ExternalEnforcer != nil {
					extEnf = rawExternalEnforcer(i, j, strings.TrimSpace(*loc.Sharing.ExternalEnforcer))
				}
				hasLocalAuth := config.LocationHasAnyJulCheck(srv, loc) || config.LocationHasClientCertCheckInConfig(effCfg, srv, loc)
				switch mode {
				case "public":
					addFinding(adminapi.SecurityPostureFinding{
						Code:           "sharing_public_intentional",
						Area:           "intent",
						Severity:       "info",
						EpistemicState: "configured",
						Scope:          scope,
						Summary:        fmt.Sprintf("Route %s is declared intentionally public (unauthenticated access is expected by design).", scope),
						Remediation:    "Keep WAF and rate limiting enabled according to your public threat model.",
					})
				case "preview", "private_network":
					if extEnf != "" && !hasLocalAuth {
						addFinding(adminapi.SecurityPostureFinding{
							Code:           "sharing_external_enforcer_unverified",
							Area:           "intent",
							Severity:       "info",
							EpistemicState: "external_unknown",
							Scope:          scope,
							Summary:        fmt.Sprintf("Route %s declares %s access enforced externally by %q (not runtime-verified by Jul).", scope, mode, extEnf),
							Remediation:    "Verify network/firewall isolation so traffic cannot bypass the external enforcer.",
						})
					}
				case "restricted":
					if loc.Deny {
						continue
					}
					if extEnf != "" && !hasLocalAuth {
						addFinding(adminapi.SecurityPostureFinding{
							Code:           "sharing_external_enforcer_unverified",
							Area:           "intent",
							Severity:       "info",
							EpistemicState: "external_unknown",
							Scope:          scope,
							Summary:        fmt.Sprintf("Route %s declares restricted access enforced externally by %q (not runtime-verified by Jul).", scope, extEnf),
							Remediation:    "Verify network/firewall isolation so traffic cannot bypass the external enforcer.",
						})
					} else if !hasLocalAuth && extEnf == "" {
						addFinding(adminapi.SecurityPostureFinding{
							Code:           "sharing_restricted_without_enforcement",
							Area:           "intent",
							Severity:       "error",
							EpistemicState: "configured",
							Scope:          scope,
							Summary:        fmt.Sprintf("Route %s declares sharing.mode = restricted but has no local auth, mTLS, or external_enforcer.", scope),
							Remediation:    "Configure servers.locations.auth, require_client_cert, or declare external_enforcer.",
						})
					}
				}
			}
		}
	}

	// ── Area 7: maintenance (authority, drift, staged restart, serving sync) ─
	authStatus := s.currentAuthority()
	if authStatus.Drift {
		addFinding(adminapi.SecurityPostureFinding{
			Code:           "maintenance_external_drift_detected",
			Area:           "maintenance",
			Severity:       "warning",
			EpistemicState: "observed",
			Scope:          "global.config_authority",
			Summary:        "Managed configuration file was modified on disk outside Jul (unresolved drift).",
			Remediation:    "Preview and adopt the external edit or restore the managed baseline.",
		})
	}
	pr := s.pendingRestartState()
	if pr.Pending {
		addFinding(adminapi.SecurityPostureFinding{
			Code:           "maintenance_staged_restart_pending",
			Area:           "maintenance",
			Severity:       "warning",
			EpistemicState: "observed",
			Scope:          "config.pending_restart",
			Summary:        fmt.Sprintf("A staged restart candidate is pending for subsystem(s): %s.", strings.Join(pr.Subsystems, ", ")),
			Remediation:    "Restart the Jul process to activate the staged configuration or discard the staged candidate.",
		})
	}
	if effCfg != nil && wafEff != nil && wafEff.GlobalEnabled != effCfg.WAF.Enabled {
		addFinding(adminapi.SecurityPostureFinding{
			Code:           "maintenance_waf_serving_differs_from_persisted",
			Area:           "maintenance",
			Severity:       "warning",
			EpistemicState: "observed",
			Scope:          "waf",
			Summary:        "Persisted WAF enablement differs from the currently serving handler generation.",
			Remediation:    "Trigger a configuration reload or inspect the last reload error.",
		})
	}
	if persistedCfg != nil && rawCfg != nil && !reflect.DeepEqual(persistedCfg, rawCfg) {
		addFinding(adminapi.SecurityPostureFinding{
			Code:           "maintenance_serving_differs_from_persisted",
			Area:           "maintenance",
			Severity:       "warning",
			EpistemicState: "observed",
			Scope:          "config",
			Summary:        "Persisted on-disk configuration differs from the currently serving generation; posture findings reflect the serving generation until reload or restart completes.",
			Remediation:    "Reload or restart Jul to activate the persisted configuration, or inspect the last reload/stage error.",
		})
		if wafEff != nil && wafEff.GlobalEnabled != persistedCfg.WAF.Enabled {
			addFinding(adminapi.SecurityPostureFinding{
				Code:           "maintenance_waf_serving_differs_from_persisted",
				Area:           "maintenance",
				Severity:       "warning",
				EpistemicState: "observed",
				Scope:          "waf",
				Summary:        "Persisted WAF enablement differs from the currently serving handler generation.",
				Remediation:    "Trigger a configuration reload or inspect the last reload error.",
			})
		}
	}

	// Summarize across all 7 areas and update bounded metrics (#628-B / #633).
	areaMap := make(map[string]*adminapi.SecurityPostureAreaSummary, len(postureAreaOrder))
	metricCounts := make(map[string]map[string]int, len(postureAreaOrder))
	for _, area := range postureAreaOrder {
		areaMap[area] = &adminapi.SecurityPostureAreaSummary{
			Area:           area,
			Status:         "ok",
			EpistemicState: "configured",
			Summary:        fmt.Sprintf("No material issues detected in %s posture.", area),
		}
		metricCounts[area] = map[string]int{"warning": 0, "error": 0}
	}

	totalErr, totalWarn, totalInfo := 0, 0, 0
	for _, f := range findings {
		a := areaMap[f.Area]
		if a == nil {
			continue
		}
		switch f.Severity {
		case "error":
			a.ErrorCount++
			totalErr++
			metricCounts[f.Area]["error"]++
			a.Status = "error"
			a.EpistemicState = f.EpistemicState
			a.Summary = f.Summary
		case "warning":
			a.WarningCount++
			totalWarn++
			metricCounts[f.Area]["warning"]++
			if a.Status != "error" {
				a.Status = "warning"
				a.EpistemicState = f.EpistemicState
				a.Summary = f.Summary
			}
		default:
			a.InfoCount++
			totalInfo++
			if a.Status == "ok" {
				a.EpistemicState = f.EpistemicState
				a.Summary = f.Summary
			}
		}
	}
	if !s.deps.WAFCompiled {
		areaMap["waf"].Status = "unavailable"
		areaMap["waf"].EpistemicState = "unavailable"
	}

	areas := make([]adminapi.SecurityPostureAreaSummary, 0, len(postureAreaOrder))
	for _, name := range postureAreaOrder {
		areas = append(areas, *areaMap[name])
	}
	overall := "ok"
	if totalErr > 0 {
		overall = "error"
	} else if totalWarn > 0 {
		overall = "warning"
	}

	if s.deps.ObserveSecurityPosture != nil {
		s.deps.ObserveSecurityPosture(metricCounts)
	}
	if s.deps.ObserveSecurityEvidenceState != nil {
		wafEvState := "ok"
		if !s.deps.WAFCompiled {
			wafEvState = "unavailable"
		} else if wafEff != nil {
			var evals []*WAFEvaluationSummaryView
			if wafEff.Evaluation != nil {
				evals = append(evals, wafEff.Evaluation)
			}
			for _, ov := range wafEff.Overrides {
				if ov.Evaluation != nil {
					evals = append(evals, ov.Evaluation)
				}
			}
			for _, ev := range evals {
				if strings.HasPrefix(ev.State, "stale") {
					wafEvState = "stale"
					break
				}
				if ev.State == "failed" {
					wafEvState = "degraded"
				}
			}
		}
		s.deps.ObserveSecurityEvidenceState("waf_effective", wafEvState)

		var invState string
		if !s.deps.WAFCompiled {
			invState = "unavailable"
		} else if s.deps.WAFInvestigation == nil {
			invState = "disabled"
		} else {
			invState = s.deps.WAFInvestigation(waf.InvestigationFilter{Limit: 1}).EvidenceState()
		}
		s.deps.ObserveSecurityEvidenceState("waf_investigation", invState)

		auditState := "disabled"
		if s.audit != nil {
			if st := s.audit.statusReport(); st != nil {
				if st.Healthy {
					auditState = "ok"
				} else {
					auditState = "degraded"
				}
			}
		}
		s.deps.ObserveSecurityEvidenceState("audit_sink", auditState)
		s.deps.ObserveSecurityEvidenceState("posture_evaluator", "ok")
	}

	return adminapi.SecurityPostureResponse{
		APIVersion:        adminapi.APIVersion,
		EvaluatedAt:       adminapi.Timestamp(now),
		BaseVersion:       baseVersion,
		ServingGeneration: servingGen,
		OverallStatus:     overall,
		ErrorCount:        totalErr,
		WarningCount:      totalWarn,
		InfoCount:         totalInfo,
		Areas:             areas,
		Findings:          findings,
	}
}

// sanitizePostureToken removes TOML section brackets so v1 posture responses
// never resemble raw TOML configuration slices (TestNoV1ResponseBodyCarriesConfigurationBytes).
func sanitizePostureToken(s string) string {
	r := strings.NewReplacer(
		"[[servers.locations]]", "servers.locations",
		"[[servers]]", "servers",
		"[[upstreams]]", "upstreams",
		"[admin.rbac]", "admin.rbac",
		"[admin.tls]", "admin.tls",
		"[admin.oidc]", "admin.oidc",
		"[admin]", "admin",
		"[global]", "global",
		"[compression]", "compression",
		"[observability.access_log]", "observability.access_log",
		"[waf]", "waf",
	)
	return redact.Apply(r.Replace(s))
}

func redactPostureSummaryForStatusRead(code, area, severity string) string {
	switch code {
	case "waf_exception_review_due":
		return "One or more WAF exceptions have a past-due review_at date."
	case "trust_wide_open_trusted_proxies":
		return "A listener trusts all peers to assert forwarding headers."
	case "cache_authenticated_route_cookie_bypass_configured":
		return "An authenticated route enables response caching with explicit cookie bypass."
	case "cache_authenticated_route_review":
		return "An authenticated route enables response caching."
	case "sharing_route_withdrawn":
		return "A configured route is intentionally withdrawn."
	case "sharing_public_intentional":
		return "A configured route is declared intentionally public."
	case "sharing_external_enforcer_unverified":
		return "A restricted route declares an external enforcer not runtime-verified by Jul."
	case "sharing_restricted_without_enforcement":
		return "A restricted route has no local auth, mTLS, or external enforcer."
	default:
		return fmt.Sprintf("Posture finding %s (%s) in area %s.", code, severity, area)
	}
}
