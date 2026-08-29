// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestHTTPMethodLabelBounded proves the client-controlled request method cannot
// drive unbounded cardinality on jul_http_requests_total: recognized methods are
// recorded verbatim, and every unrecognized token collapses to a single "other"
// series. HTTP permits arbitrary method tokens, so without this a hostile client
// could mint one series per novel method (times host times code).
func TestHTTPMethodLabelBounded(t *testing.T) {
	m := NewMetrics()
	serveMethod(m, http.MethodGet)
	serveMethod(m, http.MethodPost)
	serveMethod(m, "FROBNICATE")             // unknown but valid token
	serveMethod(m, strings.Repeat("X", 200)) // long unknown token
	serveMethod(m, "WHATEVER")               // another unknown token

	got := methodCounts(t, m)

	if got[http.MethodGet] != 1 {
		t.Errorf("GET series = %v, want 1", got[http.MethodGet])
	}
	if got[http.MethodPost] != 1 {
		t.Errorf("POST series = %v, want 1", got[http.MethodPost])
	}
	if got["other"] != 3 {
		t.Errorf("\"other\" series = %v, want 3 (three distinct unknown methods folded)", got["other"])
	}
	for leaked := range got {
		switch leaked {
		case http.MethodGet, http.MethodPost, "other":
		default:
			t.Errorf("unknown method leaked as its own series: %q", leaked)
		}
	}
}

// TestMethodLabelKnownSet spot-checks that every standard HTTP method is treated
// as known (passes through) and a non-standard one is folded, independently of
// the middleware, so the allow-list is exercised directly.
func TestMethodLabelKnownSet(t *testing.T) {
	for _, meth := range []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect,
		http.MethodOptions, http.MethodTrace,
	} {
		if got := methodLabel(meth); got != meth {
			t.Errorf("methodLabel(%q) = %q, want passthrough", meth, got)
		}
	}
	for _, meth := range []string{"", "get", "PROPFIND", "MKCOL", "FOO", "purge"} {
		if got := methodLabel(meth); got != "other" {
			t.Errorf("methodLabel(%q) = %q, want \"other\"", meth, got)
		}
	}
}

// TestMetricLabelPolicy is the cardinality-regression guard: it exercises every
// metric hook, then asserts each exported jul_* family carries exactly the label
// names in the frozen policy below (mirrored in docs/core-http.md). Adding a new
// metric or, more importantly, a new label — especially one derived from request
// input (path, query, user-agent, client IP, raw host) — fails this test, so any
// cardinality growth is a deliberate, reviewed decision rather than an accident.
func TestMetricLabelPolicy(t *testing.T) {
	m := NewMetrics()
	exerciseAllMetrics(m)

	got := labelPolicy(t, m)

	contract := loadMetricContract(t)
	want := make(map[string][]string, len(contract.Metrics))
	for _, metric := range contract.Metrics {
		want[metric.Name] = metric.Labels
	}

	for name, names := range got {
		wantNames, ok := want[name]
		if !ok {
			t.Errorf("metric %q is not in docs/metrics-contract.json; add it to the contract and docs/core-http.md, and confirm its labels are bounded", name)
			continue
		}
		if !slices.Equal(names, wantNames) {
			t.Errorf("metric %q labels = %v, want %v (label policy)", name, names, wantNames)
		}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("policy metric %q was not exported after exercising all hooks", name)
		}
	}
}

// serveMethod drives one request through m.Middleware with the given method.
func serveMethod(m *Metrics, method string) {
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "/x", nil))
}

// methodCounts returns the summed counter value of jul_http_requests_total per
// "method" label value.
func methodCounts(t *testing.T, m *Metrics) map[string]float64 {
	t.Helper()
	fams, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]float64{}
	for _, fam := range fams {
		if fam.GetName() != "jul_http_requests_total" {
			continue
		}
		for _, mt := range fam.GetMetric() {
			var method string
			for _, lp := range mt.GetLabel() {
				if lp.GetName() == "method" {
					method = lp.GetValue()
				}
			}
			out[method] += mt.GetCounter().GetValue()
		}
	}
	return out
}

// labelPolicy returns, for every exported jul_* metric family, its sorted set of
// label names.
func labelPolicy(t *testing.T, m *Metrics) map[string][]string {
	t.Helper()
	fams, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string][]string{}
	for _, fam := range fams {
		name := fam.GetName()
		if !strings.HasPrefix(name, "jul_") {
			continue // ignore the runtime go_*/process_* collectors
		}
		var names []string
		if mts := fam.GetMetric(); len(mts) > 0 {
			for _, lp := range mts[0].GetLabel() {
				names = append(names, lp.GetName())
			}
		}
		sort.Strings(names)
		out[name] = names
	}
	return out
}

// exerciseAllMetrics touches every metric hook once so that each collector
// exports at least one series for the label-policy audit.
func exerciseAllMetrics(m *Metrics) {
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Cache", "HIT")
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	m.ObserveCompression("gzip")
	m.ObserveCacheRevalidation("stored")
	m.ObserveRateLimited("ip")
	m.ObserveClientAddrDerivation("xff", "accepted")
	m.ObserveAuthDecision("jwt", "allow")
	m.ObserveWAFEvent("block", "942100")
	m.ObserveEgressDecision("jwks", "allow", "", 1)
	m.ObserveBackendHealth("pool-a", "10.0.0.1:80", true)
	m.ObserveBackendsHealthy("pool-a", 2)
	m.ObserveAdmissionRejected("pool-a", "proxy_overloaded")
	m.ObserveRetryAttempt("pool-a", "attempts_exhausted")
	m.ObserveRetryBudgetDenied("pool-a")
	m.ObserveCircuitTransition("pool-a", "circuit_open")
	m.ObserveTransportRetired("graceful")
	m.SetUpstreamStatsSource(func() []UpstreamPoolStats {
		return []UpstreamPoolStats{{
			Name: "pool-a", Active: 3, Pending: 1, Connections: 2, Eligible: 2,
			ByState: map[string]int{
				"available": 2, "circuit_open": 1, "circuit_half_open": 0,
				"health_unhealthy": 0, "at_capacity": 0,
			},
		}}
	})
	m.ObserveUpstreamBackends("pool-a", 3)
	m.ObserveDiscoveryError("pool-a")
	m.ObserveProbe("pool-a", "http", true, time.Millisecond)
	m.ObserveGRPCTranscode("pkg.Svc/Method", "200")
	m.ObserveGRPCTranscodeStreamMsg("pkg.Svc/Method", "sent")
	m.ObserveGRPCProxyStream()
	m.ObservePluginInvocation("plug", "continue", time.Millisecond)
	m.ObservePluginPanic("plug")
	m.ConnState(nil, http.StateNew)
	m.HTTP3ConnDelta(1)
	m.ObserveMTLSHandshake("verified")
	m.StreamConnDelta("tcp", 1)
	m.ObserveStreamBytes("tcp", "up", 100)
	m.StreamUDPEvicted("idle")
	m.StreamUDPRejected()
	m.ObserveStreamDialFailure("tcp", "timeout")
	m.ObserveHTTPDialFailure("timeout")
	m.ObserveCertExpiry("example.com", time.Now().Add(24*time.Hour))
	// Reload and staged-restart metrics (P2-05).
	m.ReloadStarted()
	m.ObserveReload("admin", "applied_live", 42)
	m.ObserveReloadResult("applied_live", map[string]int64{"resolve": 1}, true, "resolve")
	m.ObserveStageRestart("created")
	m.SetPendingRestart(true)
	m.SetPendingRestart(false)
	m.SetConfigAuthorityDrift(true)
	m.SetConfigAuthorityDrift(false)
	m.ObserveConfigAuthorityDenied("config.raw")
	m.ObserveConfigAuthorityDenied("")
	m.ObserveManagedApplyFinalized("config.apply", "hot", "not_applied", "true")
	m.ObserveManagedApplyFinalizationError("restoration")
	m.ObserveManagedApplyHistory("config.apply", "recorded")
	m.SetManagedApplyRegistryEntries(1)
	m.ObserveManagedApplyLookup("finalizing")
	m.ObserveManagedApplyLookup("terminal")
}
