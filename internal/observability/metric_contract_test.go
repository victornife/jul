// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type metricContractDocument struct {
	Version          int                    `json:"version"`
	ReleasedBaseline releasedBaselineRef    `json:"released_baseline"`
	Metrics          []metricContractMetric `json:"metrics"`
}

type releasedBaselineRef struct {
	Tag    string `json:"tag"`
	Commit string `json:"commit"`
}

type releasedMetricDocument struct {
	Tag     string                 `json:"tag"`
	Commit  string                 `json:"commit"`
	Metrics []metricContractMetric `json:"metrics"`
}

type metricContractMetric struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Help   string   `json:"help"`
	Labels []string `json:"labels"`
	State  string   `json:"state,omitempty"`
}

func contractPath(parts ...string) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("metric contract test: runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(append([]string{root}, parts...)...)
}

func decodeContractFile(t *testing.T, path string, dst any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func loadMetricContract(t *testing.T) metricContractDocument {
	t.Helper()
	var doc metricContractDocument
	decodeContractFile(t, contractPath("docs", "metrics-contract.json"), &doc)
	if doc.Version != 1 {
		t.Fatalf("metrics contract version = %d, want 1", doc.Version)
	}
	return doc
}

func gatheredMetricContract(t *testing.T, m *Metrics) map[string]metricContractMetric {
	t.Helper()
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	out := make(map[string]metricContractMetric)
	for _, family := range families {
		name := family.GetName()
		if !strings.HasPrefix(name, "jul_") {
			continue
		}
		var labels []string
		if metrics := family.GetMetric(); len(metrics) > 0 {
			for _, pair := range metrics[0].GetLabel() {
				labels = append(labels, pair.GetName())
			}
		}
		sort.Strings(labels)
		out[name] = metricContractMetric{
			Name:   name,
			Type:   strings.ToLower(family.GetType().String()),
			Help:   family.GetHelp(),
			Labels: labels,
		}
	}
	return out
}

func normalizeMetric(m metricContractMetric) metricContractMetric {
	m.State = ""
	if len(m.Labels) == 0 {
		m.Labels = nil
	} else {
		sort.Strings(m.Labels)
	}
	return m
}

func TestMetricContractMatchesCollectors(t *testing.T) {
	m := NewMetrics()
	exerciseAllMetrics(m)
	got := gatheredMetricContract(t, m)
	contract := loadMetricContract(t)

	if contract.ReleasedBaseline.Tag != "v1.32.0" || contract.ReleasedBaseline.Commit != "6bb76a08846150663d7eeb9661edb718ef357a7c" {
		t.Fatalf("current contract baseline = %s@%s, want v1.32.0@6bb76a08846150663d7eeb9661edb718ef357a7c", contract.ReleasedBaseline.Tag, contract.ReleasedBaseline.Commit)
	}

	want := make(map[string]metricContractMetric, len(contract.Metrics))
	lastName := ""
	releasedCount, pendingCount := 0, 0
	for _, metric := range contract.Metrics {
		if metric.Name <= lastName {
			t.Fatalf("metrics contract is not strictly name-sorted at %q", metric.Name)
		}
		lastName = metric.Name
		switch metric.State {
		case "released_v1.32.0":
			releasedCount++
		case "merged_release_pending":
			pendingCount++
		default:
			t.Fatalf("metric %q has unknown contract state %q", metric.Name, metric.State)
		}
		want[metric.Name] = normalizeMetric(metric)
	}
	if releasedCount != 28 || pendingCount != 14 {
		t.Fatalf("metric contract states = %d released + %d pending, want 28 + 14", releasedCount, pendingCount)
	}

	for name, actual := range got {
		expected, ok := want[name]
		if !ok {
			t.Errorf("collector %q is absent from docs/metrics-contract.json", name)
			continue
		}
		if actual = normalizeMetric(actual); !reflect.DeepEqual(actual, expected) {
			t.Errorf("metric %q metadata drift:\n got  %+v\n want %+v", name, actual, expected)
		}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("contract metric %q was not exported after exercising every hook", name)
		}
	}
}

func TestReleasedV1320MetricContractRemainsStable(t *testing.T) {
	var released releasedMetricDocument
	decodeContractFile(t, contractPath("internal", "observability", "testdata", "v1.32.0-metrics.json"), &released)
	if released.Tag != "v1.32.0" || released.Commit != "6bb76a08846150663d7eeb9661edb718ef357a7c" {
		t.Fatalf("released baseline = %s@%s, want v1.32.0@6bb76a08846150663d7eeb9661edb718ef357a7c", released.Tag, released.Commit)
	}
	if len(released.Metrics) != 28 {
		t.Fatalf("v1.32.0 metric family count = %d, want 28", len(released.Metrics))
	}

	m := NewMetrics()
	exerciseAllMetrics(m)
	current := gatheredMetricContract(t, m)
	for _, frozen := range released.Metrics {
		actual, ok := current[frozen.Name]
		if !ok {
			t.Errorf("released metric %q was removed", frozen.Name)
			continue
		}
		if !reflect.DeepEqual(normalizeMetric(actual), normalizeMetric(frozen)) {
			t.Errorf("released metric %q changed without compatibility handling:\n got  %+v\n want %+v", frozen.Name, normalizeMetric(actual), normalizeMetric(frozen))
		}
	}
}

func TestMetricReferenceDocumentsMatchContract(t *testing.T) {
	contract := loadMetricContract(t)
	want := make(map[string]struct{}, len(contract.Metrics))
	for _, metric := range contract.Metrics {
		want[metric.Name] = struct{}{}
	}

	re := regexp.MustCompile("`(jul_[a-z0-9_:]+)`")
	for _, rel := range []string{"docs/observability.md", "docs/core-http.md"} {
		data, err := os.ReadFile(contractPath(filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		seen := map[string]struct{}{}
		for _, match := range re.FindAllStringSubmatch(string(data), -1) {
			name := match[1]
			if _, ok := want[name]; !ok {
				t.Errorf("%s references unknown or stale metric %q", rel, name)
			}
			seen[name] = struct{}{}
		}
		for name := range want {
			if _, ok := seen[name]; !ok {
				t.Errorf("%s does not document contract metric %q", rel, name)
			}
		}
	}
}
