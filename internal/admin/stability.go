// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// RouteStability classifies a route for external-contract purposes (ADR 0019 §24).
type RouteStability uint8

const apiVersionNamespace = "/api/v1"

const (
	StabilityInternal RouteStability = iota
	StabilityExternal
	StabilityPublic
	StabilityDeprecated
)

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

func (s RouteStability) External() bool {
	return s == StabilityExternal || s == StabilityPublic || s == StabilityDeprecated
}

// ExternalParameter is one machine-defined path/query/header parameter. Path
// parameters continue to be derived from the route template; this type is for
// the semantics the template cannot encode.
type ExternalParameter struct {
	Name        string
	In          string // query | header
	Description string
	Required    bool
	Type        string // string | integer | boolean
	Default     any
	Enum        []string
}

// ExternalOperation is the per-method external metadata the OpenAPI generator
// cannot safely infer from a handler implementation.
type ExternalOperation struct {
	ID      string
	Summary string
	// RequestBody names a registered schema. Empty means no request body.
	RequestBody string
	// RequestContentTypes is exact. JSON is not inferred for raw TOML bodies.
	RequestContentTypes []string
	// MaxBodyBytes publishes the server-side body cap; zero means no body.
	MaxBodyBytes int64
	// Parameters declares query/header inputs such as base_version,
	// Idempotency-Key and history pagination.
	Parameters []ExternalParameter
	Response   string
	// SuccessStatus is the primary documented success status. Zero means 200.
	SuccessStatus int
	// AdditionalSuccessStatuses models operations such as apply lookup whose
	// same response schema is returned as both 200 (terminal) and 202 (pending).
	AdditionalSuccessStatuses []int
	Errors                    []string
}

type ExternalRoute struct {
	Pattern     string
	Method      string
	Stability   RouteStability
	Operation   ExternalOperation
	Permissions []string
	Public      bool
	Sunset      string
}

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

var (
	externalOperationCount int
	internalRouteCount     int
)

func init() {
	for _, spec := range Catalog {
		if spec.Stability.External() {
			externalOperationCount += len(spec.Methods)
			continue
		}
		internalRouteCount++
	}
}

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
