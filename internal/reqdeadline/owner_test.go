// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package reqdeadline

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeWriter records every read deadline the owner applies.
type fakeWriter struct {
	http.ResponseWriter
	mu        sync.Mutex
	deadlines []time.Time
	err       error
}

func (f *fakeWriter) SetReadDeadline(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadlines = append(f.deadlines, t)
	return f.err
}

func (f *fakeWriter) applied() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.deadlines...)
}

// manualTimer lets a test fire the body deadline at a chosen point.
type manualTimer struct {
	mu      sync.Mutex
	d       time.Duration
	f       func()
	stopped bool
	armed   int
}

func (m *manualTimer) after(d time.Duration, f func()) stopper {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.d, m.f, m.stopped = d, f, false
	m.armed++
	return m
}

func (m *manualTimer) Stop() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	was := !m.stopped
	m.stopped = true
	return was
}

func (m *manualTimer) fire() {
	m.mu.Lock()
	f := m.f
	m.mu.Unlock()
	if f != nil {
		f()
	}
}

type results struct {
	mu  sync.Mutex
	got []string
}

func (r *results) record(proto, result string) {
	r.mu.Lock()
	r.got = append(r.got, proto+"/"+result)
	r.mu.Unlock()
}

func (r *results) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

// stubConn is a registered HTTP/1 connection whose deadlines always apply.
type stubConn struct{ net.Conn }

func (*stubConn) SetReadDeadline(time.Time) error { return nil }
func (*stubConn) Close() error                    { return nil }

// h1Owner returns an HTTP/1 owner on w with a registered connection.
func h1Owner(w http.ResponseWriter) *Owner { return build(w, ProtoH1, &stubConn{}) }

func newOwner(t *testing.T, w http.ResponseWriter, proto string) (*Owner, *manualTimer, *results) {
	t.Helper()
	rec := &results{}
	o := build(w, proto, &stubConn{})
	o.record = rec.record
	mt := &manualTimer{}
	o.afterFunc = mt.after
	return o, mt, rec
}

func onlyPast(t *testing.T, ds []time.Time, want int) {
	t.Helper()
	if len(ds) != want {
		t.Fatalf("applied deadlines = %v, want %d", ds, want)
	}
	for _, d := range ds {
		if !d.Equal(pastDeadline) {
			t.Fatalf("owner applied a non-past deadline %v; it must never set a future deadline", d)
		}
	}
}

func TestExpiryAbortsOnceAndNeverSetsAFutureDeadline(t *testing.T) {
	fw := &fakeWriter{}
	o, mt, rec := newOwner(t, fw, ProtoH2)
	if err := o.BeginBodyRead(5*time.Second, false, nil); err != nil {
		t.Fatal(err)
	}
	if mt.d <= 0 || mt.d > 5*time.Second {
		t.Fatalf("timer armed for %v, want (0,5s]", mt.d)
	}
	mt.fire()
	mt.fire() // a duplicate callback must not double count
	if o.EndBodyRead() != BodyTimedOut {
		t.Fatal("EndBodyRead = false after enforced expiry")
	}
	onlyPast(t, fw.applied(), 1)
	if got := rec.list(); len(got) != 1 || got[0] != "h2/timeout" {
		t.Fatalf("results = %v, want [h2/timeout]", got)
	}
	if !o.Aborted() {
		t.Fatal("expiry did not leave a sticky abort")
	}
}

func TestSuccessfulArrivalTouchesNoTransportDeadline(t *testing.T) {
	fw := &fakeWriter{}
	o, mt, rec := newOwner(t, fw, ProtoH1)
	if err := o.BeginBodyRead(time.Minute, false, nil); err != nil {
		t.Fatal(err)
	}
	if o.EndBodyRead() != BodyComplete {
		t.Fatal("EndBodyRead reported a timeout without expiry")
	}
	if !mt.stopped {
		t.Fatal("body timer not stopped at arrival")
	}
	mt.fire() // a timer that raced Stop is a no-op once the phase is done
	o.End()
	onlyPast(t, fw.applied(), 0)
	if got := rec.list(); len(got) != 0 {
		t.Fatalf("results = %v, want none", got)
	}
}

func TestDeadlineFixedAtFirstBeginNeverRefreshed(t *testing.T) {
	fw := &fakeWriter{}
	o, mt, _ := newOwner(t, fw, ProtoH1)
	if err := o.BeginBodyRead(time.Minute, false, nil); err != nil {
		t.Fatal(err)
	}
	first := o.deadline
	if err := o.BeginBodyRead(time.Hour, false, nil); err != nil {
		t.Fatal(err)
	}
	if !o.deadline.Equal(first) || mt.armed != 1 {
		t.Fatalf("deadline refreshed: %v -> %v, armed %d", first, o.deadline, mt.armed)
	}
	o.EndBodyRead()
	// A replayed read after the phase ended arms nothing.
	if err := o.BeginBodyRead(time.Minute, false, nil); err != nil {
		t.Fatal(err)
	}
	if mt.armed != 1 {
		t.Fatalf("timer re-armed after the body phase ended (%d)", mt.armed)
	}
}

func TestDisabledTimeoutArmsNothingButAbortStillWorks(t *testing.T) {
	fw := &fakeWriter{}
	o, mt, rec := newOwner(t, fw, ProtoH3)
	if err := o.BeginBodyRead(0, false, nil); err != nil {
		t.Fatal(err)
	}
	if mt.armed != 0 {
		t.Fatal("body_timeout=0s armed a timer")
	}
	if err := o.Abort(); err != nil {
		t.Fatal(err)
	}
	if o.EndBodyRead() != BodyComplete {
		t.Fatal("abort reported as a body timeout")
	}
	onlyPast(t, fw.applied(), 1)
	if len(rec.list()) != 0 {
		t.Fatal("abort recorded a body-timeout result")
	}
}

func TestObserveRecordsWouldTimeoutOnceWithoutAbort(t *testing.T) {
	fw := &fakeWriter{}
	o, mt, rec := newOwner(t, fw, ProtoH1)
	if err := o.BeginBodyRead(time.Second, true, nil); err != nil {
		t.Fatal(err)
	}
	mt.fire()
	mt.fire()
	if o.EndBodyRead() != BodyComplete {
		t.Fatal("observe mode reported an enforced timeout")
	}
	onlyPast(t, fw.applied(), 0)
	if got := rec.list(); len(got) != 1 || got[0] != "h1/would_timeout" {
		t.Fatalf("results = %v, want [h1/would_timeout]", got)
	}
}

// TestUnregisteredH1IsUnsupported: an HTTP/1 owner without a registered
// connection cannot end a read whose abort is refused, so even a writer that
// applies deadlines is unsupported: enforce fails before any read, observe
// records unsupported and continues, and no deadline is ever probed.
func TestUnregisteredH1IsUnsupported(t *testing.T) {
	for _, observe := range []bool{false, true} {
		fw := &fakeWriter{}
		o := New(fw, ProtoH1)
		rec := &results{}
		o.record = rec.record
		mt := &manualTimer{}
		o.afterFunc = mt.after
		if o.Supported() {
			t.Fatal("unregistered HTTP/1 owner reported as supported")
		}
		err := o.BeginBodyRead(time.Second, observe, nil)
		if observe && err != nil || !observe && !errors.Is(err, ErrUnsupported) {
			t.Fatalf("observe=%v BeginBodyRead = %v", observe, err)
		}
		if got := rec.list(); len(got) != 1 || got[0] != "h1/unsupported" {
			t.Fatalf("observe=%v results = %v, want one unsupported", observe, got)
		}
		if n := len(fw.applied()); n != 0 {
			t.Fatalf("observe=%v applied %d deadlines", observe, n)
		}
		o.EndBodyRead()
	}
}

func TestUnsupportedWriter(t *testing.T) {
	t.Run("enforce fails closed", func(t *testing.T) {
		o, mt, rec := newOwner(t, httptest.NewRecorder(), ProtoH1)
		if o.Supported() {
			t.Fatal("recorder reported as supporting read deadlines")
		}
		if err := o.BeginBodyRead(time.Second, false, nil); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("BeginBodyRead = %v, want ErrUnsupported", err)
		}
		if err := o.BeginBodyRead(time.Second, false, nil); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("second BeginBodyRead = %v", err)
		}
		if mt.armed != 0 {
			t.Fatal("unsupported enforce armed a timer")
		}
		if got := rec.list(); len(got) != 1 || got[0] != "h1/unsupported" {
			t.Fatalf("results = %v, want one unsupported", got)
		}
	})
	t.Run("observe records and continues", func(t *testing.T) {
		o, mt, rec := newOwner(t, httptest.NewRecorder(), ProtoH2)
		if err := o.BeginBodyRead(time.Second, true, nil); err != nil {
			t.Fatalf("observe BeginBodyRead = %v", err)
		}
		mt.fire()
		if o.EndBodyRead() != BodyComplete {
			t.Fatal("observe reported timeout")
		}
		got := rec.list()
		if len(got) != 2 || got[0] != "h2/unsupported" || got[1] != "h2/would_timeout" {
			t.Fatalf("results = %v", got)
		}
	})
	t.Run("disabled timeout needs no support", func(t *testing.T) {
		o, _, rec := newOwner(t, httptest.NewRecorder(), ProtoH1)
		if err := o.BeginBodyRead(0, false, nil); err != nil {
			t.Fatal(err)
		}
		if len(rec.list()) != 0 {
			t.Fatal("disabled timeout recorded unsupported")
		}
	})
	t.Run("abort reports unsupported", func(t *testing.T) {
		o, _, _ := newOwner(t, httptest.NewRecorder(), ProtoH1)
		if err := o.Abort(); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Abort = %v", err)
		}
		if !o.Aborted() {
			t.Fatal("unsupported abort not recorded as sticky")
		}
	})
}

type unwrapper struct {
	http.ResponseWriter
	inner http.ResponseWriter
}

func (u unwrapper) Unwrap() http.ResponseWriter { return u.inner }

func TestFindSetterWalksUnwrap(t *testing.T) {
	fw := &fakeWriter{ResponseWriter: httptest.NewRecorder()}
	o := h1Owner(unwrapper{ResponseWriter: httptest.NewRecorder(), inner: fw})
	if !o.Supported() {
		t.Fatal("setter behind Unwrap not found")
	}
}

func TestEndNeutralisesCallbacks(t *testing.T) {
	fw := &fakeWriter{}
	o, mt, rec := newOwner(t, fw, ProtoH2)
	if err := o.BeginBodyRead(time.Second, false, nil); err != nil {
		t.Fatal(err)
	}
	o.End()
	if !mt.stopped {
		t.Fatal("End did not stop the body timer")
	}
	mt.fire()
	if err := o.Abort(); !errors.Is(err, ErrEnded) {
		t.Fatalf("Abort after End = %v", err)
	}
	if err := o.BeginBodyRead(time.Second, false, nil); !errors.Is(err, ErrEnded) {
		t.Fatalf("BeginBodyRead after End = %v", err)
	}
	onlyPast(t, fw.applied(), 0)
	if len(rec.list()) != 0 {
		t.Fatalf("callback after End recorded %v", rec.list())
	}
}

func TestAbortIsStickyAgainstCompletionAndExpiry(t *testing.T) {
	fw := &fakeWriter{}
	o, mt, _ := newOwner(t, fw, ProtoH1)
	if err := o.BeginBodyRead(time.Second, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := o.Abort(); err != nil {
		t.Fatal(err)
	}
	o.EndBodyRead()
	mt.fire()
	if !o.Aborted() {
		t.Fatal("abort undone by completion")
	}
	// Nothing after the abort may apply a later deadline.
	onlyPast(t, fw.applied(), 1)
}

func TestContextRoundTrip(t *testing.T) {
	if From(context.Background()) != nil {
		t.Fatal("empty context has an owner")
	}
	o := New(nil, ProtoH1)
	if From(With(context.Background(), o)) != o {
		t.Fatal("owner not carried by context")
	}
	for major, want := range map[int]string{1: ProtoH1, 2: ProtoH2, 3: ProtoH3, 0: ProtoH1} {
		if got := ProtocolOf(&http.Request{ProtoMajor: major}); got != want {
			t.Fatalf("ProtocolOf(%d) = %q, want %q", major, got, want)
		}
	}
}

// TestRacingAbortExpiryCompletionAndEnd drives the owner's transitions from
// concurrent goroutines released together. Whatever the order, every applied
// deadline is in the past, at most one timeout is recorded, and nothing
// touches the writer after End returns.
func TestRacingAbortExpiryCompletionAndEnd(t *testing.T) {
	for i := 0; i < 500; i++ {
		fw := &fakeWriter{}
		rec := &results{}
		o := New(fw, ProtoH2)
		o.record = rec.record
		mt := &manualTimer{}
		o.afterFunc = mt.after
		if err := o.BeginBodyRead(time.Second, false, nil); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, f := range []func(){
			mt.fire,
			func() { _ = o.Abort() },
			func() { o.EndBodyRead() },
		} {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; f() }()
		}
		close(start)
		wg.Wait()
		o.End()
		n := len(fw.applied())
		mt.fire()
		_ = o.Abort()
		if len(fw.applied()) != n {
			t.Fatal("writer touched after End")
		}
		onlyPast(t, fw.applied(), n)
		if n == 0 || n > 2 {
			t.Fatalf("applied %d aborts, want 1 or 2", n)
		}
		timeouts := 0
		for _, r := range rec.list() {
			if r == "h2/timeout" {
				timeouts++
			}
		}
		if timeouts > 1 {
			t.Fatalf("recorded %d timeouts", timeouts)
		}
		if !o.Aborted() {
			t.Fatal("abort lost in race")
		}
	}
}

func TestRealTimerExpires(t *testing.T) {
	fw := &fakeWriter{}
	rec := &results{}
	o := h1Owner(fw)
	o.record = rec.record
	if err := o.BeginBodyRead(time.Millisecond, false, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(fw.applied()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if o.EndBodyRead() != BodyTimedOut {
		t.Fatal("real timer did not expire the body phase")
	}
	o.End()
	onlyPast(t, fw.applied(), 1)
}

// TestLateCallbackCannotAdmitOverDeadlineRead models a timer callback that is
// delayed past the deadline: completion itself judges the absolute deadline.
func TestLateCallbackCannotAdmitOverDeadlineRead(t *testing.T) {
	for _, observe := range []bool{false, true} {
		fw := &fakeWriter{}
		o, mt, rec := newOwner(t, fw, ProtoH1)
		if err := o.BeginBodyRead(time.Nanosecond, observe, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
		got := o.EndBodyRead()
		mt.fire() // the delayed callback now runs and must do nothing
		want := "h1/timeout"
		if observe {
			want = "h1/would_timeout"
		}
		if wantRes := map[bool]BodyResult{false: BodyTimedOut, true: BodyComplete}[observe]; got != wantRes {
			t.Fatalf("observe=%v: EndBodyRead = %v", observe, got)
		}
		if r := rec.list(); len(r) != 1 || r[0] != want {
			t.Fatalf("observe=%v: results = %v, want [%s]", observe, r, want)
		}
		if observe {
			onlyPast(t, fw.applied(), 0)
		} else {
			onlyPast(t, fw.applied(), 1)
		}
	}
}

// TestCallbackBlockedBehindCompletionDoesNothing forces the interleaving in
// which the callback has started but completion takes the lock first.
func TestCallbackBlockedBehindCompletionDoesNothing(t *testing.T) {
	fw := &fakeWriter{}
	o, mt, rec := newOwner(t, fw, ProtoH2)
	if err := o.BeginBodyRead(time.Hour, false, nil); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	done := make(chan struct{})
	go func() { mt.fire(); close(done) }()
	// The callback is now blocked on the owner lock. Complete the phase the
	// way EndBodyRead does while it still waits.
	o.reading, o.bodyDone = false, true
	o.mu.Unlock()
	<-done
	if o.EndBodyRead() != BodyComplete {
		t.Fatal("completion reported a timeout")
	}
	onlyPast(t, fw.applied(), 0)
	if len(rec.list()) != 0 {
		t.Fatalf("blocked callback recorded %v", rec.list())
	}
}

// blockingBody models a request body whose Read blocks until Close.
type blockingBody struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingBody() *blockingBody { return &blockingBody{closed: make(chan struct{})} }

func (b *blockingBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, errors.New("body closed")
}

func (b *blockingBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// TestRefusedAbortFailsClosed covers a writer that exposes SetReadDeadline but
// refuses it: expiry must not report a timeout or leave the read blocked.
func TestRefusedAbortFailsClosed(t *testing.T) {
	fw := &fakeWriter{err: http.ErrNotSupported}
	o, mt, rec := newOwner(t, fw, ProtoH3)
	body := newBlockingBody()
	o.InterruptWith(body)
	if err := o.BeginBodyRead(time.Second, false, nil); err != nil {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() {
		_, err := body.Read(make([]byte, 1))
		read <- err
	}()
	mt.fire()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("refused abort left the body read blocked")
	}
	if got := o.EndBodyRead(); got != BodyUnsupported {
		t.Fatalf("EndBodyRead = %v, want BodyUnsupported", got)
	}
	if got := rec.list(); len(got) != 1 || got[0] != "h3/unsupported" {
		t.Fatalf("results = %v", got)
	}
	onlyPast(t, fw.applied(), 1)
}

// TestRefusedRetirementAbortInterruptsRead covers a retirement abort on the
// same writer: the read ends, and a later window is refused.
func TestRefusedRetirementAbortInterruptsRead(t *testing.T) {
	fw := &fakeWriter{err: http.ErrNotSupported}
	o, _, _ := newOwner(t, fw, ProtoH2)
	body := newBlockingBody()
	o.InterruptWith(body)
	if err := o.BeginBodyRead(time.Hour, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := o.Abort(); !errors.Is(err, ErrUnsupported) || !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("Abort = %v", err)
	}
	select {
	case <-body.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("refused retirement abort did not interrupt the read")
	}
	if got := o.EndBodyRead(); got != BodyComplete {
		t.Fatalf("EndBodyRead = %v", got)
	}
	if err := o.BeginBodyRead(time.Hour, false, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("BeginBodyRead after refused abort = %v", err)
	}
	onlyPast(t, fw.applied(), 1)
}

// forwarder exposes SetReadDeadline but reports whether it reaches a writer
// that applies it.
type forwarder struct {
	fakeWriter
	supports bool
}

func (f *forwarder) SupportsReadDeadline() bool { return f.supports }

func TestSupportReporterDecidesSupport(t *testing.T) {
	if h1Owner(&forwarder{}).Supported() {
		t.Fatal("a forwarder reporting no support was treated as supported")
	}
	if !h1Owner(&forwarder{supports: true}).Supported() {
		t.Fatal("a forwarder reporting support was rejected")
	}
	if WriterSupports(httptest.NewRecorder()) {
		t.Fatal("a recorder applies no read deadline")
	}
	o := h1Owner(&forwarder{})
	if err := o.BeginBodyRead(time.Second, false, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("BeginBodyRead = %v", err)
	}
}

func TestRecorderFromFirstBegin(t *testing.T) {
	rec := &results{}
	o := h1Owner(&fakeWriter{})
	mt := &manualTimer{}
	o.afterFunc = mt.after
	if err := o.BeginBodyRead(time.Second, false, rec.record); err != nil {
		t.Fatal(err)
	}
	mt.fire()
	o.EndBodyRead()
	if got := rec.list(); len(got) != 1 || got[0] != "h1/timeout" {
		t.Fatalf("results = %v", got)
	}
}
