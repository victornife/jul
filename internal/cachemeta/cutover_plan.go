// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cachemeta

// CutoverPlan is a bounded description of the server-owned cache preview.
type CutoverPlan struct {
	DefaultPolicy        string   `json:"default_policy"`
	InvalidatedScopes    []string `json:"invalidated_scopes,omitempty"`
	RetainedScopes       []string `json:"retained_scopes,omitempty"`
	BroadenedToHostOrAll bool     `json:"broadened_to_host_or_all,omitempty"`
	Disposition          string   `json:"disposition"`
}
