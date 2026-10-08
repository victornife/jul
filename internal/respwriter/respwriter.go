// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package respwriter builds http.ResponseWriter wrappers that stay transparent
// to the optional interfaces the underlying writer implements.
//
// A middleware that wraps the response writer to observe or transform the
// response hides whatever optional interfaces the real writer offered. The two
// usual answers are both wrong:
//
//   - implementing nothing loses Flush (SSE stalls) and Hijack (a WebSocket
//     upgrade cannot take the connection);
//   - implementing everything and returning "unsupported" is worse, because a
//     type assertion that should fail now succeeds. Code branches on the
//     assertion, not on the error: on HTTP/2, where hijacking genuinely does not
//     exist, a handler is told it can hijack and only finds out afterwards.
//
// Wrap gives the third answer: it inspects the underlying writer once and
// returns a value implementing exactly the optional interfaces that writer
// implements — no more and no fewer — so an assertion against the wrapper has
// the same result it would have had against the writer itself.
package respwriter

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

type sendTimeoutContextKey struct{}

type sendTimeoutConnection struct {
	net.Conn
	mu               sync.Mutex
	timeout          time.Duration
	absoluteDeadline time.Time
}

func WithSendTimeoutConnection(connection net.Conn) net.Conn {
	return &sendTimeoutConnection{Conn: connection}
}

func SendTimeoutContext(ctx context.Context, connection net.Conn) context.Context {
	for {
		if tracked, ok := connection.(*sendTimeoutConnection); ok {
			return context.WithValue(ctx, sendTimeoutContextKey{}, tracked)
		}
		inner, ok := connection.(interface{ NetConn() net.Conn })
		if !ok {
			return ctx
		}
		connection = inner.NetConn()
	}
}

func SendTimeoutConnState(connection net.Conn, state http.ConnState) {
	if state != http.StateIdle && state != http.StateHijacked && state != http.StateClosed {
		return
	}
	ctx := SendTimeoutContext(context.Background(), connection)
	if tracked, ok := ctx.Value(sendTimeoutContextKey{}).(*sendTimeoutConnection); ok {
		tracked.mu.Lock()
		tracked.timeout = 0
		tracked.mu.Unlock()
	}
}

func FinishRequestSendTimeout(writer http.ResponseWriter, request *http.Request, timeout time.Duration, absolute time.Time) {
	if _, ok := request.Context().Value(sendTimeoutContextKey{}).(*sendTimeoutConnection); ok && request.ProtoMajor == 1 {
		_ = http.NewResponseController(writer).Flush()
		return
	}
	FinishSendTimeout(writer, timeout, absolute)
}

func (connection *sendTimeoutConnection) SetWriteDeadline(deadline time.Time) error {
	connection.mu.Lock()
	connection.absoluteDeadline = deadline
	connection.mu.Unlock()
	return connection.Conn.SetWriteDeadline(deadline)
}

func (connection *sendTimeoutConnection) Write(body []byte) (int, error) {
	connection.mu.Lock()
	timeout, absolute := connection.timeout, connection.absoluteDeadline
	connection.mu.Unlock()
	if timeout <= 0 {
		return connection.Conn.Write(body)
	}
	defer func() { _ = connection.Conn.SetWriteDeadline(absolute) }()
	interval := timeout / 8
	if interval <= 0 {
		interval = timeout
	}
	if interval > 25*time.Millisecond {
		interval = 25 * time.Millisecond
	}
	lastProgress := time.Now()
	written := 0
	for written < len(body) {
		deadline := lastProgress.Add(timeout)
		if !absolute.IsZero() && absolute.Before(deadline) {
			deadline = absolute
		}
		pollDeadline := time.Now().Add(interval)
		if deadline.Before(pollDeadline) {
			pollDeadline = deadline
		}
		if err := connection.Conn.SetWriteDeadline(pollDeadline); err != nil {
			return written, err
		}
		count, err := connection.Conn.Write(body[written:])
		written += count
		if count > 0 {
			lastProgress = time.Now()
		}
		if err == nil {
			if count == 0 {
				return written, io.ErrNoProgress
			}
			continue
		}
		var networkError net.Error
		if !errors.As(err, &networkError) || !networkError.Timeout() {
			return written, err
		}
		if (!absolute.IsZero() && !time.Now().Before(absolute)) || time.Since(lastProgress) >= timeout {
			return written, err
		}
	}
	return written, nil
}

func WithRequestSendTimeout(writer http.ResponseWriter, request *http.Request, timeout time.Duration, absolute time.Time) http.ResponseWriter {
	if connection, ok := request.Context().Value(sendTimeoutContextKey{}).(*sendTimeoutConnection); ok && request.ProtoMajor == 1 {
		connection.mu.Lock()
		connection.timeout = timeout
		connection.mu.Unlock()
		return writer
	}
	return WithSendTimeout(writer, timeout, absolute)
}

type sendTimeoutListener struct{ net.Listener }

func WithSendTimeoutListener(listener net.Listener) net.Listener {
	return sendTimeoutListener{listener}
}

func (listener sendTimeoutListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return WithSendTimeoutConnection(connection), nil
}

func WithSendTimeout(under http.ResponseWriter, timeout time.Duration, absoluteDeadline time.Time) http.ResponseWriter {
	if timeout <= 0 {
		return under
	}
	inner := &sendTimeoutWriter{ResponseWriter: under, controller: http.NewResponseController(under), timeout: timeout, absoluteDeadline: absoluteDeadline}
	return Wrap(inner, under)
}

type sendTimeoutWriter struct {
	http.ResponseWriter
	controller       *http.ResponseController
	timeout          time.Duration
	absoluteDeadline time.Time
	hijacked         bool
}

func (writer *sendTimeoutWriter) refreshDeadline() error {
	deadline := time.Now().Add(writer.timeout)
	if !writer.absoluteDeadline.IsZero() && writer.absoluteDeadline.Before(deadline) {
		deadline = writer.absoluteDeadline
	}
	return writer.setDeadline(deadline)
}

func (writer *sendTimeoutWriter) setDeadline(deadline time.Time) error {
	err := writer.controller.SetWriteDeadline(deadline)
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (writer *sendTimeoutWriter) Write(body []byte) (int, error) {
	if err := writer.refreshDeadline(); err != nil {
		return 0, err
	}
	written, err := writer.ResponseWriter.Write(body)
	restoreErr := writer.setDeadline(writer.absoluteDeadline)
	if err != nil {
		return written, err
	}
	return written, restoreErr
}

func (writer *sendTimeoutWriter) WriteHeader(status int) {
	if writer.refreshDeadline() != nil {
		return
	}
	writer.ResponseWriter.WriteHeader(status)
	_ = writer.setDeadline(writer.absoluteDeadline)
}

func FinishSendTimeout(writer http.ResponseWriter, timeout time.Duration, absoluteDeadline time.Time) {
	controller := http.NewResponseController(writer)
	if err := controller.Flush(); err != nil {
		return
	}
	deadline := time.Now().Add(timeout)
	if !absoluteDeadline.IsZero() && absoluteDeadline.Before(deadline) {
		deadline = absoluteDeadline
	}
	_ = controller.SetWriteDeadline(deadline)
}

func (writer *sendTimeoutWriter) Flush() {
	_ = writer.FlushError()
}

func (writer *sendTimeoutWriter) FlushError() error {
	if writer.hijacked {
		return http.ErrHijacked
	}
	if err := writer.refreshDeadline(); err != nil {
		return err
	}
	err := writer.controller.Flush()
	restoreErr := writer.setDeadline(writer.absoluteDeadline)
	if err != nil {
		return err
	}
	return restoreErr
}

func (writer *sendTimeoutWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, buffer, err := writer.controller.Hijack()
	if err == nil {
		writer.hijacked = true
	}
	return connection, buffer, err
}

// Wrap returns a writer that serves Header, Write and WriteHeader from inner,
// exposes Unwrap so http.ResponseController can reach under, and implements
// exactly the optional interfaces (http.Flusher, http.Hijacker, http.Pusher,
// io.ReaderFrom) that under implements.
//
// Each optional call is served by inner when inner implements the same
// interface, so a wrapper can still intercept a flush or a hijack; otherwise the
// call is delegated straight to under.
//
// inner is typically a struct embedding under, so Header falls through
// automatically. Passing a nil inner or under is a programming error.
func Wrap(inner, under http.ResponseWriter) http.ResponseWriter {
	c := core{ResponseWriter: inner, under: under}
	var mask int
	if _, ok := under.(http.Flusher); ok {
		mask |= capFlush
	}
	if _, ok := under.(http.Hijacker); ok {
		mask |= capHijack
	}
	if _, ok := under.(http.Pusher); ok {
		mask |= capPush
	}
	if _, ok := under.(io.ReaderFrom); ok {
		mask |= capReadFrom
	}
	return variants[mask](c)
}

const (
	capFlush = 1 << iota
	capHijack
	capPush
	capReadFrom
)

// core holds the two writers and implements the shared delegation rules. It is
// embedded by every variant; the variants exist only to control which optional
// interfaces the returned value satisfies.
type core struct {
	http.ResponseWriter                     // inner
	under               http.ResponseWriter // the writer whose capabilities are mirrored
}

// Unwrap lets http.ResponseController traverse to the real writer, so
// capabilities without a classic interface (read/write deadlines, full-duplex)
// keep working through the wrapper.
func (c core) Unwrap() http.ResponseWriter { return c.under }

func (c core) flush() {
	_ = c.flushError()
}

func (c core) flushError() error {
	if f, ok := c.ResponseWriter.(interface{ FlushError() error }); ok {
		return f.FlushError()
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
		return nil
	}
	return http.NewResponseController(c.under).Flush()
}

func (c core) hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := c.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	if h, ok := c.under.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (c core) push(target string, opts *http.PushOptions) error {
	if p, ok := c.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	if p, ok := c.under.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// readFrom never hands the reader to the underlying writer's ReadFrom: inner
// exists precisely to observe or transform the bytes, and a direct hand-off
// would skip it. The copy therefore runs through inner's Write unless inner
// implements ReaderFrom itself.
func (c core) readFrom(r io.Reader) (int64, error) {
	if rf, ok := c.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(writeOnly{c.ResponseWriter}, r)
}

// writeOnly hides every method except Write so io.Copy cannot rediscover a
// ReaderFrom and recurse.
type writeOnly struct{ io.Writer }

type (
	w0    struct{ core }
	wF    struct{ core }
	wH    struct{ core }
	wFH   struct{ core }
	wP    struct{ core }
	wFP   struct{ core }
	wHP   struct{ core }
	wFHP  struct{ core }
	wR    struct{ core }
	wFR   struct{ core }
	wHR   struct{ core }
	wFHR  struct{ core }
	wPR   struct{ core }
	wFPR  struct{ core }
	wHPR  struct{ core }
	wFHPR struct{ core }
)

func (w wF) Flush()    { w.flush() }
func (w wFH) Flush()   { w.flush() }
func (w wFP) Flush()   { w.flush() }
func (w wFHP) Flush()  { w.flush() }
func (w wFR) Flush()   { w.flush() }
func (w wFHR) Flush()  { w.flush() }
func (w wFPR) Flush()  { w.flush() }
func (w wFHPR) Flush() { w.flush() }

func (w wF) FlushError() error    { return w.flushError() }
func (w wFH) FlushError() error   { return w.flushError() }
func (w wFP) FlushError() error   { return w.flushError() }
func (w wFHP) FlushError() error  { return w.flushError() }
func (w wFR) FlushError() error   { return w.flushError() }
func (w wFHR) FlushError() error  { return w.flushError() }
func (w wFPR) FlushError() error  { return w.flushError() }
func (w wFHPR) FlushError() error { return w.flushError() }

func (w wH) Hijack() (net.Conn, *bufio.ReadWriter, error)    { return w.hijack() }
func (w wFH) Hijack() (net.Conn, *bufio.ReadWriter, error)   { return w.hijack() }
func (w wHP) Hijack() (net.Conn, *bufio.ReadWriter, error)   { return w.hijack() }
func (w wFHP) Hijack() (net.Conn, *bufio.ReadWriter, error)  { return w.hijack() }
func (w wHR) Hijack() (net.Conn, *bufio.ReadWriter, error)   { return w.hijack() }
func (w wFHR) Hijack() (net.Conn, *bufio.ReadWriter, error)  { return w.hijack() }
func (w wHPR) Hijack() (net.Conn, *bufio.ReadWriter, error)  { return w.hijack() }
func (w wFHPR) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.hijack() }

func (w wP) Push(t string, o *http.PushOptions) error    { return w.push(t, o) }
func (w wFP) Push(t string, o *http.PushOptions) error   { return w.push(t, o) }
func (w wHP) Push(t string, o *http.PushOptions) error   { return w.push(t, o) }
func (w wFHP) Push(t string, o *http.PushOptions) error  { return w.push(t, o) }
func (w wPR) Push(t string, o *http.PushOptions) error   { return w.push(t, o) }
func (w wFPR) Push(t string, o *http.PushOptions) error  { return w.push(t, o) }
func (w wHPR) Push(t string, o *http.PushOptions) error  { return w.push(t, o) }
func (w wFHPR) Push(t string, o *http.PushOptions) error { return w.push(t, o) }

func (w wR) ReadFrom(r io.Reader) (int64, error)    { return w.readFrom(r) }
func (w wFR) ReadFrom(r io.Reader) (int64, error)   { return w.readFrom(r) }
func (w wHR) ReadFrom(r io.Reader) (int64, error)   { return w.readFrom(r) }
func (w wFHR) ReadFrom(r io.Reader) (int64, error)  { return w.readFrom(r) }
func (w wPR) ReadFrom(r io.Reader) (int64, error)   { return w.readFrom(r) }
func (w wFPR) ReadFrom(r io.Reader) (int64, error)  { return w.readFrom(r) }
func (w wHPR) ReadFrom(r io.Reader) (int64, error)  { return w.readFrom(r) }
func (w wFHPR) ReadFrom(r io.Reader) (int64, error) { return w.readFrom(r) }

// variants maps a capability mask to the wrapper type that implements exactly
// those interfaces. The index is the bitwise OR of the cap* constants.
var variants = [16]func(core) http.ResponseWriter{
	0:                                  func(c core) http.ResponseWriter { return w0{c} },
	capFlush:                           func(c core) http.ResponseWriter { return wF{c} },
	capHijack:                          func(c core) http.ResponseWriter { return wH{c} },
	capFlush | capHijack:               func(c core) http.ResponseWriter { return wFH{c} },
	capPush:                            func(c core) http.ResponseWriter { return wP{c} },
	capFlush | capPush:                 func(c core) http.ResponseWriter { return wFP{c} },
	capHijack | capPush:                func(c core) http.ResponseWriter { return wHP{c} },
	capFlush | capHijack | capPush:     func(c core) http.ResponseWriter { return wFHP{c} },
	capReadFrom:                        func(c core) http.ResponseWriter { return wR{c} },
	capFlush | capReadFrom:             func(c core) http.ResponseWriter { return wFR{c} },
	capHijack | capReadFrom:            func(c core) http.ResponseWriter { return wHR{c} },
	capFlush | capHijack | capReadFrom: func(c core) http.ResponseWriter { return wFHR{c} },
	capPush | capReadFrom:              func(c core) http.ResponseWriter { return wPR{c} },
	capFlush | capPush | capReadFrom:   func(c core) http.ResponseWriter { return wFPR{c} },
	capHijack | capPush | capReadFrom:  func(c core) http.ResponseWriter { return wHPR{c} },
	capFlush | capHijack | capPush | capReadFrom: func(c core) http.ResponseWriter { return wFHPR{c} },
}
