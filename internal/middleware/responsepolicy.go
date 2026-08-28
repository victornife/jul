// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"bufio"
	"context"
	"net"
	"net/http"

	"jul/internal/config"
	"jul/internal/respwriter"
)

// This file implements the response-header policy wrapper (ADR 0018 §8, §10):
// the outermost per-location ResponseWriter decorator that applies
// response_headers operations and CORS response headers exactly once, inside
// WriteHeader, immediately before delegating outward — never by pre-setting
// headers before calling next, which would put them in the shared header map
// where the cache captures them (that is defect class #332).

// responseHeaderOp is one compiled response_headers operation.
type responseHeaderOp struct {
	op    string // add | set | remove
	name  string // canonicalized
	value string
}

// policyWriterCtxKey is the unexported context key carrying the *policyWriter
// for this request, so the preflight terminator further down the chain can
// mark its own generated response without needing to unwrap the
// capability-masked http.ResponseWriter respwriter.Wrap returns.
type policyWriterCtxKey struct{}

// markGeneratedResponse tells this request's response-policy wrapper that the
// response about to be written is already exactly right — a Jul-generated
// preflight 204 — so neither the generic response_headers operations nor the
// CORS response-header injection should touch it (ADR 0018 §8b).
func markGeneratedResponse(r *http.Request) {
	if pw, ok := r.Context().Value(policyWriterCtxKey{}).(*policyWriter); ok {
		pw.skip = true
	}
}

// ResponsePolicy returns middleware implementing ADR 0018 §8/§8b/§10: the
// ordered response_headers operations, then CORS's response headers, applied
// once at the first status >= 200 (1xx passes straight through; 101 gets the
// operations but never CORS, since WebSocket has no CORS surface, §12). A
// location with neither response_headers nor cors installs no wrapper and
// allocates nothing.
func ResponsePolicy(headerOps []config.ResponseHeaderOp, cors *CORSPolicy) Middleware {
	if len(headerOps) == 0 && cors == nil {
		return nil
	}
	ops := make([]responseHeaderOp, len(headerOps))
	for i, o := range headerOps {
		value := ""
		if o.Value != nil {
			value = *o.Value
		}
		ops[i] = responseHeaderOp{op: o.Op, name: http.CanonicalHeaderKey(o.Name), value: value}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pw := &policyWriter{ResponseWriter: w, ops: ops, cors: cors, req: r}
			ctx := context.WithValue(r.Context(), policyWriterCtxKey{}, pw)
			next.ServeHTTP(pw.Writer(), r.WithContext(ctx))
		})
	}
}

// policyWriter applies the response policy at the first final status and is
// never handed to a handler directly: Writer returns a capability-transparent
// wrapper, mirroring middleware.Recorder and cache.cacheWriter.
type policyWriter struct {
	http.ResponseWriter
	ops  []responseHeaderOp
	cors *CORSPolicy
	req  *http.Request

	wroteHeader bool
	hijacked    bool
	// skip is set by the preflight terminator (markGeneratedResponse) for a
	// response it has already written completely and correctly itself.
	skip bool
}

// Writer returns the value to pass down the chain.
func (p *policyWriter) Writer() http.ResponseWriter {
	return respwriter.Wrap(p, p.ResponseWriter)
}

func (p *policyWriter) WriteHeader(code int) {
	if p.wroteHeader || p.hijacked {
		return
	}
	// 1xx (other than 101) is interim: RFC 9110 §15.2 permits any number ahead
	// of exactly one final status, so it must pass through untouched — no
	// operation, no CORS decision, no latch. This is #331's rule.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		p.ResponseWriter.WriteHeader(code)
		return
	}
	p.wroteHeader = true
	if !p.skip {
		for _, op := range p.ops {
			switch op.op {
			case "add":
				p.Header().Add(op.name, op.value)
			case "set":
				p.Header().Set(op.name, op.value)
			case "remove":
				p.Header().Del(op.name)
			}
		}
		// §12: response_headers applies to a WebSocket upgrade's 101, but CORS
		// has no surface there at all.
		if p.cors != nil && code != http.StatusSwitchingProtocols {
			p.cors.ApplyToResponse(p.Header(), p.req)
		}
	}
	p.ResponseWriter.WriteHeader(code)
}

func (p *policyWriter) Write(b []byte) (int, error) {
	if p.hijacked {
		return 0, http.ErrHijacked
	}
	if !p.wroteHeader {
		p.WriteHeader(http.StatusOK)
	}
	return p.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer. respwriter.Wrap exposes it only
// when the underlying writer is a Flusher.
func (p *policyWriter) Flush() {
	if p.hijacked {
		return
	}
	if !p.wroteHeader {
		p.WriteHeader(http.StatusOK)
	}
	if f, ok := p.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack hands the connection to the caller. Once hijacked the response is no
// longer this writer's to decorate.
func (p *policyWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := p.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, buf, err := h.Hijack()
	if err == nil {
		p.hijacked = true
	}
	return conn, buf, err
}
