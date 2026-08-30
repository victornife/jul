// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"strings"

	"jul/internal/config"
)

// routeAuditIdentity is the audit-safe identity of a route-targeting patch
// operation's target, resolved once and reused for both the operation
// summary and, ultimately, the audit trail (ADR 0019 §7). Exactly one of the
// two fields is ever set: a route with a durable route_id reports it, and
// one without reports the revision-scoped selector instead — never both,
// and never neither for a resolvable target.
type routeAuditIdentity struct {
	ResourceID string
	Selector   string
}

// resolveRouteAuditIdentity resolves t against c, the configuration as it
// stood immediately before the operation runs. Every field that mutates a
// route in place is resolved before applyPatch mutates c, because route_id
// itself is immutable outside minting (so this is safe) while other fields
// the target depends on — most obviously the match a location_set_match op
// or location_remove is about to change — are not, and would make t
// unresolvable if looked up afterward. A target that does not resolve to
// exactly one route (including every non-route-targeting op, whose target
// is structurally empty) returns the zero value.
func resolveRouteAuditIdentity(c *config.Config, t routeTarget) routeAuditIdentity {
	if strings.TrimSpace(t.Listen) == "" || strings.TrimSpace(t.Path) == "" {
		return routeAuditIdentity{}
	}
	loc, err := findLocation(c, t)
	if err != nil {
		return routeAuditIdentity{}
	}
	if id := routeID(loc); id != "" {
		return routeAuditIdentity{ResourceID: id}
	}
	return routeAuditIdentity{Selector: routeTargetSelector(t.Listen, t.ServerNames, t.MatchType, t.Path, t.Ordinal)}
}

// auditIdentityOf is resolveRouteAuditIdentity's counterpart for a location
// the caller already has a pointer to — used for location_add, whose target
// does not exist until the operation runs, so there is nothing to resolve
// beforehand.
func auditIdentityOf(listen string, serverNames []string, loc *config.LocationConfig) (resourceID, selector string) {
	if id := routeID(loc); id != "" {
		return id, ""
	}
	return "", routeTargetSelector(listen, serverNames, loc.Match.Type, loc.Match.Path, nil)
}

// routeTargetSelector renders the revision-scoped ADR 0018 §14 selector
// (listen, server_names, match_type, path, match_ordinal) as a single
// audit-safe string. It never includes a predicate, header, or query value —
// only the coordinates a typed patch already requires to address a route.
func routeTargetSelector(listen string, serverNames []string, matchType, path string, ordinal *int) string {
	mt := matchType
	if mt == "" {
		mt = "prefix"
	}
	ord := 0
	if ordinal != nil {
		ord = *ordinal
	}
	names := normalizeStringSlice(serverNames)
	return fmt.Sprintf("listen=%s server_names=%s match_type=%s path=%s match_ordinal=%d",
		listen, strings.Join(names, ","), mt, path, ord)
}
