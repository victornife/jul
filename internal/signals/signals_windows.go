// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build windows

package signals

import (
	"os"
)

// shutdownSignals are the signals that trigger graceful shutdown on Windows.
// Only os.Interrupt (Ctrl-C) is reliably delivered.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// reloadSignals is empty on Windows: there is no SIGHUP. Reload is driven by
// the config file watcher instead.
func reloadSignals() []os.Signal {
	return nil
}
