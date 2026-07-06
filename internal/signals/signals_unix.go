// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build unix

package signals

import (
	"os"
	"syscall"
)

// shutdownSignals are the signals that trigger graceful shutdown on Unix.
func shutdownSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}

// reloadSignals are the signals that trigger a config reload on Unix.
func reloadSignals() []os.Signal {
	return []os.Signal{syscall.SIGHUP}
}
