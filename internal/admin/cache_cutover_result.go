// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"jul/internal/cachemeta"
	"jul/internal/server"
	"time"
)

func cacheCutoverOutcome(result ConfigApplyResult) (string, *cachemeta.CutoverResult) {
	if result.Reload != nil && result.Reload.CacheCutover != nil {
		return result.Reload.CacheCutover.State, result.Reload.CacheCutover
	}
	if result.OK && result.Mode == "stage_restart" {
		return "staged_for_restart", nil
	}
	return result.CacheCutoverState, result.CacheCutoverResult
}

// RecordCacheCutoverReload records the publication-correlated cleanup result for
// every reload source. Managed terminal audit separately retains request actor
// attribution; this event also covers file watcher/SIGHUP and queued reloads.
func (s *Server) RecordCacheCutoverReload(r server.ReloadResult) {
	if s.audit == nil || !r.Published || r.CacheCutover == nil {
		return
	}
	out := r.CacheCutover
	if out.State == "not_required" {
		return
	}
	result := "success"
	if out.InvalidationEffective != "effective" {
		result = "failure"
	}
	s.audit.record(AuditEvent{Time: time.Now().UTC(), Actor: "system", Operation: "config.cache_cutover", Resource: "config", Result: result,
		Detail: fmt.Sprintf("reload_id=%s source=%s published=true outcome=%s cache_cutover=%s invalidation_effective=%s failed_entries=%d remaining_entries=%d", r.ID, r.Source.String(), r.Outcome, out.State, out.InvalidationEffective, out.FailedEntries, out.RemainingEntries)})
}
