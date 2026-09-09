// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"jul/internal/adminapi"
)

type v1IdempotencyKey struct {
	BootID    string
	Principal string
	Key       string
}

type v1IdempotencyRecord struct {
	Fingerprint [32]byte
	Method      string
	Operation   string
	ApplyID     string
	Status      int
	Header      http.Header
	Body        []byte
	Terminal    bool
	CreatedAt   time.Time
	CompletedAt time.Time
	Ready       chan struct{}
	readyClosed bool
}

var v1IdempotencyStore = struct {
	sync.Mutex
	records map[v1IdempotencyKey]*v1IdempotencyRecord
}{records: map[v1IdempotencyKey]*v1IdempotencyRecord{}}

func validV1IdempotencyKey(key string) bool {
	if len(key) < 8 || len(key) > 128 {
		return false
	}
	for i := range len(key) {
		c := key[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func canonicalV1ContentType(raw string) string {
	mediaType, params, err := mime.ParseMediaType(raw)
	if err != nil || mediaType == "" {
		return strings.TrimSpace(raw)
	}
	return mime.FormatMediaType(strings.ToLower(mediaType), params)
}

func v1RequestFingerprint(r *http.Request, body []byte) ([32]byte, *adminapi.Error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return [32]byte{}, adminapi.Errorf(adminapi.CodeInvalidRequest, "query parameters are malformed").WithDetails(adminapi.Details{Field: "query"})
	}
	bodyDigest := sha256.Sum256(body)
	parts := [][]byte{
		[]byte(r.Method),
		[]byte(r.URL.EscapedPath()),
		[]byte(values.Encode()),
		[]byte(canonicalV1ContentType(r.Header.Get("Content-Type"))),
		bodyDigest[:],
	}
	h := sha256.New()
	var length [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = h.Write(length[:])
		_, _ = h.Write(part)
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

func (s *Server) beginV1Idempotency(r *http.Request, body []byte) (*v1IdempotencyRecord, *adminapi.Error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return nil, nil
	}
	if !validV1IdempotencyKey(key) {
		return nil, adminapi.Errorf(adminapi.CodeInvalidRequest,
			"Idempotency-Key must be 8-128 ASCII characters from [A-Za-z0-9_-]").WithDetails(adminapi.Details{Field: "Idempotency-Key"})
	}
	ident, ok := rbacIdentityFromRequest(r)
	if !ok || ident.Principal == "" {
		return nil, adminapi.Errorf(adminapi.CodeInternalError, "The authenticated principal identity is unavailable.")
	}
	fingerprint, apiErr := v1RequestFingerprint(r, body)
	if apiErr != nil {
		return nil, apiErr
	}
	storeKey := v1IdempotencyKey{BootID: s.bootID(), Principal: ident.Principal, Key: key}

	for {
		v1IdempotencyStore.Lock()
		s.pruneV1IdempotencyLocked(time.Now())
		existing := v1IdempotencyStore.records[storeKey]
		if existing == nil {
			rec := &v1IdempotencyRecord{
				Fingerprint: fingerprint,
				Method: r.Method,
				Operation: v1OperationTemplate(r),
				CreatedAt: time.Now(),
				Ready: make(chan struct{}),
			}
			v1IdempotencyStore.records[storeKey] = rec
			v1IdempotencyStore.Unlock()
			return rec, nil
		}
		if existing.Fingerprint != fingerprint {
			details := adminapi.Details{RecordedMethod: existing.Method, RecordedOperation: existing.Operation}
			v1IdempotencyStore.Unlock()
			return nil, adminapi.Errorf(adminapi.CodeIdempotencyKeyReused,
				"Idempotency-Key is already bound to a different request.").WithDetails(details)
		}
		if existing.Terminal || existing.ApplyID != "" {
			v1IdempotencyStore.Unlock()
			return existing, nil
		}
		ready := existing.Ready
		v1IdempotencyStore.Unlock()
		select {
		case <-ready:
			continue
		case <-r.Context().Done():
			return nil, adminapi.Errorf(adminapi.CodeOperationTimeout, "The in-flight idempotent operation did not publish its apply identity before this request ended.")
		}
	}
}

func (s *Server) pruneV1IdempotencyLocked(now time.Time) {
	retention := s.ledgerRetention()
	terminal := 0
	for _, rec := range v1IdempotencyStore.records {
		if rec.Terminal {
			terminal++
		}
	}
	if terminal <= retention.MinTerminalRecords {
		return
	}
	minAge := time.Duration(retention.MinAgeSeconds) * time.Second
	for key, rec := range v1IdempotencyStore.records {
		if terminal <= retention.MinTerminalRecords {
			break
		}
		if !rec.Terminal || rec.CompletedAt.IsZero() || now.Sub(rec.CompletedAt) < minAge {
			continue
		}
		delete(v1IdempotencyStore.records, key)
		terminal--
	}
}

func completeV1Idempotency(rec *v1IdempotencyRecord, cap *v1Capture) {
	if rec == nil {
		return
	}
	applyID := extractV1ApplyID(cap.body.Bytes())
	v1IdempotencyStore.Lock()
	rec.ApplyID = applyID
	rec.Status = cap.status
	rec.Header = cap.header.Clone()
	rec.Body = append(rec.Body[:0], cap.body.Bytes()...)
	// A 202 with an apply id is deliberately non-terminal. A later duplicate is
	// told to poll that exact transaction instead of re-executing the mutation.
	rec.Terminal = cap.status != http.StatusAccepted || applyID == ""
	if rec.Terminal {
		rec.CompletedAt = time.Now()
	}
	if !rec.readyClosed {
		close(rec.Ready)
		rec.readyClosed = true
	}
	v1IdempotencyStore.Unlock()
}

func extractV1ApplyID(body []byte) string {
	var decoded map[string]any
	if json.Unmarshal(body, &decoded) != nil {
		return ""
	}
	if id, _ := decoded["apply_id"].(string); id != "" {
		return id
	}
	if reload, ok := decoded["reload"].(map[string]any); ok {
		if id, _ := reload["id"].(string); id != "" {
			return id
		}
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
	w.WriteHeader(cap.status)
	_, _ = w.Write(cap.body.Bytes())
}

func replayV1Idempotency(w http.ResponseWriter, r *http.Request, rec *v1IdempotencyRecord) {
	if !rec.Terminal {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeIdempotencyKeyInUse,
			"The idempotent operation is still in flight; poll its apply result.").WithDetails(adminapi.Details{ApplyID: rec.ApplyID}))
		return
	}
	body := append([]byte(nil), rec.Body...)
	var env adminapi.Envelope
	if json.Unmarshal(body, &env) == nil && env.Error.Code != "" {
		if requestID, ok := externalContract(r.Context()); ok {
			env.Error.RequestID = requestID
			body, _ = json.Marshal(env)
			body = append(body, '\n')
		}
	}
	for key, values := range rec.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Idempotent-Replay", "true")
	w.WriteHeader(rec.Status)
	_, _ = w.Write(body)
}

func (s *Server) runIdempotentCanonicalV1(w http.ResponseWriter, r *http.Request, baseVersion string, requestBody []byte, handler func(http.ResponseWriter, *http.Request)) {
	rec, apiErr := s.beginV1Idempotency(r, requestBody)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	if rec == nil {
		s.runCanonicalV1(w, r, baseVersion, handler)
		return
	}

	v1IdempotencyStore.Lock()
	alreadyRan := rec.Status != 0 || rec.ApplyID != "" || rec.Terminal
	v1IdempotencyStore.Unlock()
	if alreadyRan {
		replayV1Idempotency(w, r, rec)
		return
	}

	cap := newV1Capture()
	s.runCanonicalV1(cap, r, baseVersion, handler)
	completeV1Idempotency(rec, cap)
	writeCapturedV1(w, cap)
}

func v1FingerprintHex(r *http.Request, body []byte) string {
	fingerprint, err := v1RequestFingerprint(r, body)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(fingerprint[:])
}
