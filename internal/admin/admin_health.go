// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"net/http"
)

// AdminHealthStatus reports the health of the admin subsystem so that runtime
// overview and readiness probes can surface admin failures as top-level
// degraded state (F-05). Machine-facing reasons are deliberately bounded; raw
// filesystem errors remain in operator logs only.
type AdminHealthStatus struct {
	Healthy bool `json:"healthy"`
	// Reason is a short machine-readable classification: "audit_sink",
	// "admin_reload", or "admin_health".
	Reason string `json:"reason,omitempty"`
	// Detail is suitable for authenticated runtime diagnostics, but public
	// readiness deliberately omits it.
	Detail string `json:"detail,omitempty"`
}

// AdminHealthStatus returns the current admin subsystem health. A configured
// audit sink that cannot persist remains readiness-gating, while candidate
// preparation failures never overwrite the live sink's health.
func (s *Server) AdminHealthStatus() error {
	if s.audit != nil {
		if st := s.audit.statusReport(); st != nil && !st.Healthy {
			detail := "durable audit sink is degraded"
			if st.LastFailureCategory != "" {
				detail += " (" + st.LastFailureCategory + ")"
			}
			return &AdminHealthStatus{Healthy: false, Reason: "audit_sink", Detail: detail}
		}
	}
	if s.deps.AdminHealth != nil {
		if err := s.deps.AdminHealth(); err != nil {
			if status := AsAdminHealthStatus(err); status != nil {
				return err
			}
			return &AdminHealthStatus{Healthy: false, Reason: "admin_health", Detail: err.Error()}
		}
	}
	return nil
}

func AsAdminHealthStatus(err error) *AdminHealthStatus {
	var status *AdminHealthStatus
	if errors.As(err, &status) {
		return status
	}
	return nil
}

func (a *AdminHealthStatus) Error() string {
	if a.Detail != "" {
		return a.Detail
	}
	if a.Reason != "" {
		return "admin subsystem degraded: " + a.Reason
	}
	return "admin subsystem degraded"
}

func (s *Server) adminHealthProjection() *AdminHealthStatus {
	err := s.AdminHealthStatus()
	if err == nil {
		return nil
	}
	if status := AsAdminHealthStatus(err); status != nil {
		return status
	}
	return &AdminHealthStatus{Healthy: false, Reason: "admin_health", Detail: "admin subsystem degraded"}
}

// handleReadyz is intentionally a bounded public surface. It reports a closed
// reason token for admin degradation and never emits raw OS errors, paths,
// certificate material or authenticated diagnostic detail.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ready := true
	if s.deps.Ready != nil {
		ready = s.deps.Ready()
	}
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	if err := s.AdminHealthStatus(); err != nil {
		reason := "admin_health"
		if health := AsAdminHealthStatus(err); health != nil && health.Reason != "" {
			reason = health.Reason
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not ready",
			"reason": reason,
		})
		return
	}
	if s.deps.LoadConfig != nil && s.deps.Certs != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			certs := projectTLS(cfg, s.deps.Certs())
			for _, c := range certs {
				if c.DaysLeft < 0 {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{
						"status": "not ready",
						"reason": "certificate_expired",
					})
					return
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
