// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

type trackedDiscoverer struct {
	closed chan struct{}
	once   sync.Once
}

func (d *trackedDiscoverer) Resolve(context.Context) ([]Target, error) {
	return []Target{{Address: "127.0.0.1:9000", Weight: 1}}, nil
}

func (*trackedDiscoverer) Describe() string { return "tracked" }

func (d *trackedDiscoverer) Close() error {
	d.once.Do(func() { close(d.closed) })
	return nil
}

func TestRegistryDiscoveryPoolIdentityIncludesEgressGeneration(t *testing.T) {
	var created atomic.Int64
	var mu sync.Mutex
	var discoverers []*trackedDiscoverer
	registry := NewRegistry(RegistryOptions{
		NewDiscoverer: func(config.DiscoveryConfig, DialFunc) (Discoverer, error) {
			d := &trackedDiscoverer{closed: make(chan struct{})}
			created.Add(1)
			mu.Lock()
			discoverers = append(discoverers, d)
			mu.Unlock()
			return d, nil
		},
	})
	defer registry.CloseAll()

	up := discoveryUpstreamForEgressTest()
	registry.Begin()
	registry.SetEgressGeneration(11, nil)
	poolA, err := registry.For(context.Background(), up, "http")
	if err != nil {
		t.Fatalf("For A: %v", err)
	}
	registry.Commit()
	registry.Activate()

	registry.Begin()
	registry.SetEgressGeneration(11, nil)
	poolSame, err := registry.For(context.Background(), up, "http")
	if err != nil {
		t.Fatalf("For same generation: %v", err)
	}
	if poolSame != poolA {
		t.Fatal("same egress generation rebuilt discovery pool")
	}
	if created.Load() != 1 {
		t.Fatalf("discoverers after same generation = %d, want 1", created.Load())
	}
	registry.Abort()

	registry.Begin()
	registry.SetEgressGeneration(12, nil)
	poolB, err := registry.For(context.Background(), up, "http")
	if err != nil {
		t.Fatalf("For B: %v", err)
	}
	if poolB == poolA {
		t.Fatal("changed egress generation reused old discovery pool")
	}
	if created.Load() != 2 {
		t.Fatalf("discoverers after generation change = %d, want 2", created.Load())
	}
	registry.Commit()
	registry.Activate()

	mu.Lock()
	oldDiscoverer := discoverers[0]
	mu.Unlock()
	select {
	case <-oldDiscoverer.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("old discovery client/worker did not retire after generation replacement")
	}
}

func TestRegistryEgressCandidateAbortClosesFreshDiscovererOnly(t *testing.T) {
	var mu sync.Mutex
	var discoverers []*trackedDiscoverer
	registry := NewRegistry(RegistryOptions{
		NewDiscoverer: func(config.DiscoveryConfig, DialFunc) (Discoverer, error) {
			d := &trackedDiscoverer{closed: make(chan struct{})}
			mu.Lock()
			discoverers = append(discoverers, d)
			mu.Unlock()
			return d, nil
		},
	})
	defer registry.CloseAll()

	up := discoveryUpstreamForEgressTest()
	registry.Begin()
	registry.SetEgressGeneration(21, nil)
	live, err := registry.For(context.Background(), up, "http")
	if err != nil {
		t.Fatalf("For live: %v", err)
	}
	registry.Commit()
	registry.Activate()

	registry.Begin()
	registry.SetEgressGeneration(22, nil)
	candidate, err := registry.For(context.Background(), up, "http")
	if err != nil {
		t.Fatalf("For candidate: %v", err)
	}
	if candidate == live {
		t.Fatal("candidate generation unexpectedly reused live discovery pool")
	}
	registry.Abort()

	mu.Lock()
	liveDiscoverer, candidateDiscoverer := discoverers[0], discoverers[1]
	mu.Unlock()
	select {
	case <-candidateDiscoverer.closed:
	case <-time.After(time.Second):
		t.Fatal("aborted candidate discoverer was not closed")
	}
	select {
	case <-liveDiscoverer.closed:
		t.Fatal("Abort retired the live discovery generation")
	default:
	}

	registry.Begin()
	registry.SetEgressGeneration(21, nil)
	stillLive, err := registry.For(context.Background(), up, "http")
	if err != nil {
		t.Fatalf("For live after abort: %v", err)
	}
	if stillLive != live {
		t.Fatal("Abort changed live discovery pool")
	}
	registry.Abort()
}

func discoveryUpstreamForEgressTest() config.UpstreamConfig {
	return config.UpstreamConfig{
		Name: "svc",
		Discovery: &config.DiscoveryConfig{
			Type:    "consul",
			Refresh: config.Duration(time.Hour),
		},
	}
}
