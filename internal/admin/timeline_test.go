package admin

import (
	"testing"
)

func TestEventHistoryAddAndSnapshot(t *testing.T) {
	h := newEventHistory(4)
	h.add(TimelineEvent{Type: "a", Message: "first"})
	h.add(TimelineEvent{Type: "b", Message: "second"})

	snap := h.snapshot()
	if len(snap) != 2 {
		t.Fatalf("len = %d, want 2", len(snap))
	}
	if snap[0].Type != "b" {
		t.Fatalf("most recent = %q, want b", snap[0].Type)
	}
	if snap[1].Type != "a" {
		t.Fatalf("oldest = %q, want a", snap[1].Type)
	}
}

func TestEventHistorySetsTimeWhenZero(t *testing.T) {
	h := newEventHistory(4)
	h.add(TimelineEvent{Type: "x"})

	snap := h.snapshot()
	if len(snap) != 1 {
		t.Fatal("expected 1 event")
	}
	if snap[0].Time.IsZero() {
		t.Fatal("expected time to be auto-set")
	}
}

func TestEventHistoryRingWraparound(t *testing.T) {
	h := newEventHistory(3)
	h.add(TimelineEvent{Type: "1"})
	h.add(TimelineEvent{Type: "2"})
	h.add(TimelineEvent{Type: "3"})
	h.add(TimelineEvent{Type: "4"}) // overwrites "1"

	snap := h.snapshot()
	if len(snap) != 3 {
		t.Fatalf("len = %d, want 3", len(snap))
	}
	// Newest first
	if snap[0].Type != "4" {
		t.Fatalf("[0] = %q, want 4", snap[0].Type)
	}
	if snap[1].Type != "3" {
		t.Fatalf("[1] = %q, want 3", snap[1].Type)
	}
	if snap[2].Type != "2" {
		t.Fatalf("[2] = %q, want 2", snap[2].Type)
	}
}

func TestEventHistoryConcurrency(t *testing.T) {
	h := newEventHistory(100)
	var done = make(chan struct{})

	go func() {
		for i := 0; i < 500; i++ {
			h.add(TimelineEvent{Type: "writer"})
		}
		close(done)
	}()

	for i := 0; i < 500; i++ {
		_ = h.snapshot()
	}

	<-done
	// No data race or panic expected.
}

func TestEventHistoryEmptySnapshot(t *testing.T) {
	h := newEventHistory(4)
	snap := h.snapshot()
	if len(snap) != 0 {
		t.Fatalf("len = %d, want 0", len(snap))
	}
}

func TestEventHistoryDefaultCapacity(t *testing.T) {
	h := newEventHistory(0)
	if len(h.buf) != timelineCap {
		t.Fatalf("capacity = %d, want %d", len(h.buf), timelineCap)
	}
}

func TestEventHistoryFullThenPartial(t *testing.T) {
	h := newEventHistory(2)
	h.add(TimelineEvent{Type: "1"})
	h.add(TimelineEvent{Type: "2"})
	h.add(TimelineEvent{Type: "3"})

	snap := h.snapshot()
	if len(snap) != 2 {
		t.Fatalf("len = %d, want 2", len(snap))
	}
	if snap[0].Type != "3" || snap[1].Type != "2" {
		t.Fatalf("unexpected order: %v", snap)
	}

	// Now overwrite "2" with "4"
	h.add(TimelineEvent{Type: "4"})
	snap = h.snapshot()
	if snap[0].Type != "4" || snap[1].Type != "3" {
		t.Fatalf("unexpected order after overwrite: %v", snap)
	}
}
