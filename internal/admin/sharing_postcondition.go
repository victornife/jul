// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"jul/internal/config"
	"jul/internal/rbac"
	"jul/internal/server"
)

const CurrentPostconditionVersion = 1

// Closed-set external_inputs values (spec §9.4).
const (
	ExternalInputJWTKeySet      = "jwt_key_set"
	ExternalInputForwardAuth    = "forward_auth"
	ExternalInputProviderPolicy = "provider_policy"
	ExternalInputFileContents   = "file_contents"
)

// Postcondition evaluation result codes (spec §9.4).
const (
	PostconditionResultUnknown          = "unknown"
	PostconditionResultUnverifiable     = "unverifiable"
	PostconditionResultNotServingYet    = "not_serving_yet"
	PostconditionResultCandidateServing = "candidate_serving"
	PostconditionResultDiverged         = "diverged"
	PostconditionResultAccessMatches    = "access_matches"
	PostconditionResultOtherRevision    = "other_revision"
)

// SharingPlanPostcondition is the §9.4 stateless postcondition returned with a
// plan response (or marked unavailable when S17-1 disclosure rules forbid it).
type SharingPlanPostcondition struct {
	Status                string                     `json:"status"`
	UnavailableReason     string                     `json:"unavailable_reason,omitempty"`
	PostconditionVersion  int                        `json:"postcondition_version,omitempty"`
	KeyID                 string                     `json:"key_id,omitempty"`
	BaseConfigDigest      string                     `json:"base_config_digest,omitempty"`
	CandidateConfigDigest string                     `json:"candidate_config_digest,omitempty"`
	Repeatable            bool                       `json:"repeatable"`
	Entries               []SharingPostconditionItem `json:"entries,omitempty"`
}

// SharingPostconditionItem is one entry expectation inside a postcondition.
type SharingPostconditionItem struct {
	Ref            SharingEntryRef               `json:"ref"`
	Expect         string                        `json:"expect"`
	Expected       *SharingPostconditionExpected `json:"expected,omitempty"`
	ExternalInputs []string                      `json:"external_inputs,omitempty"`
}

// SharingPostconditionExpected holds the closed-set serving facts and keyed
// access fingerprint expected for a present entry (spec §9.4, RR12).
type SharingPostconditionExpected struct {
	BindScope         string   `json:"bind_scope,omitempty"`
	NetworkCheck      string   `json:"network_check,omitempty"`
	CredentialCheck   string   `json:"credential_check,omitempty"`
	ClientCertificate string   `json:"client_certificate,omitempty"`
	DeclaredIntent    string   `json:"declared_intent,omitempty"`
	Withdrawn         *bool    `json:"withdrawn,omitempty"`
	OmittedFileFacts  []string `json:"omitted_file_facts,omitempty"`
	AccessFingerprint string   `json:"access_fingerprint"`
}

// SharingPostconditionEvaluateRequest is the request body for
// POST /api/sharing/postcondition/evaluate.
type SharingPostconditionEvaluateRequest struct {
	PostconditionVersion  int                        `json:"postcondition_version"`
	KeyID                 string                     `json:"key_id"`
	BaseConfigDigest      string                     `json:"base_config_digest"`
	CandidateConfigDigest string                     `json:"candidate_config_digest"`
	Repeatable            bool                       `json:"repeatable,omitempty"`
	Entries               []SharingPostconditionItem `json:"entries"`
}

// SharingPostconditionEntryEvaluation is the per-entry evaluation result.
type SharingPostconditionEntryEvaluation struct {
	Ref              SharingEntryRef               `json:"ref"`
	DisplayLabel     string                        `json:"display_label,omitempty"`
	Expect           string                        `json:"expect"`
	Status           string                        `json:"status"`
	DifferingFields  []string                      `json:"differing_fields,omitempty"`
	Reason           string                        `json:"reason,omitempty"`
	Current          *SharingPostconditionExpected `json:"current,omitempty"`
	OmittedFileFacts []string                      `json:"omitted_file_facts,omitempty"`
	ExternalInputs   []string                      `json:"external_inputs,omitempty"`
	ExternalNotes    []string                      `json:"external_notes,omitempty"`
}

// SharingPostconditionEvaluationResponse is the response returned by the §9.4
// postcondition evaluator.
type SharingPostconditionEvaluationResponse struct {
	PostconditionVersion int                                   `json:"postcondition_version"`
	Result               string                                `json:"result"`
	Candidate            string                                `json:"candidate"`
	Access               string                                `json:"access"`
	ServingVersion       string                                `json:"serving_version,omitempty"`
	CandidateSummary     string                                `json:"candidate_summary"`
	AccessSummary        string                                `json:"access_summary"`
	Summary              string                                `json:"summary"`
	RecoveryGuidance     string                                `json:"recovery_guidance,omitempty"`
	Entries              []SharingPostconditionEntryEvaluation `json:"entries"`
}

func (s *Server) ensurePostconditionKey() (string, []byte) {
	s.sharingKeyOnce.Do(func() {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			panic("admin: crypto/rand failed to initialize postcondition key: " + err.Error())
		}
		var idBytes [8]byte
		if _, err := rand.Read(idBytes[:]); err != nil {
			panic("admin: crypto/rand failed to initialize postcondition key_id: " + err.Error())
		}
		s.sharingKey = key[:]
		s.sharingKeyID = "pk1-" + hex.EncodeToString(idBytes[:])
	})
	return s.sharingKeyID, s.sharingKey
}

func (s *Server) keyedConfigDigest(rawCfg *config.Config) (string, error) {
	if rawCfg == nil {
		return "", fmt.Errorf("nil config")
	}
	cloned, err := rawCfg.Clone()
	if err != nil {
		return "", err
	}
	canonical, err := config.Marshal(cloned)
	if err != nil {
		return "", err
	}
	_, key := s.ensurePostconditionKey()
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("jul-postcondition-config-v1\x00"))
	_, _ = mac.Write(canonical)
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) keyedAccessFingerprint(rawCfg *config.Config, srvIdx, locIdx int) string {
	_, key := s.ensurePostconditionKey()
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("jul-postcondition-access-v1\x00"))
	if rawCfg != nil {
		if cloned, err := rawCfg.Clone(); err == nil {
			rawCfg = cloned
		}
	}
	if rawCfg != nil && srvIdx >= 0 && srvIdx < len(rawCfg.Servers) {
		srv := rawCfg.Servers[srvIdx]
		if locIdx >= 0 && locIdx < len(srv.Locations) {
			loc := srv.Locations[locIdx]
			payload := canonicalWrittenAccessInputs(rawCfg, srvIdx, locIdx, loc)
			data, _ := json.Marshal(payload)
			_, _ = mac.Write(data)
		}
	}
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

type competingBlockFingerprint struct {
	Position        string   `json:"position"`
	NormalizedNames []string `json:"normalized_names"`
	CatchAll        bool     `json:"catch_all"`
}

type siblingLocationFingerprint struct {
	Match    config.MatchConfig     `json:"match"`
	Rewrites []config.RewriteConfig `json:"rewrites,omitempty"`
}

func canonicalWrittenAccessInputs(rawCfg *config.Config, srvIdx, locIdx int, loc config.LocationConfig) map[string]any {
	srv := rawCfg.Servers[srvIdx]
	listenerIn, _ := summarizeListenerInputs(rawCfg, srv.Listen)
	effWAF := effectiveRawWAFForLocation(rawCfg, loc.WAF)

	// Host dispatch on srv.Listen (§9.5): record whether srv is the listener's
	// first-declared block (which receives unmatched hosts), and the ordered
	// competing blocks on srv.Listen whose host patterns can affect dispatch to srv.
	firstOnListener := -1
	for i, other := range rawCfg.Servers {
		if other.Listen == srv.Listen {
			firstOnListener = i
			break
		}
	}
	isFirstOnListener := firstOnListener == srvIdx
	isSelfCatchAll := config.IsCatchAllServerNames(srv.ServerNames)

	var competingBlocks []competingBlockFingerprint
	for i, other := range rawCfg.Servers {
		if other.Listen != srv.Listen {
			continue
		}
		include := isFirstOnListener || isSelfCatchAll || i == srvIdx || config.ServerHostSetsOverlap(srv.ServerNames, other.ServerNames)
		if !include {
			continue
		}
		pos := "self"
		if i < srvIdx {
			pos = "before"
		} else if i > srvIdx {
			pos = "after"
		}
		normNames := make([]string, 0, len(other.ServerNames))
		for _, n := range other.ServerNames {
			normNames = append(normNames, config.NormalizeHostPattern(n))
		}
		competingBlocks = append(competingBlocks, competingBlockFingerprint{
			Position:        pos,
			NormalizedNames: normNames,
			CatchAll:        config.IsCatchAllServerNames(other.ServerNames),
		})
	}

	// Block order and sibling matches/rewrites within the same block affect fall-through (§9.5).
	siblingLocations := make([]siblingLocationFingerprint, 0, len(srv.Locations))
	for _, sl := range srv.Locations {
		siblingLocations = append(siblingLocations, siblingLocationFingerprint{
			Match:    sl.Match,
			Rewrites: sl.Rewrites,
		})
	}

	var pluginDefs map[string]config.PluginConfig
	if len(srv.Plugins) > 0 || len(loc.Plugins) > 0 || loc.Plugin != "" {
		pluginDefs = make(map[string]config.PluginConfig)
		for _, n := range srv.Plugins {
			if p, ok := rawCfg.Plugins[n]; ok {
				pluginDefs[n] = p
			}
		}
		for _, n := range loc.Plugins {
			if p, ok := rawCfg.Plugins[n]; ok {
				pluginDefs[n] = p
			}
		}
		if loc.Plugin != "" {
			if p, ok := rawCfg.Plugins[loc.Plugin]; ok {
				pluginDefs[loc.Plugin] = p
			}
		}
	}

	var referencedUpstreams map[string]config.UpstreamConfig
	if upNames := config.ResolveLocationDependencyUpstreams(rawCfg, loc); len(upNames) > 0 {
		referencedUpstreams = make(map[string]config.UpstreamConfig, len(upNames))
		for _, upName := range upNames {
			if up := findUpstreamByName(rawCfg, upName); up != nil {
				referencedUpstreams[upName] = config.UpstreamConfig{
					Name:       up.Name,
					Strategy:   up.Strategy,
					Hash:       up.Hash,
					BackendTLS: up.BackendTLS,
					Servers:    up.Servers,
					Discovery:  up.Discovery,
				}
			}
		}
	}

	return map[string]any{
		"listen":                srv.Listen,
		"server_names":          srv.ServerNames,
		"server_redirect_https": srv.RedirectHTTPS,
		"is_first_on_listener":  isFirstOnListener,
		"competing_blocks":      competingBlocks,
		"listener_inputs":       listenerIn,
		"location_index":        locIdx,
		"sibling_locations":     siblingLocations,
		"match":                 loc.Match,
		"rewrites":              loc.Rewrites,
		"server_plugins":        srv.Plugins,
		"location_plugins":      loc.Plugins,
		"handler_plugin":        loc.Plugin,
		"plugin_defs":           pluginDefs,
		"cors":                  loc.CORS,
		"auth":                  loc.Auth,
		"require_client_cert":   loc.RequireClientCert,
		"withdrawn":             loc.Withdrawn,
		"deny":                  loc.Deny,
		"action":                locationAction(&loc),
		"target":                locationTarget(&loc),
		"grpc":                  loc.GRPC,
		"grpc_transcode":        loc.GRPCTranscode,
		"backend_tls":           loc.BackendTLS,
		"fastcgi_params":        loc.FastCGIParams,
		"index":                 loc.Index,
		"try_files":             loc.TryFiles,
		"directory_listing":     loc.DirectoryListing,
		"allow_hidden":          loc.AllowHidden,
		"return":                loc.Return,
		"upstream":              referencedUpstreams,
		"sharing":               loc.Sharing,
		"waf":                   effWAF,
	}
}

// IsPatchOpRepeatable reports whether op is a target-state setter whose repeat
// is a no-op against the resulting configuration (spec §9.4, RR10).
func IsPatchOpRepeatable(op patchRequest) bool {
	switch op.Op {
	case "route_set_target",
		"route_toggle_cache",
		"route_toggle_rate_limit",
		"route_set_rate_limit",
		"location_waf_set",
		"location_set_auth",
		"location_set_sharing",
		"location_clear_sharing",
		"location_set_withdrawn",
		"location_set_action",
		"location_set_transcode",
		"location_set_predicates",
		"location_response_headers_set",
		"location_cors_set",
		"location_cache_policy_set",
		"location_toggle_require_client_cert",
		"server_set_limits",
		"server_toggle_http3",
		"server_toggle_h2c",
		"server_set_client_auth",
		"listener_set_client_address",
		"upstream_set_strategy",
		"upstream_set_health_check",
		"upstream_set_resilience",
		"upstream_set_discovery",
		"plugin_set",
		"stream_set",
		"global_set",
		"compression_set",
		"rate_limit_global_set",
		"admin_console_set",
		"admin_plugin_upload_set",
		"admin_limits_set",
		"admin_audit_sink_set":
		return true
	default:
		return false
	}
}

// IsPatchBatchRepeatable reports whether every operation in ops is repeatable.
func IsPatchBatchRepeatable(ops []patchRequest) bool {
	if len(ops) == 0 {
		return false
	}
	for _, op := range ops {
		if !IsPatchOpRepeatable(op) {
			return false
		}
	}
	return true
}

// patchOpsRequireConfigRawForPostcondition reports whether any operation in ops
// sets a field that is hidden from a caller who does not hold config:raw
// (spec §9.4 S17-1 / E60 / E61).
func patchOpsRequireConfigRawForPostcondition(ops []patchRequest) bool {
	for _, op := range ops {
		if op.Op == "plugin_set" {
			return true
		}
	}
	return false
}

func (s *Server) producePlanPostcondition(
	beforeRaw *config.Config,
	afterRaw *config.Config,
	afterEff *config.Config,
	pairs []correlatedEntryPair,
	deltas []SharingEntryDelta,
	isRawPlan bool,
	ops []patchRequest,
	callerHasRaw bool,
) SharingPlanPostcondition {
	if isRawPlan && !callerHasRaw {
		return SharingPlanPostcondition{
			Status:            "unavailable",
			UnavailableReason: "config_raw_required",
		}
	}
	if !isRawPlan && !callerHasRaw && patchOpsRequireConfigRawForPostcondition(ops) {
		return SharingPlanPostcondition{
			Status:            "unavailable",
			UnavailableReason: "config_raw_required",
		}
	}

	keyID, _ := s.ensurePostconditionKey()
	baseDigest, err1 := s.keyedConfigDigest(beforeRaw)
	candDigest, err2 := s.keyedConfigDigest(afterRaw)
	if err1 != nil || err2 != nil {
		return SharingPlanPostcondition{
			Status:            "unavailable",
			UnavailableReason: "digest_unavailable",
		}
	}

	repeatable := true
	if !isRawPlan {
		repeatable = IsPatchBatchRepeatable(ops)
	}

	// Build a lookup of affected entries from deltas.
	affectedRefs := make(map[string]bool, len(deltas))
	for _, d := range deltas {
		if d.Class != AudienceDeltaUnchanged || d.DeclaredModeBefore != d.DeclaredModeAfter {
			affectedRefs[d.Ref.String()] = true
		}
	}

	items := make([]SharingPostconditionItem, 0, len(deltas))
	for _, pair := range pairs {
		if pair.after != nil {
			a := pair.after
			ref := entryRefForLocation(a.loc, "candidate", a.srvIdx, a.locIdx)
			if !affectedRefs[ref.String()] {
				continue
			}
			expected, extInputs := s.buildExpectedEntryFacts(afterRaw, afterEff, a.srvIdx, a.locIdx)
			items = append(items, SharingPostconditionItem{
				Ref:            ref,
				Expect:         "present",
				Expected:       &expected,
				ExternalInputs: extInputs,
			})
		} else if pair.before != nil {
			b := pair.before
			ref := entryRefForLocation(b.loc, "base", b.srvIdx, b.locIdx)
			if !affectedRefs[ref.String()] {
				continue
			}
			items = append(items, SharingPostconditionItem{
				Ref:    ref,
				Expect: "absent",
			})
		}
	}

	return SharingPlanPostcondition{
		Status:                "available",
		PostconditionVersion:  CurrentPostconditionVersion,
		KeyID:                 keyID,
		BaseConfigDigest:      baseDigest,
		CandidateConfigDigest: candDigest,
		Repeatable:            repeatable,
		Entries:               items,
	}
}

func (s *Server) buildExpectedEntryFacts(
	rawCfg *config.Config,
	effCfg *config.Config,
	srvIdx, locIdx int,
) (SharingPostconditionExpected, []string) {
	rawSrv := rawCfg.Servers[srvIdx]
	rawLoc := rawSrv.Locations[locIdx]
	effSrv := effCfg.Servers[srvIdx]
	effLoc := effSrv.Locations[locIdx]

	var omitted []string
	var extInputs []string
	addExt := func(k string) {
		if !slices.Contains(extInputs, k) {
			extInputs = append(extInputs, k)
		}
	}

	// Bind scope (omitted if raw listen uses ${file:...}, RR12).
	bindScope := config.BindScope(effSrv.Listen)
	if _, hasFile := config.SecretRefSchemes(rawSrv.Listen); hasFile {
		bindScope = ""
		omitted = append(omitted, "bind_scope")
		addExt(ExternalInputFileContents)
	}

	// Network check (omitted if raw allow/deny uses ${file:...}, RR12).
	netCheck := deriveNetworkCheckKind(effLoc)
	if rawLoc.Auth != nil {
		for _, cidr := range append(append([]string(nil), rawLoc.Auth.Allow...), rawLoc.Auth.Deny...) {
			if _, hasFile := config.SecretRefSchemes(cidr); hasFile {
				netCheck = ""
				omitted = append(omitted, "network_check")
				addExt(ExternalInputFileContents)
				break
			}
		}
	}

	credCheck := deriveCredentialCheckKind(effLoc)
	certCheck := deriveClientCertCheckKind(effCfg, effSrv, effLoc)
	if _, certHasFile := config.LocationHasClientCertCheckForPlan(rawCfg, effCfg, srvIdx, effLoc); certHasFile {
		certCheck = ""
		omitted = append(omitted, "client_certificate")
		addExt(ExternalInputFileContents)
	}

	intentMode := declaredSharingMode(effLoc)
	if rawLoc.Sharing != nil {
		if _, hasFile := config.SecretRefSchemes(rawLoc.Sharing.Mode); hasFile {
			intentMode = ""
			omitted = append(omitted, "declared_intent")
			addExt(ExternalInputFileContents)
		}
	}

	withdrawn := isLocationWithdrawnByConfig(effLoc)

	// External inputs (§9.4).
	if effLoc.Auth != nil && effLoc.Auth.JWT != nil {
		addExt(ExternalInputJWTKeySet)
	}
	if effLoc.Auth != nil && effLoc.Auth.ForwardAuth != nil {
		addExt(ExternalInputForwardAuth)
	}
	if entryRequiresProviderChecklist(rawCfg, effCfg, srvIdx, locIdx) {
		addExt(ExternalInputProviderPolicy)
	}
	if len(entryFileBackedInputKinds(rawCfg, rawSrv, rawLoc)) > 0 {
		addExt(ExternalInputFileContents)
	}
	sort.Strings(extInputs)

	return SharingPostconditionExpected{
		BindScope:         bindScope,
		NetworkCheck:      netCheck,
		CredentialCheck:   credCheck,
		ClientCertificate: certCheck,
		DeclaredIntent:    intentMode,
		Withdrawn:         &withdrawn,
		OmittedFileFacts:  omitted,
		AccessFingerprint: s.keyedAccessFingerprint(rawCfg, srvIdx, locIdx),
	}, extInputs
}

func deriveNetworkCheckKind(loc config.LocationConfig) string {
	if loc.Auth == nil {
		return "none"
	}
	hasAllow := len(loc.Auth.Allow) > 0
	hasDeny := len(loc.Auth.Deny) > 0
	switch {
	case hasAllow && hasDeny:
		return "allow_and_deny"
	case hasAllow:
		return "allow_list"
	case hasDeny:
		return "deny_list"
	default:
		return "none"
	}
}

func deriveCredentialCheckKind(loc config.LocationConfig) string {
	if loc.Auth == nil {
		return "none"
	}
	switch {
	case loc.Auth.Basic != nil:
		return "basic"
	case loc.Auth.JWT != nil:
		return "jwt"
	case loc.Auth.ForwardAuth != nil:
		return "forward_auth"
	default:
		return "none"
	}
}

func deriveClientCertCheckKind(c *config.Config, srv config.ServerConfig, loc config.LocationConfig) string {
	mode := "none"
	if c != nil {
		for _, ls := range c.Servers {
			if ls.Listen != srv.Listen || ls.TLS == nil || !ls.TLS.Enabled || ls.TLS.ClientAuth == nil || !ls.TLS.ClientAuth.Active() {
				continue
			}
			m := strings.TrimSpace(ls.TLS.ClientAuth.Mode)
			if config.ContainsSecretRef(m) {
				return "unknown"
			}
			if m == "require" || mode == "none" {
				mode = m
			}
		}
	}
	switch mode {
	case "require":
		return "required_by_listener"
	case "request":
		if loc.RequireClientCert {
			return "required_by_route"
		}
		return "not_required"
	default:
		return "not_required"
	}
}

func isLocationWithdrawnByConfig(loc config.LocationConfig) bool {
	if loc.Withdrawn || loc.Deny {
		return true
	}
	if loc.Auth == nil || len(loc.Auth.Deny) == 0 {
		return false
	}
	return config.CIDRSetPermitsNone(loc.Auth.Allow, loc.Auth.Deny)
}

// EvaluatePostcondition evaluates req against one captured serving snapshot
// without re-reading the environment or files (spec §9.4).
func (s *Server) EvaluatePostcondition(req SharingPostconditionEvaluateRequest, callerHasRaw bool) SharingPostconditionEvaluationResponse {
	ver := req.PostconditionVersion
	if ver == 0 {
		ver = CurrentPostconditionVersion
	}

	var snap server.LiveSnapshot
	hasSnap := false
	if s.deps.LiveSnapshot != nil {
		snap = s.deps.LiveSnapshot()
		if snap.RawConfig != nil && snap.EffectiveConfig != nil {
			hasSnap = true
		}
	}
	if !hasSnap && s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			snap = server.LiveSnapshot{
				Generation:      1,
				RawConfig:       cfg,
				EffectiveConfig: cfg,
			}
			hasSnap = true
		}
	}

	if !hasSnap || snap.RawConfig == nil || snap.EffectiveConfig == nil {
		return SharingPostconditionEvaluationResponse{
			PostconditionVersion: ver,
			Result:               PostconditionResultUnknown,
			Candidate:            "unknown",
			Access:               "unknown",
			CandidateSummary:     "Jul cannot tell what configuration is serving.",
			AccessSummary:        "Jul cannot evaluate expected access facts without a serving snapshot.",
			Summary:              "Jul cannot tell what is serving.",
			Entries:              []SharingPostconditionEntryEvaluation{},
		}
	}

	servingVersion := s.servingVersion()
	if servingVersion == "" {
		servingVersion = server.CanonicalVersion(snap.EffectiveConfig)
	}

	keyID, _ := s.ensurePostconditionKey()
	if strings.TrimSpace(req.KeyID) == "" || req.KeyID != keyID {
		entries := make([]SharingPostconditionEntryEvaluation, 0, len(req.Entries))
		for _, item := range req.Entries {
			ev := SharingPostconditionEntryEvaluation{
				Ref:            item.Ref,
				Expect:         item.Expect,
				Status:         "unverifiable",
				Reason:         "planned_by_earlier_process",
				ExternalInputs: item.ExternalInputs,
			}
			if item.Ref.Kind == "route_id" && item.Ref.RouteID != "" {
				if srvIdx, locIdx, ok := findServingLocationByRouteID(snap.RawConfig, item.Ref.RouteID); ok {
					cur, _ := s.buildExpectedEntryFacts(snap.RawConfig, snap.EffectiveConfig, srvIdx, locIdx)
					cur.AccessFingerprint = ""
					ev.Current = &cur
					ev.DisplayLabel = entryDisplayLabel(snap.RawConfig.Servers[srvIdx], snap.RawConfig.Servers[srvIdx].Locations[locIdx])
				}
			}
			entries = append(entries, ev)
		}
		guidance := "Plan the same raw configuration or repeatable typed operations again against the running process, or inspect the listed entries' current facts and configuration history by hand."
		if !req.Repeatable {
			guidance = "This plan included non-repeatable operations (such as adding or removing a route); inspect the listed entries' current facts in the route list and configuration history by hand."
		}
		return SharingPostconditionEvaluationResponse{
			PostconditionVersion: ver,
			Result:               PostconditionResultUnverifiable,
			Candidate:            "unverifiable",
			Access:               "unverifiable",
			ServingVersion:       servingVersion,
			CandidateSummary:     "Jul has restarted since this change was planned, so it cannot compare the candidate digest.",
			AccessSummary:        "Expected access fingerprints were keyed to an earlier process.",
			Summary:              "Jul has restarted since this change was planned, so it cannot compare it.",
			RecoveryGuidance:     guidance,
			Entries:              entries,
		}
	}

	servingDigest, err := s.keyedConfigDigest(snap.RawConfig)
	if err != nil {
		return SharingPostconditionEvaluationResponse{
			PostconditionVersion: ver,
			Result:               PostconditionResultUnknown,
			Candidate:            "unknown",
			Access:               "unknown",
			ServingVersion:       servingVersion,
			CandidateSummary:     "Jul could not compute the serving configuration digest.",
			AccessSummary:        "Jul could not evaluate expected access facts.",
			Summary:              "Jul cannot tell what is serving.",
			Entries:              []SharingPostconditionEntryEvaluation{},
		}
	}

	var candidateState string
	switch servingDigest {
	case req.CandidateConfigDigest:
		candidateState = "serving"
	case req.BaseConfigDigest:
		candidateState = "base_serving"
	default:
		candidateState = "other_revision"
	}

	entries := make([]SharingPostconditionEntryEvaluation, 0, len(req.Entries))
	for _, item := range req.Entries {
		ev := s.evaluatePostconditionEntry(snap, candidateState, item, callerHasRaw)
		entries = append(entries, ev)
	}

	accessState := aggregatePostconditionAccess(entries)
	result, candSummary, accessSummary, summary, guidance := summarizePostconditionEvaluation(
		candidateState, accessState, servingVersion, entries, req.Repeatable,
	)

	return SharingPostconditionEvaluationResponse{
		PostconditionVersion: ver,
		Result:               result,
		Candidate:            candidateState,
		Access:               accessState,
		ServingVersion:       servingVersion,
		CandidateSummary:     candSummary,
		AccessSummary:        accessSummary,
		Summary:              summary,
		RecoveryGuidance:     guidance,
		Entries:              entries,
	}
}

func (s *Server) evaluatePostconditionEntry(
	snap server.LiveSnapshot,
	candidateState string,
	item SharingPostconditionItem,
	callerHasRaw bool,
) SharingPostconditionEntryEvaluation {
	ev := SharingPostconditionEntryEvaluation{
		Ref:            item.Ref,
		Expect:         item.Expect,
		ExternalInputs: item.ExternalInputs,
	}
	if item.Expected != nil {
		ev.OmittedFileFacts = item.Expected.OmittedFileFacts
	}
	ev.ExternalNotes = buildExternalInputNotes(ev.OmittedFileFacts, item.ExternalInputs, callerHasRaw)

	srvIdx, locIdx, resolvedStatus, reason := resolvePostconditionRef(snap.RawConfig, candidateState, item.Ref, item.Expect)
	if resolvedStatus == "unverifiable" {
		ev.Status = "unverifiable"
		ev.Reason = reason
		return ev
	}

	if item.Expect == "absent" {
		if resolvedStatus == "not_found" {
			ev.Status = "matches"
			return ev
		}
		ev.Status = "differs"
		ev.DifferingFields = []string{"still_present"}
		ev.DisplayLabel = entryDisplayLabel(snap.RawConfig.Servers[srvIdx], snap.RawConfig.Servers[srvIdx].Locations[locIdx])
		return ev
	}

	// Expect == "present"
	if resolvedStatus == "not_found" {
		ev.Status = "not_found"
		ev.Reason = "entry_not_found_in_serving_config"
		return ev
	}

	ev.DisplayLabel = entryDisplayLabel(snap.RawConfig.Servers[srvIdx], snap.RawConfig.Servers[srvIdx].Locations[locIdx])
	cur, _ := s.buildExpectedEntryFacts(snap.RawConfig, snap.EffectiveConfig, srvIdx, locIdx)
	curView := cur
	curView.AccessFingerprint = ""
	ev.Current = &curView

	if item.Expected == nil {
		ev.Status = "matches"
		return ev
	}

	var diffs []string
	omittedSet := make(map[string]bool, len(item.Expected.OmittedFileFacts))
	for _, f := range item.Expected.OmittedFileFacts {
		omittedSet[f] = true
	}

	if !omittedSet["bind_scope"] && item.Expected.BindScope != "" && item.Expected.BindScope != cur.BindScope {
		diffs = append(diffs, "bind_scope")
	}
	if !omittedSet["network_check"] && item.Expected.NetworkCheck != "" && item.Expected.NetworkCheck != cur.NetworkCheck {
		diffs = append(diffs, "network_check")
	}
	if !omittedSet["credential_check"] && item.Expected.CredentialCheck != "" && item.Expected.CredentialCheck != cur.CredentialCheck {
		diffs = append(diffs, "credential_check")
	}
	if !omittedSet["client_certificate"] && item.Expected.ClientCertificate != "" && item.Expected.ClientCertificate != cur.ClientCertificate {
		diffs = append(diffs, "client_certificate")
	}
	if !omittedSet["declared_intent"] && item.Expected.DeclaredIntent != "" && item.Expected.DeclaredIntent != cur.DeclaredIntent {
		diffs = append(diffs, "declared_intent")
	}
	if item.Expected.Withdrawn != nil && cur.Withdrawn != nil && *item.Expected.Withdrawn != *cur.Withdrawn {
		diffs = append(diffs, "withdrawn")
	}
	if item.Expected.AccessFingerprint != "" && item.Expected.AccessFingerprint != cur.AccessFingerprint {
		diffs = append(diffs, "access_fingerprint")
	}

	if len(diffs) > 0 {
		ev.Status = "differs"
		ev.DifferingFields = diffs
		return ev
	}
	ev.Status = "matches"
	return ev
}

func findServingLocationByRouteID(rawCfg *config.Config, id string) (int, int, bool) {
	if rawCfg == nil || id == "" {
		return -1, -1, false
	}
	for i, srv := range rawCfg.Servers {
		for j, loc := range srv.Locations {
			if loc.RouteID != nil && *loc.RouteID == id && !config.ContainsSecretRef(*loc.RouteID) {
				return i, j, true
			}
		}
	}
	return -1, -1, false
}

func resolvePostconditionRef(
	rawCfg *config.Config,
	candidateState string,
	ref SharingEntryRef,
	expect string,
) (srvIdx int, locIdx int, status string, reason string) {
	switch ref.Kind {
	case "route_id":
		if strings.TrimSpace(ref.RouteID) == "" {
			return -1, -1, "unverifiable", "empty_route_id_reference"
		}
		i, j, ok := findServingLocationByRouteID(rawCfg, strings.TrimSpace(ref.RouteID))
		if !ok {
			return -1, -1, "not_found", ""
		}
		return i, j, "found", ""

	case "position":
		if ref.Position == nil {
			return -1, -1, "unverifiable", "missing_position_coordinates"
		}
		// A revision-bound absent entry matches only while the candidate is serving (§9.4).
		if expect == "absent" {
			if candidateState == "serving" {
				return -1, -1, "not_found", ""
			}
			if candidateState == "base_serving" {
				bi := ref.Position.BlockIndex
				li := ref.Position.LocationIndex
				if bi >= 0 && bi < len(rawCfg.Servers) && li >= 0 && li < len(rawCfg.Servers[bi].Locations) {
					return bi, li, "found", ""
				}
			}
			return -1, -1, "unverifiable", "position_bound_to_non_serving_revision"
		}
		// Present position references are bound to the candidate revision and can
		// be resolved only while that revision's digest is serving (§9.4).
		if ref.Position.Revision != "" && ref.Position.Revision != "candidate" {
			return -1, -1, "unverifiable", "position_bound_to_non_candidate_revision"
		}
		if candidateState != "serving" {
			return -1, -1, "unverifiable", "position_bound_to_non_serving_revision"
		}
		bi := ref.Position.BlockIndex
		li := ref.Position.LocationIndex
		if bi < 0 || bi >= len(rawCfg.Servers) || li < 0 || li >= len(rawCfg.Servers[bi].Locations) {
			return -1, -1, "unverifiable", "position_out_of_range"
		}
		return bi, li, "found", ""

	default:
		return -1, -1, "unverifiable", "unknown_reference_kind"
	}
}

func aggregatePostconditionAccess(entries []SharingPostconditionEntryEvaluation) string {
	if len(entries) == 0 {
		return "none_expected"
	}
	hasUnknown := false
	hasDiffers := false
	hasUnverifiable := false
	allMatch := true
	for _, e := range entries {
		switch e.Status {
		case "matches":
		case "unknown":
			hasUnknown = true
			allMatch = false
		case "differs", "not_found":
			hasDiffers = true
			allMatch = false
		case "unverifiable":
			hasUnverifiable = true
			allMatch = false
		default:
			allMatch = false
		}
	}
	switch {
	case hasUnknown:
		return "unknown"
	case allMatch:
		return "matches"
	case hasDiffers:
		return "differs"
	case hasUnverifiable:
		return "unverifiable"
	default:
		return "unknown"
	}
}

func summarizePostconditionEvaluation(
	candidateState, accessState, servingVersion string,
	entries []SharingPostconditionEntryEvaluation,
	repeatable bool,
) (result, candSummary, accessSummary, summary, guidance string) {
	switch candidateState {
	case "serving":
		candSummary = "The exact planned configuration is serving."
	case "base_serving":
		candSummary = "The baseline configuration this change was planned against is still serving."
	case "other_revision":
		candSummary = fmt.Sprintf("A different configuration (%s) is serving.", servingVersion)
	case "unverifiable":
		candSummary = "Jul has restarted since this change was planned, so it cannot compare the candidate digest."
	default:
		candSummary = "Jul cannot tell what configuration is serving."
	}

	switch accessState {
	case "matches":
		accessSummary = "All expected entry access facts and fingerprints match what is serving."
	case "none_expected":
		accessSummary = "This change expected no entry access facts to compare."
	case "differs":
		var names []string
		for _, e := range entries {
			if e.Status == "differs" || e.Status == "not_found" {
				lbl := e.DisplayLabel
				if lbl == "" {
					lbl = e.Ref.String()
				}
				if len(e.DifferingFields) > 0 {
					names = append(names, fmt.Sprintf("%s (%s)", lbl, strings.Join(e.DifferingFields, ", ")))
				} else {
					names = append(names, fmt.Sprintf("%s (not found)", lbl))
				}
			}
		}
		accessSummary = fmt.Sprintf("What is serving differs from the plan for: %s.", strings.Join(names, "; "))
	case "unverifiable":
		var names []string
		for _, e := range entries {
			if e.Status == "unverifiable" {
				lbl := e.DisplayLabel
				if lbl == "" {
					lbl = e.Ref.String()
				}
				names = append(names, fmt.Sprintf("%s (%s)", lbl, e.Reason))
			}
		}
		accessSummary = fmt.Sprintf("Jul cannot compare these entries: %s.", strings.Join(names, "; "))
	default:
		accessSummary = "Jul cannot tell whether the expected access facts match."
	}

	// Precedence table from spec §9.4:
	switch {
	case candidateState == "unknown" || accessState == "unknown":
		result = PostconditionResultUnknown
		summary = "Jul cannot tell what is serving."
	case candidateState == "unverifiable":
		result = PostconditionResultUnverifiable
		summary = "Jul has restarted since this change was planned, so it cannot compare it."
		if repeatable {
			guidance = "Plan the same raw configuration or repeatable operations again, or check the listed entries' current facts and the history by hand."
		} else {
			guidance = "Check the listed entries' current facts and the history by hand; this plan used non-repeatable operations."
		}
	case candidateState == "base_serving":
		result = PostconditionResultNotServingYet
		summary = "Not serving yet: the configuration this change was planned against is still serving."
	case candidateState == "serving" && (accessState == "matches" || accessState == "none_expected"):
		result = PostconditionResultCandidateServing
		if accessState == "matches" {
			summary = "The planned configuration is serving, and its expected access facts match."
		} else {
			summary = "The planned configuration is serving; this change expected no access facts to compare."
		}
	case accessState == "differs":
		result = PostconditionResultDiverged
		summary = "What is serving differs from the plan. " + accessSummary
	case accessState == "unverifiable":
		result = PostconditionResultUnverifiable
		summary = "Jul cannot compare these entries. " + accessSummary
		guidance = "Add a literal route_id in raw TOML for durable cross-revision verification, or inspect the route list and history by hand."
	case candidateState == "other_revision" && accessState == "matches":
		result = PostconditionResultAccessMatches
		summary = fmt.Sprintf("The expected access facts match, but a different configuration (%s) is serving.", servingVersion)
	case candidateState == "other_revision" && accessState == "none_expected":
		result = PostconditionResultOtherRevision
		summary = fmt.Sprintf("A different configuration (%s) is serving; this change expected no access facts to compare.", servingVersion)
	default:
		result = PostconditionResultUnknown
		summary = "Jul cannot tell what is serving."
	}
	return result, candSummary, accessSummary, summary, guidance
}

func buildExternalInputNotes(omittedFacts, extInputs []string, _ bool) []string {
	var notes []string
	for _, f := range omittedFacts {
		switch f {
		case "bind_scope":
			notes = append(notes, "Bind scope comes from a file and was not compared.")
		case "network_check":
			notes = append(notes, "Network CIDR check comes from a file and was not compared.")
		case "client_certificate":
			notes = append(notes, "Client-certificate mode comes from a file and was not compared.")
		case "declared_intent":
			notes = append(notes, "Declared sharing intent comes from a file and was not compared.")
		}
	}
	for _, ext := range extInputs {
		switch ext {
		case ExternalInputJWTKeySet:
			notes = append(notes, "This entry's JWT key set is controlled by its issuer and was not compared.")
		case ExternalInputForwardAuth:
			notes = append(notes, "This entry's forward-auth service decisions are external and were not compared.")
		case ExternalInputProviderPolicy:
			notes = append(notes, "This entry's external provider policy is outside Jul and was not compared.")
		case ExternalInputFileContents:
			notes = append(notes, "This entry's credential or rule file contents were not compared; test the change.")
		}
	}
	return notes
}

func (s *Server) handleSharingPostconditionEvaluate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if id, ok := rbac.IdentityFromContext(r.Context()); ok && !id.Has(rbac.ConfigRead) {
		writeForbidden(w, r, rbac.ConfigRead, id)
		return
	}
	body, err := readBoundedBody(r, 1<<18)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "Invalid postcondition payload."})
		return
	}
	var req SharingPostconditionEvaluateRequest
	if err := decodePatchJSON(strings.NewReader(string(body)), &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "Malformed postcondition JSON: " + err.Error()})
		return
	}
	callerHasRaw := true
	if id, ok := rbac.IdentityFromContext(r.Context()); ok {
		callerHasRaw = id.Has(rbac.ConfigRaw)
	}
	res := s.EvaluatePostcondition(req, callerHasRaw)
	writeJSON(w, http.StatusOK, res)
}
