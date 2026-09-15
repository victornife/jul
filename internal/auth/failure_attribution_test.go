// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

func authAttributionPool(t *testing.T) *upstream.Pool {
	return authAttributionPoolWithMaxFails(t, 1)
}

func authAttributionPoolWithMaxFails(t *testing.T, maxFails int) *upstream.Pool {
	t.Helper()
	p, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "auth-health",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: "auth.test:80", Weight: 1}},
		MaxFails:    maxFails,
		FailTimeout: config.Duration(time.Hour),
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

type authContextBody struct{ ctx context.Context }

func (b authContextBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (authContextBody) Close() error { return nil }

func TestAuthDependencyClientCancellationIsHealthNeutralAndFailsClosed(t *testing.T) {
	p := authAttributionPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	d := &dependency{
		pool: p,
		client: &http.Client{Transport: roundTripperFn(func(r *http.Request) (*http.Response, error) {
			cancel()
			<-r.Context().Done()
			return nil, r.Context().Err()
		})},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://auth.test/verify", nil)
	if _, err := d.do(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation (the dependency must still fail closed)", err)
	}
	b := p.Backends()[0]
	if !b.Available() || b.FailCount() != 0 || b.Inflight() != 0 {
		t.Fatalf("auth client cancellation poisoned backend: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestAuthDependencyCancellationDuringBodyPreservesPriorFailure(t *testing.T) {
	p := authAttributionPoolWithMaxFails(t, 3)
	prior, err := p.Pick()
	if err != nil {
		t.Fatal(err)
	}
	p.RecordAttempt(prior, upstream.BackendProtocolFailure(upstream.ReasonUpstreamConnectFailed))
	p.Release(prior.Backend)

	ctx, cancel := context.WithCancel(context.Background())
	d := &dependency{
		pool: p,
		client: &http.Client{Transport: roundTripperFn(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: authContextBody{ctx: r.Context()}, ContentLength: -1}, nil
		})},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://auth.test/verify", nil)
	resp, err := d.do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := io.ReadAll(resp.Body); !errors.Is(err, context.Canceled) {
		t.Fatalf("body error = %v, want context canceled", err)
	}
	_ = resp.Body.Close()
	b := p.Backends()[0]
	if !b.Available() || b.FailCount() != 1 || b.Inflight() != 0 {
		t.Fatalf("mid-body auth cancellation changed prior health evidence: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestAuthDependencyBackendFailureStillTripsCircuit(t *testing.T) {
	p := authAttributionPool(t)
	d := &dependency{
		pool: p,
		client: &http.Client{Transport: roundTripperFn(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset")
		})},
	}
	req, _ := http.NewRequest(http.MethodGet, "http://auth.test/verify", nil)
	if _, err := d.do(req); err == nil {
		t.Fatal("backend failure returned no error")
	}
	if p.Backends()[0].Available() {
		t.Fatal("genuine auth backend failure did not trip max_fails=1 circuit")
	}
}
