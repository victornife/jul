//go:build soak && stream

package stream

import (
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// TestSoakUDPChurn drives sustained UDP source-address churn through a real
// stream listener and asserts the proxy stays bounded: a flood of short-lived
// clients, each from a fresh ephemeral source port, must never push the live
// session count past max_udp_sessions, must fully tear down every reaped/evicted
// session (no goroutine or backend-socket leak), and must not grow the heap
// without bound.
//
// This is the UDP analogue of the ADR-0005 proxy soak (internal/handler:TestSoak)
// and the stability backing for the v1.16 UDP session-safety hardening: source
// addresses are trivially spoofed, so an unbounded session table or a per-session
// teardown leak would be a public-internet DoS vector. The test is excluded from
// the normal `go test ./...` run by the `soak` build tag — scripts/soak.sh (and
// the release workflow) run it explicitly. Duration and concurrency are
// env-tunable so CI smoke runs finish in seconds while a release run soaks for
// minutes:
//
//	SOAK_DURATION  wall-clock run time     (default 30s)
//	SOAK_WORKERS   concurrent churn clients (default 16)
func TestSoakUDPChurn(t *testing.T) {
	duration := soakEnvDuration("SOAK_DURATION", 30*time.Second)
	workers := soakEnvInt("SOAK_WORKERS", 16)

	// A hard per-listener cap with a short idle timeout: under churn the cap
	// bounds the live session table while idle reaping continuously tears down
	// abandoned sessions, exercising the create/evict/reap paths in a tight loop.
	const maxSessions = 256
	const idle = 200 * time.Millisecond

	backend, stopBackend := udpEcho(t)
	defer stopBackend()
	addr := freeUDPAddr(t)

	var m udpMetrics
	s := newTestServer(t, m.hooks())
	if err := s.Reload([]config.StreamServer{{
		Listen:         addr,
		Protocol:       "udp",
		ProxyPass:      backend,
		MaxUDPSessions: maxSessions,
		IdleTimeout:    config.Duration(idle),
	}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}

	serverAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("resolve server addr: %v", err)
	}

	// One churn burst: a brand-new client socket (fresh ephemeral source port,
	// hence a new session keyed by source address) sends one datagram, reads the
	// echo if it arrives, and is abandoned — leaving the server-side session to
	// be reaped on idle or evicted at the cap.
	churn := func() {
		c, err := net.DialUDP("udp", nil, serverAddr)
		if err != nil {
			return // ephemeral-port pressure under heavy churn is not a failure
		}
		defer c.Close()
		if _, err := c.Write([]byte("ping")); err != nil {
			return
		}
		_ = c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		buf := make([]byte, 16)
		_, _ = c.Read(buf) // reply is best-effort; a capped-out client gets none
	}

	// Warm up, then let the warm-up sessions reap so the baseline reflects the
	// idle listener (its serveUDP goroutine) rather than transient churn state.
	for i := 0; i < workers*4; i++ {
		churn()
	}
	if !eventually(func() bool { return m.conns.Load() == 0 }) {
		t.Fatalf("warm-up sessions did not reap: %d still live", m.conns.Load())
	}
	baseGoroutines, baseHeap := soakSample()

	var (
		sends    atomic.Int64
		peak     atomic.Int64
		stopPoll = make(chan struct{})
		wg       sync.WaitGroup
	)

	// Sampler: track the high-water mark of live sessions throughout the run.
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stopPoll:
				return
			case <-tick.C:
				if n := m.conns.Load(); n > peak.Load() {
					peak.Store(n)
				}
			}
		}
	}()

	deadline := time.Now().Add(duration)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				churn()
				sends.Add(1)
			}
		}()
	}

	// Wait only for the churn workers; stop the sampler afterward.
	doneWorkers := make(chan struct{})
	go func() { wg.Wait(); close(doneWorkers) }()
	time.Sleep(duration)
	close(stopPoll)
	<-doneWorkers

	if sends.Load() == 0 {
		t.Fatal("no datagrams sent; the soak did not exercise the listener")
	}

	// Bounded sessions: the cap is a hard ceiling, so the live session table must
	// never have exceeded it no matter how many distinct sources churned through.
	if p := peak.Load(); p > maxSessions {
		t.Errorf("live UDP sessions peaked at %d, exceeding the cap of %d", p, maxSessions)
	}
	// Churn must have actually pressured the teardown paths (idle reaping and/or
	// cap eviction); otherwise the soak proved nothing about session cleanup.
	if reaped := m.evictedIdle.Load() + m.evictedLRU.Load(); reaped == 0 {
		t.Errorf("no sessions were reaped or evicted over %d sends; churn not exercised", sends.Load())
	}

	// Let the trailing sessions reap, then sample. A leaked session, backend
	// socket, or per-session goroutine would keep the gauge above zero here.
	if !eventually(func() bool { return m.conns.Load() == 0 }) {
		t.Errorf("sessions did not drain after churn stopped: %d still live", m.conns.Load())
	}
	endGoroutines, endHeap := soakSample()

	t.Logf("soak/udp: duration=%s workers=%d sends=%d peakSessions=%d cap=%d",
		duration, workers, sends.Load(), peak.Load(), maxSessions)
	t.Logf("soak/udp: reaped(idle=%d lru=%d) rejected=%d", m.evictedIdle.Load(), m.evictedLRU.Load(), m.rejected.Load())
	t.Logf("soak/udp: goroutines %d -> %d, heap %d -> %d bytes", baseGoroutines, endGoroutines, baseHeap, endHeap)

	// Goroutine gate: each live session owns a downstream relay goroutine, so a
	// teardown leak would grow in proportion to the (many thousands of) churned
	// clients. A generous constant slack absorbs scheduler lag without masking it.
	if growth := endGoroutines - baseGoroutines; growth > 2*workers+32 {
		t.Errorf("goroutine leak: grew by %d (%d -> %d)", growth, baseGoroutines, endGoroutines)
	}
	// Heap gate: bounded post-GC growth. A per-session leak would balloon the heap
	// far past this budget after a sustained churn.
	const heapBudget = 64 << 20 // 64 MiB
	if growth := int64(endHeap) - int64(baseHeap); growth > heapBudget {
		t.Errorf("heap growth %d bytes exceeds budget %d bytes", growth, heapBudget)
	}
}

// soakSample forces GC and returns the current goroutine count and live heap.
func soakSample() (goroutines int, heap uint64) {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return runtime.NumGoroutine(), ms.HeapAlloc
}

func soakEnvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func soakEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
