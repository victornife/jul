// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	refv1 "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestDescriptorSetRejectsOversizedFileBeforeDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.pb")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxDescriptorSetBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRoutesFromFile(path); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("oversized descriptor result = %v", err)
	}
}

func TestReflectionDescriptorBudgetIsCumulative(t *testing.T) {
	raw, err := proto.Marshal(&descriptorpb.FileDescriptorProto{Name: proto.String("test.proto")})
	if err != nil {
		t.Fatal(err)
	}
	resp := &refv1.FileDescriptorResponse{FileDescriptorProto: [][]byte{raw}}
	f := &reflectionFiles{collected: make(map[string]*descriptorpb.FileDescriptorProto), seen: make(map[string]bool), bytes: maxDescriptorSetBytes - len(raw)}
	if err := f.add(resp); err != nil {
		t.Fatalf("descriptor at limit rejected: %v", err)
	}
	if err := f.add(resp); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("second reflection reply bypassed aggregate limit: %v", err)
	}
}
