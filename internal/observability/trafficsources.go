package observability

import (
	"net"
	"net/url"
	"strings"
	"sync"
)

// trafficSourcesCap bounds how many distinct keys each top-N rollup retains
// before overflow is folded into an "(other)" bucket. Keeping this small caps
// memory and, crucially, keeps the projection's cardinality bounded regardless
// of how many distinct hosts/origins/referers the edge sees.
const trafficSourcesCap = 64

// trafficKeyMaxLen caps the stored length of any normalized key so a hostile
// client cannot inflate memory with very long Origin/Referer values.
const trafficKeyMaxLen = 253

// TrafficSources is the bounded, privacy-preserving projection of where traffic
// is coming from, surfaced on the Console Overview (Console v2 Milestone 1.4).
// All maps are top-N rollups with overflow folded into "(other)"; no raw URLs,
// query strings, tokens, cookies, or Authorization headers are ever stored.
type TrafficSources struct {
	// Hosts is the top requested Host headers (port stripped, lower-cased).
	Hosts map[string]float64 `json:"hosts"`
	// Origins is the top Origin request headers (scheme+host, lower-cased).
	Origins map[string]float64 `json:"origins"`
	// Referers is the top Referer hosts (host only; path and query discarded).
	Referers map[string]float64 `json:"referers"`
	// PreflightCount is the number of CORS preflight (OPTIONS) requests seen.
	PreflightCount float64 `json:"preflight_count"`
	// SameOrigin and CrossOrigin estimate the same- vs cross-origin split based
	// on comparing the Origin header host to the request Host.
	SameOrigin  float64 `json:"same_origin"`
	CrossOrigin float64 `json:"cross_origin"`
}

// topNCounter is a bounded key→count rollup. Once it holds maxKeys distinct
// keys, any new key is folded into the "(other)" bucket so cardinality — and
// therefore memory and the exported projection size — stays bounded. It is
// guarded by the enclosing trafficTracker's mutex.
type topNCounter struct {
	maxKeys int
	values  map[string]float64
}

func newTopNCounter(maxKeys int) *topNCounter {
	return &topNCounter{maxKeys: maxKeys, values: make(map[string]float64)}
}

func (c *topNCounter) add(key string) {
	if _, ok := c.values[key]; ok {
		c.values[key]++
		return
	}
	if len(c.values) >= c.maxKeys {
		c.values["(other)"]++
		return
	}
	c.values[key] = 1
}

// snapshot returns a copy of the counter's current state for JSON export.
func (c *topNCounter) snapshot() map[string]float64 {
	out := make(map[string]float64, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

// trafficTracker maintains the bounded top-N rollups behind TrafficSources. It
// is safe for concurrent use and is updated once per request from the metrics
// middleware.
type trafficTracker struct {
	mu          sync.Mutex
	hosts       *topNCounter
	origins     *topNCounter
	referers    *topNCounter
	preflight   float64
	sameOrigin  float64
	crossOrigin float64
}

func newTrafficTracker() *trafficTracker {
	return &trafficTracker{
		hosts:    newTopNCounter(trafficSourcesCap),
		origins:  newTopNCounter(trafficSourcesCap),
		referers: newTopNCounter(trafficSourcesCap),
	}
}

// record folds one request into the rollups. host is the request Host header;
// origin and referer are the corresponding request headers (possibly empty);
// method is the HTTP method. Only hostnames/origins are retained — never full
// URLs, query strings, or credentials.
func (t *trafficTracker) record(host, origin, referer, method string) {
	h := normalizeHost(host)
	o := normalizeOrigin(origin)
	ref := normalizeRefererHost(referer)

	t.mu.Lock()
	defer t.mu.Unlock()

	t.hosts.add(h)
	t.origins.add(o)
	t.referers.add(ref)

	if strings.EqualFold(method, "OPTIONS") {
		t.preflight++
	}
	// Only classify same/cross origin when an Origin header is actually present;
	// a request without one is neither (e.g. a top-level navigation).
	if origin != "" {
		if originHost(origin) == h {
			t.sameOrigin++
		} else {
			t.crossOrigin++
		}
	}
}

// snapshot returns a point-in-time copy of the traffic-source rollups.
func (t *trafficTracker) snapshot() TrafficSources {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TrafficSources{
		Hosts:          t.hosts.snapshot(),
		Origins:        t.origins.snapshot(),
		Referers:       t.referers.snapshot(),
		PreflightCount: t.preflight,
		SameOrigin:     t.sameOrigin,
		CrossOrigin:    t.crossOrigin,
	}
}

// normalizeHost lower-cases a Host header, strips any port, caps its length,
// and maps the empty value to "(none)".
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "(none)"
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	return capLen(host)
}

// normalizeOrigin reduces an Origin header to scheme://host (no path, query, or
// port), lower-cased, mapping empty to "(none)".
func normalizeOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return "(none)"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return capLen(strings.ToLower(origin))
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname()) // Hostname() drops the port
	if scheme == "" {
		return capLen(host)
	}
	return capLen(scheme + "://" + host)
}

// originHost extracts just the lower-cased host from an Origin header for the
// same/cross-origin comparison.
func originHost(origin string) string {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || u.Host == "" {
		return ""
	}
	return capLen(strings.ToLower(u.Hostname()))
}

// normalizeRefererHost keeps only the host of a Referer URL — never the path or
// query string — lower-cased and length-capped, mapping empty to "(none)".
func normalizeRefererHost(referer string) string {
	referer = strings.TrimSpace(referer)
	if referer == "" {
		return "(none)"
	}
	u, err := url.Parse(referer)
	if err != nil || u.Host == "" {
		return "(none)"
	}
	return capLen(strings.ToLower(u.Hostname()))
}

// capLen truncates s to trafficKeyMaxLen runes-by-bytes so a hostile header
// cannot bloat the rollup.
func capLen(s string) string {
	if len(s) > trafficKeyMaxLen {
		return s[:trafficKeyMaxLen]
	}
	return s
}
