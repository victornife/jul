// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/middleware"
)

func TestNewMetricsDefaults(t *testing.T) {
	m := NewMetrics()
	if m == nil {
		t.Fatal("NewMetrics returned nil")
	}
	if m.startTime.IsZero() {
		t.Error("startTime not set")
	}
	if m.hostLabelEnabled.Load() {
		t.Error("hostLabel should be off by default")
	}
}

func TestHTTPHostLabelsDefaultToBoundedOther(t *testing.T) {
	metrics := NewMetrics(WithHostLabel(true))
	handler := metrics.Middleware(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) }))
	for _, host := range []string{"one.attacker.test", "two.attacker.test"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil))
	}
	families, err := metrics.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "jul_http_requests_total" {
			continue
		}
		if len(family.GetMetric()) != 1 {
			t.Fatalf("raw host values created %d series", len(family.GetMetric()))
		}
		for _, label := range family.GetMetric()[0].GetLabel() {
			if label.GetName() == "host" && label.GetValue() != "_other" {
				t.Fatalf("unbounded host label=%q", label.GetValue())
			}
		}
	}
}

func TestOptInHTTPRouteLabelsUseRoutingProjection(t *testing.T) {
	metrics := NewMetrics(WithHostLabel(true), WithRouteLabel(true))
	handler := metrics.Middleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		middleware.SetHTTPMetricLabels(request, "api.example", "route-one")
		_, _ = writer.Write([]byte("body"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://arbitrary.attacker.test/path", nil))
	families, err := metrics.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, family := range families {
		if family.GetName() != "jul_http_requests_total" && family.GetName() != "jul_http_request_duration_seconds" && family.GetName() != "jul_http_response_bytes_total" {
			continue
		}
		seen[family.GetName()] = true
		labels := make(map[string]string)
		for _, label := range family.GetMetric()[0].GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}
		if labels["host"] != "api.example" || labels["route"] != "route-one" {
			t.Fatalf("%s labels=%v", family.GetName(), labels)
		}
		if family.GetName() == "jul_http_response_bytes_total" && family.GetMetric()[0].GetCounter().GetValue() != 4 {
			t.Fatal("route byte counter lost response bytes")
		}
	}
	if len(seen) != 3 {
		t.Fatalf("missing route metric families: %v", seen)
	}
}

func TestRouteMetricInventoryPrunesRetiredLabels(t *testing.T) {
	metrics := NewMetrics(WithHostLabel(true), WithRouteLabel(true))
	oldID, newID := "old-route", "new-route"
	metrics.SetHTTPLabelInventory([]config.ServerConfig{{ServerNames: []string{"old.example"}, Locations: []config.LocationConfig{{RouteID: &oldID}}}})
	handler := metrics.Middleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		middleware.SetHTTPMetricLabels(request, "old.example", oldID)
		_, _ = writer.Write([]byte("body"))
	}))
	request := httptest.NewRequest(http.MethodGet, "http://old.example/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	metrics.SetHTTPLabelInventory([]config.ServerConfig{{ServerNames: []string{"new.example"}, Locations: []config.LocationConfig{{RouteID: &newID}}}})
	handler.ServeHTTP(httptest.NewRecorder(), request)
	families, err := metrics.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "jul_http_requests_total" && family.GetName() != "jul_http_request_duration_seconds" && family.GetName() != "jul_http_response_bytes_total" {
			continue
		}
		if len(family.GetMetric()) != 1 {
			t.Fatalf("retired series retained in %s", family.GetName())
		}
		for _, label := range family.GetMetric()[0].GetLabel() {
			if label.GetName() == "host" && label.GetValue() != "_other" {
				t.Fatal("retired host recreated")
			}
			if label.GetName() == "route" && label.GetValue() != "_unidentified" {
				t.Fatal("retired route recreated")
			}
		}
	}
	if got := metrics.Snapshot().HTTPResponseBytesTotal; got != 8 {
		t.Fatalf("lifetime node bytes reset after label pruning: %v", got)
	}
	if got := metrics.Snapshot().RequestsTotal; got != 2 {
		t.Fatalf("lifetime node requests reset after label pruning: %v", got)
	}
}

func TestRouteMetricsSnapshotAggregatesBoundedCounters(t *testing.T) {
	metrics := NewMetrics(WithRouteLabel(true), WithHostLabel(true))
	handler := metrics.Middleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		middleware.SetHTTPMetricLabels(request, "api.example", "route-one")
		if request.URL.Path == "/failure" {
			writer.WriteHeader(http.StatusInternalServerError)
		}
		_, _ = writer.Write([]byte("body"))
	}))
	for _, path := range []string{"/success", "/failure"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.test"+path, nil))
	}
	snapshot := metrics.Snapshot()
	if !snapshot.RouteMetricsEnabled || len(snapshot.RouteMetrics) != 1 {
		t.Fatalf("route snapshot=%+v", snapshot.RouteMetrics)
	}
	row := snapshot.RouteMetrics[0]
	if row.RouteID != "route-one" || row.Requests != 2 || row.Errors != 1 || row.ResponseBytes != 8 || row.DurationCount != 2 {
		t.Fatalf("row=%+v", row)
	}
	if disabled := NewMetrics().Snapshot(); disabled.RouteMetricsEnabled || len(disabled.RouteMetrics) != 0 {
		t.Fatal("disabled mode fabricated route data")
	}
}

func TestWithHostLabel(t *testing.T) {
	m := NewMetrics(WithHostLabel(true))
	if !m.hostLabelEnabled.Load() {
		t.Error("hostLabel should be on")
	}
}

func TestMetricsHandler(t *testing.T) {
	m := NewMetrics()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !contains(body, "jul_listener_conns") {
		t.Error("expected jul_listener_conns in output")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestMetricsMiddlewareIncrements(t *testing.T) {
	m := NewMetrics(WithHostLabel(true))
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("ok"))
	}))
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestObserveCompression(t *testing.T) {
	m := NewMetrics()
	m.ObserveCompression("gzip")
}

func TestObserveRateLimited(t *testing.T) {
	m := NewMetrics()
	m.ObserveRateLimited("ip")
}

func TestObserveAuthDecision(t *testing.T) {
	m := NewMetrics()
	m.ObserveAuthDecision("basic", "allow")
	m.ObserveAuthDecision("jwt", "deny")
}

func TestObserveWAFEvent(t *testing.T) {
	m := NewMetrics()
	m.ObserveWAFEvent("block", "rule-1")
}

func TestObserveBackendHealth(t *testing.T) {
	m := NewMetrics()
	m.ObserveBackendHealth("pool1", "127.0.0.1:3000", true)
	m.ObserveBackendHealth("pool1", "127.0.0.1:3000", false)
}

func TestObserveUpstreamBackends(t *testing.T) {
	m := NewMetrics()
	m.ObserveUpstreamBackends("pool1", 3)
}

func TestObserveDiscoveryError(t *testing.T) {
	m := NewMetrics()
	m.ObserveDiscoveryError("pool1")
}

func TestObserveProbe(t *testing.T) {
	m := NewMetrics()
	m.ObserveProbe("pool1", "http", true, 10*time.Millisecond)
	m.ObserveProbe("pool1", "http", false, 0)
}

func TestObserveGRPCTranscode(t *testing.T) {
	m := NewMetrics()
	m.ObserveGRPCTranscode("/package.Service/Method", "200")
}

func TestObserveGRPCTranscodeStreamMsg(t *testing.T) {
	m := NewMetrics()
	m.ObserveGRPCTranscodeStreamMsg("/package.Service/Method", "sent")
}

func TestObserveGRPCProxyStream(t *testing.T) {
	m := NewMetrics()
	m.ObserveGRPCProxyStream()
}

func TestObservePluginInvocation(t *testing.T) {
	m := NewMetrics()
	m.ObservePluginInvocation("myplugin", "continue", 5*time.Millisecond)
	m.ObservePluginInvocation("myplugin", "error", 0)
}

func TestObservePluginPanic(t *testing.T) {
	m := NewMetrics()
	m.ObservePluginPanic("myplugin")
}

func TestHTTP3ConnDelta(t *testing.T) {
	m := NewMetrics()
	m.HTTP3ConnDelta(2)
	m.HTTP3ConnDelta(-1)
}

func TestObserveAltSvcTransition(t *testing.T) {
	m := NewMetrics()
	m.ObserveAltSvcTransition("advertise")
	m.ObserveAltSvcTransition("clear")
}

func TestObserveMTLSHandshake(t *testing.T) {
	m := NewMetrics()
	m.ObserveMTLSHandshake("verified")
	m.ObserveMTLSHandshake("rejected")
}

func TestStreamMetrics(t *testing.T) {
	m := NewMetrics()
	m.StreamConnDelta("tcp", 1)
	m.StreamConnDelta("tcp", -1)
	m.StreamConnDelta("udp", 1)
	m.ObserveStreamBytes("tcp", "up", 1024)
	m.ObserveStreamBytes("tcp", "down", 2048)
	m.StreamUDPEvicted("idle")
	m.StreamUDPEvicted("lru")
	m.StreamUDPRejected()
}

func TestTrafficSnapshot(t *testing.T) {
	m := NewMetrics()
	snap := m.TrafficSnapshot()
	if snap.Hosts == nil {
		t.Error("expected non-nil hosts map")
	}
}

func TestSnapshot(t *testing.T) {
	m := NewMetrics()
	snap := m.Snapshot()
	if !snap.Available {
		t.Error("expected available")
	}
	if snap.UptimeSeconds < 0 {
		t.Error("expected non-negative uptime")
	}
}

func TestObserveCertExpiry(t *testing.T) {
	m := NewMetrics()
	m.ObserveCertExpiry("example.com", time.Now().Add(time.Hour))
	// Renewal should be observed only when expiry advances.
	m.ObserveCertExpiry("example.com", time.Now().Add(2*time.Hour))
}

func TestObserveCertRenewalError(t *testing.T) {
	m := NewMetrics()
	m.ObserveCertRenewalError("example.com", "timeout")
}

func TestRequestSamplesAndFailingRoutes(t *testing.T) {
	m := NewMetrics()
	if len(m.RequestSamples()) != 0 {
		t.Error("expected empty samples initially")
	}
	if len(m.FailingRoutes(10)) != 0 {
		t.Error("expected empty failing routes initially")
	}
}

func TestUpstreamHealthHistory(t *testing.T) {
	m := NewMetrics()
	if len(m.UpstreamHealthHistory()) != 0 {
		t.Error("expected empty health history initially")
	}
}

func TestCertRenewalHistory(t *testing.T) {
	m := NewMetrics()
	if len(m.CertRenewalHistory()) != 0 {
		t.Error("expected empty cert history initially")
	}
}

func TestConnState(t *testing.T) {
	m := NewMetrics()
	conn := &net.TCPConn{}
	m.ConnState(conn, http.StateNew)
	m.ConnState(conn, http.StateClosed)
	// Hijacked should also decrement.
	m.ConnState(conn, http.StateHijacked)
}
