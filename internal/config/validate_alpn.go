// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// StreamALPNMaxRules bounds configured per-connection rule evaluation.
const StreamALPNMaxRules = 256

// StreamALPNMaxProtocols bounds both a rule's identifiers and inspected offers.
const StreamALPNMaxProtocols = 64

// StreamALPNMaxProtocolLen is RFC 7301's one-byte identifier length limit.
const StreamALPNMaxProtocolLen = 255

// ValidateStreamALPNRoutes checks bounded rule structure. Target resolution is
// performed by the normal stream validator and pool builder, not this helper.
// It is also used by preflight/runtime callers constructing typed configs.
func ValidateStreamALPNRoutes(routes []StreamALPNRoute) error {
	if len(routes) > StreamALPNMaxRules {
		return fmt.Errorf("alpn_routes: at most %d rules are supported", StreamALPNMaxRules)
	}
	var errs []error
	for i, route := range routes {
		where := fmt.Sprintf("alpn_routes[%d]", i)
		host := route.ServerName
		invalidHost := host == "" || len(host) > 255 || host != strings.TrimSpace(host)
		if host != "*" {
			for _, ch := range host {
				if ch <= 32 || ch >= 127 || ch == '*' {
					invalidHost = true
				}
			}
		}
		if invalidHost {
			errs = append(errs, fmt.Errorf("%s.server_name: an exact ASCII SNI name or * is required (maximum 255 bytes)", where))
		}
		if len(route.Protocols) == 0 || len(route.Protocols) > StreamALPNMaxProtocols {
			errs = append(errs, fmt.Errorf("%s.protocols: require 1–%d identifiers", where, StreamALPNMaxProtocols))
			continue
		}
		seen := make(map[string]bool, len(route.Protocols))
		for j, protocol := range route.Protocols {
			if len(protocol) == 0 || len(protocol) > StreamALPNMaxProtocolLen || !utf8.ValidString(protocol) {
				errs = append(errs, fmt.Errorf("%s.protocols[%d]: require a UTF-8 string of 1–255 bytes", where, j))
			}
			if seen[protocol] {
				errs = append(errs, fmt.Errorf("%s.protocols[%d]: duplicate identifier", where, j))
			}
			seen[protocol] = true
		}
		if strings.TrimSpace(route.ProxyPass) == "" {
			errs = append(errs, fmt.Errorf("%s.proxy_pass: a target is required", where))
		}
	}
	return errors.Join(errs...)
}
