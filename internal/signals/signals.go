// Package signals wires OS signals to shutdown and reload events in a
// cross-platform way. Shutdown signals (SIGINT/SIGTERM) work everywhere;
// reload via SIGHUP is Unix-only. On Windows, reload is driven solely by the
// config file watcher (see the config package).
package signals

import (
	"context"
	"os"
	"os/signal"
)

// Listen starts watching for OS signals. The returned context is cancelled
// when a shutdown signal arrives. The returned channel receives an empty
// struct on each reload signal. Call stop to release the signal handlers.
func Listen(parent context.Context) (ctx context.Context, reload <-chan struct{}, stop func()) {
	ctx, cancel := context.WithCancel(parent)
	reloadCh := make(chan struct{}, 1)

	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, shutdownSignals()...)

	reloadSig := make(chan os.Signal, 1)
	if sigs := reloadSignals(); len(sigs) > 0 {
		signal.Notify(reloadSig, sigs...)
	}

	go func() {
		for {
			select {
			case <-parent.Done():
				return
			case <-shutdownCh:
				cancel()
				return
			case <-reloadSig:
				select {
				case reloadCh <- struct{}{}:
				default:
				}
			}
		}
	}()

	stop = func() {
		signal.Stop(shutdownCh)
		signal.Stop(reloadSig)
		cancel()
	}
	return ctx, reloadCh, stop
}
