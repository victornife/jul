// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"jul/internal/upstream"
	"strings"
)

// alpnRoute is an immutable compiled rule. Both the map and slice containing
// rules are owned by one route snapshot, never the caller's mutable config.
type alpnRoute struct {
	serverName string
	protocols  map[string]struct{}
	pool       *upstream.Pool
}

func (r *route) selectPool(hello clientHelloInfo) *upstream.Pool {
	host := strings.ToLower(hello.serverName)
	// Exact-SNI rules always outrank wildcard rules. Within a tier the first
	// configured rule wins, independent of map iteration and client offer order.
	for _, exact := range []bool{true, false} {
		for _, rule := range r.alpnRoutes {
			if exact {
				if host == "" || rule.serverName == "*" || rule.serverName != host {
					continue
				}
			} else if rule.serverName != "*" {
				continue
			}
			for _, offered := range hello.protocols {
				if _, ok := rule.protocols[offered]; ok {
					return rule.pool
				}
			}
		}
	}
	if host != "" && r.sniPools[host] != nil {
		return r.sniPools[host]
	}
	if r.sniPools["*"] != nil {
		return r.sniPools["*"]
	}
	return r.defaultPool
}
