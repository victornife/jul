// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"

	"jul/internal/adminapi"
	"jul/internal/rbac"
)

const v1BodyLimit = int64(1 << 20)

func v1IdempotencyParameter() ExternalParameter {
	return ExternalParameter{
		Name:        "Idempotency-Key",
		In:          "header",
		Type:        "string",
		Required:    false,
		Description: "Optional 8–128 byte [A-Za-z0-9_-] key, scoped to the authenticated principal and current boot. Reusing it for byte-identical input replays the recorded operation; reusing it for different input conflicts.",
	}
}

var v1WriteCatalog = []RouteSpec{
	{
		Pattern:    "/api/v1/config/validate",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigWrite,
		Stability:  StabilityExternal,
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:                  "validateConfig",
			Summary:             "Strictly parse, resolve and validate candidate TOML without persistence or runtime side effects.",
			RequestBody:         "RawTOML",
			RequestContentTypes: []string{"application/toml", "text/plain"},
			MaxBodyBytes:        v1BodyLimit,
			Response:            "ConfigValidationResponse",
			Errors:              []string{"validation_failed", "payload_too_large", "unsupported_media_type"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1ConfigValidate) },
	},
	{
		Pattern:           "/api/v1/config/plan",
		RequiresConfigRaw: true,
		Methods:           []string{http.MethodPost},
		Permission:        rbac.ConfigWrite,
		Stability:         StabilityExternal,
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:                  "planConfig",
			Summary:             "Assess candidate TOML against the authoritative baseline: validation, lint, diff and lifecycle, without any side effect.",
			RequestBody:         "RawTOML",
			RequestContentTypes: []string{"application/toml", "text/plain"},
			MaxBodyBytes:        v1BodyLimit,
			Parameters: []ExternalParameter{{
				Name: "base_version", In: "query", Type: "string",
				Description: "Optional canonical version to pin the assessment. The response always returns the version actually assessed.",
			}},
			Response: "ConfigPlanResponse",
			Errors:   []string{"validation_failed", "stale_base_version", "storage_unavailable", "operation_timeout", "payload_too_large", "unsupported_media_type"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1ConfigPlan) },
	},
	{
		Pattern:    "/api/v1/routes/test",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigWrite,
		Stability:  StabilityExternal,
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:                  "testRoute",
			Summary:             "Dry-run a synthetic request through the canonical router without sending traffic or mutating state.",
			RequestBody:         "RouteTestRequest",
			RequestContentTypes: []string{"application/json"},
			MaxBodyBytes:        v1BodyLimit,
			Response:            "RouteTestResponse",
			Errors:              []string{"storage_unavailable", "payload_too_large", "unsupported_media_type"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1RouteTest) },
	},
	{
		Pattern:           "/api/v1/config/patch",
		RequiresConfigRaw: true,
		Methods:           []string{http.MethodPost},
		Permission:        rbac.ConfigWrite,
		Stability:         StabilityExternal,
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:                  "previewConfigPatch",
			Summary:             "Preview an ordered typed patch batch atomically against one baseline without persistence.",
			RequestBody:         "PatchApplyRequest",
			RequestContentTypes: []string{"application/json"},
			MaxBodyBytes:        v1BodyLimit,
			Response:            "PatchPreviewResponse",
			Errors:              []string{"validation_failed", "operation_failed", "stale_base_version", "not_implemented", "operation_timeout", "payload_too_large", "unsupported_media_type"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1ConfigPatch) },
	},
	{
		Pattern:    "/api/v1/config/apply",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigApply,
		Stability:  StabilityExternal,
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:                  "applyConfig",
			Summary:             "Apply or stage exact candidate TOML through the authoritative managed-apply coordinator.",
			RequestBody:         "RawTOML",
			RequestContentTypes: []string{"application/toml", "text/plain"},
			MaxBodyBytes:        v1BodyLimit,
			Parameters: []ExternalParameter{
				{Name: "base_version", In: "query", Type: "string", Required: true, Description: "Canonical version the candidate was reviewed against."},
				{Name: "mode", In: "query", Type: "string", Default: "hot", Enum: []string{"hot", "stage_restart"}},
				{Name: "confirm_admin", In: "query", Type: "boolean", Default: false, Description: "Explicit confirmation for an admin-reachability-affecting transition."},
				v1IdempotencyParameter(),
			},
			Response:                  "ConfigApplyResponse",
			AdditionalSuccessStatuses: []int{http.StatusAccepted},
			Errors:                    []string{"validation_failed", "stale_base_version", "drift_detected", "config_authority_read_only", "pending_restart_conflict", "restart_required", "admin_reachability_confirmation_required", "idempotency_key_reused", "idempotency_key_in_flight", "operation_timeout", "not_implemented", "payload_too_large", "unsupported_media_type"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1ConfigApply) },
	},
	{
		Pattern:    "/api/v1/config/patch/apply",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigApply,
		Stability:  StabilityExternal,
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:                  "applyConfigPatch",
			Summary:             "Re-execute and atomically apply an ordered typed patch batch against its mandatory base_version.",
			RequestBody:         "PatchApplyRequest",
			RequestContentTypes: []string{"application/json"},
			MaxBodyBytes:        v1BodyLimit,
			Parameters: []ExternalParameter{
				{Name: "mode", In: "query", Type: "string", Default: "hot", Enum: []string{"hot", "stage_restart"}},
				{Name: "confirm_admin", In: "query", Type: "boolean", Default: false},
				v1IdempotencyParameter(),
			},
			Response:                  "ConfigApplyResponse",
			AdditionalSuccessStatuses: []int{http.StatusAccepted},
			Errors:                    []string{"validation_failed", "operation_failed", "stale_base_version", "drift_detected", "config_authority_read_only", "pending_restart_conflict", "restart_required", "admin_reachability_confirmation_required", "idempotency_key_reused", "idempotency_key_in_flight", "operation_timeout", "not_implemented", "payload_too_large", "unsupported_media_type"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1ConfigPatchApply) },
	},
	{
		Pattern:    "/api/v1/config/rollback",
		Methods:    []string{http.MethodPost},
		Permission: rbac.HistoryRollback,
		Stability:  StabilityExternal,
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:                  "rollbackConfig",
			Summary:             "Roll back to a stored history revision through the managed coordinator, bound to the reviewed base_version.",
			RequestBody:         "ConfigRollbackRequest",
			RequestContentTypes: []string{"application/json"},
			MaxBodyBytes:        v1BodyLimit,
			Parameters: []ExternalParameter{
				{Name: "confirm_admin", In: "query", Type: "boolean", Default: false},
				v1IdempotencyParameter(),
			},
			Response:                  "ConfigApplyResponse",
			AdditionalSuccessStatuses: []int{http.StatusAccepted},
			Errors:                    []string{"not_found", "validation_failed", "stale_base_version", "drift_detected", "config_authority_read_only", "pending_restart_conflict", "restart_required", "admin_reachability_confirmation_required", "idempotency_key_reused", "idempotency_key_in_flight", "operation_timeout", "not_implemented", "payload_too_large", "unsupported_media_type"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1Rollback) },
	},
	{
		Pattern:           "/api/v1/config/adopt-external/preview",
		RequiresConfigRaw: true,
		Methods:           []string{http.MethodPost},
		Permission:        rbac.ConfigAdopt,
		Stability:         StabilityExternal,
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:                  "previewAdoptExternal",
			Summary:             "Assess the current external configuration against the managed baseline without changing authority, disk, history or runtime.",
			RequestBody:         "AdoptExternalRequest",
			RequestContentTypes: []string{"application/json"},
			MaxBodyBytes:        v1BodyLimit,
			Response:            "AdoptPreviewResult",
			Errors:              []string{"drift_detected", "not_implemented", "payload_too_large", "unsupported_media_type"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1AdoptPreview) },
	},
	{
		Pattern:    "/api/v1/config/adopt-external",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigAdopt,
		Stability:  StabilityExternal,
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:                        "adoptExternal",
			Summary:                   "Adopt the externally modified file as the managed baseline after digest re-verification and explicit confirmation.",
			RequestBody:               "AdoptExternalRequest",
			RequestContentTypes:       []string{"application/json"},
			MaxBodyBytes:              v1BodyLimit,
			Parameters:                []ExternalParameter{v1IdempotencyParameter()},
			Response:                  "ConfigApplyResponse",
			AdditionalSuccessStatuses: []int{http.StatusAccepted},
			Errors:                    []string{"invalid_request", "validation_failed", "stale_base_version", "drift_detected", "config_authority_read_only", "pending_restart_conflict", "restart_required", "idempotency_key_reused", "idempotency_key_in_flight", "operation_timeout", "not_implemented", "payload_too_large", "unsupported_media_type"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1Adopt) },
	},
	{
		Pattern:    "/api/v1/config/pending-restart/discard",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigApply,
		Stability:  StabilityExternal,
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:      "discardPendingRestart",
			Summary: "Discard the managed staged restart and restore the previous persisted configuration atomically.",
			Parameters: []ExternalParameter{
				{Name: "base_version", In: "query", Type: "string", Required: true},
				v1IdempotencyParameter(),
			},
			Response: "ConfigApplyResponse",
			Errors:   []string{"stale_base_version", "drift_detected", "config_authority_read_only", "pending_restart_conflict", "idempotency_key_reused", "idempotency_key_in_flight", "not_implemented"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1DiscardPendingRestart) },
	},
	{Pattern: "/api/v1/cache/inspect", Methods: []string{http.MethodPost}, Permission: rbac.CacheInspect, Stability: StabilityExternal,
		LimitClasses: map[string]limitKind{http.MethodPost: limitRead},
		Operations:   map[string]ExternalOperation{http.MethodPost: {ID: "inspectCache", Summary: "Read bounded configured-host cache metadata and observed decisions without origin traffic.", RequestBody: "CacheInspectRequest", RequestContentTypes: []string{"application/json"}, MaxBodyBytes: v1BodyLimit, Response: "CacheInspectResponse", Errors: []string{"invalid_request", "storage_unavailable", "payload_too_large", "unsupported_media_type"}}},
		Handler:      func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1CacheInspect) }},
	{
		Pattern:      "/api/v1/cache/purge/preview",
		Methods:      []string{http.MethodPost},
		Permission:   rbac.CachePurge,
		Stability:    StabilityExternal,
		LimitClasses: map[string]limitKind{http.MethodPost: limitRead},
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:                  "previewCachePurge",
			Summary:             "Non-destructively preview a scoped cache invalidation selector (exact URL, prefix, glob, tag, host, key, or all) and mint a principal-and-scope-bound preview_token.",
			RequestBody:         "CachePurgePreviewRequest",
			RequestContentTypes: []string{"application/json"},
			MaxBodyBytes:        v1BodyLimit,
			Response:            "CachePurgePreviewResponse",
			Errors:              []string{"invalid_request", "payload_too_large", "unsupported_media_type"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1CachePurgePreview) },
	},
	{
		Pattern:      "/api/v1/cache/purge",
		Methods:      []string{http.MethodPost},
		Permission:   rbac.CachePurge,
		Stability:    StabilityExternal,
		LimitClasses: map[string]limitKind{http.MethodPost: limitWrite},
		Operations: map[string]ExternalOperation{http.MethodPost: {
			ID:                  "purgeCache",
			Summary:             "Execute a scoped cache invalidation across memory and disk tiers with in-flight fill fencing and durable tombstones. Operational in both managed and file_owned authority modes.",
			RequestBody:         "CachePurgeRequest",
			RequestContentTypes: []string{"application/json"},
			MaxBodyBytes:        v1BodyLimit,
			Parameters:          []ExternalParameter{v1IdempotencyParameter()},
			Response:            "CachePurgeResponse",
			Errors:              []string{"invalid_request", "not_found", "idempotency_key_reused", "idempotency_key_in_flight", "storage_unavailable", "payload_too_large", "unsupported_media_type"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1CachePurge) },
	},
	{
		Pattern:        "/api/v1/cache/purges/{operation_id}",
		Methods:        []string{http.MethodGet},
		AnyPermissions: []rbac.Permission{rbac.CachePurge, rbac.AuditRead, rbac.AdminManage},
		Stability:      StabilityExternal,
		LimitClasses:   map[string]limitKind{http.MethodGet: limitRead},
		Operations: map[string]ExternalOperation{http.MethodGet: {
			ID:                        "getCachePurgeOperation",
			Summary:                   "Retrieve the status and outcome of one cache invalidation operation from the bounded operation ledger.",
			Response:                  "CachePurgeResponse",
			AdditionalSuccessStatuses: []int{http.StatusAccepted},
			Errors:                    []string{"invalid_request", "not_found", "storage_unavailable"},
		}},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleV1CachePurgeGet) },
	},
}

func init() {
	Catalog = append(Catalog, v1WriteCatalog...)

	const clientAddressPattern = "/api/v1/listeners/{addr}/client_address"
	for i := range Catalog {
		spec := &Catalog[i]
		switch spec.Pattern {
		case clientAddressPattern:
			patchOp := ExternalOperation{
				ID:                  "updateListenerClientAddress",
				Summary:             "Atomically update the trusted-proxy/client-address policy for every server block on one listener.",
				RequestBody:         "ListenerClientAddressRequest",
				RequestContentTypes: []string{"application/json"},
				MaxBodyBytes:        v1BodyLimit,
				Parameters: []ExternalParameter{
					{Name: "mode", In: "query", Type: "string", Default: "hot", Enum: []string{"hot", "stage_restart"}},
					v1IdempotencyParameter(),
				},
				Response:                  "ConfigApplyResponse",
				AdditionalSuccessStatuses: []int{http.StatusAccepted},
				Errors:                    []string{"not_found", "validation_failed", "operation_failed", "stale_base_version", "drift_detected", "config_authority_read_only", "pending_restart_conflict", "restart_required", "idempotency_key_reused", "idempotency_key_in_flight", "operation_timeout", "not_implemented", "payload_too_large", "unsupported_media_type"},
			}
			spec.Methods = []string{http.MethodGet, http.MethodPatch}
			spec.Permission = ""
			spec.Permissions = map[string]rbac.Permission{
				http.MethodGet:   rbac.ConfigRead,
				http.MethodPatch: rbac.ConfigTrust,
			}
			if spec.Operations == nil {
				spec.Operations = map[string]ExternalOperation{}
			}
			spec.Operations[http.MethodPatch] = patchOp
			spec.Handler = func(s *Server) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.Method {
					case http.MethodGet:
						s.handleV1ClientAddress(w, r)
					case http.MethodPatch:
						s.handleV1ClientAddressWrite(w, r)
					default:
						w.Header().Set("Allow", "GET, PATCH")
						writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, "this operation accepts GET or PATCH, not %s", r.Method))
					}
				})
			}
		case "/api/v1/config/history":
			op := spec.Operations[http.MethodGet]
			op.Parameters = []ExternalParameter{
				{Name: "limit", In: "query", Type: "integer", Default: 50, Description: "Requested page size. Omitted means 50. Syntactically valid integers below 1 are normalized to 1; values above 200 are normalized to 200. The response reports the effective limit and limit_clamped. No OpenAPI minimum/maximum is imposed because out-of-range integers are valid normalized input."},
				{Name: "cursor", In: "query", Type: "string", Description: "Opaque continuation cursor returned by the previous page. Do not construct or inspect it."},
			}
			spec.Operations[http.MethodGet] = op
		case "/api/v1/config/applies/{apply_id}":
			op := spec.Operations[http.MethodGet]
			op.AdditionalSuccessStatuses = []int{http.StatusAccepted}
			spec.Operations[http.MethodGet] = op
		}
	}
	refreshExternalEndpointList()
}
