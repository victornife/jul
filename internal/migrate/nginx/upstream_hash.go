// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"fmt"
	"strings"

	"jul/internal/config"
)

// nginxHashKey is an NGINX `hash` key expression translated onto one of Jul's
// closed affinity key sources (ADR 0021), or the reason it cannot be.
type nginxHashKey struct {
	key, name  string
	consistent bool
	reason     string
}

// parseNginxHashKey maps the parameters of an NGINX `hash` directive. Only a
// single bare variable naming the client address, one request header or one
// cookie is representable: anything else — a literal, a concatenation, a URI
// or argument variable — would need an expression language Jul deliberately
// does not have.
func parseNginxHashKey(params []string) nginxHashKey {
	var out nginxHashKey
	if len(params) == 0 {
		out.reason = "hash requires a key"
		return out
	}
	if len(params) > 2 || (len(params) == 2 && params[1] != "consistent") {
		out.reason = fmt.Sprintf("unsupported hash parameters %q", strings.Join(params, " "))
		return out
	}
	out.consistent = len(params) == 2
	expr := params[0]
	switch {
	case expr == "$remote_addr" || expr == "$binary_remote_addr":
		out.key = config.HashKeyClientIP
	case strings.HasPrefix(expr, "$http_") && nginxVariableSuffix(expr[len("$http_"):]):
		// $http_x_tenant reads header X-Tenant: NGINX lowercases the name and
		// turns dashes into underscores, so the reverse is the header name.
		out.key, out.name = config.HashKeyHeader, strings.ReplaceAll(expr[len("$http_"):], "_", "-")
	case strings.HasPrefix(expr, "$cookie_") && nginxVariableSuffix(expr[len("$cookie_"):]):
		out.key, out.name = config.HashKeyCookie, expr[len("$cookie_"):]
	default:
		out.reason = fmt.Sprintf("hash key %q is not a single client-address, request-header or cookie variable", expr)
	}
	return out
}

// nginxVariableSuffix reports whether s is a plain variable-name suffix with no
// further interpolation, so "$http_x_tenant" qualifies and "$http_x${uri}" does
// not.
func nginxVariableSuffix(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// classifyUpstreamHash grades an NGINX `hash` directive. A representable key is
// approximated, never supported: the key source matches but the placement
// function does not. Keyless requests agree: NGINX round-robins an empty key,
// which is Jul's default hash.fallback.
func classifyUpstreamHash(params []string) capability {
	k := parseNginxHashKey(params)
	if k.reason != "" {
		return blockingWithTargets("NGX_UPSTREAM_HASH_KEY", RiskAvailability,
			k.reason+"; Jul's consistent_hash accepts only client_ip, one header or one cookie, so request affinity cannot be preserved automatically",
			[]string{"upstreams[].strategy", "upstreams[].hash.key"})
	}
	source := k.key
	if k.name != "" {
		source += " " + k.name
	}
	ring := "modular hashing, which remaps most keys when membership changes"
	if k.consistent {
		ring = "a ketama ring"
	}
	return approximatedWithTargets("NGX_UPSTREAM_HASH", RiskAvailability,
		fmt.Sprintf("hash maps to consistent_hash on %s; Jul uses rendezvous hashing where NGINX uses %s, so keys are re-placed once at cutover; requests without the key are round-robined by both", source, ring),
		[]string{"upstreams[].strategy", "upstreams[].hash.key"})
}

func approximatedWithTargets(code string, risk AssessmentRisk, message string, targets []string) capability {
	c := approximated(code, risk, message)
	c.targetPaths = targets
	return c
}

func blockingWithTargets(code string, risk AssessmentRisk, message string, targets []string) capability {
	c := blocking(code, risk, message)
	c.targetPaths = targets
	return c
}
