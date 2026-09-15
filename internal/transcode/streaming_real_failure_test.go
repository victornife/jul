// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func startFailingStreamServer(t *testing.T, fd protoreflect.FileDescriptor, code codes.Code) string {
	t.Helper()
	item := fd.Messages().ByName("Item")
	recvOneThenFail := func(_ any, stream grpc.ServerStream) error {
		if err := stream.RecvMsg(dynamicpb.NewMessage(item)); err != nil {
			return err
		}
		return status.Error(code, "backend terminal failure")
	}
	recvAllThenFail := func(_ any, stream grpc.ServerStream) error {
		for {
			err := stream.RecvMsg(dynamicpb.NewMessage(item))
			if errors.Is(err, io.EOF) {
				return status.Error(code, "backend terminal failure")
			}
			if err != nil {
				return err
			}
		}
	}

	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "streamecho.StreamEcho",
		HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{
			{StreamName: "Down", Handler: recvOneThenFail, ServerStreams: true},
			{StreamName: "Up", Handler: recvAllThenFail, ClientStreams: true},
			{StreamName: "Both", Handler: recvOneThenFail, ServerStreams: true, ClientStreams: true},
		},
		Metadata: "streamecho/streamecho.proto",
	}, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func newStreamTranscoderAt(t *testing.T, addr string, maxFails int, retry upstream.RetryOverride) (*Transcoder, *upstream.Pool) {
	t.Helper()
	fdp := streamingFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	raw, err := proto.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	desc := filepath.Join(t.TempDir(), "streamecho.pb")
	if err := os.WriteFile(desc, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "real-stream-failure",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails:    maxFails,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tr, err := New(context.Background(), config.GRPCTranscodeConfig{
		Target:        addr,
		DescriptorSet: desc,
		Streaming:     true,
		StreamMode:    "ndjson",
	}, p, nil, Options{Retry: retry})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr, p
}

func newFailingStreamTranscoder(t *testing.T, code codes.Code) (*Transcoder, *upstream.Pool) {
	t.Helper()
	fdp := streamingFileDescriptorProto(t)
	files, err := filesFromSet(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}})
	if err != nil {
		t.Fatal(err)
	}
	fd, err := files.FindFileByPath("streamecho/streamecho.proto")
	if err != nil {
		t.Fatal(err)
	}
	return newStreamTranscoderAt(t, startFailingStreamServer(t, fd, code), 1, upstream.RetryOverride{})
}

func startBlockingStreamServer(t *testing.T, fd protoreflect.FileDescriptor) (string, <-chan struct{}, *grpc.Server) {
	t.Helper()
	item := fd.Messages().ByName("Item")
	started := make(chan struct{})
	down := func(_ any, stream grpc.ServerStream) error {
		if err := stream.RecvMsg(dynamicpb.NewMessage(item)); err != nil {
			return err
		}
		close(started)
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "streamecho.StreamEcho",
		HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{
			{StreamName: "Down", Handler: down, ServerStreams: true},
		},
		Metadata: "streamecho/streamecho.proto",
	}, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String(), started, srv
}

func TestRealGRPCStreamingUnavailableTripsCircuit(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{name: "server stream", path: "/v1/down", body: `{"value":"x"}`},
		{name: "client stream", path: "/v1/up", body: `[{"value":"x"}]`},
		{name: "bidirectional stream", path: "/v1/both", body: `[{"value":"x"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, pool := newFailingStreamTranscoder(t, codes.Unavailable)
			doRequest(t, tr, http.MethodPost, tc.path, tc.body, nil)
			backend := pool.Backends()[0]
			if backend.Available() || backend.State() != upstream.StateCircuitOpen {
				t.Fatalf("max_fails=1 backend state = %q, available=%t; want open and ineligible after real grpc-go Unavailable", backend.State(), backend.Available())
			}
			if _, err := pool.Pick(); err == nil {
				t.Fatal("max_fails=1 circuit selected a backend after Unavailable")
			}
		})
	}
}

func TestRealGRPCStreamFinalizationCancelsOnlyDerivedStreamContext(t *testing.T) {
	fdp := streamingFileDescriptorProto(t)
	files, err := filesFromSet(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}})
	if err != nil {
		t.Fatal(err)
	}
	fd, err := files.FindFileByPath("streamecho/streamecho.proto")
	if err != nil {
		t.Fatal(err)
	}
	addr := startFailingStreamServer(t, fd, codes.Unavailable)
	conn, err := dial(addr, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	attempt, cancel := context.WithCancel(context.Background())
	defer cancel()
	desc := &grpc.StreamDesc{ServerStreams: true}
	stream, err := conn.NewStream(attempt, desc, "/streamecho.StreamEcho/Down")
	if err != nil {
		t.Fatal(err)
	}
	input := dynamicpb.NewMessage(fd.Messages().ByName("Item"))
	if err := stream.SendMsg(input); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	err = stream.RecvMsg(dynamicpb.NewMessage(fd.Messages().ByName("Item")))
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("terminal status = %v, want Unavailable", status.Code(err))
	}
	if !errors.Is(stream.Context().Err(), context.Canceled) {
		t.Fatalf("grpc-go stream context error = %v, want canceled after terminal status", stream.Context().Err())
	}
	if err := attempt.Err(); err != nil {
		t.Fatalf("Jul-owned attempt context was mutated by grpc-go: %v", err)
	}
	classification := classifyGRPCAttempt(err, context.Background(), attempt)
	if classification.Origin() != upstream.OriginBackendProtocol || classification.Health() != upstream.HealthFailure {
		t.Fatalf("classification = origin %q health %d; want backend protocol failure", classification.Origin(), classification.Health())
	}
}

func TestRealGRPCStreamingApplicationStatusIsCircuitSuccess(t *testing.T) {
	tr, pool := newFailingStreamTranscoder(t, codes.InvalidArgument)
	doRequest(t, tr, http.MethodPost, "/v1/down", `{"value":"x"}`, nil)
	backend := pool.Backends()[0]
	if !backend.Available() || backend.FailCount() != 0 {
		t.Fatalf("application status changed backend health: state=%q fails=%d", backend.State(), backend.FailCount())
	}
}

func TestRealGRPCStreamingClientCancellationDuringReceiveIsNeutral(t *testing.T) {
	fdp := streamingFileDescriptorProto(t)
	files, err := filesFromSet(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}})
	if err != nil {
		t.Fatal(err)
	}
	fd, err := files.FindFileByPath("streamecho/streamecho.proto")
	if err != nil {
		t.Fatal(err)
	}
	addr, started, _ := startBlockingStreamServer(t, fd)
	tr, pool := newStreamTranscoderAt(t, addr, 1, upstream.RetryOverride{})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/down", strings.NewReader(`{"value":"x"}`)).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		tr.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	<-started
	cancel()
	<-done
	backend := pool.Backends()[0]
	if !backend.Available() || backend.FailCount() != 0 || backend.Inflight() != 0 {
		t.Fatalf("client cancellation changed backend health: state=%q fails=%d inflight=%d", backend.State(), backend.FailCount(), backend.Inflight())
	}
}

func TestRealGRPCStreamingBackendTransportClosureTripsCircuit(t *testing.T) {
	fdp := streamingFileDescriptorProto(t)
	files, err := filesFromSet(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}})
	if err != nil {
		t.Fatal(err)
	}
	fd, err := files.FindFileByPath("streamecho/streamecho.proto")
	if err != nil {
		t.Fatal(err)
	}
	addr, started, server := startBlockingStreamServer(t, fd)
	tr, pool := newStreamTranscoderAt(t, addr, 1, upstream.RetryOverride{})
	req := httptest.NewRequest(http.MethodPost, "/v1/down", strings.NewReader(`{"value":"x"}`))
	done := make(chan struct{})
	go func() {
		tr.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	<-started
	server.Stop()
	<-done
	backend := pool.Backends()[0]
	if backend.Available() || backend.State() != upstream.StateCircuitOpen || backend.Inflight() != 0 {
		t.Fatalf("backend transport closure state=%q available=%t inflight=%d; want open/ineligible/0", backend.State(), backend.Available(), backend.Inflight())
	}
}

func TestRealGRPCStreamingDeadlinesAreHealthNeutral(t *testing.T) {
	fdp := streamingFileDescriptorProto(t)
	files, err := filesFromSet(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}})
	if err != nil {
		t.Fatal(err)
	}
	fd, err := files.FindFileByPath("streamecho/streamecho.proto")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Jul attempt deadline", func(t *testing.T) {
		addr, started, _ := startBlockingStreamServer(t, fd)
		tr, pool := newStreamTranscoderAt(t, addr, 1, upstream.RetryOverride{Deadline: 20 * time.Millisecond})
		done := make(chan struct{})
		go func() {
			tr.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/down", strings.NewReader(`{"value":"x"}`)))
			close(done)
		}()
		<-started
		<-done
		backend := pool.Backends()[0]
		if !backend.Available() || backend.FailCount() != 0 || backend.Inflight() != 0 {
			t.Fatalf("Jul deadline changed backend health: state=%q fails=%d inflight=%d", backend.State(), backend.FailCount(), backend.Inflight())
		}
	})

	t.Run("inbound client deadline", func(t *testing.T) {
		addr, started, _ := startBlockingStreamServer(t, fd)
		tr, pool := newStreamTranscoderAt(t, addr, 1, upstream.RetryOverride{})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		req := httptest.NewRequest(http.MethodPost, "/v1/down", strings.NewReader(`{"value":"x"}`)).WithContext(ctx)
		done := make(chan struct{})
		go func() {
			tr.ServeHTTP(httptest.NewRecorder(), req)
			close(done)
		}()
		<-started
		<-done
		backend := pool.Backends()[0]
		if !backend.Available() || backend.FailCount() != 0 || backend.Inflight() != 0 {
			t.Fatalf("client deadline changed backend health: state=%q fails=%d inflight=%d", backend.State(), backend.FailCount(), backend.Inflight())
		}
	})
}
