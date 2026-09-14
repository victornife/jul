// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package egress

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/goleak"

	"jul/internal/config"
)

// TestDynamicClientTighteningCannotReuseOldHTTP2Connection is the H2 analogue
// of the H1 keep-alive test in generation_test.go. The first request creates a
// real HTTP/2 connection under policy A. Policy B is then published with a
// distinct generation-owned Transport. The next request to exactly the same
// origin must be blocked without reaching the server; reusing A's H2 pool would
// incorrectly make it succeed without a new guarded dial.
func TestDynamicClientTighteningCannotReuseOldHTTP2Connection(t *testing.T) {
	var requests atomic.Int64
	var sawH2 atomic.Bool
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.ProtoMajor == 2 {
			sawH2.Store(true)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	m, err := NewManager(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	seedTLSTransportForGeneration(t, m.Current(), SubsystemOCSP, srv.Client().Transport)
	client := m.Client(SubsystemOCSP, 0)

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("policy A H2 request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if !sawH2.Load() {
		t.Fatalf("first request protocol = %s, want HTTP/2", resp.Proto)
	}

	candidate, changed, err := m.Prepare(config.EgressConfig{Enabled: true, Allow: []string{"192.0.2.1"}})
	if err != nil || !changed {
		t.Fatalf("Prepare B = changed %v err %v", changed, err)
	}
	// Give B the same trusted TLS roots/HTTP2 settings as A but its own
	// transport/pool. This is exactly the production ownership distinction the
	// test is proving; only the egress policy changes.
	seedTLSTransportForGeneration(t, candidate, SubsystemOCSP, srv.Client().Transport)
	old := m.Publish(candidate)
	defer old.CloseIdleConnections()

	resp, err = client.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("policy B H2 request err = %v, want ErrBlocked", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("blocked request reused old H2 pool: server requests = %d, want 1", requests.Load())
	}
}

func seedTLSTransportForGeneration(t *testing.T, generation *Generation, subsystem string, baseRT http.RoundTripper) {
	t.Helper()
	base, ok := baseRT.(*http.Transport)
	if !ok {
		t.Fatalf("test server transport = %T, want *http.Transport", baseRT)
	}
	transport := generation.For(subsystem).Transport(base.Clone())
	var rt http.RoundTripper = transport
	if generation.policy != nil {
		rt = &guardedRoundTripper{subsystem: subsystem, policy: generation.policy, next: transport}
	}
	generation.mu.Lock()
	generation.http[subsystem] = &generationHTTP{transport: transport, rt: rt}
	generation.mu.Unlock()
}

func TestManagerConcurrentReadsAndPolicyChurnRaceClean(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	m, err := NewManager(config.EgressConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	const readers = 16
	const iterations = 200
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations*4; j++ {
				generation := m.Current()
				if generation == nil {
					t.Error("Current returned nil")
					return
				}
				_ = generation.ID()
				_ = generation.Enabled()
				_ = generation.For(SubsystemAuth)
			}
		}()
	}

	close(start)
	var retired []*Generation
	for i := 0; i < iterations; i++ {
		var cfg config.EgressConfig
		if i%2 == 0 {
			cfg = config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}}
		}
		candidate, changed, err := m.Prepare(cfg)
		if err != nil {
			t.Fatalf("Prepare iteration %d: %v", i, err)
		}
		if !changed {
			t.Fatalf("Prepare iteration %d unexpectedly unchanged", i)
		}
		retired = append(retired, m.Publish(candidate))
	}
	wg.Wait()
	for _, generation := range retired {
		if generation != nil {
			generation.CloseIdleConnections()
		}
	}
}
