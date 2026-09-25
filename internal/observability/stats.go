// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

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

	// CacheHitRatio is HIT / (HIT+MISS+STALE+REVALIDATED+BYPASS), or 0 when no cache events
	// have been recorded. CacheEvents holds the cumulative per-state counts.
	CacheHitRatio float64            `json:"cacheHitRatio"`
	CacheEvents   map[string]float64 `json:"cacheEvents"`

	// Methods holds cumulative request counts by HTTP method (GET, POST, etc).
	Methods map[string]float64 `json:"methods"`

	// RateLimited holds cumulative counts of requests rejected by rate limiting,
	// keyed by the key kind (ip/header/jwt). It powers the Rate Limit editor's
	// observability section (Console v2 Milestone 3.3).
	RateLimited map[string]float64 `json:"rateLimited"`

	// ── Runtime resources and capacity (#431) ──────────────────────────────
	//
	// These fields answer "is this instance healthy, and which resource will
	// saturate first" from data Jul already collects (the standard Go/process
	// Prometheus collectors, the cache, and the upstream resilience
	// projection) rather than a second telemetry stack. A nil pointer means
	// unavailable — a platform/collector could not report the value — which is
	// distinct from a true zero (#431 §9); it is never a sentinel like -1.

	// CPUCores is the process CPU rate in cores used, computed as
	// delta(process_cpu_seconds_total)/delta(wall_time) between this call and
	// the previous one (nil on the first call or if the collector could not
	// report CPU time). It is deliberately not a percentage: a truthful
	// scheduler-capacity denominator is not available portably (#431 §10).
	CPUCores *float64 `json:"cpuCores,omitempty"`
	// RSSBytes is the OS-resident memory of the whole process (process_resident_memory_bytes) —
	// not just Go's heap; WASM linear memory, mmap and non-Go allocations can
	// make this exceed GoHeapAllocBytes.
	RSSBytes *float64 `json:"rssBytes,omitempty"`
	// GoHeapAllocBytes is Go-runtime-managed heap memory currently allocated
	// (go_memstats_heap_alloc_bytes). It is not total process memory.
	GoHeapAllocBytes *float64 `json:"goHeapAllocBytes,omitempty"`
	Goroutines       *float64 `json:"goroutines,omitempty"`
	// OpenFDs/MaxFDs are open file descriptors and the platform's per-process
	// ceiling. MaxFDs is nil when the platform cannot report a ceiling; the
	// Console must render "unavailable", never "N / 0" or a fake percentage.
	OpenFDs *float64 `json:"openFDs,omitempty"`
	MaxFDs  *float64 `json:"maxFDs,omitempty"`

	// HTTPResponseBytesTotal is the cumulative jul_http_response_bytes_total
	// counter: HTTP response-body bytes written to clients, after
	// content-encoding/compression, before HTTP/TLS/transport framing, and
	// excluding bytes written after a connection hijack (WebSocket framing is
	// not counted). The Console derives a bytes/sec trend from consecutive
	// snapshots itself (#431 §25) rather than the server keeping a second
	// rolling series.
	HTTPResponseBytesTotal float64 `json:"httpResponseBytesTotal"`

	// CacheTiers is the occupancy of every configured cache tier. Absent
	// entirely when caching is disabled.
	CacheTiers []CacheTierOccupancy `json:"cacheTiers,omitempty"`

	// Upstream capacity: a bounded worst-pool summary for Overview, computed
	// only over pools with a finite (>0) configured limit — an unbounded pool
	// never contributes a fake percentage (#431 §16).
	UpstreamWorstActive  *PoolPressure `json:"upstreamWorstActive,omitempty"`
	UpstreamWorstPending *PoolPressure `json:"upstreamWorstPending,omitempty"`
	// UpstreamNoEligible lists (sorted) pool names with zero eligible backends
	// right now — a health condition, not a percentage.
	UpstreamNoEligible []string `json:"upstreamNoEligible,omitempty"`
	// UpstreamBudgetExhausted lists (sorted) pool names whose configured retry
	// budget currently grants zero further retries.
	UpstreamBudgetExhausted []string `json:"upstreamBudgetExhausted,omitempty"`
}

// CacheTierOccupancy is one cache tier's occupancy for the Console capacity
// card. OccupancyRatio is present only when MaxBytes > 0 — an unbounded or
// disabled tier never gets a fabricated percentage (#431 §17).
type CacheTierOccupancy struct {
	Tier           string   `json:"tier"`
	Bytes          float64  `json:"bytes"`
	MaxBytes       float64  `json:"maxBytes,omitempty"`
	Entries        float64  `json:"entries"`
	Evictions      float64  `json:"evictions"`
	OccupancyRatio *float64 `json:"occupancyRatio,omitempty"`
}

// PoolPressure is one bounded pool-pressure reading: a configured operator
// pool name (not a raw backend address) with a truthful current/max ratio.
type PoolPressure struct {
	Pool    string  `json:"pool"`
	Current float64 `json:"current"`
	Max     float64 `json:"max"`
	Ratio   float64 `json:"ratio"`
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
		haveCPU      bool
		cpuSeconds   float64
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
		case "jul_http_response_bytes_total":
			for _, metric := range mf.GetMetric() {
				snap.HTTPResponseBytesTotal += metric.GetCounter().GetValue()
			}
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
		// The remaining cases project the standard Go/process collectors
		// already registered in NewMetrics (#431): this is a read of existing
		// authoritative state, not a second telemetry stack. Each is read from
		// its own family so a collector error on one (reported as an
		// InvalidMetric which simply has no matching family) leaves the others
		// intact rather than losing the whole snapshot.
		case "process_cpu_seconds_total":
			for _, metric := range mf.GetMetric() {
				cpuSeconds += metric.GetCounter().GetValue()
				haveCPU = true
			}
		case "process_resident_memory_bytes":
			v := lastGauge(mf)
			snap.RSSBytes = &v
		case "process_open_fds":
			v := lastGauge(mf)
			snap.OpenFDs = &v
		case "process_max_fds":
			v := lastGauge(mf)
			snap.MaxFDs = &v
		case "go_goroutines":
			v := lastGauge(mf)
			snap.Goroutines = &v
		case "go_memstats_heap_alloc_bytes":
			v := lastGauge(mf)
			snap.GoHeapAllocBytes = &v
		}
	}

	snap.CPUCores = m.cpuRate(haveCPU, cpuSeconds)
	snap.CacheTiers = cacheTierOccupancy(m.cacheTierSnapshot())
	snap.UpstreamWorstActive, snap.UpstreamWorstPending, snap.UpstreamNoEligible, snap.UpstreamBudgetExhausted =
		upstreamCapacitySummary(m.upstreamCapacitySnapshot())

	if latencyCount > 0 {
		snap.LatencyAvgMs = (latencySum / latencyCount) * 1000
	}
	merged := mergeBuckets(buckets, latencyCount)
	snap.LatencyP50Ms = histogramQuantile(0.50, merged) * 1000
	snap.LatencyP95Ms = histogramQuantile(0.95, merged) * 1000
	snap.LatencyP99Ms = histogramQuantile(0.99, merged) * 1000

	hits := snap.CacheEvents["HIT"]
	cacheTotal := hits
	for _, state := range []string{"MISS", "STALE", "REVALIDATED", "BYPASS"} {
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

// cpuRate derives the process CPU rate in cores used —
// delta(process_cpu_seconds_total)/delta(wall_time) — between this call and
// the previous one, sharing rates' statsMu/statsLast timestamp so a single
// wall-clock baseline governs every rate this Snapshot call derives (#431
// §11). It returns nil rather than a fabricated zero when this call carries no
// CPU sample (collector unavailable on this platform) or there is no prior
// baseline yet (the first call).
func (m *Metrics) cpuRate(haveSample bool, cpuSeconds float64) *float64 {
	if !haveSample {
		return nil
	}
	m.statsMu.Lock()
	defer m.statsMu.Unlock()

	var rate *float64
	if m.statsHaveCPU && !m.statsLast.IsZero() {
		if dt := time.Since(m.statsLast).Seconds(); dt > 0 {
			r := (cpuSeconds - m.statsLastCPU) / dt
			if r < 0 {
				r = 0 // guard only; process_cpu_seconds_total is monotonic within one process lifetime
			}
			rate = &r
		}
	}
	m.statsLastCPU = cpuSeconds
	m.statsHaveCPU = true
	return rate
}

// cacheTierOccupancy projects the cache's live tier state for the Console
// capacity card. OccupancyRatio is set only when MaxBytes > 0 (#431 §17).
func cacheTierOccupancy(tiers []CacheTierStats) []CacheTierOccupancy {
	if len(tiers) == 0 {
		return nil
	}
	out := make([]CacheTierOccupancy, 0, len(tiers))
	for _, t := range tiers {
		occ := CacheTierOccupancy{
			Tier:      t.Tier,
			Bytes:     float64(t.Bytes),
			MaxBytes:  float64(t.MaxBytes),
			Entries:   float64(t.Entries),
			Evictions: float64(t.Evictions),
		}
		if t.MaxBytes > 0 {
			r := float64(t.Bytes) / float64(t.MaxBytes)
			occ.OccupancyRatio = &r
		}
		out = append(out, occ)
	}
	return out
}

// upstreamCapacitySummary reduces every pool's live resilience state to the
// bounded Overview summary (#431 §16): the single worst active-pressure pool,
// the single worst pending-pressure pool (each only among pools with a
// finite, positive limit — an unbounded pool never produces a fake
// percentage), every pool with no eligible backend right now, and every pool
// whose retry budget currently grants zero further retries. Pools are
// considered in name order so a tie between two pools at the same ratio
// always resolves to the same (alphabetically first) pool rather than
// whichever happened to be visited first by map iteration.
func upstreamCapacitySummary(pools []UpstreamPoolStats) (worstActive, worstPending *PoolPressure, noEligible, budgetExhausted []string) {
	sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
	for _, p := range pools {
		if p.MaxActive > 0 {
			ratio := float64(p.Active) / float64(p.MaxActive)
			if worstActive == nil || ratio > worstActive.Ratio {
				worstActive = &PoolPressure{Pool: p.Name, Current: float64(p.Active), Max: float64(p.MaxActive), Ratio: ratio}
			}
		}
		if p.MaxPending > 0 {
			ratio := float64(p.Pending) / float64(p.MaxPending)
			if worstPending == nil || ratio > worstPending.Ratio {
				worstPending = &PoolPressure{Pool: p.Name, Current: float64(p.Pending), Max: float64(p.MaxPending), Ratio: ratio}
			}
		}
		if p.Eligible == 0 {
			noEligible = append(noEligible, p.Name)
		}
		if p.BudgetPercent > 0 && p.BudgetRemaining == 0 {
			budgetExhausted = append(budgetExhausted, p.Name)
		}
	}
	return worstActive, worstPending, noEligible, budgetExhausted
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
