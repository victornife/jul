package upstream

import (
	"testing"
	"time"

	"jul/internal/config"
)

func pool(t *testing.T, strategy string, servers ...config.UpstreamServer) *Pool {
	t.Helper()
	p, err := NewPool(config.UpstreamConfig{
		Name:        "test",
		Strategy:    strategy,
		Servers:     servers,
		MaxFails:    2,
		FailTimeout: config.Duration(50 * time.Millisecond),
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return p
}

func TestRoundRobinDistribution(t *testing.T) {
	p := pool(t, "round_robin",
		config.UpstreamServer{Address: "10.0.0.1:80", Weight: 1},
		config.UpstreamServer{Address: "10.0.0.2:80", Weight: 1},
	)
	counts := map[string]int{}
	for i := 0; i < 10; i++ {
		b, err := p.Pick()
		if err != nil {
			t.Fatal(err)
		}
		counts[b.Address]++
		p.Release(b)
	}
	if counts["10.0.0.1:80"] != 5 || counts["10.0.0.2:80"] != 5 {
		t.Fatalf("round-robin distribution = %v, want 5/5", counts)
	}
}

func TestWeightedDistribution(t *testing.T) {
	p := pool(t, "weighted_round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 3},
		config.UpstreamServer{Address: "b:80", Weight: 1},
	)
	counts := map[string]int{}
	for i := 0; i < 8; i++ {
		b, _ := p.Pick()
		counts[b.Address]++
		p.Release(b)
	}
	if counts["a:80"] != 6 || counts["b:80"] != 2 {
		t.Fatalf("weighted distribution = %v, want a=6 b=2", counts)
	}
}

func TestLeastConn(t *testing.T) {
	p := pool(t, "least_conn",
		config.UpstreamServer{Address: "a:80", Weight: 1},
		config.UpstreamServer{Address: "b:80", Weight: 1},
	)
	// Pick a and hold it (in-flight), so next pick should prefer b.
	first, _ := p.Pick()
	second, _ := p.Pick()
	if first.Address == second.Address {
		t.Fatalf("least_conn picked same backend twice: %s", first.Address)
	}
	p.Release(first)
	p.Release(second)
}

func TestPassiveHealthAndRecovery(t *testing.T) {
	p := pool(t, "round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 1},
		config.UpstreamServer{Address: "b:80", Weight: 1},
	)

	// Trip backend "a" by recording maxFails (2) failures.
	for _, b := range p.Backends() {
		if b.Address == "a:80" {
			p.MarkFailure(b)
			p.MarkFailure(b)
		}
	}

	// With "a" down, every pick should return "b".
	for i := 0; i < 5; i++ {
		b, err := p.Pick()
		if err != nil {
			t.Fatal(err)
		}
		if b.Address != "b:80" {
			t.Fatalf("expected only b while a is down, got %s", b.Address)
		}
		p.Release(b)
	}

	// After the cooldown elapses, "a" becomes available again (half-open).
	time.Sleep(70 * time.Millisecond)
	seenA := false
	for i := 0; i < 10; i++ {
		b, _ := p.Pick()
		if b.Address == "a:80" {
			seenA = true
		}
		p.Release(b)
	}
	if !seenA {
		t.Fatal("backend a did not recover after cooldown")
	}
}

func TestAllDownReturnsError(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "a:80", Weight: 1})
	for _, b := range p.Backends() {
		p.MarkFailure(b)
		p.MarkFailure(b)
	}
	if _, err := p.Pick(); err != ErrNoAvailableBackend {
		t.Fatalf("expected ErrNoAvailableBackend, got %v", err)
	}
}
