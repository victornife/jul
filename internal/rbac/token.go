// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package rbac

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	// MinTokenLen is the minimum acceptable raw token length (bytes). Tokens
	// shorter than this fail config validation with a lint-level warning.
	MinTokenLen = 32
	// tokenIDLen is the number of hex chars taken from the SHA-256 hash to
	// form the public TokenID. 12 hex chars = 6 bytes = 48 bits — enough for
	// logging/audit without exposing secrets.
	tokenIDLen = 12
	// bearerPrefix is the expected Authorization header prefix (with trailing space).
	bearerPrefix = "Bearer "
)

// hashToken returns the SHA-256 hash of the raw token.
// The raw token is never retained as a map key or in logs.
func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// tokenID derives the public, non-secret token identifier from its hash.
// It is the first tokenIDLen hex characters of the SHA-256 digest.
func tokenID(hash []byte) string {
	return hex.EncodeToString(hash)[:tokenIDLen]
}

// extractBearer returns the raw token from an Authorization header value,
// stripping the "Bearer " prefix. It normalizes only the prefix, never the
// token bytes themselves.
func extractBearer(h string) (string, bool) {
	if !strings.HasPrefix(h, bearerPrefix) {
		return "", false
	}
	tok := h[len(bearerPrefix):]
	if tok == "" {
		return "", false
	}
	return tok, true
}

// principalEntry is the internal per-principal record held by a built Policy.
type principalEntry struct {
	name        string
	role        string
	tokenHash   []byte
	tokenID     string
	permissions []Permission
	disabled    bool
	expiresAt   time.Time // zero means no expiry
	legacy      bool
}

// expired reports whether this entry should be treated as inactive at the given time.
func (e *principalEntry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// active reports whether this entry can authenticate at the given time.
func (e *principalEntry) active(now time.Time) bool {
	return !e.disabled && !e.expired(now)
}

// matchToken returns true iff the provided raw token (after hashing) matches
// this entry using constant-time comparison.
func (e *principalEntry) matchToken(raw string) bool {
	if len(e.tokenHash) == 0 {
		return false
	}
	h := hashToken(raw)
	return subtle.ConstantTimeCompare(h, e.tokenHash) == 1
}

// ValidateToken returns a descriptive error if the raw token is too short or
// empty. It does NOT hash the token; use this at validation time only.
func ValidateToken(raw string) error {
	if raw == "" {
		return fmt.Errorf("token must not be empty")
	}
	if len(raw) < MinTokenLen {
		return fmt.Errorf("token is too short (%d chars); minimum is %d for entropy", len(raw), MinTokenLen)
	}
	return nil
}
