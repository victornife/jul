package observability

import (
	"math"
	"sort"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// StatsSnapshot is a point-in-time, JSON-serializable view of the server's
// runtime metrics for the web console dashboard. It is intentionally a flat,
// presentation-oriented projection of the Prometheus registry rather than a
// raw exposition: the console polls it on a short interval and renders cards,
// gauges, and sparklines from these fields.
//
// All counter-derived fields are cumulative since process start except
// RequestsPerSec and ErrorRate, which are computed as deltas over the interval
// between successive Snapshot calls.
type StatsSnapshot struct {
	// Available is false on the zero value returned when no metrics source is
	// wired, letting the console distinguish "no data yet" from "all zeroes".
	Available bool `json:"available"`

	UptimeSeconds float64 `json:"uptimeSeconds"`

	RequestsTotal  float64 `json:"requestsTotal"`
	RequestsPerSec float64 `json:"requestsPerSec"`
	InFlight       float64 `json:"inFlight"`
	Connections    float64 `json:"connections"`

	// ErrorRate is the fraction (0..1) of requests in the most recent interval
	// that returned a 5xx status. StatusClasses holds cumulative counts keyed by
	// status class ("2xx", "3xx", "4xx", "5xx").
	ErrorRate     float64            `json:"errorRate"`
	StatusClasses map[string]float64 `json:"statusClasses"`

	LatencyAvgMs float64 `json:"latencyAvgMs"`
	LatencyP50Ms float64 `json:"latencyP50Ms"`
	LatencyP95Ms float64 `json:"latencyP95Ms"`
	LatencyP99Ms float64 `json:"latencyP99Ms"`

	// CacheHitRatio is HIT / (HIT+MISS+STALE+BYPASS), or 0 when no cache events
	// have been recorded. CacheEvents holds the cumulative per-state counts.
	CacheHitRatio float64            `json:"cacheHitRatio"`
	CacheEvents   map[string]float64 `json:"cacheEvents"`

	// Methods holds cumulative request counts by HTTP method (GET, POST, etc).
	Methods map[string]float64 `json:"methods"`

	// RateLimited holds cumulative counts of requests rejected by rate limiting,
	// keyed by the key kind (ip/header/jwt). It powers the Rate Limit editor's
	// observability section (Console v2 Milestone 3.3).
	RateLimited map[string]float64 `json:"rateLimited"`
}

// Snapshot gathers the private registry and projects it into a StatsSnapshot.
// It maintains a small amount of rolling state (guarded by statsMu) so it can
// report requests-per-second and a windowed error rate from the underlying
// monotonic counters. It is safe to call concurrently.
func (m *Metrics) Snapshot() StatsSnapshot {
	families, err := m.registry.Gather()
	if err != nil {
		// Gather only errors on collector bugs; degrade to "available, empty"
		// rather than failing the dashboard.
		families = nil
	}

	snap := StatsSnapshot{
		Available:     true,
		UptimeSeconds: time.Since(m.startTime).Seconds(),
		StatusClasses: map[string]float64{},
		CacheEvents:   map[string]float64{},
		Methods:       map[string]float64{},
		RateLimited:   map[string]float64{},
	}

	var (
		latencySum   float64
		latencyCount float64
		buckets      = map[float64]float64{}
	)

	for _, mf := range families {
		switch mf.GetName() {
		case "jul_http_requests_total":
			for _, metric := range mf.GetMetric() {
				v := metric.GetCounter().GetValue()
				snap.RequestsTotal += v
				if class := statusClass(labelValue(metric, "code")); class != "" {
					snap.StatusClasses[class] += v
				}
				if method := labelValue(metric, "method"); method != "" {
					snap.Methods[method] += v
				}
			}
		case "jul_http_requests_in_flight":
			snap.InFlight = lastGauge(mf)
		case "jul_listener_conns":
			snap.Connections = lastGauge(mf)
		case "jul_cache_events_total":
			for _, metric := range mf.GetMetric() {
				state := labelValue(metric, "state")
				if state == "" {
					continue
				}
				snap.CacheEvents[state] += metric.GetCounter().GetValue()
			}
		case "jul_http_ratelimited_total":
			for _, metric := range mf.GetMetric() {
				kind := labelValue(metric, "key")
				if kind == "" {
					kind = "other"
				}
				snap.RateLimited[kind] += metric.GetCounter().GetValue()
			}
		case "jul_http_request_duration_seconds":
			for _, metric := range mf.GetMetric() {
				h := metric.GetHistogram()
				if h == nil {
					continue
				}
				latencySum += h.GetSampleSum()
				latencyCount += float64(h.GetSampleCount())
				for _, b := range h.GetBucket() {
					ub := b.GetUpperBound()
					if math.IsInf(ub, 1) {
						// The implicit +Inf bucket is synthesized from the total
						// sample count in mergeBuckets; skip any explicit one to
						// avoid double-counting.
						continue
					}
					buckets[ub] += float64(b.GetCumulativeCount())
				}
			}
		}
	}

	if latencyCount > 0 {
		snap.LatencyAvgMs = (latencySum / latencyCount) * 1000
	}
	merged := mergeBuckets(buckets, latencyCount)
	snap.LatencyP50Ms = histogramQuantile(0.50, merged) * 1000
	snap.LatencyP95Ms = histogramQuantile(0.95, merged) * 1000
	snap.LatencyP99Ms = histogramQuantile(0.99, merged) * 1000

	hits := snap.CacheEvents["HIT"]
	cacheTotal := hits
	for _, state := range []string{"MISS", "STALE", "BYPASS"} {
		cacheTotal += snap.CacheEvents[state]
	}
	if cacheTotal > 0 {
		snap.CacheHitRatio = hits / cacheTotal
	}

	snap.RequestsPerSec, snap.ErrorRate = m.rates(snap.RequestsTotal, snap.StatusClasses["5xx"])
	return snap
}

// rates derives requests-per-second and the windowed 5xx error rate from the
// cumulative totals, comparing against the previous Snapshot call. The first
// call after startup has no baseline and returns zeroes.
func (m *Metrics) rates(total, total5xx float64) (rps, errorRate float64) {
	now := time.Now()
	m.statsMu.Lock()
	defer m.statsMu.Unlock()

	if !m.statsLast.IsZero() {
		if dt := now.Sub(m.statsLast).Seconds(); dt > 0 {
			rps = (total - m.statsLastTotal) / dt
			if rps < 0 {
				rps = 0 // counters reset (e.g. reload) — avoid negatives
			}
		}
		if deltaTotal := total - m.statsLastTotal; deltaTotal > 0 {
			delta5xx := total5xx - m.statsLastClasses["5xx"]
			if delta5xx > 0 {
				errorRate = delta5xx / deltaTotal
			}
		}
	}

	m.statsLast = now
	m.statsLastTotal = total
	m.statsLastClasses = map[string]float64{"5xx": total5xx}
	return rps, errorRate
}

// labelValue returns the value of the named label on a metric, or "".
func labelValue(metric *dto.Metric, name string) string {
	for _, lp := range metric.GetLabel() {
		if lp.GetName() == name {
			return lp.GetValue()
		}
	}
	return ""
}

// lastGauge returns the value of the last gauge sample in a family (there is a
// single series for the unlabeled gauges used here).
func lastGauge(mf *dto.MetricFamily) float64 {
	var v float64
	for _, metric := range mf.GetMetric() {
		v = metric.GetGauge().GetValue()
	}
	return v
}

// statusClass maps an HTTP status code string to its class bucket, e.g. "404"
// to "4xx". It returns "" for empty or malformed codes.
func statusClass(code string) string {
	if code == "" {
		return ""
	}
	switch code[0] {
	case '1', '2', '3', '4', '5':
		return string(code[0]) + "xx"
	default:
		return ""
	}
}

// bucketBound pairs a histogram bucket upper bound with its cumulative count.
type bucketBound struct {
	upper float64
	count float64
}

// mergeBuckets converts the upper-bound→cumulative-count map into a sorted
// slice and appends the implicit +Inf bucket carrying the full sample count so
// histogramQuantile has a well-defined total.
func mergeBuckets(buckets map[float64]float64, total float64) []bucketBound {
	out := make([]bucketBound, 0, len(buckets)+1)
	for ub, c := range buckets {
		out = append(out, bucketBound{upper: ub, count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].upper < out[j].upper })
	out = append(out, bucketBound{upper: math.Inf(1), count: total})
	return out
}

// histogramQuantile approximates the q-quantile (0..1) from cumulative buckets,
// mirroring Prometheus's histogram_quantile: it locates the bucket whose
// cumulative count first reaches rank q*total and linearly interpolates within
// it. Buckets must be sorted ascending by upper bound with a trailing +Inf
// bucket holding the total count.
func histogramQuantile(q float64, buckets []bucketBound) float64 {
	if len(buckets) == 0 {
		return 0
	}
	total := buckets[len(buckets)-1].count
	if total <= 0 {
		return 0
	}
	rank := q * total

	i := 0
	for i < len(buckets) && buckets[i].count < rank {
		i++
	}
	if i == len(buckets) {
		i = len(buckets) - 1
	}

	upper := buckets[i].upper
	var lower, cumLower float64
	if i > 0 {
		lower = buckets[i-1].upper
		cumLower = buckets[i-1].count
	}
	if math.IsInf(upper, 1) {
		// Everything beyond the largest finite bound: report that bound.
		return lower
	}
	count := buckets[i].count - cumLower
	if count <= 0 {
		return upper
	}
	return lower + (upper-lower)*(rank-cumLower)/count
}
