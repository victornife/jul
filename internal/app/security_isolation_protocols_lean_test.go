// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !grpc || !stream || !waf

package app

import "testing"

const fullProtocolCapabilitiesCompiled = false

func verifyFullProtocolIsolationBoundaries(_ *testing.T, _ *HandlerFactory, _ string) bool {
	return false
}
