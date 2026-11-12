// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package reqdeadline owns the read deadlines Jul applies to a request on top
// of the transport's own server deadline (ADR 0032 H6).
//
// There is one Owner per request or stream, and it never sets a future
// deadline. The server deadline (the HTTP/1 connection read deadline, or the
// HTTP/2 stream timer) stays exactly as the transport installed it, so it is
// never extended, cleared or restored. Jul applies its own deadlines as a
// sticky abort: a past read deadline on the request's own transport writer.
// The abort happens when the inspection body-arrival deadline expires, or when
// a retirement abort is requested. The effective deadline is therefore the
// earliest of the server deadline, the body deadline and any abort, and
// nothing can undo an abort.
package reqdeadline

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Protocol labels, matching jul_inspection_body_timeouts_total{protocol}.
const (
	ProtoH1 = "h1"
	ProtoH2 = "h2"
	ProtoH3 = "h3"
)

// Body-arrival results, matching jul_inspection_body_timeouts_total{result}.
const (
	ResultTimeout      = "timeout"
	ResultWouldTimeout = "would_timeout"
	ResultUnsupported  = "unsupported"
)

var (
	// ErrUnsupported reports that the request's writer cannot apply a read
	// deadline, so an enforced body deadline cannot be guaranteed.
	ErrUnsupported = errors.New("reqdeadline: read deadline unsupported")
	// ErrEnded reports use of an owner whose request handler has returned.
	ErrEnded = errors.New("reqdeadline: request ended")
)

// pastDeadline is the deadline every abort applies.
var pastDeadline = time.Unix(1, 0)

type readDeadliner interface {
	SetReadDeadline(time.Time) error
}

// Recorder receives each body-arrival result at most once per request.
type Recorder func(protocol, result string)

type recordedSet uint8

const (
	recTimeout recordedSet = 1 << iota
	recWouldTimeout
	recUnsupported
)

type stopper interface{ Stop() bool }

// Owner coordinates the read deadline of one request or stream.
type Owner struct {
	mu     sync.Mutex
	setter readDeadliner
	proto  string
	record Recorder

	ended   bool
	aborted bool

	// The body-arrival phase. Its deadline is fixed by the first
	// BeginBodyRead and never refreshed.
	bodyStarted bool
	bodyDone    bool
	reading     bool
	observe     bool
	timeout     time.Duration
	deadline    time.Time
	timer       stopper
	expired     bool
	recorded    recordedSet

	// afterFunc is a test seam for the body timer.
	afterFunc func(time.Duration, func()) stopper
}

// New returns the owner for a request served through w. w is the transport's
// own writer, or a writer that forwards SetReadDeadline to it. proto is one of
// the Proto* labels.
func New(w http.ResponseWriter, proto string) *Owner {
	return &Owner{setter: findSetter(w), proto: proto}
}

// ProtocolOf returns the protocol label for r.
func ProtocolOf(r *http.Request) string {
	switch r.ProtoMajor {
	case 2:
		return ProtoH2
	case 3:
		return ProtoH3
	default:
		return ProtoH1
	}
}

// findSetter walks w's Unwrap chain the way http.ResponseController does, and
// returns the first writer that applies read deadlines.
func findSetter(w http.ResponseWriter) readDeadliner {
	for w != nil {
		if s, ok := w.(readDeadliner); ok {
			return s
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = u.Unwrap()
	}
	return nil
}

type ctxKey struct{}

// With returns ctx carrying o.
func With(ctx context.Context, o *Owner) context.Context {
	return context.WithValue(ctx, ctxKey{}, o)
}

// From returns the owner carried by ctx, or nil.
func From(ctx context.Context) *Owner {
	o, _ := ctx.Value(ctxKey{}).(*Owner)
	return o
}

// Supported reports whether the request's writer applies read deadlines.
func (o *Owner) Supported() bool { return o.setter != nil }

// Protocol returns the owner's protocol label.
func (o *Owner) Protocol() string { return o.proto }

// Abort ends every current and future body read of this request by applying a
// past read deadline. It is sticky: nothing the owner does afterwards sets a
// later deadline. It returns ErrUnsupported when the writer cannot apply the
// deadline. After End it does nothing and returns ErrEnded.
func (o *Owner) Abort() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ended {
		return ErrEnded
	}
	return o.abortLocked()
}

func (o *Owner) abortLocked() error {
	o.aborted = true
	if o.setter == nil {
		return ErrUnsupported
	}
	// A past deadline never blocks. HTTP/1 sets the connection deadline,
	// HTTP/2 closes the stream's body pipe directly, and HTTP/3 sets the
	// stream deadline.
	return o.setter.SetReadDeadline(pastDeadline)
}

// Aborted reports whether a sticky abort has been requested.
func (o *Owner) Aborted() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.aborted
}

// BeginBodyRead opens a read window of the inspection body-arrival phase. The
// absolute deadline start+timeout is fixed by the first call for the request.
// Later calls (another plugin, a retry, a replayed read) never refresh it. A
// timeout <= 0 applies no inspection deadline. In observe mode the deadline is
// not applied, and a read that outlasts it is recorded once as would_timeout.
//
// In enforce mode, when the writer cannot apply a read deadline, it records
// result unsupported once and fails with ErrUnsupported. In observe mode the
// same condition is recorded and the read continues. record, which may be nil,
// receives each body-arrival result at most once for the request; the first
// call's recorder is kept.
func (o *Owner) BeginBodyRead(timeout time.Duration, observe bool, record Recorder) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ended {
		return ErrEnded
	}
	if !o.bodyStarted {
		o.bodyStarted = true
		if record != nil {
			o.record = record
		}
		o.timeout = timeout
		o.observe = observe
		if timeout > 0 {
			o.deadline = time.Now().Add(timeout)
		}
	}
	o.reading = true
	if o.timeout <= 0 || o.bodyDone {
		return nil
	}
	if o.setter == nil {
		o.recordLocked(recUnsupported, ResultUnsupported)
		if !o.observe {
			o.reading = false
			return ErrUnsupported
		}
	}
	if o.timer == nil && !o.expired {
		after := o.afterFunc
		if after == nil {
			after = func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }
		}
		o.timer = after(time.Until(o.deadline), o.fire)
	}
	return nil
}

// fire runs when the body-arrival deadline passes.
func (o *Owner) fire() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.timer = nil
	if o.ended || o.bodyDone || !o.reading || o.expired {
		return
	}
	o.expireLocked()
}

func (o *Owner) expireLocked() {
	o.expired = true
	if o.observe || o.setter == nil {
		o.recordLocked(recWouldTimeout, ResultWouldTimeout)
		return
	}
	o.recordLocked(recTimeout, ResultTimeout)
	if o.abortLocked() != nil {
		// The writer refused the abort, so the read is not ended by it.
		// It still answers 408 when it returns, and the refusal is visible.
		o.recordLocked(recUnsupported, ResultUnsupported)
	}
}

// EndBodyRead closes the window opened by BeginBodyRead and ends the
// body-arrival phase, whether the read completed or failed. A later
// BeginBodyRead never arms another timer. A timer callback that is already
// running but has not yet taken the owner lock finds the phase done and does
// nothing. Expiry is judged against the absolute deadline, not only by whether
// the callback ran, so a delayed callback cannot let an over-deadline read
// through. It reports whether the enforced deadline expired; the abort is then
// applied, and the caller answers 408 whatever the read returned.
func (o *Owner) EndBodyRead() (timedOut bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.reading && !o.bodyDone && !o.expired && o.timeout > 0 && !o.ended && !time.Now().Before(o.deadline) {
		o.expireLocked()
	}
	o.reading = false
	o.bodyDone = true
	if o.timer != nil {
		o.timer.Stop()
		o.timer = nil
	}
	return o.expired && !o.observe && o.setter != nil
}

// End marks the request handler as returned. After it, no timer, abort or
// read-deadline call touches the writer, which may no longer be valid.
func (o *Owner) End() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ended = true
	if o.timer != nil {
		o.timer.Stop()
		o.timer = nil
	}
}

func (o *Owner) recordLocked(bit recordedSet, result string) {
	if o.recorded&bit != 0 {
		return
	}
	o.recorded |= bit
	if o.record != nil {
		o.record(o.proto, result)
	}
}
