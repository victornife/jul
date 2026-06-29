package admin

import (
	"testing"
	"time"
)

func TestHubSubscribe(t *testing.T) {
	h := newHub()
	ch := h.Subscribe()
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	defer close(ch)

	h.Broadcast(Event{Type: "test", Time: time.Now()})

	select {
	case ev := <-ch:
		if ev.Type != "test" {
			t.Fatalf("event type = %q, want test", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestHubSubscribeWhenClosed(t *testing.T) {
	h := newHub()
	h.Close()

	ch := h.Subscribe()
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("expected closed channel when hub is closed")
		}
	default:
		// channel may already be drained
	}
}

func TestHubBroadcastDropsForSlowReader(t *testing.T) {
	h := newHub()
	ch := h.Subscribe()
	defer close(ch)

	// Fill the buffer (capacity 8) then broadcast one more.
	for i := 0; i < 10; i++ {
		h.Broadcast(Event{Type: "flood", Time: time.Now()})
	}

	// The slow reader should have received at most 8 events (buffer capacity).
	count := 0
	done := time.After(100 * time.Millisecond)
loop:
	for {
		select {
		case <-ch:
			count++
		case <-done:
			break loop
		}
	}
	if count > 8 {
		t.Fatalf("received %d events, expected at most 8 (buffer capacity)", count)
	}
}

func TestHubCloseDrainsSubscribers(t *testing.T) {
	h := newHub()
	ch1 := h.Subscribe()
	ch2 := h.Subscribe()

	h.Close()

	for _, ch := range []chan Event{ch1, ch2} {
		_, open := <-ch
		if open {
			t.Fatal("expected channel to be closed after hub shutdown")
		}
	}
}

func TestHubCloseIdempotent(t *testing.T) {
	h := newHub()
	ch := h.Subscribe()

	h.Close()
	// drain the closed channel to avoid goroutine leak if we were ranging
	<-ch

	h.Close() // should not panic
}

func TestHubBroadcastAfterClose(t *testing.T) {
	h := newHub()
	ch := h.Subscribe()

	h.Close()
	// After Close(), hub closes all subscriber channels.
	_, open := <-ch
	if open {
		t.Fatal("expected channel to be closed by hub")
	}

	// Broadcast after close should not panic.
	h.Broadcast(Event{Type: "late", Time: time.Now()})
}

func TestHubMultipleSubscribers(t *testing.T) {
	h := newHub()
	ch1 := h.Subscribe()
	ch2 := h.Subscribe()

	h.Broadcast(Event{Type: "multi", Time: time.Now()})

	for i, ch := range []chan Event{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Type != "multi" {
				t.Fatalf("subscriber %d: type = %q, want multi", i, ev.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d timed out", i)
		}
	}

	// Clean up: close channels to trigger unsubscribe.
	close(ch1)
	close(ch2)
}

func TestHubUnsubscribe(t *testing.T) {
	h := newHub()
	ch := h.Subscribe()

	// Manually call unsubscribe (normally called when channel is closed)
	h.unsubscribe(ch)

	_, open := <-ch
	if open {
		t.Fatal("expected channel to be closed after unsubscribe")
	}

	// Broadcast after unsubscribe should not panic.
	h.Broadcast(Event{Type: "after", Time: time.Now()})
}
