// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package redact maintains a process-wide registry of secret values so they can
// be masked wherever the server emits text an operator might see — primarily
// logs. Secret references resolved from the configuration (see SEC-1 in
// internal/config) register their plaintext here; a redacting io.Writer wrapped
// around the log sink then replaces any occurrence with a fixed mask.
//
// The registry is replaced wholesale on every config reload (via Replace or,
// preferentially, Install) so deleted secrets stop being masked and new ones
// are added. It is safe for concurrent use and deliberately ignores very short
// values to avoid masking incidental substrings.
//
// New code should construct a redact.State during secret resolution and call
// Install only at the reload commit boundary. The legacy Add/Replace/Snapshot
// mutators remain for callers that have not yet migrated.
package redact

import (
	"io"
	"sync/atomic"
)

// Mask is the replacement written in place of a registered secret.
const Mask = "***"

// DefaultMinLen is the default shortest value the registry will mask. Masking
// one- or two-character values would corrupt unrelated log text, so short
// secrets are left unregistered (a token that short is not meaningfully secret
// anyway). SetMinLen can lower or raise this floor from the configuration.
const DefaultMinLen = 4

var live atomic.Pointer[State]

func init() {
	empty := EmptyState()
	live.Store(&empty)
}

// Global returns the currently installed redaction state. Callers that need to
// redact during request handling should use this snapshot so they observe the
// generation that was active when the request began.
func Global() State {
	return *live.Load()
}

// Install atomically replaces the live redaction state with s. This is the
// only mutation that should happen on a successful reload; validation and
// preflight must build a State and discard it on failure rather than mutate
// the live registry.
func Install(s State) {
	live.Store(&s)
}

// SetMinLen sets the shortest value Add will mask. It is applied from the
// configuration during secret resolution (and on every reload), so an operator
// whose secrets are shorter than the default can opt into masking them at the
// cost of possibly masking incidental short substrings of log text. A value
// below 1 restores DefaultMinLen.
//
// Deprecated: build a redact.State and call Install at reload commit time.
func SetMinLen(n int) {
	Install(Global().WithMinLen(n))
}

// Add registers a secret value to be masked from redacted output. Empty or very
// short values (shorter than the current floor; see SetMinLen) are ignored. It
// is safe for concurrent use and idempotent.
//
// Deprecated: build a redact.State and call Install at reload commit time.
func Add(value string) {
	Install(Global().WithValue(value))
}

// Count returns the number of distinct registered secret values.
func Count() int {
	return Global().Count()
}

// Apply returns s with every registered secret value replaced by Mask. When no
// secrets are registered it returns s unchanged (the common case, so the hot
// path takes only a read lock and no allocation).
func Apply(s string) string {
	return Global().Apply(s)
}

// dynamicRedactingWriter masks using the current global redaction state on
// every write. This is essential for long-lived log sinks created before
// startup secrets are installed: a writer that captured a snapshot at
// construction time would never see secrets added by later redact.Install calls.
type dynamicRedactingWriter struct{ w io.Writer }

func (rw *dynamicRedactingWriter) Write(p []byte) (int, error) {
	out := Global().Apply(string(p))
	if out == string(p) {
		return rw.w.Write(p)
	}
	if _, err := rw.w.Write([]byte(out)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Writer wraps w so that every write has registered secrets masked first. It is
// intended to wrap a log sink (e.g. os.Stderr) so secret values never reach the
// logs even if a message or attribute interpolates one. slog's handlers emit a
// whole record per Write, so masking per Write does not split a secret across
// calls.
//
// The returned writer reads the live global state on each Write so secret
// rotation via Install takes effect immediately.
func Writer(w io.Writer) io.Writer { return &dynamicRedactingWriter{w: w} }


// Replace replaces the entire secret registry with newSecrets.
// It is used on config reload to prune secrets that were deleted from the
// configuration so their values no longer get redacted from logs.
//
// Deprecated: build a redact.State and call Install at reload commit time.
func Replace(newSecrets map[string]struct{}) {
	values := make([]string, 0, len(newSecrets))
	for v := range newSecrets {
		values = append(values, v)
	}
	Install(NewState(values, Global().MinLen()))
}

// Snapshot returns a shallow copy of the current secret registry. Use it
// together with Restore to ensure a rejected validation or preflight candidate
// cannot permanently update the registry with its secrets when the serving
// config's secrets should remain authoritative.
//
// Deprecated: build a redact.State and call Install at reload commit time.
func Snapshot() map[string]struct{} {
	s := Global()
	snap := make(map[string]struct{}, len(s.values))
	for k := range s.values {
		snap[k] = struct{}{}
	}
	return snap
}

// Restore replaces the registry with a previously captured snapshot. It is
// equivalent to Replace but signals intent: this is a rollback, not a forward
// update. Pass the result of Snapshot; passing nil clears the registry.
//
// Deprecated: build a redact.State and call Install at reload commit time.
func Restore(snapshot map[string]struct{}) {
	if snapshot == nil {
		Install(EmptyState())
		return
	}
	Replace(snapshot)
}
