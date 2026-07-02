//go:build windows

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"golang.org/x/sys/windows/svc"

	"jul/internal/config"
)

const serviceName = "jul"

// runService runs jul under the Windows Service Control Manager when the
// process is launched as a service. When running interactively (e.g. from a
// console) it returns handled=false so the caller proceeds with normal
// foreground startup and flag parsing.
func runService() (handled bool, exitCode int) {
	isSvc, err := svc.IsWindowsService()
	if err != nil || !isSvc {
		return false, 0
	}

	// Service mode has no interactive flag parsing; recover the config path
	// from the image path arguments, defaulting to server.toml.
	fs := flag.NewFlagSet(serviceName, flag.ContinueOnError)
	configPath := fs.String("config", "server.toml", "path to the TOML configuration file")
	_ = fs.Parse(os.Args[1:])

	h := &edgeService{configPath: *configPath}
	if err := svc.Run(serviceName, h); err != nil {
		fmt.Fprintf(os.Stderr, "service %q failed: %v\n", serviceName, err)
		return true, 1
	}
	return true, h.exitCode
}

// edgeService adapts the server lifecycle to the Windows service control
// protocol.
type edgeService struct {
	configPath string
	exitCode   int
	// serveFn, when non-nil, replaces the call to serve() inside Execute.
	// It is used by unit tests to avoid binding real listeners.
	serveFn func(context.Context, <-chan struct{}, config.Source, *config.Config) int
}

// Execute is invoked by the SCM. It loads configuration, starts serving, and
// translates Stop/Shutdown control requests into context cancellation.
func (s *edgeService) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (svcSpecificEC bool, exitCode uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	src := config.NewTOMLSource(s.configPath)
	cfg, err := src.Load()
	if err == nil {
		err = config.Validate(cfg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "service config error: %v\n", err)
		s.exitCode = 1
		return false, 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		if s.serveFn != nil {
			done <- s.serveFn(ctx, nil, src, cfg)
			return
		}
		done <- serve(ctx, nil, src, cfg)
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				s.exitCode = <-done
				return false, uint32(s.exitCode)
			default:
			}
		case code := <-done:
			// serve exited on its own (fatal error during startup or runtime).
			cancel()
			s.exitCode = code
			return false, uint32(code)
		}
	}
}
