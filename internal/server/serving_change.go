// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"reflect"
	"strings"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

// ServingChangeEvidence is a bounded explanation of a serving-change
// assessment. It is deliberately internal operational evidence: configured
// paths, values, and resource fingerprints never leave the process.
type ServingChangeEvidence string

const (
	ServingEffectiveEqual       ServingChangeEvidence = "effective_equal"
	ServingIgnoredOnly          ServingChangeEvidence = "ignored_only"
	ServingConfigChanged        ServingChangeEvidence = "config_changed"
	ServingRuntimeInputChanged  ServingChangeEvidence = "runtime_resource_changed"
	ServingUnknownExternalInput ServingChangeEvidence = "unknown_external_input"
)

// ServingChangeAssessment reports whether the candidate is proven to leave
// the serving runtime unchanged. NoChange is intentionally proof-based: false
// means either a real change or uncertainty, both of which take the normal
// reload path.
type ServingChangeAssessment struct {
	NoChange bool
	Evidence ServingChangeEvidence
}

// AssessServingChange composes the authoritative lifecycle classification with
// the live identities of resources whose bytes can change independently of the
// configuration. It runs after resolution, validation, and restart checks and
// before any generation-owned resource is prepared.
func (p *ReloadPlan) AssessServingChange() error {
	return p.runPhase("change_assessment", func() error {
		live := p.s.LiveSnapshot()
		addrs := make([]string, 0, len(live.Listeners))
		for addr := range live.Listeners {
			addrs = append(addrs, addr)
		}
		classified, err := lifecycle.Classify(live.EffectiveConfig, p.Candidate.Effective, lifecycle.Live{
			BoundHTTPAddrs: addrs,
		})
		if err != nil {
			return err
		}

		limitsOnly := inspectionLimitsOnlyChanges(classified.Changes) &&
			inspectionLimitsOnlyConfig(live.EffectiveConfig, p.Candidate.Effective)
		for _, change := range classified.Changes {
			if !change.Ignored && !limitsOnly {
				p.ServingChange = ServingChangeAssessment{Evidence: ServingConfigChanged}
				return nil
			}
		}
		if !limitsOnly && (!servingServersEqual(live.EffectiveConfig, p.Candidate.Effective) ||
			!servingBackendsEqual(live.EffectiveConfig, p.Candidate.Effective)) {
			p.ServingChange = ServingChangeAssessment{Evidence: ServingConfigChanged}
			return nil
		}

		// A serving no-op exists only relative to an installed generation. This
		// also keeps direct-call tests and partially initialized servers from
		// treating an absent runtime as equivalent to a candidate.
		if p.s.handlers.Load() == nil {
			p.ServingChange = ServingChangeAssessment{Evidence: ServingUnknownExternalInput}
			return nil
		}

		if !p.s.staticCertificatesUnchanged(p.Candidate.Effective) {
			p.ServingChange = ServingChangeAssessment{Evidence: ServingRuntimeInputChanged}
			return nil
		}
		if !p.s.clientAuthUnchanged(p.Candidate.Effective) {
			p.ServingChange = ServingChangeAssessment{Evidence: ServingRuntimeInputChanged}
			return nil
		}
		if !p.s.adminTLSInputsUnchanged(p.Candidate.Effective.Admin) {
			p.ServingChange = ServingChangeAssessment{Evidence: ServingRuntimeInputChanged}
			return nil
		}
		if !p.s.adminRuntimeHealthy(p.Candidate.Effective.Admin) {
			p.ServingChange = ServingChangeAssessment{Evidence: ServingRuntimeInputChanged}
			return nil
		}
		if evidence, proven := p.s.pluginModulesUnchanged(p.Candidate.Effective.Plugins); !proven {
			p.ServingChange = ServingChangeAssessment{Evidence: evidence}
			return nil
		}
		if hasOpaqueReloadInputs(p.Candidate.Effective) {
			p.ServingChange = ServingChangeAssessment{Evidence: ServingUnknownExternalInput}
			return nil
		}

		if limitsOnly {
			if p.s.Inspection == nil {
				p.ServingChange = ServingChangeAssessment{Evidence: ServingConfigChanged}
				return nil
			}
			snapshot := p.s.Inspection.Snapshot()
			p.inspectionLimitsOnly = true
			p.inspectionLimitsVersion = snapshot.LimitsVersion
			p.inspectionGeneration = live.Generation
			p.ServingChange = ServingChangeAssessment{Evidence: ServingConfigChanged}
			return nil
		}

		evidence := ServingEffectiveEqual
		if len(classified.Changes) > 0 {
			evidence = ServingIgnoredOnly
		}
		p.ServingChange = ServingChangeAssessment{NoChange: true, Evidence: evidence}
		return nil
	})
}

func inspectionLimitsOnlyChanges(changes []lifecycle.Change) bool {
	if len(changes) == 0 {
		return false
	}
	for _, change := range changes {
		if change.Ignored || !isInspectionLimitPath(change.Path) {
			return false
		}
	}
	return true
}

func inspectionLimitsOnlyConfig(current, candidate *config.Config) bool {
	if current == nil || candidate == nil {
		return false
	}
	// Keyed lifecycle extraction can hide meaningful route order or duplicates.
	normalized := *candidate
	normalized.Global.Inspection.Mode = current.Global.Inspection.Mode
	normalized.Global.Inspection.MaxBufferedBytes = current.Global.Inspection.MaxBufferedBytes
	normalized.Global.Inspection.MaxGuestCapacityBytes = current.Global.Inspection.MaxGuestCapacityBytes
	normalized.Global.Inspection.MaxResponseHeaderBytes = current.Global.Inspection.MaxResponseHeaderBytes
	normalized.Global.Inspection.MaxConcurrentEvaluations = current.Global.Inspection.MaxConcurrentEvaluations
	normalized.Global.Inspection.EvaluationQueueTimeout = current.Global.Inspection.EvaluationQueueTimeout
	normalized.Global.Inspection.BodyTimeout = current.Global.Inspection.BodyTimeout
	return reflect.DeepEqual(current, &normalized)
}

// Lifecycle's keyed extraction can collapse duplicate entries and lose route order.
func servingServersEqual(current, candidate *config.Config) bool {
	if current == nil || candidate == nil || len(current.Servers) != len(candidate.Servers) {
		return false
	}
	for i := range current.Servers {
		normalized := candidate.Servers[i]
		normalized.AccessLog = current.Servers[i].AccessLog
		normalized.ErrorLog = current.Servers[i].ErrorLog
		if !reflect.DeepEqual(current.Servers[i], normalized) {
			return false
		}
	}
	return true
}

func servingBackendsEqual(current, candidate *config.Config) bool {
	if current == nil || candidate == nil || len(current.Upstreams) != len(candidate.Upstreams) {
		return false
	}
	byName := make(map[string][]config.UpstreamServer, len(current.Upstreams))
	for _, pool := range current.Upstreams {
		if _, exists := byName[pool.Name]; exists {
			return false
		}
		byName[pool.Name] = pool.Servers
	}
	for _, pool := range candidate.Upstreams {
		before, exists := byName[pool.Name]
		if !exists || len(before) != len(pool.Servers) {
			return false
		}
		delete(byName, pool.Name)
		for i, server := range pool.Servers {
			if before[i] != server {
				return false
			}
		}
	}
	return len(byName) == 0
}

func isInspectionLimitPath(path string) bool {
	switch path {
	case "global.inspection.mode",
		"global.inspection.max_buffered_bytes",
		"global.inspection.max_guest_capacity_bytes",
		"global.inspection.max_response_header_bytes",
		"global.inspection.max_concurrent_evaluations",
		"global.inspection.evaluation_queue_timeout",
		"global.inspection.body_timeout":
		return true
	default:
		return false
	}
}

// staticCertificatesUnchanged compares candidate file content with the
// identity captured when each retained listener installed its provider. A
// same-path certificate/key rotation is therefore a real reload.
func (s *Server) staticCertificatesUnchanged(next *config.Config) bool {
	for _, addr := range uniqueListenAddrs(next.Servers) {
		bindings, _, tlsOK := tlsBindingsForAddr(next.Servers, addr)
		if !tlsOK || acmeEnabledForAddr(next.Servers, addr) {
			continue
		}
		s.mu.Lock()
		entry := s.listeners[addr]
		s.mu.Unlock()
		if entry == nil || entry.provider == nil {
			return false
		}
		if tlsIdentityFingerprint(bindings) != entry.certFingerprint {
			return false
		}
	}
	return true
}

// adminTLSInputsUnchanged delegates comparison to the admin listener, which
// owns the installed certificate fingerprint. Without that proof the reload
// proceeds normally.
func (s *Server) adminTLSInputsUnchanged(cfg config.AdminConfig) bool {
	if !cfg.Enabled || cfg.TLS == nil || !cfg.TLS.Enabled {
		return true
	}
	return s.AdminTLSInputsUnchanged != nil && s.AdminTLSInputsUnchanged(cfg)
}

// adminRuntimeHealthy delegates to the admin listener, which owns the live
// audit-sink and plugin-upload-directory health. It is only consulted when
// cfg actually configures one of those resources; otherwise there is nothing
// whose live state could have diverged from configuration, and the normal
// admin-TLS/opaque-input checks already cover everything else PrepareAdmin
// would touch.
func (s *Server) adminRuntimeHealthy(cfg config.AdminConfig) bool {
	if !adminRuntimeNeedsHealthProof(cfg) {
		return true
	}
	return s.AdminRuntimeHealthy != nil && s.AdminRuntimeHealthy(cfg)
}

// adminRuntimeNeedsHealthProof reports whether cfg configures an admin
// runtime resource whose usability can change independently of configuration
// content: a durable audit sink (its target path can become writable or
// unwritable without any config edit) or the plugin-upload directory (its
// permissions can change the same way).
func adminRuntimeNeedsHealthProof(cfg config.AdminConfig) bool {
	if !cfg.Enabled {
		return false
	}
	if strings.TrimSpace(cfg.AuditLogFile) != "" {
		return true
	}
	uploadEnabled := cfg.PluginUploadEnabled == nil || *cfg.PluginUploadEnabled
	return uploadEnabled && cfg.PluginUploadMaxSize > 0
}

// pluginModulesUnchanged proves that no WASM module's executable bytes changed
// behind an unchanged declaration (#429): a path is not a content identity.
// Without the proof hook, inline modules are still proven by configuration
// equality, but a path-backed module is opaque and fails closed.
func (s *Server) pluginModulesUnchanged(plugins map[string]config.PluginConfig) (ServingChangeEvidence, bool) {
	if len(plugins) == 0 {
		return "", true
	}
	if s.PluginModulesUnchanged == nil {
		for _, plugin := range plugins {
			if plugin.Path != "" {
				return ServingUnknownExternalInput, false
			}
		}
		return "", true
	}
	if !s.PluginModulesUnchanged(plugins) {
		return ServingRuntimeInputChanged, false
	}
	return "", true
}

// hasOpaqueReloadInputs identifies resources whose current installed content
// identity is not exposed to the reload coordinator. Presence is enough to
// fail closed: their normal reload remains the supported way to pick up an
// in-place external change.
func hasOpaqueReloadInputs(c *config.Config) bool {
	for i := range c.Upstreams {
		up := &c.Upstreams[i]
		if backendTLSHasOpaqueInputs(up.BackendTLS) {
			return true
		}
		if up.Discovery != nil {
			if consul := up.Discovery.Consul; consul != nil {
				if backendTLSHasOpaqueInputs(consul.TLS) || strings.HasPrefix(strings.ToLower(strings.TrimSpace(consul.Address)), "https://") {
					return true
				}
			}
			// Kubernetes can take its API address, token, and CA from
			// process/mounted in-cluster inputs even when every override is empty.
			if up.Discovery.Kubernetes != nil {
				return true
			}
		}
	}
	for i := range c.Servers {
		for j := range c.Servers[i].Locations {
			loc := &c.Servers[i].Locations[j]
			if loc.Root != "" || loc.GRPCTranscode != nil || backendTLSHasOpaqueInputs(loc.BackendTLS) {
				return true
			}
			// A TLS transport is generation-owned even without an explicit
			// backend_tls block. The normal path retires its connection pool and
			// obtains fresh peer/system-trust evidence; a no-op must not hide that
			// current reload behavior.
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(loc.ProxyPass)), "https://") {
				return true
			}
			if loc.Auth != nil && loc.Auth.Basic != nil && loc.Auth.Basic.File != "" {
				return true
			}
			if loc.WAF != nil && wafHasOpaqueInputs(*loc.WAF) {
				return true
			}
		}
	}
	return wafHasOpaqueInputs(c.WAF)
}

// wafFileTokens are the SecLang constructs that make the parser read a file:
// Include, @pmFromFile/@pmf, @ipMatchFromFile/@ipMatchF and @validateSchema.
var wafFileTokens = []string{"include", "fromfile", "@pmf", "@ipmatchf", "validateschema"}

// wafHasOpaqueInputs reports a WAF policy whose compiled rules depend on files
// on disk: rule files, or inline rules that include or load one (#440).
func wafHasOpaqueInputs(w config.WAFConfig) bool {
	if len(w.DirectivesFiles) > 0 {
		return true
	}
	inline := strings.ToLower(w.InlineRules)
	for _, tok := range wafFileTokens {
		if strings.Contains(inline, tok) {
			return true
		}
	}
	return false
}

func backendTLSHasOpaqueInputs(tls *config.BackendTLSConfig) bool {
	// Even a block without explicit files may resolve platform roots while the
	// handler generation is prepared. Until the installed policy exposes a
	// complete content fingerprint, preserve the normal reload conservatively.
	return tls != nil
}
