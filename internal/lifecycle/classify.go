// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"errors"
	"sort"
	"strings"

	"jul/internal/config"
)

// Live is the runtime state a conditional classification depends on. It is a
// value snapshot: Classify performs no I/O, so preview and apply can call it
// with the same snapshot and obtain identical results.
//
// It carries only what a registry entry is actually conditional on today. A
// future condition — a live admin listener, a compiled capability — adds its
// field here together with the entry and the test that exercises it, rather than
// being reserved in advance.
type Live struct {
	// BoundHTTPAddrs lists the HTTP listen addresses currently bound. An empty
	// slice means "not known"; Classify then treats every address present in the
	// before configuration as retained, which is the conservative reading.
	BoundHTTPAddrs []string
}

// Change is the resolved disposition of one configuration path for a concrete
// transition.
type Change struct {
	// Path is the canonical registry path that changed.
	Path string
	// Declared is the class recorded in the registry.
	Declared Class
	// Effective is the class that actually governs this transition. It differs
	// from Declared only for conditional entries, where the live listener set
	// decides whether a bind-time value is adopted now or only after a restart.
	Effective Class
	// Subsystem is the bounded operational group of the path.
	Subsystem Subsystem
	// Reason is the registry reason for the declared class.
	Reason string
	// Detail explains a conditional resolution in bounded, value-free words.
	// It is empty for a non-conditional entry.
	Detail string
	// Ignored is true when no runtime consumer reads the path.
	Ignored bool
	// Reserved is true when configuration validation rejects the path.
	Reserved bool
}

// Result is the complete, bounded classification of a transition. Every field
// contains canonical paths, closed class names, bounded subsystem identifiers
// and fixed reason text — never configured values, resolved secrets or file
// contents — so it is safe to render in an API response, the Console and audit
// records.
type Result struct {
	// Changes lists every registered path whose effective value differs, in
	// path order.
	Changes []Change
	// RestartRequired lists the paths that cannot take effect until the process
	// restarts.
	RestartRequired []string
	// NewListenerOnly lists the paths that will be adopted by a newly bound
	// listener but not by one kept across the reload.
	NewListenerOnly []string
	// IgnoredDeprecated lists changed paths that no runtime consumer reads.
	IgnoredDeprecated []string
	// ValidationRejected lists changed paths that configuration validation
	// rejects.
	ValidationRejected []string
	// CanApplyHot reports whether the whole candidate can take effect on a hot
	// reload.
	CanApplyHot bool
	// CanStageRestart reports whether the candidate can be persisted and staged
	// for the next restart instead of applied hot.
	CanStageRestart bool
}

// ErrNilConfig is returned when a classification request omits a configuration.
var ErrNilConfig = errors.New("lifecycle: classify requires both a before and a candidate configuration")

// Classify resolves the disposition of a complete before/candidate transition.
//
// It is the one side-effect-free classification service: reload gating, managed
// apply preflight, planned-restart marking, diff projections and the
// configuration preview API all read their verdict from here rather than
// re-deriving it, so preview cannot disagree with apply.
//
// Both configurations must already be resolved (secret references expanded);
// classification compares effective values, so changing a ${file:...} reference
// to an equivalent literal is not reported as a change.
func Classify(before, candidate *config.Config, live Live) (Result, error) {
	if before == nil || candidate == nil {
		return Result{}, ErrNilConfig
	}

	retained := retainedAddrs(before, candidate, live)

	res := Result{Changes: make([]Change, 0, 8)}
	for _, d := range DiffConfig(before, candidate) {
		e, ok := Lookup(d.Path)
		if !ok {
			return Result{}, &ErrUnclassified{Path: d.Path}
		}
		effective, detail := resolveConditional(e, before, candidate, retained)
		res.Changes = append(res.Changes, Change{
			Path:      e.Path,
			Declared:  e.Class,
			Effective: effective,
			Subsystem: e.Subsystem,
			Reason:    e.Reason,
			Detail:    detail,
			Ignored:   e.Ignored,
			Reserved:  e.Reserved,
		})
		switch effective {
		case RestartRequiredClass:
			res.RestartRequired = append(res.RestartRequired, e.Path)
		case NewListenerOnlyClass:
			res.NewListenerOnly = append(res.NewListenerOnly, e.Path)
		case IgnoredDeprecatedClass:
			res.IgnoredDeprecated = append(res.IgnoredDeprecated, e.Path)
		case ValidationRejectedReservedClass:
			res.ValidationRejected = append(res.ValidationRejected, e.Path)
		}
	}
	sort.Strings(res.RestartRequired)
	sort.Strings(res.NewListenerOnly)
	sort.Strings(res.IgnoredDeprecated)
	sort.Strings(res.ValidationRejected)

	res.CanApplyHot = len(res.RestartRequired) == 0 && len(res.ValidationRejected) == 0
	res.CanStageRestart = len(res.ValidationRejected) == 0
	return res, nil
}

// ClassifyPath resolves the declared class of a single path, failing closed for
// a path with no disposition.
func ClassifyPath(path string) (Entry, error) {
	e, ok := Lookup(path)
	if !ok {
		return Entry{}, &ErrUnclassified{Path: path}
	}
	return e, nil
}

// Conditional detail sentences. They are fixed strings so a Result stays
// bounded and free of operator-defined identifiers.
const (
	detailRetainedListener = "at least one listen address kept across this reload changed the value it bound with, so the running listener keeps the old value until the process restarts"
	detailNewListenerOnly  = "only listen addresses that are added or removed by this reload are affected, so the new socket binds with the new value and no running listener is stranded"
	detailAddressAdded     = "the change adds or removes a listener rather than editing one that is kept, so it takes effect on this reload"
)

// resolveConditional turns a declared class into the class that governs this
// transition. A non-conditional entry resolves to its declared class.
func resolveConditional(e Entry, before, candidate *config.Config, retained map[string]bool) (Class, string) {
	if !e.Conditional {
		return e.Class, ""
	}

	switch {
	case e.AddressKeyed:
		// TLS, HTTP/3 and h2c values are captured per listen address. If a
		// retained address changed, the running listener is stranded.
		if addressKeyedChangedOnRetained(e.Path, before, candidate, retained) {
			return RestartRequiredClass, detailRetainedListener
		}
		return NewListenerOnlyClass, detailNewListenerOnly

	case e.Path == "servers.*.listen" || e.Path == "stream.*.listen":
		// Editing a listen address always creates a socket and retires another,
		// which the listener diff performs during the reload.
		return HotReloadClass, detailAddressAdded

	default:
		// Remaining conditional entries are bind-time values resolved per
		// address (listener timeouts and limits) or installed on every listener
		// at bind time (the connection cap).
		if serverScopedChangedOnRetained(e.Path, before, candidate, retained) {
			return RestartRequiredClass, detailRetainedListener
		}
		return NewListenerOnlyClass, detailNewListenerOnly
	}
}

// retainedAddrs returns the listen addresses that survive the transition. When
// the live snapshot lists bound addresses they are authoritative, because an
// address present in the before configuration may have failed to bind.
func retainedAddrs(before, candidate *config.Config, live Live) map[string]bool {
	old := map[string]bool{}
	if len(live.BoundHTTPAddrs) > 0 {
		for _, a := range live.BoundHTTPAddrs {
			old[a] = true
		}
	} else {
		for i := range before.Servers {
			old[before.Servers[i].Listen] = true
		}
	}
	out := map[string]bool{}
	for i := range candidate.Servers {
		if addr := candidate.Servers[i].Listen; old[addr] {
			out[addr] = true
		}
	}
	return out
}

// addressKeyedChangedOnRetained reports whether an address-keyed path differs on
// an address that survives the reload.
func addressKeyedChangedOnRetained(path string, before, candidate *config.Config, retained map[string]bool) bool {
	bv, ok1 := EffectiveValue(before, path)
	av, ok2 := EffectiveValue(candidate, path)
	if !ok1 || !ok2 {
		return true
	}
	bm, ok1 := bv.(map[string]any)
	am, ok2 := av.(map[string]any)
	if !ok1 || !ok2 {
		return !deepEqualValues(bv, av)
	}
	for addr := range retained {
		if !deepEqualValues(bm[addr], am[addr]) {
			return true
		}
	}
	return false
}

// serverScopedChangedOnRetained reports whether a per-server-block value differs
// for a block bound to a retained address. Values that are not keyed by server
// block — the global connection cap — count as changed on every retained
// address, because bind() installs them on each listener.
func serverScopedChangedOnRetained(path string, before, candidate *config.Config, retained map[string]bool) bool {
	if len(retained) == 0 {
		return false
	}
	bv, ok1 := EffectiveValue(before, path)
	av, ok2 := EffectiveValue(candidate, path)
	if !ok1 || !ok2 {
		return true
	}
	bm, isMapB := bv.(map[string]any)
	am, isMapA := av.(map[string]any)
	if !isMapB || !isMapA {
		// A global listener-installed value such as rate_limit.max_conns.
		return !deepEqualValues(bv, av)
	}
	keys := map[string]bool{}
	for k := range bm {
		keys[k] = true
	}
	for k := range am {
		keys[k] = true
	}
	for k := range keys {
		if !retained[addrFromServerKey(k)] {
			continue
		}
		if !deepEqualValues(bm[k], am[k]) {
			return true
		}
	}
	return false
}

// addrFromServerKey extracts the listen address from a serverKey
// ("names@addr"). A key without the separator is returned unchanged so an
// unexpected shape degrades to "not retained" rather than panicking.
func addrFromServerKey(key string) string {
	if i := strings.LastIndex(key, "@"); i >= 0 {
		return key[i+1:]
	}
	return key
}
