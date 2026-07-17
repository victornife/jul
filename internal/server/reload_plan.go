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
	"jul/internal/upstream"
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
//
// 10. PostCommit — apply dynamic side effects (log level, GOMAXPROCS, stream reload).
//
// On any failure before Publish, Abort must be called to release all candidate
// resources without touching live state.
type ReloadPlan struct {
	s     *Server
	start time.Time

	// rawConfig is the unexpanded configuration loaded from the source. It is
	// preserved inside Candidate on Resolve.
	rawConfig *config.Config

	// Candidate is the single immutable configuration object for this reload
	// transaction. It carries the raw (unexpanded) config, the resolved
	// effective config, the redaction state, and secret digests. Secrets are
	// resolved exactly once per reload (R7-05).
	Candidate *config.Candidate

	// StartupFP is the effective startup fingerprint the candidate is compared
	// against. It is captured from the server at plan creation.
	StartupFP lifecycle.Fingerprint

	// CandidateFP is the effective fingerprint of Candidate.Effective.
	CandidateFP lifecycle.Fingerprint

	// Handlers is the per-listen-address handler tree built by the factory.
	Handlers map[string]http.Handler

	// GenID is the unique generation identifier for redaction registry
	// retirement.
	GenID uint64

	handlerCommit func() (snapshots upstream.SnapshotMap, retirePrev func())
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
		rawConfig:       raw,
		StartupFP:       s.startupFP,
		oldAddrs:        setOf(uniqueListenAddrs(s.cfg.Servers)),
		stagedListeners: make(map[string]*listenerEntry),
	}
}

// Resolve builds the immutable Candidate for this reload and computes the
// candidate fingerprint. Secrets are resolved exactly once here.
func (p *ReloadPlan) Resolve() error {
	candidate, err := config.NewCandidate(p.rawConfig)
	if err != nil {
		return fmt.Errorf("candidate: %w", err)
	}
	p.Candidate = candidate
	p.CandidateFP = lifecycle.ComputeFingerprint(candidate.Effective)
	p.newAddrs = setOf(uniqueListenAddrs(candidate.Effective.Servers))
	return nil
}

// Validate runs the configured runtime validator against the already-resolved
// effective candidate. Validating the same Candidate.Effective that will be
// published guarantees that a secret rotation between resolve and validate
// cannot make validation inspect different bytes from those that are published
// (R6-07, R7-05).
func (p *ReloadPlan) Validate() error {
	if p.s.validate == nil {
		return nil
	}
	if err := p.s.validate(p.Candidate.Effective); err != nil {
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
	if reason, need := p.s.listenerBoundRebindRequired(p.Candidate.Effective); need {
		return fmt.Errorf("restart_required: %s", reason)
	}
	if reason, need := ACMERestartRequired(p.s.cfg.Servers, p.Candidate.Effective.Servers); need {
		return fmt.Errorf("restart_required: %s", reason)
	}
	return nil
}

// Prepare builds the handler tree and stages the upstream/generation resources.
// It does not commit anything; Publish/Abort must follow.
func (p *ReloadPlan) Prepare() error {
	handlers, genID, commit, abort, _, err := p.s.factory(p.Candidate.Effective)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	p.Handlers = handlers
	p.GenID = genID
	p.handlerCommit = commit
	p.handlerAbort = abort
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
		entry, err := p.s.buildListenerEntry(addr, p.Candidate.Effective)
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
	// Commit the staged upstream pools first, then capture the generation-scoped
	// snapshots from the now-live registry. This guarantees that the published
	// handler generation carries the backend view of the configuration it
	// represents, not the previous generation's live state.
	snapshots, retirePrev := p.handlerCommit()
	// Register the new generation's redaction state and install the union of
	// all active generations so in-flight requests on the previous generation
	// continue to have their secrets masked. The previous generation's retire
	// callback will remove its entry from the active set once it drains
	// (R6-06, R7-02).
	p.s.registerRedactionGen(p.GenID, p.Candidate.Redaction)

	p.s.cfg = p.Candidate.Effective
	p.s.rawCfg = p.Candidate.Raw

	prevGen := p.s.handlers.Load()
	p.s.handlers.Store(newHandlerGen(p.Handlers, snapshots, p.GenID))
	if prevGen != nil {
		p.s.retireGen(prevGen, func() {
			if retirePrev != nil {
				retirePrev()
			}
			p.s.retireRedactionGen(prevGen.genID)
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

// RefreshCerts is now a no-op: TLS certificate rotation is restart-only
// (R7-07). The reload plan no longer attempts to refresh certificates.
func (p *ReloadPlan) RefreshCerts() []string {
	return nil
}

// PostCommit applies dynamic side effects that must only run on a committed
// reload: log level, GOMAXPROCS, and stream-proxy reload.
func (p *ReloadPlan) PostCommit() error {
	if p.s.OnReloaded != nil {
		return p.s.OnReloaded(p.Candidate.Effective)
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
