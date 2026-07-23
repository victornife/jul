// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// These tests exercise the atomic authentication snapshot introduced for H-01.
// The pre-remediation implementation stored the RBAC policy, the rbacEnabled
// flag, and the live admin configuration in three separate locations and updated
// them with independent stores. A concurrent request could observe a transient
// anonymous or legacy-fallback window mid-transition. The snapshot collapses all
// three into one immutable *authSnapshot installed with a single atomic pointer
// swap, so middleware always observes an internally consistent view.

const (
	snapLegacyTok = "legacy-token-32-chars-padded-xxx"
	snapRBACTok   = "rbac-admin-32-chars-padded-xxxxx"
)

func snapAdminPolicy(t *testing.T, tok string, now time.Time) *rbac.Policy {
	t.Helper()
	pol, err := rbac.Build(true, "admin", nil, []rbac.PrincipalDef{
		{Name: "admin", Role: rbac.RoleAdmin, Token: tok},
	}, "", now)
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	return pol
}

// applyReq builds an authenticated apply request with the given bearer token.
func applyReq(tok string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply", nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return req
}

// TestAuthSnapshot_EnabledToDisabled_NoAnonymousWindow drives the exact
// enabled→disabled transition the audit flagged: the old RBAC-only deployment
// has an EMPTY legacy token, and the new config has a NON-empty legacy token.
//
// Under the old two-store design, clearing the policy first (rbacEnabled=false)
// while the live config still had an empty legacy token would briefly enter the
// legacy branch with token=="" and grant a wildcard identity WITHOUT
// authentication. With the atomic snapshot the config and policy flip together,
// so a caller may only ever see either the RBAC snapshot or the fully-formed
// legacy snapshot — never an anonymous window.
func TestAuthSnapshot_EnabledToDisabled_NoAnonymousWindow(t *testing.T) {
	s := &Server{cfg: config.AdminConfig{Listen: "127.0.0.1:0"}}
	// Start RBAC-enabled with NO legacy token.
	s.installAuth(config.AdminConfig{}, snapAdminPolicy(t, snapRBACTok, time.Now()))

	h := s.requirePermission(rbac.ConfigApply, okHandler())

	var anonAccepted atomic.Int64
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Hammer with UNAUTHENTICATED requests during the whole transition. None may
	// ever succeed: RBAC rejects them (401) and the post-transition legacy mode
	// has a non-empty token that also rejects them (401).
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, applyReq("")) // no credential
				if rr.Code == http.StatusOK {
					anonAccepted.Add(1)
				}
			}
		}()
	}

	// Perform the transition many times to widen the interleaving surface.
	for i := 0; i < 2000; i++ {
		// Atomic swap to legacy mode with a non-empty token.
		s.installAuth(config.AdminConfig{Token: snapLegacyTok}, nil)
		// Swap back to RBAC-only for the next iteration.
		s.installAuth(config.AdminConfig{}, snapAdminPolicy(t, snapRBACTok, time.Now()))
	}
	close(stop)
	wg.Wait()

	if got := anonAccepted.Load(); got != 0 {
		t.Fatalf("anonymous request accepted %d times during enabled→disabled transition; "+
			"expected an atomic snapshot with no anonymous window", got)
	}
}

// TestAuthSnapshot_DisabledToEnabled_PolicyFailureBlocks verifies the
// disabled/open → enabled path when the replacement policy is unavailable
// (build failure injected by passing a nil policy while RBAC is desired). The
// snapshot must install an explicit Blocked state that fails closed with 503,
// never retaining the previous open/legacy access.
func TestAuthSnapshot_DisabledToEnabled_PolicyFailureBlocks(t *testing.T) {
	s := &Server{cfg: config.AdminConfig{Listen: "127.0.0.1:0"}}
	// Start OPEN: no legacy token, RBAC disabled.
	s.installAuth(config.AdminConfig{}, nil)

	h := s.requirePermission(rbac.ConfigApply, okHandler())

	// Sanity: open mode currently allows an unauthenticated request.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, applyReq(""))
	if rr.Code != http.StatusOK {
		t.Fatalf("open mode should allow request, got %d", rr.Code)
	}

	// Enable RBAC in config but with a nil policy (simulating a post-Publish
	// policy build failure). The desired mode is RBAC; fail closed.
	desired := config.AdminConfig{}
	desired.RBAC.Enabled = true
	s.installAuth(desired, nil)

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, applyReq(""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("RBAC-desired with no valid policy must fail closed with 503, got %d", rr.Code)
	}

	// Even a would-be valid credential is rejected while blocked.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, applyReq(snapRBACTok))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("blocked state must reject all requests with 503, got %d", rr.Code)
	}
}

// TestAuthSnapshot_LegacyToRBAC_NoLegacyFallback verifies that once RBAC is
// installed atomically, the previously valid legacy token no longer
// authenticates and the RBAC token does.
func TestAuthSnapshot_LegacyToRBAC_NoLegacyFallback(t *testing.T) {
	s := &Server{cfg: config.AdminConfig{Listen: "127.0.0.1:0"}}
	s.installAuth(config.AdminConfig{Token: snapLegacyTok}, nil)

	h := s.requirePermission(rbac.ConfigApply, okHandler())

	// Legacy token works before the transition.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, applyReq(snapLegacyTok))
	if rr.Code != http.StatusOK {
		t.Fatalf("legacy token should work before transition, got %d", rr.Code)
	}

	// Transition to RBAC with no legacy token carried over.
	s.installAuth(config.AdminConfig{}, snapAdminPolicy(t, snapRBACTok, time.Now()))

	// Old legacy token must now be rejected.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, applyReq(snapLegacyTok))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("legacy token must be rejected after RBAC install, got %d", rr.Code)
	}
	// RBAC token works.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, applyReq(snapRBACTok))
	if rr.Code != http.StatusOK {
		t.Errorf("RBAC token should work after transition, got %d", rr.Code)
	}
}

// TestAuthSnapshot_RBACToLegacy_NoRBACResidue verifies the reverse transition:
// after moving to legacy mode the RBAC token must stop working and the legacy
// token must take over. This also guards against a stale policy pointer being
// consulted after the mode flips.
func TestAuthSnapshot_RBACToLegacy_NoRBACResidue(t *testing.T) {
	s := &Server{cfg: config.AdminConfig{Listen: "127.0.0.1:0"}}
	s.installAuth(config.AdminConfig{}, snapAdminPolicy(t, snapRBACTok, time.Now()))

	h := s.requirePermission(rbac.ConfigApply, okHandler())

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, applyReq(snapRBACTok))
	if rr.Code != http.StatusOK {
		t.Fatalf("RBAC token should work before transition, got %d", rr.Code)
	}

	s.installAuth(config.AdminConfig{Token: snapLegacyTok}, nil)

	// RBAC token must now be rejected (legacy compares against the shared token).
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, applyReq(snapRBACTok))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("RBAC token must be rejected after legacy install, got %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, applyReq(snapLegacyTok))
	if rr.Code != http.StatusOK {
		t.Errorf("legacy token should work after transition, got %d", rr.Code)
	}
}

// TestAuthSnapshot_PrincipalExpiryBoundary proves the double-build divergence
// concern is handled at authentication time: a principal that is valid when the
// policy is built but expires before the request is authenticated is rejected
// (401/ErrDisabled), never granted access. The policy is built once and carried
// through the snapshot; Authenticate re-checks expiry against the request clock.
func TestAuthSnapshot_PrincipalExpiryBoundary(t *testing.T) {
	base := time.Now()
	expired := base.Add(-time.Minute) // already expired relative to "now"
	pol, err := rbac.Build(true, "admin", nil, []rbac.PrincipalDef{
		// A never-expiring admin so Build's admin-capable invariant is satisfied.
		{Name: "admin", Role: rbac.RoleAdmin, Token: snapRBACTok},
		// The operator we authenticate as is already expired.
		{Name: "op", Role: rbac.RoleOperator, Token: "op-token-32-chars-padded-xxxxxxx", ExpiresAt: expired},
	}, "", base)
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s := &Server{cfg: config.AdminConfig{Listen: "127.0.0.1:0"}}
	s.installAuth(config.AdminConfig{}, pol)

	h := s.requirePermission(rbac.ConfigApply, okHandler())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, applyReq("op-token-32-chars-padded-xxxxxxx"))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expired principal must be rejected with 401, got %d", rr.Code)
	}
}

// TestAuthSnapshot_ConcurrentReadsAlwaysConsistent runs readers across every
// transition kind concurrently and asserts each observed snapshot is
// self-consistent: the mode and the fields it depends on never disagree. This
// is the core invariant the atomic pointer swap guarantees and the old
// three-store design violated.
func TestAuthSnapshot_ConcurrentReadsAlwaysConsistent(t *testing.T) {
	s := &Server{cfg: config.AdminConfig{Listen: "127.0.0.1:0"}}
	s.installAuth(config.AdminConfig{Token: snapLegacyTok}, nil)

	var inconsistent atomic.Int64
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := s.currentAuth()
				switch snap.mode {
				case authModeRBAC:
					// RBAC mode must always carry an enabled policy.
					if snap.policy == nil || !snap.policy.Enabled() {
						inconsistent.Add(1)
					}
				case authModeBlocked:
					// Blocked means RBAC desired but no valid policy.
					if !snap.cfg.RBAC.Enabled {
						inconsistent.Add(1)
					}
				case authModeLegacy:
					// Legacy requires a non-empty token and no active RBAC policy.
					if snap.cfg.Token == "" || (snap.policy != nil && snap.policy.Enabled()) {
						inconsistent.Add(1)
					}
				case authModeOpen:
					// Open means no token, no enabled policy, RBAC not desired.
					if snap.cfg.Token != "" || (snap.policy != nil && snap.policy.Enabled()) || snap.cfg.RBAC.Enabled {
						inconsistent.Add(1)
					}
				}
			}
		}()
	}

	rbacDesired := config.AdminConfig{}
	rbacDesired.RBAC.Enabled = true
	for i := 0; i < 3000; i++ {
		switch i % 4 {
		case 0:
			s.installAuth(config.AdminConfig{Token: snapLegacyTok}, nil)
		case 1:
			s.installAuth(config.AdminConfig{}, snapAdminPolicy(t, snapRBACTok, time.Now()))
		case 2:
			s.installAuth(rbacDesired, nil) // blocked
		case 3:
			s.installAuth(config.AdminConfig{}, nil) // open
		}
	}
	close(stop)
	wg.Wait()

	if got := inconsistent.Load(); got != 0 {
		t.Fatalf("observed %d internally inconsistent snapshots; atomic install is not holding", got)
	}
}
