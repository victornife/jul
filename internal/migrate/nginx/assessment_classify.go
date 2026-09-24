// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"sort"
	"strconv"
	"strings"

	"jul/internal/clientaddr"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
)

type assessmentWalker struct {
	assessment *Assessment
}

type walkFacts struct {
	extraListen  bool
	corsConflict bool
	// httpProxyProtocolUsable is true when an HTTP server block declares a
	// complete, self-consistent PROXY-protocol identity trio: a `listen ...
	// proxy_protocol;` token, `real_ip_header proxy_protocol;`, and at least
	// one valid `set_real_ip_from`. It gates both the listen token and the
	// real_ip_header directive so neither can be classified supported alone.
	httpProxyProtocolUsable bool
	// streamIsUDP is true when a stream server block's listen directive
	// carries the udp token. It gates the standalone outbound `proxy_protocol`
	// directive, which Jul only supports for tcp streams.
	streamIsUDP bool // upstreamMaxFailsConsistent and upstreamFailTimeoutConsistent are true
	// when every server in the enclosing upstream block that specifies
	// max_fails (respectively fail_timeout) agrees on the same value. Jul's
	// circuit breaker is upstream-wide, not per-backend, so a disagreement
	// cannot be translated without silently picking one backend's threshold.
	upstreamMaxFailsConsistent    bool
	upstreamFailTimeoutConsistent bool
	// serverClientAuthUsable is true when an HTTP server block declares a
	// complete mTLS pairing: `ssl_verify_client on|optional;` plus a non-empty
	// `ssl_client_certificate`. It gates both directives so neither can be
	// classified supported alone (Jul's client_auth always requires a
	// ca_file whenever its mode is not "none").
	serverClientAuthUsable bool
}

func (w *assessmentWalker) walk(context AssessmentContext, d ngx.IDirective, facts walkFacts) {
	if d == nil {
		return
	}
	cap := classifyDirective(context, d, facts)
	w.assessment.Results = append(w.assessment.Results, AssessmentResult{
		Code:        cap.code,
		Class:       cap.class,
		Severity:    cap.severity,
		Risk:        cap.risk,
		Context:     context,
		Directive:   d.GetName(),
		Line:        d.GetLine(),
		Message:     cap.message,
		TargetPaths: append([]string(nil), cap.targetPaths...),
	})

	childContext, recurse := nestedContext(context, d.GetName())
	if !recurse {
		return
	}
	kids := orderedChildren(d)
	locationFacts := walkFacts{}
	switch {
	case childContext == ContextLocation:
		locationFacts.corsConflict = hasStaticCORSConflict(kids)
	case childContext == ContextServer:
		locationFacts.httpProxyProtocolUsable = serverHasUsableHTTPProxyProtocolIdentity(kids)
		locationFacts.serverClientAuthUsable = serverHasUsableClientAuth(kids)
	case childContext == ContextStream && d.GetName() == "server":
		locationFacts.streamIsUDP = streamServerListenIsUDP(kids)
	case childContext == ContextUpstream:
		locationFacts.upstreamMaxFailsConsistent, locationFacts.upstreamFailTimeoutConsistent = upstreamFailoverConsistency(kids)
	}
	seenListen := false
	for _, child := range kids {
		childFacts := locationFacts
		if childContext == ContextServer && child.GetName() == "listen" {
			childFacts.extraListen = seenListen
			seenListen = true
		}
		w.walk(childContext, child, childFacts)
	}
}

func classifyDirective(context AssessmentContext, d ngx.IDirective, facts walkFacts) capability {
	name := d.GetName()
	params := paramValues(d)
	if isRealIPDirective(name) {
		return classifyRealIP(name, params, facts)
	}
	if cap, ok := capabilityRegistry[capabilityKey{context: context, name: name}]; ok {
		switch {
		case context == ContextServer && name == "listen":
			return classifyListen(params, facts.extraListen, facts.httpProxyProtocolUsable)
		case context == ContextStream && name == "listen":
			return classifyStreamListen(params)
		case context == ContextStream && name == "proxy_pass":
			return classifyStreamProxyPass(params)
		case context == ContextStream && name == "proxy_protocol":
			return classifyStreamProxyProtocol(params, facts.streamIsUDP)
		case context == ContextStream && name == "proxy_timeout":
			return classifyStreamDuration("proxy_timeout", "NGX_STREAM_PROXY_TIMEOUT", params)
		case context == ContextStream && name == "proxy_connect_timeout":
			return classifyStreamDuration("proxy_connect_timeout", "NGX_STREAM_PROXY_CONNECT_TIMEOUT", params)
		case context == ContextStream && name == "ssl_preread":
			return classifyStreamSSLPreread(params)
		case context == ContextServer && name == "ssl_protocols":
			return classifyTLSProtocols(params)
		case context == ContextServer && name == "ssl_verify_client":
			return classifySSLVerifyClient(params, facts.serverClientAuthUsable)
		case context == ContextServer && name == "ssl_client_certificate":
			return classifySSLClientCertificate(params, facts.serverClientAuthUsable)
		case context == ContextLocation && name == "proxy_pass":
			return classifyProxyPass(params)
		case context == ContextLocation && name == "grpc_pass":
			return classifyGRPCPass(params)
		case context == ContextLocation && name == "fastcgi_param":
			return classifyFastCGIParam(params)
		case context == ContextHTTP && name == "proxy_cache_path":
			return classifyProxyCachePath(params)
		case context == ContextLocation && name == "proxy_cache":
			return classifyProxyCache(params)
		case context == ContextLocation && name == "proxy_cache_valid":
			return classifyProxyCacheValid(params)
		case context == ContextLocation && name == "proxy_connect_timeout":
			return classifyLocationDuration("proxy_connect_timeout", "NGX_LOCATION_PROXY_CONNECT_TIMEOUT", params)
		case context == ContextLocation && name == "proxy_read_timeout":
			return classifyLocationDuration("proxy_read_timeout", "NGX_LOCATION_PROXY_READ_TIMEOUT", params)
		case context == ContextLocation && name == "proxy_send_timeout":
			return classifyLocationDuration("proxy_send_timeout", "NGX_LOCATION_PROXY_SEND_TIMEOUT", params)
		case context == ContextLocation && name == "proxy_next_upstream_tries":
			return classifyProxyNextUpstreamTries(params)
		case context == ContextLocation && name == "return":
			return classifyReturn(params, false)
		case context == ContextLocation && name == "rewrite":
			return classifyRewrite(params)
		case context == ContextLocation && name == "add_header":
			return classifyAddHeader(params, facts.corsConflict)
		case context == ContextLocation && name == "limit_except":
			return classifyLimitExcept(d, params)
		case context == ContextServer && name == "return":
			return classifyReturn(params, true)
		case context == ContextUpstream && name == "server":
			return classifyUpstreamServer(params, facts.upstreamMaxFailsConsistent, facts.upstreamFailTimeoutConsistent)
		case context == ContextServer && name == "location":
			return classifyLocation(d)
		default:
			return cap
		}
	}

	switch context {
	case ContextEvents:
		return ignored("NGX_EVENTS_UNMAPPED", RiskOperational, "NGINX event-loop directive has no Jul configuration equivalent")
	case ContextStream:
		return blocking("NGX_STREAM_UNSUPPORTED", RiskRouting, "directive belongs to the unsupported NGINX stream module")
	case ContextMail:
		return blocking("NGX_MAIL_UNSUPPORTED", RiskRouting, "directive belongs to the unsupported NGINX mail module")
	case ContextVariable:
		return blocking("NGX_VARIABLE_BLOCK_UNSUPPORTED", RiskRouting, "directive belongs to an unsupported variable-driven block")
	default:
		return blocking("NGX_DIRECTIVE_UNSUPPORTED", defaultRisk(name), "directive is not translated in this context")
	}
}

func classifyRealIP(name string, params []string, facts walkFacts) capability {
	switch name {
	case "set_real_ip_from":
		if len(params) == 0 || strings.HasPrefix(strings.TrimSpace(params[0]), "unix:") {
			return blocking("NGX_REALIP_TRUST_SOURCE", RiskSecurity, "trusted proxy source is missing or not representable")
		}
		if _, err := clientaddr.ParsePrefix(strings.TrimSpace(params[0])); err != nil {
			return blocking("NGX_REALIP_TRUST_SOURCE", RiskSecurity, "trusted proxy source is not a canonical IP address or CIDR")
		}
		return supported("NGX_REALIP_TRUST_SOURCE", RiskSecurity, "trusted proxy source is translated", []string{"servers[].client_address.trusted_proxies"})
	case "real_ip_header":
		if len(params) == 0 {
			return blocking("NGX_REALIP_HEADER", RiskSecurity, "real_ip_header is missing a supported header name")
		}
		switch strings.ToLower(strings.TrimSpace(params[0])) {
		case "x-forwarded-for", "forwarded":
			return supported("NGX_REALIP_HEADER", RiskSecurity, "trusted forwarded header is translated", []string{"servers[].client_address.forwarded_headers"})
		case "proxy_protocol":
			if facts.httpProxyProtocolUsable {
				return supported("NGX_REALIP_HEADER_PROXY_PROTOCOL", RiskSecurity, "trusted inbound PROXY-protocol identity is translated", []string{"servers[].proxy_protocol", "servers[].client_address.trusted_proxies"})
			}
			return blocking("NGX_REALIP_HEADER_PROXY_PROTOCOL", RiskSecurity, "real_ip_header proxy_protocol requires a matching 'listen ... proxy_protocol;' and at least one valid set_real_ip_from in the same server block")
		default:
			return blocking("NGX_REALIP_HEADER", RiskSecurity, "this real_ip_header form is not safely representable")
		}
	case "real_ip_recursive":
		if len(params) > 0 && strings.EqualFold(strings.TrimSpace(params[0]), "off") {
			return blocking("NGX_REALIP_RECURSIVE", RiskSecurity, "Jul always evaluates trusted proxy chains right to left")
		}
		return supported("NGX_REALIP_RECURSIVE", RiskSecurity, "right-to-left trusted-chain evaluation is already Jul's behavior", nil)
	default:
		return blocking("NGX_REALIP_UNSUPPORTED", RiskSecurity, "real-IP directive is not translated")
	}
}

func classifyListen(params []string, extra bool, proxyProtocolUsable bool) capability {
	if extra {
		return approximated("NGX_SERVER_EXTRA_LISTEN", RiskAvailability, "only the first distinct listen address in a server block is kept")
	}
	listen, _ := parseListen(params)
	if listen == "" {
		return blocking("NGX_SERVER_LISTEN_UNSUPPORTED", RiskAvailability, "listen address is missing or not representable")
	}
	for _, p := range params[1:] {
		switch strings.ToLower(p) {
		case "ssl":
			continue
		case "http2", "default_server":
			return approximated("NGX_SERVER_LISTEN_OPTION", RiskAvailability, "listen option is implicit or has different selection semantics in Jul")
		case "proxy_protocol":
			if !proxyProtocolUsable {
				return blocking("NGX_SERVER_LISTEN_PROXY_PROTOCOL", RiskSecurity, "proxy_protocol requires a matching real_ip_header proxy_protocol and at least one valid set_real_ip_from in the same server block")
			}
		default:
			return blocking("NGX_SERVER_LISTEN_OPTION", RiskSecurity, "listen option is not translated")
		}
	}
	return capabilityRegistry[capabilityKey{ContextServer, "listen"}]
}

func classifyTLSProtocols(params []string) capability {
	if len(params) == 0 {
		return blocking("NGX_SERVER_TLS_PROTOCOLS", RiskSecurity, "ssl_protocols is empty")
	}
	legacy := false
	supportedProtocol := false
	for _, p := range params {
		switch p {
		case "TLSv1.2", "TLSv1.3":
			supportedProtocol = true
		case "TLSv1", "TLSv1.1", "SSLv2", "SSLv3":
			legacy = true
		default:
			return blocking("NGX_SERVER_TLS_PROTOCOLS", RiskSecurity, "ssl_protocols contains an unknown protocol")
		}
	}
	if legacy {
		return approximated("NGX_SERVER_TLS_PROTOCOLS_LEGACY", RiskSecurity, "legacy protocols are dropped and the minimum is raised to TLS 1.2")
	}
	if !supportedProtocol {
		return blocking("NGX_SERVER_TLS_PROTOCOLS", RiskSecurity, "no supported TLS protocol remains")
	}
	return capabilityRegistry[capabilityKey{ContextServer, "ssl_protocols"}]
}

func classifyLocation(d ngx.IDirective) capability {
	mod, path, ok := locationModifierAndPath(d)
	if !ok || strings.HasPrefix(path, "@") {
		return blocking("NGX_LOCATION_MATCH", RiskRouting, "location match is not representable")
	}
	switch mod {
	case "", "=", "~":
		return capabilityRegistry[capabilityKey{ContextServer, "location"}]
	case "^~", "~*":
		return approximated("NGX_LOCATION_MATCH", RiskRouting, "location modifier has precedence or case-sensitivity semantics Jul cannot preserve exactly")
	default:
		return approximated("NGX_LOCATION_MATCH", RiskRouting, "unknown location modifier is treated as a prefix match")
	}
}

func classifyProxyPass(params []string) capability {
	if len(params) == 0 || strings.TrimSpace(params[0]) == "" {
		return blocking("NGX_LOCATION_PROXY_PASS", RiskRouting, "proxy_pass target is missing")
	}
	v := strings.TrimSpace(params[0])
	if strings.Contains(v, "$") {
		return blocking("NGX_LOCATION_PROXY_PASS_DYNAMIC", RiskSecurity, "variable-derived proxy targets are not translated")
	}
	if proxyPassHasURI(v) {
		return approximated("NGX_LOCATION_PROXY_PASS_URI", RiskRouting, "proxy_pass URI rewriting semantics are not preserved exactly")
	}
	return capabilityRegistry[capabilityKey{ContextLocation, "proxy_pass"}]
}

func proxyPassHasURI(v string) bool {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(v, "http://"), "https://")
	return strings.Contains(trimmed, "/")
}

// classifyGRPCPass judges a location's grpc_pass in isolation: Jul's native
// gRPC passthrough (loc.GRPC=true) reuses the same proxy_pass field ordinary
// HTTP proxying does, dialing h2c for a grpc:// target or HTTP/2+TLS for
// grpcs://, so it is bound by the same limits - no variable-derived target,
// no direct Unix socket (a named [[upstreams]] entry is required, exactly as
// plain proxy_pass requires) - plus grpc_pass's own scheme vocabulary.
func classifyGRPCPass(params []string) capability {
	if len(params) == 0 || strings.TrimSpace(params[0]) == "" {
		return blocking("NGX_LOCATION_GRPC_PASS", RiskRouting, "grpc_pass target is missing")
	}
	v := strings.TrimSpace(params[0])
	if strings.Contains(v, "$") {
		return blocking("NGX_LOCATION_GRPC_PASS_DYNAMIC", RiskSecurity, "variable-derived grpc_pass targets are not translated")
	}
	if strings.Contains(strings.ToLower(v), "unix:") {
		return blocking("NGX_LOCATION_GRPC_PASS_UNIX", RiskRouting, "direct Unix grpc_pass is not representable; create a named [[upstreams]] entry with servers = [\"unix:/path/to/socket.sock\"] and proxy_pass = \"http://<upstream-name>\", grpc = true")
	}
	if _, recognized := normalizeGRPCPassScheme(v); !recognized {
		return blocking("NGX_LOCATION_GRPC_PASS_SCHEME", RiskRouting, "grpc_pass scheme is not representable; only grpc:// (h2c), grpcs:// (HTTP/2+TLS), or a bare upstream/host name are translated")
	}
	return capabilityRegistry[capabilityKey{ContextLocation, "grpc_pass"}]
}

// classifyFastCGIParam judges one fastcgi_param name/value pair in isolation.
// Only a literal value (no nginx variable) is representable: Jul's
// fastcgi_params is a static map, with no per-request variable substitution
// engine behind it.
func classifyFastCGIParam(params []string) capability {
	if len(params) < 2 || strings.TrimSpace(params[0]) == "" {
		return blocking("NGX_LOCATION_FASTCGI_PARAM", RiskRouting, "fastcgi_param requires a name and a value")
	}
	if strings.Contains(params[1], "$") {
		return blocking("NGX_LOCATION_FASTCGI_PARAM_DYNAMIC", RiskRouting, "variable-derived fastcgi_param values are not translated; Jul's fastcgi_params is a static map")
	}
	return capabilityRegistry[capabilityKey{ContextLocation, "fastcgi_param"}]
}

// classifyProxyCachePath judges a `proxy_cache_path` declaration in
// isolation: a non-empty, non-variable path and a well-formed
// keys_zone=name:size token. Whether this zone is the single one actually in
// consistent use across every location is a whole-file question the
// translator resolves separately (resolveHTTPCache) and surfaces as a
// synthetic NGX_CACHE_ZONE_CONFLICT finding when it is not.
func classifyProxyCachePath(params []string) capability {
	if _, name, ok := parseCacheZonePath(params); !ok || name == "" {
		return blocking("NGX_HTTP_CACHE_PATH", RiskPerformance, "proxy_cache_path is missing a path or a valid keys_zone=name:size")
	}
	return capabilityRegistry[capabilityKey{ContextHTTP, "proxy_cache_path"}]
}

// classifyProxyCache judges a location's `proxy_cache <name>;` in isolation.
// An explicit "off" opts out (matching Jul's default of no per-location
// cache) and a variable-derived name cannot be resolved statically; any
// other name is optimistically supported here, the same pattern used for
// set_real_ip_from/real_ip_header, with cross-location zone-consistency
// conflicts added afterward as a synthetic finding rather than judged per
// directive.
func classifyProxyCache(params []string) capability {
	if len(params) == 0 || strings.TrimSpace(params[0]) == "" {
		return blocking("NGX_LOCATION_CACHE_MISSING", RiskSecurity, "proxy_cache has no zone name")
	}
	name := strings.TrimSpace(params[0])
	if name == "off" {
		return ignored("NGX_LOCATION_CACHE_OFF", RiskSecurity, "explicit proxy_cache off matches Jul's default of no per-location cache")
	}
	if strings.Contains(name, "$") {
		return blocking("NGX_LOCATION_CACHE_DYNAMIC", RiskSecurity, "variable-derived cache zone selection is not translated")
	}
	return capabilityRegistry[capabilityKey{ContextLocation, "proxy_cache"}]
}

// classifyProxyCacheValid recognizes the bounded proxy_cache_valid forms Jul's
// single default_ttl can represent, reusing parseSimpleCacheValidTime so the
// assessment and the translator can never disagree about which forms
// resolve.
func classifyProxyCacheValid(params []string) capability {
	if _, ok := parseSimpleCacheValidTime(params); !ok {
		return blocking("NGX_LOCATION_CACHE_VALID_UNSUPPORTED", RiskPerformance, "proxy_cache_valid uses per-status-code times, the \"any\" keyword, or a malformed time; Jul has one default_ttl")
	}
	return capabilityRegistry[capabilityKey{ContextLocation, "proxy_cache_valid"}]
}

// classifyLocationDuration judges an HTTP location's proxy_connect_timeout/
// proxy_read_timeout/proxy_send_timeout, reusing the same nginx duration
// parser as the stream equivalents so the assessment and translator can never
// disagree about which forms resolve.
func classifyLocationDuration(directive, code string, params []string) capability {
	if len(params) == 0 {
		return blocking(code, RiskAvailability, "duration value is missing")
	}
	if _, ok := parseNginxDuration(params[0]); !ok {
		return blocking(code, RiskAvailability, "duration value is not representable (supported units: ms, s, m, h)")
	}
	return capabilityRegistry[capabilityKey{ContextLocation, directive}]
}

// classifyProxyNextUpstreamTries judges proxy_next_upstream_tries: only an
// explicit bound of 2 or more is representable, since Jul's retry_attempts=0
// means "inherit the pool default", not an explicit zero, so nginx's 0
// (unlimited) and 1 (no retry) cannot be distinguished from it.
func classifyProxyNextUpstreamTries(params []string) capability {
	if len(params) == 0 {
		return blocking("NGX_LOCATION_RETRY_ATTEMPTS", RiskAvailability, "proxy_next_upstream_tries has no value")
	}
	n, err := strconv.Atoi(params[0])
	if err != nil {
		return blocking("NGX_LOCATION_RETRY_ATTEMPTS", RiskAvailability, "proxy_next_upstream_tries is not a whole number")
	}
	if n < 2 {
		return blocking("NGX_LOCATION_RETRY_ATTEMPTS", RiskAvailability, "0 (unlimited) and 1 (no retry) cannot be distinguished from Jul's retry_attempts=0, which means \"inherit the pool default\"")
	}
	return capabilityRegistry[capabilityKey{ContextLocation, "proxy_next_upstream_tries"}]
}

func classifyReturn(params []string, serverLevel bool) capability {
	if len(params) == 0 {
		return blocking("NGX_RETURN_MALFORMED", RiskRouting, "return directive has no status or target")
	}
	if serverLevel {
		return capabilityRegistry[capabilityKey{ContextServer, "return"}]
	}
	code, err := strconv.Atoi(params[0])
	if err == nil {
		if len(params) > 1 && (code < 300 || code >= 400) {
			return approximated("NGX_LOCATION_RETURN_BODY", RiskRouting, "non-redirect response body is dropped")
		}
		if code >= 300 && code < 400 && len(params) > 1 {
			return classifyRedirectTarget(params[1])
		}
		return capabilityRegistry[capabilityKey{ContextLocation, "return"}]
	}
	return classifyRedirectTarget(params[0])
}

func classifyRedirectTarget(target string) capability {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//") {
		return approximated(
			"NGX_LOCATION_RETURN_ABSOLUTE_REDIRECT",
			RiskRouting,
			"NGINX expands a local redirect to an absolute URL by default while Jul preserves the relative target",
		)
	}
	return capabilityRegistry[capabilityKey{ContextLocation, "return"}]
}

func classifyRewrite(params []string) capability {
	if len(params) < 2 {
		return blocking("NGX_LOCATION_REWRITE", RiskRouting, "rewrite requires a pattern and replacement")
	}
	if len(params) > 2 {
		switch params[2] {
		case "last", "break", "redirect", "permanent":
		default:
			return approximated("NGX_LOCATION_REWRITE_FLAG", RiskRouting, "unknown rewrite flag is ignored")
		}
	}
	return capabilityRegistry[capabilityKey{ContextLocation, "rewrite"}]
}

func classifyAddHeader(params []string, corsConflict bool) capability {
	if len(params) < 2 {
		return blocking("NGX_LOCATION_ADD_HEADER", RiskSecurity, "add_header is malformed")
	}
	name, value := params[0], unquoteHeaderValue(params[1])
	always := len(params) > 2 && params[2] == "always"
	if !always {
		return blocking("NGX_LOCATION_ADD_HEADER_STATUS", RiskSecurity, "header lacks always; translating it would widen application to error responses")
	}
	if strings.Contains(value, "$") {
		return blocking("NGX_LOCATION_ADD_HEADER_DYNAMIC", RiskSecurity, "variable-derived response-header values are not translated")
	}
	if corsConflict && corsHeaderField(name) != "" {
		return blocking("NGX_LOCATION_CORS_CONFLICT", RiskSecurity, "static CORS headers combine wildcard origin with credentials")
	}
	if strings.EqualFold(name, "Access-Control-Max-Age") {
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err != nil || n < 0 {
			return blocking("NGX_LOCATION_CORS_MAX_AGE", RiskSecurity, "CORS max age is not a non-negative whole number")
		}
	}
	return capabilityRegistry[capabilityKey{ContextLocation, "add_header"}]
}

func classifyLimitExcept(d ngx.IDirective, params []string) capability {
	if len(params) == 0 {
		return blocking("NGX_LOCATION_LIMIT_EXCEPT", RiskSecurity, "limit_except contains no methods")
	}
	kids := children(d)
	if len(kids) != 1 || !isDenyAllOrReturn403(kids[0]) {
		return blocking("NGX_LOCATION_LIMIT_EXCEPT_BODY", RiskSecurity, "limit_except body is not a bare deny-all or return-403")
	}
	return capabilityRegistry[capabilityKey{ContextLocation, "limit_except"}]
}

func classifyUpstreamServer(params []string, maxFailsConsistent, failTimeoutConsistent bool) capability {
	if len(params) == 0 || strings.TrimSpace(params[0]) == "" {
		return blocking("NGX_UPSTREAM_SERVER", RiskAvailability, "upstream server has no address")
	}
	for _, p := range params[1:] {
		switch {
		case strings.HasPrefix(p, "weight="):
			if n, err := strconv.Atoi(strings.TrimPrefix(p, "weight=")); err != nil || n < 1 {
				return blocking("NGX_UPSTREAM_SERVER_WEIGHT", RiskAvailability, "upstream weight is invalid")
			}
		case p == "down":
			return approximated("NGX_UPSTREAM_SERVER_DOWN", RiskAvailability, "backend marked down is omitted from the generated pool")
		case strings.HasPrefix(p, "max_fails="):
			if n, err := strconv.Atoi(strings.TrimPrefix(p, "max_fails=")); err != nil || n < 0 {
				return blocking("NGX_UPSTREAM_SERVER_MAX_FAILS", RiskAvailability, "max_fails is not a non-negative whole number")
			}
			if !maxFailsConsistent {
				return approximated("NGX_UPSTREAM_SERVER_MAX_FAILS", RiskAvailability, "backends in this upstream disagree on max_fails; Jul's circuit breaker is upstream-wide, so its own default was kept")
			}
		case strings.HasPrefix(p, "fail_timeout="):
			if _, ok := parseNginxDuration(strings.TrimPrefix(p, "fail_timeout=")); !ok {
				return blocking("NGX_UPSTREAM_SERVER_FAIL_TIMEOUT", RiskAvailability, "fail_timeout is not a representable duration")
			}
			if !failTimeoutConsistent {
				return approximated("NGX_UPSTREAM_SERVER_FAIL_TIMEOUT", RiskAvailability, "backends in this upstream disagree on fail_timeout; Jul's circuit breaker is upstream-wide, so its own default was kept")
			}
		default:
			return blocking("NGX_UPSTREAM_SERVER_OPTION", RiskAvailability, "upstream server option is not translated")
		}
	}
	return capabilityRegistry[capabilityKey{ContextUpstream, "server"}]
}

// upstreamFailoverConsistency scans every "server" directive in an upstream
// block and reports whether every explicit max_fails value (respectively
// fail_timeout value) agrees. Jul's circuit breaker is upstream-wide
// (config.ResilienceConfig), unlike nginx's per-backend max_fails/
// fail_timeout, so translation is only lossless when there is nothing to
// disagree about.
func upstreamFailoverConsistency(kids []ngx.IDirective) (maxFailsConsistent, failTimeoutConsistent bool) {
	var maxFails []string
	var failTimeout []string
	for _, c := range kids {
		if c.GetName() != "server" {
			continue
		}
		for _, p := range paramValues(c) {
			switch {
			case strings.HasPrefix(p, "max_fails="):
				maxFails = append(maxFails, strings.TrimPrefix(p, "max_fails="))
			case strings.HasPrefix(p, "fail_timeout="):
				failTimeout = append(failTimeout, strings.TrimPrefix(p, "fail_timeout="))
			}
		}
	}
	return allStringsSame(maxFails), allStringsSame(failTimeout)
}

func allStringsSame(vs []string) bool {
	if len(vs) == 0 {
		return true
	}
	for _, v := range vs[1:] {
		if v != vs[0] {
			return false
		}
	}
	return true
}

// serverHasUsableHTTPProxyProtocolIdentity reports whether an HTTP server
// block declares the complete trio Jul needs to translate inbound
// PROXY-protocol identity: the listen token, the matching real_ip_header
// value, and at least one valid trusted CIDR. Any one missing keeps the
// listen token and the real_ip_header directive both blocking, rather than
// promoting an incomplete or inert declaration.
func serverHasUsableHTTPProxyProtocolIdentity(kids []ngx.IDirective) bool {
	var listenHasToken, headerIsProxyProtocol, hasValidTrustedSource bool
	for _, c := range kids {
		switch c.GetName() {
		case "listen":
			for _, p := range paramValues(c) {
				if strings.EqualFold(p, "proxy_protocol") {
					listenHasToken = true
				}
			}
		case "real_ip_header":
			p := paramValues(c)
			if len(p) > 0 && strings.EqualFold(strings.TrimSpace(p[0]), "proxy_protocol") {
				headerIsProxyProtocol = true
			}
		case "set_real_ip_from":
			p := paramValues(c)
			if len(p) == 0 {
				continue
			}
			entry := strings.TrimSpace(p[0])
			if strings.HasPrefix(entry, "unix:") {
				continue
			}
			if _, err := clientaddr.ParsePrefix(entry); err == nil {
				hasValidTrustedSource = true
			}
		}
	}
	return listenHasToken && headerIsProxyProtocol && hasValidTrustedSource
}

// serverHasUsableClientAuth reports whether an HTTP server block declares a
// complete mTLS pairing: `ssl_verify_client on|optional;` plus a non-empty
// `ssl_client_certificate`. Jul's client_auth always requires a ca_file
// whenever its mode is not "none", so ssl_verify_client alone (no CA bundle)
// or ssl_client_certificate alone (nginx never enables verification without
// ssl_verify_client either) cannot be translated; both stay blocking unless
// both are present together.
func serverHasUsableClientAuth(kids []ngx.IDirective) bool {
	var mode, caFile string
	for _, c := range kids {
		switch c.GetName() {
		case "ssl_verify_client":
			if p := paramValues(c); len(p) > 0 {
				mode = strings.ToLower(strings.TrimSpace(p[0]))
			}
		case "ssl_client_certificate":
			if p := paramValues(c); len(p) > 0 {
				caFile = strings.TrimSpace(p[0])
			}
		}
	}
	return (mode == "on" || mode == "optional") && caFile != ""
}

// classifySSLVerifyClient judges `ssl_verify_client` in isolation plus the
// cross-directive usable fact. "optional_no_ca" accepts a client certificate
// without validating it against any CA at all, which Jul's client_auth
// cannot represent (it always validates against ca_file when enabled), so it
// stays blocking regardless of sibling directives.
func classifySSLVerifyClient(params []string, usable bool) capability {
	if len(params) == 0 {
		return blocking("NGX_SERVER_CLIENT_AUTH_MODE", RiskSecurity, "ssl_verify_client has no value")
	}
	switch strings.ToLower(strings.TrimSpace(params[0])) {
	case "on", "optional":
		if !usable {
			return blocking("NGX_SERVER_CLIENT_AUTH_MODE", RiskSecurity, "ssl_verify_client requires a non-empty ssl_client_certificate in the same server block")
		}
		return capabilityRegistry[capabilityKey{ContextServer, "ssl_verify_client"}]
	case "optional_no_ca":
		return blocking("NGX_SERVER_CLIENT_AUTH_NO_CA", RiskSecurity, "optional_no_ca accepts a client certificate without validating it against any CA; Jul's client_auth always validates against ca_file once enabled")
	case "off":
		return ignored("NGX_SERVER_CLIENT_AUTH_OFF", RiskSecurity, "explicit ssl_verify_client off matches Jul's default of no client-certificate verification")
	default:
		return blocking("NGX_SERVER_CLIENT_AUTH_MODE", RiskSecurity, "ssl_verify_client value is not recognized")
	}
}

// classifySSLClientCertificate judges `ssl_client_certificate` in isolation
// plus the cross-directive usable fact: without a matching ssl_verify_client
// on|optional, nginx never actually enables verification either, so an
// orphaned ssl_client_certificate is not representable as mTLS.
func classifySSLClientCertificate(params []string, usable bool) capability {
	if len(params) == 0 || strings.TrimSpace(params[0]) == "" {
		return blocking("NGX_SERVER_CLIENT_AUTH_CA", RiskSecurity, "ssl_client_certificate has no path")
	}
	if !usable {
		return blocking("NGX_SERVER_CLIENT_AUTH_CA", RiskSecurity, "ssl_client_certificate requires ssl_verify_client on|optional in the same server block")
	}
	return capabilityRegistry[capabilityKey{ContextServer, "ssl_client_certificate"}]
}

// streamServerListenIsUDP reports whether a stream server block's listen
// directive carries the udp token, gating the standalone outbound
// proxy_protocol directive (Jul only supports it for tcp streams).
func streamServerListenIsUDP(kids []ngx.IDirective) bool {
	for _, c := range kids {
		if c.GetName() != "listen" {
			continue
		}
		for _, p := range paramValues(c) {
			if strings.EqualFold(p, "udp") {
				return true
			}
		}
	}
	return false
}

// streamListenTokens are the recognized stream `listen` parameters beyond the
// address itself. Anything else is an unrecognized option and stays blocking.
var streamListenOperationalTokens = map[string]bool{
	"bind": true, "reuseport": true,
}

// classifyStreamListen judges a stream server's listen directive in isolation,
// reusing the exact same token validation translateStreamServer applies so
// the assessment and the generated candidate can never disagree about which
// listen forms are representable.
func classifyStreamListen(params []string) capability {
	_, _, code, message := parseStreamListen(params)
	if code != "" {
		risk := RiskSecurity
		if code == "NGX_STREAM_LISTEN_UNSUPPORTED" {
			risk = RiskAvailability
		}
		return blocking(code, risk, message)
	}
	return capabilityRegistry[capabilityKey{ContextStream, "listen"}]
}

// classifyStreamProxyPass judges a stream proxy_pass target in isolation. Only
// a literal backend (a named upstream or a host:port) is representable; any
// variable-derived target cannot be resolved statically.
func classifyStreamProxyPass(params []string) capability {
	if len(params) == 0 || strings.TrimSpace(params[0]) == "" {
		return blocking("NGX_STREAM_PROXY_PASS", RiskRouting, "proxy_pass target is missing")
	}
	if strings.Contains(params[0], "$") {
		return blocking("NGX_STREAM_PROXY_PASS_DYNAMIC", RiskSecurity, "variable-derived proxy targets are not translated")
	}
	return capabilityRegistry[capabilityKey{ContextStream, "proxy_pass"}]
}

// classifyStreamProxyProtocol judges the standalone outbound `proxy_protocol
// on|off;` directive (ngx_stream_proxy_module). Unlike inbound PROXY protocol,
// this has no trust-boundary concern - Jul is asserting its own peer address
// to its own backend - so it is fully representable for tcp streams. Jul's
// own validation rejects proxy_protocol on udp streams, so that combination
// stays blocking rather than emitting a candidate known to fail validation.
func classifyStreamProxyProtocol(params []string, isUDP bool) capability {
	if len(params) == 0 {
		return blocking("NGX_STREAM_PROXY_PROTOCOL_OUT", RiskSecurity, "proxy_protocol requires an on or off value")
	}
	switch strings.ToLower(strings.TrimSpace(params[0])) {
	case "on":
		if isUDP {
			return blocking("NGX_STREAM_PROXY_PROTOCOL_UDP", RiskSecurity, "outbound PROXY protocol is only supported for tcp stream listeners")
		}
		return capabilityRegistry[capabilityKey{ContextStream, "proxy_protocol"}]
	case "off":
		return ignored("NGX_STREAM_PROXY_PROTOCOL_OUT_OFF", RiskSecurity, "proxy_protocol off matches Jul's default (no outbound header)")
	default:
		return blocking("NGX_STREAM_PROXY_PROTOCOL_OUT", RiskSecurity, "proxy_protocol requires an on or off value")
	}
}

// classifyStreamDuration judges an nginx stream duration directive (bare
// digits meaning seconds, or a Go-compatible duration string).
func classifyStreamDuration(directive, code string, params []string) capability {
	if len(params) == 0 {
		return blocking(code, RiskAvailability, "duration value is missing")
	}
	if _, ok := parseNginxDuration(params[0]); !ok {
		return blocking(code, RiskAvailability, "duration value is not representable (supported units: ms, s, m, h)")
	}
	return capabilityRegistry[capabilityKey{ContextStream, directive}]
}

// classifyStreamSSLPreread judges the `ssl_preread on|off;` directive itself.
// Whether it actually produces bounded SNI routing depends on sibling
// server_name/proxy_pass values across every server sharing the listen
// address, which is a cross-block decision made at translate time (mirroring
// how conflicting client_address policies are resolved after every server on
// an address is known); this directive is fine on its own either way.
func classifyStreamSSLPreread(params []string) capability {
	if len(params) == 0 {
		return blocking("NGX_STREAM_SSL_PREREAD", RiskRouting, "ssl_preread requires an on or off value")
	}
	switch strings.ToLower(strings.TrimSpace(params[0])) {
	case "on", "off":
		return capabilityRegistry[capabilityKey{ContextStream, "ssl_preread"}]
	default:
		return blocking("NGX_STREAM_SSL_PREREAD", RiskRouting, "ssl_preread requires an on or off value")
	}
}

func nestedContext(parent AssessmentContext, name string) (AssessmentContext, bool) {
	switch name {
	case "http":
		return ContextHTTP, true
	case "events":
		return ContextEvents, true
	case "server":
		if parent == ContextStream {
			return ContextStream, true
		}
		if parent == ContextMail {
			return ContextMail, true
		}
		return ContextServer, true
	case "location":
		return ContextLocation, true
	case "upstream":
		return ContextUpstream, true
	case "limit_except":
		return ContextLimitExcept, true
	case "stream":
		return ContextStream, true
	case "mail":
		return ContextMail, true
	case "map", "geo", "split_clients":
		return ContextVariable, true
	default:
		return parent, dHasBlockName(name)
	}
}

// dHasBlockName conservatively recurses into known block-shaped directives.
func dHasBlockName(name string) bool {
	switch name {
	case "if", "types", "geo", "map", "split_clients":
		return true
	default:
		return false
	}
}

func orderedChildren(d ngx.IDirective) []ngx.IDirective {
	if d == nil {
		return nil
	}
	switch typed := d.(type) {
	case *ngx.HTTP:
		out := make([]ngx.IDirective, 0, len(typed.Directives)+len(typed.Servers))
		out = append(out, typed.Directives...)
		for _, s := range typed.Servers {
			out = append(out, s)
		}
		return orderedDirectives(out)
	case *ngx.Upstream:
		out := make([]ngx.IDirective, 0, len(typed.Directives)+len(typed.UpstreamServers))
		out = append(out, typed.Directives...)
		for _, s := range typed.UpstreamServers {
			out = append(out, s)
		}
		return orderedDirectives(out)
	default:
		return orderedDirectives(children(d))
	}
}

func orderedDirectives(in []ngx.IDirective) []ngx.IDirective {
	out := append([]ngx.IDirective(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := out[i].GetLine(), out[j].GetLine()
		if li == lj {
			return false
		}
		if li == 0 {
			return false
		}
		if lj == 0 {
			return true
		}
		return li < lj
	})
	return out
}

func hasStaticCORSConflict(kids []ngx.IDirective) bool {
	star, credentials := false, false
	for _, d := range kids {
		if d.GetName() != "add_header" {
			continue
		}
		params := paramValues(d)
		if len(params) < 3 || params[2] != "always" || strings.Contains(params[1], "$") {
			continue
		}
		name, value := strings.ToLower(params[0]), strings.ToLower(strings.TrimSpace(unquoteHeaderValue(params[1])))
		switch name {
		case "access-control-allow-origin":
			star = value == "*"
		case "access-control-allow-credentials":
			credentials = value == "true"
		}
	}
	return star && credentials
}

func (w *assessmentWalker) addTranslationSynthetic(rep *Report) {
	if rep == nil {
		return
	}
	for _, f := range rep.Skipped {
		switch {
		case f.Name == "real_ip_header" && strings.Contains(f.Reason, "defaulted to X-Real-IP"):
			w.assessment.Results = append(w.assessment.Results, syntheticResult(
				"NGX_REALIP_HEADER_REQUIRED", AssessmentBlocking, AssessmentError, RiskSecurity,
				ContextServer, "real_ip_header", f.Line,
				"trusted proxy sources require an explicit Forwarded or X-Forwarded-For header",
			))
		case f.Name == "set_real_ip_from" && f.Line == 0 && strings.Contains(f.Reason, "different realip policies"):
			w.assessment.Results = append(w.assessment.Results, syntheticResult(
				"NGX_REALIP_LISTENER_CONFLICT", AssessmentBlocking, AssessmentError, RiskSecurity,
				ContextServer, "set_real_ip_from", 0,
				"server blocks sharing a listen address declare incompatible trusted-proxy policies",
			))
		case f.Name == "proxy_cache" && strings.Contains(f.Reason, "cache zone"):
			w.assessment.Results = append(w.assessment.Results, syntheticResult(
				"NGX_CACHE_ZONE_CONFLICT", AssessmentBlocking, AssessmentError, RiskSecurity,
				ContextLocation, "proxy_cache", f.Line,
				f.Reason,
			))
		}
	}
}

func syntheticResult(code string, class AssessmentClass, severity AssessmentSeverity, risk AssessmentRisk, context AssessmentContext, directive string, line int, message string) AssessmentResult {
	return AssessmentResult{Code: code, Class: class, Severity: severity, Risk: risk, Context: context, Directive: directive, Line: line, Message: message, Synthetic: true}
}
