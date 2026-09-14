// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"

	"jul/internal/config"
)

const adminPrepareFailureAuditSink = "audit_sink_unusable"

func (s *Server) prepareAuditRuntime(cfg config.AdminConfig, prepared *PreparedAuth) error {
	if s == nil || s.audit == nil || prepared == nil {
		return nil
	}
	resolved, err := resolveAuditSinkConfig(cfg.AuditLogFile, cfg.AuditLogRotateMaxMB, cfg.AuditLogRotateKeep)
	if err != nil {
		s.recordAdminPrepareFailure(adminPrepareFailureAuditSink)
		return newAdminRuntimePrepareError(adminPrepareFailureAuditSink, err)
	}
	transition, err := s.audit.prepareTransition(resolved)
	if err != nil {
		s.recordAdminPrepareFailure(adminPrepareFailureAuditSink)
		return newAdminRuntimePrepareError(adminPrepareFailureAuditSink, err)
	}
	prepared.audit = transition
	return nil
}

// CommitPreparedAdminRuntime is the no-fail publication step for the complete
// prepared admin artifact. When the sink changes, the auth/config snapshot is
// published while the audit event linearization lock is held and the sink swap
// immediately follows before that barrier is released. Thus any request that
// observes the new admin snapshot cannot linearize an audit event to the old
// sink. No filesystem operation occurs in this section.
func (s *Server) CommitPreparedAdminRuntime(prepared *PreparedAuth) {
	if prepared == nil {
		return
	}
	commitRuntime := func() {
		// Publish history retention before the immutable admin snapshot that
		// advertises it. This operation cannot fail and performs no deletion.
		if prepared.historyKeepSet && s.hist != nil {
			prepared.historyNeedsPrune = s.hist.setRetention(prepared.historyKeep)
		}
		s.CommitPreparedAuth(prepared)
	}
	if prepared.audit == nil {
		commitRuntime()
		return
	}
	prepared.audit.commitWith(commitRuntime)
}

// AbortPreparedAdminRuntime releases a candidate audit resource without
// touching the live sink or the process-lifetime ring/ID sequence.
func (s *Server) AbortPreparedAdminRuntime(prepared *PreparedAuth) {
	if prepared == nil || prepared.audit == nil {
		return
	}
	prepared.audit.abort()
}

// RetirePreparedAdminRuntime drains only events selected for the previous sink
// before Publish, then releases its physical writer. Failure is advisory: the
// candidate is already committed and is never rolled back here.
func (s *Server) RetirePreparedAdminRuntime(ctx context.Context, prepared *PreparedAuth) {
	if prepared == nil {
		return
	}
	if prepared.historyNeedsPrune && s.hist != nil {
		if err := s.hist.pruneCurrent(); err != nil {
			s.recordHistoryRetentionFailure()
			if s.log != nil {
				s.log.Warn("configuration history retention prune degraded after publish", "error", err)
			}
		} else {
			s.clearHistoryRetentionFailure()
		}
	}
	if prepared.audit != nil {
		prepared.audit.retire(ctx)
	}
}
