// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"net/http"
)

// AdminHealthStatus reports the health of the admin subsystem so that runtime
// overview and readiness probes can surface admin failures as top-level
// degraded state (F-05). It carries the audit-sink status plus any additional
// composition-root-level degradation reported through Deps.AdminHealth.
type AdminHealthStatus struct {
	// Healthy is true when no admin subsystem failure is active.
	Healthy bool `json:"healthy"`
	// Reason is a short machine-readable classification: "audit_sink",
	// "admin_reload", or "admin_health".
	Reason string `json:"reason,omitempty"`
	// Detail is a human-readable explanation of the degradation.
	Detail string `json:"detail,omitempty"`
}

// AdminHealthStatus returns the current admin subsystem health. It checks the
// durable audit sink and any composition-root health hook. A non-nil error is
// returned when the subsystem is degraded; the error can be cast to an
// AdminHealthStatus via AsAdminHealthStatus when a structured reason is needed.
func (s *Server) AdminHealthStatus() error {
	if s.audit != nil {
		if st := s.audit.statusReport(); st != nil && !st.Healthy {
			return &AdminHealthStatus{
				Healthy: false,
				Reason:  "audit_sink",
				Detail:  "durable audit sink is degraded: " + st.Error,
			}
		}
	}
	if s.deps.AdminHealth != nil {
		if err := s.deps.AdminHealth(); err != nil {
			if status := AsAdminHealthStatus(err); status != nil {
				return err
			}
			return &AdminHealthStatus{
				Healthy: false,
				Reason:  "admin_health",
				Detail:  err.Error(),
			}
		}
	}
	return nil
}

// AsAdminHealthStatus extracts an AdminHealthStatus from an error. It returns
// nil if the error is not an *AdminHealthStatus.
func AsAdminHealthStatus(err error) *AdminHealthStatus {
	var status *AdminHealthStatus
	if errors.As(err, &status) {
		return status
	}
	return nil
}

// Error implements the error interface so AdminHealthStatus can be returned
// from Deps.AdminHealth and from Server.AdminHealthStatus.
func (a *AdminHealthStatus) Error() string {
	if a.Detail != "" {
		return a.Detail
	}
	if a.Reason != "" {
		return "admin subsystem degraded: " + a.Reason
	}
	return "admin subsystem degraded"
}

// adminHealthProjection returns a value suitable for JSON serialization in the
// runtime overview. It returns nil when healthy so the field is omitted from
// the overview when there is no degradation.
func (s *Server) adminHealthProjection() *AdminHealthStatus {
	err := s.AdminHealthStatus()
	if err == nil {
		return nil
	}
	if status := AsAdminHealthStatus(err); status != nil {
		return status
	}
	return &AdminHealthStatus{
		Healthy: false,
		Reason:  "admin_health",
		Detail:  err.Error(),
	}
}

// handleReadyz reports readiness to serve traffic.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ready := true
	if s.deps.Ready != nil {
		ready = s.deps.Ready()
	}
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	// Readiness gate: admin subsystem failures degrade readiness (F-05).
	if err := s.AdminHealthStatus(); err != nil {
		code := http.StatusServiceUnavailable
		if health := AsAdminHealthStatus(err); health != nil {
			writeJSON(w, code, map[string]any{
				"status": "not ready",
				"reason": health.Reason,
				"detail": health.Detail,
			})
			return
		}
		writeJSON(w, code, map[string]string{
			"status": "not ready",
			"reason": err.Error(),
		})
		return
	}
	// Readiness gate: any expired certificate prevents traffic serving.
	if s.deps.LoadConfig != nil && s.deps.Certs != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			certs := projectTLS(cfg, s.deps.Certs())
			for _, c := range certs {
				if c.DaysLeft < 0 {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{
						"status": "not ready",
						"reason": "certificate expired for " + c.ServerNames[0],
					})
					return
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
