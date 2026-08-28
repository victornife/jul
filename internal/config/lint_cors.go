// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"net/textproto"
	"strings"
)

// This file holds the lint rules ADR 0018 §15 assigns to the response-policy
// and CORS work (#146): findings that are valid configurations but likely
// operator mistakes, as opposed to validate_response_headers.go and
// validate_cors.go, which reject outright-invalid ones.

// responsePolicyDiagnostics reports every §15 finding owned by this issue for
// one server block's locations.
func responsePolicyDiagnostics(srv *ServerConfig, serverIndex int) []Diagnostic {
	var diags []Diagnostic
	for j := range srv.Locations {
		loc := &srv.Locations[j]
		where := fmt.Sprintf("servers[%d].locations[%d]", serverIndex, j)
		diags = append(diags, corsOriginDiagnostics(loc, where)...)
		diags = append(diags, corsGRPCDiagnostics(loc, where)...)
		diags = append(diags, responseHeaderLintDiagnostics(loc, where)...)
		diags = append(diags, corsPreflightPredicateDiagnostics(loc, where)...)
	}
	return diags
}

// corsOriginDiagnostics warns on a "null" entry in a non-wildcard
// allowed_origins: it is accepted (validation lets it through), but it is
// meaningful only because a client can legitimately send Origin: null (a
// sandboxed iframe, a local file, a redirect), and an operator who copied it
// from an example without meaning to allow that case should know.
func corsOriginDiagnostics(loc *LocationConfig, where string) []Diagnostic {
	if loc.CORS == nil {
		return nil
	}
	wildcard := false
	for _, o := range loc.CORS.AllowedOrigins {
		if o == "*" {
			wildcard = true
		}
	}
	if wildcard {
		return nil
	}
	var diags []Diagnostic
	for i, o := range loc.CORS.AllowedOrigins {
		if o == "null" {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("%s.cors.allowed_origins[%d]", where, i),
				Message:  `"null" grants any request whose Origin is the literal string "null" (a sandboxed iframe, a local file, or some redirects) — not "no origin"`,
				Hint:     "remove it unless you specifically intend to trust null-origin requests",
			})
		}
	}
	return diags
}

// corsGRPCDiagnostics warns when a location carries a CORS policy alongside
// native gRPC passthrough. §12 accepts the combination (native gRPC is
// end-to-end HTTP/2, so response_headers still applies to the HTTP response
// headers), but a gRPC client is not a browser and never sends a CORS
// preflight, so the policy has no client that will ever exercise it.
func corsGRPCDiagnostics(loc *LocationConfig, where string) []Diagnostic {
	if !loc.GRPC {
		return nil
	}
	var diags []Diagnostic
	if loc.CORS != nil && loc.CORS.Enabled {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    where + ".cors",
			Message:  "cors is enabled on a native gRPC (grpc = true) location; a gRPC client never sends a CORS preflight, so this policy has no client that will exercise it",
			Hint:     "remove cors from a native gRPC location, or confirm a browser-based gRPC-Web client actually reaches it",
		})
	}
	return diags
}

// responseHeaderLintDiagnostics warns on a Content-Type response_headers
// operation at a native gRPC location: gRPC's content type is part of the
// framing contract (application/grpc[+format]) and overriding it breaks every
// gRPC client, silently, since Jul does not validate it against the protocol.
func responseHeaderLintDiagnostics(loc *LocationConfig, where string) []Diagnostic {
	var diags []Diagnostic
	for i, op := range loc.ResponseHeaders {
		name := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(op.Name))
		field := fmt.Sprintf("%s.response_headers[%d]", where, i)
		if loc.GRPC && name == "Content-Type" {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    field,
				Message:  "a Content-Type operation at a native gRPC (grpc = true) location changes the framing content type every gRPC client expects",
				Hint:     "remove this operation; gRPC's content type is not a response-header policy concern",
			})
		}
		if strings.HasPrefix(name, "Access-Control-") && (loc.CORS == nil || !loc.CORS.Enabled) {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    field,
				Message:  fmt.Sprintf("%q is set directly with cors.enabled = false; a migrated add_header Access-Control-* directive works, but Jul will not keep it consistent (no origin/vary logic, no credentials check)", name),
				Hint:     "prefer [servers.locations.cors] unless this is a deliberate static header",
			})
		}
		if name == "Vary" && op.Op == "add" && !loc.Cache {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    field,
				Message:  "add on Vary at a location with no cache of its own is a directive to downstream caches only; Jul's own cache is not involved",
				Hint:     "this is fine if a downstream/browser cache is the intended audience",
			})
		}
	}
	return diags
}

// corsPreflightPredicateDiagnostics warns when a cors.enabled location also
// carries header predicates. A browser strips a preflight of nearly every
// request header it will send the real request with, so a header predicate
// will not select this location's own preflight — the route stays unreachable
// for exactly the requests CORS exists to unlock, and the failure looks like an
// intermittent CORS bug rather than the routing outcome it is. methods is
// deliberately not affected: it widens for a preflight (§2); headers do not,
// because exempting them would create a second, invisible matching mode.
func corsPreflightPredicateDiagnostics(loc *LocationConfig, where string) []Diagnostic {
	if loc.CORS == nil || !loc.CORS.Enabled || len(loc.Match.Headers) == 0 {
		return nil
	}
	return []Diagnostic{{
		Severity: SeverityWarning,
		Field:    where + ".match.headers",
		Message:  "cors.enabled = true with header predicates: a browser preflight carries none of the application headers the real request will, so this location will not be selected for its own preflight",
		Hint:     "match the request by method or path instead, and let a downstream check enforce the header",
	}}
}
