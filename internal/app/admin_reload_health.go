// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"sync/atomic"

	"jul/internal/admin"
	"jul/internal/server"
)

// adminReloadHealth preserves a post-Publish admin degradation until another
// published reload repairs it. A pre-Publish failure or semantic no-op is an
// observation, not a repair, and must not clear the state.
type adminReloadHealth struct {
	active  atomic.Bool
	lastErr atomic.Pointer[string]
}

func (h *adminReloadHealth) observe(result server.ReloadResult) {
	if !result.Published {
		return
	}
	if result.Admin.Status == server.ReloadSubsystemFailed || result.Admin.Status == server.ReloadSubsystemTimedOut {
		message := "admin subsystem reload failed"
		if result.Admin.Error != "" {
			message += ": " + result.Admin.Error
		}
		h.lastErr.Store(&message)
		h.active.Store(true)
		return
	}
	h.lastErr.Store(nil)
	h.active.Store(false)
}

func (h *adminReloadHealth) health() error {
	if !h.active.Load() {
		return nil
	}
	detail := "admin subsystem reload failed"
	if message := h.lastErr.Load(); message != nil && *message != "" {
		detail = *message
	}
	return &admin.AdminHealthStatus{Healthy: false, Reason: "admin_reload", Detail: detail}
}
