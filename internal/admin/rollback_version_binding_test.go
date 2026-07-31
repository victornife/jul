// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"jul/internal/config"
)

// rollbackFixture builds a file-backed admin server with history enabled, seeds
// one apply so a snapshot exists to roll back to, and returns the router, the
// on-disk config path, the newest snapshot id (the pre-apply configuration), and
// the canonical base_version of the currently persisted configuration.
func rollbackFixture(t *testing.T) (h http.Handler, cfgPath, rollbackID, baseVersion string) {
	t.Helper()
	s, cfgPath := concurrentWriteServer(t)
	h = s.routes()

	// One apply so the seed becomes a snapshot and the persisted config differs
	// from it (a non-empty rollback diff).
	if rr := rbApply(t, h, "http://127.0.0.1:9101", ""); rr.Code != http.StatusOK {
		t.Fatalf("seed apply: status %d, body %s", rr.Code, rr.Body.String())
	}
	rollbackID = rbNewestSnapshot(t, h)
	baseVersion = rbConfigBaseVersion(t, h)
	return h, cfgPath, rollbackID, baseVersion
}

// rbApply performs a structured route-target patch apply, optionally carrying an
// optimistic-concurrency base version.
func rbApply(t *testing.T, h http.Handler, target, baseVersion string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(patchApplyRequest{
		BaseVersion: baseVersion,
		Ops:         []patchRequest{{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/", Target: target}},
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader(body)))
	return rr
}

// rbConfigBaseVersion reads the persisted base_version the way the Console does.
func rbConfigBaseVersion(t *testing.T, h http.Handler) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/config: status %d, body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		BaseVersion string `json:"base_version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode /api/config: %v", err)
	}
	return out.BaseVersion
}

// rbDiffBaseVersion previews a rollback and returns the base_version the preview
// reports — the exact configuration the operator reviews.
func rbDiffBaseVersion(t *testing.T, h http.Handler, id string) (summary, baseVersion string) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/history/"+id+"/diff", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET diff: status %d, body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Summary     string `json:"summary"`
		BaseVersion string `json:"base_version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode diff: %v", err)
	}
	return out.Summary, out.BaseVersion
}

// rbNewestSnapshot returns the newest history snapshot id.
func rbNewestSnapshot(t *testing.T, h http.Handler) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/history", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("history list: status %d, body %s", rr.Code, rr.Body.String())
	}
	var entries []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one snapshot")
	}
	return entries[0].ID
}

// rbRollback posts a rollback with an optional base_version and confirm_admin.
func rbRollback(t *testing.T, h http.Handler, path, id, baseVersion string, confirmAdmin bool) *httptest.ResponseRecorder {
	t.Helper()
	payload := map[string]string{"id": id}
	if baseVersion != "" {
		payload["base_version"] = baseVersion
	}
	body, _ := json.Marshal(payload)
	url := path
	if confirmAdmin {
		url += "?confirm_admin=true"
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body)))
	return rr
}

func rbTarget(t *testing.T, cfgPath string) string {
	t.Helper()
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	c, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if len(c.Servers) == 0 || len(c.Servers[0].Locations) == 0 {
		t.Fatalf("unexpected config shape:\n%s", raw)
	}
	return c.Servers[0].Locations[0].ProxyPass
}

// TestRollbackDiffReturnsBaseVersion proves the rollback preview reports the
// canonical base_version it was computed against (Net-new issue 1) and that the
// diff fields remain at the top level (non-breaking response shape).
func TestRollbackDiffReturnsBaseVersion(t *testing.T) {
	h, _, rollbackID, want := rollbackFixture(t)
	summary, got := rbDiffBaseVersion(t, h, rollbackID)
	if summary == "" {
		t.Error("expected a non-empty diff summary")
	}
	if got == "" {
		t.Fatal("expected base_version in the rollback preview")
	}
	if got != want {
		t.Fatalf("preview base_version = %q, want persisted %q", got, want)
	}
}

// TestRollbackStaleBaseVersionConflicts proves the version binding: previewing at
// V1, mutating the config to V2, then confirming the rollback with the stale V1
// is rejected with 409 and nothing is written — on both rollback endpoints.
func TestRollbackStaleBaseVersionConflicts(t *testing.T) {
	for _, path := range []string{"/api/history/rollback", "/api/config/rollback"} {
		t.Run(path, func(t *testing.T) {
			h, cfgPath, rollbackID, v1 := rollbackFixture(t)

			// A concurrent writer moves the config from V1 to V2.
			if rr := rbApply(t, h, "http://127.0.0.1:9202", ""); rr.Code != http.StatusOK {
				t.Fatalf("mutate to V2: status %d, body %s", rr.Code, rr.Body.String())
			}
			beforeTarget := rbTarget(t, cfgPath)
			if beforeTarget != "http://127.0.0.1:9202" {
				t.Fatalf("pre-rollback target = %q, want the V2 change", beforeTarget)
			}

			rr := rbRollback(t, h, path, rollbackID, v1, false)
			if rr.Code != http.StatusConflict {
				t.Fatalf("stale rollback status = %d, want 409; body %s", rr.Code, rr.Body.String())
			}
			var out struct {
				OK             bool   `json:"ok"`
				Conflict       bool   `json:"conflict"`
				CurrentVersion string `json:"current_version"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode conflict: %v", err)
			}
			if out.OK || !out.Conflict {
				t.Fatalf("expected ok=false conflict=true, got %+v", out)
			}
			if out.CurrentVersion == "" || out.CurrentVersion == v1 {
				t.Fatalf("current_version = %q, want the fresh V2 version", out.CurrentVersion)
			}
			// Nothing was written: the V2 change is still the persisted target.
			if got := rbTarget(t, cfgPath); got != beforeTarget {
				t.Fatalf("config changed on a rejected rollback: %q -> %q", beforeTarget, got)
			}
		})
	}
}

// TestRollbackFreshBaseVersionSucceeds proves that refreshing the preview after a
// concurrent change and confirming with the fresh base_version applies the
// rollback.
func TestRollbackFreshBaseVersionSucceeds(t *testing.T) {
	h, cfgPath, rollbackID, _ := rollbackFixture(t)
	origTarget := "http://127.0.0.1:9000" // the seeded snapshot's target

	if rr := rbApply(t, h, "http://127.0.0.1:9202", ""); rr.Code != http.StatusOK {
		t.Fatalf("mutate to V2: status %d, body %s", rr.Code, rr.Body.String())
	}
	// Refresh the preview: base_version now tracks the mutated config.
	_, v2 := rbDiffBaseVersion(t, h, rollbackID)
	if v2 == "" {
		t.Fatal("expected a refreshed base_version")
	}

	rr := rbRollback(t, h, "/api/config/rollback", rollbackID, v2, false)
	if rr.Code != http.StatusOK {
		t.Fatalf("fresh rollback status = %d, want 200; body %s", rr.Code, rr.Body.String())
	}
	if got := rbTarget(t, cfgPath); got != origTarget {
		t.Fatalf("after rollback target = %q, want the snapshot's %q", got, origTarget)
	}
}

// TestRollbackConfirmAdminDoesNotBypassVersionCheck proves the admin-confirmation
// variant: a confirmed retry (confirm_admin=true) carrying a stale base_version
// is still rejected with 409, so the confirmed transition stays bound to the
// operation the operator reviewed rather than silently absorbing later changes.
func TestRollbackConfirmAdminDoesNotBypassVersionCheck(t *testing.T) {
	h, cfgPath, rollbackID, v1 := rollbackFixture(t)

	if rr := rbApply(t, h, "http://127.0.0.1:9202", ""); rr.Code != http.StatusOK {
		t.Fatalf("mutate to V2: status %d, body %s", rr.Code, rr.Body.String())
	}
	beforeTarget := rbTarget(t, cfgPath)

	rr := rbRollback(t, h, "/api/config/rollback", rollbackID, v1, true)
	if rr.Code != http.StatusConflict {
		t.Fatalf("confirmed stale rollback status = %d, want 409; body %s", rr.Code, rr.Body.String())
	}
	if got := rbTarget(t, cfgPath); got != beforeTarget {
		t.Fatalf("config changed on a rejected confirmed rollback: %q -> %q", beforeTarget, got)
	}
}

// TestRollbackExternalFileChangeConflicts proves an out-of-band disk edit after
// the preview is detected: the rollback carrying the pre-edit base_version is
// rejected with 409 and the external edit is not overwritten.
func TestRollbackExternalFileChangeConflicts(t *testing.T) {
	h, cfgPath, rollbackID, _ := rollbackFixture(t)
	_, v1 := rbDiffBaseVersion(t, h, rollbackID)

	external, err := config.Marshal(config.ProxyTarget("127.0.0.1:9500", ":8080"))
	if err != nil {
		t.Fatalf("marshal external: %v", err)
	}
	if err := os.WriteFile(cfgPath, external, 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}

	rr := rbRollback(t, h, "/api/config/rollback", rollbackID, v1, false)
	if rr.Code != http.StatusConflict {
		t.Fatalf("rollback after external edit status = %d, want 409; body %s", rr.Code, rr.Body.String())
	}
	if got := rbTarget(t, cfgPath); got != "http://127.0.0.1:9500" {
		t.Fatalf("external edit overwritten: target = %q, want the external :9500", got)
	}
}

// TestRollbackEmptyBaseVersionSkipsCheck proves the check is opt-in: a rollback
// with no base_version (a legacy client or an explicit force) still applies even
// after a concurrent change.
func TestRollbackEmptyBaseVersionSkipsCheck(t *testing.T) {
	h, cfgPath, rollbackID, _ := rollbackFixture(t)

	if rr := rbApply(t, h, "http://127.0.0.1:9202", ""); rr.Code != http.StatusOK {
		t.Fatalf("mutate to V2: status %d, body %s", rr.Code, rr.Body.String())
	}
	rr := rbRollback(t, h, "/api/config/rollback", rollbackID, "", false)
	if rr.Code != http.StatusOK {
		t.Fatalf("force rollback status = %d, want 200; body %s", rr.Code, rr.Body.String())
	}
	if got := rbTarget(t, cfgPath); got != "http://127.0.0.1:9000" {
		t.Fatalf("after force rollback target = %q, want the snapshot's :9000", got)
	}
}
