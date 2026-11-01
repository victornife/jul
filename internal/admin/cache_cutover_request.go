// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"jul/internal/adminapi"
	"jul/internal/config"
)

// bindCacheCutoverRequest uses one canonical request option for preview,
// persistence admission and publication. TOML requests use a query field;
// structured requests can use cache_cutover in their JSON body.
func bindCacheCutoverRequest(r *http.Request, bodyMode, applyMode string) error {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return fmt.Errorf("cache_cutover query parameters are malformed")
	}
	values := query["cache_cutover"]
	if len(values) > 1 {
		return fmt.Errorf("cache_cutover must be specified once")
	}
	mode := query.Get("cache_cutover")
	if bodyMode != "" {
		if mode != "" && mode != bodyMode {
			return fmt.Errorf("conflicting cache_cutover body and query options")
		}
		mode = bodyMode
	}
	if err := config.ValidateCacheCutoverMode(mode, applyMode); err != nil {
		return err
	}
	*r = *r.WithContext(config.WithCacheCutoverMode(r.Context(), mode))
	return nil
}

func prepareCacheCutoverRequest(w http.ResponseWriter, r *http.Request, bodyMode, applyMode string) bool {
	if err := bindCacheCutoverRequest(r, bodyMode, applyMode); err != nil {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, "%s", err.Error()).WithDetails(adminapi.Details{Field: "cache_cutover"}))
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return false
	}
	return true
}

// Older embedding hooks only write raw configuration and cannot guarantee the
// accepted publication option. Refuse it before any write instead of dropping it.
func requireCacheCutoverSupport(w http.ResponseWriter, r *http.Request, available bool) bool {
	if available || config.CacheCutoverMode(r.Context()) == "" {
		return true
	}
	const message = "cache_cutover requires the correlated configuration apply coordinator"
	if strings.HasPrefix(r.URL.Path, "/api/v1/") {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotImplemented, message))
	} else {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": message})
	}
	return false
}
