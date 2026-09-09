// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/rbac"
)

func TestV1IdempotencyKeyGrammar(t *testing.T) {
	for _, key := range []string{"abcdefgh", "A1_b-C9Z", strings.Repeat("x", 128)} {
		if !validV1IdempotencyKey(key) {
			t.Errorf("valid key %q rejected", key)
		}
	}
	for _, key := range []string{"", "short", strings.Repeat("x", 129), "white space", "slash/key", "ümlaut12"} {
		if validV1IdempotencyKey(key) {
			t.Errorf("invalid key %q accepted", key)
		}
	}
}

func TestCanonicalV1QueryIsSortedDecodedAndInjective(t *testing.T) {
	a, err := canonicalV1Query("z=2&a=3&a=1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalV1Query("a=1&z=2&a=3")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("equivalent pair sets differ: %q vs %q", a, b)
	}

	joined, err := canonicalV1Query("a=b%26c%3Dd")
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := canonicalV1Query("a=b&c=d")
	if err != nil {
		t.Fatal(err)
	}
	if string(joined) == string(pairs) {
		t.Fatalf("length-prefixed encoding collided: %q", joined)
	}
}

func TestV1RequestFingerprintCoversWholeWireRequest(t *testing.T) {
	makeRequest := func(target, contentType, body string) (*http.Request, []byte) {
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		return r, []byte(body)
	}
	baseReq, baseBody := makeRequest("http://example/api/v1/config/apply?base_version=v1&mode=hot", "application/toml", "[global]\n")
	base, apiErr := v1RequestFingerprint(baseReq, baseBody)
	if apiErr != nil {
		t.Fatal(apiErr)
	}

	cases := []struct {
		name string
		req  *http.Request
		body []byte
	}{
		{"other query parameter", func() *http.Request {
			r, _ := makeRequest("http://example/api/v1/config/apply?base_version=v1&mode=stage_restart", "application/toml", "[global]\n")
			return r
		}(), []byte("[global]\n")},
		{"other path", func() *http.Request {
			r, _ := makeRequest("http://example/api/v1/config/patch/apply?base_version=v1&mode=hot", "application/toml", "[global]\n")
			return r
		}(), []byte("[global]\n")},
		{"other content type", func() *http.Request {
			r, _ := makeRequest("http://example/api/v1/config/apply?base_version=v1&mode=hot", "text/plain", "[global]\n")
			return r
		}(), []byte("[global]\n")},
		{"other exact body bytes", func() *http.Request {
			r, _ := makeRequest("http://example/api/v1/config/apply?base_version=v1&mode=hot", "application/toml", "[global] \n")
			return r
		}(), []byte("[global] \n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, apiErr := v1RequestFingerprint(tc.req, tc.body)
			if apiErr != nil {
				t.Fatal(apiErr)
			}
			if got == base {
				t.Fatal("semantically distinct wire request reused fingerprint")
			}
		})
	}

	orderedReq, orderedBody := makeRequest("http://example/api/v1/config/apply?mode=hot&base_version=v1", "application/toml", "[global]\n")
	ordered, apiErr := v1RequestFingerprint(orderedReq, orderedBody)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if ordered != base {
		t.Fatal("query pair ordering changed the canonical fingerprint")
	}

	jsonAReq, jsonABody := makeRequest("http://example/api/v1/config/patch/apply?mode=hot", "application/json", `{"a":1,"b":2}`)
	jsonBReq, jsonBBody := makeRequest("http://example/api/v1/config/patch/apply?mode=hot", "application/json", `{"b":2,"a":1}`)
	jsonA, _ := v1RequestFingerprint(jsonAReq, jsonABody)
	jsonB, _ := v1RequestFingerprint(jsonBReq, jsonBBody)
	if jsonA == jsonB {
		t.Fatal("JSON key reordering was canonicalized; ADR requires exact body bytes")
	}
}

func TestManagedApplyRegistryRetainsPrivateIdempotencyBinding(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	id := "rl_abcdef123456_1"
	if err := reg.BeginPending(ManagedApplyRecord{ID: id, Operation: ApplyOperationConfigApply}); err != nil {
		t.Fatal(err)
	}
	fingerprint := [32]byte{1, 2, 3}
	if err := reg.BindIdempotency(id, "retry-key", fingerprint, http.MethodPost, "/api/v1/config/apply", "alice"); err != nil {
		t.Fatal(err)
	}
	got, ok := reg.FindIdempotency("alice", "retry-key")
	if !ok || got.ID != id || got.IdempotencyFingerprint != fingerprint {
		t.Fatalf("binding lookup = %+v, %v", got, ok)
	}
	if _, ok := reg.FindIdempotency("bob", "retry-key"); ok {
		t.Fatal("idempotency key escaped principal scope")
	}

	if err := reg.Complete(ManagedApplyRecord{
		ID: id, Operation: ApplyOperationConfigApply,
		Result: ConfigApplyResult{ApplyID: id, Mode: "hot", OK: true},
	}); err != nil {
		t.Fatal(err)
	}
	got, ok = reg.FindIdempotency("alice", "retry-key")
	if !ok || got.State != ManagedApplyTerminal || got.IdempotencyMethod != http.MethodPost {
		t.Fatalf("binding lost at terminalization: %+v, %v", got, ok)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"retry-key", "alice", "/api/v1/config/apply"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("private idempotency metadata leaked through ledger JSON: %s", encoded)
		}
	}
}

func TestV1IdempotencyPendingThenTerminalReplay(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	s := &Server{deps: Deps{ManagedApplies: reg, BootID: func() string { return "abcdef123456" }}}
	const id = "rl_abcdef123456_1"
	const key = "retry-key-123"
	calls := 0

	request := func(body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://example/api/v1/config/apply?base_version=v1&mode=hot", strings.NewReader(body))
		r.Pattern = "/api/v1/config/apply"
		r.Header.Set("Content-Type", "application/toml")
		r.Header.Set("Idempotency-Key", key)
		ident := rbac.Identity{Principal: "alice", TokenID: "tok-1", Permissions: []rbac.Permission{rbac.ConfigApply}}
		return r.WithContext(rbac.WithIdentity(r.Context(), ident))
	}

	handler := func(w http.ResponseWriter, r *http.Request) {
		calls++
		binding := v1IdempotencyFromRequest(r)
		if binding == nil {
			t.Fatal("validated idempotency binding did not reach the canonical mutation")
		}
		result := ConfigApplyResult{ApplyID: id, Mode: "hot", OK: false}
		if err := reg.BeginPending(ManagedApplyRecord{
			ID:                     id,
			Operation:              ApplyOperationConfigApply,
			Result:                 result,
			IdempotencyKey:         binding.Key,
			IdempotencyFingerprint: binding.Fingerprint,
			IdempotencyMethod:      binding.Method,
			IdempotencyOperation:   binding.Operation,
			IdempotencyPrincipal:   binding.Principal,
		}); err != nil {
			t.Fatalf("begin pending: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(result)
	}

	first := httptest.NewRecorder()
	if projected := s.runIdempotentCanonicalV1(first, request("[global]\n"), "v1", []byte("[global]\n"), handler); projected {
		t.Fatal("first execution reported itself as replay")
	}
	if calls != 1 || first.Code != http.StatusAccepted {
		t.Fatalf("first result: calls=%d status=%d body=%s", calls, first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	s.runIdempotentCanonicalV1(second, request("[global]\n"), "v1", []byte("[global]\n"), handler)
	if calls != 1 || second.Code != http.StatusConflict {
		t.Fatalf("pending replay executed again: calls=%d status=%d body=%s", calls, second.Code, second.Body.String())
	}
	var pending adminapi.Envelope
	if err := json.Unmarshal(second.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Error.Code != adminapi.CodeIdempotencyKeyInUse || pending.Error.Details.ApplyID != id {
		t.Fatalf("pending replay = %+v", pending.Error)
	}

	if err := reg.Complete(ManagedApplyRecord{
		ID: id, Operation: ApplyOperationConfigApply,
		Result: ConfigApplyResult{ApplyID: id, Mode: "hot", OK: true, AppOutcome: "applied"},
	}); err != nil {
		t.Fatal(err)
	}
	terminal := httptest.NewRecorder()
	projected := s.runIdempotentCanonicalV1(terminal, request("[global]\n"), "v1", []byte("[global]\n"), handler)
	if !projected || calls != 1 || terminal.Code != http.StatusOK {
		t.Fatalf("terminal replay: projected=%v calls=%d status=%d body=%s", projected, calls, terminal.Code, terminal.Body.String())
	}
	var replay adminapi.ConfigApplyResponse
	if err := json.Unmarshal(terminal.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if !replay.IdempotentReplay || !replay.Terminal || replay.ApplyID != id {
		t.Fatalf("terminal replay body = %+v", replay)
	}

	reused := httptest.NewRecorder()
	s.runIdempotentCanonicalV1(reused, request("[global]\n# changed\n"), "v1", []byte("[global]\n# changed\n"), handler)
	if calls != 1 || reused.Code != http.StatusConflict {
		t.Fatalf("different fingerprint executed: calls=%d status=%d", calls, reused.Code)
	}
	var conflict adminapi.Envelope
	if err := json.Unmarshal(reused.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Error.Code != adminapi.CodeIdempotencyKeyReused || conflict.Error.Details.RecordedOperation != "/api/v1/config/apply" {
		t.Fatalf("reuse conflict = %+v", conflict.Error)
	}
}

func TestV1IdempotencyUsesPrincipalAcrossCredentialRotation(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	s := &Server{deps: Deps{ManagedApplies: reg, BootID: func() string { return "abcdef123456" }}}
	const key = "rotation-key-123"
	const id = "rl_abcdef123456_7"
	calls := 0

	request := func(tokenID string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://example/api/v1/config/apply?base_version=v1&mode=hot", strings.NewReader("[global]\n"))
		r.Pattern = "/api/v1/config/apply"
		r.Header.Set("Content-Type", "application/toml")
		r.Header.Set("Idempotency-Key", key)
		ident := rbac.Identity{Principal: "alice", TokenID: tokenID, Permissions: []rbac.Permission{rbac.ConfigApply}}
		return r.WithContext(rbac.WithIdentity(r.Context(), ident))
	}

	handler := func(w http.ResponseWriter, r *http.Request) {
		calls++
		binding := v1IdempotencyFromRequest(r)
		if binding == nil {
			t.Fatal("missing idempotency binding")
		}
		if err := reg.BeginPending(ManagedApplyRecord{
			ID:                     id,
			Operation:              ApplyOperationConfigApply,
			Result:                 ConfigApplyResult{ApplyID: id, Mode: "hot", OK: true},
			IdempotencyKey:         binding.Key,
			IdempotencyFingerprint: binding.Fingerprint,
			IdempotencyMethod:      binding.Method,
			IdempotencyOperation:   binding.Operation,
			IdempotencyPrincipal:   binding.Principal,
		}); err != nil {
			t.Fatal(err)
		}
		if err := reg.Complete(ManagedApplyRecord{
			ID: id, Operation: ApplyOperationConfigApply,
			Result: ConfigApplyResult{ApplyID: id, Mode: "hot", OK: true, AppOutcome: "applied"},
		}); err != nil {
			t.Fatal(err)
		}
		writeAPIJSON(w, http.StatusOK, ConfigApplyResult{ApplyID: id, Mode: "hot", OK: true})
	}

	first := httptest.NewRecorder()
	s.runIdempotentCanonicalV1(first, request("token-before-rotation"), "v1", []byte("[global]\n"), handler)
	second := httptest.NewRecorder()
	projected := s.runIdempotentCanonicalV1(second, request("token-after-rotation"), "v1", []byte("[global]\n"), handler)
	if !projected || calls != 1 {
		t.Fatalf("same principal after token rotation did not replay: projected=%v calls=%d body=%s", projected, calls, second.Body.String())
	}
}

func TestV1IdempotencyConcurrentDuplicateExecutesOnce(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	s := &Server{deps: Deps{ManagedApplies: reg, BootID: func() string { return "abcdef123456" }}}
	const key = "concurrent-key-123"
	const id = "rl_abcdef123456_9"
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})

	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://example/api/v1/config/apply?base_version=v1&mode=hot", strings.NewReader("[global]\n"))
		r.Pattern = "/api/v1/config/apply"
		r.Header.Set("Content-Type", "application/toml")
		r.Header.Set("Idempotency-Key", key)
		ident := rbac.Identity{Principal: "alice", TokenID: "tok-1", Permissions: []rbac.Permission{rbac.ConfigApply}}
		return r.WithContext(rbac.WithIdentity(r.Context(), ident))
	}

	handler := func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		binding := v1IdempotencyFromRequest(r)
		if binding == nil {
			t.Error("missing idempotency binding")
			return
		}
		if err := reg.BeginPending(ManagedApplyRecord{
			ID:                     id,
			Operation:              ApplyOperationConfigApply,
			Result:                 ConfigApplyResult{ApplyID: id, Mode: "hot"},
			IdempotencyKey:         binding.Key,
			IdempotencyFingerprint: binding.Fingerprint,
			IdempotencyMethod:      binding.Method,
			IdempotencyOperation:   binding.Operation,
			IdempotencyPrincipal:   binding.Principal,
		}); err != nil {
			t.Error(err)
			return
		}
		close(entered)
		<-release
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(ConfigApplyResult{ApplyID: id, Mode: "hot"})
	}

	type outcome struct {
		code int
		body string
	}
	out := make(chan outcome, 2)
	call := func() {
		rr := httptest.NewRecorder()
		s.runIdempotentCanonicalV1(rr, request(), "v1", []byte("[global]\n"), handler)
		out <- outcome{code: rr.Code, body: rr.Body.String()}
	}
	go call()
	<-entered
	go call()
	close(release)
	first := <-out
	second := <-out

	if calls.Load() != 1 {
		t.Fatalf("canonical mutation executed %d times, want exactly once", calls.Load())
	}
	codes := map[int]int{first.code: 1}
	codes[second.code]++
	if codes[http.StatusAccepted] != 1 || codes[http.StatusConflict] != 1 {
		t.Fatalf("duplicate outcomes = (%d %s) and (%d %s), want one 202 and one 409", first.code, first.body, second.code, second.body)
	}
}

func TestV1IdempotencyBootAndEvictionBoundReplay(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	reg := NewManagedApplyRegistry(1, time.Millisecond)
	fingerprint := sha256.Sum256([]byte("request-one"))
	firstID := "rl_abcdef123456_1"
	if err := reg.BeginPending(ManagedApplyRecord{
		ID: firstID, Operation: ApplyOperationConfigApply,
		IdempotencyKey: "eviction-key-1", IdempotencyFingerprint: fingerprint,
		IdempotencyMethod: http.MethodPost, IdempotencyOperation: "/api/v1/config/apply", IdempotencyPrincipal: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Complete(ManagedApplyRecord{
		ID: firstID, Operation: ApplyOperationConfigApply, CompletedAt: old,
		Result: ConfigApplyResult{ApplyID: firstID, Mode: "hot", OK: true},
	}); err != nil {
		t.Fatal(err)
	}
	secondID := "rl_abcdef123456_2"
	if err := reg.BeginPending(ManagedApplyRecord{ID: secondID, Operation: ApplyOperationConfigApply}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Complete(ManagedApplyRecord{
		ID: secondID, Operation: ApplyOperationConfigApply, CompletedAt: old,
		Result: ConfigApplyResult{ApplyID: secondID, Mode: "hot", OK: true},
	}); err != nil {
		t.Fatal(err)
	}
	reg.Prune(time.Now())
	if _, ok := reg.FindIdempotency("alice", "eviction-key-1"); ok {
		t.Fatal("evicted terminal record retained its idempotency binding")
	}

	freshBoot := NewManagedApplyRegistry(1, time.Millisecond)
	if _, ok := freshBoot.FindIdempotency("alice", "eviction-key-1"); ok {
		t.Fatal("idempotency binding survived a new boot-scoped registry")
	}
}

func TestV1IdempotencyRejectsMalformedQueryBeforeExecution(t *testing.T) {
	s := &Server{deps: Deps{ManagedApplies: NewManagedApplyRegistry(0, 0)}}
	r := httptest.NewRequest(http.MethodPost, "http://example/api/v1/config/apply", strings.NewReader("[global]\n"))
	r.URL.RawQuery = "base_version=%zz"
	r.Pattern = "/api/v1/config/apply"
	r.Header.Set("Content-Type", "application/toml")
	r.Header.Set("Idempotency-Key", "malformed-query-key")
	ident := rbac.Identity{Principal: "alice", TokenID: "tok-1", Permissions: []rbac.Permission{rbac.ConfigApply}}
	r = r.WithContext(rbac.WithIdentity(r.Context(), ident))
	called := false
	rr := httptest.NewRecorder()
	s.runIdempotentCanonicalV1(rr, r, "v1", []byte("[global]\n"), func(http.ResponseWriter, *http.Request) { called = true })
	if called {
		t.Fatal("malformed query reached the canonical mutation")
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var env adminapi.Envelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != adminapi.CodeInvalidRequest || env.Error.Details.Field != "query" {
		t.Fatalf("error = %+v", env.Error)
	}
}
