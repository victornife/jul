// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"sync"
	"time"
)

// Clock is the smallest time abstraction needed by ConfigApplyCoordinator so
// deterministic tests can advance time explicitly instead of racing the host
// scheduler. The production composition root leaves the coordinator clock nil,
// which selects the real wall-clock implementation. This is intentionally an
// internal test seam, not a public runtime contract.
type Clock interface {
	Now() time.Time
	Since(time.Time) time.Duration
	Until(time.Time) time.Duration
	After(time.Duration) <-chan time.Time
	NewTimer(time.Duration) Timer
}

// Timer is the subset of time.Timer used by the coordinator.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// realClock is the production wall-clock implementation.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) Since(t time.Time) time.Duration        { return time.Since(t) }
func (realClock) Until(t time.Time) time.Duration        { return time.Until(t) }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) NewTimer(d time.Duration) Timer         { return &realTimer{time.NewTimer(d)} }

type realTimer struct{ *time.Timer }

func (t *realTimer) C() <-chan time.Time { return t.Timer.C }
func (t *realTimer) Stop() bool          { return t.Timer.Stop() }

// coordinatorClock returns the configured clock or the real wall clock when
// none has been injected.
func (c *ConfigApplyCoordinator) coordinatorClock() Clock {
	if c.clock != nil {
		return c.clock
	}
	return realClock{}
}

// withDeadline mirrors context.WithDeadline but uses the coordinator clock.
// It lets deterministic tests trigger deadline expiry by advancing a fake
// clock rather than sleeping and competing with the host scheduler.
func (c *ConfigApplyCoordinator) withDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	clk := c.coordinatorClock()
	if clk == (realClock{}) {
		return context.WithDeadline(parent, deadline)
	}
	ctx := &clockDeadlineCtx{
		parent:   parent,
		deadline: deadline,
		done:     make(chan struct{}),
		clock:    clk,
	}
	remaining := clk.Until(deadline)
	if remaining <= 0 {
		ctx.cancel(context.DeadlineExceeded)
		return ctx, func() { ctx.cancel(context.Canceled) }
	}
	timer := clk.NewTimer(remaining)
	ctx.timer = timer

	parentDone := parent.Done()
	if parentDone == nil {
		// No parent cancellation: only the deadline timer can trigger expiry.
		go func() {
			<-timer.C()
			ctx.cancel(context.DeadlineExceeded)
		}()
		return ctx, func() { ctx.cancel(context.Canceled) }
	}

	go func() {
		select {
		case <-parentDone:
			ctx.cancel(parent.Err())
		case <-timer.C():
			ctx.cancel(context.DeadlineExceeded)
		}
		// Release the timer in case the parent fired first.
		timer.Stop()
	}()
	return ctx, func() { ctx.cancel(context.Canceled) }
}

// withTimeout mirrors context.WithTimeout using the coordinator clock.
func (c *ConfigApplyCoordinator) withTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return c.withDeadline(parent, c.coordinatorClock().Now())
	}
	return c.withDeadline(parent, c.coordinatorClock().Now().Add(timeout))
}

// clockDeadlineCtx is a context.Context implementation whose cancellation is
// driven by a Clock rather than the Go runtime timer.
type clockDeadlineCtx struct {
	parent   context.Context
	deadline time.Time
	done     chan struct{}
	errMu    sync.Mutex
	err      error
	once     sync.Once
	clock    Clock
	timer    Timer
}

func (ctx *clockDeadlineCtx) Deadline() (time.Time, bool) { return ctx.deadline, true }
func (ctx *clockDeadlineCtx) Done() <-chan struct{}       { return ctx.done }

func (ctx *clockDeadlineCtx) Err() error {
	ctx.errMu.Lock()
	defer ctx.errMu.Unlock()
	return ctx.err
}

func (ctx *clockDeadlineCtx) Value(key any) any { return ctx.parent.Value(key) }

func (ctx *clockDeadlineCtx) cancel(err error) {
	ctx.once.Do(func() {
		ctx.errMu.Lock()
		ctx.err = err
		ctx.errMu.Unlock()
		if ctx.timer != nil {
			ctx.timer.Stop()
		}
		close(ctx.done)
	})
}

// fakeClock is a deterministic, manually-advanced clock for tests. Timers fire
// only when Advance moves the clock past their deadline. It is safe to call
// Advance while goroutines are blocked on timers created by this clock.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Since(t time.Time) time.Duration { return f.Now().Sub(t) }
func (f *fakeClock) Until(t time.Time) time.Duration { return t.Sub(f.Now()) }

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	return f.NewTimer(d).C()
}

func (f *fakeClock) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	ft := &fakeTimer{clock: f, deadline: f.now.Add(d), c: make(chan time.Time, 1)}
	f.timers = append(f.timers, ft)
	// A zero or negative duration fires immediately so callers that compute a
	// non-positive remaining time observe the timer without requiring another
	// Advance call.
	fireNow := d <= 0
	if fireNow {
		ft.fired = true
	}
	now := f.now
	f.mu.Unlock()
	if fireNow {
		ft.c <- now
	}
	return ft
}

// Advance moves fake time forward by d and fires any timers whose deadlines
// have been reached.
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	var fired []*fakeTimer
	for _, t := range f.timers {
		if t.fireIfReady(f.now) {
			fired = append(fired, t)
		}
	}
	f.mu.Unlock()
	for _, t := range fired {
		t.c <- f.now
	}
}

type fakeTimer struct {
	clock    *fakeClock
	deadline time.Time
	c        chan time.Time
	mu       sync.Mutex
	stopped  bool
	fired    bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fired {
		return false
	}
	wasStopped := t.stopped
	t.stopped = true
	return !wasStopped
}

func (t *fakeTimer) fireIfReady(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.fired || now.Before(t.deadline) {
		return false
	}
	t.fired = true
	return true
}
