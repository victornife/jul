// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package logthrottle

import (
	"sync"
	"testing"
	"time"
)

func TestZeroValueAdmitsTheFirstEvent(t *testing.T) {
	var l Limiter
	if !l.Allow(time.Hour) {
		t.Fatal("the zero value suppressed the first event")
	}
}

func TestOneEventPerInterval(t *testing.T) {
	var l Limiter
	if !l.Allow(time.Hour) {
		t.Fatal("first event was suppressed")
	}
	for i := range 100 {
		if l.Allow(time.Hour) {
			t.Fatalf("event %d was admitted inside the interval", i)
		}
	}
}

func TestElapsedIntervalAdmitsAgain(t *testing.T) {
	var l Limiter
	if !l.Allow(time.Nanosecond) {
		t.Fatal("first event was suppressed")
	}
	time.Sleep(time.Millisecond)
	if !l.Allow(time.Nanosecond) {
		t.Fatal("event after the interval was suppressed")
	}
}

// TestConcurrentCallersAdmitExactlyOne is the property that matters for a
// listener flooded by a remote peer: many goroutines racing produce one line,
// not one per goroutine.
func TestConcurrentCallersAdmitExactlyOne(t *testing.T) {
	var (
		l        Limiter
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
	)
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow(time.Hour) {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 1 {
		t.Fatalf("admitted %d events, want exactly 1", admitted)
	}
}
