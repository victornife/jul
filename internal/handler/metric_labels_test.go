// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/observability"
	"jul/internal/router"
	"jul/internal/upstream"
)

func TestHTTPRouteAndHostSeriesBoundedByConfiguration(t *testing.T) {
	id := "configured-route"
	configuration := &config.Config{Servers: []config.ServerConfig{{
		Listen: "test", ServerNames: []string{"api.example", "*.tenant.example"},
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/api/"}, Root: ".", RouteID: &id},
			{Match: config.MatchConfig{Type: "exact", Path: "/anonymous"}, Root: "."},
		},
	}}}
	build := func(config.ServerConfig, config.LocationConfig) (http.Handler, error) {
		return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, "body") }), nil
	}
	routes, err := router.New(configuration, map[string]router.Builder{router.ActionStatic: build}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		metrics := observability.NewMetrics(observability.WithHostLabel(true), observability.WithRouteLabel(enabled))
		metrics.SetHTTPLabelInventory(configuration.Servers)
		handler := metrics.Middleware(routes.For("test"))
		for request := 0; request < 100; request++ {
			for _, target := range []string{
				fmt.Sprintf("http://host-%d.attacker.test/api/path-%d", request, request),
				fmt.Sprintf("http://tenant-%d.tenant.example/api/path-%d", request, request),
				fmt.Sprintf("http://host-%d.attacker.test/missing-%d", request, request),
				"http://api.example/anonymous",
			} {
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
			}
		}
		families, err := metrics.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() != "jul_http_requests_total" && family.GetName() != "jul_http_request_duration_seconds" && family.GetName() != "jul_http_response_bytes_total" {
				continue
			}
			if len(family.GetMetric()) > 4 {
				t.Fatalf("%s raw requests expanded series to %d", family.GetName(), len(family.GetMetric()))
			}
			for _, series := range family.GetMetric() {
				labels := make(map[string]string)
				for _, label := range series.GetLabel() {
					labels[label.GetName()] = label.GetValue()
				}
				if host, ok := labels["host"]; ok && host != "_other" && host != "api.example" {
					t.Fatalf("unbounded host=%q", host)
				}
				if enabled {
					if labels["route"] != id && labels["route"] != "_unidentified" && labels["route"] != "_unmatched" {
						t.Fatalf("unexpected route=%q", labels["route"])
					}
				} else if _, ok := labels["route"]; ok {
					t.Fatal("disabled mode changed released label shape")
				}
			}
		}
	}
}

func TestConfiguredIPv6MetricHostUsesCanonicalRouterName(t *testing.T) {
	configuration := config.ProxyTarget("http://127.0.0.1:8082", "127.0.0.1:8081")
	configuration.Servers[0].ServerNames = []string{"[::1]"}
	if err := config.Validate(configuration); err != nil {
		t.Fatal(err)
	}
	metrics := observability.NewMetrics(observability.WithHostLabel(true), observability.WithRouteLabel(true))
	metrics.SetHTTPLabelInventory(configuration.Servers)
	routes, err := router.New(configuration, map[string]router.Builder{router.ActionProxy: func(config.ServerConfig, config.LocationConfig) (http.Handler, error) {
		return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, "body") }), nil
	}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	metrics.Middleware(routes.For("127.0.0.1:8081")).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://[::1]:8081/", nil))
	for _, family := range []string{"jul_http_requests_total", "jul_http_request_duration_seconds", "jul_http_response_bytes_total"} {
		values := labelValuesFor(t, metrics, family, "host")
		if len(values) != 1 || values[0] != "[::1]" {
			t.Fatalf("%s host=%v, want [::1]", family, values)
		}
	}
}

// labelValuesFor returns the distinct values a metric family carries for one
// label name.
func labelValuesFor(t *testing.T, m *observability.Metrics, metric, label string) []string {
	t.Helper()
	families, err := m.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var out []string
	for _, f := range families {
		if f.GetName() != metric {
			continue
		}
		for _, series := range f.GetMetric() {
			for _, l := range series.GetLabel() {
				if l.GetName() == label {
					out = append(out, l.GetValue())
				}
			}
		}
	}
	return out
}

// labelNamesFor returns the label names a metric family carries.
func labelNamesFor(t *testing.T, m *observability.Metrics, metric string) []string {
	t.Helper()
	families, err := m.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != metric {
			continue
		}
		for _, series := range f.GetMetric() {
			var names []string
			for _, l := range series.GetLabel() {
				names = append(names, l.GetName())
			}
			return names
		}
	}
	return nil
}

// TestAdmissionRejectionLabelsEqualTheReasonEnum is the cardinality guarantee
// the taxonomy exists to provide.
//
// It lives here rather than in internal/observability because that package
// deliberately does not import internal/upstream — the hooks exist precisely to
// keep the dependency out — so this is the lowest package that can see both the
// enum and the exported label values.
//
// The assertion is equality, not containment. A label value outside the enum
// would be unbounded cardinality; an enum member never exported would be a
// reason an operator cannot alert on.
func TestAdmissionRejectionLabelsEqualTheReasonEnum(t *testing.T) {
	m := observability.NewMetrics()
	for _, r := range upstream.Reasons() {
		m.ObserveAdmissionRejected("pool", string(r))
	}

	got := labelValuesFor(t, m, "jul_upstream_admission_rejected_total", "reason")
	want := make([]string, 0, len(upstream.Reasons()))
	for _, r := range upstream.Reasons() {
		want = append(want, string(r))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("reason labels = %v, want exactly the enum %v", got, want)
	}
}

// TestCircuitTransitionLabelsEqualTheBackendStates pins the same property for
// the transition counter's destination state.
func TestCircuitTransitionLabelsEqualTheBackendStates(t *testing.T) {
	m := observability.NewMetrics()
	for _, s := range upstream.BackendStates() {
		m.ObserveCircuitTransition("pool", string(s))
	}

	got := labelValuesFor(t, m, "jul_upstream_circuit_transitions_total", "to")
	want := make([]string, 0, len(upstream.BackendStates()))
	for _, s := range upstream.BackendStates() {
		want = append(want, string(s))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("transition labels = %v, want exactly the state set %v", got, want)
	}
}

// TestNoMetricCarriesABackendAddress is the standing rule, asserted rather than
// remembered. A backend address is unbounded under pod churn, which is the
// defect this whole slice removed.
func TestNoMetricCarriesABackendAddress(t *testing.T) {
	m := observability.NewMetrics()
	for _, name := range []string{
		"jul_upstream_admission_rejected_total",
		"jul_upstream_circuit_transitions_total",
		"jul_upstream_retry_attempts_total",
		"jul_upstream_retry_budget_denied_total",
	} {
		if labels := labelNamesFor(t, m, name); slices.Contains(labels, "backend") {
			t.Errorf("metric %q carries a backend label: %v", name, labels)
		}
	}
}

// TestConnectionsGaugeTracksTheDialer pins that jul_upstream_connections is
// sourced from real dials and returns to zero.
//
// net/http enforces MaxConnsPerHost internally and exposes no live count, so
// this number exists only because Jul wraps its own dialer. A metric that
// reported a constant zero would be worse than no metric, and nothing else in
// the suite would notice.
func TestConnectionsGaugeTracksTheDialer(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	p, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "conns",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: strings.TrimPrefix(backend.URL, "http://"), Weight: 1}},
	}, "http")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer p.Close()

	if got := p.Stats().Connections; got != 0 {
		t.Fatalf("connections before any request = %d, want 0", got)
	}

	tr := newProxyTransport(config.LocationConfig{}, nil, 0, p)
	res, err := tr.RoundTrip(httptest.NewRequest(http.MethodGet, backend.URL, nil))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()

	if got := p.Stats().Connections; got != 1 {
		t.Fatalf("connections after one request = %d, want 1", got)
	}

	tr.CloseIdleConnections()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.Stats().Connections == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connections after closing idle conns = %d, want 0", p.Stats().Connections)
}
