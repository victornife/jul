// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"testing"
	"time"
)

// TestDriftOnlySignalConsumerNilChannelReturnsImmediately pins the nil-input
// guard: a process with no SIGHUP source must not block forever.
func TestDriftOnlySignalConsumerNilChannelReturnsImmediately(t *testing.T) {
	done := make(chan struct{})
	go func() {
		driftOnlySignalConsumer(context.Background(), nil, make(chan struct{}, 1))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("driftOnlySignalConsumer did not return for a nil channel")
	}
}

// TestDriftOnlySignalConsumerSchedulesAssessment pins ADR 0019 §11 point 5:
// a signal schedules a drift assessment (never a reload) and bursts coalesce
// into the single-slot request channel.
func TestDriftOnlySignalConsumerSchedulesAssessment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan struct{})
	assessRequests := make(chan struct{}, 1)
	go driftOnlySignalConsumer(ctx, in, assessRequests)

	in <- struct{}{}
	select {
	case <-assessRequests:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a scheduled drift assessment")
	}

	// A second signal while the slot is already full must not block the
	// consumer (coalesced, per MergeReload's own burst-coalescing pattern).
	in <- struct{}{}
	in <- struct{}{}
}

// TestDriftOnlySignalConsumerStopsOnContextDone pins clean shutdown.
func TestDriftOnlySignalConsumerStopsOnContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan struct{})
	done := make(chan struct{})
	go func() {
		driftOnlySignalConsumer(ctx, in, make(chan struct{}, 1))
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("driftOnlySignalConsumer did not stop when ctx was canceled")
	}
}

// TestDriftOnlySignalConsumerStopsOnClosedChannel pins the closed-channel exit.
func TestDriftOnlySignalConsumerStopsOnClosedChannel(t *testing.T) {
	in := make(chan struct{})
	done := make(chan struct{})
	go func() {
		driftOnlySignalConsumer(context.Background(), in, make(chan struct{}, 1))
		close(done)
	}()
	close(in)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("driftOnlySignalConsumer did not stop when in was closed")
	}
}

// TestDriftOnlyFileConsumerNilChannelReturnsImmediately mirrors the signal
// consumer's nil-input guard for the file-watch source.
func TestDriftOnlyFileConsumerNilChannelReturnsImmediately(t *testing.T) {
	done := make(chan struct{})
	go func() {
		driftOnlyFileConsumer(context.Background(), nil, make(chan struct{}, 1))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("driftOnlyFileConsumer did not return for a nil channel")
	}
}

// TestDriftOnlyFileConsumerSchedulesAssessment pins ADR 0019 §11 point 4: a
// watcher event schedules a drift assessment, ignoring the reported digest.
func TestDriftOnlyFileConsumerSchedulesAssessment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan [32]byte)
	assessRequests := make(chan struct{}, 1)
	go driftOnlyFileConsumer(ctx, in, assessRequests)

	in <- [32]byte{1, 2, 3}
	select {
	case <-assessRequests:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a scheduled drift assessment")
	}
}

// TestDriftOnlyFileConsumerStopsOnContextDone and closed-channel pin clean
// shutdown, mirroring the signal consumer.
func TestDriftOnlyFileConsumerStopsOnContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan [32]byte)
	done := make(chan struct{})
	go func() {
		driftOnlyFileConsumer(ctx, in, make(chan struct{}, 1))
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("driftOnlyFileConsumer did not stop when ctx was canceled")
	}
}

func TestDriftOnlyFileConsumerStopsOnClosedChannel(t *testing.T) {
	in := make(chan [32]byte)
	done := make(chan struct{})
	go func() {
		driftOnlyFileConsumer(context.Background(), in, make(chan struct{}, 1))
		close(done)
	}()
	close(in)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("driftOnlyFileConsumer did not stop when in was closed")
	}
}
