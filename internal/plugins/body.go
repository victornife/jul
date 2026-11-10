// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// errPluginClosed fails a request that reaches a plugin whose generation has
// been retired and closed. It is answered 500 and is not a guest panic.
var errPluginClosed = errors.New("plugin: generation retired")

// errBodyNotPrepared fails a read_request_body call whose invocation had no
// prepared body. Negotiation prepares the body for every module importing the
// function, so this is a defensive invariant, not an expected path.
var errBodyNotPrepared = errors.New("plugin: request body was not prepared")

// Request-body preparation failure reasons, a closed set (#699).
const (
	bodyFailTooLarge  = "too_large"
	bodyFailReadError = "read_error"
	bodyFailCanceled  = "canceled"
	bodyFailRetired   = "retired"
)

// pastDeadline aborts a blocked body read when a plugin is retired.
var pastDeadline = time.Unix(1, 0)

// preparedBody is the request body after a plugin prepared it: the complete,
// bounded bytes, replayed downstream. Close still closes the original body so
// the transport keeps its own cleanup and cancellation responsibilities.
type preparedBody struct {
	data []byte
	rd   *bytes.Reader
	orig io.ReadCloser
}

func newPreparedBody(data []byte, orig io.ReadCloser) *preparedBody {
	return &preparedBody{data: data, rd: bytes.NewReader(data), orig: orig}
}

func (b *preparedBody) Read(p []byte) (int, error) { return b.rd.Read(p) }

// WriteTo keeps io.Copy from the prepared body allocation-free, as the
// previous io.NopCloser(bytes.Reader) replacement did.
func (b *preparedBody) WriteTo(w io.Writer) (int64, error) { return b.rd.WriteTo(w) }

func (b *preparedBody) Close() error { return b.orig.Close() }

// unread reports that nothing has consumed the replay yet, so a later plugin
// in the chain can use data as the complete body without copying it.
func (b *preparedBody) unread() bool { return b.rd.Len() == len(b.data) }

// readBounded reads r to EOF, failing with errBodyTooLarge once more than
// limit bytes arrive regardless of any declared length. A trusted sizeHint
// (Content-Length within the limit) sizes the buffer exactly; otherwise it
// grows as data arrives.
func readBounded(r io.Reader, sizeHint int64, limit int) ([]byte, error) {
	n := min(512, limit+1)
	if sizeHint >= 0 && sizeHint <= int64(limit) {
		n = int(sizeHint) + 1
	}
	buf := make([]byte, 0, n)
	lr := &io.LimitedReader{R: r, N: int64(limit) + 1}
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		k, err := lr.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+k]
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if len(buf) > limit {
		return nil, errBodyTooLarge
	}
	return buf, nil
}

// prepareRequestBody returns r's complete body within this plugin's
// max_request_body before any instance is acquired, so upload arrival is
// bounded by the transport, not the guest call timeout (#699). A body already
// prepared by an earlier plugin in the chain and not yet consumed is reused
// without copying, subject to this plugin's own limit.
func (p *plugin) prepareRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	if pb, ok := r.Body.(*preparedBody); ok && pb.unread() {
		if len(pb.data) > p.maxReqBody {
			return nil, errBodyTooLarge
		}
		return pb.data, nil
	}
	if r.ContentLength > int64(p.maxReqBody) {
		return nil, errBodyTooLarge
	}
	done, err := p.trackPreparation(w)
	if err != nil {
		return nil, err
	}
	data, err := readBounded(r.Body, r.ContentLength, p.maxReqBody)
	done()
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return nil, err
		}
		return nil, fmt.Errorf("plugin: read request body: %w", err)
	}
	r.Body = newPreparedBody(data, r.Body)
	r.ContentLength = int64(len(data))
	return data, nil
}

// prepare runs preparation for a request-phase invocation and attributes a
// failure to one closed reason. A failed or abandoned preparation never
// acquires an instance or invokes the guest.
func (p *plugin) prepare(ctx context.Context, w http.ResponseWriter, inv *invocation) error {
	data, err := p.prepareRequestBody(w, inv.r)
	if err == nil && p.closed.Load() {
		err = errPluginClosed
	}
	if err == nil && ctx.Err() != nil {
		err = fmt.Errorf("plugin: request ended before invocation: %w", ctx.Err())
	}
	if err == nil {
		inv.body, inv.bodyPrepared = data, true
		return nil
	}
	reason := bodyFailReadError
	switch {
	case errors.Is(err, errBodyTooLarge):
		reason = bodyFailTooLarge
	case p.closed.Load():
		reason, err = bodyFailRetired, errPluginClosed
	case errors.Is(err, os.ErrDeadlineExceeded):
		// An expired server read timeout also ends the request context on
		// HTTP/1; it is still a read failure, not a client cancellation.
	case ctx.Err() != nil:
		reason = bodyFailCanceled
	}
	p.onBodyFail(p.name, reason)
	return err
}

// trackPreparation registers w's request as reading its body for this plugin
// until done is called, so close can abort the read through the request's
// own read deadline. That deadline is per request on HTTP/1 and per stream on
// HTTP/2 and HTTP/3; the connection is never closed for it.
func (p *plugin) trackPreparation(w http.ResponseWriter) (done func(), err error) {
	rc := http.NewResponseController(w)
	p.prepMu.Lock()
	defer p.prepMu.Unlock()
	if p.closed.Load() {
		return nil, errPluginClosed
	}
	if p.preparing == nil {
		p.preparing = make(map[*http.ResponseController]struct{})
	}
	p.preparing[rc] = struct{}{}
	return func() {
		p.prepMu.Lock()
		delete(p.preparing, rc)
		p.prepMu.Unlock()
	}, nil
}

// abortPreparations marks p closed and ends every body read in progress. It
// holds prepMu while setting deadlines so no request's ResponseWriter is used
// after its preparation has returned.
func (p *plugin) abortPreparations() {
	p.prepMu.Lock()
	defer p.prepMu.Unlock()
	p.closed.Store(true)
	for rc := range p.preparing {
		_ = rc.SetReadDeadline(pastDeadline)
	}
}
