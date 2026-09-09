// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/sha256"
	"hash"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"jul/internal/adminapi"
)

// v1IdempotencyAdmission is coordination only, not retained idempotency state.
// The durable-within-a-boot binding lives exclusively on ManagedApplyRecord as
// required by ADR 0019 §27.1. Serializing this low-rate control-plane admission
// closes the pre-ledger race between two requests carrying the same key.
var v1IdempotencyAdmission sync.Mutex

type v1IdempotencyMetadata struct {
	Key         string
	Fingerprint [32]byte
	Method      string
	Operation   string
	Principal   string
}

func validV1IdempotencyKey(key string) bool {
	if len(key) < 8 || len(key) > 128 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// writeLengthPrefixed implements the ADR's decimal-byte-length grammar exactly.
func writeLengthPrefixed(h hash.Hash, value []byte) {
	_, _ = h.Write([]byte(strconv.Itoa(len(value))))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write(value)
}

// canonicalV1Query parses and percent-decodes the query, then sorts by
// (name,value) and emits each pair with independent decimal byte-length
// prefixes. There is deliberately no separator whose decoded occurrence could
// make two pair sets serialize identically.
func canonicalV1Query(raw string) ([]byte, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, err
	}
	type pair struct{ name, value string }
	pairs := make([]pair, 0)
	for name, vals := range values {
		if len(vals) == 0 {
			pairs = append(pairs, pair{name: name})
			continue
		}
		for _, value := range vals {
			pairs = append(pairs, pair{name: name, value: value})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].name == pairs[j].name {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].name < pairs[j].name
	})
	var b strings.Builder
	for _, p := range pairs {
		b.WriteString(strconv.Itoa(len([]byte(p.name))))
		b.WriteByte(':')
		b.WriteString(p.name)
		b.WriteString(strconv.Itoa(len([]byte(p.value))))
		b.WriteByte(':')
		b.WriteString(p.value)
	}
	return []byte(b.String()), nil
}

func v1RequestFingerprint(r *http.Request, body []byte) ([32]byte, *adminapi.Error) {
	query, err := canonicalV1Query(r.URL.RawQuery)
	if err != nil {
		return [32]byte{}, adminapi.Errorf(adminapi.CodeInvalidRequest, "query parameters are malformed").
			WithDetails(adminapi.Details{Field: "query"})
	}
	bodyDigest := sha256.Sum256(body)
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	parts := [][]byte{
		[]byte(r.Method),
		[]byte(path),
		query,
		[]byte(strings.TrimSpace(r.Header.Get("Content-Type"))),
		bodyDigest[:],
	}
	h := sha256.New()
	for _, part := range parts {
		writeLengthPrefixed(h, part)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func v1OperationTemplate(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return r.URL.Path
}

func (s *Server) v1IdempotencyMetadata(r *http.Request, body []byte) (*v1IdempotencyMetadata, *adminapi.Error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return nil, nil
	}
	if !validV1IdempotencyKey(key) {
		return nil, adminapi.Errorf(adminapi.CodeInvalidRequest,
			"Idempotency-Key must be 8-128 ASCII characters from [A-Za-z0-9_-]").
			WithDetails(adminapi.Details{Field: "Idempotency-Key"})
	}
	ident, ok := rbacIdentityFromRequest(r)
	if !ok || ident.Principal == "" {
		return nil, adminapi.Errorf(adminapi.CodeInternalError, "The authenticated principal identity is unavailable.")
	}
	fingerprint, apiErr := v1RequestFingerprint(r, body)
	if apiErr != nil {
		return nil, apiErr
	}
	return &v1IdempotencyMetadata{
		Key:         key,
		Fingerprint: fingerprint,
		Method:      r.Method,
		Operation:   v1OperationTemplate(r),
		Principal:   ident.Principal,
	}, nil
}

// runIdempotentCanonicalV1 executes a new mutation at most once for a retained
// principal/key binding. It returns true when it already wrote a fully projected
// v1 response (terminal replay); callers must then skip ordinary projection.
func (s *Server) runIdempotentCanonicalV1(
	w http.ResponseWriter,
	r *http.Request,
	baseVersion string,
	requestBody []byte,
	handler func(http.ResponseWriter, *http.Request),
) bool {
	meta, apiErr := s.v1IdempotencyMetadata(r, requestBody)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return false
	}
	if meta == nil {
		s.runCanonicalV1(w, r, baseVersion, handler)
		return false
	}
	if s.deps.ManagedApplies == nil {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInternalError,
			"Idempotency requires the managed apply ledger, which is unavailable."))
		return false
	}

	v1IdempotencyAdmission.Lock()
	defer v1IdempotencyAdmission.Unlock()

	if existing, ok := s.deps.ManagedApplies.FindIdempotency(meta.Principal, meta.Key); ok {
		if existing.IdempotencyFingerprint != meta.Fingerprint {
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeIdempotencyKeyReused,
				"Idempotency-Key is already bound to a different request.").WithDetails(adminapi.Details{
				RecordedMethod:    existing.IdempotencyMethod,
				RecordedOperation: existing.IdempotencyOperation,
			}))
			return false
		}
		if existing.State != ManagedApplyTerminal {
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeIdempotencyKeyInUse,
				"The idempotent operation is still in flight; poll its apply result.").
				WithDetails(adminapi.Details{ApplyID: existing.ID}))
			return false
		}
		response := s.v1ConfigApplyResponse(existing.Result, http.StatusOK)
		response.IdempotentReplay = true
		writeAPIJSON(w, http.StatusOK, response)
		return true
	}

	cap := newV1Capture()
	s.runCanonicalV1(cap, r, baseVersion, handler)
	if cap.status > 0 && cap.status < http.StatusBadRequest {
		applyID := extractV1ApplyID(cap.body.Bytes())
		if applyID != "" {
			if err := s.deps.ManagedApplies.BindIdempotency(
				applyID,
				meta.Key,
				meta.Fingerprint,
				meta.Method,
				meta.Operation,
				meta.Principal,
			); err != nil {
				cap.reset()
				writeAPIError(cap, r, adminapi.Errorf(adminapi.CodeInternalError,
					"The mutation completed but its idempotency binding could not be retained."))
			}
		}
	}
	writeCapturedV1(w, cap)
	return false
}

func extractV1ApplyID(body []byte) string {
	var result ConfigApplyResult
	if jsonErr := json.Unmarshal(body, &result); jsonErr == nil && result.ApplyID != "" {
		return result.ApplyID
	}
	return ""
}

func writeCapturedV1(w http.ResponseWriter, cap *v1Capture) {
	for key, values := range cap.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	status := cap.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(cap.body.Bytes())
}
