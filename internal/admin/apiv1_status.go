// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"time"

	"jul/internal/adminapi"
	"jul/internal/buildcaps"
	"jul/internal/configcontract"
	"jul/internal/server"
)

// handleV1Status serves GET /api/v1/status: the control-plane state of this
// server (ADR 0019 §24).
//
// It reports serving and persisted versions, who owns the configuration and
// why, drift, any staged restart, the last managed transaction, and the boot
// identity that delimits the terminal ledger.
//
// It reports data-plane readiness alongside all of that *precisely so the two
// are not conflated*: drift and a pending restart are control-plane conditions,
// and a data plane that removed itself from a load balancer because somebody
// edited a file would turn a configuration problem into an outage.
func (s *Server) handleV1Status(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}

	authority := s.currentAuthority()
	out := adminapi.StatusResponse{
		APIVersion:      adminapi.APIVersion,
		Ready:           s.dataPlaneReady(),
		ServingVersion:  s.servingVersion(),
		AuthorityState:  authorityState(authority),
		Drift:           driftState(authority),
		PendingRestart:  s.pendingRestartState(),
		LastApply:       s.lastApplySummary(),
		BootID:          s.bootID(),
		LedgerRetention: s.ledgerRetention(),
	}
	// The persisted version is the canonical version of what is on disk, which
	// is what the authority assessment already read. Reading the file a second
	// time here would let the two disagree within one response.
	out.PersistedVersion = authority.DiskVersion

	writeAPIJSON(w, http.StatusOK, out)
}

// handleV1Capabilities serves GET /api/v1/capabilities (ADR 0019 §30).
//
// An external client must not have to infer capability from an error, so this
// reports what the build serves rather than leaving a client to discover it
// from a 404 — which is also why an operation absent from a build answers
// 501 not_implemented naming the capability instead.
func (s *Server) handleV1Capabilities(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}

	authority := s.currentAuthority()
	writeAPIJSON(w, http.StatusOK, adminapi.CapabilitiesResponse{
		APIVersion: adminapi.APIVersion,
		// The configuration schema stays build-independent: a lean binary
		// reports the same schema as a fully tagged one, because a field
		// belonging to an uncompiled feature is present and annotated rather
		// than omitted. API surface availability is the separate question
		// Endpoints answers.
		ConfigSchemaVersion: configcontract.ContractVersion,
		Build:               s.buildCapabilities(),
		Endpoints:           externalEndpoints(),
		AuthorityState:      authorityState(authority),
		BootID:              s.bootID(),
		LedgerRetention:     s.ledgerRetention(),
	})
}

// externalEndpointList is the projection of the route catalog's external
// surface, built once.
//
// It is filled in init() rather than by a package-level initializer or on
// demand, because the Catalog literal holds handler closures that reach this
// projection: anything reachable from Catalog's initializer that also *reads*
// Catalog is an initialization cycle. init() runs after every package variable
// is initialized, and the catalog is immutable afterwards.
var externalEndpointList []adminapi.EndpointAvailability

func init() {
	byPattern := map[string]*adminapi.EndpointAvailability{}
	var order []string
	for _, route := range ExternalRoutes() {
		e, ok := byPattern[route.Pattern]
		if !ok {
			e = &adminapi.EndpointAvailability{
				Path: route.Pattern,
				// Every external operation in this build is served. A
				// capability-gated operation would report false and name the
				// build flag it needs; none is gated today, and reporting a
				// speculative false would be a lie about this binary.
				Available:   true,
				Stability:   route.Stability.String(),
				Permissions: route.Permissions,
				SunsetOn:    route.Sunset,
			}
			byPattern[route.Pattern] = e
			order = append(order, route.Pattern)
		}
		e.Methods = append(e.Methods, route.Method)
	}
	externalEndpointList = make([]adminapi.EndpointAvailability, 0, len(order))
	for _, p := range order {
		externalEndpointList = append(externalEndpointList, *byPattern[p])
	}
}

// externalEndpoints returns a copy of the projection, so a handler cannot hand
// a caller a slice another request could observe being mutated.
func externalEndpoints() []adminapi.EndpointAvailability {
	out := make([]adminapi.EndpointAvailability, len(externalEndpointList))
	copy(out, externalEndpointList)
	return out
}

func authorityState(a ConfigAuthorityStatus) adminapi.AuthorityState {
	return adminapi.AuthorityState{
		ConfigAuthority:          a.Mode,
		ConfigAuthoritySource:    a.Source,
		ConfigState:              a.ConfigState,
		ConfigInconsistentReason: a.InconsistentReason,
	}
}

func driftState(a ConfigAuthorityStatus) adminapi.DriftState {
	return adminapi.DriftState{
		Detected:        a.Drift,
		DetectedAt:      adminapi.Timestamp(a.DriftDetectedAt),
		BaselineVersion: a.BaselineVersion,
		DiskVersion:     a.DiskVersion,
		DiskRawDigest:   a.DiskRawDigest,
		DiskParseError:  a.DiskParseError,
	}
}

// dataPlaneReady answers the same question /readyz answers.
func (s *Server) dataPlaneReady() bool {
	if s.deps.Ready == nil {
		return true
	}
	return s.deps.Ready()
}

// servingVersion is the canonical version of the live runtime, computed from
// the same snapshot the mutation paths use.
func (s *Server) servingVersion() string {
	if s.deps.LiveSnapshot == nil {
		return ""
	}
	snap := s.deps.LiveSnapshot()
	if snap.EffectiveConfig == nil {
		return ""
	}
	return server.CanonicalVersion(snap.EffectiveConfig)
}

func (s *Server) pendingRestartState() adminapi.PendingRestartState {
	if s.deps.PendingRestart == nil {
		return adminapi.PendingRestartState{}
	}
	st := s.deps.PendingRestart()
	if st == nil {
		return adminapi.PendingRestartState{}
	}
	return adminapi.PendingRestartState{
		Pending:          true,
		State:            st.State,
		StagedAt:         st.StagedAt,
		StagedVersion:    st.StagedVersion,
		PersistedVersion: st.PersistedVersion,
		ServingVersion:   st.ServingVersion,
		Subsystems:       st.Subsystems,
		DiscardAvailable: st.DiscardAvailable,
	}
}

// lastApplySummary projects the most recent managed apply. It carries no actor
// and no source address: those stay behind the audit API, which is a different
// permission.
func (s *Server) lastApplySummary() *adminapi.ApplySummary {
	if s.deps.LastManagedApply == nil {
		return nil
	}
	last := s.deps.LastManagedApply()
	if last == nil {
		return nil
	}
	return &adminapi.ApplySummary{
		ApplyID:     last.ID,
		State:       string(ManagedApplyTerminal),
		Outcome:     last.Outcome,
		Mode:        last.Mode,
		CompletedAt: adminapi.Timestamp(last.CompletedAt),
		Degraded:    degradationsOf(last),
	}
}

// degradationsOf maps a terminal outcome's provenance failures onto §33.2's
// closed set. It returns an empty, non-nil slice on a clean success so a client
// can test the array unconditionally rather than checking for the key.
//
// A degradation never upgrades or downgrades the outcome: "did the change take
// effect" and "is anything about this operation unhealthy" are independent, and
// the outcome field above is left exactly as the coordinator reported it.
func degradationsOf(o *ManagedApplyOutcome) []adminapi.Degradation {
	out := []adminapi.Degradation{}
	if o == nil {
		return out
	}
	if o.HistoryError != "" {
		out = append(out, adminapi.Degradation{Kind: "history_error", Message: o.HistoryError})
	}
	if o.FinalizationError != "" {
		out = append(out, adminapi.Degradation{Kind: "finalization_error", Message: o.FinalizationError})
	}
	return out
}

// bootID is this process's apply-instance identity. A changed value tells a
// client its replay window and every idempotency binding are gone
// (ADR 0019 §27.2).
func (s *Server) bootID() string {
	if s.deps.BootID == nil {
		return s.fallbackBootID
	}
	return s.deps.BootID()
}

// ledgerRetention publishes the terminal ledger's bounds, read from the ledger
// itself rather than restated, so the published contract cannot drift from the
// behaviour.
func (s *Server) ledgerRetention() adminapi.LedgerRetention {
	minRecords, minAge := defaultManagedApplyMaxTerminal, defaultManagedApplyTTL
	if s.deps.ManagedApplies != nil {
		minRecords, minAge = s.deps.ManagedApplies.RetentionBounds()
	}
	return adminapi.LedgerRetention{
		MinTerminalRecords: minRecords,
		MinAgeSeconds:      int(minAge.Seconds()),
		Policy:             "evict_after_both",
	}
}

func (s *Server) buildCapabilities() buildcaps.Flags {
	if s.deps.BuildCapabilities == nil {
		return buildcaps.Compiled()
	}
	return s.deps.BuildCapabilities()
}

// newBootID mints a 12-hex-character boot-scoped identifier, matching the
// format the apply coordinator embeds in `rl_<instance>_<seq>` so the two are
// indistinguishable in shape to a client. It is correlation metadata, not a
// secret; the fallback only runs if the OS CSPRNG is unavailable.
func newBootID() string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%d-%d", os.Getpid(), time.Now().UTC().UnixNano()))
	return hex.EncodeToString(sum[:6])
}
