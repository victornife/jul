// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/rbac"
)

// TestV1ConfigReportsMetadataAndNoBytes is the property that keeps this
// operation on the right side of §36: /api/v1/config describes the
// configuration, it never returns it.
func TestV1ConfigReportsMetadataAndNoBytes(t *testing.T) {
	const secret = "super-secret-upstream-password"
	s := newTestServer(t, config.AdminConfig{}, Deps{
		Authority: func() ConfigAuthorityStatus {
			return ConfigAuthorityStatus{Mode: "managed", Source: "explicit", DiskVersion: "9f2c1ab7d4e05863"}
		},
		ReadConfigRaw: func() ([]byte, error) {
			return []byte("[global]\ntoken = \"" + secret + "\"\n"), nil
		},
	})

	rr := getV1(t, s, "/api/v1/config", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, secret) {
		t.Fatal("/api/v1/config returned configuration content")
	}
	if strings.Contains(body, "[global]") || strings.Contains(body, "\"raw\"") {
		t.Fatalf("/api/v1/config returned configuration bytes: %s", body)
	}

	got := decodeInto[adminapi.ConfigResponse](t, rr)
	if got.PersistedVersion != "9f2c1ab7d4e05863" || got.ConfigAuthority != "managed" {
		t.Fatalf("metadata = %+v", got)
	}
}

func TestV1PendingRestart(t *testing.T) {
	t.Run("none staged", func(t *testing.T) {
		s := newTestServer(t, config.AdminConfig{}, Deps{})
		got := decodeInto[adminapi.PendingRestartResponse](t, getV1(t, s, "/api/v1/config/pending-restart", ""))
		if got.Pending {
			t.Fatalf("pending = true with nothing staged: %+v", got)
		}
	})

	t.Run("staged", func(t *testing.T) {
		s := newTestServer(t, config.AdminConfig{}, Deps{
			PendingRestart: func() *PendingRestartStatus {
				return &PendingRestartStatus{
					State: "managed_staged", StagedVersion: "9f2c1ab7d4e05863",
					Subsystems: []string{"cache", "listener"}, DiscardAvailable: true,
				}
			},
		})
		got := decodeInto[adminapi.PendingRestartResponse](t, getV1(t, s, "/api/v1/config/pending-restart", ""))
		if !got.Pending || got.State != "managed_staged" {
			t.Fatalf("response = %+v", got)
		}
		if len(got.Subsystems) != 2 || !got.DiscardAvailable {
			t.Fatalf("response = %+v", got)
		}
	})
}

// TestV1ApplyGetDistinguishesPendingFromTerminal is the assertion the whole
// polling contract rests on. §33.2 is explicit that a non-terminal record may
// still carry a meaningful state, and that a client which stopped polling on a
// 200 — or on a non-empty outcome — would wait forever for a result that had
// not happened yet.
func TestV1ApplyGetDistinguishesPendingFromTerminal(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	if err := reg.BeginPending(ManagedApplyRecord{
		ID: "rl_aaaaaaaaaaaa_1", State: ManagedApplyPending,
		Operation: ApplyOperationConfigApply, StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("begin pending: %v", err)
	}

	s := newTestServer(t, config.AdminConfig{}, Deps{ManagedApplies: reg})

	rr := getV1(t, s, "/api/v1/config/applies/rl_aaaaaaaaaaaa_1", "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("a pending record answered %d, want 202", rr.Code)
	}
	got := decodeInto[adminapi.ApplyResultResponse](t, rr)
	if got.Terminal {
		t.Fatal("a pending record reported terminal = true")
	}
	if got.State != string(ManagedApplyPending) {
		t.Fatalf("state = %q", got.State)
	}
	if got.Degraded == nil {
		t.Fatal("degraded must be present even on a pending record")
	}
	if got.BootID == "" {
		t.Fatal("boot_id is absent; a client cannot tell an evicted record from a lost ledger without it")
	}
}

func TestV1ApplyGetUnknownIDIsNotFound(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{ManagedApplies: NewManagedApplyRegistry(0, 0)})

	rr := getV1(t, s, "/api/v1/config/applies/rl_bbbbbbbbbbbb_9", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	env := decodeEnvelope(t, rr)
	if env.Error.Code != adminapi.CodeNotFound {
		t.Fatalf("code = %q", env.Error.Code)
	}
	if env.Error.Details.Kind != "managed_apply" {
		t.Fatalf("details = %+v", env.Error.Details)
	}
	// The message must explain eviction, because "not found" for a record that
	// existed an hour ago is otherwise indistinguishable from a client bug.
	if !strings.Contains(env.Error.Message, "evicted") {
		t.Errorf("the message does not mention eviction: %q", env.Error.Message)
	}
}

// TestV1ApplyGetMalformedIDIsAUsageError pins §33.1's placement: a malformed id
// means the *command* was wrong (exit 2), not that the configuration was.
func TestV1ApplyGetMalformedIDIsAUsageError(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{ManagedApplies: NewManagedApplyRegistry(0, 0)})

	rr := getV1(t, s, "/api/v1/config/applies/not-an-apply-id", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	env := decodeEnvelope(t, rr)
	if env.Error.Code != adminapi.CodeInvalidRequest {
		t.Fatalf("code = %q, want invalid_request", env.Error.Code)
	}
	if env.Error.Details.Field != "apply_id" {
		t.Fatalf("details.field = %q", env.Error.Details.Field)
	}
}

// TestV1ApplyGetKeepsTheOwnershipRule is the authorization rule the internal
// route already enforces. An external alias that relaxed it would be exactly
// the drift ADR 0019 §24 warns about: the same operation, a weaker check.
func TestV1ApplyGetKeepsTheOwnershipRule(t *testing.T) {
	const rollbackOnly = "rollback-only-token-32-chars----"
	pol, err := rbac.Build(true, "admin",
		map[string][]string{"rollbacker": {"history:rollback"}},
		[]rbac.PrincipalDef{
			{Name: "rb", Role: "rollbacker", Token: rollbackOnly},
			{Name: "admin-user", Role: rbac.RoleAdmin, Token: "admin-token-32-chars-padded-----"},
		}, "", time.Now())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}

	reg := NewManagedApplyRegistry(0, 0)
	// An apply owned by somebody else, which a rollback-only principal must not
	// be able to probe by id.
	if err := reg.BeginPending(ManagedApplyRecord{
		ID: "rl_cccccccccccc_1", State: ManagedApplyPending,
		Operation: ApplyOperationConfigApply, OwnerTokenID: "other-token-id", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("begin pending: %v", err)
	}

	s := newTestServer(t, config.AdminConfig{RBAC: config.AdminRBACConfig{Enabled: true}}, Deps{ManagedApplies: reg})
	s.UpdatePolicy(pol)

	rr := getV1(t, s, "/api/v1/config/applies/rl_cccccccccccc_1", rollbackOnly)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	env := decodeEnvelope(t, rr)
	if env.Error.Code != adminapi.CodeForbidden {
		t.Fatalf("code = %q", env.Error.Code)
	}
	// The refusal must not confirm the record exists, and must not name a
	// permission the caller already holds.
	if strings.Contains(env.Error.Message, "rl_cccccccccccc_1") {
		t.Error("the refusal confirmed the record exists")
	}
	if env.Error.Details.RequiredPermission != "" {
		t.Errorf("the refusal named a permission the caller holds: %q", env.Error.Details.RequiredPermission)
	}
}

// historyServer writes n snapshots and returns a server whose history store
// holds them.
func historyServer(t *testing.T, n int) *Server {
	t.Helper()
	dir := t.TempDir()
	s := newTestServer(t, config.AdminConfig{HistoryDir: dir, HistoryKeep: 500}, Deps{})
	for i := range n {
		if _, err := s.hist.snapshot(fmt.Appendf(nil, "[global]\nlog_level = \"info\" # %d\n", i)); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
		// The id encodes a timestamp; keep them distinct and ordered.
		time.Sleep(2 * time.Millisecond)
	}
	return s
}

// TestV1HistoryListPaginates covers §24a's rule for the only unbounded v1
// collection: newest first, default 50, cap 200, opaque cursor.
func TestV1HistoryListPaginates(t *testing.T) {
	s := historyServer(t, 7)

	first := decodeInto[adminapi.HistoryListResponse](t, getV1(t, s, "/api/v1/config/history?limit=3", ""))
	if len(first.Entries) != 3 {
		t.Fatalf("first page has %d entries, want 3", len(first.Entries))
	}
	if first.Limit != 3 {
		t.Fatalf("limit = %d", first.Limit)
	}
	if first.NextCursor == "" {
		t.Fatal("no cursor with more entries remaining")
	}
	// Newest first, by id, which is monotonic by construction.
	for i := 1; i < len(first.Entries); i++ {
		if first.Entries[i-1].ID < first.Entries[i].ID {
			t.Fatalf("entries are not newest-first: %q before %q", first.Entries[i-1].ID, first.Entries[i].ID)
		}
	}

	second := decodeInto[adminapi.HistoryListResponse](t,
		getV1(t, s, "/api/v1/config/history?limit=3&cursor="+first.NextCursor, ""))
	if len(second.Entries) != 3 {
		t.Fatalf("second page has %d entries, want 3", len(second.Entries))
	}
	// The pages must not overlap.
	seen := map[string]bool{}
	for _, e := range append(append([]adminapi.HistoryEntry{}, first.Entries...), second.Entries...) {
		if seen[e.ID] {
			t.Fatalf("entry %q appeared on both pages", e.ID)
		}
		seen[e.ID] = true
	}

	last := decodeInto[adminapi.HistoryListResponse](t,
		getV1(t, s, "/api/v1/config/history?limit=3&cursor="+second.NextCursor, ""))
	if len(last.Entries) != 1 {
		t.Fatalf("last page has %d entries, want 1", len(last.Entries))
	}
	if last.NextCursor != "" {
		t.Fatalf("the last page supplied a cursor: %q", last.NextCursor)
	}
}

func TestV1HistoryListDefaultAndCap(t *testing.T) {
	s := historyServer(t, 2)

	def := decodeInto[adminapi.HistoryListResponse](t, getV1(t, s, "/api/v1/config/history", ""))
	if def.Limit != adminapi.HistoryLimitDefault {
		t.Fatalf("default limit = %d, want %d", def.Limit, adminapi.HistoryLimitDefault)
	}

	// An out-of-range limit is reported, not silently clamped: a client asking
	// for 1000 and receiving 200 without being told has a paging bug it cannot
	// see.
	for _, bad := range []string{"0", "-1", "201", "abc", "1e3"} {
		rr := getV1(t, s, "/api/v1/config/history?limit="+bad, "")
		if rr.Code != http.StatusBadRequest {
			t.Errorf("limit=%s produced %d, want 400", bad, rr.Code)
			continue
		}
		if env := decodeEnvelope(t, rr); env.Error.Details.Field != "limit" {
			t.Errorf("limit=%s: details.field = %q", bad, env.Error.Details.Field)
		}
	}
}

func TestV1HistoryListRejectsAnUnknownCursor(t *testing.T) {
	s := historyServer(t, 2)
	rr := getV1(t, s, "/api/v1/config/history?cursor=not-a-real-snapshot", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if env := decodeEnvelope(t, rr); env.Error.Details.Field != "cursor" {
		t.Fatalf("details.field = %q", env.Error.Details.Field)
	}
}

// TestV1HistoryListCarriesNoBodiesOrActors: a snapshot is a configuration file
// and may hold literal secrets, and attribution belongs to the audit API.
func TestV1HistoryListCarriesNoBodiesOrActors(t *testing.T) {
	const secret = "super-secret-upstream-password"
	dir := t.TempDir()
	s := newTestServer(t, config.AdminConfig{HistoryDir: dir, HistoryKeep: 50}, Deps{})
	if _, err := s.hist.snapshot([]byte("[global]\ntoken = \"" + secret + "\"\n")); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	rr := getV1(t, s, "/api/v1/config/history", "")
	body := rr.Body.String()
	if strings.Contains(body, secret) {
		t.Fatal("the history listing returned snapshot content")
	}
	for _, forbidden := range []string{`"actor"`, `"raw"`, `"reason"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the listing published %s: %s", forbidden, body)
		}
	}

	got := decodeInto[adminapi.HistoryListResponse](t, rr)
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %d", len(got.Entries))
	}
	if got.Entries[0].RecordedAt == "" || !strings.HasSuffix(got.Entries[0].RecordedAt, "Z") {
		t.Errorf("recorded_at = %q; §24a requires RFC 3339 with a Z offset", got.Entries[0].RecordedAt)
	}
}

// TestV1HistoryListEmptyIsAnArrayNotNull: a client iterating the result must
// not have to special-case null.
func TestV1HistoryListEmptyIsAnArrayNotNull(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}, Deps{})
	rr := getV1(t, s, "/api/v1/config/history", "")
	if !strings.Contains(rr.Body.String(), `"entries":[]`) {
		t.Fatalf("an empty listing must serialize entries as []: %s", rr.Body.String())
	}
}

// TestV1HistoryStorageFailureIsReportedAsUnavailable, not as a validation
// error: the caller did nothing wrong and retrying the same request is the
// correct response.
func TestV1HistoryStorageFailureIsReportedAsUnavailable(t *testing.T) {
	dir := t.TempDir()
	s := newTestServer(t, config.AdminConfig{HistoryDir: dir, HistoryKeep: 50}, Deps{})
	if _, err := s.hist.snapshot([]byte("[global]\n")); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory mode does not deny reads")
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Skipf("cannot make the history directory unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	rr := getV1(t, s, "/api/v1/config/history", "")
	if rr.Code == http.StatusOK {
		t.Skip("the platform ignored the directory mode")
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	env := decodeEnvelope(t, rr)
	if env.Error.Code != adminapi.CodeStorageUnavailable {
		t.Fatalf("code = %q", env.Error.Code)
	}
	if strings.Contains(env.Error.Message, dir) {
		t.Fatalf("the error disclosed the history path: %q", env.Error.Message)
	}
}

// TestV1ConfigReadRoutesAreSideEffectFree: every operation in this group is a
// read, and a read that mutated would be the worst possible surprise in a CLI
// that runs them to decide whether to mutate.
func TestV1ConfigReadRoutesAreSideEffectFree(t *testing.T) {
	var writes int
	s := newTestServer(t, config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}, Deps{
		WriteConfigRaw: func([]byte) error { writes++; return nil },
		ApplyConfigRaw: func(ApplyRequestContext, []byte, string) (ConfigApplyResult, error) {
			writes++
			return ConfigApplyResult{}, nil
		},
		DiscardPendingRestart: func() (ConfigApplyResult, error) { writes++; return ConfigApplyResult{}, nil },
		ManagedApplies:        NewManagedApplyRegistry(0, 0),
	})

	for _, path := range []string{
		"/api/v1/config",
		"/api/v1/config/pending-restart",
		"/api/v1/config/history",
		"/api/v1/config/applies/rl_aaaaaaaaaaaa_1",
	} {
		rr := getV1(t, s, path, "")
		if rr.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control = %q", path, rr.Header().Get("Cache-Control"))
		}
	}
	if writes != 0 {
		t.Fatalf("the read group performed %d writes", writes)
	}
}

// TestV1ConfigGroupRejectsNonGET keeps the whole group on one convention.
func TestV1ConfigGroupRejectsNonGET(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{HistoryDir: t.TempDir()}, Deps{
		ManagedApplies: NewManagedApplyRegistry(0, 0),
	})
	for _, path := range []string{
		"/api/v1/config",
		"/api/v1/config/pending-restart",
		"/api/v1/config/history",
		"/api/v1/config/applies/rl_aaaaaaaaaaaa_1",
	} {
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, path, nil))
		if rr.Code != http.StatusBadRequest && rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s DELETE = %d", path, rr.Code)
		}
	}
}
