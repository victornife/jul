// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"

	"jul/internal/redact"
)

// secretRefRE matches a single secret reference embedded in a configuration
// string: ${env:NAME} (an environment variable) or ${file:/path} (the contents
// of a file). A string may contain several references and surrounding literal
// text. The scheme and body are captured; an unknown scheme is reported as an
// error rather than silently passed through.
var secretRefRE = regexp.MustCompile(`\$\{(env|file|secret):([^}]*)\}`)

// secretRefAnyRE detects a reference of any scheme so that a malformed or
// unknown scheme can be flagged instead of being left in place verbatim (which
// would leak the literal "${...}" into, say, a token field).
var secretRefAnyRE = regexp.MustCompile(`\$\{([A-Za-z_]+):([^}]*)\}`)

// Resolve resolves secret references (${env:NAME}, ${file:/path}) in a deep
// copy of c and returns the expanded configuration, a self-contained redaction
// State covering all resolved values, and a digest map keyed by the original
// reference string (e.g. "${file:/path}") whose value is a SHA-256 digest of
// the bytes actually consumed. It does not mutate the live redaction registry.
//
// The returned redaction state should be installed with redact.Install only at
// the reload commit boundary. Validation and preflight can call Resolve and
// discard the state on failure.
//
// If c is already resolved (contains no secret references), Resolve returns a
// deep copy and an empty redaction state. This lets multiple consumers call
// Resolve on the same effective candidate without re-reading secret sources or
// changing the candidate between validation and publication (R6-07).
func Resolve(c *Config) (*Config, redact.State, map[string]string, error) {
	clone, err := c.Clone()
	if err != nil {
		return nil, redact.State{}, nil, fmt.Errorf("clone config for secret resolution: %w", err)
	}

	minLen := redact.DefaultMinLen
	if clone.Global.RedactMinSecretLength > 0 {
		minLen = clone.Global.RedactMinSecretLength
	}

	// Short-circuit if the config is already resolved: return the clone and an
	// empty redaction state so the candidate does not change between phases.
	if IsResolved(clone) {
		return clone, redact.NewState(nil, minLen), map[string]string{}, nil
	}

	var errs []error
	active := make(map[string]struct{})
	digests := make(map[string]string)

	walkConfigStrings(clone, func(s string) string {
		out, err := resolveSecretRefs(s, active, digests, minLen)
		if err != nil {
			errs = append(errs, err)
			return s
		}
		return out
	})

	if err := errors.Join(errs...); err != nil {
		return nil, redact.State{}, nil, err
	}

	values := make([]string, 0, len(active))
	for v := range active {
		values = append(values, v)
	}
	state := redact.NewState(values, minLen)
	return clone, state, digests, nil
}

// ExpandSecrets resolves secret references in every string field of c in place,
// replacing each reference with its resolved value, and installs the resulting
// redaction State as the live global state. Resolved values are masked from
// logs. It returns an error that joins every reference it could not resolve.
//
// It is invoked on the serving configuration just before the runtime is built
// (and on every reload). The on-disk and admin-facing representations keep the
// unresolved references, so secrets are never written back to disk or surfaced
// through the console.
//
// Deprecated: prefer Resolve for new code and call redact.Install only at the
// reload commit boundary.
func ExpandSecrets(c *Config) error {
	expanded, state, _, err := Resolve(c)
	if err != nil {
		return err
	}
	*c = *expanded
	redact.Install(state)
	return nil
}

// CountSecretRefs reports how many secret references appear across all string
// fields of the configuration. It is used by the console Status panel to show
// that secret references are in use without resolving (or exposing) them.
func CountSecretRefs(c *Config) int {
	n := 0
	walkConfigStrings(c, func(s string) string {
		n += len(secretRefRE.FindAllStringIndex(s, -1))
		return s
	})
	return n
}

// IsResolved reports whether c contains no unresolved secret references.
// A resolved config can be passed to consumers that would otherwise resolve
// again, ensuring one immutable effective candidate per transaction (R6-07).
func IsResolved(c *Config) bool {
	resolved := true
	walkConfigStrings(c, func(s string) string {
		if strings.Contains(s, "${") {
			resolved = false
		}
		return s
	})
	return resolved
}

// containsSecretRef reports whether s carries at least one supported secret
// reference (${env:...}, ${file:...}). Lint uses it to tell a literal secret
// from one already externalized.
func containsSecretRef(s string) bool {
	return secretRefRE.MatchString(s)
}

// resolveSecretRefs replaces every recognized secret reference in s with its
// resolved value, registering the value for log redaction and recording a
// digest of the consumed secret. A reference using an unknown scheme (e.g.
// ${vault:...}) is an error so typos fail loudly.
func resolveSecretRefs(s string, active map[string]struct{}, digests map[string]string, minLen int) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
	// Reject an unknown scheme up front so a typo (${evn:...}, ${vault:...})
	// fails loudly instead of leaking the literal "${...}" into a secret field.
	for _, m := range secretRefAnyRE.FindAllStringSubmatch(s, -1) {
		switch m[1] {
		case "env", "file", "secret":
		default:
			return s, fmt.Errorf("unknown secret reference scheme %q in %q (want env or file)", m[1], m[0])
		}
	}
	var resolveErr error
	out := secretRefRE.ReplaceAllStringFunc(s, func(match string) string {
		m := secretRefRE.FindStringSubmatch(match)
		scheme, body := m[1], strings.TrimSpace(m[2])
		val, err := resolveOne(scheme, body)
		if err != nil {
			if resolveErr == nil {
				resolveErr = err
			}
			return match
		}
		if len(val) >= minLen {
			active[val] = struct{}{}
		}
		digests[match] = digestBytes([]byte(val))
		return val
	})
	if resolveErr != nil {
		return s, resolveErr
	}
	return out, nil
}

// resolveOne resolves a single reference body for a scheme.
func resolveOne(scheme, body string) (string, error) {
	switch scheme {
	case "env":
		if body == "" {
			return "", errors.New("empty ${env:} reference (want ${env:NAME})")
		}
		val, ok := os.LookupEnv(body)
		if !ok {
			return "", fmt.Errorf("environment variable %q referenced by ${env:%s} is not set", body, body)
		}
		return val, nil
	case "file", "secret":
		if body == "" {
			return "", errors.New("empty ${file:} reference (want ${file:/path})")
		}
		data, err := os.ReadFile(body)
		if err != nil {
			return "", fmt.Errorf("reading ${file:%s}: %w", body, err)
		}
		// Trim a single trailing newline (and surrounding whitespace) so a
		// secret stored one-per-file does not carry the editor's newline.
		return strings.TrimRight(string(data), "\r\n"), nil
	default:
		return "", fmt.Errorf("unknown secret reference scheme %q (want env or file)", scheme)
	}
}

func digestBytes(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// walkConfigStrings visits every settable string in the configuration —
// including strings nested in structs, pointers, slices, arrays, and string-
// valued maps — and replaces each with fn(value). It uses reflection so new
// string fields are covered automatically without touching this walker.
func walkConfigStrings(c *Config, fn func(string) string) {
	walkValue(reflect.ValueOf(c), fn)
}

func walkValue(v reflect.Value, fn func(string) string) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			walkValue(v.Elem(), fn)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			walkValue(v.Field(i), fn)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkValue(v.Index(i), fn)
		}
	case reflect.Map:
		// Only string-valued maps carry secret references worth resolving (the
		// keys are field names, not values). Rebuild entries whose value changes.
		if v.Type().Elem().Kind() == reflect.String {
			for _, k := range v.MapKeys() {
				old := v.MapIndex(k).String()
				if nv := fn(old); nv != old {
					v.SetMapIndex(k, reflect.ValueOf(nv).Convert(v.Type().Elem()))
				}
			}
		}
	case reflect.String:
		if v.CanSet() {
			if nv := fn(v.String()); nv != v.String() {
				v.SetString(nv)
			}
		}
	default:
	}
}
