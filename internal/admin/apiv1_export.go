// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"strings"

	"jul/internal/adminapi"
	"jul/internal/config"
)

// handleV1ConfigExport serves GET /api/v1/config/export: the whole
// configuration as a redacted structured projection at one revision.
//
// The four collections are captured from a single read, so reading them
// together cannot straddle a reload and produce a document that never existed.
func (s *Server) handleV1ConfigExport(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	state, apiErr := s.v1ConfigState()
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	c := state.Config
	writeAPIJSON(w, http.StatusOK, adminapi.ConfigExportResponse{
		APIVersion:     adminapi.APIVersion,
		BaseVersion:    state.Version,
		Redacted:       true,
		SecretRefCount: config.CountSecretRefs(c),
		Global:         exportGlobal(c),
		Listeners:      v1Listeners(c),
		Routes:         v1Routes(c),
		Upstreams:      s.v1Upstreams(c),
		Streams:        v1Streams(c),
	})
}

// exportGlobal projects the reviewed global scalars. It names each field
// explicitly rather than reflecting the block, so a field added to the schema
// later is absent until someone publishes it.
func exportGlobal(c *config.Config) adminapi.ExportGlobal {
	if c == nil {
		return adminapi.ExportGlobal{}
	}
	g := c.Global
	return adminapi.ExportGlobal{
		LogLevel:            g.LogLevel,
		LogFormat:           g.LogFormat,
		ConfigAuthority:     g.ConfigAuthority,
		ShutdownTimeoutMS:   g.ShutdownTimeout.Std().Milliseconds(),
		ReloadTimeoutMS:     g.ReloadTimeout.Std().Milliseconds(),
		CompressionEnabled:  c.Compression.IsEnabled(),
		CacheEnabled:        c.Cache.Enabled,
		RateLimitEnabled:    c.RateLimit.Enabled,
		WAFEnabled:          c.WAF.Enabled,
		TracingEnabled:      c.Observability.Tracing.Enabled,
		AccessLogConfigured: strings.TrimSpace(g.AccessLog) != "",
		ErrorLogConfigured:  strings.TrimSpace(g.ErrorLog) != "",
	}
}

// handleV1HistoryDiff serves GET /api/v1/config/history/{id}/diff: what
// rolling back to a stored revision would change.
//
// It reads the snapshot server-side and accepts no request body, which is what
// lets a rollback-only principal preview exactly what its rollback would do
// without holding config:write and without submitting candidate TOML. That is
// also why the snapshot body itself never crosses the API: the diff is the
// answer the operator needs, and the body would be a second raw-readback path
// (ADR 0019 §24).
func (s *Server) handleV1HistoryDiff(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	id := r.PathValue("id")
	raw, err := s.hist.get(id)
	if err != nil {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotFound, "No stored configuration revision with that id.").
			WithDetails(adminapi.Details{Kind: "history_revision", ID: id}))
		return
	}
	after, err := config.Parse(raw)
	if err != nil {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInternalError,
			"The stored revision is no longer valid configuration and cannot be compared."))
		return
	}
	state, apiErr := s.v1ConfigState()
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}

	diff := diffConfigs(state.Config, after)
	writeAPIJSON(w, http.StatusOK, adminapi.HistoryDiffResponse{
		APIVersion:    adminapi.APIVersion,
		BaseVersion:   state.Version,
		HistoryID:     id,
		Summary:       diff.Summary,
		Affected:      diff.Affected,
		Warnings:      diff.Warnings,
		Additions:     v1DiffEntries(diff.Additions),
		Removals:      v1DiffEntries(diff.Removals),
		Modifications: v1DiffEntries(diff.Modifications),
	})
}

func v1DiffEntries(in []DiffEntry) []adminapi.DiffEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]adminapi.DiffEntry, 0, len(in))
	for _, e := range in {
		out = append(out, adminapi.DiffEntry{
			Kind:           e.Kind,
			Name:           e.Name,
			Before:         e.Before,
			After:          e.After,
			Detail:         e.Detail,
			LifecycleClass: e.LifecycleClass,
		})
	}
	return out
}
