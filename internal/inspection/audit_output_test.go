// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"testing"
	"time"
)

func TestAuditWriteAbandonedChargeStaysVisibleUntilReturn(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 4<<20, 1), Hooks{})
	lease := acquireTestLease(t, g)
	r5, err := lease.Charge(AuditBytes, 4096)
	if err != nil {
		t.Fatal(err)
	}
	w, ok := g.BeginAuditWrite(1000)
	if !ok {
		t.Fatal("audit write refused on an empty set")
	}
	w.Abandon()
	if err := r5.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	snap := g.Snapshot()
	if snap.Usage[AuditBytes] != 1000 || snap.AuditOutput.Abandoned != 1 || snap.AuditOutput.AbandonedBytes != 1000 {
		t.Fatalf("after lease close: audit=%d output=%+v", snap.Usage[AuditBytes], snap.AuditOutput)
	}
	w.Finish()
	w.Finish()
	snap = g.Snapshot()
	if snap.Usage[AuditBytes] != 0 || snap.AuditOutput != (AuditOutputSnapshot{MaxEntries: AuditOutputMaxEntries, MaxBytes: auditOutputMinBytes}) {
		t.Fatalf("after return: audit=%d output=%+v", snap.Usage[AuditBytes], snap.AuditOutput)
	}
}

func TestAuditWriteInTimeHoldsNoProcessCharge(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 4<<20, 1), Hooks{})
	w, ok := g.BeginAuditWrite(1000)
	if !ok {
		t.Fatal("audit write refused on an empty set")
	}
	if snap := g.Snapshot(); snap.Usage[AuditBytes] != 0 || snap.AuditOutput.InFlight != 1 {
		t.Fatalf("in flight: audit=%d output=%+v", snap.Usage[AuditBytes], snap.AuditOutput)
	}
	w.Finish()
	w.Abandon()
	if snap := g.Snapshot(); snap.Usage[AuditBytes] != 0 || snap.AuditOutput.InFlight != 0 || snap.AuditOutput.Abandoned != 0 {
		t.Fatalf("finished: audit=%d output=%+v", snap.Usage[AuditBytes], snap.AuditOutput)
	}
}

func TestAuditOutputSaturationByMode(t *testing.T) {
	for _, mode := range []Mode{Enforce, Observe} {
		t.Run(string(mode), func(t *testing.T) {
			var drops []string
			var rejections []Mode
			g := newTestGovernor(t, testLimits(mode, 1, time.Second, 64<<20, 1), Hooks{
				OnAuditDrop: func(reason string) { drops = append(drops, reason) },
				OnRejection: func(reason Outcome, m Mode) {
					if reason != AuditSaturated {
						t.Errorf("rejection reason %s", reason)
					}
					rejections = append(rejections, m)
				},
			})
			writes := make([]*AuditWrite, 0, AuditOutputMaxEntries)
			for range AuditOutputMaxEntries {
				w, ok := g.BeginAuditWrite(10)
				if !ok {
					t.Fatal("refused below the entry bound")
				}
				w.Abandon()
				writes = append(writes, w)
			}
			if _, ok := g.BeginAuditWrite(10); ok {
				t.Fatal("audit write accepted beyond the entry bound")
			}
			err := g.AdmitAuditOutput()
			if mode == Enforce && !IsOutcome(err, AuditSaturated) {
				t.Fatalf("enforce admission = %v", err)
			}
			if mode == Observe && err != nil {
				t.Fatalf("observe admission = %v", err)
			}
			if mode == Enforce && !IsOverload(err) {
				t.Fatal("audit saturation is not classified as overload")
			}
			index := 0
			if mode == Observe {
				index = 1
			}
			snap := g.Snapshot()
			if snap.Rejections[RejectAudit][index] != 1 || snap.AuditOutput.Drops != 1 ||
				len(drops) != 1 || drops[0] != AuditDropAbandonedFull || len(rejections) != 1 || rejections[0] != mode {
				t.Fatalf("snapshot %+v drops %v rejections %v", snap, drops, rejections)
			}
			for _, w := range writes {
				w.Finish()
			}
			if err := g.AdmitAuditOutput(); err != nil {
				t.Fatalf("admission after drain = %v", err)
			}
			if snap := g.Snapshot(); snap.Usage[AuditBytes] != 0 || snap.AuditOutput.Abandoned != 0 {
				t.Fatalf("after drain %+v", snap)
			}
		})
	}
}

func TestAuditOutputByteBound(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 8<<20, 1), Hooks{})
	bound := g.Snapshot().AuditOutput.MaxBytes
	if bound != 2<<20 {
		t.Fatalf("bound = %d", bound)
	}
	big, ok := g.BeginAuditWrite(bound + 1)
	if !ok {
		t.Fatal("one oversized record must be accepted into an empty set")
	}
	if _, ok := g.BeginAuditWrite(1); ok {
		t.Fatal("record accepted while the byte bound is exceeded")
	}
	if err := g.AdmitAuditOutput(); !IsOutcome(err, AuditSaturated) {
		t.Fatalf("admission over byte bound = %v", err)
	}
	big.Finish()
	half, ok := g.BeginAuditWrite(bound / 2)
	if !ok {
		t.Fatal("refused below the byte bound")
	}
	if _, ok := g.BeginAuditWrite(bound/2 + 1); ok {
		t.Fatal("record accepted past the byte bound")
	}
	half.Finish()
}

func TestChargeCommittedNeverRefusesAndCountsOverrun(t *testing.T) {
	for _, mode := range []Mode{Enforce, Observe} {
		t.Run(string(mode), func(t *testing.T) {
			var rejections int
			g := newTestGovernor(t, testLimits(mode, 1, 0, 1000, 1), Hooks{
				OnRejection: func(Outcome, Mode) { rejections++ },
			})
			lease := acquireTestLease(t, g)
			c, err := lease.ChargeCommitted(AuditBytes, 5000)
			if err != nil {
				t.Fatal(err)
			}
			snap := g.Snapshot()
			if snap.Usage[AuditBytes] != 5000 || snap.CommittedOverruns != 1 || rejections != 0 {
				t.Fatalf("usage=%d overruns=%d rejections=%d", snap.Usage[AuditBytes], snap.CommittedOverruns, rejections)
			}
			if _, err := lease.ChargeCommitted(AuditBytes, 0); err != nil {
				t.Fatal(err)
			}
			if err := c.Release(); err != nil {
				t.Fatal(err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			if snap := g.Snapshot(); snap.Usage[AuditBytes] != 0 || snap.CommittedOverruns != 1 {
				t.Fatalf("after release: usage=%d overruns=%d", snap.Usage[AuditBytes], snap.CommittedOverruns)
			}
		})
	}
}

func TestNoteAuditDropBudget(t *testing.T) {
	var reasons []string
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 4<<20, 1), Hooks{
		OnAuditDrop: func(r string) { reasons = append(reasons, r) },
	})
	g.NoteAuditDrop(AuditDropBudget)
	var nilGov *Governor
	nilGov.NoteAuditDrop(AuditDropBudget)
	if snap := g.Snapshot(); snap.AuditOutput.Drops != 1 || len(reasons) != 1 || reasons[0] != AuditDropBudget {
		t.Fatalf("drops=%d reasons=%v", snap.AuditOutput.Drops, reasons)
	}
}
