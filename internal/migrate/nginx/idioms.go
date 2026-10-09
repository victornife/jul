// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"fmt"
	ngx "github.com/tufanbarisyildirim/gonginx/config"
	"jul/internal/config"
	"math"
	"strconv"
	"strings"
	"time"
)

// idiomScope resolves sibling directives before locations, independent of AST order.
type idiomScope struct {
	expires *config.Duration
	mime    *config.MIMEConfig
}

func (t *translator) resolveIdioms(kids []ngx.IDirective, inherited idiomScope) idiomScope {
	scope := idiomScope{expires: inherited.expires, mime: config.ResolveMIME(inherited.mime)}
	var localTypes map[string]string
	for _, d := range kids {
		switch d.GetName() {
		case "expires":
			value, ok := parseExpires(paramValues(d))
			scope.expires = value // off and unrepresentable forms clear an inherited value
			if !ok {
				t.report.skip(d, "expires requires off or a signed whole-second duration; modified, @time and variables are not translated")
			}
		case "types":
			table, err := parseMIMETypes(d)
			if err != nil {
				t.report.skip(d, err.Error())
				continue
			}
			if localTypes == nil {
				localTypes = map[string]string{}
			}
			for ext, typ := range table {
				localTypes[ext] = typ // NGINX's last declaration wins.
			}
			scope.mime = nginxMIME(scope.mime)
			scope.mime.Types = &localTypes
		case "default_type":
			params := paramValues(d)
			if len(params) != 1 || !validMediaType(unquoteHeaderValue(params[0])) {
				t.report.skip(d, "default_type requires one valid static media type")
				continue
			}
			scope.mime = nginxMIME(scope.mime)
			scope.mime.DefaultType = unquoteHeaderValue(params[0])
		}
	}
	return scope
}

// NGINX's core default extension table is html, gif and jpeg; an explicit
// default_type must not accidentally retain Jul's host database or sniffing.
func nginxMIME(policy *config.MIMEConfig) *config.MIMEConfig {
	out := config.ResolveMIME(policy)
	if out == nil {
		out = &config.MIMEConfig{}
	}
	if out.Types == nil {
		table := map[string]string{".html": "text/html", ".gif": "image/gif", ".jpg": "image/jpeg"}
		out.Types = &table
	}
	if out.DefaultType == "" {
		out.DefaultType = "text/plain"
	}
	return out
}

func parseExpires(params []string) (*config.Duration, bool) {
	if len(params) != 1 {
		return nil, false
	}
	raw := unquoteHeaderValue(params[0])
	if raw == "off" {
		return nil, true
	}
	negative := strings.HasPrefix(raw, "-")
	if negative || strings.HasPrefix(raw, "+") {
		raw = raw[1:]
	}
	if raw == "" {
		return nil, false
	}
	// Match NGINX's seconds grammar: descending units, optional spaces after
	// components, and a final bare seconds value. Bound it to Go's duration.
	multipliers := map[byte]int64{'y': 365 * 86400, 'M': 30 * 86400, 'w': 7 * 86400, 'd': 86400, 'h': 3600, 'm': 60, 's': 1}
	ranks := map[byte]int{'y': 1, 'M': 2, 'w': 3, 'd': 4, 'h': 5, 'm': 6, 's': 7}
	var total int64
	previous, valid := 0, false
	for len(raw) > 0 {
		n := 0
		for n < len(raw) && raw[n] >= '0' && raw[n] <= '9' {
			n++
		}
		var value int64
		if n > 0 {
			var err error
			value, err = strconv.ParseInt(raw[:n], 10, 64)
			if err != nil {
				return nil, false
			}
			valid = true
		}
		raw = raw[n:]
		multiplier := int64(1)
		if len(raw) > 0 {
			unit := raw[0]
			raw = raw[1:]
			if unit == 'm' && strings.HasPrefix(raw, "s") {
				return nil, false // NGINX expires uses seconds, never milliseconds.
			}
			if unit == ' ' {
				if previous >= 7 {
					return nil, false
				}
				previous = 9
			} else {
				var ok bool
				multiplier, ok = multipliers[unit]
				if !ok || ranks[unit] <= previous {
					return nil, false
				}
				previous = ranks[unit]
			}
			raw = strings.TrimLeft(raw, " ")
		}
		if value > math.MaxInt64/int64(time.Second)/multiplier {
			return nil, false
		}
		seconds := value * multiplier
		if seconds > math.MaxInt64/int64(time.Second)-total {
			return nil, false
		}
		total += seconds
	}
	if !valid {
		return nil, false
	}
	if negative {
		total = -total
	}
	duration := config.Duration(time.Duration(total) * time.Second)
	return &duration, true
}

func parseMIMETypes(d ngx.IDirective) (map[string]string, error) {
	table := map[string]string{}
	if len(paramValues(d)) != 0 {
		return nil, fmt.Errorf("types requires a static extension block")
	}
	for _, entry := range children(d) {
		if entry.GetName() == "include" {
			// Resolved children are inserted beside the include by the bounded
			// resolver. Keep an unresolved include blocking, regardless of name.
			if include, ok := entry.(*ngx.Include); ok && len(include.Configs) > 0 {
				continue
			}
			return nil, fmt.Errorf("types include must be resolved with --follow-includes")
		}
		typ := unquoteHeaderValue(entry.GetName())
		exts := paramValues(entry)
		if len(exts) == 0 || !validMediaType(typ) {
			return nil, fmt.Errorf("types requires valid media types followed by extensions")
		}
		for _, ext := range exts {
			if ext == "" || len(ext) > 63 {
				return nil, fmt.Errorf("types extension is empty or too long")
			}
			ext = strings.ToLower(unquoteHeaderValue(ext))
			for _, c := range ext {
				if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' && c != '+' {
					return nil, fmt.Errorf("types extension %q is not representable", ext)
				}
			}
			ext = "." + ext
			if _, exists := table[ext]; !exists && len(table) >= 4096 {
				return nil, fmt.Errorf("types supports at most 4096 extensions")
			}
			table[ext] = typ
		}
	}
	return table, nil
}

func validMediaType(value string) bool { return config.ValidMIMEType(value) }

func exactWebSocket(kids []ngx.IDirective) bool {
	protocol, upgrade, connection, proxy := 0, 0, 0, 0
	for _, d := range kids {
		p := paramValues(d)
		switch d.GetName() {
		case "proxy_pass":
			if len(p) == 1 && !strings.Contains(p[0], "$") && !isDirectUnixProxyPass(p[0]) {
				proxy++
			}
		case "proxy_http_version":
			if len(p) == 1 && unquoteHeaderValue(p[0]) == "1.1" {
				protocol++
			} else {
				return false
			}
		case "proxy_set_header":
			if len(p) != 2 {
				return false
			}
			switch strings.ToLower(p[0]) {
			case "upgrade":
				if unquoteHeaderValue(p[1]) != "$http_upgrade" {
					return false
				}
				upgrade++
			case "connection":
				if !strings.EqualFold(unquoteHeaderValue(p[1]), "upgrade") {
					return false
				}
				connection++
			}
		}
	}
	return protocol == 1 && upgrade == 1 && connection == 1 && proxy == 1
}

func classifyIdiom(context AssessmentContext, d ngx.IDirective, facts walkFacts) (capability, bool) {
	name, p := d.GetName(), paramValues(d)
	if context == ContextHTTP || context == ContextServer || context == ContextLocation {
		prefix := "NGX_" + strings.ToUpper(string(context)) + "_"
		target := "mime"
		if context == ContextServer {
			target = "servers[].mime"
		}
		if context == ContextLocation {
			target = "servers[].locations[].mime"
		}
		switch name {
		case "expires":
			if _, ok := parseExpires(p); !ok {
				return blocking(prefix+"EXPIRES", RiskOperational, "only off and signed plain durations are translated; modified, @time, variables and special epoch/max forms remain blocking"), true
			}
			return supported(prefix+"EXPIRES", RiskOperational, "response-time expiration, inherited into locations; negative durations emit no-cache", []string{"servers[].locations[].expires"}), true
		case "types":
			if _, err := parseMIMETypes(d); err != nil {
				return blocking(prefix+"TYPES", RiskOperational, err.Error()), true
			}
			return supported(prefix+"TYPES", RiskOperational, "static extension table replaces inherited mappings, including an empty block", []string{target + ".types.*", target + ".default_type"}), true
		case "default_type":
			if len(p) != 1 || !validMediaType(unquoteHeaderValue(p[0])) {
				return blocking(prefix+"DEFAULT_TYPE", RiskOperational, "one static valid media type is required"), true
			}
			return supported(prefix+"DEFAULT_TYPE", RiskOperational, "unmapped-file media type is translated", []string{target + ".default_type", target + ".types.*"}), true
		}
	}
	if context == ContextServer && name == "client_max_body_size" {
		var size config.Size
		if len(p) != 1 || size.UnmarshalText([]byte(p[0])) != nil || size <= 0 {
			return blocking("NGX_SERVER_BODY_LIMIT", RiskSecurity, "only positive body limits translate; NGINX zero/unlimited has no target zero equivalent"), true
		}
		return supported("NGX_SERVER_BODY_LIMIT", RiskSecurity, "positive request body limit is translated", []string{"servers[].client_max_body_size"}), true
	}
	if context == ContextHTTP && name == "gzip_types" {
		if len(p) == 0 {
			return blocking("NGX_HTTP_GZIP_TYPES", RiskPerformance, "gzip_types requires media types"), true
		}
		for _, typ := range p {
			if typ != "*" && (!validMediaType(typ) || strings.HasSuffix(typ, "/*")) {
				return blocking("NGX_HTTP_GZIP_TYPES", RiskPerformance, "only exact media types and * are representable; MIME-family patterns are not NGINX wildcards"), true
			}
		}
		return supported("NGX_HTTP_GZIP_TYPES", RiskPerformance, "compression media types include NGINX's implicit text/html", []string{"compression.types"}), true
	}
	if context == ContextLocation {
		if name == "proxy_http_version" || name == "proxy_set_header" && len(p) > 0 && (strings.EqualFold(p[0], "Upgrade") || strings.EqualFold(p[0], "Connection")) {
			if facts.webSocket {
				return informational("NGX_LOCATION_WEBSOCKET", RiskRouting, "the exact HTTP/1.1 Upgrade/Connection trio uses native WebSocket proxy handling"), true
			}
			return blocking("NGX_LOCATION_WEBSOCKET", RiskRouting, "only the complete static WebSocket trio with a usable proxy_pass is recognized"), true
		}
		if name == "proxy_buffering" {
			if len(p) == 1 && p[0] == "off" {
				return supported("NGX_LOCATION_PROXY_BUFFERING", RiskPerformance, "disable buffering and flush each proxy write immediately", []string{"servers[].locations[].proxy_buffering"}), true
			}
			return blocking("NGX_LOCATION_PROXY_BUFFERING", RiskPerformance, "only proxy_buffering off is translated"), true
		}
	}
	return capability{}, false
}

// appendGZIPTypes mirrors NGINX's sibling accumulation and all-types sentinel.
func appendGZIPTypes(current, additions []string) []string {
	if len(current) == 1 && current[0] == "*" {
		return current
	}
	if current == nil {
		current = []string{"text/html"}
	}
	seen := make(map[string]bool, len(current))
	for _, typ := range current {
		seen[typ] = true
	}
	for _, typ := range additions {
		if typ == "*" {
			return []string{"*"}
		}
		typ = strings.ToLower(typ)
		if !seen[typ] {
			current = append(current, typ)
			seen[typ] = true
		}
	}
	return current
}
