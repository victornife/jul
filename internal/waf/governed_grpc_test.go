// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf && grpc

package waf

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"jul/internal/config"
	"jul/internal/inspection"
)

type governedStreamingService interface {
	Watch(grpc.ServerStream) error
}

type governedStreamService struct {
	started chan struct{}
	release <-chan struct{}
}

func (s *governedStreamService) Watch(stream grpc.ServerStream) error {
	var request emptypb.Empty
	if err := stream.RecvMsg(&request); err != nil {
		return err
	}
	if err := stream.SendMsg(&emptypb.Empty{}); err != nil {
		return err
	}
	close(s.started)
	<-s.release
	stream.SetTrailer(metadata.Pairs("x-stream-finished", "yes"))
	return stream.SendMsg(&emptypb.Empty{})
}

func TestGovernedWAFStreamingGRPCCommitReleasesTransaction(t *testing.T) {
	g := newGovernor(t, governedLimits(inspection.Enforce))
	fw := governedFirewall(t, config.WAFConfig{}, g, Hooks{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseHandler()
	service := &governedStreamService{started: make(chan struct{}), release: release}
	grpcServer := grpc.NewServer()
	grpcServer.RegisterService(&grpc.ServiceDesc{
		ServiceName: "test.Stream",
		HandlerType: (*governedStreamingService)(nil),
		Streams: []grpc.StreamDesc{{
			StreamName:    "Watch",
			Handler:       func(srv any, stream grpc.ServerStream) error { return srv.(governedStreamingService).Watch(stream) },
			ServerStreams: true,
		}},
	}, service)
	srv := httptest.NewUnstartedServer(fw.Middleware()(grpcServer))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(func() {
		grpcServer.Stop()
		srv.Close()
	})

	tlsConfig := srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	conn, err := grpc.NewClient(
		"passthrough:///"+srv.Listener.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/test.Stream/Watch")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SendMsg(&emptypb.Empty{}); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	var reply emptypb.Empty
	if err := stream.RecvMsg(&reply); err != nil {
		t.Fatalf("first stream message: %v", err)
	}
	select {
	case <-service.started:
	case <-ctx.Done():
		t.Fatal("server did not hold the stream after its first message")
	}
	during := g.Snapshot()
	if during.ActiveLeases != 0 || during.ActiveEvaluations != 0 ||
		during.Usage[inspection.WAFStateBytes] != 0 || during.Usage[inspection.AuditBytes] != 0 ||
		during.Usage[inspection.BufferBytes] != 0 {
		t.Fatalf("gRPC stream retained WAF transaction ownership after first message: %+v", during)
	}

	releaseHandler()
	if err := stream.RecvMsg(&reply); err != nil {
		t.Fatalf("second stream message: %v", err)
	}
	if err := stream.RecvMsg(&reply); err != io.EOF {
		t.Fatalf("stream completion = %v, want EOF/success", err)
	}
	if got := stream.Trailer().Get("x-stream-finished"); len(got) != 1 || got[0] != "yes" {
		t.Fatalf("stream trailer = %v, want x-stream-finished=yes", got)
	}
	waitDrained(t, g)
}

func TestGovernedWAFResponseAdmissionPreservesNativeGRPCStatusAndConnection(t *testing.T) {
	limits := governedLimits(inspection.Enforce)
	limits.MaxBytes = 64 << 20
	g := newGovernor(t, limits)
	investigation := NewInvestigationStore(8, time.Minute)
	investigation.SetEnabled(true)
	cfg := config.WAFConfig{}
	applyTestDefaults(&cfg)
	cfg.Enabled = true
	fw, err := New(context.Background(), cfg, Options{Governor: g, Investigation: investigation})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fw.Close() })
	filler, _, _, err := g.AcquireLease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var reached atomic.Bool
	mux.Handle("/limited/", fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		var used uint64
		for _, amount := range g.Snapshot().Usage {
			used += amount
		}
		if _, err := filler.Charge(inspection.BufferBytes, limits.MaxBytes-used-1024); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		w.WriteHeader(http.StatusOK)
	})))
	mux.HandleFunc("/healthy/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{0, 0, 0, 0, 0})
		w.Header().Set("Grpc-Status", "0")
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	var conns atomic.Int64
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	tlsConfig := srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	conn, err := grpc.NewClient(
		"passthrough:///"+srv.Listener.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var reply emptypb.Empty
	err = conn.Invoke(ctx, "/limited/Method", &emptypb.Empty{}, &reply)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("limited call status = %s (%v), want UNAVAILABLE", status.Code(err), err)
	}
	if !reached.Load() {
		t.Fatal("limited origin handler was not reached")
	}
	events := investigation.Snapshot(time.Time{}, InvestigationFilter{}).Events
	if len(events) != 1 || events[0].Outcome != OutcomeInspectionRefused ||
		events[0].ClientStatus != http.StatusOK || events[0].ErrorCategory != inspection.ReasonOverloaded {
		t.Fatalf("gRPC refusal outcome = %+v, want refused HTTP 200 / UNAVAILABLE", events)
	}
	if err := conn.Invoke(ctx, "/healthy/Method", &emptypb.Empty{}, &reply); err != nil {
		t.Fatalf("healthy sibling call: %v", err)
	}
	if conns.Load() != 1 {
		t.Fatalf("gRPC refusal did not preserve the H2 connection: %d connections", conns.Load())
	}
	_ = filler.Close()
	waitDrained(t, g)
}
