// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"sort"
	"strings"

	"jul/internal/adminapi"
	"jul/internal/config"
)

// patchHash validates a strategy/hash pair from a patch and returns the
// [upstreams.hash] block to store. consistent_hash requires an explicit key;
// every other strategy clears the block, and sending one with them is an error
// rather than a silently discarded field. Field-level rules (token names,
// duplicate backends, stream applicability) are config.Validate's, which every
// patch candidate passes through.
func patchHash(op, strategy string, h *upstreamHash) (*config.HashConfig, error) {
	switch strategy {
	case "", "round_robin", "weighted_round_robin", "least_conn":
		if h != nil {
			return nil, fmt.Errorf("%s: hash applies only to strategy consistent_hash", op)
		}
		return nil, nil
	case "consistent_hash":
		if h == nil || strings.TrimSpace(h.Key) == "" {
			return nil, fmt.Errorf("%s: strategy consistent_hash requires hash.key (client_ip, header or cookie)", op)
		}
		return &config.HashConfig{
			Key:      strings.TrimSpace(h.Key),
			Name:     strings.TrimSpace(h.Name),
			Fallback: strings.TrimSpace(h.Fallback),
		}, nil
	default:
		return nil, fmt.Errorf("%s: invalid strategy %q (want round_robin|weighted_round_robin|least_conn|consistent_hash)", op, strategy)
	}
}

// strategySummary names a strategy for a patch summary, including the key
// source for consistent_hash. It names the header or cookie, never a value.
func strategySummary(strategy string, h *config.HashConfig) string {
	if strategy == "consistent_hash" && h != nil {
		return "consistent_hash (" + hashSummary(h) + ")"
	}
	return orDefault(strategy, "round_robin")
}

// hashSummary is a stable one-line description of a hash block for diffs.
func hashSummary(h *config.HashConfig) string {
	if h == nil {
		return ""
	}
	key := h.Key
	if h.Name != "" {
		key += ":" + h.Name
	}
	return fmt.Sprintf("key=%s fallback=%s", key, orDefault(h.Fallback, "round_robin"))
}

// affinityDetail is the Status row detail: pool count and key kinds, never a
// header or cookie name's value.
func affinityDetail(pools int, kinds map[string]bool) string {
	if pools == 0 {
		return ""
	}
	ks := make([]string, 0, len(kinds))
	for k := range kinds {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return countUnit(pools, "pool") + " (key: " + strings.Join(ks, ", ") + ")"
}

// upstreamHashView projects an upstream's effective hash policy, or nil when
// it does not hash.
func upstreamHashView(up *config.UpstreamConfig) *adminapi.UpstreamHash {
	if up.Strategy != "consistent_hash" || up.Hash == nil {
		return nil
	}
	applies := "http_and_stream"
	if up.Hash.Key != config.HashKeyClientIP {
		applies = "http"
	}
	return &adminapi.UpstreamHash{
		Key:       up.Hash.Key,
		Name:      up.Hash.Name,
		Fallback:  orDefault(up.Hash.Fallback, "round_robin"),
		Algorithm: orDefault(up.Hash.Algorithm, config.HashAlgorithmRendezvousV1),
		AppliesTo: applies,
	}
}
