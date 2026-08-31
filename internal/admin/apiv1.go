// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"jul/internal/adminapi"
)

// externalContractKey marks a request as belonging to the external /api/v1
// contract and carries the request id minted for it.
type externalContractKey struct{}

// externalContract reports whether this request is being served under the
// external contract, and the request id minted for it.
//
// It exists so the shared authentication and authorization middleware can
// render its failures as the §26 envelope on an external route and keep its
// existing shape on an internal one — **without forking the middleware**.
// ADR 0019 §24 is explicit that a permission or authority check present on one
// path and absent on the other is exactly the drift this contract exists to
// prevent, so there is one implementation of the check and two renderings of
// its refusal, selected by the route's classification.
func externalContract(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(externalContractKey{}).(string)
	return id, ok
}

// withExternalContract mints the request id and marks the request. It is
// applied by routes() to every route classified external, outside the
// authentication middleware, so a refusal that happens before authentication
// still carries a correlation id.
func (s *Server) withExternalContract(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := adminapi.NewRequestID()
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), externalContractKey{}, id)))
	})
}

// writeAPIError renders an adminapi.Error as the §26 envelope, using the
// request id already minted for this request. It is the only way an external
// route reports a failure.
func writeAPIError(w http.ResponseWriter, r *http.Request, e *adminapi.Error) {
	id, ok := externalContract(r.Context())
	if !ok {
		// A caller reached this without the marker, which is a wiring mistake
		// rather than a client error. Mint one so the response is still
		// correlatable instead of carrying an empty field.
		id = adminapi.NewRequestID()
		w.Header().Set(requestIDHeader, id)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.Status())
	_ = json.NewEncoder(w).Encode(adminapi.Envelope{Error: adminapi.Body{
		Code:      e.Code,
		Message:   e.Message,
		Details:   e.Details,
		RequestID: id,
	}})
}

// writeAPIJSON renders a successful external response.
func writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// requireExternalMethod enforces the accepted method on an external route. A
// mismatch is invalid_request rather than a bare 405 body, because every
// /api/v1 failure carries the envelope.
func requireExternalMethod(w http.ResponseWriter, r *http.Request, allow string) bool {
	if r.Method == allow {
		return true
	}
	w.Header().Set("Allow", allow)
	writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest,
		"this operation accepts %s, not %s", allow, r.Method))
	return false
}
