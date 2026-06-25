package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── request samples (Milestone 5.1) ──────────────────────────────────────────

func TestSanitizePathRedactsSensitiveSegments(t *testing.T) {
	cases := map[string]string{
		"/users/12345/profile":                        "/users/:id/profile",
		"/orgs/acme/users/jane@example.com":           "/orgs/acme/users/:email",
		"/files/550e8400-e29b-41d4-a716-446655440000": "/files/:id",
		"/sessions/a1b2c3d4e5f6a7b8c9d0e1f2":          "/sessions/:id",
		"/keys/sk-live-AbC123dEf456GhI789jkl":         "/keys/:token",
		"/dashboard/settings":                         "/dashboard/settings",
		"/":                                           "/",
		"/api/v1/health":                              "/api/v1/health",
	}
	for in, want := range cases {
		if got := sanitizePath(in); got != want {
			t.Errorf("sanitizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizePathStripsQueryThenRedacts(t *testing.T) {
	if got := sanitizePath("/users/99999?token=secret#frag"); got != "/users/:id" {
		t.Errorf("got %q, want /users/:id", got)
	}
}

func TestRequestSampleBufferNewestFirst(t *testing.T) {
	b := newRequestSampleBuffer(8)
	for i := 0; i < 3; i++ {
		b.record(RequestSample{Method: http.MethodGet, Path: "/p", Status: 200 + i})
	}
	got := b.snapshot()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// Newest record (Status 202) must be first.
	if got[0].Status != 202 || got[2].Status != 200 {
		t.Fatalf("order = [%d..%d], want newest-first", got[0].Status, got[2].Status)
	}
}

func TestRequestSampleBufferRingOverwrites(t *testing.T) {
	b := newRequestSampleBuffer(4)
	for i := 0; i < 10; i++ {
		b.record(RequestSample{Path: "/p", Status: i})
	}
	got := b.snapshot()
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4 (capacity)", len(got))
	}
	// Last four written were statuses 6,7,8,9 → newest-first 9,8,7,6.
	if got[0].Status != 9 || got[3].Status != 6 {
		t.Fatalf("retained = [%d..%d], want 9..6", got[0].Status, got[3].Status)
	}
}

func TestRequestSampleRedactsOriginAndUA(t *testing.T) {
	b := newRequestSampleBuffer(4)
	b.record(RequestSample{
		Path:      "/secret?token=abc#frag",
		Origin:    "https://app.example.com:8443",
		UserAgent: "Mozilla/5.0 (X11) Chrome/120 Safari/537",
	})
	s := b.snapshot()[0]
	if strings.ContainsAny(s.Path, "?#") {
		t.Errorf("path %q still carries query/fragment", s.Path)
	}
	if s.UserAgent != "Chrome" {
		t.Errorf("ua family = %q, want Chrome", s.UserAgent)
	}
	if strings.Contains(s.Origin, "token") {
		t.Errorf("origin %q must not carry credentials", s.Origin)
	}
}

func TestSanitizePath(t *testing.T) {
	cases := map[string]string{
		"":                        "/",
		"/a/b":                    "/a/b",
		"/a?x=1":                  "/a",
		"/a#frag":                 "/a",
		strings.Repeat("/x", 200): strings.Repeat("/x", 200)[:samplePathMaxLen],
	}
	for in, want := range cases {
		if got := sanitizePath(in); got != want {
			t.Errorf("sanitizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUserAgentFamily(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"curl/8.4.0":                "curl",
		"Wget/1.21":                 "wget",
		"Googlebot/2.1":             "bot",
		"... Edg/120 ...":           "Edge",
		"... Firefox/121 ...":       "Firefox",
		"... Chrome/120 Safari ...": "Chrome",
		"... Version/17 Safari ...": "Safari",
	}
	for in, want := range cases {
		if got := userAgentFamily(in); got != want {
			t.Errorf("userAgentFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── failing routes (Milestone 5.2) ───────────────────────────────────────────

func TestRouteFailuresExcludeHealthyPaths(t *testing.T) {
	tr := newRouteFailureTracker(16)
	tr.record("/ok", 200, 5)
	tr.record("/ok", 200, 6)
	tr.record("/bad", 500, 12)
	out := tr.snapshot(0)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1 (only failing path)", len(out))
	}
	if out[0].Path != "/bad" || out[0].Status5xx != 1 {
		t.Fatalf("got %+v, want /bad with one 5xx", out[0])
	}
}

func TestRouteFailuresRankingAndErrorRate(t *testing.T) {
	tr := newRouteFailureTracker(16)
	// /five: one 5xx; /four: two 4xx. 5xx must rank first.
	tr.record("/five", 503, 30)
	tr.record("/four", 404, 10)
	tr.record("/four", 404, 11)
	out := tr.snapshot(0)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Path != "/five" {
		t.Fatalf("first = %q, want /five (5xx ranks above 4xx)", out[0].Path)
	}
	if out[1].ErrorRate != 1.0 {
		t.Errorf("/four error_rate = %v, want 1.0", out[1].ErrorRate)
	}
}

func TestRouteFailuresTopNLimit(t *testing.T) {
	tr := newRouteFailureTracker(64)
	for i := 0; i < 5; i++ {
		tr.record("/p"+string(rune('a'+i)), 500, float64(i))
	}
	if got := tr.snapshot(2); len(got) != 2 {
		t.Fatalf("snapshot(2) len = %d, want 2", len(got))
	}
}

func TestRouteFailuresOverflowToOther(t *testing.T) {
	tr := newRouteFailureTracker(2)
	tr.record("/a", 500, 1)
	tr.record("/b", 500, 1)
	tr.record("/c", 500, 1) // exceeds cap → folded into "(other)"
	out := tr.snapshot(0)
	var hasOther bool
	for _, rf := range out {
		if rf.Path == "(other)" {
			hasOther = true
		}
	}
	if !hasOther {
		t.Fatalf("expected an (other) bucket, got %+v", out)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	if got := percentile(nil, 0.95); got != 0 {
		t.Errorf("empty percentile = %v, want 0", got)
	}
	vals := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	// Nearest-rank: idx = int(0.95 * (10-1)) = int(8.55) = 8 → 90.
	if got := percentile(vals, 0.95); got != 90 {
		t.Errorf("p95 = %v, want 90", got)
	}
	if got := percentile(vals, 0.5); got != 50 {
		t.Errorf("p50 = %v, want 50", got)
	}
}

// ── health history (Milestone 5.5) ───────────────────────────────────────────

func TestHealthHistorySeedsWithoutTransition(t *testing.T) {
	tr := newHealthHistoryTracker()
	tr.record("pool", "be1", true) // seed
	tr.record("pool", "be1", true) // still healthy → no transition
	out := tr.snapshot()
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].Transitions != 0 {
		t.Errorf("transitions = %d, want 0 after steady probes", out[0].Transitions)
	}
	if !out[0].Healthy {
		t.Error("backend should be healthy")
	}
}

func TestHealthHistoryCountsTransitions(t *testing.T) {
	tr := newHealthHistoryTracker()
	tr.record("pool", "be1", true)  // seed up
	tr.record("pool", "be1", false) // down (1)
	tr.record("pool", "be1", true)  // up (2)
	out := tr.snapshot()
	if out[0].Transitions != 2 {
		t.Fatalf("transitions = %d, want 2", out[0].Transitions)
	}
	if out[0].LastUp == nil || out[0].LastDown == nil {
		t.Error("both LastUp and LastDown should be set")
	}
}

func TestHealthHistoryFlapping(t *testing.T) {
	tr := newHealthHistoryTracker()
	tr.record("pool", "be1", true) // seed up
	// Toggle starting from down so all four records flip the state, yielding
	// flapThreshold transitions within the flap window → flapping.
	for i := 0; i < 4; i++ {
		tr.record("pool", "be1", i%2 == 1)
	}
	if !tr.snapshot()[0].Flapping {
		t.Error("backend with >= flapThreshold recent transitions should flap")
	}
}

// ── cert history (Milestone 5.6) ─────────────────────────────────────────────

func TestCertHistoryRenewalAndError(t *testing.T) {
	tr := newCertHistoryTracker()
	expiry := time.Now().Add(60 * 24 * time.Hour)
	tr.recordRenewal("example.com", expiry, "Let's Encrypt", false)
	tr.recordError("example.com", "dns challenge failed")

	out := tr.snapshot()
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	h := out[0]
	if h.Issuer != "Let's Encrypt" {
		t.Errorf("issuer = %q", h.Issuer)
	}
	if h.LastError != "dns challenge failed" {
		t.Errorf("last error = %q", h.LastError)
	}
	if h.DaysLeft < 58 || h.DaysLeft > 60 {
		t.Errorf("days left = %d, want ~59", h.DaysLeft)
	}
	// Recent is newest-first: the error must precede the renewal.
	if len(h.Recent) != 2 || h.Recent[0].Success {
		t.Fatalf("recent = %+v, want error first", h.Recent)
	}
}

// ── Metrics wiring (public surface used by the admin Deps hooks) ─────────────

func TestMetricsRequestSampleAndFailingRouteWiring(t *testing.T) {
	m := NewMetrics()
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.Header.Set("User-Agent", "curl/8")
	h.ServeHTTP(httptest.NewRecorder(), req)

	samples := m.RequestSamples()
	if len(samples) != 1 || samples[0].Path != "/boom" || samples[0].Status != 500 {
		t.Fatalf("samples = %+v, want one /boom 500", samples)
	}
	if samples[0].UserAgent != "curl" {
		t.Errorf("ua = %q, want curl", samples[0].UserAgent)
	}
	routes := m.FailingRoutes(10)
	if len(routes) != 1 || routes[0].Path != "/boom" || routes[0].Status5xx != 1 {
		t.Fatalf("failing routes = %+v, want one /boom 5xx", routes)
	}
}

func TestMetricsUpstreamAndCertHistoryWiring(t *testing.T) {
	m := NewMetrics()
	m.ObserveBackendHealth("pool", "be1", true)
	m.ObserveBackendHealth("pool", "be1", false)
	if hist := m.UpstreamHealthHistory(); len(hist) != 1 || hist[0].Transitions != 1 {
		t.Fatalf("upstream history = %+v, want one transition", hist)
	}

	// First expiry seeds; a later expiry counts as a renewal.
	m.ObserveCertExpiry("example.com", time.Now().Add(24*time.Hour))
	m.ObserveCertExpiry("example.com", time.Now().Add(90*24*time.Hour))
	m.ObserveCertRenewalError("example.com", "rate limited")
	certs := m.CertRenewalHistory()
	if len(certs) != 1 {
		t.Fatalf("cert history len = %d, want 1", len(certs))
	}
	if certs[0].LastError != "rate limited" {
		t.Errorf("cert last error = %q", certs[0].LastError)
	}
}
