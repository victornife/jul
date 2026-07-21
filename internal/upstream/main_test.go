// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("net.(*Resolver).lookupIP"),
		goleak.IgnoreTopFunction("syscall.syscalln"),
		goleak.IgnoreTopFunction("net.cgoLookupHostIP"),
		goleak.IgnoreTopFunction("net.cgoLookupIP"),
		goleak.IgnoreTopFunction("net._C_getaddrinfo"),
		goleak.IgnoreTopFunction("net._C2func_getaddrinfo"),
	)
}
