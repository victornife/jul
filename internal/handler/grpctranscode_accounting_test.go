// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"jul/internal/config"
	"jul/internal/upstream"
)

// transcodeDescriptorSet writes a one-method descriptor set with a
// google.api.http annotation, which is the minimum a transcoding route needs to
// have any routes at all.
func transcodeDescriptorSet(t *testing.T, streaming ...bool) string {
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
				Name:            proto.String("Echo"),
				InputType:       proto.String(".echo.EchoRequest"),
				OutputType:      proto.String(".echo.EchoReply"),
				ServerStreaming: proto.Bool(len(streaming) != 0 && streaming[0]),
				Options:         opts,
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
	return path
}

func TestLongLivedTranscodedGRPCReloadAndShutdown(t *testing.T) {
	for _, phase := range []string{"reload_graceful_completion", "forced_retirement", "shutdown"} {
		t.Run(phase, func(t *testing.T) {
			path := transcodeDescriptorSet(t, true)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var set descriptorpb.FileDescriptorSet
			if err := proto.Unmarshal(raw, &set); err != nil {
				t.Fatal(err)
			}
			descriptor, err := protodesc.NewFile(set.File[0], protoregistry.GlobalFiles)
			if err != nil {
				t.Fatal(err)
			}
			commands := make(chan string, 1)
			backendDone := make(chan struct{})
			backend := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
				defer close(backendDone)
				request := dynamicpb.NewMessage(descriptor.Messages().ByName("EchoRequest"))
				if err := stream.RecvMsg(request); err != nil {
					return err
				}
				send := func(message string) error {
					response := dynamicpb.NewMessage(descriptor.Messages().ByName("EchoReply"))
					response.Set(response.Descriptor().Fields().ByName("message"), protoreflect.ValueOfString(message))
					return stream.SendMsg(response)
				}
				if err := send("before"); err != nil {
					return err
				}
				for {
					select {
					case <-stream.Context().Done():
						return stream.Context().Err()
					case command := <-commands:
						if err := send(command); err != nil {
							return err
						}
						if command == "after reload" {
							return nil
						}
					}
				}
			}))
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			go func() { _ = backend.Serve(listener) }()
			t.Cleanup(backend.Stop)
			proxy, err := NewGRPCTranscode(context.Background(), config.ServerConfig{}, config.LocationConfig{GRPCTranscode: &config.GRPCTranscodeConfig{Target: listener.Addr().String(), DescriptorSet: path, Streaming: true}}, nil, nil, grpcTestLogger(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			closer := proxy.(io.Closer)
			t.Cleanup(func() { _ = closer.Close() })
			grace := 100 * time.Millisecond
			if phase == "reload_graceful_completion" {
				grace = time.Hour
			}
			fixture := newLongLivedServer(t, proxy, grace)
			client := &http.Client{Timeout: 10 * time.Second}
			response, err := client.Post("http://"+fixture.address+"/v1/echo", "application/json", strings.NewReader(`{"message":"begin"}`))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", response.StatusCode)
			}
			decoder := json.NewDecoder(response.Body)
			var first map[string]any
			if err := decoder.Decode(&first); err != nil || first["message"] != "before" {
				t.Fatalf("first=%v err=%v", first, err)
			}
			if phase == "shutdown" {
				fixture.stop()
			} else {
				fixture.reload(t)
				if phase == "forced_retirement" {
					fixture.waitRetired(t)
				} else {
					commands <- "after reload"
					var next map[string]any
					if err := decoder.Decode(&next); err != nil || next["message"] != "after reload" {
						t.Fatalf("reload frame=%v err=%v", next, err)
					}
				}
			}
			select {
			case <-backendDone:
			case <-time.After(5 * time.Second):
				t.Fatal("backend stream did not complete/cancel")
			}
			if phase != "reload_graceful_completion" {
				var next map[string]any
				if err := decoder.Decode(&next); err == nil && next["message"] != nil {
					t.Fatalf("transcoded RPC still emitted data after %s: %v", phase, next)
				}
			} else {
				if _, err := io.Copy(io.Discard, response.Body); err != nil {
					t.Fatal(err)
				}
				fixture.waitRetired(t)
			}
		})
	}
}

// admittedTranscoder builds a transcoding route under the supplied pool policy.
// The backend is deliberately unreachable: this test is about admission
// accounting, and the error path is the one where a leaked slot would hurt most.
func admittedTranscoder(t *testing.T, r *config.ResilienceConfig) (*admittedHandler, *upstream.Admission) {
	t.Helper()
	loc := config.LocationConfig{
		GRPCTranscode: &config.GRPCTranscodeConfig{
			Target:        "tcapi",
			DescriptorSet: transcodeDescriptorSet(t),
		},
	}
	ups := map[string]config.UpstreamConfig{"tcapi": {
		Name:       "tcapi",
		Strategy:   "round_robin",
		Servers:    []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}},
		MaxFails:   3,
		Resilience: r,
	}}
	h, err := NewGRPCTranscode(context.Background(), config.ServerConfig{}, loc, ups, nil, grpcTestLogger(), nil, nil)
	if err != nil {
		t.Fatalf("NewGRPCTranscode: %v", err)
	}
	ah, ok := h.(*admittedHandler)
	if !ok {
		t.Fatalf("NewGRPCTranscode returned %T, want *admittedHandler: transcoding must acquire admission", h)
	}
	t.Cleanup(func() { _ = ah.Close() })
	return ah, ah.admission
}

// TestTranscodeAccountingReleasesOnEveryPath pins the transcoding row of the
// accounting matrix: one slot per call, returned even when the call fails.
func TestTranscodeAccountingReleasesOnEveryPath(t *testing.T) {
	h, adm := admittedTranscoder(t, &config.ResilienceConfig{MaxActiveRequests: 4})

	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{"message":"hi"}`)))
		if rec.Code == http.StatusOK {
			t.Fatalf("call %d unexpectedly succeeded against an unreachable backend", i)
		}
	}
	if got := adm.Active(); got != 0 {
		t.Fatalf("active after 10 failed transcoded calls = %d, want 0", got)
	}
}

// TestTranscodeAccountingEnforcesLimit pins that the pool limit binds on a
// transcoding route, and that a rejection there is the same 503 the HTTP path
// returns rather than a transcoding-specific status.
func TestTranscodeAccountingEnforcesLimit(t *testing.T) {
	h, adm := admittedTranscoder(t, &config.ResilienceConfig{MaxActiveRequests: 1})

	// Hold the only slot directly, which is exactly what an in-flight call does.
	release, err := adm.Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{"message":"hi"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("overload response on a transcoding route carries no Retry-After")
	}

	release()
	if got := adm.Active(); got != 0 {
		t.Fatalf("active at quiesce = %d, want 0", got)
	}
}
