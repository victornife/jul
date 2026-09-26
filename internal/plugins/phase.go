// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bufio"
	"context"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"

	"jul/internal/respwriter"
)

// This file implements the jul-abi/v2 response phase (ADR 0020 §3–§8): the
// per-request subscription record written by a request hook, the response
// point installed just inside the location's WAF, and the per-subscription
// writer layer that presents the action's response to handle_response.

type phaseKey struct{}

// subscription is one plugin's request for a response callback, recorded when
// its handle_request returned Continue. It pins the plugin of the generation
// that ran the request hook, so a reload mid-request cannot switch modules.
type subscription struct {
	p     *plugin
	mode  uint32
	state []byte
}

// phaseChain is the request-scoped list of subscriptions, outermost first.
type phaseChain struct {
	subs    []subscription
	reached bool
}

// subscribe records a response subscription on r, returning the request to
// pass on (a new one only when this is the request's first subscription).
func subscribe(r *http.Request, p *plugin, mode uint32, state []byte) *http.Request {
	c, _ := r.Context().Value(phaseKey{}).(*phaseChain)
	if c == nil {
		c = &phaseChain{}
		r = r.WithContext(context.WithValue(r.Context(), phaseKey{}, c))
	}
	c.subs = append(c.subs, subscription{p: p, mode: mode, state: state})
	return r
}

// identityRequestHeaders are removed from the request handed to the action
// under a body subscription, so the guest is presented the whole identity
// representation and a client cannot pick an encoding or range the guest
// cannot inspect (ADR 0020 §5).
var identityRequestHeaders = []string{"Range", "If-Range", "Accept-Encoding"}

// replacedValidators describe the exact bytes of a representation and are
// dropped when a guest replaces the body.
var replacedValidators = []string{"Etag", "Content-Md5", "Digest", "Content-Digest", "Repr-Digest"}

// responsePoint is the middleware at the v2 response point. Without a
// subscription on the request it is a pass-through.
func responsePoint(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Context().Value(phaseKey{}).(*phaseChain)
		if c == nil || c.reached || len(c.subs) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		c.reached = true

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		inner := r.WithContext(ctx)
		for _, s := range c.subs {
			if s.mode == responseModeBody {
				inner.Header = r.Header.Clone()
				for _, h := range identityRequestHeaders {
					inner.Header.Del(h)
				}
				break
			}
		}

		entry := w.Header().Clone()
		layers := make([]*responseLayer, len(c.subs))
		cur := w
		for i, s := range c.subs {
			l := &responseLayer{parent: cur, req: r, sub: s, cancel: cancel, entry: entry}
			layers[i] = l
			cur = respwriter.Wrap(l, cur)
		}
		serveInner(next, cur, inner, layers)
		for i := len(layers) - 1; i >= 0; i-- {
			layers[i].finish()
		}
	})
}

// serveInner runs the action, absorbing only the handler abort this response
// point induced by cancelling the action after a layer discarded the response.
func serveInner(next http.Handler, w http.ResponseWriter, r *http.Request, layers []*responseLayer) {
	defer func() {
		if rec := recover(); rec != nil {
			if rec == http.ErrAbortHandler {
				for _, l := range layers {
					if l.state == layerDiscard {
						return
					}
				}
			}
			panic(rec)
		}
	}()
	next.ServeHTTP(w, r)
}

type layerState uint8

const (
	// layerPending: nothing written yet.
	layerPending layerState = iota
	// layerBuffering: a body subscription collecting an eligible body.
	layerBuffering
	// layerPassthrough: committed to the parent; writes go straight through.
	layerPassthrough
	// layerDiscard: the action's response was rejected or the hook failed;
	// action writes are dropped and finish writes the replacement.
	layerDiscard
	// layerUpgraded: a protocol switch took the connection.
	layerUpgraded
)

// responseLayer presents the response to one subscription. Header is the
// shared response header map; nothing reaches parent until the hook decided.
type responseLayer struct {
	parent http.ResponseWriter
	req    *http.Request
	sub    subscription
	cancel context.CancelFunc
	entry  http.Header

	state   layerState
	status  int
	buf     []byte
	hookRan bool
	final   int
	failed  bool
}

func (l *responseLayer) Header() http.Header { return l.parent.Header() }

func (l *responseLayer) WriteHeader(code int) {
	if l.state != layerPending {
		return
	}
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		l.parent.WriteHeader(code)
		return
	}
	l.status = code
	if code == http.StatusSwitchingProtocols {
		l.state = layerUpgraded
		l.parent.WriteHeader(code)
		return
	}
	state := classifyResponse(l.sub.mode, l.req.Method, code, l.Header(), l.sub.p.maxRespBody)
	if state == bodyAvailable {
		l.state = layerBuffering
		if n, err := strconv.Atoi(l.Header().Get("Content-Length")); err == nil && n > 0 {
			l.buf = make([]byte, 0, n)
		}
		return
	}
	if view, ok := l.decide(state, nil); ok {
		l.parent.WriteHeader(view.status)
		l.state = layerPassthrough
	}
}

func (l *responseLayer) Write(p []byte) (int, error) {
	switch l.state {
	case layerPending:
		l.WriteHeader(http.StatusOK)
		return l.Write(p)
	case layerBuffering:
		if len(l.buf)+len(p) <= l.sub.p.maxRespBody {
			l.buf = append(l.buf, p...)
			return len(p), nil
		}
		view, ok := l.decide(bodyTooLarge, nil)
		buffered := l.buf
		l.buf = nil
		if !ok {
			return len(p), nil
		}
		l.parent.WriteHeader(view.status)
		l.state = layerPassthrough
		if len(buffered) > 0 {
			if _, err := l.parent.Write(buffered); err != nil {
				return 0, err
			}
		}
		return l.parent.Write(p)
	case layerPassthrough:
		return l.parent.Write(p)
	case layerDiscard:
		return len(p), nil
	default:
		return 0, http.ErrHijacked
	}
}

// Flush is exposed only when the parent is a Flusher (respwriter.Wrap). A
// flush while buffering is absorbed: the body is bounded by max_response_body
// and the reverse proxy flushes every response of unknown length.
func (l *responseLayer) Flush() {
	if l.state == layerPending {
		l.WriteHeader(http.StatusOK)
	}
	if l.state != layerPassthrough {
		return
	}
	if f, ok := l.parent.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack is exposed only when the parent is a Hijacker. Once the connection
// is taken the response is committed; the hook observes it after the fact.
func (l *responseLayer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := l.parent.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := h.Hijack()
	if err == nil {
		l.state = layerUpgraded
		l.buf = nil
		if l.status == 0 {
			l.status = http.StatusSwitchingProtocols
		}
	}
	return conn, rw, err
}

// finish completes the layer after the action returned.
func (l *responseLayer) finish() {
	if l.state == layerPending {
		l.WriteHeader(http.StatusOK)
	}
	switch l.state {
	case layerBuffering:
		view, ok := l.decide(bodyAvailable, l.buf)
		l.buf = nil
		if !ok {
			l.writeFinal()
			return
		}
		h := l.Header()
		h.Del("Transfer-Encoding")
		h.Del("Accept-Ranges")
		if view.replaced {
			for _, k := range replacedValidators {
				h.Del(k)
			}
		}
		h.Set("Content-Length", strconv.Itoa(len(view.body)))
		l.parent.WriteHeader(view.status)
		l.state = layerPassthrough
		if len(view.body) > 0 {
			_, _ = l.parent.Write(view.body)
		}
	case layerDiscard:
		l.writeFinal()
	case layerUpgraded:
		if !l.hookRan {
			// Already committed: an error is counted by invokeResponse and
			// there is nothing left to change.
			_, _, _ = l.run(bodyUpgraded, nil, true)
		}
	}
}

// decide runs the hook before commitment. It returns the view and true to
// deliver the (mutated) response, or false after switching to layerDiscard:
// the action is cancelled and finish writes the rejection or failure.
func (l *responseLayer) decide(state uint32, body []byte) (*responseView, bool) {
	view, res, err := l.run(state, body, false)
	switch {
	case err != nil:
		l.final, l.failed = http.StatusInternalServerError, true
	case res == responseReject:
		l.final = http.StatusBadGateway
		if view.statusSet && view.status >= 400 && view.status <= 599 {
			l.final = view.status
		}
	default:
		return view, true
	}
	l.state = layerDiscard
	l.cancel()
	return view, false
}

func (l *responseLayer) run(state uint32, body []byte, committed bool) (*responseView, uint32, error) {
	l.hookRan = true
	p := l.sub.p
	if l.sub.mode == responseModeBody && state != bodyAvailable {
		p.onNoBody(p.name, bodyStateLabel(state))
	}
	view := &responseView{origStatus: l.status, status: l.status, header: l.Header(), body: body, bodyState: state, committed: committed}
	res, err := p.invokeResponse(l.req.Context(), l.req, view, l.sub.state)
	return view, res, err
}

// writeFinal replaces the action's response: the header map returns to what it
// was before the action ran, and the status/body are the rejection or failure.
func (l *responseLayer) writeFinal() {
	h := l.Header()
	for k := range h {
		delete(h, k)
	}
	for k, v := range l.entry {
		h[k] = append([]string(nil), v...)
	}
	var body []byte
	if l.failed {
		body = []byte("plugin error\n")
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("X-Content-Type-Options", "nosniff")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	l.parent.WriteHeader(l.final)
	l.state = layerPassthrough
	if len(body) > 0 {
		_, _ = l.parent.Write(body)
	}
}

// classifyResponse is the single body-eligibility function (ADR 0020 §5). It
// runs when the action commits its status; a buffered body that later grows
// past the cap becomes bodyTooLarge.
func classifyResponse(mode uint32, method string, status int, h http.Header, maxBody int) uint32 {
	if mode != responseModeBody {
		return bodyNotRequested
	}
	if method == http.MethodHead || status == http.StatusNoContent || status == http.StatusNotModified {
		return bodyNone
	}
	if status == http.StatusPartialContent {
		return bodyPartial
	}
	for _, ce := range h.Values("Content-Encoding") {
		for _, tok := range strings.Split(ce, ",") {
			if t := strings.TrimSpace(tok); t != "" && !strings.EqualFold(t, "identity") {
				return bodyEncoded
			}
		}
	}
	if streamingResponse(h) {
		return bodyStreaming
	}
	if cl := strings.TrimSpace(h.Get("Content-Length")); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil && n > int64(maxBody) {
			return bodyTooLarge
		}
	}
	return bodyAvailable
}

// streamingResponse reports a response that declares streaming framing.
func streamingResponse(h http.Header) bool {
	mt, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
	switch {
	case mt == "text/event-stream", mt == "multipart/x-mixed-replace", strings.HasPrefix(mt, "application/grpc"):
		return true
	case strings.EqualFold(strings.TrimSpace(h.Get("X-Accel-Buffering")), "no"):
		return true
	}
	return len(h.Values("Trailer")) > 0
}
