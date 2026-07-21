// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file holds the finder/locator functions and config-mutation helpers used
// by applyPatch (patch.go). They are pure helpers: they take a *config.Config
// and return a targeted pointer or mutate a targeted sub-struct, but they own no
// server state. Keeping them here lets patch.go stay focused on operation
// dispatch; keeping them in the admin package (rather than a sub-package) avoids
// circular imports since they reference both config types and patch_types.go DTOs.

import (
	"fmt"
	"strings"

	"jul/internal/config"
)

// applyServerLimits sets the per-server limit/timeout fields on the server block
// addressed by Listen. Only non-empty fields in req.Limits are applied, so the
// edit is sparse; each value is parsed through its config type so a malformed
// size/duration is rejected (without mutating) rather than silently dropped.
func applyServerLimits(c *config.Config, req patchRequest) (string, error) {
	if req.Limits == nil {
		return "", fmt.Errorf("server_set_limits: limits payload is required")
	}
	srv, err := findServer(c, req.Listen)
	if err != nil {
		return "", err
	}
	applied := make([]string, 0, 6)
	setSize := func(name, val string, dst *config.Size) error {
		if strings.TrimSpace(val) == "" {
			return nil
		}
		var s config.Size
		if err := s.UnmarshalText([]byte(val)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = s
		applied = append(applied, fmt.Sprintf("%s=%s", name, val))
		return nil
	}
	setDur := func(name, val string, dst *config.Duration) error {
		if strings.TrimSpace(val) == "" {
			return nil
		}
		var d config.Duration
		if err := d.UnmarshalText([]byte(val)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = d
		applied = append(applied, fmt.Sprintf("%s=%s", name, val))
		return nil
	}
	if err := setSize("client_max_body_size", req.Limits.ClientMaxBodySize, &srv.ClientMaxBodySize); err != nil {
		return "", err
	}
	if err := setDur("read_header_timeout", req.Limits.ReadHeaderTimeout, &srv.ReadHeaderTimeout); err != nil {
		return "", err
	}
	if err := setDur("read_timeout", req.Limits.ReadTimeout, &srv.ReadTimeout); err != nil {
		return "", err
	}
	if err := setDur("write_timeout", req.Limits.WriteTimeout, &srv.WriteTimeout); err != nil {
		return "", err
	}
	if err := setDur("idle_timeout", req.Limits.IdleTimeout, &srv.IdleTimeout); err != nil {
		return "", err
	}
	if err := setSize("max_header_bytes", req.Limits.MaxHeaderBytes, &srv.MaxHeaderBytes); err != nil {
		return "", err
	}
	if len(applied) == 0 {
		return "", fmt.Errorf("server_set_limits: no limit fields provided")
	}
	return fmt.Sprintf("server %s limits updated (%s)", req.Listen, strings.Join(applied, ", ")), nil
}

// findServer returns a pointer to the first server block bound to listen.
func findServer(c *config.Config, listen string) (*config.ServerConfig, error) {
	if strings.TrimSpace(listen) == "" {
		return nil, fmt.Errorf("server target requires a listen address")
	}
	for i := range c.Servers {
		if c.Servers[i].Listen == listen {
			return &c.Servers[i], nil
		}
	}
	return nil, fmt.Errorf("no server found for listen %q", listen)
}

// findServerByNames returns the single server block bound to listen whose
// server_names set equals serverNames, so route_rename targets exactly one
// virtual host even when several blocks share a listen. An ambiguous or missing
// target is rejected rather than guessed (the console sends the current names
// from the route projection).
func findServerByNames(c *config.Config, listen string, serverNames []string) (*config.ServerConfig, error) {
	if strings.TrimSpace(listen) == "" {
		return nil, fmt.Errorf("server target requires a listen address")
	}
	var found *config.ServerConfig
	matches := 0
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.Listen == listen && stringSetsEqual(srv.ServerNames, serverNames) {
			found = srv
			matches++
		}
	}
	switch {
	case matches == 0:
		return nil, fmt.Errorf("no server found for listen %q names %v", listen, serverNames)
	case matches > 1:
		return nil, fmt.Errorf("server target is ambiguous: %d blocks match listen %q names %v", matches, listen, serverNames)
	default:
		return found, nil
	}
}

// findServerIndex resolves the same unique listen + ServerNames coordinates as
// findServerByNames but returns the slice index, which server_remove needs to
// delete the block. Ambiguous or missing targets are rejected rather than
// guessed, matching the finder used by in-place edits.
func findServerIndex(c *config.Config, listen string, serverNames []string) (int, error) {
	if strings.TrimSpace(listen) == "" {
		return -1, fmt.Errorf("server target requires a listen address")
	}
	idx, matches := -1, 0
	for i := range c.Servers {
		if c.Servers[i].Listen == listen && stringSetsEqual(c.Servers[i].ServerNames, serverNames) {
			idx = i
			matches++
		}
	}
	switch {
	case matches == 0:
		return -1, fmt.Errorf("no server found for listen %q names %v", listen, serverNames)
	case matches > 1:
		return -1, fmt.Errorf("server target is ambiguous: %d blocks match listen %q names %v", matches, listen, serverNames)
	default:
		return idx, nil
	}
}

// serverNamesTaken reports whether a server block other than self on the same
// listen already serves the given host-names set, so a rename never produces
// two indistinguishable virtual hosts.
func serverNamesTaken(c *config.Config, listen string, names []string, self *config.ServerConfig) bool {
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv == self {
			continue
		}
		if srv.Listen == listen && stringSetsEqual(srv.ServerNames, names) {
			return true
		}
	}
	return false
}

// namesLabel renders a server_names set for an audit summary, or "(any host)"
// for the catch-all block that has no names.
func namesLabel(names []string) string {
	if len(names) == 0 {
		return "(any host)"
	}
	return strings.Join(names, ", ")
}

// buildLocationAuth converts the guided auth payload into a *config.AuthConfig
// for exactly one method, mirroring the route-creation form. It returns a short
// human label for the audit summary, and rejects a method whose required fields
// are missing rather than persisting an inert auth block.
// findLocation returns a pointer to the single location uniquely identified by
// its server's listen address and ServerNames set plus the location's match
// type and path, so a mutation updates exactly the intended route in place.
// Matching on listen + path alone (the earlier behavior) could silently target
// the wrong virtual host when several server blocks share a listen, or the
// wrong location when a path repeats under different match types. The console
// always sends the full coordinates from the route projection; a target that
// resolves to more than one location is rejected rather than guessed.
func findLocation(c *config.Config, listen string, serverNames []string, matchType, path string) (*config.LocationConfig, error) {
	if strings.TrimSpace(listen) == "" || strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("route target requires both listen and path")
	}
	var found *config.LocationConfig
	matches := 0
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.Listen != listen || !stringSetsEqual(srv.ServerNames, serverNames) {
			continue
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			if loc.Match.Path == path && loc.Match.Type == matchType {
				found = loc
				matches++
			}
		}
	}
	switch {
	case matches == 0:
		return nil, fmt.Errorf("no route found for listen %q names %v match %q path %q", listen, serverNames, matchType, path)
	case matches > 1:
		return nil, fmt.Errorf("route target is ambiguous: %d locations match listen %q names %v match %q path %q", matches, listen, serverNames, matchType, path)
	default:
		return found, nil
	}
}

// findLocationIndex resolves the same unique route coordinates as findLocation
// but returns the enclosing server index and the location index, which
// location_remove needs to splice the location out of its server's slice.
func findLocationIndex(c *config.Config, listen string, serverNames []string, matchType, path string) (int, int, error) {
	if strings.TrimSpace(listen) == "" || strings.TrimSpace(path) == "" {
		return -1, -1, fmt.Errorf("route target requires both listen and path")
	}
	srvIdx, locIdx, matches := -1, -1, 0
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.Listen != listen || !stringSetsEqual(srv.ServerNames, serverNames) {
			continue
		}
		for j := range srv.Locations {
			if srv.Locations[j].Match.Path == path && srv.Locations[j].Match.Type == matchType {
				srvIdx, locIdx = i, j
				matches++
			}
		}
	}
	switch {
	case matches == 0:
		return -1, -1, fmt.Errorf("no route found for listen %q names %v match %q path %q", listen, serverNames, matchType, path)
	case matches > 1:
		return -1, -1, fmt.Errorf("route target is ambiguous: %d locations match listen %q names %v match %q path %q", matches, listen, serverNames, matchType, path)
	default:
		return srvIdx, locIdx, nil
	}
}

// stringSetsEqual reports whether a and b contain the same elements regardless
// of order (multiset equality, so repeated server names are compared exactly).
func stringSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

// normMatchType normalizes a location match type, treating an empty string as
// "prefix" (the default), so two routes that differ only by an implicit vs.
// explicit prefix are compared as equal.
func normMatchType(t string) string {
	if strings.TrimSpace(t) == "" {
		return "prefix"
	}
	return t
}

// locationMatchTaken reports whether a location other than self under the server
// identified by listen + serverNames already has the match (matchType, path),
// so location_set_match never renames a route onto an existing one.
func locationMatchTaken(c *config.Config, listen string, serverNames []string, matchType, path string, self *config.LocationConfig) bool {
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.Listen != listen || !stringSetsEqual(srv.ServerNames, serverNames) {
			continue
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			if loc == self {
				continue
			}
			if normMatchType(loc.Match.Type) == matchType && loc.Match.Path == path {
				return true
			}
		}
	}
	return false
}

// setLocationAction replaces a location's action wholesale: it clears every
// action discriminator (and the action-specific helper fields) so no
// conflicting leftover remains, then sets the chosen one. It covers the
// tag-free actions the console edits structurally — proxy / static / redirect /
// return / deny. Richer actions (gRPC, transcode, FastCGI/uWSGI, handler
// plugin) are left to raw editing, so the editor offers this op only when the
// current action is already one of these. The validated re-parse still has the
// final say (e.g. proxy_pass must reference a known upstream). It returns the
// action label for the audit summary.
func setLocationAction(loc *config.LocationConfig, a locationActionPayload) (string, error) {
	kind := strings.ToLower(strings.TrimSpace(a.Kind))
	target := strings.TrimSpace(a.Target)

	// Clear all action discriminators and their action-specific helper fields so
	// the result is a single clean action with no orphaned config.
	loc.Root, loc.Index, loc.TryFiles = "", nil, nil
	loc.DirectoryListing, loc.AllowHidden, loc.CacheControl = false, false, ""
	loc.ProxyPass, loc.GRPC = "", false
	loc.ProxyConnectTimeout, loc.ProxyReadTimeout, loc.ProxySendTimeout = 0, 0, 0
	loc.FastCGIPass, loc.FastCGIParams, loc.UWSGIPass = "", nil, ""
	loc.Redirect, loc.Return, loc.Deny = "", 0, false
	loc.GRPCTranscode, loc.Plugin = nil, ""

	switch kind {
	case "proxy":
		if target == "" {
			return "", fmt.Errorf("location_set_action: the proxy action requires a target")
		}
		loc.ProxyPass = target
	case "grpc_proxy":
		// gRPC reverse proxy: sets proxy_pass and enables the GRPC flag so Jul
		// strips content-type framing and speaks h2c to the backend. The server
		// that owns this location must have h2c enabled (server_toggle_h2c);
		// the caller is responsible for emitting that op.
		if target == "" {
			return "", fmt.Errorf("location_set_action: the grpc_proxy action requires a target")
		}
		loc.ProxyPass = target
		loc.GRPC = true
	case "static":
		if target == "" {
			return "", fmt.Errorf("location_set_action: the static action requires a root path")
		}
		loc.Root = target
		// Caching applies to proxy/fastcgi responses, not a static root; the
		// validated re-parse rejects cache + root, so clear any inherited toggle.
		loc.Cache = false
	case "redirect":
		if target == "" {
			return "", fmt.Errorf("location_set_action: the redirect action requires a target URL")
		}
		loc.Redirect = target
		if a.Status != 0 {
			if a.Status < 300 || a.Status > 399 {
				return "", fmt.Errorf("location_set_action: a redirect status must be in the 3xx range")
			}
			loc.Return = a.Status
		}
	case "return":
		if a.Status == 0 {
			return "", fmt.Errorf("location_set_action: the return action requires a status code")
		}
		loc.Return = a.Status
	case "deny":
		loc.Deny = true
	default:
		return "", fmt.Errorf("location_set_action: unknown action %q (want proxy, grpc_proxy, static, redirect, return, or deny)", a.Kind)
	}
	return kind, nil
}

// findUpstream returns a pointer to the upstream pool with the given name.
func findUpstream(c *config.Config, name string) (*config.UpstreamConfig, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("upstream name is required")
	}
	for i := range c.Upstreams {
		if c.Upstreams[i].Name == name {
			return &c.Upstreams[i], nil
		}
	}
	return nil, fmt.Errorf("no upstream named %q", name)
}

// findUpstreamIndex resolves an upstream pool by name and returns its slice
// index, which upstream_remove needs to delete the pool.
func findUpstreamIndex(c *config.Config, name string) (int, error) {
	if strings.TrimSpace(name) == "" {
		return -1, fmt.Errorf("upstream name is required")
	}
	for i := range c.Upstreams {
		if c.Upstreams[i].Name == name {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no upstream named %q", name)
}

// upstreamReferences returns the "listen path" labels of every route whose
// proxy_pass targets the named upstream (bare name or http(s):// prefixed), so
// upstream_remove can refuse a deletion that would leave a dangling reference
// with an actionable pointer instead of a generic downstream validation error.
func upstreamReferences(c *config.Config, name string) []string {
	targets := map[string]bool{
		name:              true,
		"http://" + name:  true,
		"https://" + name: true,
	}
	var refs []string
	for i := range c.Servers {
		srv := &c.Servers[i]
		for j := range srv.Locations {
			if targets[strings.TrimSpace(srv.Locations[j].ProxyPass)] {
				refs = append(refs, fmt.Sprintf("%s %s", srv.Listen, srv.Locations[j].Match.Path))
			}
		}
	}
	return refs
}
