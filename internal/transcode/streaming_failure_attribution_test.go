// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/upstream"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

type scriptedClientStream struct {
	ctx       context.Context
	send      func(any) error
	recv      func(any) error
	closeSend func() error
}

func (*scriptedClientStream) Header() (metadata.MD, error) { return nil, nil }
func (*scriptedClientStream) Trailer() metadata.MD         { return nil }
func (s *scriptedClientStream) CloseSend() error {
	if s.closeSend != nil {
		return s.closeSend()
	}
	return nil
}
func (s *scriptedClientStream) Context() context.Context { return s.ctx }
func (s *scriptedClientStream) SendMsg(msg any) error {
	if s.send != nil {
		return s.send(msg)
	}
	return nil
}
func (s *scriptedClientStream) RecvMsg(msg any) error {
	if s.recv != nil {
		return s.recv(msg)
	}
	return io.EOF
}

type streamFailingWriter struct{ header http.Header }

func (w *streamFailingWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (*streamFailingWriter) WriteHeader(int) {}
func (*streamFailingWriter) Write([]byte) (int, error) {
	return 0, errors.New("downstream connection closed")
}

func streamingFailureRoute(t *testing.T, name string) *route {
	t.Helper()
	routes, err := routesFromSet(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{streamingFileDescriptorProto(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range routes {
		if string(rt.method.Name()) == name {
			return rt
		}
	}
	t.Fatalf("streaming route %q not found", name)
	return nil
}

func streamingFailurePool(t *testing.T) *upstream.Pool {
	t.Helper()
	p, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "stream-failure",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}},
		MaxFails: 3,
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func pickStreamAttempt(t *testing.T, p *upstream.Pool) upstream.Attempt {
	t.Helper()
	attempt, err := p.Pick()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Release(attempt.Backend) })
	return attempt
}

func TestServerStreamSendFailureRecordsBackendFault(t *testing.T) {
	p := streamingFailurePool(t)
	tr := &Transcoder{pool: p, maxMsg: 1 << 20}
	rt := streamingFailureRoute(t, "Down")
	ctx := context.Background()
	cs := &scriptedClientStream{
		ctx: ctx,
		send: func(any) error {
			return status.Error(codes.Unavailable, "send failed")
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/down", strings.NewReader(`{"value":"x"}`))
	if got := tr.serveServerStream(httptest.NewRecorder(), req, rt, nil, cs, ctx, "Down", pickStreamAttempt(t, p)); got != healthRecorded {
		t.Fatalf("health = %d, want recorded", got)
	}
	if got := p.Backends()[0].FailCount(); got != 1 {
		t.Fatalf("failure count = %d, want 1", got)
	}
}

func TestClientStreamTransportFailuresRecordBackendFault(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sendErr error
		recvErr error
	}{
		{name: "send", sendErr: status.Error(codes.Unavailable, "send failed")},
		{name: "receive", recvErr: status.Error(codes.Unavailable, "receive failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := streamingFailurePool(t)
			tr := &Transcoder{pool: p, maxMsg: 1 << 20}
			rt := streamingFailureRoute(t, "Up")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cs := &scriptedClientStream{
				ctx:  ctx,
				send: func(any) error { return tc.sendErr },
				recv: func(any) error { return tc.recvErr },
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/up", strings.NewReader(`{"value":"x"}`)).WithContext(ctx)
			if got := tr.serveClientStream(httptest.NewRecorder(), req, rt, nil, cs, ctx, cancel, "Up", pickStreamAttempt(t, p)); got != healthRecorded {
				t.Fatalf("health = %d, want recorded", got)
			}
			if got := p.Backends()[0].FailCount(); got != 1 {
				t.Fatalf("failure count = %d, want 1", got)
			}
		})
	}
}

func TestBidiSendFailuresKeepTheirOriginalAttribution(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		sendErr   error
		wantFails int32
	}{
		{name: "decode", body: `not-json`, wantFails: 0},
		{name: "backend", body: `{"value":"x"}`, sendErr: status.Error(codes.Unavailable, "send failed"), wantFails: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := streamingFailurePool(t)
			tr := &Transcoder{pool: p}
			rt := streamingFailureRoute(t, "Both")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cs := &scriptedClientStream{
				ctx:  ctx,
				send: func(any) error { return tc.sendErr },
				recv: func(any) error {
					<-ctx.Done()
					return status.Error(codes.Canceled, ctx.Err().Error())
				},
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/both", strings.NewReader(tc.body)).WithContext(ctx)
			if got := tr.serveBidiStream(httptest.NewRecorder(), req, rt, nil, cs, ctx, cancel, "Both", pickStreamAttempt(t, p)); got != healthRecorded {
				t.Fatalf("health = %d, want recorded", got)
			}
			if got := p.Backends()[0].FailCount(); got != tc.wantFails {
				t.Fatalf("failure count = %d, want %d", got, tc.wantFails)
			}
		})
	}
}

func TestPumpRepliesClassifiesBackendAndDownstreamFailures(t *testing.T) {
	t.Run("backend receive", func(t *testing.T) {
		p := streamingFailurePool(t)
		tr := &Transcoder{pool: p}
		rt := streamingFailureRoute(t, "Down")
		cs := &scriptedClientStream{
			ctx:  context.Background(),
			recv: func(any) error { return status.Error(codes.Unavailable, "receive failed") },
		}
		resp := newStreamResponder(httptest.NewRecorder(), "ndjson")
		if got := tr.pumpReplies(resp, cs, rt, "Down", pickStreamAttempt(t, p), context.Background(), context.Background()); got != healthRecorded {
			t.Fatalf("health = %d, want recorded", got)
		}
		if got := p.Backends()[0].FailCount(); got != 1 {
			t.Fatalf("failure count = %d, want 1", got)
		}
	})

	t.Run("downstream write", func(t *testing.T) {
		p := streamingFailurePool(t)
		tr := &Transcoder{pool: p}
		rt := streamingFailureRoute(t, "Down")
		attempt := pickStreamAttempt(t, p)
		out := dynamicpb.NewMessage(rt.method.Output())
		field := out.Descriptor().Fields().ByName("value")
		out.Set(field, out.NewField(field))
		cs := &scriptedClientStream{
			ctx: context.Background(),
			recv: func(msg any) error {
				msg.(*dynamicpb.Message).Set(field, out.Get(field))
				return nil
			},
		}
		resp := newStreamResponder(&streamFailingWriter{}, "ndjson")
		if got := tr.pumpReplies(resp, cs, rt, "Down", attempt, context.Background(), context.Background()); got != healthRecorded {
			t.Fatalf("health = %d, want recorded", got)
		}
		if got := p.Backends()[0].FailCount(); got != 0 {
			t.Fatalf("downstream failure changed backend failure count to %d", got)
		}
	})
}

func TestBidiReceiveAndDownstreamFailuresAreAttributedOnce(t *testing.T) {
	for _, tc := range []struct {
		name      string
		writer    http.ResponseWriter
		recv      func(any) error
		wantFails int32
	}{
		{
			name:      "backend receive",
			writer:    httptest.NewRecorder(),
			recv:      func(any) error { return status.Error(codes.Unavailable, "receive failed") },
			wantFails: 1,
		},
		{
			name:   "downstream write",
			writer: &streamFailingWriter{},
			recv: func(msg any) error {
				out := msg.(*dynamicpb.Message)
				field := out.Descriptor().Fields().ByName("value")
				out.Set(field, out.NewField(field))
				return nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := streamingFailurePool(t)
			tr := &Transcoder{pool: p}
			rt := streamingFailureRoute(t, "Both")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cs := &scriptedClientStream{ctx: ctx, recv: tc.recv}
			req := httptest.NewRequest(http.MethodPost, "/v1/both", strings.NewReader(`{"value":"x"}`)).WithContext(ctx)
			if got := tr.serveBidiStream(tc.writer, req, rt, nil, cs, ctx, cancel, "Both", pickStreamAttempt(t, p)); got != healthRecorded {
				t.Fatalf("health = %d, want recorded", got)
			}
			if got := p.Backends()[0].FailCount(); got != tc.wantFails {
				t.Fatalf("failure count = %d, want %d", got, tc.wantFails)
			}
		})
	}
}

func TestClientStreamDownstreamWriteFailureIsNeutral(t *testing.T) {
	p := streamingFailurePool(t)
	tr := &Transcoder{pool: p}
	rt := streamingFailureRoute(t, "Up")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &scriptedClientStream{
		ctx: ctx,
		recv: func(msg any) error {
			out := msg.(*dynamicpb.Message)
			field := out.Descriptor().Fields().ByName("value")
			out.Set(field, out.NewField(field))
			return nil
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/up", strings.NewReader(`[{"value":"x"}]`)).WithContext(ctx)
	if got := tr.serveClientStream(&streamFailingWriter{}, req, rt, nil, cs, ctx, cancel, "Up", pickStreamAttempt(t, p)); got != healthRecorded {
		t.Fatalf("health = %d, want neutral recorded result", got)
	}
	if got := p.Backends()[0].FailCount(); got != 0 {
		t.Fatalf("downstream write failure changed backend failure count to %d", got)
	}
}

func TestServerStreamSSEEndWriteFailureIsNeutral(t *testing.T) {
	p := streamingFailurePool(t)
	tr := &Transcoder{pool: p}
	rt := streamingFailureRoute(t, "Down")
	cs := &scriptedClientStream{ctx: context.Background()}
	resp := newStreamResponder(&streamFailingWriter{}, "sse")
	if got := tr.pumpReplies(resp, cs, rt, "Down", pickStreamAttempt(t, p), context.Background(), context.Background()); got != healthRecorded {
		t.Fatalf("health = %d, want neutral recorded result", got)
	}
	if got := p.Backends()[0].FailCount(); got != 0 {
		t.Fatalf("SSE completion write failure changed backend failure count to %d", got)
	}
}

func TestStreamingJulAttemptTimeoutIsNeutral(t *testing.T) {
	p := streamingFailurePool(t)
	tr := &Transcoder{pool: p}
	rt := streamingFailureRoute(t, "Down")
	attempt, cancel := context.WithCancel(context.Background())
	cancel()
	cs := &scriptedClientStream{
		ctx:  attempt,
		recv: func(any) error { return status.Error(codes.DeadlineExceeded, "attempt deadline") },
	}
	resp := newStreamResponder(httptest.NewRecorder(), "ndjson")
	if got := tr.pumpReplies(resp, cs, rt, "Down", pickStreamAttempt(t, p), context.Background(), attempt); got != healthRecorded {
		t.Fatalf("health = %d, want neutral recorded result", got)
	}
	if got := p.Backends()[0].FailCount(); got != 0 {
		t.Fatalf("Jul timeout changed backend failure count to %d", got)
	}
}

func TestStreamingCloseSendFailuresKeepBackendAttribution(t *testing.T) {
	for _, kind := range []string{"server", "client", "bidi"} {
		t.Run(kind, func(t *testing.T) {
			p := streamingFailurePool(t)
			tr := &Transcoder{pool: p, maxMsg: 1 << 20}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var closes atomic.Int32
			var sends atomic.Int32
			closeErr := status.Error(codes.Unavailable, "close send failed")
			cs := &scriptedClientStream{
				ctx:       ctx,
				send:      func(any) error { sends.Add(1); return nil },
				closeSend: func() error { closes.Add(1); return closeErr },
				recv: func(any) error {
					<-ctx.Done()
					return status.Error(codes.Canceled, ctx.Err().Error())
				},
			}
			body := `[{"value":"x"}]`
			path := "/v1/" + map[string]string{"server": "down", "client": "up", "bidi": "both"}[kind]
			if kind == "server" {
				body = `{"value":"x"}`
			}
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
			var got streamHealth
			switch kind {
			case "server":
				got = tr.serveServerStream(httptest.NewRecorder(), req, streamingFailureRoute(t, "Down"), nil, cs, ctx, "Down", pickStreamAttempt(t, p))
			case "client":
				got = tr.serveClientStream(httptest.NewRecorder(), req, streamingFailureRoute(t, "Up"), nil, cs, ctx, cancel, "Up", pickStreamAttempt(t, p))
			default:
				got = tr.serveBidiStream(httptest.NewRecorder(), req, streamingFailureRoute(t, "Both"), nil, cs, ctx, cancel, "Both", pickStreamAttempt(t, p))
			}
			if got != healthRecorded {
				t.Fatalf("health = %d, want recorded", got)
			}
			if closes.Load() != 1 {
				t.Fatalf("CloseSend calls = %d, want 1 (SendMsg calls=%d)", closes.Load(), sends.Load())
			}
			if fails := p.Backends()[0].FailCount(); fails != 1 {
				classification := classifyGRPCAttempt(closeErr, req.Context(), ctx)
				t.Fatalf("failure count = %d, want 1 (classification origin=%q health=%d)", fails, classification.Origin(), classification.Health())
			}
		})
	}
}

func TestBidiSSEEndWriteFailureIsNeutral(t *testing.T) {
	p := streamingFailurePool(t)
	tr := &Transcoder{pool: p, streamMode: "sse"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &scriptedClientStream{ctx: ctx}
	req := httptest.NewRequest(http.MethodPost, "/v1/both", strings.NewReader(""))
	if got := tr.serveBidiStream(&streamFailingWriter{}, req, streamingFailureRoute(t, "Both"), nil, cs, ctx, cancel, "Both", pickStreamAttempt(t, p)); got != healthRecorded {
		t.Fatalf("health = %d, want neutral recorded result", got)
	}
	if fails := p.Backends()[0].FailCount(); fails != 0 {
		t.Fatalf("SSE end write failure changed backend failure count to %d", fails)
	}
}
