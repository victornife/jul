// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"net/http"
	"testing"
)

func TestManagedApplyRegistryIdempotencyGuardBranches(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	fp := [32]byte{7, 8, 9}
	if err := reg.BindIdempotency("bad", "retry-key", fp, http.MethodPost, "/api/v1/config/apply", "alice"); !errors.Is(err, ErrManagedApplyInvalidID) {
		t.Fatalf("invalid id error=%v", err)
	}
	missing := "rl_abcdef123456_9"
	if err := reg.BindIdempotency(missing, "retry-key", fp, http.MethodPost, "/api/v1/config/apply", "alice"); !errors.Is(err, ErrManagedApplyRecordIncomplete) {
		t.Fatalf("missing record error=%v", err)
	}

	id := "rl_abcdef123456_10"
	if err := reg.BeginPending(ManagedApplyRecord{ID: id, Operation: ApplyOperationConfigApply}); err != nil {
		t.Fatal(err)
	}
	if err := reg.BindIdempotency(id, "retry-key", fp, http.MethodPost, "/api/v1/config/apply", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := reg.BindIdempotency(id, "retry-key", fp, http.MethodPost, "/api/v1/config/apply", "alice"); err != nil {
		t.Fatalf("identical immutable rebind=%v", err)
	}
	if err := reg.BindIdempotency(id, "other-key", fp, http.MethodPost, "/api/v1/config/apply", "alice"); !errors.Is(err, ErrManagedApplyIdempotencyMismatch) {
		t.Fatalf("mismatched rebind error=%v", err)
	}

	if _, ok := (*ManagedApplyRegistry)(nil).FindIdempotency("alice", "retry-key"); ok {
		t.Fatal("nil registry returned a binding")
	}
	if _, ok := reg.FindIdempotency("", "retry-key"); ok {
		t.Fatal("empty principal returned a binding")
	}
	if _, ok := reg.FindIdempotency("alice", ""); ok {
		t.Fatal("empty key returned a binding")
	}
	if _, ok := reg.FindIdempotency("nobody", "retry-key"); ok {
		t.Fatal("unknown binding was found")
	}
}

func TestManagedApplyRegistryFinalizationRejectsIncompleteMode(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	id := "rl_abcdef123456_11"
	if err := reg.BeginPending(ManagedApplyRecord{
		ID: id, Operation: ApplyOperationConfigApply,
		Result: ConfigApplyResult{ApplyID: id, Mode: "hot"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.FailFinalization(ManagedApplyRecord{
		ID: id, Operation: ApplyOperationConfigApply,
		Result: ConfigApplyResult{ApplyID: id, Mode: "invalid"},
	}); !errors.Is(err, ErrManagedApplyRecordIncomplete) {
		t.Fatalf("incomplete terminal mode error=%v", err)
	}
}
