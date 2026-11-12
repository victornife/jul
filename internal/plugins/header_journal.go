// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"net/http"
	"net/textproto"
)

// headerJournal records, for each response header a request-phase guest has
// set, the value the header had before the first plugin set it. A
// request-phase failure answers with a replacement error response (503, 408 or
// 500), so the headers the plugins set for the response they meant to produce,
// such as Content-Encoding, must not describe it. Headers set by other
// middleware are untouched, and compression middleware sets Content-Encoding
// only when it compresses.
type headerJournal map[string]headerPrev

type headerPrev struct {
	values []string
	had    bool
}

type journalCtxKey struct{}

// inheritedJournal returns the journal of the plugin layers outside this one
// that continued the request, or nil.
func inheritedJournal(ctx context.Context) headerJournal {
	j, _ := ctx.Value(journalCtxKey{}).(headerJournal)
	return j
}

// setResponseHeader sets a response header for the guest and journals the
// header's previous value the first time a plugin sets it for this request.
func (inv *invocation) setResponseHeader(name, val string) {
	h := inv.w.Header()
	key := textproto.CanonicalMIMEHeaderKey(name)
	if _, ok := inv.journal[key]; !ok {
		if _, ok := inheritedJournal(inv.r.Context())[key]; !ok {
			if inv.journal == nil {
				inv.journal = headerJournal{}
			}
			prev, had := h[key]
			// Set replaces the slice, so prev keeps the old values.
			inv.journal[key] = headerPrev{values: prev, had: had}
		}
	}
	h.Set(name, val)
}

// continueWithJournal returns r carrying the journal of every plugin layer so
// far, so an inner plugin's failure can undo this layer's header changes too.
// The inherited journal is copied, never shared.
func continueWithJournal(r *http.Request, inv *invocation) *http.Request {
	if len(inv.journal) == 0 {
		return r
	}
	parent := inheritedJournal(r.Context())
	merged := make(headerJournal, len(parent)+len(inv.journal))
	for k, v := range parent {
		merged[k] = v
	}
	for k, v := range inv.journal {
		merged[k] = v
	}
	return r.WithContext(context.WithValue(r.Context(), journalCtxKey{}, merged))
}

// undoPluginHeaders restores every header the plugin layers set for this
// request to its value before the first plugin set it. inv may be nil when the
// failure happened before the guest was called.
func undoPluginHeaders(w http.ResponseWriter, r *http.Request, inv *invocation) {
	h := w.Header()
	restore := func(j headerJournal) {
		for k, prev := range j {
			if prev.had {
				h[k] = prev.values
			} else {
				delete(h, k)
			}
		}
	}
	if inv != nil {
		restore(inv.journal)
	}
	restore(inheritedJournal(r.Context()))
}
