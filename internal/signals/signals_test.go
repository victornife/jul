// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package signals

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

const signalTestTimeout = 5 * time.Second

// TestShutdownSignalsNonEmpty holds on every platform: Listen always installs
// at least one shutdown signal, or a shutdown request could never arrive.
func TestShutdownSignalsNonEmpty(t *testing.T) {
	if sigs := shutdownSignals(); len(sigs) == 0 {
		t.Fatal("shutdownSignals() returned none; graceful shutdown would be unreachable")
	}
}

// TestListenShutdownSignalCancelsContext proves the documented contract:
// sending a shutdown signal to the process cancels the returned context.
// Skipped on Windows: os.Process.Signal(os.Interrupt) against the test
// binary's own pid requires GenerateConsoleCtrlEvent to reach a process
// group sharing a console, which a non-interactive CI runner does not
// attach — the OS call itself fails there ("not supported by windows"),
// not the code under test.
func TestListenShutdownSignalCancelsContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("self-signaling os.Interrupt is not reliable on a non-interactive Windows runner")
	}

	ctx, _, stop := Listen(context.Background())
	defer stop()

	if err := selfSignal(shutdownSignals()[0]); err != nil {
		t.Fatalf("signal self process: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(signalTestTimeout):
		t.Fatal("context was not cancelled after a shutdown signal")
	}
}

// TestListenReloadSignalDelivers proves a reload signal (SIGHUP on Unix) is
// delivered on the reload channel without cancelling the context. Reload
// signals are empty on Windows (reload is file-watcher driven there), so
// there is nothing to send.
func TestListenReloadSignalDelivers(t *testing.T) {
	sigs := reloadSignals()
	if len(sigs) == 0 {
		t.Skip("no reload signals on this platform")
	}

	ctx, reload, stop := Listen(context.Background())
	defer stop()

	if err := selfSignal(sigs[0]); err != nil {
		t.Fatalf("signal self process: %v", err)
	}

	select {
	case <-reload:
	case <-ctx.Done():
		t.Fatal("context was cancelled by a reload signal")
	case <-time.After(signalTestTimeout):
		t.Fatal("reload channel did not receive after a reload signal")
	}

	select {
	case <-ctx.Done():
		t.Fatal("context was cancelled by a reload signal")
	default:
	}
}

// TestListenReloadChannelNeverBlocksSender proves the reload channel's
// buffer-of-one plus non-blocking send means a burst of reload signals can
// never wedge the internal dispatch goroutine, even though only one pending
// reload is ever queued.
func TestListenReloadChannelNeverBlocksSender(t *testing.T) {
	sigs := reloadSignals()
	if len(sigs) == 0 {
		t.Skip("no reload signals on this platform")
	}

	ctx, reload, stop := Listen(context.Background())
	defer stop()

	for i := 0; i < 5; i++ {
		if err := selfSignal(sigs[0]); err != nil {
			t.Fatalf("signal self process: %v", err)
		}
	}

	select {
	case <-reload:
	case <-time.After(signalTestTimeout):
		t.Fatal("reload channel never received despite a burst of reload signals")
	}

	// The dispatch goroutine must still be alive and responsive: a shutdown
	// signal sent right after the burst must still cancel ctx.
	if err := selfSignal(shutdownSignals()[0]); err != nil {
		t.Fatalf("signal self process: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(signalTestTimeout):
		t.Fatal("dispatch goroutine stopped responding after a reload-signal burst")
	}
}

// TestListenStopCancelsContext proves stop() releases the handlers and
// cancels the context, without hanging. It deliberately does not re-send the
// shutdown signal after stop() — signal.Stop restores the OS default
// disposition, and the default action for SIGINT/os.Interrupt is process
// termination.
func TestListenStopCancelsContext(t *testing.T) {
	ctx, _, stop := Listen(context.Background())
	stop()

	select {
	case <-ctx.Done():
	case <-time.After(signalTestTimeout):
		t.Fatal("stop() did not cancel the returned context")
	}
}

// TestListenParentCancellationPropagates proves the returned context is a
// true child of the caller's context: cancelling the parent cancels it too,
// and the internal dispatch goroutine exits instead of leaking.
func TestListenParentCancellationPropagates(t *testing.T) {
	before := goroutineCountSettled()

	parent, cancelParent := context.WithCancel(context.Background())
	ctx, _, stop := Listen(parent)
	defer stop()

	cancelParent()

	select {
	case <-ctx.Done():
	case <-time.After(signalTestTimeout):
		t.Fatal("parent cancellation did not propagate to the returned context")
	}

	after := goroutineCountSettled()
	if growth := after - before; growth > 2 {
		t.Errorf("goroutine count grew by %d after parent cancellation; want the dispatch goroutine to exit", growth)
	}
}

// selfSignal sends sig to the current process, the standard way to exercise
// signal.Notify handling without depending on an external process.
func selfSignal(sig os.Signal) error {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

// goroutineCountSettled samples runtime.NumGoroutine() after giving
// recently-stopped goroutines a chance to actually exit.
func goroutineCountSettled() int {
	runtime.Gosched()
	time.Sleep(50 * time.Millisecond)
	return runtime.NumGoroutine()
}
