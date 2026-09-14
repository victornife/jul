// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build otel

package observability

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestDynamicRootSamplerDefensiveStates(t *testing.T) {
	t.Parallel()
	p := sdktrace.SamplingParameters{
		ParentContext: context.Background(),
		TraceID:       trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		Name:          "root",
	}

	var nilSampler *dynamicRootSampler
	nilSampler.Update(1) // explicit no-op contract
	if got := nilSampler.ShouldSample(p).Decision; got != sdktrace.Drop {
		t.Fatalf("nil sampler decision=%v, want Drop", got)
	}

	empty := &dynamicRootSampler{}
	if got := empty.ShouldSample(p).Decision; got != sdktrace.Drop {
		t.Fatalf("empty sampler decision=%v, want Drop", got)
	}
	if got := empty.Description(); got != "JulDynamicRootSampler" {
		t.Fatalf("Description()=%q", got)
	}
}

func TestProcessSampleRatioPublishSeamWithoutTracingIsNoop(t *testing.T) {
	old := activeRootSampler.Load()
	defer activeRootSampler.Store(old)
	activeRootSampler.Store(nil)
	UpdateTracingSampleRatio(0.42)
}
