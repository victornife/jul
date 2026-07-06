// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package redact maintains a process-wide registry of secret values so they can
// be masked wherever the server emits text an operator might see — primarily
// logs. Secret references resolved from the configuration (see SEC-1 in
// internal/config) register their plaintext here; a redacting io.Writer wrapped
// around the log sink then replaces any occurrence with a fixed mask.
//
// The registry is replaced wholesale on every config reload (via Replace) so
// deleted secrets stop being masked and new ones are added. It is safe for
// concurrent use and deliberately ignores very short values to avoid masking
// incidental substrings.
package redact

import (
	"io"
	"strings"
	"sync"
)

// Mask is the replacement written in place of a registered secret.
const Mask = "***"

// DefaultMinLen is the default shortest value the registry will mask. Masking
// one- or two-character values would corrupt unrelated log text, so short
// secrets are left unregistered (a token that short is not meaningfully secret
// anyway). SetMinLen can lower or raise this floor from the configuration.
const DefaultMinLen = 4

var (
	mu      sync.RWMutex
	minLen  = DefaultMinLen
	secrets = map[string]struct{}{}
)

// SetMinLen sets the shortest value Add will mask. It is applied from the
// configuration during secret resolution (and on every reload), so an operator
// whose secrets are shorter than the default can opt into masking them at the
// cost of possibly masking incidental short substrings of log text. A value
// below 1 restores DefaultMinLen. Safe for concurrent use; intended to be
// called before secrets are registered.
func SetMinLen(n int) {
	if n < 1 {
		n = DefaultMinLen
	}
	mu.Lock()
	minLen = n
	mu.Unlock()
}

// Add registers a secret value to be masked from redacted output. Empty or very
// short values (shorter than the current floor; see SetMinLen) are ignored. It
// is safe for concurrent use and idempotent.
func Add(value string) {
	mu.Lock()
	defer mu.Unlock()
	if len(value) < minLen {
		return
	}
	secrets[value] = struct{}{}
}

// Count returns the number of distinct registered secret values.
func Count() int {
	mu.RLock()
	defer mu.RUnlock()
	return len(secrets)
}

// Apply returns s with every registered secret value replaced by Mask. When no
// secrets are registered it returns s unchanged (the common case, so the hot
// path takes only a read lock and no allocation).
func Apply(s string) string {
	mu.RLock()
	defer mu.RUnlock()
	if len(secrets) == 0 {
		return s
	}
	for v := range secrets {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, Mask)
		}
	}
	return s
}

// Writer wraps w so that every write has registered secrets masked first. It is
// intended to wrap a log sink (e.g. os.Stderr) so secret values never reach the
// logs even if a message or attribute interpolates one. slog's handlers emit a
// whole record per Write, so masking per Write does not split a secret across
// calls.
func Writer(w io.Writer) io.Writer { return &redactingWriter{w: w} }

type redactingWriter struct{ w io.Writer }

// Write masks registered secrets in p and forwards the result. It always
// reports len(p) bytes consumed (the caller's view) so callers that check
// n == len(p) are satisfied even when masking changes the byte length.
func (rw *redactingWriter) Write(p []byte) (int, error) {
	out := Apply(string(p))
	if out == string(p) {
		return rw.w.Write(p)
	}
	if _, err := rw.w.Write([]byte(out)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Replace replaces the entire secret registry with newSecrets.
// It is used on config reload to prune secrets that were deleted from the
// configuration so their values no longer get redacted from logs.
func Replace(newSecrets map[string]struct{}) {
	mu.Lock()
	defer mu.Unlock()
	secrets = newSecrets
}
