// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"jul/internal/config"
)

type churnDiscoverer struct {
	started    chan struct{}
	closed     chan struct{}
	startOnce  sync.Once
	closeOnce  sync.Once
	closeCalls atomic.Int64
}

func (d *churnDiscoverer) Resolve(ctx context.Context) ([]Target, error) {
	d.startOnce.Do(func() { close(d.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*churnDiscoverer) Describe() string { return "churn" }

func (d *churnDiscoverer) Close() error {
	d.closeCalls.Add(1)
	d.closeOnce.Do(func() { close(d.closed) })
	return nil
}

func TestDiscoveryWorkerGenerationChurnNoLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	p, err := NewPool(config.UpstreamConfig{
		Name:    "svc",
		Servers: []config.UpstreamServer{{Address: "127.0.0.1:8000", Weight: 1}},
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	const generations = 64
	for i := 0; i < generations; i++ {
		d := &churnDiscoverer{
			started: make(chan struct{}),
			closed:  make(chan struct{}),
		}
		p.StartDiscovery(d, time.Hour, DiscoveryHooks{}, nil)
		select {
		case <-d.started:
		case <-time.After(time.Second):
			p.Close()
			t.Fatalf("generation %d did not start", i)
		}

		// Publish of the next egress generation fences/cancels the current
		// worker without retiring the backend pool. Calling it twice also proves
		// retirement is idempotent and cannot double-close the discoverer.
		p.StopDiscovery()
		p.StopDiscovery()
		select {
		case <-d.closed:
		case <-time.After(time.Second):
			p.Close()
			t.Fatalf("generation %d did not retire", i)
		}
		if got := d.closeCalls.Load(); got != 1 {
			p.Close()
			t.Fatalf("generation %d discoverer Close calls = %d, want 1", i, got)
		}
	}

	p.Close()
}
