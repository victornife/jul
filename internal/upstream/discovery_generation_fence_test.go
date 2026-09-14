// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

type blockingDiscoverer struct {
	started chan struct{}
	release chan struct{}
	targets []Target
	once    sync.Once
}

func (d *blockingDiscoverer) Resolve(context.Context) ([]Target, error) {
	d.once.Do(func() { close(d.started) })
	<-d.release // deliberately ignore cancellation to prove epoch fencing
	return d.targets, nil
}
func (*blockingDiscoverer) Describe() string { return "blocking" }

type immediateDiscoverer struct{ targets []Target }
func (d immediateDiscoverer) Resolve(context.Context) ([]Target, error) { return d.targets, nil }
func (immediateDiscoverer) Describe() string { return "immediate" }

func TestDiscoveryResultFromRetiredEgressGenerationCannotOverwriteNewGeneration(t *testing.T) {
	p, err := NewPool(config.UpstreamConfig{
		Name: "svc",
		Servers: []config.UpstreamServer{{Address: "127.0.0.1:8000", Weight: 1}},
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	old := &blockingDiscoverer{
		started: make(chan struct{}),
		release: make(chan struct{}),
		targets: []Target{{Address: "127.0.0.1:8001", Weight: 1}},
	}
	p.StartDiscovery(old, time.Hour, DiscoveryHooks{}, nil)
	select {
	case <-old.started:
	case <-time.After(time.Second):
		t.Fatal("old discovery generation did not start")
	}

	// This is the Publish boundary for the discovery worker. Generation A is
	// cancelled/fenced, then B is installed. The A provider deliberately ignores
	// cancellation and returns later; its result must be dropped.
	p.StopDiscovery()
	p.StartDiscovery(immediateDiscoverer{targets: []Target{{Address: "127.0.0.1:8002", Weight: 1}}}, time.Hour, DiscoveryHooks{}, nil)

	deadline := time.Now().Add(time.Second)
	for {
		backends := p.Backends()
		if len(backends) == 1 && backends[0].Address == "127.0.0.1:8002" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new generation targets not applied: %+v", backends)
		}
		time.Sleep(time.Millisecond)
	}

	close(old.release)
	time.Sleep(20 * time.Millisecond)
	backends := p.Backends()
	if len(backends) != 1 || backends[0].Address != "127.0.0.1:8002" {
		t.Fatalf("late generation-A result overwrote B: %+v", backends)
	}
}
