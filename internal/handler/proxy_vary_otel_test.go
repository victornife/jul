// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build otel

package handler

import (
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/propagation"

	"jul/internal/config"
)

func TestProxyTransformedVaryIncludesTracingPropagation(t *testing.T) {
	names, complete := ProxyTransformedRequestHeaders(config.LocationConfig{}, 256)
	if !complete {
		t.Fatal("proxy Vary policy was unexpectedly incomplete")
	}
	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	for _, field := range propagator.Fields() {
		if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, field) }) {
			t.Errorf("tracing propagation field %q is absent from the proxy Vary policy", field)
		}
	}
}
