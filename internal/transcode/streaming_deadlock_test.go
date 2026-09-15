// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/upstream"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// deadlockGuard bounds how long a test waits for a channel before declaring a
// reproduced deadlock. It is a deadlock guard only (per the no-sleep-based-
// synchronization rule): the channels themselves are the real signal, this
// only prevents the test suite from hanging forever if the bug reproduces.
const deadlockGuard = 10 * time.Second

// blockingBody is a deterministic downstream request body reproducing a
// client that sends one valid JSON frame and then stalls: the first Read
// returns the frame, the next Read signals that it has started (via Started)
// and then blocks until Close is called. Close is safe to call more than
// once and counts its calls.
type blockingBody struct {
	frame     []byte
	served    bool
	Started   chan struct{}
	startOnce sync.Once
	block     chan struct{}
	closeOnce sync.Once
	closes    atomic.Int32
}

func newBlockingBody(frame string) *blockingBody {
	return &blockingBody{
		frame:   []byte(frame),
		Started: make(chan struct{}),
		block:   make(chan struct{}),
	}
}

func (b *blockingBody) Read(p []byte) (int, error) {
	if !b.served {
		b.served = true
		return copy(p, b.frame), nil
	}
	b.startOnce.Do(func() { close(b.Started) })
	<-b.block
	return 0, io.ErrClosedPipe
}

// Close unblocks a pending Read and records that it was called. It never
// blocks and is safe to call any number of times.
func (b *blockingBody) Close() error {
	b.closes.Add(1)
	b.closeOnce.Do(func() { close(b.block) })
	return nil
}

func (b *blockingBody) CloseCalls() int32 { return b.closes.Load() }

// startEarlyAbortClientStreamServer serves only the client-streaming "Up"
// method: it receives exactly one message, then immediately returns a
// terminal status without waiting for the client to finish uploading,
// reproducing a backend that aborts a client-streaming call early.
func startEarlyAbortClientStreamServer(t *testing.T, fd protoreflect.FileDescriptor, code codes.Code) string {
	t.Helper()
	item := fd.Messages().ByName("Item")
	up := func(_ any, stream grpc.ServerStream) error {
		if err := stream.RecvMsg(dynamicpb.NewMessage(item)); err != nil {
			return err
		}
		return status.Error(code, "backend terminal failure")
	}
	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "streamecho.StreamEcho",
		HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{
			{StreamName: "Up", Handler: up, ClientStreams: true},
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

// startEarlySuccessClientStreamServer serves only "Up": it receives exactly
// one message and immediately answers successfully, without waiting for the
// client to finish uploading (a client-streaming RPC may legally do this).
func startEarlySuccessClientStreamServer(t *testing.T, fd protoreflect.FileDescriptor) string {
	t.Helper()
	item := fd.Messages().ByName("Item")
	valueField := item.Fields().ByName("value")
	up := func(_ any, stream grpc.ServerStream) error {
		in := dynamicpb.NewMessage(item)
		if err := stream.RecvMsg(in); err != nil {
			return err
		}
		out := dynamicpb.NewMessage(item)
		out.Set(valueField, in.Get(valueField))
		return stream.SendMsg(out)
	}
	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "streamecho.StreamEcho",
		HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{
			{StreamName: "Up", Handler: up, ClientStreams: true},
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

func streamEchoFile(t *testing.T) protoreflect.FileDescriptor {
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
	return fd
}

// TestRealGRPCClientStreamBackendAbortUnblocksStalledUpload reproduces
// Blocker A's client-streaming defect: a real grpc-go backend aborts the RPC
// after one message while the downstream request body stalls (leaves the
// upload open). Before the fix, sendRequestFrames ran to completion before
// RecvMsg was ever called, so the handler blocked forever on the stalled
// body read even though the backend had already answered.
func TestRealGRPCClientStreamBackendAbortUnblocksStalledUpload(t *testing.T) {
	fd := streamEchoFile(t)
	addr := startEarlyAbortClientStreamServer(t, fd, codes.Unavailable)
	tr, pool := newStreamTranscoderAt(t, addr, 1, upstream.RetryOverride{})

	body := newBlockingBody(`{"value":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/up", body)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		tr.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-body.Started:
	case <-time.After(deadlockGuard):
		t.Fatal("downstream upload never reached the blocked read (first frame was not sent)")
	}

	select {
	case <-done:
	case <-time.After(deadlockGuard):
		t.Fatal("handler deadlocked: backend terminal result did not unblock the stalled downstream upload")
	}

	if calls := body.CloseCalls(); calls == 0 {
		t.Fatal("Jul never closed the stalled request body after the backend terminated")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s; want 503 (Unavailable)", rec.Code, rec.Body.String())
	}
	backend := pool.Backends()[0]
	if backend.Available() || backend.State() != upstream.StateCircuitOpen || backend.Inflight() != 0 {
		t.Fatalf("backend state = %q available=%t inflight=%d; want open/ineligible/0 after real grpc-go Unavailable",
			backend.State(), backend.Available(), backend.Inflight())
	}
}

// TestRealGRPCBidiBackendAbortUnblocksStalledUpload reproduces Blocker A's
// bidirectional-streaming defect: the receive loop's plain RecvMsg-error
// paths never unblocked a sender stalled on the downstream body before
// waiting on <-done.
func TestRealGRPCBidiBackendAbortUnblocksStalledUpload(t *testing.T) {
	fd := streamEchoFile(t)
	addr := startFailingStreamServer(t, fd, codes.Unavailable) // "Both" reads one message then fails.
	tr, pool := newStreamTranscoderAt(t, addr, 1, upstream.RetryOverride{})

	body := newBlockingBody(`{"value":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/both", body)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		tr.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-body.Started:
	case <-time.After(deadlockGuard):
		t.Fatal("downstream upload never reached the blocked read (first frame was not sent)")
	}

	select {
	case <-done:
	case <-time.After(deadlockGuard):
		t.Fatal("bidi handler deadlocked: backend terminal result did not unblock the stalled downstream upload")
	}

	if calls := body.CloseCalls(); calls == 0 {
		t.Fatal("Jul never closed the stalled request body after the backend terminated")
	}
	backend := pool.Backends()[0]
	if backend.Available() || backend.State() != upstream.StateCircuitOpen || backend.Inflight() != 0 {
		t.Fatalf("backend state = %q available=%t inflight=%d; want open/ineligible/0 after real grpc-go Unavailable",
			backend.State(), backend.Available(), backend.Inflight())
	}
}

// TestRealGRPCClientStreamCleanCompletionUnblocksStalledUpload covers the
// non-error half of the same invariant: a backend may legally answer a
// client-streaming call successfully before the client finishes uploading.
// That must also unblock the stalled body read rather than only a failure
// path doing so.
func TestRealGRPCClientStreamCleanCompletionUnblocksStalledUpload(t *testing.T) {
	fd := streamEchoFile(t)
	addr := startEarlySuccessClientStreamServer(t, fd)
	tr, _ := newStreamTranscoderAt(t, addr, 1, upstream.RetryOverride{})

	body := newBlockingBody(`{"value":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/up", body)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		tr.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-body.Started:
	case <-time.After(deadlockGuard):
		t.Fatal("downstream upload never reached the blocked read (first frame was not sent)")
	}

	select {
	case <-done:
	case <-time.After(deadlockGuard):
		t.Fatal("handler deadlocked after a clean early backend completion")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	if calls := body.CloseCalls(); calls == 0 {
		t.Fatal("Jul never closed the stalled request body after the backend completed successfully")
	}
}
