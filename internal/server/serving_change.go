// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
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

		for _, change := range classified.Changes {
			if !change.Ignored {
				p.ServingChange = ServingChangeAssessment{Evidence: ServingConfigChanged}
				return nil
			}
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
		if !p.s.adminTLSInputsUnchanged(p.Candidate.Effective.Admin) {
			p.ServingChange = ServingChangeAssessment{Evidence: ServingRuntimeInputChanged}
			return nil
		}
		if hasOpaqueReloadInputs(p.Candidate.Effective) {
			p.ServingChange = ServingChangeAssessment{Evidence: ServingUnknownExternalInput}
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

// hasOpaqueReloadInputs identifies resources whose current installed content
// identity is not exposed to the reload coordinator. Presence is enough to
// fail closed: their normal reload remains the supported way to pick up an
// in-place external change.
func hasOpaqueReloadInputs(c *config.Config) bool {
	for _, plugin := range c.Plugins {
		if plugin.Path != "" {
			return true
		}
	}
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
			if loc.WAF != nil && len(loc.WAF.DirectivesFiles) > 0 {
				return true
			}
		}
	}
	return len(c.WAF.DirectivesFiles) > 0
}

func backendTLSHasOpaqueInputs(tls *config.BackendTLSConfig) bool {
	// Even a block without explicit files may resolve platform roots while the
	// handler generation is prepared. Until the installed policy exposes a
	// complete content fingerprint, preserve the normal reload conservatively.
	return tls != nil
}
