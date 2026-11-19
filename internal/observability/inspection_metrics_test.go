// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"testing"

	"jul/internal/inspection"
)

func TestGuestReservationMetricsReportManagedCapacity(t *testing.T) {
	m := NewMetrics()
	m.SetInspectionSource(func() InspectionMetricsSnapshot {
		s := InspectionMetricsSnapshot{
			GuestCapacityBytes: 128 << 10,
			GuestCapacityLimit: 256 << 10,
		}
		s.GuestInstances[inspection.GuestFailedRetained] = 2
		return s
	})
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundCapacity, foundLimit, foundRetained := false, false, false
	for _, family := range families {
		switch family.GetName() {
		case "jul_inspection_guest_capacity_bytes":
			if got := family.GetMetric()[0].GetGauge().GetValue(); got != 128<<10 {
				t.Errorf("reserved capacity = %v, want %d", got, 128<<10)
			}
			foundCapacity = true
		case "jul_inspection_guest_capacity_bytes_limit":
			if got := family.GetMetric()[0].GetGauge().GetValue(); got != 256<<10 {
				t.Errorf("capacity limit = %v, want %d", got, 256<<10)
			}
			foundLimit = true
		case "jul_inspection_guest_instances":
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "state" {
						if label.GetValue() == "failed_retained" {
							foundRetained = true
							if got := metric.GetGauge().GetValue(); got != 2 {
								t.Errorf("retained instance gauge = %v, want 2", got)
							}
						}
						if label.GetValue() == "teardown_unconfirmed" {
							t.Error("removed teardown-unconfirmed state is still exposed")
						}
					}
				}
			}
		}
	}
	if !foundCapacity || !foundLimit || !foundRetained {
		t.Fatalf("reservation metric families missing: capacity=%t limit=%t retained=%t", foundCapacity, foundLimit, foundRetained)
	}
}
