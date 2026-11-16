// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"testing"

	"jul/internal/inspection"
)

func TestGuestTeardownDebtMetric(t *testing.T) {
	m := NewMetrics()
	m.SetInspectionSource(func() InspectionMetricsSnapshot {
		s := InspectionMetricsSnapshot{GuestCapacityBytes: 128 << 10}
		s.GuestInstances[inspection.GuestTeardownUnconfirmed] = 2
		return s
	})
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, family := range families {
		if family.GetName() != "jul_inspection_guest_instances" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "state" && label.GetValue() == "teardown_unconfirmed" {
					found = true
					if got := metric.GetGauge().GetValue(); got != 2 {
						t.Errorf("debt gauge = %v, want 2", got)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("teardown_unconfirmed metric missing")
	}
}
