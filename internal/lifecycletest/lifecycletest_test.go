// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycletest

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type recordingTB struct {
	testing.TB
	errs []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func TestResourceLifecycle(t *testing.T) {
	r := NewResource("pool")
	if err := r.Acquire(); err != nil {
		t.Fatalf("open resource Acquire: %v", err)
	}
	rec := &recordingTB{TB: t}
	AssertOpen(rec, r)
	AssertClosedOnce(rec, r)
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "want exactly once") {
		t.Fatalf("open resource assertions = %q", rec.errs)
	}

	_ = r.Close()
	if err := r.Acquire(); !errors.Is(err, ErrRetired) {
		t.Fatalf("retired Acquire err = %v, want ErrRetired", err)
	}
	if r.Acquired() != 1 {
		t.Fatalf("Acquired = %d, want 1", r.Acquired())
	}
	rec = &recordingTB{TB: t}
	AssertClosedOnce(rec, r)
	AssertOpen(rec, r)
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "want still open") {
		t.Fatalf("closed resource assertions = %q", rec.errs)
	}

	_ = r.Close()
	rec = &recordingTB{TB: t}
	AssertClosedOnce(rec, r)
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "closed 2 time(s)") {
		t.Fatalf("double close not detected: %q", rec.errs)
	}
}

func TestAwaitQuiescence(t *testing.T) {
	base := Take()
	stop := make(chan struct{})
	go func() { <-stop }()
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(stop)
	}()
	AwaitQuiescence(t, base, 0)

	prev := quiescenceWait
	quiescenceWait = 50 * time.Millisecond
	defer func() { quiescenceWait = prev }()
	leak := make(chan struct{})
	defer close(leak)
	go func() { <-leak }()
	rec := &recordingTB{TB: t}
	AwaitQuiescence(rec, base, 0)
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "did not return to quiescence") {
		t.Fatalf("leak not reported: %d errors", len(rec.errs))
	}
}

func TestWithin(t *testing.T) {
	cases := []struct {
		base, now Snapshot
		slack     int
		want      bool
	}{
		{Snapshot{10, 5}, Snapshot{10, 5}, 0, true},
		{Snapshot{10, 5}, Snapshot{11, 5}, 0, false},
		{Snapshot{10, 5}, Snapshot{11, 6}, 1, true},
		{Snapshot{10, 5}, Snapshot{10, 7}, 1, false},
		{Snapshot{10, -1}, Snapshot{10, 99}, 0, true},
		{Snapshot{10, 5}, Snapshot{10, -1}, 0, true},
	}
	for _, c := range cases {
		if got := within(c.base, c.now, c.slack); got != c.want {
			t.Errorf("within(%v,%v,%d) = %v, want %v", c.base, c.now, c.slack, got, c.want)
		}
	}
}
