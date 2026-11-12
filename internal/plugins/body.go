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
	"math"
	"net/http"
	"os"

	"jul/internal/inspection"
	"jul/internal/reqdeadline"
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

// initialBodyBuffer caps the allocation made before any body byte arrives,
// so a client declaring a large Content-Length and then stalling pins at
// most this much rather than its declared length.
const initialBodyBuffer = 32 << 10

// readBounded reads r to EOF, failing with errBodyTooLarge once more than
// limit bytes arrive regardless of any declared length. The buffer grows only
// as data arrives; an unverified sizeHint (Content-Length within the limit)
// just stops growth from overshooting the declared length. Every capacity is
// charged through charge before it is allocated (ADR 0032 §4); on failure the
// buffer is dropped and refunded once the read has returned. On success the
// caller owns the returned buffer and its charge.
func readBounded(r io.Reader, sizeHint int64, limit int, charge chargeFunc) (*chargedBuf, error) {
	if limit < 0 || limit == math.MaxInt {
		return nil, errBodyTooLarge
	}
	ceiling := limit + 1
	want := ceiling
	known := sizeHint >= 0 && sizeHint <= int64(limit)
	if known {
		want = int(sizeHint) + 1
	}
	initial := min(512, want)
	if known {
		initial = min(initialBodyBuffer, want)
	}
	cb, err := newChargedBuf(charge, initial)
	if err != nil {
		return nil, err
	}
	lr := &io.LimitedReader{R: r, N: int64(ceiling)}
	for {
		if len(cb.b) == cap(cb.b) {
			if err := cb.grow(nextBodyCap(cap(cb.b), want, ceiling)); err != nil {
				cb.release()
				return nil, err
			}
		}
		k, err := lr.Read(cb.b[len(cb.b):cap(cb.b)])
		cb.b = cb.b[:len(cb.b)+k]
		if err == io.EOF {
			break
		}
		if err != nil {
			cb.release()
			return nil, err
		}
	}
	if len(cb.b) > limit {
		cb.release()
		return nil, errBodyTooLarge
	}
	return cb, nil
}

// nextBodyCap doubles c without overflow, stopping once at the declared
// length (want) and never exceeding ceiling (limit+1).
func nextBodyCap(c, want, ceiling int) int {
	n := ceiling
	if c <= ceiling/2 {
		n = 2 * c
	}
	if c < want && n > want {
		n = want
	}
	return max(n, c)
}

// prepareRequestBody returns r's complete body within this plugin's
// max_request_body before any instance is acquired, so upload arrival is
// bounded by the transport, not the guest call timeout (#699). A body already
// prepared by an earlier plugin in the chain and not yet consumed is reused
// without copying or charging again, subject to this plugin's own limit.
//
// The read runs inside the inspection body-arrival phase of the request's
// deadline owner (ADR 0032 H6), and its buffer is charged to the request
// lease, which keeps the prepared body charged until the lease closes.
func (p *plugin) prepareRequestBody(w http.ResponseWriter, inv *invocation) ([]byte, error) {
	r := inv.r
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
	owner := reqdeadline.From(r.Context())
	if owner == nil {
		// Outside the server entry point (tests, embedded handlers) the
		// owner is local to this read and ends with it.
		owner = reqdeadline.NewForRequest(w, r)
		defer owner.End()
	}
	done, err := p.trackPreparation(owner)
	if err != nil {
		return nil, err
	}
	defer done()
	limits := p.gov.Limits()
	// If the writer refuses an abort, closing the body ends the read instead
	// (HTTP/2, HTTP/3); HTTP/1 uses the connection's read deadline.
	owner.InterruptWith(r.Body)
	if err := owner.BeginBodyRead(limits.BodyTimeout, limits.Mode == inspection.Observe, p.gov.RecordBodyTimeout); err != nil {
		owner.EndBodyRead()
		if errors.Is(err, reqdeadline.ErrUnsupported) {
			return nil, inspection.ErrDeadlineUnsupported
		}
		return nil, fmt.Errorf("plugin: read request body: %w", err)
	}
	cb, err := readBounded(r.Body, r.ContentLength, p.maxReqBody, leaseCharger(inv.lease))
	switch owner.EndBodyRead() {
	case reqdeadline.BodyTimedOut:
		cb.release()
		return nil, inspection.ErrBodyTimeout
	case reqdeadline.BodyUnsupported:
		cb.release()
		return nil, inspection.ErrDeadlineUnsupported
	}
	if err != nil {
		if errors.Is(err, errBodyTooLarge) || inspection.IsOverload(err) {
			return nil, err
		}
		return nil, fmt.Errorf("plugin: read request body: %w", err)
	}
	// The prepared body keeps cb's array and its charge until the lease
	// closes: downstream handlers and later plugins read it.
	r.Body = newPreparedBody(cb.b, r.Body)
	r.ContentLength = int64(len(cb.b))
	return cb.b, nil
}

// prepare runs preparation for a request-phase invocation and attributes a
// failure to one closed reason. A failed or abandoned preparation never
// acquires an instance or invokes the guest. Governor and deadline outcomes
// are counted by the inspection metrics, not as body-preparation failures.
func (p *plugin) prepare(ctx context.Context, w http.ResponseWriter, inv *invocation) error {
	data, err := p.prepareRequestBody(w, inv)
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
	case errors.Is(err, inspection.ErrBodyTimeout), errors.Is(err, inspection.ErrDeadlineUnsupported), inspection.IsOverload(err):
		return err
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

// trackPreparation registers the request's deadline owner as reading its body
// for this plugin until done is called, so close can abort the read. The
// owner applies the abort to the request's own read deadline: per request on
// HTTP/1 and per stream on HTTP/2 and HTTP/3; the connection is never closed
// for it. The abort is sticky, and once the request handler has returned the
// owner ignores it, so no ResponseWriter is used after its request ended.
func (p *plugin) trackPreparation(o *reqdeadline.Owner) (done func(), err error) {
	p.prepMu.Lock()
	defer p.prepMu.Unlock()
	if p.closed.Load() {
		return nil, errPluginClosed
	}
	if p.preparing == nil {
		p.preparing = make(map[*reqdeadline.Owner]int)
	}
	p.preparing[o]++
	return func() {
		p.prepMu.Lock()
		if p.preparing[o]--; p.preparing[o] <= 0 {
			delete(p.preparing, o)
		}
		p.prepMu.Unlock()
	}, nil
}

// abortPreparations marks p closed and aborts every body read in progress.
func (p *plugin) abortPreparations() {
	p.prepMu.Lock()
	defer p.prepMu.Unlock()
	p.closed.Store(true)
	unsupported := 0
	for o := range p.preparing {
		if errors.Is(o.Abort(), reqdeadline.ErrUnsupported) {
			unsupported++
		}
	}
	if unsupported > 0 && p.log != nil {
		// The read is interrupted through the transport instead; the
		// request still fails as retired and never reaches a guest.
		p.log.Warn("plugin retired while request bodies were arriving; read deadline unsupported by the response writer",
			"plugin", p.name, "requests", unsupported)
	}
}
