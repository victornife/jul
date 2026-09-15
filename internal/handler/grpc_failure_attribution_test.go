// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

type grpcRoundTripFunc func(*http.Request) (*http.Response, error)

func (f grpcRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func grpcAttributionPool(t *testing.T, failTimeout time.Duration) *upstream.Pool {
	return grpcAttributionPoolWithMaxFails(t, 1, failTimeout)
}

func grpcAttributionPoolWithMaxFails(t *testing.T, maxFails int, failTimeout time.Duration) *upstream.Pool {
	t.Helper()
	p, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "grpc-health",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: "backend.test:80", Weight: 1}},
		MaxFails:    maxFails,
		FailTimeout: config.Duration(failTimeout),
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

type grpcContextBody struct{ ctx context.Context }

func (b grpcContextBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (grpcContextBody) Close() error { return nil }

func TestNativeGRPCClientCancellationDoesNotTripBackend(t *testing.T) {
	p := grpcAttributionPool(t, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	tr := &grpcBalancingTransport{
		pool: p,
		base: grpcRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			cancel()
			<-r.Context().Done()
			return nil, r.Context().Err()
		}),
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://edge.test/service.Method", nil)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("cancelled call returned no error")
	}
	b := p.Backends()[0]
	if !b.Available() || b.FailCount() != 0 || b.Inflight() != 0 {
		t.Fatalf("client cancellation poisoned backend: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestNativeGRPCClientDeadlineDoesNotTripBackend(t *testing.T) {
	p := grpcAttributionPool(t, time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	tr := &grpcBalancingTransport{
		pool: p,
		base: grpcRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, r.Context().Err()
		}),
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://edge.test/service.Method", nil)
	if _, err := tr.RoundTrip(req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	b := p.Backends()[0]
	if !b.Available() || b.FailCount() != 0 || b.Inflight() != 0 {
		t.Fatalf("client deadline poisoned backend: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestNativeGRPCClientCancellationDuringBodyPreservesPriorFailure(t *testing.T) {
	p := grpcAttributionPoolWithMaxFails(t, 3, time.Hour)
	prior, err := p.Pick()
	if err != nil {
		t.Fatal(err)
	}
	p.RecordAttempt(prior, upstream.BackendProtocolFailure(upstream.ReasonUpstreamConnectFailed))
	p.Release(prior.Backend)

	ctx, cancel := context.WithCancel(context.Background())
	tr := &grpcBalancingTransport{
		pool: p,
		base: grpcRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: grpcContextBody{ctx: r.Context()}, ContentLength: -1}, nil
		}),
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://edge.test/service.Method", nil)
	resp, err := tr.RoundTrip(req)
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
		t.Fatalf("mid-body gRPC cancellation changed prior health evidence: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestNativeGRPCBackendFailureStillTripsCircuit(t *testing.T) {
	p := grpcAttributionPool(t, time.Hour)
	tr := &grpcBalancingTransport{
		pool: p,
		base: grpcRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("backend reset")
		}),
	}
	req, _ := http.NewRequest(http.MethodPost, "http://edge.test/service.Method", nil)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("backend failure returned no error")
	}
	b := p.Backends()[0]
	if b.Available() || b.Inflight() != 0 {
		t.Fatalf("backend failure did not trip cleanly: available=%t inflight=%d", b.Available(), b.Inflight())
	}
}

func TestNativeGRPCApplicationStatusIsBackendSuccess(t *testing.T) {
	p := grpcAttributionPool(t, time.Hour)
	tr := &grpcBalancingTransport{
		pool: p,
		base: grpcRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Grpc-Status": []string{"14"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}),
	}
	req, _ := http.NewRequest(http.MethodPost, "http://edge.test/service.Method", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	b := p.Backends()[0]
	if !b.Available() || b.FailCount() != 0 || b.Inflight() != 0 {
		t.Fatalf("gRPC application status changed transport health: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestNativeGRPCTLSIdentityFailureIsPoolNeutral(t *testing.T) {
	p := grpcAttributionPool(t, time.Hour)
	tr := &grpcBalancingTransport{
		pool: p,
		base: grpcRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, x509.HostnameError{Host: "wrong.internal"}
		}),
	}
	req, _ := http.NewRequest(http.MethodPost, "http://edge.test/service.Method", nil)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("identity failure returned no error")
	}
	b := p.Backends()[0]
	if !b.Available() || b.FailCount() != 0 {
		t.Fatalf("route-level identity failure changed shared pool health: available=%t fails=%d", b.Available(), b.FailCount())
	}
}

func TestNativeGRPCHalfOpenCancellationReturnsProbe(t *testing.T) {
	p := grpcAttributionPool(t, time.Millisecond)
	first, err := p.Pick()
	if err != nil {
		t.Fatal(err)
	}
	p.RecordAttempt(first, upstream.BackendProtocolFailure(upstream.ReasonUpstreamConnectFailed))
	p.Release(first.Backend)
	time.Sleep(2 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	tr := &grpcBalancingTransport{
		pool: p,
		base: grpcRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			cancel()
			<-r.Context().Done()
			return nil, r.Context().Err()
		}),
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://edge.test/service.Method", nil)
	_, _ = tr.RoundTrip(req)

	next, err := p.Pick()
	if err != nil {
		t.Fatalf("cancelled half-open call did not return probe slot: %v", err)
	}
	p.RecordAttempt(next, upstream.SuccessfulAttempt())
	p.Release(next.Backend)
}
