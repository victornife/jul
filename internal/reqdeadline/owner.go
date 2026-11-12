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
	"fmt"
	"io"
	"net"
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

// SupportReporter is implemented by a writer that forwards SetReadDeadline to
// another writer, so the presence of the method alone does not prove that a
// deadline will be applied. A writer reporting false is treated as having no
// read deadline at all.
type SupportReporter interface {
	SupportsReadDeadline() bool
}

// WriterSupports reports whether w, or the writer it unwraps to, applies read
// deadlines. Forwarding writers use it to implement SupportReporter.
func WriterSupports(w http.ResponseWriter) bool { return findSetter(w) != nil }

// BodyResult is the outcome of a body-arrival window.
type BodyResult uint8

const (
	// BodyComplete means no enforced deadline expired; the read's own
	// result stands.
	BodyComplete BodyResult = iota
	// BodyTimedOut means the enforced deadline expired and was applied;
	// the caller answers 408 whatever the read returned.
	BodyTimedOut
	// BodyUnsupported means the enforced deadline expired but the writer
	// refused the abort; the read was interrupted instead and the caller
	// fails closed with 500.
	BodyUnsupported
)

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
	// refused records that the writer rejected an abort.
	refused bool
	// interrupt ends the current read when an abort is refused. It is set
	// only inside a body-arrival window and used at most once.
	interrupt   io.Closer
	interrupted bool
	// conn is the HTTP/1 request's connection, when the server registered it
	// with ConnContext. It replaces interrupt on HTTP/1, whose body holds its
	// lock during a blocked read, so closing the body cannot end that read.
	conn net.Conn
	// enforceable reports that a deadline can be guaranteed: the writer
	// applies read deadlines, and a refused abort can still end the read
	// through the transport. An HTTP/1 owner needs a registered connection.
	enforceable bool

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
// the Proto* labels. An HTTP/1 owner built by New has no registered
// connection, so its deadlines are unsupported; use NewForRequest.
func New(w http.ResponseWriter, proto string) *Owner {
	return build(w, proto, nil)
}

// NewForRequest returns the owner for r served through w. On HTTP/1 it takes
// the request's connection, if the server registered it with ConnContext, as
// the transport-level interrupt for a refused abort. Without one, nothing can
// end a blocked HTTP/1 read whose abort is refused, so the owner reports its
// deadlines as unsupported: an enforced body deadline fails closed before
// reading.
func NewForRequest(w http.ResponseWriter, r *http.Request) *Owner {
	conn, _ := r.Context().Value(connKey{}).(net.Conn)
	return build(w, ProtocolOf(r), conn)
}

func build(w http.ResponseWriter, proto string, conn net.Conn) *Owner {
	o := &Owner{setter: findSetter(w), proto: proto}
	if proto == ProtoH1 {
		o.conn = conn
	}
	o.enforceable = o.setter != nil && (proto != ProtoH1 || conn != nil)
	return o
}

type connKey struct{}

// ConnContext records c as the connection of the requests served on it. It
// has the signature of http.Server.ConnContext; only HTTP/1 owners use it.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
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
			if r, ok := w.(SupportReporter); ok && !r.SupportsReadDeadline() {
				return nil
			}
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

// Supported reports whether the owner can guarantee a read deadline: the
// writer applies read deadlines and, on HTTP/1, a connection is registered.
func (o *Owner) Supported() bool { return o.enforceable }

// Protocol returns the owner's protocol label.
func (o *Owner) Protocol() string { return o.proto }

// Abort ends every current and future body read of this request by applying a
// past read deadline. It is sticky: nothing the owner does afterwards sets a
// later deadline. When the writer cannot apply the deadline it returns an
// error matching ErrUnsupported, and a read in progress is interrupted through
// the closer given to InterruptWith instead. After End it does nothing and
// returns ErrEnded.
func (o *Owner) Abort() error {
	o.mu.Lock()
	if o.ended {
		o.mu.Unlock()
		return ErrEnded
	}
	err := o.abortLocked()
	c := o.takeInterruptLocked(err)
	o.mu.Unlock()
	closeInterrupt(c)
	return err
}

func (o *Owner) abortLocked() error {
	o.aborted = true
	if o.setter == nil {
		o.refused = true
		return ErrUnsupported
	}
	// A past deadline never blocks. HTTP/1 sets the connection deadline,
	// HTTP/2 closes the stream's body pipe directly, and HTTP/3 sets the
	// stream deadline.
	if err := o.setter.SetReadDeadline(pastDeadline); err != nil {
		o.refused = true
		return fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	return nil
}

// InterruptWith registers c to end the current body read if an abort is
// refused, typically the request body itself: closing it unblocks a pending
// read on HTTP/2 and HTTP/3. It applies to the window opened by the next
// BeginBodyRead and is dropped by EndBodyRead. It is called before
// BeginBodyRead, and c is closed at most once, never under the owner lock.
//
// An HTTP/1 owner never closes c: a net/http HTTP/1 body holds its lock while
// a read blocks, so closing it would wait for the read instead of ending it.
// The registered connection's own past read deadline ends the read instead,
// or closing the connection if it refuses that too. An HTTP/1 owner without a
// registered connection is unsupported, so an enforced read never starts.
func (o *Owner) InterruptWith(c io.Closer) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.interrupt = c
}

// takeInterruptLocked interrupts the current read after a refused abort and
// returns any closer still to run outside the lock, or nil. The HTTP/1
// connection deadline is applied under the lock, inside the read window, so
// it can never reach a later request on the same connection.
func (o *Owner) takeInterruptLocked(abortErr error) io.Closer {
	if abortErr == nil || !o.reading || o.interrupted {
		return nil
	}
	if o.proto == ProtoH1 {
		if o.conn == nil {
			return nil
		}
		o.interrupted = true
		if o.conn.SetReadDeadline(pastDeadline) == nil {
			return nil
		}
		return o.conn
	}
	if o.interrupt == nil {
		return nil
	}
	o.interrupted = true
	return o.interrupt
}

func closeInterrupt(c io.Closer) {
	if c != nil {
		_ = c.Close()
	}
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
// In enforce mode, when the owner cannot guarantee a read deadline (the writer
// does not apply one, or an HTTP/1 connection is not registered), it records
// result unsupported once and fails with ErrUnsupported before any read. In observe mode the
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
	if o.aborted && o.refused {
		// An abort was refused before this window opened, so nothing
		// would end the read.
		o.reading = false
		return ErrUnsupported
	}
	if o.timeout <= 0 || o.bodyDone {
		return nil
	}
	if !o.enforceable {
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
	o.timer = nil
	if o.ended || o.bodyDone || !o.reading || o.expired {
		o.mu.Unlock()
		return
	}
	c := o.takeInterruptLocked(o.expireLocked())
	o.mu.Unlock()
	// Closing may block (a TLS close notify), so it runs outside the lock.
	closeInterrupt(c)
}

// expireLocked marks the enforced deadline as passed and applies the abort. It
// returns the abort error when the writer refused it.
func (o *Owner) expireLocked() error {
	o.expired = true
	if o.observe || !o.enforceable {
		o.recordLocked(recWouldTimeout, ResultWouldTimeout)
		return nil
	}
	if err := o.abortLocked(); err != nil {
		// The writer refused the abort, so the deadline cannot be
		// enforced: the read is interrupted and the request fails closed.
		o.recordLocked(recUnsupported, ResultUnsupported)
		return err
	}
	o.recordLocked(recTimeout, ResultTimeout)
	return nil
}

// EndBodyRead closes the window opened by BeginBodyRead and ends the
// body-arrival phase, whether the read completed or failed. A later
// BeginBodyRead never arms another timer. A timer callback that is already
// running but has not yet taken the owner lock finds the phase done and does
// nothing. Expiry is judged against the absolute deadline, not only by whether
// the callback ran, so a delayed callback cannot let an over-deadline read
// through. When the enforced deadline expired it reports BodyTimedOut, or
// BodyUnsupported if the writer refused the abort; the caller then answers 408
// or 500 whatever the read returned.
func (o *Owner) EndBodyRead() BodyResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.reading && !o.bodyDone && !o.expired && o.timeout > 0 && !o.ended && !time.Now().Before(o.deadline) {
		// The read has already returned, so there is nothing to interrupt.
		_ = o.expireLocked()
	}
	o.reading = false
	o.bodyDone = true
	o.interrupt = nil
	if o.timer != nil {
		o.timer.Stop()
		o.timer = nil
	}
	switch {
	case !o.expired || o.observe || !o.enforceable:
		return BodyComplete
	case o.refused:
		return BodyUnsupported
	default:
		return BodyTimedOut
	}
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
