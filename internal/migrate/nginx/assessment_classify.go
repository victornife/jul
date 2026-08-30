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
	if childContext == ContextLocation {
		locationFacts.corsConflict = hasStaticCORSConflict(kids)
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
		return classifyRealIP(name, params)
	}
	if cap, ok := capabilityRegistry[capabilityKey{context: context, name: name}]; ok {
		switch {
		case context == ContextServer && name == "listen":
			return classifyListen(params, facts.extraListen)
		case context == ContextServer && name == "ssl_protocols":
			return classifyTLSProtocols(params)
		case context == ContextLocation && name == "proxy_pass":
			return classifyProxyPass(params)
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
			return classifyUpstreamServer(params)
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

func classifyRealIP(name string, params []string) capability {
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

func classifyListen(params []string, extra bool) capability {
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

func classifyReturn(params []string, serverLevel bool) capability {
	if len(params) == 0 {
		return blocking("NGX_RETURN_MALFORMED", RiskRouting, "return directive has no status or target")
	}
	if serverLevel {
		return capabilityRegistry[capabilityKey{ContextServer, "return"}]
	}
	code, err := strconv.Atoi(params[0])
	if err == nil && len(params) > 1 && (code < 300 || code >= 400) {
		return approximated("NGX_LOCATION_RETURN_BODY", RiskRouting, "non-redirect response body is dropped")
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

func classifyUpstreamServer(params []string) capability {
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
		default:
			return blocking("NGX_UPSTREAM_SERVER_OPTION", RiskAvailability, "upstream server option is not translated")
		}
	}
	return capabilityRegistry[capabilityKey{ContextUpstream, "server"}]
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
		}
	}
}

func syntheticResult(code string, class AssessmentClass, severity AssessmentSeverity, risk AssessmentRisk, context AssessmentContext, directive string, line int, message string) AssessmentResult {
	return AssessmentResult{Code: code, Class: class, Severity: severity, Risk: risk, Context: context, Directive: directive, Line: line, Message: message, Synthetic: true}
}
