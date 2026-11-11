// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc && stream && waf

package app

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"jul/internal/config"
	"jul/internal/stream"
)

const fullProtocolCapabilitiesCompiled = true

type basicAuthRPCCreds struct {
	authHeader string
}

func (b basicAuthRPCCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": b.authHeader}, nil
}

func (b basicAuthRPCCreds) RequireTransportSecurity() bool { return false }

func writeIsolationEchoDescriptorSet(t *testing.T) (string, protoreflect.FileDescriptor) {
	t.Helper()
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
				Name:       proto.String("Echo"),
				InputType:  proto.String(".echo.EchoRequest"),
				OutputType: proto.String(".echo.EchoReply"),
				Options:    opts,
			}},
		}},
	}}}
	raw, err := proto.Marshal(set)
	if err != nil {
		t.Fatalf("marshal descriptor set: %v", err)
	}
	path := filepath.Join(t.TempDir(), "echo.pb")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write descriptor set: %v", err)
	}
	fd, err := protodesc.NewFile(set.File[0], protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return path, fd
}

func verifyFullProtocolIsolationBoundaries(t *testing.T, factory *HandlerFactory, htpasswdPath string) bool {
	t.Helper()
	ctx := context.Background()
	descPath, fd := writeIsolationEchoDescriptorSet(t)

	var grpcBackendHits atomic.Int64
	grpcBackend := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, s grpc.ServerStream) error {
		grpcBackendHits.Add(1)
		reqMsg := dynamicpb.NewMessage(fd.Messages().ByName("EchoRequest"))
		if err := s.RecvMsg(reqMsg); err != nil {
			return err
		}
		val := reqMsg.Get(reqMsg.Descriptor().Fields().ByName("message")).String()
		respMsg := dynamicpb.NewMessage(fd.Messages().ByName("EchoReply"))
		respMsg.Set(respMsg.Descriptor().Fields().ByName("message"), protoreflect.ValueOfString("grpc-echo:"+val))
		return s.SendMsg(respMsg)
	}))
	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("grpc listen: %v", err)
	}
	go func() { _ = grpcBackend.Serve(grpcLn) }()
	t.Cleanup(grpcBackend.Stop)

	protoCfg := &config.Config{
		Upstreams: []config.UpstreamConfig{
			{
				Name:     "grpc_pool",
				Strategy: "round_robin",
				Servers:  []config.UpstreamServer{{Address: grpcLn.Addr().String(), Weight: 1}},
			},
		},
		Servers: []config.ServerConfig{{
			Name:        "proto-srv",
			Listen:      ":8080",
			ServerNames: []string{"app.example.test"},
			Locations: []config.LocationConfig{
				{
					// Real Jul native gRPC passthrough route (GRPC: true) with Basic Auth and WAF disabled.
					RouteID:   config.String("native-grpc-route"),
					Match:     config.MatchConfig{Type: "prefix", Path: "/echo.EchoService/"},
					GRPC:      true,
					ProxyPass: "http://grpc_pool",
					Auth: &config.AuthConfig{
						Basic: &config.BasicAuthConfig{File: htpasswdPath, Realm: "gRPC"},
					},
					WAF: &config.WAFConfig{Enabled: false},
				},
				{
					// Real Jul gRPC-JSON transcoding route (ActionGRPCTranscode) protected by WAF.
					RouteID: config.String("transcode-grpc-route"),
					Match:   config.MatchConfig{Type: "prefix", Path: "/v1/echo"},
					GRPCTranscode: &config.GRPCTranscodeConfig{
						Target:        grpcLn.Addr().String(),
						DescriptorSet: descPath,
					},
					WAF: &config.WAFConfig{
						Enabled:     true,
						Mode:        "block",
						BlockStatus: 403,
						CRSEnabled:  true,
					},
				},
			},
		}},
	}

	hMap, _, commit, _, err := factory.Prepare(ctx, provisionedInspection(protoCfg))
	if err != nil {
		t.Fatalf("Prepare protoCfg: %v", err)
	}
	_, retire := commit()
	defer retire()

	h2Srv := httptest.NewUnstartedServer(hMap[":8080"])
	h2Srv.EnableHTTP2 = true
	h2Srv.StartTLS()
	defer h2Srv.Close()

	// (a) Unauthenticated request to Jul's native gRPC route is denied with 401 before reaching grpcBackend.
	beforeGRPC := grpcBackendHits.Load()
	unauthGRPCReq, _ := http.NewRequest(http.MethodPost, h2Srv.URL+"/echo.EchoService/Echo", strings.NewReader(""))
	unauthGRPCReq.Host = "app.example.test"
	unauthGRPCReq.Header.Set("Content-Type", "application/grpc")
	unauthGRPCResp, err := h2Srv.Client().Do(unauthGRPCReq)
	if err != nil {
		t.Fatalf("unauth native gRPC: %v", err)
	}
	_ = unauthGRPCResp.Body.Close()
	if unauthGRPCResp.StatusCode != http.StatusUnauthorized || grpcBackendHits.Load() != beforeGRPC {
		t.Fatalf("unauth native gRPC status=%d hits=%d->%d", unauthGRPCResp.StatusCode, beforeGRPC, grpcBackendHits.Load())
	}

	// (b) Malicious JSON request to Jul's gRPC-JSON transcoding route (/v1/echo) is blocked by WAF (403) before reaching grpcBackend!
	attackTranscodeReq, _ := http.NewRequest(http.MethodPost, h2Srv.URL+"/v1/echo", strings.NewReader(`{"message":"1' OR '1'='1"}`))
	attackTranscodeReq.Host = "app.example.test"
	attackTranscodeReq.Header.Set("Content-Type", "application/json")
	attackTranscodeResp, err := h2Srv.Client().Do(attackTranscodeReq)
	if err != nil {
		t.Fatalf("attack transcoded gRPC: %v", err)
	}
	_ = attackTranscodeResp.Body.Close()
	if attackTranscodeResp.StatusCode != http.StatusForbidden || grpcBackendHits.Load() != beforeGRPC {
		t.Fatalf("attack transcoded gRPC status=%d hits=%d->%d, want 403 and 0 backend hits", attackTranscodeResp.StatusCode, beforeGRPC, grpcBackendHits.Load())
	}

	// (c) Clean JSON request to Jul's gRPC-JSON transcoding route (/v1/echo) passes WAF, transcodes to protobuf, and invokes grpcBackend!
	cleanTranscodeReq, _ := http.NewRequest(http.MethodPost, h2Srv.URL+"/v1/echo", strings.NewReader(`{"message":"hello"}`))
	cleanTranscodeReq.Host = "app.example.test"
	cleanTranscodeReq.Header.Set("Content-Type", "application/json")
	cleanTranscodeResp, err := h2Srv.Client().Do(cleanTranscodeReq)
	if err != nil {
		t.Fatalf("clean transcoded gRPC: %v", err)
	}
	transcodeBody, _ := io.ReadAll(cleanTranscodeResp.Body)
	_ = cleanTranscodeResp.Body.Close()
	if cleanTranscodeResp.StatusCode != http.StatusOK || !strings.Contains(string(transcodeBody), "grpc-echo:hello") || grpcBackendHits.Load() != beforeGRPC+1 {
		t.Fatalf("clean transcoded gRPC status=%d body=%q hits=%d", cleanTranscodeResp.StatusCode, string(transcodeBody), grpcBackendHits.Load())
	}

	// (d) Authenticated native gRPC client call THROUGH Jul's HTTP/2 TLS listener (h2Srv)
	// verifies Jul's native gRPC proxy (GRPC: true) forwards protobuf RPCs and trailers end-to-end!
	beforeNative := grpcBackendHits.Load()
	tlsCreds := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "app.example.test",
		NextProtos:         []string{"h2"},
	})
	basicAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("op:secret-pass-123"))
	grpcConn, err := grpc.NewClient(
		h2Srv.Listener.Addr().String(),
		grpc.WithTransportCredentials(tlsCreds),
		grpc.WithAuthority("app.example.test"),
		grpc.WithPerRPCCredentials(basicAuthRPCCreds{authHeader: basicAuth}),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient via Jul: %v", err)
	}
	defer grpcConn.Close()
	inMsg := dynamicpb.NewMessage(fd.Messages().ByName("EchoRequest"))
	inMsg.Set(inMsg.Descriptor().Fields().ByName("message"), protoreflect.ValueOfString("native"))
	outMsg := dynamicpb.NewMessage(fd.Messages().ByName("EchoReply"))
	if err := grpcConn.Invoke(ctx, "/echo.EchoService/Echo", inMsg, outMsg); err != nil {
		t.Fatalf("grpc Invoke through Jul native gRPC proxy: %v", err)
	}
	gotNative := outMsg.Get(outMsg.Descriptor().Fields().ByName("message")).String()
	if gotNative != "grpc-echo:native" || grpcBackendHits.Load() != beforeNative+1 {
		t.Fatalf("native gRPC via Jul reply=%q hits=%d->%d", gotNative, beforeNative, grpcBackendHits.Load())
	}

	// (e) Real Jul L4 TCP stream proxy (stream.NewServer): relays opaque TCP frames to backend without HTTP WAF parsing.
	tcpBackendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcpBackendLn: %v", err)
	}
	defer tcpBackendLn.Close()
	go func() {
		c, err := tcpBackendLn.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 128)
		n, _ := c.Read(buf)
		_, _ = c.Write(append([]byte("jul-stream:"), buf[:n]...))
	}()

	probeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probeLn: %v", err)
	}
	streamListenAddr := probeLn.Addr().String()
	_ = probeLn.Close()

	streamSrv := stream.NewServer(stream.Options{Logger: factory.Log})
	defer func() { _ = streamSrv.Close() }()
	if err := streamSrv.Reload([]config.StreamServer{{
		Listen:    streamListenAddr,
		Protocol:  "tcp",
		ProxyPass: tcpBackendLn.Addr().String(),
	}}, nil); err != nil {
		t.Fatalf("streamSrv.Reload: %v", err)
	}

	l4Conn, err := net.DialTimeout("tcp", streamListenAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial Jul streamSrv %s: %v", streamListenAddr, err)
	}
	defer l4Conn.Close()
	_, _ = l4Conn.Write([]byte("1' OR '1'='1"))
	l4Buf := make([]byte, 128)
	l4N, err := l4Conn.Read(l4Buf)
	if err != nil || string(l4Buf[:l4N]) != "jul-stream:1' OR '1'='1" {
		t.Fatalf("Jul stream proxy reply = %q, err=%v", string(l4Buf[:l4N]), err)
	}
	return true
}
