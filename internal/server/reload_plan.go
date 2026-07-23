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
	// ctx bounds the pre-publish phases. Post-publish work uses the process
	// context because Publish is the point of no return.
	ctx context.Context

	// rawConfig is the unexpanded configuration loaded from the source. It is
	// preserved inside Candidate on Resolve.
	rawConfig *config.Config

	// Candidate is the single immutable configuration object for this reload
	// transaction. It carries the raw (unexpanded) config, the resolved
	// effective config, the redaction state, and secret digests. Secrets are
	// resolved exactly once per reload (R7-05).
	Candidate *config.Candidate
	// PreparedAdmin is built before persistence for managed applies or during
	// Prepare for source-driven reloads, then installed exactly once at Publish.
	PreparedAdmin *PreparedCommit

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

	// published is true once Publish has succeeded; it gates Abort safety.
	published bool
	// phaseDurations records wall-clock time spent in each named phase.
	phaseDurations map[string]time.Duration
}

// newReloadPlan creates a plan for reloading raw (unexpanded) config into s.
// When candidate is non-nil it is used directly and secrets are not resolved
// again; this is the admin apply path that hands off the preflight candidate.
func (s *Server) newReloadPlan(ctx context.Context, raw *config.Config, candidate *config.Candidate, preparedAdmin *PreparedCommit) *ReloadPlan {
	return &ReloadPlan{
		s:               s,
		ctx:             ctx,
		start:           time.Now(),
		rawConfig:       raw,
		Candidate:       candidate,
		PreparedAdmin:   preparedAdmin,
		StartupFP:       s.startupFP,
		oldAddrs:        setOf(uniqueListenAddrs(s.cfg.Servers)),
		stagedListeners: make(map[string]*listenerEntry),
		phaseDurations:  make(map[string]time.Duration),
	}
}

// runPhase executes fn and records its duration under name. It returns early
// with ctx.Err() when the plan context is cancelled before fn runs.
func (p *ReloadPlan) runPhase(name string, fn func() error) error {
	if err := p.ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	start := time.Now()
	err := fn()
	p.phaseDurations[name] = time.Since(start)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return p.ctx.Err()
}

// phaseDurationMS returns the duration of a named phase in milliseconds.
func (p *ReloadPlan) phaseDurationMS(name string) int64 {
	if d, ok := p.phaseDurations[name]; ok {
		return d.Milliseconds()
	}
	return 0
}

// Resolve builds the immutable Candidate for this reload and computes the
// candidate fingerprint. Secrets are resolved exactly once here, unless a
// candidate was already supplied by the admin preflight handoff.
func (p *ReloadPlan) Resolve() error {
	return p.runPhase("resolve", func() error {
		if p.Candidate == nil {
			candidate, err := config.NewCandidate(p.rawConfig)
			if err != nil {
				return fmt.Errorf("candidate: %w", err)
			}
			p.Candidate = candidate
		}
		p.CandidateFP = lifecycle.ComputeFingerprint(p.Candidate.Effective)
		p.newAddrs = setOf(uniqueListenAddrs(p.Candidate.Effective.Servers))
		return nil
	})
}

// Validate runs the configured runtime validator against the already-resolved
// effective candidate. Validating the same Candidate.Effective that will be
// published guarantees that a secret rotation between resolve and validate
// cannot make validation inspect different bytes from those that are published
// (R6-07, R7-05).
func (p *ReloadPlan) Validate() error {
	return p.runPhase("validate", func() error {
		if p.s.validate == nil {
			return nil
		}
		if err := p.s.validate(p.Candidate.Effective); err != nil {
			return fmt.Errorf("validate: %w", err)
		}
		return nil
	})
}

// Lifecycle checks restart-required classification using the effective startup
// and candidate fingerprints, then checks for bind-time listener property
// changes that require a rebind.
func (p *ReloadPlan) Lifecycle() error {
	return p.runPhase("lifecycle", func() error {
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
	})
}

// Prepare builds the handler tree and stages the upstream/generation resources.
// It does not commit anything; Publish/Abort must follow.
func (p *ReloadPlan) Prepare() error {
	return p.runPhase("prepare", func() error {
		if p.PreparedAdmin == nil && p.s.PrepareAdmin != nil {
			prepared, err := p.s.PrepareAdmin(p.Candidate.Effective.Admin)
			if err != nil {
				return fmt.Errorf("prepare admin auth: %w", err)
			}
			p.PreparedAdmin = prepared
		}
		handlers, genID, commit, abort, err := p.s.factory(p.ctx, p.Candidate.Effective)
		if err != nil {
			return fmt.Errorf("build: %w", err)
		}
		p.Handlers = handlers
		p.GenID = genID
		p.handlerCommit = commit
		p.handlerAbort = abort
		return nil
	})
}

// StageListeners binds every newly added listen address without starting the
// accept loop. On any bind failure it records the error and returns a non-nil
// error; successfully staged entries are retained so Abort can close them.
func (p *ReloadPlan) StageListeners() error {
	return p.runPhase("stage_listeners", func() error {
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
	})
}

// Publish promotes the prepared generation to live, installs the new redaction
// state, and atomically publishes the new handler generation and configs. It
// must be called only after Prepare and StageListeners have succeeded. The
// returned retire callback closes the previous generation's resources and must
// be invoked only after that generation has drained.
func (p *ReloadPlan) Publish() (retirePrev func(), err error) {
	if err := p.ctx.Err(); err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	start := time.Now()
	defer func() {
		p.phaseDurations["publish"] = time.Since(start)
		if err == nil {
			p.published = true
		}
	}()
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

	prevGen := p.s.handlers.Load()
	newGen := newHandlerGen(p.Handlers, snapshots, p.GenID)
	p.s.handlers.Store(newGen)

	// Update the authoritative config pointers so code that still reads s.cfg
	// directly sees the new effective config. Then publish the coherent runtime
	// snapshot with the listener set that exists right now; Activate and
	// RetireRemovedListeners will update it again once listener changes settle
	// (R10-02).
	p.s.cfg = p.Candidate.Effective
	p.s.rawCfg = p.Candidate.Raw
	p.PreparedAdmin.Commit()
	p.publishRuntimeState()

	if prevGen != nil {
		genID := prevGen.genID
		p.s.retireGen(prevGen, func() {
			if retirePrev != nil {
				retirePrev()
			}
		}, func() {
			p.s.retireRedactionGen(genID)
		}, func() {
			p.s.retireRedactionForGen(genID)
		})
	} else if retirePrev != nil {
		retirePrev()
	}
	return retirePrev, nil
}

// FinalizeRuntimeState republishes the runtime snapshot after listener
// activation and retirement have settled. This ensures LiveSnapshot reflects
// the actually bound listener set rather than the pre-activation state.
func (p *ReloadPlan) FinalizeRuntimeState() {
	p.publishRuntimeState()
}

// publishRuntimeState captures s.cfg, s.rawCfg, the current handler generation,
// and the currently bound listener set into an immutable runtimeState and
// stores it atomically. Callers must not mutate the configs after publication.
func (p *ReloadPlan) publishRuntimeState() {
	var gen uint64
	if g := p.s.handlers.Load(); g != nil {
		gen = g.genID
	}
	rs := &runtimeState{
		EffectiveConfig: p.s.cfg,
		RawConfig:       p.s.rawCfg,
		Listeners:       p.s.boundListenerSnapshot(),
		Generation:      gen,
	}
	p.s.runtimeState.Store(rs)
}

// Activate releases the staged TCP listeners to accept connections. It must be
// called after Publish so connections draining from the backlog see the new
// handler generation immediately.
func (p *ReloadPlan) Activate() error {
	start := time.Now()
	defer func() { p.phaseDurations["activate"] = time.Since(start) }()
	for _, entry := range p.stagedListeners {
		p.s.startServing(entry)
	}
	return nil
}

// RetireRemovedListeners stops listeners whose addresses are no longer in the
// effective config and removes them from s.listeners.
func (p *ReloadPlan) RetireRemovedListeners() {
	start := time.Now()
	defer func() { p.phaseDurations["retire"] = time.Since(start) }()
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
	start := time.Now()
	defer func() { p.phaseDurations["refresh_certs"] = time.Since(start) }()
	return nil
}

// PostCommit applies dynamic side effects that must only run on a committed
// reload: log level, GOMAXPROCS, RBAC policy update, and stream-proxy reload.
// It returns admin- and stream-specific errors separately so the server can
// report them in distinct ReloadResult subsystems.
func (p *ReloadPlan) PostCommit() (adminErr, streamErr error) {
	start := time.Now()
	defer func() { p.phaseDurations["post_commit"] = time.Since(start) }()
	if p.s.OnReloaded != nil {
		return p.s.OnReloaded(p.Candidate.Effective)
	}
	return nil, nil
}

// Abort releases every candidate resource owned by the plan. It is safe to
// call after any phase before Publish and is a no-op after Publish.
func (p *ReloadPlan) Abort() {
	if p.published {
		return
	}
	if p.handlerAbort != nil {
		p.handlerAbort()
	}
	p.PreparedAdmin.Abort()
	for _, entry := range p.stagedListeners {
		_ = entry.ln.Close()
		if entry.h3 != nil {
			_ = entry.h3.Close(context.Background())
		}
	}
	p.stagedListeners = nil
}
