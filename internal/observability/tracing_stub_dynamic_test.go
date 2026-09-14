// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !otel

package observability

import "testing"

func TestLeanDynamicSampleRatioSeamsAreNoop(t *testing.T) {
	tr := &Tracer{}
	tr.UpdateSampleRatio(0.25)
	UpdateTracingSampleRatio(0.75)
}
