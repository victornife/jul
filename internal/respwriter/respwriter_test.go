// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package respwriter

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	err       error
}

func (writer *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	writer.deadlines = append(writer.deadlines, deadline)
	return writer.err
}

func TestSendTimeoutRefreshesWritesAndFlushes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		under := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		nested := Wrap(under, under)
		writer := WithSendTimeout(nested, time.Second, time.Time{})
		before := time.Now()
		if _, err := writer.Write([]byte("first")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(250 * time.Millisecond)
		writer.(http.Flusher).Flush()
		time.Sleep(250 * time.Millisecond)
		if _, err := io.Copy(writer, strings.NewReader("second")); err != nil {
			t.Fatal(err)
		}
		want := []time.Time{
			before.Add(time.Second), {},
			before.Add(1250 * time.Millisecond), {},
			before.Add(1500 * time.Millisecond), {},
		}
		if len(under.deadlines) != len(want) || under.Body.String() != "firstsecond" || !under.Flushed {
			t.Fatalf("deadlines=%v body=%q flushed=%v", under.deadlines, under.Body.String(), under.Flushed)
		}
		for index, deadline := range under.deadlines {
			if !deadline.Equal(want[index]) {
				t.Fatalf("deadline[%d]=%v, want %v", index, deadline, want[index])
			}
		}
		if !under.deadlines[4].After(under.deadlines[0]) {
			t.Fatal("later writes did not refresh the deadline")
		}
	})
}

func TestSendTimeoutPreservesAbsoluteDeadline(t *testing.T) {
	under := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	absolute := time.Now().Add(time.Second)
	writer := WithSendTimeout(under, time.Hour, absolute)
	if _, err := writer.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	if len(under.deadlines) != 2 || !under.deadlines[0].Equal(absolute) || !under.deadlines[1].Equal(absolute) {
		t.Fatalf("deadlines=%v, want fixed %v", under.deadlines, absolute)
	}
}

func TestSendTimeoutDisabledAndUnsupported(t *testing.T) {
	under := httptest.NewRecorder()
	if WithSendTimeout(under, 0, time.Time{}) != under {
		t.Fatal("disabled timeout changed the writer")
	}
	writer := WithSendTimeout(under, time.Second, time.Time{})
	if _, err := writer.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	writer.(http.Flusher).Flush()
	if under.Body.String() != "body" || !under.Flushed {
		t.Fatal("unsupported deadline writer lost its response")
	}
}

func TestSendTimeoutDeadlineFailureDoesNotWriteOrFlush(t *testing.T) {
	failure := errors.New("deadline rejected")
	under := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), err: failure}
	writer := WithSendTimeout(under, time.Second, time.Time{})
	if count, err := writer.Write([]byte("body")); count != 0 || !errors.Is(err, failure) {
		t.Fatalf("write=%d,%v", count, err)
	}
	writer.(http.Flusher).Flush()
	if err := http.NewResponseController(writer).Flush(); !errors.Is(err, failure) {
		t.Fatalf("controller flush=%v, want deadline error", err)
	}
	if under.Body.Len() != 0 || under.Flushed {
		t.Fatal("failed deadline allowed output")
	}
}

type sendFailureWriter struct {
	*deadlineRecorder
	failure error
}

func (writer *sendFailureWriter) Write([]byte) (int, error) { return 0, writer.failure }
func (writer *sendFailureWriter) FlushError() error         { return writer.failure }

func TestSendTimeoutPreservesOutputErrors(t *testing.T) {
	failure := errors.New("transport failed")
	under := &sendFailureWriter{deadlineRecorder: &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}, failure: failure}
	writer := WithSendTimeout(under, time.Second, time.Time{})
	if _, err := writer.Write([]byte("body")); !errors.Is(err, failure) {
		t.Fatalf("write=%v", err)
	}
	if err := http.NewResponseController(writer).Flush(); !errors.Is(err, failure) {
		t.Fatalf("flush=%v", err)
	}
}

type sendHijackWriter struct{ *deadlineRecorder }

func (writer *sendHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, nil }

func TestSendTimeoutLeavesHijackedConnectionAlone(t *testing.T) {
	under := &sendHijackWriter{deadlineRecorder: &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}}
	writer := WithSendTimeout(under, time.Second, time.Time{})
	if _, _, err := http.NewResponseController(writer).Hijack(); err != nil {
		t.Fatal(err)
	}
	if err := http.NewResponseController(writer).Flush(); err != nil || len(under.deadlines) != 0 {
		t.Fatalf("post-hijack flush set deadlines: err=%v deadlines=%v", err, under.deadlines)
	}
}

// caps is the capability set of a writer, used both to build a fake underlying
// writer and to assert what the wrapper exposes.
type caps struct {
	flush    bool
	hijack   bool
	push     bool
	readFrom bool
}

func (c caps) String() string {
	var b strings.Builder
	for _, p := range []struct {
		on bool
		s  string
	}{{c.flush, "F"}, {c.hijack, "H"}, {c.push, "P"}, {c.readFrom, "R"}} {
		if p.on {
			b.WriteString(p.s)
		}
	}
	if b.Len() == 0 {
		return "none"
	}
	return b.String()
}

// calls records which optional method the underlying writer actually saw.
type calls struct {
	flush, hijack, push, readFrom int
}

// base is the plain writer every fake builds on.
type base struct {
	http.ResponseWriter
	c *calls
}

func (b base) doFlush()  { b.c.flush++ }
func (b base) doHijack() { b.c.hijack++ }
func (b base) doPush()   { b.c.push++ }

// The 16 fake underlying writers. Each implements exactly the interfaces its
// name spells, which is what makes the assertion matrix meaningful.
type (
	uNone struct{ base }
	uF    struct{ base }
	uH    struct{ base }
	uFH   struct{ base }
	uP    struct{ base }
	uFP   struct{ base }
	uHP   struct{ base }
	uFHP  struct{ base }
	uR    struct{ base }
	uFR   struct{ base }
	uHR   struct{ base }
	uFHR  struct{ base }
	uPR   struct{ base }
	uFPR  struct{ base }
	uHPR  struct{ base }
	uFHPR struct{ base }
)

func hijackResult(b base) (net.Conn, *bufio.ReadWriter, error) {
	b.doHijack()
	return nil, nil, errors.New("fake hijack")
}

func readFromResult(b base, r io.Reader) (int64, error) {
	b.c.readFrom++
	return io.Copy(b.ResponseWriter, r)
}

func (u uF) Flush()    { u.doFlush() }
func (u uFH) Flush()   { u.doFlush() }
func (u uFP) Flush()   { u.doFlush() }
func (u uFHP) Flush()  { u.doFlush() }
func (u uFR) Flush()   { u.doFlush() }
func (u uFHR) Flush()  { u.doFlush() }
func (u uFPR) Flush()  { u.doFlush() }
func (u uFHPR) Flush() { u.doFlush() }

func (u uH) Hijack() (net.Conn, *bufio.ReadWriter, error)    { return hijackResult(u.base) }
func (u uFH) Hijack() (net.Conn, *bufio.ReadWriter, error)   { return hijackResult(u.base) }
func (u uHP) Hijack() (net.Conn, *bufio.ReadWriter, error)   { return hijackResult(u.base) }
func (u uFHP) Hijack() (net.Conn, *bufio.ReadWriter, error)  { return hijackResult(u.base) }
func (u uHR) Hijack() (net.Conn, *bufio.ReadWriter, error)   { return hijackResult(u.base) }
func (u uFHR) Hijack() (net.Conn, *bufio.ReadWriter, error)  { return hijackResult(u.base) }
func (u uHPR) Hijack() (net.Conn, *bufio.ReadWriter, error)  { return hijackResult(u.base) }
func (u uFHPR) Hijack() (net.Conn, *bufio.ReadWriter, error) { return hijackResult(u.base) }

func (u uP) Push(string, *http.PushOptions) error    { u.doPush(); return nil }
func (u uFP) Push(string, *http.PushOptions) error   { u.doPush(); return nil }
func (u uHP) Push(string, *http.PushOptions) error   { u.doPush(); return nil }
func (u uFHP) Push(string, *http.PushOptions) error  { u.doPush(); return nil }
func (u uPR) Push(string, *http.PushOptions) error   { u.doPush(); return nil }
func (u uFPR) Push(string, *http.PushOptions) error  { u.doPush(); return nil }
func (u uHPR) Push(string, *http.PushOptions) error  { u.doPush(); return nil }
func (u uFHPR) Push(string, *http.PushOptions) error { u.doPush(); return nil }

func (u uR) ReadFrom(r io.Reader) (int64, error)    { return readFromResult(u.base, r) }
func (u uFR) ReadFrom(r io.Reader) (int64, error)   { return readFromResult(u.base, r) }
func (u uHR) ReadFrom(r io.Reader) (int64, error)   { return readFromResult(u.base, r) }
func (u uFHR) ReadFrom(r io.Reader) (int64, error)  { return readFromResult(u.base, r) }
func (u uPR) ReadFrom(r io.Reader) (int64, error)   { return readFromResult(u.base, r) }
func (u uFPR) ReadFrom(r io.Reader) (int64, error)  { return readFromResult(u.base, r) }
func (u uHPR) ReadFrom(r io.Reader) (int64, error)  { return readFromResult(u.base, r) }
func (u uFHPR) ReadFrom(r io.Reader) (int64, error) { return readFromResult(u.base, r) }

// newUnderlying builds the fake writer implementing exactly c.
func newUnderlying(c caps, rec *httptest.ResponseRecorder, seen *calls) http.ResponseWriter {
	b := base{ResponseWriter: rec, c: seen}
	switch {
	case !c.flush && !c.hijack && !c.push && !c.readFrom:
		return uNone{b}
	case c.flush && !c.hijack && !c.push && !c.readFrom:
		return uF{b}
	case !c.flush && c.hijack && !c.push && !c.readFrom:
		return uH{b}
	case c.flush && c.hijack && !c.push && !c.readFrom:
		return uFH{b}
	case !c.flush && !c.hijack && c.push && !c.readFrom:
		return uP{b}
	case c.flush && !c.hijack && c.push && !c.readFrom:
		return uFP{b}
	case !c.flush && c.hijack && c.push && !c.readFrom:
		return uHP{b}
	case c.flush && c.hijack && c.push && !c.readFrom:
		return uFHP{b}
	case !c.flush && !c.hijack && !c.push && c.readFrom:
		return uR{b}
	case c.flush && !c.hijack && !c.push && c.readFrom:
		return uFR{b}
	case !c.flush && c.hijack && !c.push && c.readFrom:
		return uHR{b}
	case c.flush && c.hijack && !c.push && c.readFrom:
		return uFHR{b}
	case !c.flush && !c.hijack && c.push && c.readFrom:
		return uPR{b}
	case c.flush && !c.hijack && c.push && c.readFrom:
		return uFPR{b}
	case !c.flush && c.hijack && c.push && c.readFrom:
		return uHPR{b}
	default:
		return uFHPR{b}
	}
}

func allCaps() []caps {
	var out []caps
	for i := 0; i < 16; i++ {
		out = append(out, caps{
			flush:    i&1 != 0,
			hijack:   i&2 != 0,
			push:     i&4 != 0,
			readFrom: i&8 != 0,
		})
	}
	return out
}

// passthrough is an inner wrapper that adds no optional interface of its own,
// so every optional call must reach the underlying writer.
type passthrough struct{ http.ResponseWriter }

// TestWrapMirrorsUnderlyingCapabilities is the interface matrix: for each of the
// 16 possible underlying capability sets, the wrapper must satisfy exactly the
// same type assertions. Advertising more is the defect this package exists to
// prevent; advertising less breaks SSE and WebSocket upgrades.
func TestWrapMirrorsUnderlyingCapabilities(t *testing.T) {
	for _, c := range allCaps() {
		t.Run(c.String(), func(t *testing.T) {
			rec := httptest.NewRecorder()
			var seen calls
			under := newUnderlying(c, rec, &seen)
			got := Wrap(passthrough{under}, under)

			if _, ok := got.(http.Flusher); ok != c.flush {
				t.Errorf("http.Flusher = %v, want %v", ok, c.flush)
			}
			if _, ok := got.(http.Hijacker); ok != c.hijack {
				t.Errorf("http.Hijacker = %v, want %v", ok, c.hijack)
			}
			if _, ok := got.(http.Pusher); ok != c.push {
				t.Errorf("http.Pusher = %v, want %v", ok, c.push)
			}
			if _, ok := got.(io.ReaderFrom); ok != c.readFrom {
				t.Errorf("io.ReaderFrom = %v, want %v", ok, c.readFrom)
			}
		})
	}
}

// TestWrapDelegatesToUnderlying proves the mirrored interfaces are not merely
// declared: each call reaches the underlying writer.
func TestWrapDelegatesToUnderlying(t *testing.T) {
	rec := httptest.NewRecorder()
	var seen calls
	under := newUnderlying(caps{flush: true, hijack: true, push: true, readFrom: true}, rec, &seen)
	got := Wrap(passthrough{under}, under)

	got.(http.Flusher).Flush()
	if _, _, err := got.(http.Hijacker).Hijack(); err == nil {
		t.Error("Hijack error was swallowed")
	}
	if err := got.(http.Pusher).Push("/x", nil); err != nil {
		t.Errorf("Push: %v", err)
	}
	if _, err := got.(io.ReaderFrom).ReadFrom(strings.NewReader("payload")); err != nil {
		t.Errorf("ReadFrom: %v", err)
	}

	if seen.flush != 1 || seen.hijack != 1 || seen.push != 1 {
		t.Errorf("underlying calls = %+v, want one of each", seen)
	}
	if rec.Body.String() != "payload" {
		t.Errorf("body = %q, want payload", rec.Body.String())
	}
}

// intercepting is an inner wrapper that implements every optional interface
// itself, which is how the cache and compression writers observe a flush or take
// over a hijack.
type intercepting struct {
	http.ResponseWriter
	seen *calls
}

func (i intercepting) Flush() { i.seen.flush++ }
func (i intercepting) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	i.seen.hijack++
	return nil, nil, errors.New("inner hijack")
}
func (i intercepting) Push(string, *http.PushOptions) error { i.seen.push++; return nil }
func (i intercepting) ReadFrom(io.Reader) (int64, error)    { i.seen.readFrom++; return 0, nil }

// TestWrapPrefersInnerImplementation proves a wrapper can still intercept: when
// inner implements an optional interface, the call goes to inner and not past it.
func TestWrapPrefersInnerImplementation(t *testing.T) {
	rec := httptest.NewRecorder()
	var underSeen, innerSeen calls
	under := newUnderlying(caps{flush: true, hijack: true, push: true, readFrom: true}, rec, &underSeen)
	got := Wrap(intercepting{ResponseWriter: under, seen: &innerSeen}, under)

	got.(http.Flusher).Flush()
	_, _, _ = got.(http.Hijacker).Hijack()
	_ = got.(http.Pusher).Push("/x", nil)
	_, _ = got.(io.ReaderFrom).ReadFrom(strings.NewReader("x"))

	if innerSeen != (calls{flush: 1, hijack: 1, push: 1, readFrom: 1}) {
		t.Errorf("inner calls = %+v, want one of each", innerSeen)
	}
	if underSeen != (calls{}) {
		t.Errorf("underlying calls = %+v, want none (inner must intercept)", underSeen)
	}
}

// TestReadFromRoutesThroughInnerWrite proves the capture contract: when inner
// does not implement ReaderFrom, the bytes must still pass through its Write
// rather than being handed to the underlying ReaderFrom, which would skip
// observation entirely.
func TestReadFromRoutesThroughInnerWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	var seen calls
	under := newUnderlying(caps{readFrom: true}, rec, &seen)

	var captured []byte
	inner := &capturingWriter{ResponseWriter: under, captured: &captured}
	got := Wrap(inner, under)

	n, err := got.(io.ReaderFrom).ReadFrom(strings.NewReader("hello world"))
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != int64(len("hello world")) {
		t.Errorf("n = %d, want %d", n, len("hello world"))
	}
	if string(captured) != "hello world" {
		t.Errorf("inner captured %q, want the full payload", captured)
	}
	if seen.readFrom != 0 {
		t.Error("bytes bypassed inner via the underlying ReaderFrom")
	}
	if rec.Body.String() != "hello world" {
		t.Errorf("client body = %q", rec.Body.String())
	}
}

type capturingWriter struct {
	http.ResponseWriter
	captured *[]byte
}

func (c *capturingWriter) Write(p []byte) (int, error) {
	*c.captured = append(*c.captured, p...)
	return c.ResponseWriter.Write(p)
}

// TestUnwrapReachesUnderlying proves http.ResponseController can traverse the
// wrapper, which is how the standard library reaches capabilities that have no
// classic interface, and how httputil.ReverseProxy finds Hijack.
func TestUnwrapReachesUnderlying(t *testing.T) {
	rec := httptest.NewRecorder()
	var seen calls
	under := newUnderlying(caps{flush: true}, rec, &seen)
	got := Wrap(passthrough{under}, under)

	u, ok := got.(interface{ Unwrap() http.ResponseWriter })
	if !ok {
		t.Fatal("wrapper does not implement Unwrap")
	}
	if u.Unwrap() != under {
		t.Error("Unwrap did not return the underlying writer")
	}

	// An inner that implements no optional interface still lets the controller
	// flush, because the controller traverses Unwrap.
	if err := http.NewResponseController(got).Flush(); err != nil {
		t.Fatalf("ResponseController.Flush: %v", err)
	}
	if seen.flush == 0 {
		t.Error("ResponseController.Flush did not reach the underlying writer")
	}
}

// TestResponseControllerReportsUnsupported proves the negative case: when the
// underlying writer cannot hijack, neither the assertion nor the controller
// pretends otherwise.
func TestResponseControllerReportsUnsupported(t *testing.T) {
	rec := httptest.NewRecorder()
	var seen calls
	under := newUnderlying(caps{flush: true}, rec, &seen)
	got := Wrap(passthrough{under}, under)

	if _, ok := got.(http.Hijacker); ok {
		t.Fatal("wrapper advertises Hijacker over a non-hijackable writer")
	}
	_, _, err := http.NewResponseController(got).Hijack()
	if !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("ResponseController.Hijack error = %v, want http.ErrNotSupported", err)
	}
}

// TestWrapForwardsMandatoryMethods checks the boring half of the contract.
func TestWrapForwardsMandatoryMethods(t *testing.T) {
	rec := httptest.NewRecorder()
	var seen calls
	under := newUnderlying(caps{}, rec, &seen)
	got := Wrap(passthrough{under}, under)

	got.Header().Set("X-Test", "1")
	got.WriteHeader(http.StatusTeapot)
	if _, err := got.Write([]byte("body")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.Code)
	}
	if rec.Header().Get("X-Test") != "1" {
		t.Error("header did not reach the underlying writer")
	}
	if rec.Body.String() != "body" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

// TestNestedWrapPreservesCapabilities proves composition: the production chain
// stacks several wrappers, and the innermost handler must still see exactly the
// real connection's capabilities.
func TestNestedWrapPreservesCapabilities(t *testing.T) {
	for _, c := range allCaps() {
		t.Run(c.String(), func(t *testing.T) {
			rec := httptest.NewRecorder()
			var seen calls
			under := newUnderlying(c, rec, &seen)

			outer := WithSendTimeout(Wrap(passthrough{under}, under), time.Second, time.Time{})
			middle := Wrap(passthrough{outer}, outer)
			inner := Wrap(passthrough{middle}, middle)

			if _, ok := inner.(http.Flusher); ok != c.flush {
				t.Errorf("nested http.Flusher = %v, want %v", ok, c.flush)
			}
			if _, ok := inner.(http.Hijacker); ok != c.hijack {
				t.Errorf("nested http.Hijacker = %v, want %v", ok, c.hijack)
			}
			if _, ok := inner.(http.Pusher); ok != c.push {
				t.Errorf("nested http.Pusher = %v, want %v", ok, c.push)
			}
			if _, ok := inner.(io.ReaderFrom); ok != c.readFrom {
				t.Errorf("nested io.ReaderFrom = %v, want %v", ok, c.readFrom)
			}
			if c.flush {
				inner.(http.Flusher).Flush()
				if seen.flush != 1 {
					t.Errorf("nested flush reached the underlying writer %d times, want 1", seen.flush)
				}
			}
		})
	}
}
