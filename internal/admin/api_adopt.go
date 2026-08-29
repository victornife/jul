// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file holds the adopt-external handlers (ADR 0019 §14):
//
//   GET  /api/config/adopt-external/preview — side-effect-free assessment
//   POST /api/config/adopt-external         — the authenticated adoption

import (
	"encoding/json"
	"io"
	"net/http"
)

// handleAdoptExternalPreview assesses the current external configuration
// file against the managed baseline without any side effect: strict decode,
// lint/lifecycle classification, and a diff against the previous managed
// configuration when one exists. It is always allowed to run — including in
// file_owned mode or before any adoption — because it writes nothing; the
// underlying assessment reports why adoption is (or is not) meaningful.
func (s *Server) handleAdoptExternalPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.AdoptExternalPreview == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	result, err := s.deps.AdoptExternalPreview()
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleAdoptExternal performs the authenticated adopt-external operation
// (ADR 0019 §14). It is a mutating endpoint and is denied first in
// file_owned mode, before the request body is even read.
func (s *Server) handleAdoptExternal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.denyIfFileOwned(w, r, string(ApplyOperationAdoptExternal)) {
		return
	}
	if s.deps.AdoptExternal == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req AdoptExternalRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	reqCtx := applyRequestContext(r, ApplyOperationAdoptExternal)
	s.bindManagedApplyDeadline(&reqCtx)

	result, err := s.deps.AdoptExternal(reqCtx, req)
	if err != nil {
		code := configApplyErrorStatus(result, err)
		s.recordAudit(r, string(ApplyOperationAdoptExternal), "config", "failure", "coordinator error: "+err.Error())
		writeJSON(w, code, result)
		return
	}
	status := configApplyResultStatus(result)
	if status != http.StatusOK {
		s.recordAudit(r, string(ApplyOperationAdoptExternal), "config", "failure", "adoption rejected: "+result.Message)
		writeJSON(w, status, result)
		return
	}
	s.recordAudit(r, string(ApplyOperationAdoptExternal), "config", "success", "external configuration adopted (origin="+result.Origin+")")
	s.emit("config", "adopted", "warning", "An external configuration file was adopted as the managed baseline.")
	writeJSON(w, http.StatusOK, result)
}
