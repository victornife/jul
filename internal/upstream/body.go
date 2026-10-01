// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// OwedBodyLength is the body length a backend must deliver for resp before
// WrapAttemptBody may call the attempt successful, or -1 when no length is
// owed. A response to HEAD and a 1xx, 204 or 304 response carry no content
// whatever Content-Length says (RFC 9110 §8.6, §9.3.2, §15.3.5,
// §15.4.5); treating their empty body as truncated failed every proxied HEAD
// and counted it against the backend (#534).
func OwedBodyLength(resp *http.Response) int64 {
	if resp.Request != nil && resp.Request.Method == http.MethodHead {
		return -1
	}
	if resp.StatusCode < 200 || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
		return -1
	}
	return resp.ContentLength
}

// WrapAttemptBody delays an attempt's health verdict until its response body
// reaches a real outcome. A complete body is success, an inbound or Jul-owned
// cancellation is neutral, and a backend-owned read/write error is failure.
// Closing an incomplete body without another error is a neutral local decision.
// complete is invoked exactly once.
//
// Protocol upgrades expose io.ReadWriteCloser; the returned wrapper preserves
// that interface so HTTP tunnel and WebSocket splicing keep working.
func WrapAttemptBody(body io.ReadCloser, expected int64, inbound, attempt context.Context, complete func(AttemptClassification, error)) io.ReadCloser {
	ab := &attemptBody{
		ReadCloser: body,
		expected:   expected,
		inbound:    inbound,
		attempt:    attempt,
		complete:   complete,
	}
	if rw, ok := body.(io.ReadWriteCloser); ok {
		return &attemptRWBody{attemptBody: ab, w: rw}
	}
	return ab
}

type attemptBody struct {
	io.ReadCloser
	expected int64
	read     atomic.Int64
	once     sync.Once
	inbound  context.Context
	attempt  context.Context
	complete func(AttemptClassification, error)
}

func (b *attemptBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	seen := b.read.Add(int64(n))
	switch {
	case errors.Is(err, io.EOF) && b.expected >= 0 && seen < b.expected:
		// EOF before the declared body length is a backend truncation, not a
		// successful response. Preserve that error for the proxy's copy loop.
		err = io.ErrUnexpectedEOF
		b.finish(ClassifyAttemptError(err, b.inbound, b.attempt), err)
	case errors.Is(err, io.EOF), b.expected >= 0 && seen >= b.expected:
		b.finish(SuccessfulAttempt(), nil)
	case err != nil:
		b.finish(ClassifyAttemptError(err, b.inbound, b.attempt), err)
	}
	return n, err
}

func (b *attemptBody) Close() error {
	err := b.ReadCloser.Close()
	switch {
	case b.expected >= 0 && b.read.Load() >= b.expected:
		b.finish(SuccessfulAttempt(), nil)
	case b.inbound != nil && b.inbound.Err() != nil:
		b.finish(ClassifyAttemptError(b.inbound.Err(), b.inbound, b.attempt), b.inbound.Err())
	case b.attempt != nil && b.attempt.Err() != nil:
		b.finish(ClassifyAttemptError(b.attempt.Err(), b.inbound, b.attempt), b.attempt.Err())
	case err != nil:
		b.finish(ClassifyAttemptError(err, b.inbound, b.attempt), err)
	default:
		b.finish(JulPolicyFailure(""), nil)
	}
	return err
}

func (b *attemptBody) finish(classification AttemptClassification, err error) {
	b.once.Do(func() { b.complete(classification, err) })
}

type attemptRWBody struct {
	*attemptBody
	w io.Writer
}

func (b *attemptRWBody) Write(p []byte) (int, error) {
	n, err := b.w.Write(p)
	if err != nil {
		b.finish(ClassifyAttemptError(err, b.inbound, b.attempt), err)
	}
	return n, err
}
