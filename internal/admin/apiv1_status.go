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
	out.PersistedVersion = authority.DiskVersion
	writeAPIJSON(w, http.StatusOK, out)
}

func (s *Server) handleV1Capabilities(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	authority := s.currentAuthority()
	writeAPIJSON(w, http.StatusOK, adminapi.CapabilitiesResponse{
		APIVersion:          adminapi.APIVersion,
		ConfigSchemaVersion: configcontract.ContractVersion,
		Build:               s.buildCapabilities(),
		Endpoints:           externalEndpoints(),
		ExitCodes:           adminapi.ExitCodes(),
		AuthorityState:      authorityState(authority),
		BootID:              s.bootID(),
		LedgerRetention:     s.ledgerRetention(),
	})
}

var externalEndpointList []adminapi.EndpointAvailability

func init() {
	byPattern := map[string]*adminapi.EndpointAvailability{}
	var order []string
	for _, route := range ExternalRoutes() {
		e, ok := byPattern[route.Pattern]
		if !ok {
			e = &adminapi.EndpointAvailability{
				Path:        route.Pattern,
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

func (s *Server) dataPlaneReady() bool {
	if s.deps.Ready == nil {
		return true
	}
	return s.deps.Ready()
}

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

func (s *Server) bootID() string {
	if s.deps.BootID == nil {
		return s.fallbackBootID
	}
	return s.deps.BootID()
}

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

func newBootID() string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%d-%d", os.Getpid(), time.Now().UTC().UnixNano()))
	return hex.EncodeToString(sum[:6])
}
