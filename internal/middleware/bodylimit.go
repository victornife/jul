// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import "net/http"

// BodyLimit returns middleware that caps the request body at maxBytes. A
// request that declares a Content-Length over the limit is rejected up front
// with 413 before its body is read; otherwise the body is wrapped with
// http.MaxBytesReader, which makes reads fail and triggers a 413 once the limit
// is exceeded (covering chunked uploads with no declared length). A non-positive
// limit disables the cap.
func BodyLimit(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		if maxBytes <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A known Content-Length over the limit can be refused immediately,
			// before reading (and, for a proxy, before dialing the upstream). An
			// unknown length (-1, e.g. chunked) is handled by MaxBytesReader.
			if r.ContentLength > maxBytes {
				http.Error(w, "request entity too large", http.StatusRequestEntityTooLarge)
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}
