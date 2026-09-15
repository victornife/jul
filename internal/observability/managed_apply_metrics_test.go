// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// managedApplySeries returns the value of the metric family `name` whose labels
// exactly match `want`, or fails when no such single series exists. It proves a
// bounded metric emits the expected value under a specific label set.
func managedApplySeries(t *testing.T, m *Metrics, name string, want map[string]string) float64 {
	t.Helper()
	fams, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, fam := range fams {
		if fam.GetName() != name {
			continue
		}
		for _, mt := range fam.GetMetric() {
			if labelsMatch(mt.GetLabel(), want) {
				if c := mt.GetCounter(); c != nil {
					return c.GetValue()
				}
				if g := mt.GetGauge(); g != nil {
					return g.GetValue()
				}
				t.Fatalf("metric %q is neither a counter nor a gauge", name)
			}
		}
		t.Fatalf("metric %q has no series matching labels %v", name, want)
	}
	t.Fatalf("metric family %q not found", name)
	return 0
}

func labelsMatch(got []*dto.LabelPair, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, lp := range got {
		if want[lp.GetName()] != lp.GetValue() {
			return false
		}
	}
	return true
}

// TestManagedApplyFinalizationErrorComponentLabel proves the finalization-error
// counter is labeled by the bounded component so a restoration failure is
// distinguishable from a callback panic (WS06 §7.5).
func TestManagedApplyFinalizationErrorComponentLabel(t *testing.T) {
	m := NewMetrics()
	m.ObserveManagedApplyFinalizationError("restoration")
	m.ObserveManagedApplyFinalizationError("restoration")
	m.ObserveManagedApplyFinalizationError("callback_panic")
	m.ObserveManagedApplyFinalizationError("")

	if got := managedApplySeries(t, m, "jul_managed_apply_finalization_errors_total", map[string]string{"component": "restoration"}); got != 2 {
		t.Errorf("restoration count = %v, want 2", got)
	}
	if got := managedApplySeries(t, m, "jul_managed_apply_finalization_errors_total", map[string]string{"component": "callback_panic"}); got != 1 {
		t.Errorf("callback_panic count = %v, want 1", got)
	}
	if got := managedApplySeries(t, m, "jul_managed_apply_finalization_errors_total", map[string]string{"component": "unknown"}); got != 1 {
		t.Errorf("empty component normalized to unknown count = %v, want 1", got)
	}
}

// TestManagedApplyRegistryEntriesGauge proves the retained-ledger gauge reflects
// the last published value (WS06 §7.5).
func TestManagedApplyRegistryEntriesGauge(t *testing.T) {
	m := NewMetrics()
	m.SetManagedApplyRegistryEntries(3)
	if got := managedApplySeries(t, m, "jul_managed_apply_terminal_registry_entries", map[string]string{}); got != 3 {
		t.Errorf("gauge = %v, want 3", got)
	}
	m.SetManagedApplyRegistryEntries(1)
	if got := managedApplySeries(t, m, "jul_managed_apply_terminal_registry_entries", map[string]string{}); got != 1 {
		t.Errorf("gauge after update = %v, want 1", got)
	}
}

// TestManagedApplyLookupCounter proves the exact-ID lookup counter is bounded to
// the lookup result and normalizes an empty result to "unknown" (WS06 §7.5).
func TestManagedApplyLookupCounter(t *testing.T) {
	m := NewMetrics()
	m.ObserveManagedApplyLookup("missing")
	m.ObserveManagedApplyLookup("missing")
	m.ObserveManagedApplyLookup("terminal")
	m.ObserveManagedApplyLookup("")

	if got := managedApplySeries(t, m, "jul_managed_apply_terminal_lookup_total", map[string]string{"result": "missing"}); got != 2 {
		t.Errorf("missing count = %v, want 2", got)
	}
	if got := managedApplySeries(t, m, "jul_managed_apply_terminal_lookup_total", map[string]string{"result": "terminal"}); got != 1 {
		t.Errorf("terminal count = %v, want 1", got)
	}
	if got := managedApplySeries(t, m, "jul_managed_apply_terminal_lookup_total", map[string]string{"result": "unknown"}); got != 1 {
		t.Errorf("empty result normalized to unknown count = %v, want 1", got)
	}
}

func TestNoChangeReloadMetricsAreTerminalAndPhaseTruthful(t *testing.T) {
	m := NewMetrics()
	m.ReloadStarted()
	m.ObserveReload("sighup", "no_change", 3)
	m.ObserveReloadResult("no_change", map[string]int64{
		"resolve":           1,
		"validate":          1,
		"lifecycle":         1,
		"change_assessment": 0,
	}, false, "")

	if got := managedApplySeries(t, m, "jul_reload_total", map[string]string{"source": "sighup", "outcome": "no_change"}); got != 1 {
		t.Errorf("no_change reload count = %v, want 1", got)
	}
	if got := managedApplySeries(t, m, "jul_reload_in_progress", map[string]string{}); got != 0 {
		t.Errorf("reload in-progress gauge = %v, want 0", got)
	}

	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	wantPhases := map[string]bool{
		"resolve": true, "validate": true, "lifecycle": true, "change_assessment": true,
	}
	for _, family := range families {
		if family.GetName() != "jul_reload_phase_duration_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, pair := range metric.GetLabel() {
				labels[pair.GetName()] = pair.GetValue()
			}
			if labels["outcome"] != "no_change" {
				continue
			}
			if !wantPhases[labels["phase"]] {
				t.Fatalf("no_change fabricated phase %q", labels["phase"])
			}
			delete(wantPhases, labels["phase"])
		}
	}
	if len(wantPhases) != 0 {
		t.Fatalf("no_change phase series missing: %v", wantPhases)
	}
}
