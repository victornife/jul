// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
			DirectivesFiles:   trimNonEmpty(req.WAF.DirectivesFiles),
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
		newNames := trimNonEmpty(req.NewServerNames)
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
		names := trimNonEmpty(req.ServerNames)
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
		return "", fmt.Errorf("location_set_action: unknown action %q (want proxy, static, redirect, return, or deny)", a.Kind)
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

// handleConfigPatch applies a single structured edit to the running config and
// returns the generated full diff for review BEFORE the change is applied — it
// does not persist. The UI shows the diff and the operator confirms via the
// existing /api/config/apply path with the returned candidate TOML.
// POST /api/config/patch
func (s *Server) handleConfigPatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.LoadConfig == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req patchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Snapshot the pre-patch config so the diff reflects exactly this edit.
	before, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	summary, err := applyPatch(cfg, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "The edit could not be applied.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	candidate, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Re-parse the before/after so the diff is computed over parsed models,
	// mirroring handleConfigDiff.
	beforeCfg, err := config.Parse(before)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	resp := map[string]any{
		"ok":        true,
		"summary":   summary,
		"candidate": string(candidate),
		"diff":      diffConfigs(beforeCfg, cfg),
		// base_version is the version of the config this candidate was computed
		// from. A client echoes it back to /api/config/patch/apply so a stale
		// edit is rejected (409) instead of silently clobbering a concurrent
		// change (P2-12 optimistic concurrency).
		"base_version": configVersion(before),
	}
	// Cheap preview validation: parse the candidate and run the same structural,
	// secret-expansion, and WAF/auth dry-run checks as /api/config/validate, so
	// the operator sees problems BEFORE confirming the apply. This is advisory —
	// the authoritative, heavier full-factory preflight still runs at apply time
	// (WriteConfigRaw -> applyPreflight). A failing check does not block the
	// preview: the diff is still returned so the operator can see what the edit
	// would do, with the errors surfaced alongside it.
	if verr := validateRaw(candidate); verr != nil {
		resp["validation_errors"] = humanizeErr(verr.Error())
	}
	writeJSON(w, http.StatusOK, resp)
}

// configVersion is a short, stable fingerprint of a configuration used for
// optimistic concurrency. It is computed over the canonical marshaled form, so
// it is insensitive to comments and whitespace in the on-disk file and matches
// between a preview and a later apply of the same logical config.
func configVersion(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// patchApplyRequest is a server-side, atomic, conflict-checked batch of patch
// operations. Unlike the preview endpoint it persists the result: every op is
// applied to a single freshly-loaded config under a lock, and the result is
// written through the same validated preflight as /api/config/apply.
type patchApplyRequest struct {
	// BaseVersion is the config version the ops were computed against (returned
	// by the preview as base_version, or by a config read). When non-empty the
	// apply is rejected with 409 Conflict if the live config has changed since,
	// preventing a stale edit from silently clobbering a concurrent change. An
	// empty value skips the check (an explicit force-apply).
	BaseVersion string `json:"base_version,omitempty"`
	// Ops are applied in order to one config; a failure in any op aborts the
	// whole batch before anything is written (all-or-nothing).
	Ops []patchRequest `json:"ops"`
}

// conflictResponse is the 409 body when an apply is rejected because the live
// config changed since the edit was prepared. CurrentVersion lets the client
// reload, recompute, and retry.
type conflictResponse struct {
	OK             bool   `json:"ok"`
	Conflict       bool   `json:"conflict"`
	Message        string `json:"message"`
	CurrentVersion string `json:"current_version,omitempty"`
}

// handleConfigPatchApply applies a batch of structured patch operations
// atomically and entirely server-side — it never trusts a client-rendered
// candidate. All ops are applied to one freshly-loaded config under s.applyMu,
// and the result is persisted through the same validated WriteConfigRaw
// preflight as /api/config/apply, so a config that passes cannot fail the
// subsequent build. Optimistic concurrency (base_version) prevents a stale edit
// from silently clobbering a concurrent change (P2-12 lost update).
// POST /api/config/patch/apply
func (s *Server) handleConfigPatchApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.LoadConfig == nil || s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req patchApplyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Ops) == 0 {
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "No patch operations were provided.",
			Errors:  humanizeErr("patch: at least one operation is required"),
		})
		return
	}

	// Serialize the whole read-modify-write so the version check and the write
	// are atomic. Without this, two concurrent applies could both read the same
	// base version, both pass the conflict check, and the second would silently
	// clobber the first.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	before, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	currentVersion := configVersion(before)
	if req.BaseVersion != "" && req.BaseVersion != currentVersion {
		s.recordAudit("config.patch", "config", "failure", "rejected: base version stale (concurrent change)", adminClientIP(r))
		writeJSON(w, http.StatusConflict, conflictResponse{
			OK:             false,
			Conflict:       true,
			Message:        "The configuration changed since this edit was prepared; reload and try again.",
			CurrentVersion: currentVersion,
		})
		return
	}

	// Apply every op to the single loaded config. A failure in any op aborts the
	// whole batch before anything is written, so the apply is all-or-nothing.
	summaries := make([]string, 0, len(req.Ops))
	for i, op := range req.Ops {
		summary, aerr := applyPatch(cfg, op)
		if aerr != nil {
			writeJSON(w, http.StatusBadRequest, validationErrorResponse{
				OK:      false,
				Message: fmt.Sprintf("Operation %d could not be applied; no change was made.", i+1),
				Errors:  humanizeErr(aerr.Error()),
			})
			return
		}
		summaries = append(summaries, summary)
	}

	candidate, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Snapshot the prior config, then persist through the authoritative preflight
	// (WriteConfigRaw). A rejection here means nothing was written, preserving
	// the all-or-nothing guarantee.
	prev := s.currentRaw()
	if err := s.deps.WriteConfigRaw(candidate); err != nil {
		if errors.Is(err, ErrRestartRequired) {
			s.writeRestartRequired(w, r, "config.patch", err)
			return
		}
		s.recordAudit("config.patch", "config", "failure", "rejected: invalid configuration", adminClientIP(r))
		s.emit("config", "apply_failed", "error", "Structured patch apply was rejected (invalid).")
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "The configuration contains errors; no change was applied.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	s.recordHistory(prev)
	s.recordAudit("config.patch", "config", "success", strings.Join(summaries, "; "), adminClientIP(r))
	s.emit("config", "apply", "info", "Structured patch validated and saved; the live runtime is reloading.")

	beforeCfg, _ := config.Parse(before)
	// Return a post-apply status delta so the UI can reflect what changed. It is
	// derived from the persisted configuration: the apply preflight guarantees
	// the runtime will build this config, but the reload that swaps it in is
	// asynchronous, so "pending_reload" tells the UI this is the configuration
	// taking effect rather than a confirmation that the swap has completed.
	var status []FeatureStatus
	if s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			status = s.runtimeStatus(cfg)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"pending_reload": true,
		"version":        configVersion(candidate),
		"summary":        summaries,
		"diff":           diffConfigs(beforeCfg, cfg),
		"status":         status,
		"message":        "Structured patch validated and saved. The live runtime is reloading to apply it.",
		// previous_reload mirrors the /api/config/apply response: carries
		// timed_out=true when the prior reload exceeded the configured
		// reload_timeout so the Console can surface a slow-reload warning.
		"previous_reload": func() interface{} {
			if s.deps.LastReload == nil {
				return nil
			}
			return s.deps.LastReload()
		}(),
	})
}
