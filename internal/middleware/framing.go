// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"net/http"
)

// FramingGuard closes the connection after an HTTP/1.x request whose framing a
// front proxy could have read differently, so leftover bytes are never parsed
// as a second request (request smuggling, RFC 9112 §6.1 and §6.3).
//
//   - A chunked request. When it also carried Content-Length, RFC 9112 §6.3
//     requires the server to close the connection after responding: a front
//     proxy that framed it by Content-Length would otherwise get the bytes
//     after the chunked terminator served as another request. net/http drops
//     Content-Length before a handler runs, so the guard cannot tell the two
//     apart and closes after every chunked HTTP/1.x request.
//   - An HTTP/1.0 request with a body method and no Content-Length. net/http
//     discards Transfer-Encoding on HTTP/1.0 before a handler runs and treats
//     the body as empty; RFC 9112 §6.1 calls that framing faulty and requires
//     the connection to close, which net/http does not do for a keep-alive
//     HTTP/1.0 client.
//
// HTTP/2 and HTTP/3 frame requests themselves and are untouched.
func FramingGuard() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor == 1 && ambiguousFraming(r) {
				w.Header().Set("Connection", "close")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func ambiguousFraming(r *http.Request) bool {
	if len(r.TransferEncoding) > 0 {
		return true
	}
	if r.ProtoMinor != 0 || len(r.Header.Values("Content-Length")) > 0 {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodDelete:
		return false
	}
	return true
}
