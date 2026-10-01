// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"errors"
	"io"
	"net/http"
	"sync"
)

// ClientBodyError marks an attempt that failed because the inbound request
// body could not be read: malformed chunked framing, a body over
// client_max_body_size, or an upload the client cut off. It is the client's
// failure. Counting it against the backend let any client take healthy
// backends out of rotation with a few malformed or oversized uploads.
type ClientBodyError struct{ Err error }

func (e *ClientBodyError) Error() string { return "read inbound request body: " + e.Err.Error() }
func (e *ClientBodyError) Unwrap() error { return e.Err }

// InboundBody watches a request body that is streamed to a backend and keeps
// its first read error.
type InboundBody struct {
	io.ReadCloser
	mu  sync.Mutex
	err error
}

// WatchInboundBody replaces req.Body with a watcher and returns it, or returns
// nil when the request has no body.
func WatchInboundBody(req *http.Request) *InboundBody {
	if req.Body == nil || req.Body == http.NoBody {
		return nil
	}
	b := &InboundBody{ReadCloser: req.Body}
	req.Body = b
	return b
}

func (b *InboundBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.mu.Lock()
		if b.err == nil {
			b.err = err
		}
		b.mu.Unlock()
	}
	return n, err
}

// Attribute returns err unchanged unless reading the inbound body failed, in
// which case the attempt failed because of the client and the body error is
// returned as a *ClientBodyError.
func (b *InboundBody) Attribute(err error) error {
	if b == nil || err == nil {
		return err
	}
	b.mu.Lock()
	bodyErr := b.err
	b.mu.Unlock()
	if bodyErr == nil {
		return err
	}
	return &ClientBodyError{Err: bodyErr}
}
