// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// This file holds the per-location and location-referenced validators (actions,
// gRPC transcoding, plugins, proxy_pass, and match), split out of validate.go to
// keep each validation file focused and under the size bar (Finding CQ-3).

// validateLocation checks a single location for a valid, unambiguous action and
// that any referenced resources are well-formed.
func validateLocation(loc LocationConfig, where string, upstreamNames map[string]int, plugins map[string]PluginConfig) []error {
	var errs []error

	// Count configured actions to catch conflicts (e.g. root + proxy_pass).
	var actions []string
	if loc.Root != "" {
		actions = append(actions, "root")
	}
	if loc.ProxyPass != "" {
		actions = append(actions, "proxy_pass")
	}
	if loc.FastCGIPass != "" {
		actions = append(actions, "fastcgi_pass")
	}
	if loc.UWSGIPass != "" {
		actions = append(actions, "uwsgi_pass")
	}
	// redirect and return combine into a single redirect action: when both are
	// set, return is the redirect's status code (see router.redirectHandler), so
	// they are not in conflict. A bare return (no target) is its own action.
	switch {
	case loc.Redirect != "" && loc.Return != 0:
		actions = append(actions, "redirect")
		if loc.Return < 300 || loc.Return > 399 {
			errs = append(errs, fmt.Errorf("%s: return %d cannot be combined with a redirect target (use a 3xx status)", where, loc.Return))
		}
	case loc.Redirect != "":
		actions = append(actions, "redirect")
	case loc.Return != 0:
		actions = append(actions, "return")
	}
	if loc.Deny {
		actions = append(actions, "deny")
	}
	if loc.GRPCTranscode != nil {
		actions = append(actions, "grpc_transcode")
	}
	if loc.Plugin != "" {
		actions = append(actions, "plugin")
	}
	if len(actions) > 1 {
		errs = append(errs, fmt.Errorf("%s: conflicting actions %v (set exactly one)", where, actions))
	}

	if loc.ProxyPass != "" {
		errs = append(errs, validateProxyPass(loc.ProxyPass, where, upstreamNames)...)
	}
	// gRPC passthrough (grpc = true) is a flavor of proxy_pass, not a standalone
	// action; it requires proxy_pass to name the gRPC backend. Whether the build
	// can serve it (the "grpc" tag) is reported at handler build time.
	if loc.GRPC && loc.ProxyPass == "" {
		errs = append(errs, fmt.Errorf("%s: grpc = true requires proxy_pass (the gRPC backend)", where))
	}
	if loc.GRPCTranscode != nil {
		errs = append(errs, validateGRPCTranscode(loc.GRPCTranscode, where+".grpc_transcode", upstreamNames)...)
	}

	// Handler plugin action must reference a "handler" plugin; per-location
	// middleware plugins must reference "middleware" plugins.
	if loc.Plugin != "" {
		errs = append(errs, validatePluginRef(plugins, loc.Plugin, "handler", where+".plugin")...)
	}
	for k, name := range loc.Plugins {
		errs = append(errs, validatePluginRef(plugins, name, "middleware", fmt.Sprintf("%s.plugins[%d]", where, k))...)
	}

	for k, rw := range loc.Rewrites {
		if _, err := regexp.Compile(rw.Pattern); err != nil {
			errs = append(errs, fmt.Errorf("%s.rewrites[%d]: invalid pattern %q: %v", where, k, rw.Pattern, err))
		}
		switch rw.Flag {
		case "", "last", "break", "redirect", "permanent":
		default:
			errs = append(errs, fmt.Errorf("%s.rewrites[%d]: invalid flag %q (want last|break|redirect|permanent)", where, k, rw.Flag))
		}
	}

	if loc.Cache && loc.Root != "" {
		errs = append(errs, fmt.Errorf("%s: cache applies to proxy/fastcgi responses, not static 'root' locations", where))
	}
	if loc.RateLimit != nil {
		errs = append(errs, validateRateLimit(*loc.RateLimit, where+".rate_limit", true)...)
	}
	if loc.Auth != nil {
		errs = append(errs, validateAuth(loc.Auth, where+".auth")...)
	}
	return errs
}

// validateGRPCTranscode checks a gRPC-JSON transcoding block. The target must be
// a known upstream or a host:port, and exactly one descriptor source
// (descriptor_set file or use_reflection) must be configured. The descriptor
// file's existence is checked here; parsing it (which needs the protobuf runtime
// compiled in with the "grpc" tag) happens at handler build time.
func validateGRPCTranscode(g *GRPCTranscodeConfig, where string, upstreamNames map[string]int) []error {
	if g == nil {
		return nil
	}
	var errs []error
	if strings.TrimSpace(g.Target) == "" {
		errs = append(errs, fmt.Errorf("%s: target is required (upstream name or host:port)", where))
	} else if !strings.Contains(g.Target, ":") && upstreamNames[g.Target] == 0 {
		errs = append(errs, fmt.Errorf("%s: target %q is neither a known upstream nor a host:port", where, g.Target))
	}
	switch {
	case g.DescriptorSet == "" && !g.UseReflection:
		errs = append(errs, fmt.Errorf("%s: set exactly one of descriptor_set or use_reflection", where))
	case g.DescriptorSet != "" && g.UseReflection:
		errs = append(errs, fmt.Errorf("%s: descriptor_set and use_reflection are mutually exclusive", where))
	}
	if g.DescriptorSet != "" {
		if info, err := os.Stat(g.DescriptorSet); err != nil {
			errs = append(errs, fmt.Errorf("%s: descriptor_set %q: %v", where, g.DescriptorSet, err))
		} else if info.IsDir() {
			errs = append(errs, fmt.Errorf("%s: descriptor_set %q is a directory, not a file", where, g.DescriptorSet))
		}
	}
	switch strings.ToLower(strings.TrimSpace(g.StreamMode)) {
	case "", "ndjson", "sse":
	default:
		errs = append(errs, fmt.Errorf("%s: stream_mode %q must be \"ndjson\" or \"sse\"", where, g.StreamMode))
	}
	if g.MaxMessageSize.Bytes() < 0 {
		errs = append(errs, fmt.Errorf("%s: max_message_size must not be negative", where))
	}
	return errs
}

// validatePlugins checks each [plugins.NAME] declaration: a valid type, exactly
// one module source (path or inline), a readable path when given, sane limits,
// and that the fetch capability comes with an allowed-hosts allowlist. Whether
// the "wasm" build tag is compiled in is reported when the plugin set is built.
func validatePlugins(plugins map[string]PluginConfig) []error {
	var errs []error
	for name, p := range plugins {
		where := fmt.Sprintf("[plugins.%s]", name)
		if strings.TrimSpace(name) == "" {
			errs = append(errs, errors.New("[plugins]: plugin name must not be empty"))
		}
		switch strings.TrimSpace(p.Type) {
		case "", "middleware", "handler":
		default:
			errs = append(errs, fmt.Errorf("%s: invalid type %q (want middleware|handler)", where, p.Type))
		}
		hasPath := strings.TrimSpace(p.Path) != ""
		hasInline := strings.TrimSpace(p.Inline) != ""
		switch {
		case hasPath && hasInline:
			errs = append(errs, fmt.Errorf("%s: set exactly one of path or inline, not both", where))
		case !hasPath && !hasInline:
			errs = append(errs, fmt.Errorf("%s: a module source is required (set path or inline)", where))
		}
		if hasPath {
			if info, err := os.Stat(p.Path); err != nil {
				errs = append(errs, fmt.Errorf("%s: path %q: %v", where, p.Path, err))
			} else if info.IsDir() {
				errs = append(errs, fmt.Errorf("%s: path %q is a directory, not a .wasm file", where, p.Path))
			}
		}
		if p.MemoryLimit.Bytes() < 0 {
			errs = append(errs, fmt.Errorf("%s: memory_limit must not be negative", where))
		}
		if p.Timeout.Std() < 0 {
			errs = append(errs, fmt.Errorf("%s: timeout must not be negative", where))
		}
		if p.Fetch && len(p.AllowedHosts) == 0 {
			errs = append(errs, fmt.Errorf("%s: fetch is enabled but allowed_hosts is empty (an allowlist is required)", where))
		}
		if p.MaxRequestBody.Bytes() < 0 {
			errs = append(errs, fmt.Errorf("%s: max_request_body must not be negative", where))
		}
		if p.MaxResponseBody.Bytes() < 0 {
			errs = append(errs, fmt.Errorf("%s: max_response_body must not be negative", where))
		}
		if p.MaxFetchResponse.Bytes() < 0 {
			errs = append(errs, fmt.Errorf("%s: max_fetch_response must not be negative", where))
		}
		if p.FetchTimeout.Std() < 0 {
			errs = append(errs, fmt.Errorf("%s: fetch_timeout must not be negative", where))
		}
		if p.KVMaxEntries < 0 {
			errs = append(errs, fmt.Errorf("%s: kv_max_entries must not be negative", where))
		}
		if p.KVMaxBytes.Bytes() < 0 {
			errs = append(errs, fmt.Errorf("%s: kv_max_bytes must not be negative", where))
		}
	}
	return errs
}

// validatePluginRef checks that a referenced plugin name exists and has the
// expected type (middleware or handler).
func validatePluginRef(plugins map[string]PluginConfig, name, wantType, where string) []error {
	if strings.TrimSpace(name) == "" {
		return []error{fmt.Errorf("%s: plugin name must not be empty", where)}
	}
	p, ok := plugins[name]
	if !ok {
		return []error{fmt.Errorf("%s: references unknown plugin %q (declare it under [plugins.%s])", where, name, name)}
	}
	got := strings.TrimSpace(p.Type)
	if got == "" {
		got = "middleware"
	}
	if got != wantType {
		return []error{fmt.Errorf("%s: plugin %q has type %q but a %q plugin is required here", where, name, got, wantType)}
	}
	return nil
}

// validateProxyPass checks the proxy_pass target form and, for upstream
// references, that the named upstream exists.
func validateProxyPass(pass, where string, upstreamNames map[string]int) []error {
	u, err := url.Parse(pass)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return []error{fmt.Errorf("%s: invalid proxy_pass %q (want http(s)://host:port or http://upstream-name)", where, pass)}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return []error{fmt.Errorf("%s: proxy_pass scheme %q must be http or https", where, u.Scheme)}
	}
	// A host without a port that is not a known upstream and not an IP looks
	// like an upstream reference typo.
	if !strings.Contains(u.Host, ":") && upstreamNames[u.Host] == 0 && !strings.Contains(u.Host, ".") {
		return []error{fmt.Errorf("%s: proxy_pass references unknown upstream %q", where, u.Host)}
	}
	// Reject embedded credentials so secrets are not stored in the config file
	// and do not leak through search projections.
	if u.User != nil {
		return []error{fmt.Errorf("%s: proxy_pass must not contain credentials (use headers or TLS for authentication)", where)}
	}
	return nil
}

func validateMatch(m MatchConfig, where string) error {
	switch m.Type {
	case "exact", "prefix", "regex":
	case "":
		return fmt.Errorf("%s: match.type is required (exact|prefix|regex)", where)
	default:
		return fmt.Errorf("%s: invalid match.type %q (want exact|prefix|regex)", where, m.Type)
	}
	if strings.TrimSpace(m.Path) == "" {
		return fmt.Errorf("%s: match.path is required", where)
	}
	if m.Type == "regex" {
		if _, err := regexp.Compile(m.Path); err != nil {
			return fmt.Errorf("%s: invalid match regex %q: %v", where, m.Path, err)
		}
	}
	return nil
}
