// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"
)

func TestStorageSnapshotAbsentUntilWiredOrEmpty(t *testing.T) {
	m := NewMetrics()
	if snap := m.Snapshot(); snap.Storage != nil || snap.StorageHints != nil {
		t.Fatalf("unwired storage = %+v / %+v", snap.Storage, snap.StorageHints)
	}
	m.SetStorageSource(func() []StorageHeadroom { return nil }, StorageHints{LowRatio: 0.1, CriticalRatio: 0.05})
	if snap := m.Snapshot(); snap.Storage != nil || snap.StorageHints != nil {
		t.Fatalf("no configured category must omit storage and hints, got %+v / %+v", snap.Storage, snap.StorageHints)
	}
}

func TestStorageSnapshotAndMetricOmitUnknownFields(t *testing.T) {
	m := NewMetrics()
	avail, total, ratio := 900.0, 1000.0, 0.9
	m.SetStorageSource(func() []StorageHeadroom {
		return []StorageHeadroom{
			{Category: "cache", State: "ok", AvailableBytes: &avail, TotalBytes: &total, AvailableRatio: &ratio, SharedWith: []string{"access_log"}},
			{Category: "access_log", State: "ok", AvailableBytes: &avail, TotalBytes: &total, AvailableRatio: &ratio, SharedWith: []string{"cache"}},
			{Category: "acme", State: "unavailable", Reason: "unsupported_platform"},
		}
	}, StorageHints{LowRatio: 0.1, CriticalRatio: 0.05})

	snap := m.Snapshot()
	if len(snap.Storage) != 3 || snap.StorageHints == nil || snap.StorageHints.LowRatio != 0.1 {
		t.Fatalf("snapshot storage = %+v hints = %+v", snap.Storage, snap.StorageHints)
	}

	got := storageSeries(t, m)
	want := map[string]float64{
		"access_log/available": 900, "access_log/total": 1000,
		"cache/available": 900, "cache/total": 1000,
	}
	if len(got) != len(want) {
		t.Fatalf("jul_storage_bytes series = %v, want %v (unknown fields must be absent, never 0)", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("jul_storage_bytes{%s} = %v, want %v", k, got[k], v)
		}
	}
}

func storageSeries(t *testing.T, m *Metrics) map[string]float64 {
	t.Helper()
	fams, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range fams {
		if f.GetName() != "jul_storage_bytes" {
			continue
		}
		for _, s := range f.GetMetric() {
			out[labelValue(s, "category")+"/"+labelValue(s, "kind")] = s.GetGauge().GetValue()
		}
	}
	return out
}

func TestStorageCollectorExportsNothingUnwired(t *testing.T) {
	if got := storageSeries(t, NewMetrics()); len(got) != 0 {
		t.Fatalf("unwired jul_storage_bytes series = %v", got)
	}
}

func TestWriteHealth(t *testing.T) {
	var h WriteHealth
	if n, f := h.Snapshot(); n != 0 || f {
		t.Fatalf("zero = (%d, %v)", n, f)
	}
	h.OK()
	if n := h.Fail(); n != 1 {
		t.Fatalf("Fail() = %d", n)
	}
	h.Fail()
	if n, f := h.Snapshot(); n != 2 || !f {
		t.Fatalf("after failures = (%d, %v)", n, f)
	}
	h.OK()
	if n, f := h.Snapshot(); n != 2 || f {
		t.Fatalf("after recovery = (%d, %v), want count kept and flag cleared", n, f)
	}
}

func TestFailureReportingWriterFeedsWriteHealth(t *testing.T) {
	var h WriteHealth
	var logs bytes.Buffer
	fail := &failureReportingWriter{w: failingWriter{err: errors.New("no space left on device")}, log: slog.New(slog.NewTextHandler(&logs, nil)), sink: "file", health: &h}
	_, _ = fail.Write([]byte("line\n"))
	if n, f := h.Snapshot(); n != 1 || !f {
		t.Fatalf("after failed write = (%d, %v)", n, f)
	}
	ok := &failureReportingWriter{w: &bytes.Buffer{}, health: &h}
	_, _ = ok.Write([]byte("line\n"))
	if n, f := h.Snapshot(); n != 1 || f {
		t.Fatalf("after successful write = (%d, %v)", n, f)
	}
}
