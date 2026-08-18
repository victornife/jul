// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"jul/internal/config"
	"jul/internal/upstream"
)

// transcodeDescriptorSet writes a one-method descriptor set with a
// google.api.http annotation, which is the minimum a transcoding route needs to
// have any routes at all.
func transcodeDescriptorSet(t *testing.T) string {
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
	return path
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
