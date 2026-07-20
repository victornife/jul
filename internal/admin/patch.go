// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// applyPatch operation dispatch.
//
// This file contains only the applyPatch switch that routes each patch op to
// its inline implementation. Helper functions (finders, locators, config-
// mutation utilities) live in patch_helpers.go. HTTP handler wiring and the
// handleConfigPatch / handleConfigPatchApply handlers live in patch_http.go.
// The wire/DTO types used by this switch live in patch_types.go. Builder
// functions (buildLocationAuth, buildHealthCheck, buildDiscovery, etc.) live
// in patch_builders.go.

import (
	"fmt"
	"strings"

	"jul/internal/config"
)

// applyPatch mutates c in place according to req, returning a human-readable
// description of the change for the audit log, or an error when the target is
// not found or the operation is unknown.
func applyPatch(c *config.Config, req patchRequest) (string, error) {
	switch req.Op {
	case "route_set_target":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(req.Target) == "" {
			return "", fmt.Errorf("route_set_target: target is required")
		}
		loc.ProxyPass = req.Target
		return fmt.Sprintf("route %s%s proxy_pass set to %s", req.Listen, req.Path, req.Target), nil

	case "route_toggle_cache":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.Enabled == nil {
			return "", fmt.Errorf("route_toggle_cache: enabled is required")
		}
		loc.Cache = *req.Enabled
		return fmt.Sprintf("route %s%s cache %s", req.Listen, req.Path, onOff(*req.Enabled)), nil

	case "route_toggle_rate_limit":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.Enabled == nil {
			return "", fmt.Errorf("route_toggle_rate_limit: enabled is required")
		}
		if *req.Enabled {
			if loc.RateLimit == nil {
				loc.RateLimit = &config.RateLimitConfig{}
			}
			loc.RateLimit.Enabled = true
			if loc.RateLimit.Rate <= 0 {
				loc.RateLimit.Rate = 100
			}
			if loc.RateLimit.Burst <= 0 {
				loc.RateLimit.Burst = loc.RateLimit.Rate
			}
			if loc.RateLimit.Key == "" {
				loc.RateLimit.Key = "ip"
			}
		} else if loc.RateLimit != nil {
			loc.RateLimit.Enabled = false
		}
		return fmt.Sprintf("route %s%s rate limit %s", req.Listen, req.Path, onOff(*req.Enabled)), nil

	case "route_set_rate_limit":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.RateLimit == nil {
			return "", fmt.Errorf("route_set_rate_limit: rate_limit payload is required")
		}
		if req.RateLimit.Enabled {
			// Validate before mutating so a rejected op leaves no partial state.
			rate := req.RateLimit.Rate
			if rate <= 0 {
				return "", fmt.Errorf("route_set_rate_limit: rate must be > 0")
			}
			burst := req.RateLimit.Burst
			if burst <= 0 {
				burst = rate
			}
			key := strings.TrimSpace(req.RateLimit.Key)
			if key == "" {
				key = "ip"
			}
			if !config.ValidRateKey(key) {
				return "", fmt.Errorf("route_set_rate_limit: invalid key %q", key)
			}
			if loc.RateLimit == nil {
				loc.RateLimit = &config.RateLimitConfig{}
			}
			loc.RateLimit.Enabled = true
			loc.RateLimit.Rate = rate
			loc.RateLimit.Burst = burst
			loc.RateLimit.Key = key
		} else {
			if loc.RateLimit != nil {
				loc.RateLimit.Enabled = false
			}
		}
		if loc.RateLimit != nil && loc.RateLimit.Enabled {
			return fmt.Sprintf("route %s%s rate limit set (%d req/s, burst %d, key %s)",
				req.Listen, req.Path, loc.RateLimit.Rate, loc.RateLimit.Burst, loc.RateLimit.Key), nil
		}
		return fmt.Sprintf("route %s%s rate limit disabled", req.Listen, req.Path), nil

	case "location_waf_set":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.WAF == nil {
			return "", fmt.Errorf("location_waf_set: waf payload is required")
		}
		mode := strings.TrimSpace(req.WAF.Mode)
		if mode == "" {
			mode = "block"
		}
		if mode != "block" && mode != "detect" {
			return "", fmt.Errorf("location_waf_set: mode must be %q or %q", "block", "detect")
		}
		// The override REPLACES the global policy for this location wholesale (it
		// is not merged), which is exactly the semantics the security panel
		// discloses. As of Phase 4e the guided editor surfaces every override
		// field and seeds them from the projection, so building a fresh
		// config.WAFConfig from the full payload round-trips faithfully rather
		// than clobbering unshown rules. Defaults (block_status 403, body limit
		// 128 KiB, CRS paranoia) are applied by the parser on re-parse.
		var bodyLimit config.Size
		if raw := strings.TrimSpace(req.WAF.RequestBodyLimit); raw != "" {
			if err := bodyLimit.UnmarshalText([]byte(raw)); err != nil {
				return "", fmt.Errorf("location_waf_set: request_body_limit: %w", err)
			}
		}
		loc.WAF = &config.WAFConfig{
			Enabled:           req.WAF.Enabled,
			Mode:              mode,
			BlockStatus:       req.WAF.BlockStatus,
			DirectivesFiles:   normalizeStringSlice(req.WAF.DirectivesFiles),
			InlineRules:       strings.TrimSpace(req.WAF.InlineRules),
			CRSEnabled:        req.WAF.CRSEnabled,
			Paranoia:          req.WAF.Paranoia,
			RequestBodyLimit:  bodyLimit,
			ResponseBodyCheck: req.WAF.ResponseBodyCheck,
		}
		return fmt.Sprintf("route %s%s WAF override set (%s%s)", req.Listen, req.Path,
			onOff(req.WAF.Enabled), wafModeNote(req.WAF.Enabled, mode, req.WAF.CRSEnabled)), nil

	case "location_waf_clear":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if loc.WAF == nil {
			return "", fmt.Errorf("route %s%s has no WAF override to clear", req.Listen, req.Path)
		}
		loc.WAF = nil
		return fmt.Sprintf("route %s%s WAF override cleared (inherits the global [waf])", req.Listen, req.Path), nil

	case "location_set_auth":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.Auth == nil {
			return "", fmt.Errorf("location_set_auth: auth payload is required")
		}
		ac, summary, err := buildLocationAuth(*req.Auth)
		if err != nil {
			return "", err
		}
		loc.Auth = ac
		return fmt.Sprintf("route %s%s auth set (%s)", req.Listen, req.Path, summary), nil

	case "location_clear_auth":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if loc.Auth == nil {
			return "", fmt.Errorf("route %s%s has no auth rule to clear", req.Listen, req.Path)
		}
		loc.Auth = nil
		return fmt.Sprintf("route %s%s auth cleared", req.Listen, req.Path), nil

	case "location_set_match":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.Match == nil {
			return "", fmt.Errorf("location_set_match: match payload is required")
		}
		newType := normMatchType(req.Match.Type)
		if newType != "exact" && newType != "prefix" && newType != "regex" {
			return "", fmt.Errorf("location_set_match: type must be %q, %q, or %q", "exact", "prefix", "regex")
		}
		newPath := strings.TrimSpace(req.Match.Path)
		if newPath == "" {
			return "", fmt.Errorf("location_set_match: path is required")
		}
		if newType == normMatchType(req.MatchType) && newPath == strings.TrimSpace(req.Path) {
			return "", fmt.Errorf("location_set_match: the match is unchanged")
		}
		// A route is identified by its match, so refuse a change that would
		// collide with another route on the same server (the validated re-parse
		// also rejects duplicate locations, but this gives a clearer message
		// before the diff is generated).
		if locationMatchTaken(c, req.Listen, req.ServerNames, newType, newPath, loc) {
			return "", fmt.Errorf("location_set_match: a route with match %s %q already exists on %s", newType, newPath, req.Listen)
		}
		loc.Match.Type = newType
		loc.Match.Path = newPath
		return fmt.Sprintf("route %s%s match changed to %s %s", req.Listen, req.Path, newType, newPath), nil

	case "location_set_action":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.Action == nil {
			return "", fmt.Errorf("location_set_action: action payload is required")
		}
		kind, err := setLocationAction(loc, *req.Action)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("route %s%s action changed to %s", req.Listen, req.Path, kind), nil

	case "location_set_transcode":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.Transcode == nil {
			return "", fmt.Errorf("location_set_transcode: transcode payload is required")
		}
		target := strings.TrimSpace(req.Transcode.Target)
		if target == "" {
			return "", fmt.Errorf("location_set_transcode: target is required")
		}
		// At least one descriptor source must be configured.
		hasDescriptor := strings.TrimSpace(req.Transcode.DescriptorPath) != ""
		hasReflection := req.Transcode.UseReflection
		if !hasDescriptor && !hasReflection {
			return "", fmt.Errorf("location_set_transcode: set exactly one of descriptor_path or use_reflection")
		}
		if hasDescriptor && hasReflection {
			return "", fmt.Errorf("location_set_transcode: descriptor_path and use_reflection are mutually exclusive")
		}
		var maxSize config.Size
		if req.Transcode.MaxMessageSize != "" {
			if err := maxSize.UnmarshalText([]byte(req.Transcode.MaxMessageSize)); err != nil {
				return "", fmt.Errorf("location_set_transcode: max_message_size: %w", err)
			}
		}
		streamMode := strings.ToLower(strings.TrimSpace(req.Transcode.StreamMode))
		if streamMode == "" {
			streamMode = "ndjson"
		}
		switch streamMode {
		case "ndjson", "sse":
		default:
			return "", fmt.Errorf("location_set_transcode: stream_mode must be %q or %q", "ndjson", "sse")
		}
		// Clear all other action discriminators so the location becomes a
		// clean grpc_transcode route with no orphaned fields.
		loc.Root, loc.Index, loc.TryFiles = "", nil, nil
		loc.DirectoryListing, loc.AllowHidden, loc.CacheControl = false, false, ""
		loc.ProxyConnectTimeout, loc.ProxyReadTimeout, loc.ProxySendTimeout = 0, 0, 0
		loc.FastCGIPass, loc.FastCGIParams, loc.UWSGIPass = "", nil, ""
		loc.Redirect, loc.Return, loc.Deny = "", 0, false
		loc.GRPC, loc.Plugin = false, ""
		loc.ProxyPass = ""
		loc.GRPCTranscode = &config.GRPCTranscodeConfig{
			Target:         target,
			DescriptorSet:  strings.TrimSpace(req.Transcode.DescriptorPath),
			UseReflection:  hasReflection,
			TLS:            req.Transcode.TLS,
			PreserveNames:  req.Transcode.PreserveNames,
			Streaming:      req.Transcode.Streaming,
			StreamMode:     streamMode,
			MaxMessageSize: maxSize,
		}
		return fmt.Sprintf("route %s%s transcode settings updated", req.Listen, req.Path), nil

	case "upstream_add_backend":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		addr := strings.TrimSpace(req.Address)
		if addr == "" {
			return "", fmt.Errorf("upstream_add_backend: address is required")
		}
		for _, s := range up.Servers {
			if s.Address == addr {
				return "", fmt.Errorf("upstream %q already has backend %q", req.Upstream, addr)
			}
		}
		weight := req.Weight
		if weight < 1 {
			weight = 1
		}
		up.Servers = append(up.Servers, config.UpstreamServer{Address: addr, Weight: weight})
		return fmt.Sprintf("upstream %s added backend %s (weight %d)", req.Upstream, addr, weight), nil

	case "upstream_remove_backend":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		addr := strings.TrimSpace(req.Address)
		idx := -1
		for i, s := range up.Servers {
			if s.Address == addr {
				idx = i
				break
			}
		}
		if idx < 0 {
			return "", fmt.Errorf("upstream %q has no backend %q", req.Upstream, addr)
		}
		if len(up.Servers) == 1 {
			return "", fmt.Errorf("cannot remove the last backend of upstream %q", req.Upstream)
		}
		up.Servers = append(up.Servers[:idx], up.Servers[idx+1:]...)
		return fmt.Sprintf("upstream %s removed backend %s", req.Upstream, addr), nil

	case "upstream_set_strategy":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		strat := strings.TrimSpace(req.Strategy)
		switch strat {
		case "", "round_robin", "weighted_round_robin", "least_conn":
		default:
			return "", fmt.Errorf("upstream_set_strategy: invalid strategy %q (want round_robin|weighted_round_robin|least_conn)", strat)
		}
		up.Strategy = strat
		return fmt.Sprintf("upstream %s strategy set to %s", req.Upstream, orDefault(strat, "round_robin")), nil

	case "upstream_set_health_check":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		if req.HealthCheck == nil {
			return "", fmt.Errorf("upstream_set_health_check: health_check payload is required")
		}
		hc, summary, err := buildHealthCheck(*req.HealthCheck)
		if err != nil {
			return "", err
		}
		up.HealthCheck = hc
		return fmt.Sprintf("upstream %s active health checks %s", req.Upstream, summary), nil

	case "upstream_set_discovery":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		if req.Discovery == nil {
			return "", fmt.Errorf("upstream_set_discovery: discovery payload is required")
		}
		disc, summary, err := buildDiscovery(*req.Discovery, up.Discovery)
		if err != nil {
			return "", err
		}
		up.Discovery = disc
		return fmt.Sprintf("upstream %s discovery %s", req.Upstream, summary), nil

	case "server_set_limits":
		return applyServerLimits(c, req)

	case "server_toggle_http3":
		srv, err := findServer(c, req.Listen)
		if err != nil {
			return "", err
		}
		if req.Enabled == nil {
			return "", fmt.Errorf("server_toggle_http3: enabled is required")
		}
		if *req.Enabled {
			// HTTP/3 shares the block's TLS certificates, so it can only run on a
			// TLS-enabled listener. The validated apply path also rejects this (and
			// an enabled block in a build without the "http3" tag); the near-side
			// check just gives a clearer message before the diff is generated.
			if srv.TLS == nil || !srv.TLS.Enabled {
				return "", fmt.Errorf("server_toggle_http3: HTTP/3 requires TLS on server %s — enable TLS first", req.Listen)
			}
			if srv.HTTP3 == nil {
				srv.HTTP3 = &config.HTTP3Config{}
			}
			srv.HTTP3.Enabled = true
		} else {
			// Disabling removes the block entirely so the serialized config stays
			// clean (rather than leaving an inert [http3] enabled = false).
			srv.HTTP3 = nil
		}
		return fmt.Sprintf("server %s HTTP/3 %s", req.Listen, onOff(*req.Enabled)), nil

	case "server_toggle_h2c":
		srv, err := findServer(c, req.Listen)
		if err != nil {
			return "", err
		}
		if req.Enabled == nil {
			return "", fmt.Errorf("server_toggle_h2c: enabled is required")
		}
		// h2c is cleartext HTTP/2: it only applies to a plaintext listener, since a
		// TLS listener already negotiates HTTP/2 via ALPN.
		if *req.Enabled && srv.TLS != nil && srv.TLS.Enabled {
			return "", fmt.Errorf("server_toggle_h2c: h2c applies only to a plaintext listener; server %s already negotiates HTTP/2 over TLS", req.Listen)
		}
		srv.H2C = *req.Enabled
		return fmt.Sprintf("server %s h2c %s", req.Listen, onOff(*req.Enabled)), nil

	case "route_rename":
		srv, err := findServerByNames(c, req.Listen, req.ServerNames)
		if err != nil {
			return "", err
		}
		newNames := normalizeStringSlice(req.NewServerNames)
		if stringSetsEqual(srv.ServerNames, newNames) {
			return "", fmt.Errorf("route_rename: the host names are unchanged")
		}
		// Two server blocks on the same listen with the same host-names set are
		// indistinguishable, so refuse a rename that would create a duplicate.
		if serverNamesTaken(c, req.Listen, newNames, srv) {
			return "", fmt.Errorf("route_rename: another server block on %s already serves %s", req.Listen, namesLabel(newNames))
		}
		old := srv.ServerNames
		srv.ServerNames = newNames
		return fmt.Sprintf("server %s host names changed (%s → %s)", req.Listen, namesLabel(old), namesLabel(newNames)), nil

	case "plugin_set":
		name := strings.TrimSpace(req.PluginName)
		if name == "" {
			return "", fmt.Errorf("plugin_set: plugin_name is required")
		}
		if req.PluginDef == nil {
			return "", fmt.Errorf("plugin_set: plugin is required")
		}
		existing, existed := c.Plugins[name]
		pc, summary, err := buildPlugin(*req.PluginDef, existing)
		if err != nil {
			return "", err
		}
		if c.Plugins == nil {
			c.Plugins = make(map[string]config.PluginConfig)
		}
		c.Plugins[name] = pc
		verb := "added"
		if existed {
			verb = "updated"
		}
		return fmt.Sprintf("plugin %s %s (%s)", name, verb, summary), nil

	case "plugin_remove":
		name := strings.TrimSpace(req.PluginName)
		if name == "" {
			return "", fmt.Errorf("plugin_remove: plugin_name is required")
		}
		if _, ok := c.Plugins[name]; !ok {
			return "", fmt.Errorf("plugin_remove: no plugin named %q", name)
		}
		if refs := pluginReferences(c, name); len(refs) > 0 {
			return "", fmt.Errorf("plugin_remove: plugin %q is still attached to %s; detach it first", name, strings.Join(refs, ", "))
		}
		delete(c.Plugins, name)
		return fmt.Sprintf("plugin %s removed", name), nil

	case "location_attach_plugin":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		name := strings.TrimSpace(req.PluginName)
		if name == "" {
			return "", fmt.Errorf("location_attach_plugin: plugin_name is required")
		}
		pc, ok := c.Plugins[name]
		if !ok {
			return "", fmt.Errorf("location_attach_plugin: no plugin named %q", name)
		}
		if pluginTypeOrDefault(pc) != "middleware" {
			return "", fmt.Errorf("location_attach_plugin: plugin %q is a handler plugin, not middleware; attach it as the route action instead", name)
		}
		for _, p := range loc.Plugins {
			if p == name {
				return "", fmt.Errorf("location_attach_plugin: plugin %q is already attached to route %s%s", name, req.Listen, loc.Match.Path)
			}
		}
		loc.Plugins = append(loc.Plugins, name)
		return fmt.Sprintf("plugin %s attached to route %s%s", name, req.Listen, loc.Match.Path), nil

	case "location_detach_plugin":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		name := strings.TrimSpace(req.PluginName)
		if name == "" {
			return "", fmt.Errorf("location_detach_plugin: plugin_name is required")
		}
		idx := -1
		for i, p := range loc.Plugins {
			if p == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return "", fmt.Errorf("location_detach_plugin: plugin %q is not attached to route %s%s", name, req.Listen, loc.Match.Path)
		}
		loc.Plugins = append(loc.Plugins[:idx], loc.Plugins[idx+1:]...)
		return fmt.Sprintf("plugin %s detached from route %s%s", name, req.Listen, loc.Match.Path), nil

	case "stream_add":
		if req.Stream == nil {
			return "", fmt.Errorf("stream_add: stream is required")
		}
		st, summary, err := buildStream(*req.Stream)
		if err != nil {
			return "", fmt.Errorf("stream_add: %w", err)
		}
		if streamTaken(c, st.Listen, st.Protocol, -1) {
			return "", fmt.Errorf("stream_add: a %s stream listening on %s already exists", streamProtoOrDefault(st.Protocol), st.Listen)
		}
		c.Streams = append(c.Streams, st)
		return fmt.Sprintf("stream %s added", summary), nil

	case "stream_set":
		if req.Stream == nil {
			return "", fmt.Errorf("stream_set: stream is required")
		}
		idx, err := findStreamIndex(c, req.Listen, req.StreamProtocol)
		if err != nil {
			return "", fmt.Errorf("stream_set: %w", err)
		}
		st, summary, err := buildStream(*req.Stream)
		if err != nil {
			return "", fmt.Errorf("stream_set: %w", err)
		}
		// Editing may change the listen/protocol (the stream's identity); refuse
		// a change that would collide with a different existing stream.
		if streamTaken(c, st.Listen, st.Protocol, idx) {
			return "", fmt.Errorf("stream_set: a %s stream listening on %s already exists", streamProtoOrDefault(st.Protocol), st.Listen)
		}
		c.Streams[idx] = st
		return fmt.Sprintf("stream %s updated", summary), nil

	case "stream_remove":
		idx, err := findStreamIndex(c, req.Listen, req.StreamProtocol)
		if err != nil {
			return "", fmt.Errorf("stream_remove: %w", err)
		}
		st := c.Streams[idx]
		c.Streams = append(c.Streams[:idx], c.Streams[idx+1:]...)
		return fmt.Sprintf("stream %s removed", streamSummary(st)), nil

	case "server_set_client_auth":
		if req.ClientAuth == nil {
			return "", fmt.Errorf("server_set_client_auth: client_auth is required")
		}
		srv, err := findServerByNames(c, req.Listen, req.ServerNames)
		if err != nil {
			return "", err
		}
		// Mutual TLS only applies on a TLS listener (the validator rejects
		// client_auth without tls.enabled); surface it before the diff.
		if srv.TLS == nil || !srv.TLS.Enabled {
			return "", fmt.Errorf("server_set_client_auth: mutual TLS requires TLS on server %s — enable TLS first", req.Listen)
		}
		ca, summary, err := buildClientAuth(*req.ClientAuth)
		if err != nil {
			return "", fmt.Errorf("server_set_client_auth: %w", err)
		}
		// Disabling mutual TLS would invalidate any per-location
		// require_client_cert under this server (the validator rejects it
		// without an active client_auth); refuse with a clear message first.
		if !ca.Active() {
			if paths := serverRequireClientCertPaths(srv); len(paths) > 0 {
				return "", fmt.Errorf("server_set_client_auth: cannot disable mutual TLS while these routes still require a client certificate: %s; clear them first", strings.Join(paths, ", "))
			}
		}
		srv.TLS.ClientAuth = ca
		return fmt.Sprintf("server %s mutual TLS %s", req.Listen, summary), nil

	case "location_toggle_require_client_cert":
		if req.Enabled == nil {
			return "", fmt.Errorf("location_toggle_require_client_cert: enabled is required")
		}
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if *req.Enabled {
			// Requiring a client certificate is meaningful only when the server
			// requests one; the validator enforces the same dependency.
			srv, err := findServerByNames(c, req.Listen, req.ServerNames)
			if err != nil {
				return "", err
			}
			if srv.TLS == nil || !srv.TLS.ClientAuth.Active() {
				return "", fmt.Errorf("location_toggle_require_client_cert: server %s must have mutual TLS enabled (mode request or require) first", req.Listen)
			}
		}
		loc.RequireClientCert = *req.Enabled
		return fmt.Sprintf("route %s%s require client certificate %s", req.Listen, loc.Match.Path, onOff(*req.Enabled)), nil

	case "server_add":
		listen := strings.TrimSpace(req.Listen)
		if listen == "" {
			return "", fmt.Errorf("server_add: listen is required")
		}
		names := normalizeStringSlice(req.ServerNames)
		// Two server blocks on the same listen with the same host-names set are
		// indistinguishable, so refuse a create that would duplicate one.
		if serverNamesTaken(c, listen, names, nil) {
			return "", fmt.Errorf("server_add: a server block on %s already serves %s", listen, namesLabel(names))
		}
		c.Servers = append(c.Servers, config.ServerConfig{Listen: listen, ServerNames: names})
		return fmt.Sprintf("server %s added (%s)", listen, namesLabel(names)), nil

	case "server_remove":
		idx, err := findServerIndex(c, req.Listen, req.ServerNames)
		if err != nil {
			return "", err
		}
		if len(c.Servers) == 1 {
			return "", fmt.Errorf("server_remove: cannot remove the only server block; at least one [[servers]] block is required")
		}
		srv := c.Servers[idx]
		c.Servers = append(c.Servers[:idx], c.Servers[idx+1:]...)
		return fmt.Sprintf("server %s removed (%s)", srv.Listen, namesLabel(srv.ServerNames)), nil

	case "location_add":
		srv, err := findServerByNames(c, req.Listen, req.ServerNames)
		if err != nil {
			return "", err
		}
		if req.Match == nil {
			return "", fmt.Errorf("location_add: match_set (type + path) is required")
		}
		if req.Action == nil {
			return "", fmt.Errorf("location_add: action is required")
		}
		matchType := normMatchType(req.Match.Type)
		path := strings.TrimSpace(req.Match.Path)
		if path == "" {
			return "", fmt.Errorf("location_add: match path is required")
		}
		if locationMatchTaken(c, req.Listen, req.ServerNames, matchType, path, nil) {
			return "", fmt.Errorf("location_add: a route with match %s %q already exists on server %s", matchType, path, req.Listen)
		}
		loc := config.LocationConfig{Match: config.MatchConfig{Type: matchType, Path: path}}
		label, err := setLocationAction(&loc, *req.Action)
		if err != nil {
			return "", err
		}
		srv.Locations = append(srv.Locations, loc)
		return fmt.Sprintf("route %s %q (%s) added on server %s", matchType, path, label, req.Listen), nil

	case "location_remove":
		srvIdx, locIdx, err := findLocationIndex(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		srv := &c.Servers[srvIdx]
		loc := srv.Locations[locIdx]
		srv.Locations = append(srv.Locations[:locIdx], srv.Locations[locIdx+1:]...)
		return fmt.Sprintf("route %s %q removed from server %s", normMatchType(loc.Match.Type), loc.Match.Path, req.Listen), nil

	case "upstream_add":
		name := strings.TrimSpace(req.Upstream)
		if name == "" {
			return "", fmt.Errorf("upstream_add: upstream name is required")
		}
		if _, err := findUpstream(c, name); err == nil {
			return "", fmt.Errorf("upstream_add: an upstream named %q already exists", name)
		}
		addr := strings.TrimSpace(req.Address)
		if addr == "" {
			return "", fmt.Errorf("upstream_add: address (first backend) is required")
		}
		weight := req.Weight
		if weight < 1 {
			weight = 1
		}
		strat := strings.TrimSpace(req.Strategy)
		switch strat {
		case "", "round_robin", "weighted_round_robin", "least_conn":
		default:
			return "", fmt.Errorf("upstream_add: invalid strategy %q (want round_robin|weighted_round_robin|least_conn)", strat)
		}
		c.Upstreams = append(c.Upstreams, config.UpstreamConfig{
			Name:     name,
			Strategy: strat,
			Servers:  []config.UpstreamServer{{Address: addr, Weight: weight}},
		})
		return fmt.Sprintf("upstream %s added with backend %s (weight %d)", name, addr, weight), nil

	case "upstream_remove":
		idx, err := findUpstreamIndex(c, req.Upstream)
		if err != nil {
			return "", err
		}
		name := c.Upstreams[idx].Name
		if refs := upstreamReferences(c, name); len(refs) > 0 {
			return "", fmt.Errorf("upstream_remove: upstream %q is still referenced by %s; repoint or remove those routes first", name, strings.Join(refs, ", "))
		}
		c.Upstreams = append(c.Upstreams[:idx], c.Upstreams[idx+1:]...)
		return fmt.Sprintf("upstream %s removed", name), nil

	default:
		return "", fmt.Errorf("unknown patch op %q", req.Op)
	}
}
