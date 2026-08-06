// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package background

import (
	"context"

	"jul/internal/middleware"
	"jul/internal/upstream"
)

// Detach builds the context for a background operation: it is rooted in parent
// (the process/generation lifetime) and carries an explicit allow-list of
// request-scoped values copied from src. The whole point is that src's
// cancellation and deadline are NOT inherited, so a client disconnect cannot
// abort work the server decided to finish.
//
// Context-value inventory — everything below the cache in the handler chain is
// the location action itself (proxy, static, FastCGI, gRPC), because the
// per-location plugin, client-certificate, authentication, rate-limit and WAF
// middleware all wrap OUTSIDE the cache. The allow-list therefore covers
// exactly what those actions read:
//
//   - upstream pool snapshot — REQUIRED. Backend selection (PickCtx /
//     BackendsCtx) prefers the generation-scoped snapshot, so a revalidation
//     must see the same backend set as the request that started it.
//   - mutual-TLS client identity — REQUIRED. The reverse proxy expands
//     $ssl_client_* variables from it; dropping it would send the origin a
//     different request than the one being revalidated.
//   - request id — carried for log correlation. A bounded, already-logged
//     value.
//   - trace id — carried for log correlation, same reasoning.
//
// Deliberately NOT copied:
//
//   - authentication claims: consumed by the rate-limit key function, which
//     runs outside the cache and is not re-entered by a revalidation. Copying
//     them would retain decoded token payloads for the life of the refresh.
//   - plugin invocation state: plugin middleware also wraps outside the cache.
//   - the tracing span: a revalidation is not part of the client request's
//     trace and must not extend its lifetime; the cache starts its own span
//     from the background context instead.
//   - redaction generation: redaction is process-global state installed by the
//     server, not a context value, so it needs no propagation.
//   - client cancellation and the request deadline: replaced by the process
//     lifetime plus a bounded operation deadline.
func Detach(parent, src context.Context) context.Context {
	if src == nil {
		return parent
	}
	if snaps := upstream.SnapshotsFrom(src); len(snaps) > 0 {
		parent = upstream.WithSnapshot(parent, snaps)
	}
	if id := middleware.ClientIdentityFrom(src); id != nil {
		parent = middleware.WithClientIdentity(parent, id)
	}
	if id := middleware.RequestIDFrom(src); id != "" {
		parent = middleware.WithRequestID(parent, id)
	}
	if id := middleware.TraceIDFrom(src); id != "" {
		parent = middleware.WithTraceID(parent, id)
	}
	return parent
}
