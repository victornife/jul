// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"jul/internal/backendtls"
	"jul/internal/config"
)

// HealthHook is called when the active checker changes a backend's health
// verdict (and once per backend at startup to seed the initial state). It is
// used to drive a per-backend health gauge. It may be nil.
type HealthHook func(pool, backend string, healthy bool)

// ProbeHook is called after every probe with its outcome and latency, used to
// drive probe counters and a latency histogram. It may be nil.
type ProbeHook func(pool string, success bool, latency time.Duration)

// healthParams is the resolved, validated probe configuration for a pool.
type healthParams struct {
	typ                string // "http" | "tcp"
	path               string
	interval           time.Duration
	timeout            time.Duration
	healthyThreshold   int
	unhealthyThreshold int
	expectStatus       []int
	expectBody         string
}

// healthParamsFrom resolves a HealthCheckConfig into probe parameters, applying
// the same defaults as the config parser so a checker started directly (for
// example in tests) behaves identically to one built from a parsed config.
func healthParamsFrom(cfg config.HealthCheckConfig) healthParams {
	p := healthParams{
		typ:                cfg.Type,
		path:               cfg.Path,
		interval:           cfg.Interval.Std(),
		timeout:            cfg.Timeout.Std(),
		healthyThreshold:   cfg.HealthyThreshold,
		unhealthyThreshold: cfg.UnhealthyThreshold,
		expectStatus:       cfg.ExpectStatus,
		expectBody:         cfg.ExpectBody,
	}
	if p.typ == "" {
		p.typ = "http"
	}
	if p.interval <= 0 {
		p.interval = 5 * time.Second
	}
	if p.timeout <= 0 || p.timeout >= p.interval {
		p.timeout = p.interval / 2
	}
	if p.healthyThreshold < 1 {
		p.healthyThreshold = 2
	}
	if p.unhealthyThreshold < 1 {
		p.unhealthyThreshold = 3
	}
	if p.typ == "http" && len(p.expectStatus) == 0 {
		p.expectStatus = []int{200}
	}
	return p
}

// probeState tracks a single backend's consecutive-probe counters and current
// active-health verdict. It is owned by the checker goroutine, so it needs no
// synchronization.
type probeState struct {
	consecutiveOK   int
	consecutiveFail int
	healthy         bool
}

// healthChecker runs active probes for one pool until the pool is closed.
type healthChecker struct {
	pool     *Pool
	params   healthParams
	onHealth HealthHook
	onProbe  ProbeHook
	client   *http.Client
	dialer   *net.Dialer
	states   map[*Backend]*probeState
}

// StartHealthChecks launches the active health-check goroutine for the pool. It
// runs until the pool is Closed (via Done) and must be called at most once per
// pool. onHealth and onProbe (either may be nil) receive health transitions and
// per-probe outcomes for metrics.
func (p *Pool) StartHealthChecks(cfg config.HealthCheckConfig, onHealth HealthHook, onProbe ProbeHook) {
	p.StartHealthChecksWithTLS(cfg, nil, onHealth, onProbe)
}

// StartHealthChecksWithTLS is StartHealthChecks with the pool's resolved
// backend trust policy.
//
// A backend is never reported healthy under weaker verification than the
// requests Jul will send it (ADR 0016 §9): the probe client uses the same
// resolved policy as live traffic, so a private-CA or mutually-authenticated
// backend is probed exactly as it is used. A nil policy keeps the previous
// behaviour — Go's defaults, which verify against the platform trust store.
//
// Raw TCP probes are unchanged: they are reachability checks and have never
// represented identity verification.
func (p *Pool) StartHealthChecksWithTLS(cfg config.HealthCheckConfig, policy *backendtls.Policy, onHealth HealthHook, onProbe ProbeHook) {
	params := healthParamsFrom(cfg)
	hc := &healthChecker{
		pool:     p,
		params:   params,
		onHealth: onHealth,
		onProbe:  onProbe,
		dialer:   &net.Dialer{Timeout: params.timeout},
		states:   make(map[*Backend]*probeState),
	}
	hc.client = &http.Client{
		Timeout: params.timeout,
		// Probes must not follow redirects: a 3xx that is not in expect_status
		// is a failed probe, not a reason to chase another URL.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     probeTransport(params.timeout, policy),
	}
	go hc.run()
}

// run is the checker loop. It staggers the first round by a random fraction of
// the interval to avoid synchronizing probe bursts across pools (thundering
// herd), then probes every interval until the pool is closed.
func (hc *healthChecker) run() {
	timer := time.NewTimer(jitter(hc.params.interval))
	defer timer.Stop()
	for {
		select {
		case <-hc.pool.Done():
			return
		case <-timer.C:
			hc.probeAll()
			timer.Reset(hc.params.interval)
		}
	}
}

// probeAll probes every current backend once and updates its verdict. It reads
// the live backend set each round so backends added or removed by
// UpdateBackends are picked up without restarting the checker.
func (hc *healthChecker) probeAll() {
	backends := hc.pool.Backends()
	live := make(map[*Backend]struct{}, len(backends))
	for _, b := range backends {
		live[b] = struct{}{}
		select {
		case <-hc.pool.Done():
			return
		default:
		}
		hc.probeOne(b)
	}
	// Drop state for backends no longer in the pool so the map cannot grow
	// without bound across reloads.
	for b := range hc.states {
		if _, ok := live[b]; !ok {
			delete(hc.states, b)
		}
	}
}

// probeOne runs a single probe against a backend and applies threshold-based
// hysteresis to its active-health verdict.
func (hc *healthChecker) probeOne(b *Backend) {
	st := hc.states[b]
	if st == nil {
		// First sighting: assume healthy (matches Backend's initial state) and
		// seed the health gauge.
		st = &probeState{healthy: true}
		hc.states[b] = st
		if hc.onHealth != nil {
			hc.onHealth(hc.pool.name, b.Address, true)
		}
	}

	start := time.Now()
	ok := hc.probe(b)
	if hc.onProbe != nil {
		hc.onProbe(hc.pool.name, ok, time.Since(start))
	}

	if ok {
		st.consecutiveFail = 0
		st.consecutiveOK++
		if !st.healthy && st.consecutiveOK >= hc.params.healthyThreshold {
			st.healthy = true
			b.setActiveHealthy(true)
			// A recovered backend should re-enter rotation immediately, so also
			// clear any passive cooldown left over from earlier live-traffic
			// failures.
			hc.pool.MarkSuccess(b)
			if hc.onHealth != nil {
				hc.onHealth(hc.pool.name, b.Address, true)
			}
		}
		return
	}

	st.consecutiveOK = 0
	st.consecutiveFail++
	if st.healthy && st.consecutiveFail >= hc.params.unhealthyThreshold {
		st.healthy = false
		b.setActiveHealthy(false)
		if hc.onHealth != nil {
			hc.onHealth(hc.pool.name, b.Address, false)
		}
	}
}

// probe performs one probe of the configured type, returning whether it passed.
func (hc *healthChecker) probe(b *Backend) bool {
	ctx, cancel := context.WithTimeout(context.Background(), hc.params.timeout)
	defer cancel()
	switch hc.params.typ {
	case "tcp":
		return hc.probeTCP(ctx, b)
	default:
		return hc.probeHTTP(ctx, b)
	}
}

// probeHTTP issues a GET to the backend's probe path and checks the status (and
// optionally a body substring).
func (hc *healthChecker) probeHTTP(ctx context.Context, b *Backend) bool {
	u := &url.URL{Scheme: b.URL.Scheme, Host: b.Address, Path: hc.params.path}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "jul-healthcheck")
	resp, err := hc.client.Do(req)
	if err != nil {
		return false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	if !statusAllowed(resp.StatusCode, hc.params.expectStatus) {
		return false
	}
	if hc.params.expectBody != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err != nil {
			return false
		}
		if !strings.Contains(string(body), hc.params.expectBody) {
			return false
		}
	}
	return true
}

// probeTCP succeeds if a TCP connection to the backend can be established.
func (hc *healthChecker) probeTCP(ctx context.Context, b *Backend) bool {
	conn, err := hc.dialer.DialContext(ctx, "tcp", b.Address)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// statusAllowed reports whether code is in the expected set. An empty set
// accepts any 2xx.
func statusAllowed(code int, expect []int) bool {
	if len(expect) == 0 {
		return code >= 200 && code < 300
	}
	for _, c := range expect {
		if c == code {
			return true
		}
	}
	return false
}

// jitter returns a random duration in [d/2, d) used to stagger the first probe
// round across pools. For very small intervals it falls back to d.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(half)))
}

// probeTransport builds the probe client's transport. It sets the resolved
// backend TLS policy when there is one, so the probe verifies the backend the
// same way live traffic does.
func probeTransport(timeout time.Duration, policy *backendtls.Policy) *http.Transport {
	t := &http.Transport{
		// Each probe opens a fresh connection so a broken backend cannot be
		// masked by a pooled, already-established keep-alive connection.
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: timeout,
	}
	if policy != nil {
		t.TLSClientConfig = policy.ClientConfig()
	}
	return t
}
