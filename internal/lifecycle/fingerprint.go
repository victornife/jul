// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"jul/internal/config"
)

var initialGOMAXPROCS = runtime.GOMAXPROCS(0)

// InitialGOMAXPROCS returns the GOMAXPROCS value in effect before the server
// applied any worker_threads cap. It resolves "auto" back to the original
// container-aware default.
func InitialGOMAXPROCS() int { return initialGOMAXPROCS }

// Fingerprint captures the effective values of the startup-consumed
// configuration fields. It is computed from the expanded configuration (secret
// references already resolved) and stored on the live server after startup; a
// later reload whose fingerprint differs on a startup-consumed path must be
// rejected as restart-required.
//
// Values never contain secret material: secret-bearing paths are stored as
// digests, and certificate, key, CA and CRL material is stored as a digest of
// the bytes the listener would load.
type Fingerprint struct {
	// Values maps a startup-consumed registry path to its effective value or
	// digest, in the same dot notation as the registry.
	Values map[string]any `json:"values"`
}

// EmptyFingerprint returns a fingerprint with no values.
func EmptyFingerprint() Fingerprint {
	return Fingerprint{Values: map[string]any{}}
}

// ComputeFingerprint builds a fingerprint from the expanded effective config.
func ComputeFingerprint(cfg *config.Config) Fingerprint {
	fp := EmptyFingerprint()
	for _, e := range StartupFields() {
		v, ok := EffectiveValue(cfg, e.Path)
		if !ok {
			// Unreachable while the registry drives the extractor map; failing
			// closed here keeps a future refactor from silently dropping a
			// startup-consumed path out of the comparison.
			continue
		}
		fp.Values[e.Path] = v
	}
	return fp
}

// Diff returns the startup-consumed paths whose effective value differs between
// two fingerprints, comparing every path literally.
func Diff(a, b Fingerprint) []string {
	return diffStartup(a, b, false)
}

// DiffAddressAware returns the startup-consumed paths that differ between two
// fingerprints, comparing address-keyed paths per listen address so that adding
// or removing a listener does not produce a restart-required verdict for an
// address nobody edited.
func DiffAddressAware(a, b Fingerprint) []string {
	return diffStartup(a, b, true)
}

func diffStartup(a, b Fingerprint, addressAware bool) []string {
	var out []string
	for _, e := range StartupFields() {
		av, ok1 := a.Values[e.Path]
		bv, ok2 := b.Values[e.Path]
		if !ok1 || !ok2 {
			out = append(out, e.Path)
			continue
		}
		if addressAware && e.AddressKeyed {
			if diffAddressKeyed(av, bv) {
				out = append(out, e.Path)
			}
			continue
		}
		if addressAware && e.CollectionKeyed {
			if commonKeysDiffer(av, bv) {
				out = append(out, e.Path)
			}
			continue
		}
		if !deepEqualValues(av, bv) {
			out = append(out, e.Path)
		}
	}
	return out
}

// RestartRequired returns the first startup-consumed path that differs between
// the startup and candidate fingerprints, with the reason recorded in the
// registry.
func RestartRequired(startup, candidate Fingerprint) (string, bool) {
	paths := DiffAddressAware(startup, candidate)
	if len(paths) == 0 {
		return "", false
	}
	e, ok := Lookup(paths[0])
	if !ok {
		return fmt.Sprintf("%s changed", paths[0]), true
	}
	return fmt.Sprintf("%s changed (%s)", e.Path, e.Reason), true
}

// diffAddressKeyed reports whether two address-keyed maps differ for an address
// present in both. An address that only exists on one side was added or removed
// rather than edited, and the listener diff already handles it.
func diffAddressKeyed(startup, candidate any) bool {
	om, ok1 := startup.(map[string]any)
	nm, ok2 := candidate.(map[string]any)
	if !ok1 || !ok2 {
		return true
	}
	for addr, sv := range om {
		cv, ok := nm[addr]
		if !ok {
			continue // listener removed; no longer relevant
		}
		if !deepEqualValues(sv, cv) {
			return true
		}
	}
	return false
}

// deepEqualValues compares two normalized values. Only the shapes the
// extractors produce are handled explicitly; anything else falls back to a
// string comparison so an unexpected type still compares deterministically.
func deepEqualValues(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case int:
		bv, ok := b.(int)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case []string:
		bv, ok := b.([]string)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if av[i] != bv[i] {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !deepEqualValues(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, va := range av {
			vb, present := bv[k]
			if !present || !deepEqualValues(va, vb) {
				return false
			}
		}
		return true
	default:
		return fmt.Sprint(a) == fmt.Sprint(b)
	}
}

// effectiveWorkerThreads resolves "auto" and empty to the GOMAXPROCS value in
// effect before the server applied any cap, so switching a numeric cap back to
// "auto" is detected as a change.
func effectiveWorkerThreads(raw string) any {
	if n := parseWorkerThreads(raw); n > 0 {
		return n
	}
	return initialGOMAXPROCS
}

// parseWorkerThreads mirrors the runtime conversion applied after validation.
func parseWorkerThreads(raw string) int {
	if raw == "" || strings.EqualFold(raw, "auto") {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// digestString hashes an inline value. It is used for fields whose effective
// value is the secret or configuration text itself. It never reads the
// filesystem, so a path-like string is compared as a path and a growing file
// cannot create a false restart signal (R6-03).
func digestString(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

// digestFileContent reads path and returns a digest of its bytes. An unreadable
// path yields a stable error marker, which still signals a change away from a
// previously readable file.
func digestFileContent(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("error:%v", err)
	}
	h := sha256.Sum256(data)
	return "file-sha256:" + hex.EncodeToString(h[:])
}

// digestTLSMaterial canonicalizes certificate, key, CA or CRL material. After
// secret resolution the value may be inline PEM, a ${file:...} reference already
// resolved to PEM, or a plain file path. Inline PEM is digested as text; a
// readable file path is digested by content so rotating the file in place
// without editing the configuration is still detected; an unreadable path is
// digested as text so a misconfiguration compares stably.
//
// The path check accepts Unix absolute and relative paths as well as Windows
// absolute and UNC paths, since os.Stat resolves all of them.
func digestTLSMaterial(s string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "-----BEGIN") {
		return digestString(s)
	}
	info, err := os.Stat(s)
	if err == nil && !info.IsDir() {
		return digestFileContent(s)
	}
	return digestString(s)
}
