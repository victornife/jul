// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"jul/internal/cachemeta"
	"jul/internal/config"
	"jul/internal/server"
	"net/http"
	"strings"
	"testing"
)

func TestCacheCutoverOutcomeSurvivesResponsePollAndAudit(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	for _, effective := range []string{"effective", "incomplete", "uncertain"} {
		t.Run(effective, func(t *testing.T) {
			out := &cachemeta.CutoverResult{State: "succeeded", InvalidationEffective: effective, Operations: 1, RecoveryGuidance: "Configuration is live. Reconcile cache via the versioned purge flow."}
			outcome := server.ReloadAppliedLive
			if effective != "effective" {
				out.State = "partial"
				outcome = server.ReloadAppliedDegraded
				out.FailedEntries = 1
				out.RemainingEntries = 1
			}
			result := ConfigApplyResult{ApplyID: "correlated-cutover", OK: true, Mode: "hot", Persisted: true, Reload: &server.ReloadResult{ID: "correlated-cutover", Outcome: outcome, Published: true, CacheCutover: out}}
			direct := s.v1ConfigApplyResponse(result, http.StatusOK)
			poll := s.applyResultResponse(ManagedApplyRecord{ID: result.ApplyID, State: ManagedApplyTerminal, Result: result})
			if direct.CacheCutoverResult == nil || poll.CacheCutoverResult == nil || direct.CacheCutoverState != out.State || poll.CacheCutoverState != out.State || !direct.OK || !poll.OK {
				t.Fatalf("outcome lost: direct=%+v poll=%+v", direct, poll)
			}
			raw, err := json.Marshal(direct)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "recovery_guidance") {
				t.Fatalf("missing guidance: %s", raw)
			}
			s.RecordManagedApplyOutcome(ApplyRequestContext{Operation: ApplyOperation("config.apply"), Actor: "operator"}, ManagedApplyOutcome{ID: result.ApplyID, OK: true, Outcome: string(outcome), CacheCutover: out})
			events := s.audit.snapshot("", "", 1)
			if len(events) != 1 || !strings.Contains(events[0].Detail, "apply_id=correlated-cutover") || !strings.Contains(events[0].Detail, "invalidation_effective="+effective) {
				t.Fatalf("audit lost correlation/outcome: %+v", events)
			}
			want := "success"
			if effective != "effective" {
				want = "failure"
			}
			if events[0].Result != want {
				t.Fatalf("false clean success: %+v", events[0])
			}
		})
	}
	state, cutover := cacheCutoverOutcome(ConfigApplyResult{OK: true, Mode: "stage_restart"})
	if state != "staged_for_restart" || cutover != nil {
		t.Fatalf("stage falsely claims live cleanup: %s %+v", state, cutover)
	}
	state, cutover = cacheCutoverOutcome(ConfigApplyResult{})
	if state != "" || cutover != nil {
		t.Fatal("unpublished result fabricated cache work")
	}
}

func TestCacheCutoverAuditCoversSourceReloads(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	out := &cachemeta.CutoverResult{State: "uncertain", InvalidationEffective: "uncertain", Operations: 1}
	s.RecordCacheCutoverReload(server.ReloadResult{ID: "sighup-cutover", Source: server.ReloadSourceSIGHUP, Published: true, Outcome: server.ReloadAppliedDegraded, CacheCutover: out})
	events := s.audit.snapshot("config.cache_cutover", "failure", 10)
	if len(events) != 1 || !strings.Contains(events[0].Detail, "published=true") || !strings.Contains(events[0].Detail, "reload_id=sighup-cutover") {
		t.Fatalf("source reload silent: %+v", events)
	}
	s.RecordCacheCutoverReload(server.ReloadResult{CacheCutover: out})
	s.RecordCacheCutoverReload(server.ReloadResult{Published: true})
	s.RecordCacheCutoverReload(server.ReloadResult{Published: true, CacheCutover: &cachemeta.CutoverResult{State: "not_required"}})
	s.RecordCacheCutoverReload(server.ReloadResult{ID: "healthy-cutover", Source: server.ReloadSourceSIGHUP, Published: true, Outcome: server.ReloadAppliedLive, CacheCutover: &cachemeta.CutoverResult{State: "succeeded", InvalidationEffective: "effective"}})
	if len(s.audit.snapshot("config.cache_cutover", "", 10)) != 2 {
		t.Fatal("aborted or no-op generated misleading audit")
	}
	(&Server{}).RecordCacheCutoverReload(server.ReloadResult{Published: true, CacheCutover: out})
}
