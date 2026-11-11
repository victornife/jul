// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

type InspectionMetricsSnapshot struct {
	BaseBytes, BufferBytes, WAFStateBytes uint64
	MaxBytes                              uint64
	ActiveLeases                          uint64
	ActiveEvaluations, WaitingEvaluations uint64
	EvaluationLimit                       int
	AbandonedEvaluations                  uint64
}

type inspectionCollector struct {
	source atomic.Pointer[func() InspectionMetricsSnapshot]

	bytes            *prometheus.GaugeVec
	bytesLimit       prometheus.Gauge
	leases           prometheus.Gauge
	evaluations      *prometheus.GaugeVec
	evaluationsLimit prometheus.Gauge
	rejections       *prometheus.CounterVec
	bodyTimeouts     *prometheus.CounterVec
	unslottedLogging prometheus.Counter
	abandoned        prometheus.Gauge
}

func newInspectionCollector() *inspectionCollector {
	c := &inspectionCollector{
		bytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "jul_inspection_bytes",
			Help: "Current process-wide inspection reservations by class; WAF state is an estimate, not a measured heap bound.",
		}, []string{"class"}),
		bytesLimit: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jul_inspection_bytes_limit",
			Help: "Configured process-wide inspection byte ceiling.",
		}),
		leases: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jul_inspection_leases",
			Help: "Current outer request leases held by the inspection governor.",
		}),
		evaluations: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "jul_inspection_evaluations",
			Help: "Current process-wide inspection evaluations by state.",
		}, []string{"state"}),
		evaluationsLimit: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jul_inspection_evaluations_limit",
			Help: "Effective process-wide inspection evaluation slot limit.",
		}),
		rejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_inspection_rejections_total",
			Help: "Inspection admission rejections in enforce mode and would-reject decisions in observe mode.",
		}, []string{"reason", "mode"}),
		bodyTimeouts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_inspection_body_timeouts_total",
			Help: "Inspected-body receive timeout outcomes, when body integration is enabled.",
		}, []string{"protocol", "result"}),
		unslottedLogging: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jul_inspection_unslotted_logging_total",
			Help: "Phase-5 logging evaluations that proceeded without an evaluation slot.",
		}),
		abandoned: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jul_inspection_abandoned_evaluations",
			Help: "Evaluations still running after their request context ended.",
		}),
	}
	for _, class := range []string{"base", "buffer", "waf_state"} {
		c.bytes.WithLabelValues(class)
	}
	for _, state := range []string{"active", "waiting"} {
		c.evaluations.WithLabelValues(state)
	}
	for _, reason := range []string{"bytes", "waf_state", "evaluation"} {
		for _, mode := range []string{"enforce", "observe"} {
			c.rejections.WithLabelValues(reason, mode)
		}
	}
	for _, protocol := range []string{"h1", "h2", "h3"} {
		for _, result := range []string{"timeout", "would_timeout", "unsupported"} {
			c.bodyTimeouts.WithLabelValues(protocol, result)
		}
	}
	return c
}

func (c *inspectionCollector) Describe(ch chan<- *prometheus.Desc) {
	c.bytes.Describe(ch)
	c.bytesLimit.Describe(ch)
	c.leases.Describe(ch)
	c.evaluations.Describe(ch)
	c.evaluationsLimit.Describe(ch)
	c.rejections.Describe(ch)
	c.bodyTimeouts.Describe(ch)
	c.unslottedLogging.Describe(ch)
	c.abandoned.Describe(ch)
}

func (c *inspectionCollector) Collect(ch chan<- prometheus.Metric) {
	if source := c.source.Load(); source != nil && *source != nil {
		s := (*source)()
		c.bytes.WithLabelValues("base").Set(float64(s.BaseBytes))
		c.bytes.WithLabelValues("buffer").Set(float64(s.BufferBytes))
		c.bytes.WithLabelValues("waf_state").Set(float64(s.WAFStateBytes))
		c.bytesLimit.Set(float64(s.MaxBytes))
		c.leases.Set(float64(s.ActiveLeases))
		c.evaluations.WithLabelValues("active").Set(float64(s.ActiveEvaluations))
		c.evaluations.WithLabelValues("waiting").Set(float64(s.WaitingEvaluations))
		c.evaluationsLimit.Set(float64(s.EvaluationLimit))
		c.abandoned.Set(float64(s.AbandonedEvaluations))
	}
	c.bytes.Collect(ch)
	c.bytesLimit.Collect(ch)
	c.leases.Collect(ch)
	c.evaluations.Collect(ch)
	c.evaluationsLimit.Collect(ch)
	c.rejections.Collect(ch)
	c.bodyTimeouts.Collect(ch)
	c.unslottedLogging.Collect(ch)
	c.abandoned.Collect(ch)
}

func (m *Metrics) SetInspectionSource(source func() InspectionMetricsSnapshot) {
	if m != nil && m.inspection != nil {
		m.inspection.source.Store(&source)
	}
}

func (m *Metrics) ObserveInspectionRejection(reason, mode string) {
	if m == nil || m.inspection == nil {
		return
	}
	if (reason == "bytes" || reason == "waf_state" || reason == "evaluation") &&
		(mode == "enforce" || mode == "observe") {
		m.inspection.rejections.WithLabelValues(reason, mode).Inc()
	}
}

func (m *Metrics) ObserveInspectionBodyTimeout(protocol, result string) {
	if m == nil || m.inspection == nil {
		return
	}
	if (protocol == "h1" || protocol == "h2" || protocol == "h3") &&
		(result == "timeout" || result == "would_timeout" || result == "unsupported") {
		m.inspection.bodyTimeouts.WithLabelValues(protocol, result).Inc()
	}
}

func (m *Metrics) ObserveInspectionUnslottedLogging() {
	if m != nil && m.inspection != nil {
		m.inspection.unslottedLogging.Inc()
	}
}
