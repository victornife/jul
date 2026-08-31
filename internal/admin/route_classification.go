// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file is the route classification inventory required by ADR 0019 §24.
//
// Being in the admin route catalog does not make a route public. Every route
// whose Stability is StabilityInternal — which is the zero value, so it is
// every route nobody has classified — must appear here with the reason it is
// not part of the supported external contract. The guard test holds this map
// and the catalog to *exact-set equality* in both directions, so:
//
//   - adding a route without classifying it fails the build with a message
//     asking for the reason, and
//   - promoting a route to external without removing its reason fails too.
//
// A reason is a sentence a reviewer can disagree with, not a label. "Console
// dashboard shape" is a reason; "internal" is not.
var internalRouteReasons = map[string]string{
	// ── Console shell and plumbing ────────────────────────────────────────
	"/": "The Console SPA shell. An HTML document served to a browser, not a machine contract.",
	"/api/admin/me": "Console plumbing: the caller's own server-derived identity, shaped for the " +
		"Console's permission-gating. External clients discover their effective permissions through " +
		"the errors the operations themselves return, and through GET /api/v1/capabilities.",
	"/api/admin/health":        "Console plumbing: self-reported health of the Console's own API calls, for the Console's status widget.",
	"/api/admin/client-errors": "Console plumbing: a browser error sink. It accepts client-authored text and exists for the Console alone.",

	// ── Console dashboard shapes ──────────────────────────────────────────
	"/api/stats":            "A Console dashboard shape. Its fields change with the UI that renders them.",
	"/api/apps":             "A Console dashboard shape: the app-centric projection the Console's Apps screen renders.",
	"/api/search":           "A Console dashboard shape: cross-resource search tuned to the Console's result list.",
	"/api/traffic-controls": "A Console dashboard shape: the Traffic Controls screen's aggregate of several unrelated subsystems.",
	"/api/runtime/overview": "A Console dashboard shape: the overview screen's aggregate. It changes whenever the screen does.",
	"/api/status": "Superseded by GET /api/v1/status, which is the supported one. Retained unchanged for the " +
		"Console; its shape is not stable.",

	// ── Runtime projections for subsystems still completing ───────────────
	"/api/certs":    "A runtime projection that will change as the certificate subsystem completes.",
	"/api/tls":      "A runtime projection that will change as the TLS subsystem completes.",
	"/api/mtls":     "A runtime projection that will change as the mTLS subsystem completes.",
	"/api/security": "A runtime projection that will change as the security subsystem completes.",
	"/api/plugins":  "A runtime projection that will change as the plugin subsystem completes.",
	"/api/upstreams/{name}/resilience": "A resilience projection whose taxonomy is still being closed by #144. " +
		"Publishing it now would freeze a shape that issue is still deciding.",

	// ── Observability ring buffers ────────────────────────────────────────
	"/api/observability/requests":         "A ring-buffer projection sized and shaped for the Console. Its capacity is not a contract.",
	"/api/observability/failing-routes":   "A ring-buffer projection sized and shaped for the Console.",
	"/api/observability/timeline":         "A ring-buffer projection sized and shaped for the Console.",
	"/api/observability/upstream-history": "A ring-buffer projection sized and shaped for the Console.",
	"/api/observability/cert-history":     "A ring-buffer projection sized and shaped for the Console.",
	"/api/observability/logs":             "A ring-buffer projection sized and shaped for the Console.",

	// ── SSE ───────────────────────────────────────────────────────────────
	"/api/events": "Server-sent events with no Last-Event-ID resume. Publishing it would freeze a Console transport " +
		"and hand external clients a stream they cannot resume after a disconnect. ADR 0019 §31 has automation poll " +
		"GET /api/v1/config/applies/{apply_id} instead.",
	"/api/observability/logs/stream": "Server-sent events with no Last-Event-ID resume; the same reason as /api/events.",

	// ── Authoring aids ────────────────────────────────────────────────────
	"/api/wizard":          "An authoring aid for the Console's guided flows, not a machine contract.",
	"/api/wizard/generate": "An authoring aid for the Console's guided flows, not a machine contract.",

	// ── Uploads ───────────────────────────────────────────────────────────
	"/api/plugins/upload": "A multipart upload. External exposure needs its own size, filename, path-traversal and " +
		"streaming-error review, which ADR 0019 §36 defers rather than granting by default.",
	"/api/transcode/descriptor-upload": "A multipart upload; the same deferred review as /api/plugins/upload.",

	// ── Audit ─────────────────────────────────────────────────────────────
	"/api/audit": "A strong external candidate, deliberately deferred so the export format is designed on its own " +
		"terms rather than frozen by accident (ADR 0019 §36).",
	"/api/audit/export": "The same deferral as /api/audit: the export format is reviewed on its own terms first.",

	// ── Raw configuration and raw history bodies ──────────────────────────
	// These two are the reason v1 has no raw-readback path at all. They stay
	// exactly as they are for the Console and for local operators; they are
	// simply not supported external contracts. ADR 0019 §36 records the single
	// re-entry trigger both share.
	"/api/config": "Returns the exact configuration bytes, secrets included, under config:raw. No /api/v1 route returns " +
		"raw configuration bytes: reconciling raw export with \"no secret readback\" belongs in a decision that reviews " +
		"secret handling on its own terms (ADR 0019 §24, §36). GET /api/v1/config/export is the supported, redacted one.",
	"/api/config/history/{id}": "Returns a stored snapshot body under history:raw. A history snapshot is a configuration " +
		"file, so this is the same data class as /api/config and is withdrawn from v1 with it. v1 publishes the listing, " +
		"the diff and rollback; rollback needs no bytes to cross the API because the server reads its own snapshot.",
	"/api/history/get": "Returns a stored snapshot body under history:raw; the same data class and the same withdrawal " +
		"as /api/config/history/{id}.",
	"/api/config/preview":         "Renders a raw candidate under config:raw. Raw candidate echoes are never returned by a v1 operation.",
	"/api/config/patch/candidate": "Returns the exact candidate bytes a patch would produce, under config:raw. Same data class.",

	// ── Legacy unversioned operational endpoints ──────────────────────────
	"/cache/purge": "A legacy unversioned operational endpoint. Its shape predates this contract and it is not part of " +
		"the configuration surface v1 publishes.",
	"/reload": "A legacy unversioned reload trigger. It has no place in a managed-authority world, where a change is " +
		"applied through an operation with a base_version rather than by asking the server to re-read a file.",
	"/debug/pprof/": "A Go runtime detail behind admin:manage. Publishing it would make pprof's format part of Jul's " +
		"compatibility contract.",

	// ── Existing /api/… configuration routes retained for the Console ─────
	// Every one of these has a supported /api/v1 counterpart. They keep their
	// current behaviour — including base_version being optional, where empty
	// means force — precisely so the Console is unaffected by v1's stricter
	// contract (ADR 0019 §27).
	"/api/config/raw":             "Retained for the Console. POST /api/v1/config/apply is the supported operation.",
	"/api/config/settings":        "Retained for the Console's settings screen. The supported path is a typed patch through /api/v1/config/patch/apply.",
	"/api/config/validate":        "Retained for the Console. POST /api/v1/config/validate is the supported one.",
	"/api/config/diff":            "Retained for the Console. POST /api/v1/config/plan is the supported one.",
	"/api/config/patch":           "Retained for the Console. POST /api/v1/config/patch is the supported one.",
	"/api/config/patch/preview":   "Retained for the Console. POST /api/v1/config/patch is the supported one.",
	"/api/config/patch/apply":     "Retained for the Console. POST /api/v1/config/patch/apply is the supported one.",
	"/api/config/apply":           "Retained for the Console. POST /api/v1/config/apply is the supported one.",
	"/api/config/pending-restart": "Retained for the Console. GET /api/v1/config/pending-restart is the supported one.",
	"/api/config/pending-restart/discard": "Retained for the Console. POST /api/v1/config/pending-restart/discard is the " +
		"supported one.",
	"/api/config/authority/refresh": "An explicit drift re-assessment for the Console's authority banner. v1 reports drift " +
		"in GET /api/v1/status; forcing a re-read is a Console affordance, not an automation contract.",
	"/api/config/applies/{id}":      "Retained for the Console. GET /api/v1/config/applies/{apply_id} is the supported one.",
	"/api/config/history":           "Retained for the Console. GET /api/v1/config/history is the supported one.",
	"/api/config/history/{id}/diff": "Retained for the Console. GET /api/v1/config/history/{id}/diff is the supported one.",
	"/api/config/rollback":          "Retained for the Console. POST /api/v1/config/rollback is the supported one.",
	"/api/config/adopt-external":    "Retained for the Console. POST /api/v1/config/adopt-external is the supported one.",
	"/api/config/adopt-external/preview": "Retained for the Console. POST /api/v1/config/adopt-external/preview is the " +
		"supported one.",
	"/api/history":                         "A second, older history listing shape retained for the Console. GET /api/v1/config/history is the supported one.",
	"/api/history/rollback":                "A second, older rollback shape retained for the Console. POST /api/v1/config/rollback is the supported one.",
	"/api/routes":                          "Retained for the Console. GET /api/v1/routes is the supported one.",
	"/api/routes/test":                     "Retained for the Console. POST /api/v1/routes/test is the supported one.",
	"/api/upstreams":                       "Retained for the Console. GET /api/v1/upstreams is the supported one.",
	"/api/streams":                         "Retained for the Console. GET /api/v1/streams is the supported one.",
	"/api/listeners":                       "Retained for the Console. GET /api/v1/listeners is the supported one.",
	"/api/listeners/{addr}/client_address": "Retained for the Console. /api/v1/listeners/{addr}/client_address is the supported one.",
}

// InternalReason returns the recorded reason a route is not external, and
// whether one is recorded at all. It is exported so the contract test in
// internal/apicontract can assert the inventory is complete without
// duplicating it.
func InternalReason(pattern string) (string, bool) {
	r, ok := internalRouteReasons[pattern]
	return r, ok
}

// InternalRouteReasons returns a copy of the classification inventory. The copy
// keeps the map unmodifiable by a caller that only wants to read it.
func InternalRouteReasons() map[string]string {
	out := make(map[string]string, len(internalRouteReasons))
	for k, v := range internalRouteReasons {
		out[k] = v
	}
	return out
}
