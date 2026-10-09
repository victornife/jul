// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/observability"
	"jul/internal/redact"
	"jul/internal/upstream"
)

func TestTransportRetirementMetricExactlyOnce(t *testing.T) {
	for _, phase := range []string{"already_drained", "drains_within_grace", "grace_expires"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				metrics := observability.NewMetrics()
				server := leaseTestServer(time.Second)
				server.TransportRetiredHook = metrics.ObserveTransportRetired
				generation := newHandlerGen(context.Background(), time.Second, nil, nil, 1)
				var closed atomic.Int64
				if phase != "already_drained" {
					generation.inflight.Add(1)
				}
				server.retireGen(generation, func() { closed.Add(1) }, nil, nil)
				synctest.Wait()
				if phase != "already_drained" {
					if values := transportRetirementValues(t, metrics); values["graceful"] != 0 || values["forced"] != 0 {
						t.Fatalf("counted retirement before resource closure: %v", values)
					}
					if phase == "grace_expires" {
						time.Sleep(time.Second)
					} else {
						generation.release()
					}
					synctest.Wait()
				}
				wantMode := "graceful"
				if phase == "grace_expires" {
					wantMode = "forced"
				}
				values := transportRetirementValues(t, metrics)
				if values[wantMode] != 1 || values["graceful"]+values["forced"] != 1 {
					t.Fatalf("retirement counters=%v, want exactly one %s", values, wantMode)
				}
				if phase == "grace_expires" {
					if generation.inflight.Load() != 1 {
						t.Fatal("forced counter must not imply an active request was terminated")
					}
					generation.release()
				}
				server.retireTransport(generation, "forced")
				server.retireTransport(generation, "graceful")
				generation.doRetire()
				server.wg.Wait()
				values = transportRetirementValues(t, metrics)
				if closed.Load() != 1 || values[wantMode] != 1 || values["graceful"]+values["forced"] != 1 {
					t.Fatalf("repeated retirement or late drain counted twice: closures=%d values=%v", closed.Load(), values)
				}
			})
		})
	}
}

func transportRetirementValues(t *testing.T, metrics *observability.Metrics) map[string]float64 {
	t.Helper()
	families, err := metrics.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]float64)
	for _, family := range families {
		if family.GetName() != "jul_transport_retired_total" {
			continue
		}
		for _, series := range family.GetMetric() {
			if len(series.GetLabel()) != 1 || series.GetLabel()[0].GetName() != "mode" {
				t.Fatalf("unexpected retirement labels: %v", series.GetLabel())
			}
			mode := series.GetLabel()[0].GetValue()
			if mode != "graceful" && mode != "forced" {
				t.Fatalf("unbounded retirement mode=%q", mode)
			}
			values[mode] = series.GetCounter().GetValue()
		}
	}
	return values
}

func TestTransportRetirementMetricSkipsUnownedGeneration(t *testing.T) {
	metrics := observability.NewMetrics()
	server := leaseTestServer(time.Second)
	server.TransportRetiredHook = metrics.ObserveTransportRetired
	server.retireGen(nil, func() { t.Fatal("nil generation retired") }, nil, nil)
	server.retireGen(newHandlerGen(context.Background(), time.Second, nil, nil, 1), nil, nil, nil)
	if values := transportRetirementValues(t, metrics); values["graceful"] != 0 || values["forced"] != 0 {
		t.Fatalf("unowned generation counted: %v", values)
	}
}

func TestLongLivedHTTPForcedRetirementDoesNotCancelRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := leaseTestServer(time.Second)
		metrics := observability.NewMetrics()
		server.TransportRetiredHook = metrics.ObserveTransportRetired
		entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
		retired := make(chan struct{})
		var requestError error
		old := newHandlerGen(context.Background(), time.Second, map[string]http.Handler{
			":80": http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				close(entered)
				<-release
				requestError = request.Context().Err()
				_, _ = io.WriteString(writer, "old generation completed")
			}),
		}, nil, 1)
		server.handlers.Store(old)
		response := httptest.NewRecorder()
		go func() {
			server.dynamicHandler(":80").ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			close(finished)
		}()
		<-entered
		server.handlers.Store(newHandlerGen(context.Background(), time.Second, nil, nil, 2))
		server.retireGen(old, func() { close(retired) }, nil, nil)
		synctest.Wait()
		select {
		case <-retired:
			t.Fatal("resources retired before grace elapsed")
		default:
		}
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case <-retired:
		default:
			t.Fatal("active generation resources not forcibly retired at grace")
		}
		if got := old.inflight.Load(); got != 1 {
			t.Fatalf("in-flight requests after forced retirement=%d, want 1", got)
		}
		if values := transportRetirementValues(t, metrics); values["forced"] != 1 || values["graceful"] != 0 {
			t.Fatalf("resource retirement counters=%v, want forced=1 graceful=0 while request survives", values)
		}
		close(release)
		<-finished
		server.wg.Wait()
		if requestError != nil {
			t.Fatalf("retirement canceled the HTTP request: %v", requestError)
		}
		if got := response.Body.String(); got != "old generation completed" {
			t.Fatalf("response=%q", got)
		}
		select {
		case <-old.drained:
		default:
			t.Fatal("generation did not drain after request completed")
		}
	})
}

// TestRedactionGenerationRegistryUnionAndRetire verifies the per-generation
// redaction registry used by Publish: secrets are masked while any active
// generation references them and pruned only when that generation drains.
func TestRedactionGenerationRegistryUnionAndRetire(t *testing.T) {
	s := &Server{redactGens: make(map[uint64]redact.State)}

	gen1 := redact.NewState([]string{"old-secret", "shared-secret"}, redact.DefaultMinLen)
	gen2 := redact.NewState([]string{"new-secret", "shared-secret"}, redact.DefaultMinLen)

	s.registerRedactionGen(1, gen1)
	if redact.Apply("old-secret") != "***" || redact.Apply("shared-secret") != "***" {
		t.Error("generation 1 secrets not masked after register")
	}

	s.registerRedactionGen(2, gen2)
	for _, secret := range []string{"old-secret", "new-secret", "shared-secret"} {
		if redact.Apply(secret) != "***" {
			t.Errorf("%q not masked in union of active generations", secret)
		}
	}

	s.retireRedactionGen(1)
	if redact.Apply("old-secret") != "old-secret" {
		t.Error("old-secret still masked after generation 1 retired")
	}
	for _, secret := range []string{"new-secret", "shared-secret"} {
		if redact.Apply(secret) != "***" {
			t.Errorf("%q not masked after generation 1 retired", secret)
		}
	}
}

// TestDynamicHandlerInstallsGenerationSnapshots verifies that dynamicHandler
// attaches the generation-scoped pool snapshots to the request context before
// dispatching to the address handler (R7-03).
func TestDynamicHandlerInstallsGenerationSnapshots(t *testing.T) {
	reg := upstream.NewRegistry(upstream.RegistryOptions{})
	reg.Begin()
	pool, err := reg.For(context.Background(), config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "127.0.0.1:8001"}},
	}, "http")
	if err != nil {
		t.Fatalf("registry.For: %v", err)
	}
	reg.Commit()

	snap := pool.Snapshot()
	s := &Server{redactGens: make(map[uint64]redact.State)}
	s.handlers.Store(newHandlerGen(context.Background(), time.Second, map[string]http.Handler{
		":80": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count := len(pool.BackendsCtx(r.Context()))
			_, _ = io.WriteString(w, itoa(count))
		}),
	}, upstream.SnapshotMap{upstream.PoolSnapshotKey{Name: "api", Scheme: "http"}: snap}, 1))

	rec := httptest.NewRecorder()
	s.dynamicHandler(":80").ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got, want := rec.Body.String(), "1"; got != want {
		t.Errorf("got %q backends, want %q from generation snapshot", got, want)
	}
}

// TestRedactionInstallSerializedAgainstStaleOverwrite stresses the redaction
// registry by retiring the newest generation before the oldest, which would
// previously allow a stale read-modify-write to prune live secrets (R8-04).
func TestRedactionInstallSerializedAgainstStaleOverwrite(t *testing.T) {
	s := &Server{redactGens: make(map[uint64]redact.State)}

	gen1 := redact.NewState([]string{"gen1-secret"}, redact.DefaultMinLen)
	gen2 := redact.NewState([]string{"gen2-secret"}, redact.DefaultMinLen)
	gen3 := redact.NewState([]string{"gen3-secret"}, redact.DefaultMinLen)

	s.registerRedactionGen(1, gen1)
	s.registerRedactionGen(2, gen2)
	s.registerRedactionGen(3, gen3)

	// Retire in reverse order; a stale union overwrite would drop gen2.
	s.retireRedactionGen(3)
	s.retireRedactionGen(1)

	if redact.Apply("gen2-secret") != "***" {
		t.Errorf("gen2-secret not masked after out-of-order retirement; live union corrupted")
	}
	for _, secret := range []string{"gen1-secret", "gen3-secret"} {
		if redact.Apply(secret) != secret {
			t.Errorf("%q still masked after retirement", secret)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestStartupRedactionIsRegistered (R9-01) verifies that the initial redaction
// state passed to Run is installed before the server starts serving, so secrets
// resolved at startup are masked from the first request.
func TestStartupRedactionIsRegistered(t *testing.T) {
	addr := freePort(t)
	secret := "startup-secret-" + addr

	var genIDCounter atomic.Uint64
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		genID := genIDCounter.Add(1)
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, redact.Apply(secret))
		})
		m := map[string]http.Handler{addr: h}
		commitFn := func() (upstream.SnapshotMap, func()) { return nil, nil }
		abortFn := func() {}
		return m, genID, commitFn, abortFn, nil
	}

	src := &stubSource{}
	src.set(cfgWith(addr), nil)
	srv := New(cfgWith(addr), nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })

	initial := redact.NewState([]string{secret}, redact.DefaultMinLen)
	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, initial) }()
	waitForServe(t, "http://"+addr+"/", redact.Mask)

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

// TestRedactionRetiredWhenGenerationDrains verifies the normal reload path:
// when the previous generation drains before the grace timeout, its secrets
// are removed from the active set and stop being masked.
func TestRedactionRetiredWhenGenerationDrains(t *testing.T) {
	s := &Server{
		cfg:        &config.Config{Global: config.GlobalConfig{ShutdownTimeout: config.Duration(5 * time.Second)}},
		log:        slog.Default(),
		redactGens: make(map[uint64]redact.State),
	}

	oldGen := redact.NewState([]string{"old-secret"}, redact.DefaultMinLen)
	newGen := redact.NewState([]string{"new-secret"}, redact.DefaultMinLen)

	s.registerRedactionGen(1, oldGen)
	s.registerRedactionGen(2, newGen)

	g := newHandlerGen(context.Background(), time.Second, map[string]http.Handler{}, upstream.SnapshotMap{}, 1)
	g.inflight.Add(1) // simulate a request still executing on generation 1

	resourcesRetired := make(chan struct{})
	redactionRetired := make(chan struct{})

	s.retireGen(g,
		func() { close(resourcesRetired) },
		func() {
			s.retireRedactionGen(1)
			close(redactionRetired)
		},
		func() {
			s.retireRedactionForGen(1)
			close(redactionRetired)
		},
	)

	// Let the generation drain before the grace timeout fires.
	g.release()

	select {
	case <-resourcesRetired:
	case <-time.After(time.Second):
		t.Fatal("resources were not retired after drain")
	}
	select {
	case <-redactionRetired:
	case <-time.After(time.Second):
		t.Fatal("redaction was not retired after drain")
	}

	if redact.Apply("old-secret") != "old-secret" {
		t.Fatal("old-secret still masked after generation drained")
	}
	if redact.Apply("new-secret") != "***" {
		t.Fatal("new-secret not masked after old generation retired")
	}
}

// TestRedactionMovedToTombstoneOnGraceTimeout (R9-03) verifies that when a
// generation does not drain before the resource grace timeout, its secrets are
// moved to the retiredRedaction tombstone instead of blocking shutdown. The
// secrets stay masked for the process lifetime even after the generation
// eventually drains.
func TestRedactionMovedToTombstoneOnGraceTimeout(t *testing.T) {
	s := &Server{
		cfg:        &config.Config{Global: config.GlobalConfig{ShutdownTimeout: config.Duration(50 * time.Millisecond)}},
		log:        slog.Default(),
		redactGens: make(map[uint64]redact.State),
	}

	oldGen := redact.NewState([]string{"old-secret"}, redact.DefaultMinLen)
	newGen := redact.NewState([]string{"new-secret"}, redact.DefaultMinLen)

	s.registerRedactionGen(1, oldGen)
	s.registerRedactionGen(2, newGen)

	g := newHandlerGen(context.Background(), time.Second, map[string]http.Handler{}, upstream.SnapshotMap{}, 1)
	g.inflight.Add(1) // simulate a request still executing on generation 1

	resourcesRetired := make(chan struct{})
	redactionTombstoned := make(chan struct{})

	s.retireGen(g,
		func() { close(resourcesRetired) },
		func() {
			s.retireRedactionGen(1)
			close(redactionTombstoned)
		},
		func() {
			s.retireRedactionForGen(1)
			close(redactionTombstoned)
		},
	)

	// Wait for the grace timeout to fire and resources to be closed.
	select {
	case <-resourcesRetired:
	case <-time.After(time.Second):
		t.Fatal("resources were not retired within timeout")
	}

	// The redaction goroutine should also have hit the timeout and moved
	// secrets to the tombstone.
	select {
	case <-redactionTombstoned:
	case <-time.After(time.Second):
		t.Fatal("redaction was not tombstoned after grace timeout")
	}

	// Secrets must remain masked after the grace timeout: the request is still
	// in flight and the tombstone keeps masking alive.
	if redact.Apply("old-secret") != "***" {
		t.Fatal("old-secret no longer masked after grace timeout")
	}
	if redact.Apply("new-secret") != "***" {
		t.Fatal("new-secret not masked while generation 2 is active")
	}

	// Let the generation drain. With R9-03 the tombstone is permanent for the
	// process lifetime, so old-secret stays masked even after drain.
	g.release()
	time.Sleep(25 * time.Millisecond)

	if redact.Apply("old-secret") != "***" {
		t.Fatal("old-secret no longer masked after drain; tombstone should preserve it")
	}
	if redact.Apply("new-secret") != "***" {
		t.Fatal("new-secret not masked after drain")
	}
}
