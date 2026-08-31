// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package adminapi defines the external, versioned admin API contract: the
// `/api/v1` error envelope, its bounded error-code catalogue and the request
// identifiers that correlate a response with the server log (ADR 0019 §26).
//
// It is deliberately free of handler, server and configuration dependencies so
// the OpenAPI generator and the contract tests can consume the same
// declarations the running server uses. The Go declarations here are the single
// source of the generated schema — there is no hand-maintained parallel
// document (ADR 0019 §29).
package adminapi

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// Code is a machine-readable external error code. It is the machine contract;
// the accompanying human message is not, and may change in any release
// (ADR 0019 §26).
type Code string

// The bounded external error-code catalogue (ADR 0019 §26). Adding a code is an
// additive API change; changing a code's meaning or status is breaking and
// requires /api/v2 (§25).
const (
	CodeInvalidRequest        Code = "invalid_request"
	CodeValidationFailed      Code = "validation_failed"
	CodeOperationFailed       Code = "operation_failed"
	CodeUnauthenticated       Code = "unauthenticated"
	CodeForbidden             Code = "forbidden"
	CodeInsecureTransport     Code = "insecure_transport"
	CodeNotFound              Code = "not_found"
	CodeConfigAuthorityRO     Code = "config_authority_read_only"
	CodeStaleBaseVersion      Code = "stale_base_version"
	CodeDriftDetected         Code = "drift_detected"
	CodePendingRestartConf    Code = "pending_restart_conflict"
	CodeRestartRequired       Code = "restart_required"
	CodeAdminReachabilityConf Code = "admin_reachability_confirmation_required"
	CodeIdempotencyKeyReused  Code = "idempotency_key_reused"
	CodeIdempotencyKeyInUse   Code = "idempotency_key_in_flight"
	CodePayloadTooLarge       Code = "payload_too_large"
	CodeUnsupportedMediaType  Code = "unsupported_media_type"
	CodeRateLimited           Code = "rate_limited"
	CodeInternalError         Code = "internal_error"
	CodeNotImplemented        Code = "not_implemented"
	CodeStorageUnavailable    Code = "storage_unavailable"
	CodeOperationTimeout      Code = "operation_timeout"
)

// CodeSpec is one row of the catalogue: the single HTTP status the code maps
// to, its documented meaning, and the exact set of `details` keys it may carry.
type CodeSpec struct {
	// Status is the one HTTP status this code maps to. A code never carries
	// more than one status (ADR 0019 §26 rule 2), which is why
	// payload_too_large and unsupported_media_type exist as separate codes
	// rather than being folded into invalid_request.
	Status int
	// Meaning is the documented condition, rendered into OpenAPI and
	// docs/admin-api.md. It is not the wire `message`.
	Meaning string
	// DetailKeys is the closed set of JSON keys this code's `details` object
	// may contain, in documented order. A code with no details has none.
	//
	// Every key names a field *path*, a bounded contract constant or a
	// caller-supplied identifier. None of them is a value read from the
	// configuration (§26 rule 3).
	DetailKeys []string
}

// catalog is the authoritative code table. Nothing outside this file adds to
// it; the contract test asserts the constants above and this map agree.
var catalog = map[Code]CodeSpec{
	CodeInvalidRequest: {
		Status:     http.StatusBadRequest,
		Meaning:    "The request was malformed: an unparseable body, an unknown field, a bad parameter or a missing required parameter.",
		DetailKeys: []string{"field"},
	},
	CodeValidationFailed: {
		Status:     http.StatusBadRequest,
		Meaning:    "The candidate configuration is not valid. Findings carry the exact configuration path that failed.",
		DetailKeys: []string{"errors"},
	},
	CodeOperationFailed: {
		Status:     http.StatusBadRequest,
		Meaning:    "A typed patch operation was rejected. The index identifies which operation in the batch failed.",
		DetailKeys: []string{"op_index", "op", "errors"},
	},
	CodeUnauthenticated: {
		Status:  http.StatusUnauthorized,
		Meaning: "No credential was presented, or the credential is not valid. Carries no signal about whether the addressed resource exists.",
	},
	CodeForbidden: {
		Status:     http.StatusForbidden,
		Meaning:    "The principal is authenticated but does not hold the required permission. The check runs before the resource is looked up, so this carries no existence signal either.",
		DetailKeys: []string{"required_permission"},
	},
	CodeInsecureTransport: {
		Status: http.StatusForbidden,
		Meaning: "The request arrived in cleartext on a listener that is neither TLS-terminated nor bound to loopback, " +
			"on a route that consumes an admin credential. Rejected before route lookup and before authentication.",
		// `required` only. An earlier draft returned the listen address, which
		// is a configuration value, returned before authentication. `required`
		// is a constant of the contract rather than a fact about this server
		// (§26 rule 3).
		DetailKeys: []string{"required"},
	},
	CodeNotFound: {
		Status:     http.StatusNotFound,
		Meaning:    "The addressed resource does not exist. Returned only to a caller that already holds the required permission.",
		DetailKeys: []string{"kind", "id"},
	},
	CodeConfigAuthorityRO: {
		Status: http.StatusConflict,
		Meaning: "The server is in file_owned authority and does not write configuration. This is a property of the server, " +
			"identical for every principal including a wildcard admin, which is why it is 409 and not 403.",
		DetailKeys: []string{"config_authority", "config_authority_source"},
	},
	CodeStaleBaseVersion: {
		Status:     http.StatusConflict,
		Meaning:    "Optimistic-concurrency failure: the configuration changed since base_version was observed.",
		DetailKeys: []string{"base_version", "current_version"},
	},
	CodeDriftDetected: {
		Status: http.StatusConflict,
		Meaning: "Managed authority with an external write present on disk — including an adoption whose bytes changed " +
			"under it at the verification fence.",
		DetailKeys: []string{"baseline_version", "disk_version", "detected_at", "observed_digest"},
	},
	CodePendingRestartConf: {
		Status:     http.StatusConflict,
		Meaning:    "A staged planned restart blocks this operation.",
		DetailKeys: []string{"pending_restart"},
	},
	CodeRestartRequired: {
		Status:     http.StatusConflict,
		Meaning:    "The candidate cannot be hot-applied. The named subsystems require a restart.",
		DetailKeys: []string{"subsystems", "can_stage"},
	},
	CodeAdminReachabilityConf: {
		Status:     http.StatusConflict,
		Meaning:    "The change would alter admin reachability and must be confirmed explicitly.",
		DetailKeys: []string{"changes"},
	},
	CodeIdempotencyKeyReused: {
		Status: http.StatusConflict,
		Meaning: "The Idempotency-Key matches a recorded operation with a different request fingerprint. " +
			"The recorded operation is named by its route template, never by a concrete path.",
		DetailKeys: []string{"recorded_method", "recorded_operation"},
	},
	CodeIdempotencyKeyInUse: {
		Status:     http.StatusConflict,
		Meaning:    "The Idempotency-Key matches an operation that has not reached a terminal state. Poll the named apply_id rather than retrying.",
		DetailKeys: []string{"apply_id"},
	},
	CodePayloadTooLarge: {
		Status:     http.StatusRequestEntityTooLarge,
		Meaning:    "The request body exceeded the admin body cap. The cap applies to every body-bearing /api/v1 request, not only mutations.",
		DetailKeys: []string{"limit_bytes"},
	},
	CodeUnsupportedMediaType: {
		Status:     http.StatusUnsupportedMediaType,
		Meaning:    "The request Content-Type is not accepted for this operation.",
		DetailKeys: []string{"accepted"},
	},
	CodeRateLimited: {
		Status:     http.StatusTooManyRequests,
		Meaning:    "The admin rate limit was exceeded. retry_after_seconds is a requirement, and is also sent as Retry-After.",
		DetailKeys: []string{"retry_after_seconds"},
	},
	CodeInternalError: {
		Status:  http.StatusInternalServerError,
		Meaning: "An unexpected server failure. The request_id correlates the response with the server log, which holds the detail this response deliberately does not.",
	},
	CodeNotImplemented: {
		Status:     http.StatusNotImplemented,
		Meaning:    "The capability is not compiled into this build. Returned instead of 404 so a client never has to infer capability from a missing route.",
		DetailKeys: []string{"capability"},
	},
	CodeStorageUnavailable: {
		Status:  http.StatusServiceUnavailable,
		Meaning: "The configuration or history store cannot be read or written.",
	},
	CodeOperationTimeout: {
		Status:     http.StatusGatewayTimeout,
		Meaning:    "reload_timeout was exceeded before the operation reached a terminal state.",
		DetailKeys: []string{"timed_out_phase"},
	},
}

// Spec returns the catalogue row for c. ok is false for a code that is not in
// the catalogue, which is how the contract test proves the set is closed.
func Spec(c Code) (CodeSpec, bool) {
	s, ok := catalog[c]
	return s, ok
}

// Status returns the single HTTP status c maps to, or 0 when c is not a
// catalogue member. Callers that must not fail open should check Spec instead.
func (c Code) Status() int { return catalog[c].Status }

// String makes Code printable without a conversion at every call site.
func (c Code) String() string { return string(c) }

// Codes returns every catalogue code in lexical order. The order is stable so
// generated artifacts and documentation are deterministic.
func Codes() []Code {
	out := make([]Code, 0, len(catalog))
	for c := range catalog {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Finding is the per-field validation finding shape. It is the existing
// five-field shape from internal/admin/humanerrors.go, preserved exactly so
// Console error-to-field attachment and ADR 0018's exact predicate paths
// (servers[0].locations[2].match.headers[1]) keep working unchanged
// (ADR 0019 §26 rule 5).
type Finding struct {
	Code     string `json:"code"`
	Path     string `json:"path,omitempty"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
	Severity string `json:"severity"`
}

// Details is the bounded, per-code detail object. It is a closed struct rather
// than a free map so a leak of a configuration value is a compile error rather
// than a review finding: every field below is a field *path*, a bounded
// contract constant, or an identifier the caller itself supplied.
//
// No field here ever carries candidate bytes, a resolved secret, a token, or a
// value read from a configuration field (ADR 0019 §26 rule 3).
type Details struct {
	// invalid_request
	Field string `json:"field,omitempty"`
	// validation_failed, operation_failed
	Errors []Finding `json:"errors,omitempty"`
	// operation_failed
	OpIndex *int   `json:"op_index,omitempty"`
	Op      string `json:"op,omitempty"`
	// forbidden
	RequiredPermission string `json:"required_permission,omitempty"`
	// insecure_transport
	Required string `json:"required,omitempty"`
	// not_found
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"`
	// config_authority_read_only
	ConfigAuthority       string `json:"config_authority,omitempty"`
	ConfigAuthoritySource string `json:"config_authority_source,omitempty"`
	// stale_base_version
	BaseVersion    string `json:"base_version,omitempty"`
	CurrentVersion string `json:"current_version,omitempty"`
	// drift_detected
	BaselineVersion string `json:"baseline_version,omitempty"`
	DiskVersion     string `json:"disk_version,omitempty"`
	DetectedAt      string `json:"detected_at,omitempty"`
	ObservedDigest  string `json:"observed_digest,omitempty"`
	// pending_restart_conflict
	PendingRestart *bool `json:"pending_restart,omitempty"`
	// restart_required
	Subsystems []string `json:"subsystems,omitempty"`
	CanStage   *bool    `json:"can_stage,omitempty"`
	// admin_reachability_confirmation_required
	Changes []string `json:"changes,omitempty"`
	// rate_limited
	RetryAfterSeconds *int `json:"retry_after_seconds,omitempty"`
	// payload_too_large
	LimitBytes *int64 `json:"limit_bytes,omitempty"`
	// unsupported_media_type
	Accepted []string `json:"accepted,omitempty"`
	// idempotency_key_reused
	RecordedMethod string `json:"recorded_method,omitempty"`
	// RecordedOperation is the route *template* — /api/v1/routes/{route_id} —
	// never the routed path, which would contain a route_id or a listener
	// address (ADR 0019 §27.1).
	RecordedOperation string `json:"recorded_operation,omitempty"`
	// idempotency_key_in_flight
	ApplyID string `json:"apply_id,omitempty"`
	// not_implemented
	Capability string `json:"capability,omitempty"`
	// operation_timeout
	TimedOutPhase string `json:"timed_out_phase,omitempty"`
}

// Body is the error object carried by Envelope.
type Body struct {
	Code    Code    `json:"code"`
	Message string  `json:"message"`
	Details Details `json:"details,omitzero"`
	// RequestID correlates this response with the server log and is echoed in
	// the X-Request-ID response header. It is always server-minted: a
	// client-supplied value is never reflected, so the field cannot be used to
	// forge a log correlation or to smuggle bytes into an operator's terminal.
	RequestID string `json:"request_id"`
}

// Envelope is the one shape every /api/v1 response that is not a success takes
// (ADR 0019 §26).
type Envelope struct {
	Error Body `json:"error"`
}

// Error is the internal carrier a handler returns; WriteError renders it as an
// Envelope. It implements error so it can travel through ordinary Go error
// plumbing, but its Code — never the wrapped Go error — is the wire contract
// (ADR 0019 §26 rule 1).
type Error struct {
	Code    Code
	Message string
	Details Details
}

// Errorf builds an Error with a formatted human message.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// New builds an Error with the catalogue's documented meaning as its message.
// Callers with something more useful to say should use Errorf.
func New(code Code) *Error {
	return &Error{Code: code, Message: catalog[code].Meaning}
}

// WithDetails returns a copy of e carrying d. It returns a copy so a package
// level sentinel can be specialised at a call site without being mutated.
func (e *Error) WithDetails(d Details) *Error {
	out := *e
	out.Details = d
	return &out
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Status returns the HTTP status e maps to. An Error carrying a code outside
// the catalogue is a programming error and is reported as 500 rather than as a
// zero status, so a mistake fails visibly instead of writing a malformed
// response.
func (e *Error) Status() int {
	if s, ok := catalog[e.Code]; ok {
		return s.Status
	}
	return http.StatusInternalServerError
}

// NewRequestID mints a server-side correlation identifier: a 26-character
// Crockford base32 ULID — 48 bits of millisecond timestamp followed by 80 bits
// of randomness. It is lexically sortable by mint time, contains no host,
// path or configuration value, and is never derived from client input.
func NewRequestID() string { return newRequestIDAt(time.Now()) }

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func newRequestIDAt(now time.Time) string {
	var b [16]byte
	ms := uint64(now.UTC().UnixMilli())
	// 48-bit big-endian timestamp.
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	binary.BigEndian.PutUint32(b[2:6], uint32(ms))
	// crypto/rand.Read never returns an error as of Go 1.24; it panics on a
	// failing entropy source, which is the correct outcome for a server that
	// cannot generate unpredictable identifiers.
	_, _ = rand.Read(b[6:])

	out := make([]byte, 26)
	// 128 bits render as 26 base32 characters with the first carrying only the
	// top 2 bits, so the timestamp half stays byte-aligned and sortable.
	out[0] = crockford[(b[0]&0xE0)>>5]
	var acc, bits uint32
	i := 1
	acc, bits = uint32(b[0]&0x1F), 5
	for _, c := range b[1:] {
		acc = acc<<8 | uint32(c)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[i] = crockford[(acc>>bits)&0x1F]
			i++
		}
	}
	return string(out)
}
