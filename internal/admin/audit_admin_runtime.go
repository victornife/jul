// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"

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
// prepared admin artifact. The immutable request policy and durable audit sink
// generation become live at one publication boundary; the audit swap itself is
// bounded in-memory work and performs no filesystem I/O.
func (s *Server) CommitPreparedAdminRuntime(prepared *PreparedAuth) {
	if prepared == nil {
		return
	}
	if prepared.audit != nil {
		prepared.audit.commit()
	}
	s.CommitPreparedAuth(prepared)
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
	if prepared == nil || prepared.audit == nil {
		return
	}
	prepared.audit.retire(ctx)
}

func auditPrepareFailureCategory(err error) string {
	var pathErr *auditPathError
	if errors.As(err, &pathErr) {
		return string(auditFailurePath)
	}
	return string(auditFailureOpen)
}
