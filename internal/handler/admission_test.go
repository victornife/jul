// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/resilience"
	"jul/internal/upstream"
)

func testAdmission(t *testing.T, o resilience.Options) *upstream.Admission {
	t.Helper()
	p, err := resilience.Resolve(o)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return upstream.NewAdmission(p)
}

// TestAdmittedHandlerHoldsSlotForHandlerLifetime pins the shared lifetime rule
// that native gRPC and gRPC transcoding both rely on: the slot spans the whole
// of the wrapped handler's ServeHTTP, which for a streaming method is the
// stream's real lifetime rather than the moment its headers were written.
func TestAdmittedHandlerHoldsSlotForHandlerLifetime(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	adm := testAdmission(t, resilience.Options{MaxActiveRequests: 2})
	h := newAdmittedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		w.WriteHeader(http.StatusOK)
		<-finish
	}), adm, nil)

	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	<-started

	if got := adm.Active(); got != 1 {
		t.Fatalf("active while the handler is still streaming = %d, want 1", got)
	}
	close(finish)
	waitAdmissionQuiesce(t, adm)
}

// TestAdmittedHandlerReleasesOnceOnCancel pins that abandoning a stream returns
// exactly one slot, not zero and not two.
func TestAdmittedHandlerReleasesOnceOnCancel(t *testing.T) {
	adm := testAdmission(t, resilience.Options{MaxActiveRequests: 4})
	h := newAdmittedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}), adm, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))
	}()
	waitActive(t, adm, 1)
	cancel()
	<-done
	waitAdmissionQuiesce(t, adm)

	// Four fresh slots must still be available: a double release would have made
	// the counter negative and silently raised the effective limit.
	for i := 0; i < 4; i++ {
		if _, err := adm.Admit(context.Background(), nil); err != nil {
			t.Fatalf("admit %d after cancel: %v", i, err)
		}
	}
	if _, err := adm.Admit(context.Background(), nil); err == nil {
		t.Fatal("a fifth admit succeeded: the limit was raised by miscounting")
	}
}

// TestAdmittedHandlerCloseWakesQueued pins the forced-retirement wakeup for the
// protocols that use this wrapper.
func TestAdmittedHandlerCloseWakesQueued(t *testing.T) {
	adm := testAdmission(t, resilience.Options{MaxActiveRequests: 1, MaxPendingRequests: 4})
	block := make(chan struct{})
	defer close(block)
	h := newAdmittedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}), adm, nil)

	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	waitActive(t, adm, 1)

	queued := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		queued <- rec.Code
	}()
	waitPendingAdmission(t, adm, 1)

	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case code := <-queued:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a queued request was not woken by generation retirement")
	}
}

// TestAdmittedHandlerCloseRunsWrappedCloser pins that wrapping a protocol
// handler does not swallow its own teardown: the transcoder's connections and
// the gRPC transport's idle sockets are released through this path.
func TestAdmittedHandlerCloseRunsWrappedCloser(t *testing.T) {
	closed := 0
	adm := testAdmission(t, resilience.Options{})
	h := newAdmittedHandler(http.NotFoundHandler(), adm, func() error {
		closed++
		return nil
	})
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if closed == 0 {
		t.Fatal("the wrapped closer never ran")
	}
}

func waitAdmissionQuiesce(t *testing.T, a *upstream.Admission) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for a.Active() != 0 || a.Pending() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for quiesce: active=%d pending=%d", a.Active(), a.Pending())
		}
		time.Sleep(time.Millisecond)
	}
}
