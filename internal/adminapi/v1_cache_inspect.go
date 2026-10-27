// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

import "jul/internal/cachemeta"

// CacheInspectRequest selects configured cache metadata without origin traffic.
// Subsequent pages repeat the exact normalized scope; cursors grant no access.
type CacheInspectRequest struct {
	Selector CacheSelector `json:"selector"`
	Cursor   string        `json:"cursor,omitempty"`
	PageSize int           `json:"page_size,omitempty"`
}

// CacheInspectResponse is a detached, bounded observation. Metadata timestamps
// and reasons do not guarantee future reuse or explain an absent history.
type CacheInspectResponse struct {
	ConcurrentChange string               `json:"concurrent_change"`
	APIVersion       string               `json:"api_version"`
	Selector         CacheSelector        `json:"selector"`
	Inspection       cachemeta.Inspection `json:"inspection"`
	NextCursor       string               `json:"next_cursor,omitempty"`
	ExpiresAt        string               `json:"expires_at"`
	Consistency      string               `json:"consistency"`
	PolicySource     string               `json:"policy_source"`
	RouteCacheState  string               `json:"route_cache_state"`
	AuthorityState
}
