// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"strings"

	"golang.org/x/net/http/httpguts"

	"jul/internal/affinity"
)

// hashNameMaxLen bounds a configured header or cookie name. Real names are
// short tokens; the bound keeps a pasted value from masquerading as a name.
const hashNameMaxLen = 128

// validateHash checks strategy = "consistent_hash" against its [upstreams.hash]
// block. The key source must be explicit: a default key would be a silent
// choice about which clients share a backend.
func validateHash(up UpstreamConfig, where string) []error {
	h := up.Hash
	if up.Strategy != "consistent_hash" {
		if h != nil {
			return []error{fmt.Errorf("%s.hash: applies only to strategy = \"consistent_hash\" (strategy is %q)", where, strategyOrDefault(up.Strategy))}
		}
		return nil
	}
	if h == nil {
		return []error{fmt.Errorf("%s: strategy \"consistent_hash\" requires an [upstreams.hash] block with a key (client_ip, header or cookie)", where)}
	}
	where += ".hash"
	var errs []error
	switch h.Key {
	case HashKeyClientIP:
		if h.Name != "" {
			errs = append(errs, fmt.Errorf("%s.name: must not be set for key \"client_ip\"", where))
		}
	case HashKeyHeader, HashKeyCookie:
		switch {
		case h.Name == "":
			errs = append(errs, fmt.Errorf("%s.name: required for key %q", where, h.Key))
		case len(h.Name) > hashNameMaxLen:
			errs = append(errs, fmt.Errorf("%s.name: longer than %d bytes", where, hashNameMaxLen))
		case !httpguts.ValidHeaderFieldName(h.Name):
			errs = append(errs, fmt.Errorf("%s.name: %q is not a valid %s name (an RFC 9110 token)", where, h.Name, h.Key))
		case h.Key == HashKeyHeader && strings.EqualFold(h.Name, "Cookie"):
			errs = append(errs, fmt.Errorf("%s.name: use key = \"cookie\" with the cookie's name instead of hashing the whole Cookie header", where))
		}
	case "":
		errs = append(errs, fmt.Errorf("%s.key: required (client_ip, header or cookie)", where))
	default:
		errs = append(errs, fmt.Errorf("%s.key: invalid key %q (want client_ip|header|cookie)", where, h.Key))
	}
	switch h.Fallback {
	case "", "round_robin", "weighted_round_robin", "least_conn":
	default:
		errs = append(errs, fmt.Errorf("%s.fallback: invalid fallback %q (want round_robin|weighted_round_robin|least_conn)", where, h.Fallback))
	}
	switch h.Algorithm {
	case "", HashAlgorithmRendezvousV1:
	default:
		errs = append(errs, fmt.Errorf("%s.algorithm: unsupported algorithm %q (want %s)", where, h.Algorithm, HashAlgorithmRendezvousV1))
	}
	errs = append(errs, validateHashIdentities(up, where)...)
	return errs
}

// validateHashIdentities rejects two static servers with one canonical
// identity. They would score identically for every key, so which of them held
// a key would depend on list order — exactly what the mapping must not do.
func validateHashIdentities(up UpstreamConfig, where string) []error {
	var errs []error
	seen := make(map[string]int, len(up.Servers))
	for i, s := range up.Servers {
		network, address := "tcp", s.Address
		switch {
		case strings.HasPrefix(address, "unix:"):
			network, address = "unix", strings.TrimPrefix(address, "unix:")
		case strings.HasPrefix(address, "tcp://"):
			address = strings.TrimPrefix(address, "tcp://")
		}
		id := affinity.Identity(network, address)
		if j, dup := seen[id]; dup {
			errs = append(errs, fmt.Errorf("%s: servers[%d] and servers[%d] are the same backend (%s); consistent_hash needs distinct backends", strings.TrimSuffix(where, ".hash"), j, i, id))
			continue
		}
		seen[id] = i
	}
	return errs
}

func strategyOrDefault(s string) string {
	if s == "" {
		return "round_robin"
	}
	return s
}

// validateStreamHash rejects a stream route that names a consistent_hash
// upstream keyed on a header or cookie. Raw L4 has neither, so such a route
// would silently place every connection by the fallback strategy.
func validateStreamHash(c *Config) []error {
	httpOnly := map[string]string{}
	for _, up := range c.Upstreams {
		if up.Strategy == "consistent_hash" && up.Hash != nil && (up.Hash.Key == HashKeyHeader || up.Hash.Key == HashKeyCookie) {
			httpOnly[up.Name] = up.Hash.Key
		}
	}
	if len(httpOnly) == 0 {
		return nil
	}
	var errs []error
	for i, st := range c.Streams {
		for _, target := range streamTargets(st) {
			if kind, ok := httpOnly[target]; ok {
				errs = append(errs, fmt.Errorf("stream[%d]: upstream %q is keyed on a %s, which a stream route cannot read; use hash.key = \"client_ip\" or a separate upstream for L4", i, target, kind))
			}
		}
	}
	return errs
}
