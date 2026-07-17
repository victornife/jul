// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
)

// ReloadPlan is the single transaction object for a configuration reload. It
// owns every piece of candidate state from the moment the source config is
// loaded until it is either applied or aborted, so no live serving state is
// mutated during validation, lifecycle checks, or preparation.
//
// The planned phases are:
//
//  1. Resolve   — expand secrets, compute effective config + redaction state + candidate fingerprint.
//  2. Validate  — run structural/runtime validation on the raw source config.
//  3. Lifecycle — compare the candidate fingerprint against the startup fingerprint.
//  4. Prepare   — build handlers and stage the upstream/generation resources.
//  5. StageListeners — bind new TCP listeners (and HTTP/3 resources) without serving.
//  6. Publish   — commit handler generation, install redaction, swap configs and handler pointer.
//  7. Activate  — start serving on staged listeners.
//  8. Retire    — remove listeners no longer in the config and retire old handler generation.
//  9. Refresh   — reload TLS certificates.
// 10. PostCommit — apply dynamic side effects (log level, GOMAXPROCS, stream reload).
//
// On any failure before Publish, Abort must be called to release all candidate
// resources without touching live state.
type ReloadPlan struct {
	s     *Server
	start time.Time

	// RawConfig is the unexpanded configuration loaded from the source.
	RawConfig *config.Config

	// EffectiveConfig is a deep clone of RawConfig with all secret references
	// resolved. It becomes the new serving effective config on Publish.
	EffectiveConfig *config.Config

	// Redaction is the self-contained redaction state covering every secret
	// consumed by EffectiveConfig. It is installed as the live global state on
	// Publish and only on Publish.
	Redaction redact.State

	// SecretDigests maps each secret reference string to a digest of the bytes
	// actually consumed, so file-content rotation can be detected even when the
	// configured path is unchanged.
	SecretDigests map[string]string

	// StartupFP is the effective startup fingerprint the candidate is compared
	// against. It is captured from the server at plan creation.
	StartupFP lifecycle.Fingerprint

	// CandidateFP is the effective fingerprint of EffectiveConfig.
	CandidateFP lifecycle.Fingerprint

	// Handlers is the per-listen-address handler tree built by the factory.
	Handlers map[string]http.Handler

	handlerCommit func() func()
	handlerAbort  func()

	oldAddrs        map[string]struct{}
	newAddrs        map[string]struct{}
	stagedListeners map[string]*listenerEntry
	bindErrs        []string
}

// newReloadPlan creates a plan for reloading raw (unexpanded) config into s.
func (s *Server) newReloadPlan(raw *config.Config) *ReloadPlan {
	return &ReloadPlan{
		s:               s,
		start:           time.Now(),
		RawConfig:       raw,
		StartupFP:       s.startupFP,
		oldAddrs:        setOf(uniqueListenAddrs(s.cfg.Servers)),
		stagedListeners: make(map[string]*listenerEntry),
	}
}

// Resolve expands secret references into a deep-copied effective config and
// computes the candidate fingerprint. It performs no global mutation.
func (p *ReloadPlan) Resolve() error {
	clone, err := p.RawConfig.Clone()
	if err != nil {
		return fmt.Errorf("clone config: %w", err)
	}
	expanded, state, digests, err := config.Resolve(clone)
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	p.EffectiveConfig = expanded
	p.Redaction = state
	p.SecretDigests = digests
	p.CandidateFP = lifecycle.ComputeFingerprint(expanded)
	p.newAddrs = setOf(uniqueListenAddrs(expanded.Servers))
	return nil
}

// Validate runs the configured runtime validator against the already-resolved
// effective candidate. Validating the same EffectiveConfig that will be
// published guarantees that a secret rotation between resolve and validate
// cannot make validation inspect different bytes from those that are published
// (R6-07).
func (p *ReloadPlan) Validate() error {
	if p.s.validate == nil {
		return nil
	}
	if err := p.s.validate(p.EffectiveConfig); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	return nil
}

// Lifecycle checks restart-required classification using the effective startup
// and candidate fingerprints, then checks for bind-time listener property
// changes that require a rebind.
func (p *ReloadPlan) Lifecycle() error {
	if reason, need := lifecycle.RestartRequired(p.StartupFP, p.CandidateFP); need {
		return fmt.Errorf("restart_required: %s", reason)
	}
	if reason, need := p.s.listenerBoundRebindRequired(p.EffectiveConfig); need {
		return fmt.Errorf("restart_required: %s", reason)
	}
	return nil
}

// Prepare builds the handler tree and stages the upstream/generation resources.
// It does not commit anything; Publish/Abort must follow.
func (p *ReloadPlan) Prepare() error {
	handlers, commit, abort, state, err := p.s.factory(p.EffectiveConfig)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	p.Handlers = handlers
	p.handlerCommit = commit
	p.handlerAbort = abort
	// The factory is authoritative for the redaction state it resolved while
	// mutating EffectiveConfig in place. Prefer its state when non-empty.
	if state.Count() > 0 || p.Redaction.Count() == 0 {
		p.Redaction = state
	}
	return nil
}

// StageListeners binds every newly added listen address without starting the
// accept loop. On any bind failure it records the error and returns a non-nil
// error; successfully staged entries are retained so Abort can close them.
func (p *ReloadPlan) StageListeners() error {
	for addr := range p.newAddrs {
		if _, existed := p.oldAddrs[addr]; existed {
			continue
		}
		entry, err := p.s.buildListenerEntry(addr, p.EffectiveConfig)
		if err != nil {
			p.bindErrs = append(p.bindErrs, addr+": "+err.Error())
			p.s.log.Error("reload: failed to stage new listener", "addr", addr, "error", err)
			continue
		}
		p.stagedListeners[addr] = entry
		p.s.log.Debug("reload: staged new listener (not yet serving)", "addr", addr)
	}
	if len(p.bindErrs) > 0 {
		return fmt.Errorf("bind: %s", strings.Join(p.bindErrs, "; "))
	}
	return nil
}

// Publish promotes the prepared generation to live, installs the new redaction
// state, and atomically publishes the new handler generation and configs. It
// must be called only after Prepare and StageListeners have succeeded. The
// returned retire callback closes the previous generation's resources and must
// be invoked only after that generation has drained.
func (p *ReloadPlan) Publish() (retirePrev func(), err error) {
	retirePrev = p.handlerCommit()
	// Install the union of the current and new redaction states so that
	// in-flight requests on the previous generation continue to have their
	// secrets masked. The previous generation's retire callback will prune the
	// old-only secrets once it has drained (R6-06).
	merged := redact.Global().Union(p.Redaction)
	redact.Install(merged)

	p.s.cfg = p.EffectiveConfig
	p.s.rawCfg = p.RawConfig

	prevGen := p.s.handlers.Load()
	p.s.handlers.Store(newHandlerGen(p.Handlers))
	if prevGen != nil {
		p.s.retireGen(prevGen, func() {
			if retirePrev != nil {
				retirePrev()
			}
			redact.Install(p.Redaction)
		})
	} else if retirePrev != nil {
		retirePrev()
	}
	return retirePrev, nil
}

// Activate releases the staged TCP listeners to accept connections. It must be
// called after Publish so connections draining from the backlog see the new
// handler generation immediately.
func (p *ReloadPlan) Activate() error {
	for _, entry := range p.stagedListeners {
		p.s.startServing(entry)
	}
	return nil
}

// RetireRemovedListeners stops listeners whose addresses are no longer in the
// effective config and removes them from s.listeners.
func (p *ReloadPlan) RetireRemovedListeners() {
	for addr := range p.oldAddrs {
		if _, kept := p.newAddrs[addr]; !kept {
			p.s.removeListener(addr)
			p.s.log.Info("reload: removed listener", "addr", addr)
		}
	}
}

// RefreshCerts reloads TLS certificates for listeners that remain TLS-enabled.
// Errors are returned as a slice of address-qualified strings; they degrade the
// reload result but do not roll back the published config.
func (p *ReloadPlan) RefreshCerts() []string {
	return p.s.reloadCertificates()
}

// PostCommit applies dynamic side effects that must only run on a committed
// reload: log level, GOMAXPROCS, and stream-proxy reload.
func (p *ReloadPlan) PostCommit() error {
	if p.s.OnReloaded != nil {
		return p.s.OnReloaded(p.EffectiveConfig)
	}
	return nil
}

// Abort releases every candidate resource owned by the plan. It is safe to
// call after any phase before Publish and is a no-op after Publish.
func (p *ReloadPlan) Abort() {
	if p.handlerAbort != nil {
		p.handlerAbort()
	}
	for _, entry := range p.stagedListeners {
		_ = entry.ln.Close()
		if entry.h3 != nil {
			_ = entry.h3.Close(context.Background())
		}
	}
	p.stagedListeners = nil
}
