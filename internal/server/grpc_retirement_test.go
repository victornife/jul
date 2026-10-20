// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"jul/internal/config"
	"jul/internal/handler"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/router"
	"jul/internal/upstream"
)

type rawBytesCodec struct{}

func (rawBytesCodec) Marshal(v any) ([]byte, error) {
	switch b := v.(type) {
	case []byte:
		return b, nil
	case *[]byte:
		return *b, nil
	default:
		return nil, fmt.Errorf("rawBytesCodec: unexpected %T", v)
	}
}

func (rawBytesCodec) Unmarshal(data []byte, v any) error {
	p, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("rawBytesCodec: unexpected %T", v)
	}
	*p = append((*p)[:0], data...)
	return nil
}

func (rawBytesCodec) Name() string { return "raw" }

func TestRealNativeGRPCStreamRetirementAndSiblingContinuity(t *testing.T) {
	// 1. Start a real h2c gRPC backend server that echoes bidirectional frames and
	//    signals when the "/dash.Svc/Bidi" stream's context is cancelled on the backend.
	dashBackendCancelled := make(chan struct{})
	var dashCancelOnce sync.Once
	backendLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen gRPC backend: %v", err)
	}
	grpcBackend := grpc.NewServer(
		grpc.ForceServerCodec(rawBytesCodec{}),
		grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
			method, _ := grpc.MethodFromServerStream(stream)
			_ = stream.SetHeader(metadata.Pairs("x-grpc-backend", "real"))
			stream.SetTrailer(metadata.Pairs("x-echo-trailer", "ok"))
			defer func() {
				if strings.HasPrefix(method, "/dash.") {
					dashCancelOnce.Do(func() { close(dashBackendCancelled) })
				}
			}()
			for {
				var msg []byte
				if rErr := stream.RecvMsg(&msg); rErr != nil {
					if errors.Is(rErr, io.EOF) {
						return nil
					}
					return rErr
				}
				if sErr := stream.SendMsg(&msg); sErr != nil {
					return sErr
				}
			}
		}),
	)
	go func() { _ = grpcBackend.Serve(backendLis) }()
	t.Cleanup(grpcBackend.Stop)
	backendAddr := backendLis.Addr().String()

	// 2. Start a real Jul Server wired to production handler.NewGRPCProxy (router.ActionGRPC).
	dir := t.TempDir()
	srvCert, srvKey := writeSelfSigned(t, dir, "srv", "portal.example.com", "dash.example.com")
	addr := freePort(t)
	strPtr := func(s string) *string { return &s }

	baseCfg := func() *config.Config {
		return &config.Config{
			Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
			Servers: []config.ServerConfig{{
				Name:        "grpc-front",
				Listen:      addr,
				ServerNames: []string{"portal.example.com", "dash.example.com"},
				TLS:         &config.TLSConfig{Enabled: true, Cert: srvCert, Key: srvKey},
				Locations: []config.LocationConfig{
					{
						RouteID:   strPtr("portal-grpc"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/portal.Svc/"},
						ProxyPass: "http://" + backendAddr,
						GRPC:      true,
					},
					{
						RouteID:   strPtr("dash-grpc"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/dash.Svc/"},
						ProxyPass: "http://" + backendAddr,
						GRPC:      true,
					},
				},
			}},
		}
	}

	var genCounter atomic.Uint64
	var closersMu sync.Mutex
	var closers []io.Closer
	t.Cleanup(func() {
		closersMu.Lock()
		defer closersMu.Unlock()
		for _, c := range closers {
			_ = c.Close()
		}
	})

	factory := func(ctx context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		genID := genCounter.Add(1)
		builders := map[string]router.Builder{
			router.ActionProxy: func(s config.ServerConfig, l config.LocationConfig) (http.Handler, error) {
				h, bErr := handler.NewGRPCProxy(ctx, s, l, nil, nil, quietLogger(), nil)
				if bErr != nil {
					return nil, bErr
				}
				if cl, ok := h.(io.Closer); ok {
					closersMu.Lock()
					closers = append(closers, cl)
					closersMu.Unlock()
				}
				return h, nil
			},
		}
		rtr, rErr := router.New(c, builders, nil, nil, quietLogger())
		if rErr != nil {
			return nil, 0, nil, nil, rErr
		}
		out := make(map[string]http.Handler)
		for _, a := range uniqueListenAddrs(c.Servers) {
			out[a] = rtr.For(a)
		}
		return out, genID, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}

	initCfg := baseCfg()
	src := &stubSource{}
	src.set(initCfg, nil)
	srv := New(initCfg, initCfg, lifecycle.ComputeFingerprint(initCfg), quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })
	srv.exchanges.Grace = 120 * time.Millisecond
	srv.exchanges.Bound = 400 * time.Millisecond

	ready := make(chan struct{})
	srv.OnInitialGenerationReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloadCh := make(chan ReloadRequest, 1)
	go func() { _ = srv.Run(ctx, reloadCh, redact.EmptyState()) }()
	<-ready

	// 3. Connect ONE real google.golang.org/grpc ClientConn over TLS/HTTP2 (dials == 1)
	//    and open both /portal.Svc/Bidi and /dash.Svc/Bidi streams on that connection.
	var dials atomic.Int32
	tlsCreds := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "portal.example.com",
		NextProtos:         []string{"h2"},
	})
	conn, err := grpc.NewClient(
		"passthrough:///"+addr,
		grpc.WithTransportCredentials(tlsCreds),
		grpc.WithAuthority("portal.example.com"),
		grpc.WithContextDialer(func(ctx context.Context, target string) (net.Conn, error) {
			dials.Add(1)
			var d net.Dialer
			return d.DialContext(ctx, "tcp", target)
		}),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(rawBytesCodec{})),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	streamDesc := &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}
	portalStream, err := conn.NewStream(ctx, streamDesc, "/portal.Svc/Bidi")
	if err != nil {
		t.Fatalf("open portalStream: %v", err)
	}
	dashStream, err := conn.NewStream(ctx, streamDesc, "/dash.Svc/Bidi")
	if err != nil {
		t.Fatalf("open dashStream: %v", err)
	}

	// Exchange initial frames on both real gRPC streams and verify response headers are application/grpc.
	for name, st := range map[string]grpc.ClientStream{"portal": portalStream, "dash": dashStream} {
		payload := []byte("hello-" + name)
		if sErr := st.SendMsg(payload); sErr != nil {
			t.Fatalf("%s SendMsg: %v", name, sErr)
		}
		var echoed []byte
		if rErr := st.RecvMsg(&echoed); rErr != nil || string(echoed) != string(payload) {
			t.Fatalf("%s RecvMsg = %q, err = %v", name, string(echoed), rErr)
		}
		md, hErr := st.Header()
		if hErr != nil || len(md.Get("x-grpc-backend")) == 0 {
			t.Fatalf("%s missing gRPC backend header: md=%v err=%v", name, md, hErr)
		}
	}
	if dials.Load() != 1 {
		t.Fatalf("expected 1 shared HTTP/2 connection for both native gRPC streams, got %d dials", dials.Load())
	}

	// 4. Withdraw dash-grpc via ReloadSourceAdmin -> dashStream resets and backend RPC cancels
	//    within bound, while portalStream continues exchanging messages and completes cleanly!
	withdrawnCfg := baseCfg()
	withdrawnCfg.Servers[0].Locations[1].Withdrawn = true
	candWithdrawn, _ := config.NewCandidate(withdrawnCfg)
	resCh := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "withdraw-dash-grpc", Source: ReloadSourceAdmin, Candidate: candWithdrawn, Result: resCh}
	rel := <-resCh
	if rel.Outcome != ReloadAppliedLive || len(rel.EntryRetirements) != 1 {
		t.Fatalf("withdraw reload = %+v", rel)
	}
	ret := rel.EntryRetirements[0]
	if ret.EndedExchanges != 1 || ret.CollateralCount != 0 || !ret.CompletedWithinBound || ret.Claim != "complete_withdrawal" {
		t.Fatalf("unexpected native gRPC retirement result: %+v", ret)
	}

	var dashMsg []byte
	if rErr := dashStream.RecvMsg(&dashMsg); rErr == nil {
		t.Fatal("expected withdrawn native gRPC stream RecvMsg to fail")
	}
	select {
	case <-dashBackendCancelled:
	case <-time.After(time.Second):
		t.Fatal("expected backend gRPC stream context to be cancelled on route withdrawal")
	}

	// Sibling portalStream on the same H2 connection continues exchanging frames and closes cleanly with trailers.
	afterPayload := []byte("still-alive-after-sibling-withdrawal")
	if sErr := portalStream.SendMsg(afterPayload); sErr != nil {
		t.Fatalf("portalStream SendMsg after sibling withdrawal: %v", sErr)
	}
	var afterEcho []byte
	if rErr := portalStream.RecvMsg(&afterEcho); rErr != nil || string(afterEcho) != string(afterPayload) {
		t.Fatalf("portalStream RecvMsg after sibling withdrawal = %q, err = %v", string(afterEcho), rErr)
	}
	if cErr := portalStream.CloseSend(); cErr != nil {
		t.Fatalf("portalStream CloseSend: %v", cErr)
	}
	var eofBuf []byte
	if rErr := portalStream.RecvMsg(&eofBuf); !errors.Is(rErr, io.EOF) {
		t.Fatalf("expected clean EOF on portalStream, got %v", rErr)
	}
	if tr := portalStream.Trailer(); len(tr.Get("x-echo-trailer")) == 0 {
		t.Fatalf("expected x-echo-trailer on completed portalStream, got %v", tr)
	}
	if dials.Load() != 1 {
		t.Fatalf("expected 1 shared HTTP/2 connection throughout test, got %d dials", dials.Load())
	}
	deadline := time.Now().Add(time.Second)
	for srv.exchanges.ActiveCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.exchanges.ActiveCount(); got != 0 {
		t.Fatalf("expected ActiveCount=0 after streams completed, got %d", got)
	}
}

func TestRealTranscodedGRPCStreamRetirementAndBackendCancellation(t *testing.T) {
	dir := t.TempDir()
	// 1. Build a protobuf descriptor set with a server-streaming method POST /v1/echo.
	strField := func(name string, num int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String(name),
		}
	}
	opts := &descriptorpb.MethodOptions{}
	proto.SetExtension(opts, annotations.E_Http, &annotations.HttpRule{
		Pattern: &annotations.HttpRule_Post{Post: "/v1/echo"},
		Body:    "*",
	})
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name:       proto.String("echo/echo.proto"),
		Package:    proto.String("echo"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/api/annotations.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("EchoRequest"), Field: []*descriptorpb.FieldDescriptorProto{strField("message", 1)}},
			{Name: proto.String("EchoReply"), Field: []*descriptorpb.FieldDescriptorProto{strField("message", 1)}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("EchoService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:            proto.String("Echo"),
				InputType:       proto.String(".echo.EchoRequest"),
				OutputType:      proto.String(".echo.EchoReply"),
				ServerStreaming: proto.Bool(true),
				Options:         opts,
			}},
		}},
	}}}
	rawDesc, err := proto.Marshal(set)
	if err != nil {
		t.Fatalf("marshal descriptor: %v", err)
	}
	descPath := filepath.Join(dir, "echo.pb")
	if err := os.WriteFile(descPath, rawDesc, 0o600); err != nil {
		t.Fatal(err)
	}
	fileDesc, err := protodesc.NewFile(set.File[0], protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}

	backendCancelled := make(chan struct{})
	var backendCancelOnce sync.Once
	backendLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcBackend := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		defer backendCancelOnce.Do(func() { close(backendCancelled) })
		reqMsg := dynamicpb.NewMessage(fileDesc.Messages().ByName("EchoRequest"))
		if rErr := stream.RecvMsg(reqMsg); rErr != nil {
			return rErr
		}
		reply := dynamicpb.NewMessage(fileDesc.Messages().ByName("EchoReply"))
		reply.Set(reply.Descriptor().Fields().ByName("message"), protoreflect.ValueOfString("initial-transcoded-frame"))
		if sErr := stream.SendMsg(reply); sErr != nil {
			return sErr
		}
		<-stream.Context().Done()
		return stream.Context().Err()
	}))
	go func() { _ = grpcBackend.Serve(backendLis) }()
	t.Cleanup(grpcBackend.Stop)
	backendAddr := backendLis.Addr().String()

	// 2. Start a real Jul Server wired to production handler.NewGRPCTranscode (router.ActionGRPCTranscode)
	//    plus a sibling route on the same shared H2 TLS listener.
	srvCert, srvKey := writeSelfSigned(t, dir, "srv", "portal.example.com", "transcode.example.com")
	addr := freePort(t)
	strPtr := func(s string) *string { return &s }
	portalRelease := make(chan struct{})

	baseCfg := func() *config.Config {
		return &config.Config{
			Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
			Servers: []config.ServerConfig{
				{
					Name:        "portal",
					Listen:      addr,
					ServerNames: []string{"portal.example.com"},
					TLS:         &config.TLSConfig{Enabled: true, Cert: srvCert, Key: srvKey},
					Locations: []config.LocationConfig{{
						RouteID:   strPtr("portal"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/"},
						ProxyPass: "http://127.0.0.1:9001",
					}},
				},
				{
					Name:        "transcode",
					Listen:      addr,
					ServerNames: []string{"transcode.example.com"},
					TLS:         &config.TLSConfig{Enabled: true, Cert: srvCert, Key: srvKey},
					Locations: []config.LocationConfig{{
						RouteID: strPtr("transcode-route"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/v1/"},
						GRPCTranscode: &config.GRPCTranscodeConfig{
							Target:        backendAddr,
							DescriptorSet: descPath,
							Streaming:     true,
						},
					}},
				},
			},
		}
	}

	var genCounter atomic.Uint64
	var closersMu sync.Mutex
	var closers []io.Closer
	t.Cleanup(func() {
		closersMu.Lock()
		defer closersMu.Unlock()
		for _, c := range closers {
			_ = c.Close()
		}
	})

	baseStreamFactory := buildTestRouterFactory(t, make(chan struct{}), portalRelease)
	factory := func(ctx context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		genID := genCounter.Add(1)
		builders := map[string]router.Builder{
			router.ActionProxy: func(s config.ServerConfig, l config.LocationConfig) (http.Handler, error) {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, "data: start-portal\n\n")
					_ = http.NewResponseController(w).Flush()
					select {
					case <-portalRelease:
						_, _ = io.WriteString(w, "data: end-portal\n\n")
						_ = http.NewResponseController(w).Flush()
					case <-r.Context().Done():
					}
				}), nil
			},
			router.ActionGRPCTranscode: func(s config.ServerConfig, l config.LocationConfig) (http.Handler, error) {
				h, bErr := handler.NewGRPCTranscode(ctx, s, l, nil, nil, quietLogger(), nil, nil)
				if bErr != nil {
					return nil, bErr
				}
				if cl, ok := h.(io.Closer); ok {
					closersMu.Lock()
					closers = append(closers, cl)
					closersMu.Unlock()
				}
				return h, nil
			},
		}
		_ = baseStreamFactory
		rtr, rErr := router.New(c, builders, nil, nil, quietLogger())
		if rErr != nil {
			return nil, 0, nil, nil, rErr
		}
		out := make(map[string]http.Handler)
		for _, a := range uniqueListenAddrs(c.Servers) {
			out[a] = rtr.For(a)
		}
		return out, genID, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}

	initCfg := baseCfg()
	src := &stubSource{}
	src.set(initCfg, nil)
	srv := New(initCfg, initCfg, lifecycle.ComputeFingerprint(initCfg), quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })
	srv.exchanges.Grace = 120 * time.Millisecond
	srv.exchanges.Bound = 400 * time.Millisecond

	ready := make(chan struct{})
	srv.OnInitialGenerationReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloadCh := make(chan ReloadRequest, 1)
	go func() { _ = srv.Run(ctx, reloadCh, redact.EmptyState()) }()
	<-ready

	var dials atomic.Int32
	h2Tr := &http.Transport{
		ForceAttemptHTTP2: true,
		DialTLSContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dials.Add(1)
			d := &tls.Dialer{
				Config: &tls.Config{
					InsecureSkipVerify: true,
					ServerName:         "portal.example.com",
					NextProtos:         []string{"h2"},
				},
			}
			return d.DialContext(ctx, network, addr)
		},
	}
	defer h2Tr.CloseIdleConnections()
	h2Client := &http.Client{Transport: h2Tr, Timeout: 5 * time.Second}

	// Open sibling portal stream and transcoded gRPC server-streaming request on the SAME H2 connection.
	portalReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/", nil)
	portalReq.Host = "portal.example.com"
	portalResp, err := h2Client.Do(portalReq)
	if err != nil {
		t.Fatalf("portal GET: %v", err)
	}
	defer portalResp.Body.Close()
	portalBR := bufio.NewReader(portalResp.Body)
	_, _ = portalBR.ReadString('\n')

	tcReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+"/v1/echo", strings.NewReader(`{"message":"hi"}`))
	tcReq.Host = "transcode.example.com"
	tcReq.Header.Set("Content-Type", "application/json")
	tcResp, err := h2Client.Do(tcReq)
	if err != nil {
		t.Fatalf("transcode POST: %v", err)
	}
	defer tcResp.Body.Close()
	if gotCT := tcResp.Header.Get("Content-Type"); !strings.Contains(gotCT, "application/x-ndjson") && !strings.Contains(gotCT, "application/json") {
		t.Fatalf("unexpected transcode Content-Type: %q", gotCT)
	}
	tcBR := bufio.NewReader(tcResp.Body)
	firstLine, err := tcBR.ReadString('\n')
	if err != nil || !strings.Contains(firstLine, "initial-transcoded-frame") {
		t.Fatalf("transcode initial frame = %q, err = %v", firstLine, err)
	}
	if dials.Load() != 1 {
		t.Fatalf("expected 1 shared H2 connection, got %d dials", dials.Load())
	}

	// Narrow transcode-route -> downstream stream resets and backend gRPC RPC cancels within bound.
	narrowCfg := baseCfg()
	narrowCfg.Servers[1].Locations[0].Auth = &config.AuthConfig{Allow: []string{"10.254.0.0/16"}}
	candNarrow, _ := config.NewCandidate(narrowCfg)
	resCh := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "narrow-transcode", Source: ReloadSourceAdmin, Candidate: candNarrow, Result: resCh}
	rel := <-resCh
	if rel.Outcome != ReloadAppliedLive || len(rel.EntryRetirements) != 1 {
		t.Fatalf("narrow transcode reload = %+v", rel)
	}
	ret := rel.EntryRetirements[0]
	if ret.EndedExchanges != 1 || ret.CollateralCount != 0 || !ret.CompletedWithinBound || ret.Claim != "complete_withdrawal" {
		t.Fatalf("unexpected transcode retirement result: %+v", ret)
	}
	select {
	case <-backendCancelled:
	case <-time.After(time.Second):
		t.Fatal("expected transcoded backend gRPC stream context to be cancelled on route narrowing")
	}

	close(portalRelease)
	rest, pErr := io.ReadAll(portalBR)
	if pErr != nil || !strings.Contains(string(rest), "end-portal") {
		t.Fatalf("sibling portal stream failed: err=%v body=%q", pErr, string(rest))
	}
	deadline := time.Now().Add(time.Second)
	for srv.exchanges.ActiveCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.exchanges.ActiveCount(); got != 0 {
		t.Fatalf("expected ActiveCount=0 after streams completed, got %d", got)
	}
}
