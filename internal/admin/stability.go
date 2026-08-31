// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// RouteStability classifies a route for external-contract purposes
// (ADR 0019 §24).
//
// The zero value is StabilityInternal. That is the whole point: a route added
// to the catalog without anyone thinking about its external status is
// internal, so being in the admin route catalog does not make a route public.
// The guard tests then require the omission to be *explained* — an internal
// route must record why it is internal — so "why is this not external?" has an
// answer per route rather than a shrug.
type RouteStability uint8

const (
	// StabilityInternal is the fail-closed zero value: served, but not part of
	// the supported external contract, absent from the generated OpenAPI
	// document, and free to change shape in any release.
	StabilityInternal RouteStability = iota
	// StabilityExternal is a supported, versioned, authenticated endpoint. It
	// appears in OpenAPI and is covered by the compatibility policy (§25).
	StabilityExternal
	// StabilityPublic is a supported endpoint that requires no authentication
	// at all. Only /healthz and /readyz qualify: /metrics requires
	// metrics:read, so it is external *authenticated*, not public (§24a).
	StabilityPublic
	// StabilityDeprecated is a supported endpoint scheduled for removal. It
	// still appears in OpenAPI — an endpoint under a Sunset header has to be
	// described somewhere a client can find it — carrying deprecated: true
	// and its sunset date (§29).
	StabilityDeprecated
)

// String renders the classification for documentation, test failures and the
// generated artifact. The strings are part of the generated OpenAPI extension
// values, so they are stable.
func (s RouteStability) String() string {
	switch s {
	case StabilityInternal:
		return "internal"
	case StabilityExternal:
		return "external"
	case StabilityPublic:
		return "public"
	case StabilityDeprecated:
		return "deprecated"
	default:
		return "unknown"
	}
}

// External reports whether the route belongs in the generated external
// contract. Public and Deprecated routes do; Internal routes never do.
func (s RouteStability) External() bool {
	return s == StabilityExternal || s == StabilityPublic || s == StabilityDeprecated
}

// ExternalOperation is the per-method external metadata the OpenAPI generator
// needs and cannot infer: a stable operation id suitable for a generated
// client, and a one-line summary. It exists only for routes whose Stability is
// external; an internal route has none, which is what keeps an internal shape
// out of the published contract by construction.
type ExternalOperation struct {
	// ID is the OpenAPI operationId. It is part of the versioned contract:
	// renaming one breaks every generated client, so it requires /api/v2
	// (ADR 0019 §25).
	ID string
	// Summary is the one-line description rendered into OpenAPI.
	Summary string
	// RequestBody names the adminapi request DTO this operation accepts, or ""
	// for an operation with no body. The generator resolves it against the
	// registered schema set, so a typo fails the build rather than silently
	// publishing an operation with no request schema.
	RequestBody string
	// Response names the adminapi response DTO for the 2xx response.
	Response string
	// SuccessStatus is the documented success status. Zero means 200.
	SuccessStatus int
	// Errors is the set of error codes this operation can return in addition
	// to the ones every external operation can return (see adminapi.
	// UniversalErrorCodes). Listed so the generated document, and therefore a
	// generated client, enumerates the conditions a caller must handle.
	Errors []string
}

// ExternalRoute is the flattened per-method view of the catalog that the
// OpenAPI generator and the contract tests consume. It exists so neither has
// to re-derive the pairing of pattern, method, permission and operation
// metadata — a second derivation is a second source of truth.
type ExternalRoute struct {
	Pattern     string
	Method      string
	Stability   RouteStability
	Operation   ExternalOperation
	Permissions []string
	// Public is true when the route requires no authentication.
	Public bool
	// Sunset is the RFC 3339 date after which a deprecated route may be
	// removed. Empty unless Stability is StabilityDeprecated.
	Sunset string
}

// ExternalRoutes returns every externally classified route, flattened per
// method, in catalog order then method order. It is the single derivation of
// the external surface: the generator renders it, the guard test compares the
// generated document against it, and nothing else enumerates it.
func ExternalRoutes() []ExternalRoute {
	var out []ExternalRoute
	for _, spec := range Catalog {
		if !spec.Stability.External() {
			continue
		}
		for _, m := range spec.Methods {
			out = append(out, ExternalRoute{
				Pattern:     spec.Pattern,
				Method:      m,
				Stability:   spec.Stability,
				Operation:   spec.Operations[m],
				Permissions: spec.permissionsFor(m),
				Public:      spec.Public,
				Sunset:      spec.Sunset,
			})
		}
	}
	return out
}

// permissionsFor returns the permission strings required for one method, in
// declaration order. A route granting access on any of several permissions
// returns all of them; the caller needs to hold one.
func (spec RouteSpec) permissionsFor(method string) []string {
	switch {
	case spec.Public:
		return nil
	case len(spec.AnyPermissions) > 0:
		out := make([]string, 0, len(spec.AnyPermissions))
		for _, p := range spec.AnyPermissions {
			out = append(out, string(p))
		}
		return out
	case spec.Permissions != nil:
		if p, ok := spec.Permissions[method]; ok {
			return []string{string(p)}
		}
		return nil
	case spec.Authenticated:
		return nil
	default:
		return []string{string(spec.Permission)}
	}
}
