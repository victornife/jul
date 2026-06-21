//go:build grpc

package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"jul/internal/config"
)

// rawCodec is a transparent gRPC codec that carries opaque byte payloads, so the
// test client and the echo backend exchange messages without a .proto schema.
// The passthrough proxy never touches the payload, so this exercises the real
// HTTP/2 path end to end.
type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) {
	switch b := v.(type) {
	case []byte:
		return b, nil
	case *[]byte:
		return *b, nil
	}
	return nil, fmt.Errorf("rawCodec: unexpected type %T", v)
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	p, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("rawCodec: unexpected type %T", v)
	}
	*p = append((*p)[:0], data...)
	return nil
}

func (rawCodec) Name() string { return "raw" }

func grpcTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// echoStream echoes every received frame back to the caller and sets a trailer,
// so a test can assert that both streaming frames and trailers survive the
// passthrough proxy.
func echoStream(_ any, stream grpc.ServerStream) error {
	stream.SetTrailer(metadata.Pairs("x-echo-trailer", "ok"))
	for {
		var msg []byte
		if err := stream.RecvMsg(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := stream.SendMsg(&msg); err != nil {
			return err
		}
	}
}

// startGRPCEcho starts a cleartext (h2c) gRPC server that echoes any method and
// returns its address.
func startGRPCEcho(t testing.TB) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer(
		grpc.UnknownServiceHandler(echoStream),
		grpc.ForceServerCodec(rawCodec{}),
	)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

// startGRPCProxyFront builds the gRPC passthrough handler for backendAddr and
// serves it over h2c on a fresh listener, returning the front address and the
// stream counter the handler increments per forwarded call.
func startGRPCProxyFront(t testing.TB, backendAddr string) (string, *atomic.Int64) {
	t.Helper()
	var streams atomic.Int64
	loc := config.LocationConfig{
		ProxyPass: "http://" + backendAddr,
		GRPC:      true,
	}
	h, err := NewGRPCProxy(config.ServerConfig{}, loc, map[string]config.UpstreamConfig{}, nil, grpcTestLogger(), func() { streams.Add(1) })
	if err != nil {
		t.Fatalf("NewGRPCProxy: %v", err)
	}

	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen front: %v", err)
	}
	// Serve prior-knowledge cleartext HTTP/2 (h2c) via the standard library's
	// Protocols negotiation, matching how the real server enables h2c.
	srv := &http.Server{Handler: h}
	var proto http.Protocols
	proto.SetHTTP1(true)
	proto.SetUnencryptedHTTP2(true)
	srv.Protocols = &proto
	go func() { _ = srv.Serve(front) }()
	t.Cleanup(func() { _ = srv.Close() })
	return front.Addr().String(), &streams
}

func dialGRPC(t testing.TB, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(
		"passthrough:///"+addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestGRPCProxyUnary(t *testing.T) {
	backend := startGRPCEcho(t)
	front, streams := startGRPCProxyFront(t, backend)
	conn := dialGRPC(t, front)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := []byte("hello gRPC")
	var reply []byte
	var trailer metadata.MD
	if err := conn.Invoke(ctx, "/echo.Echo/Unary", req, &reply, grpc.Trailer(&trailer)); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if string(reply) != string(req) {
		t.Errorf("reply = %q, want %q", reply, req)
	}
	// Trailers (grpc-status lives here too) must survive the passthrough.
	if got := trailer.Get("x-echo-trailer"); len(got) != 1 || got[0] != "ok" {
		t.Errorf("trailer x-echo-trailer = %v, want [ok]", got)
	}
	if streams.Load() != 1 {
		t.Errorf("stream counter = %d, want 1", streams.Load())
	}
}

func TestGRPCProxyServerStream(t *testing.T) {
	backend := startGRPCEcho(t)
	front, _ := startGRPCProxyFront(t, backend)
	conn := dialGRPC(t, front)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	desc := &grpc.StreamDesc{StreamName: "Stream", ServerStreams: true, ClientStreams: true}
	cs, err := conn.NewStream(ctx, desc, "/echo.Echo/Stream")
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}

	want := []string{"one", "two", "three"}
	for _, m := range want {
		msg := []byte(m)
		if err := cs.SendMsg(&msg); err != nil {
			t.Fatalf("send %q: %v", m, err)
		}
		// Each frame must come back before the next is sent: a buffering proxy
		// would deadlock here, proving frames flush incrementally.
		var reply []byte
		if err := cs.RecvMsg(&reply); err != nil {
			t.Fatalf("recv: %v", err)
		}
		if string(reply) != m {
			t.Errorf("reply = %q, want %q", reply, m)
		}
	}
	if err := cs.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
	var tail []byte
	if err := cs.RecvMsg(&tail); !errors.Is(err, io.EOF) {
		t.Errorf("final recv = %v, want EOF", err)
	}
}

func TestNewGRPCTransportScheme(t *testing.T) {
	loc := config.LocationConfig{}

	h2cT := newGRPCTransport(loc, false)
	if !h2cT.AllowHTTP {
		t.Error("h2c transport should set AllowHTTP for cleartext HTTP/2")
	}
	if h2cT.DialTLSContext == nil {
		t.Error("h2c transport should dial via DialTLSContext")
	}

	tlsT := newGRPCTransport(loc, true)
	if tlsT.AllowHTTP {
		t.Error("TLS transport must not set AllowHTTP")
	}
	if tlsT.DialTLSContext == nil {
		t.Error("TLS transport should set DialTLSContext for the connect timeout")
	}
}
