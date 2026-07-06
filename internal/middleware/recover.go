// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover returns middleware that converts panics in downstream handlers into a
// 500 response and logs the stack trace. It isolates a single failing request
// from taking down the process.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					// ErrAbortHandler is the documented way for a handler to
					// abort without logging noise; re-panic so net/http handles it.
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					log.Error("panic recovered",
						"error", rec,
						"method", r.Method,
						"path", r.URL.Path,
						"request_id", RequestIDFrom(r.Context()),
						"stack", string(debug.Stack()),
					)
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
