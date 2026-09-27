// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// StorageHeadroom is the bounded advisory headroom of one Jul-owned storage
// category (#437). It never carries a path, mount point, volume, or device
// name: Category is a closed set (cache, access_log, audit_log, config,
// config_history, plugin_upload, acme) and Reason a closed set of storagefs
// reasons. A nil byte/ratio field means unavailable, never zero.
type StorageHeadroom struct {
	Category string `json:"category"`
	// State is ok, low, critical, unavailable or error. low/critical are
	// advisory hints against StorageHints; they never affect readiness.
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// AvailableBytes is what an unprivileged writer can still allocate on the
	// filesystem holding this category; TotalBytes is that filesystem's size.
	AvailableBytes *float64 `json:"availableBytes,omitempty"`
	TotalBytes     *float64 `json:"totalBytes,omitempty"`
	// AvailableRatio is AvailableBytes/TotalBytes, present only when both are
	// real and TotalBytes > 0.
	AvailableRatio *float64 `json:"availableRatio,omitempty"`
	// PendingCreation reports that the configured location does not exist yet;
	// capacity is that of the nearest existing parent it will be created in.
	PendingCreation bool `json:"pendingCreation,omitempty"`
	// SharedWith lists (sorted) the other categories measured on the same
	// filesystem, so an operator knows filling one starves the others.
	SharedWith []string `json:"sharedWith,omitempty"`
	// WriteFailures is the subsystem's cumulative write-failure count since
	// start, present only for subsystems that track one. WritesFailing reports
	// that the subsystem's most recent write failed — the "already failing"
	// condition, distinct from advisory low headroom.
	WriteFailures *float64 `json:"writeFailures,omitempty"`
	WritesFailing bool     `json:"writesFailing,omitempty"`
}

// StorageHints echoes the generic advisory thresholds (available fraction)
// the states were computed against, so the Console never hardcodes them.
type StorageHints struct {
	LowRatio      float64 `json:"lowRatio"`
	CriticalRatio float64 `json:"criticalRatio"`
}

// StorageSource returns the current headroom of every configured Jul-owned
// storage category. It is called per StatsSnapshot and per scrape.
type StorageSource func() []StorageHeadroom

type storageSource struct {
	src   StorageSource
	hints StorageHints
}

// storageCollector exports jul_storage_bytes{category,kind} at scrape time.
// Both labels are closed sets; a field the platform cannot report is simply
// absent rather than exported as zero.
type storageCollector struct {
	source atomic.Pointer[storageSource]
	bytes  *prometheus.Desc
}

func newStorageCollector() *storageCollector {
	return &storageCollector{
		bytes: prometheus.NewDesc("jul_storage_bytes",
			"Capacity of the filesystem holding a Jul-owned storage category, labeled by category (cache/access_log/audit_log/config/config_history/plugin_upload/acme) and kind (available/total). Absent when the platform cannot report it.",
			[]string{"category", "kind"}, nil),
	}
}

// SetStorageSource wires the Jul-owned storage headroom reader (#437).
func (m *Metrics) SetStorageSource(src StorageSource, hints StorageHints) {
	m.storage.source.Store(&storageSource{src: src, hints: hints})
}

// storageSnapshot returns the current categories and hints, or nil before a
// source is wired or when no storage category is configured.
func (m *Metrics) storageSnapshot() ([]StorageHeadroom, *StorageHints) {
	s := m.storage.source.Load()
	if s == nil {
		return nil, nil
	}
	cats := s.src()
	if len(cats) == 0 {
		return nil, nil
	}
	hints := s.hints
	return cats, &hints
}

func (c *storageCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.bytes }

func (c *storageCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.source.Load()
	if s == nil {
		return
	}
	for _, h := range s.src() {
		if h.AvailableBytes != nil {
			ch <- prometheus.MustNewConstMetric(c.bytes, prometheus.GaugeValue, *h.AvailableBytes, h.Category, "available")
		}
		if h.TotalBytes != nil {
			ch <- prometheus.MustNewConstMetric(c.bytes, prometheus.GaugeValue, *h.TotalBytes, h.Category, "total")
		}
	}
}

// WriteHealth tracks one storage writer's failures for the headroom view.
// Recovery costs a load on the success path and a store only on the
// failing→ok transition, so a hot writer does not contend on the flag.
type WriteHealth struct {
	failures atomic.Int64
	failing  atomic.Bool
}

// Fail records one failed write and returns the running failure count.
func (h *WriteHealth) Fail() int64 {
	h.failing.Store(true)
	return h.failures.Add(1)
}

// OK records one successful write.
func (h *WriteHealth) OK() {
	if h.failing.Load() {
		h.failing.Store(false)
	}
}

// Snapshot returns the cumulative failure count and whether the most recent
// write failed.
func (h *WriteHealth) Snapshot() (failures int64, failing bool) {
	return h.failures.Load(), h.failing.Load()
}

// AccessLogFileWriteHealth is the process-lifetime write health of the
// access-log file sink, shared by every sink generation so a reload does not
// reset it.
var AccessLogFileWriteHealth WriteHealth
