//go:build stream

package stream

import (
	"context"
	"net"
	"syscall"
)

// ListenTCPWithReuse opens a TCP listener.
//
// On Unix this sets SO_REUSEADDR so that rapid restarts (development or
// hot-reload) can reuse the socket while the previous process is still in
// TIME_WAIT.
//
// On Windows the semantics of SO_REUSEADDR are different and unsafe for public
// TCP servers (it allows port hijacking by another process), so the function
// uses the OS default behaviour here.  Windows listeners therefore require
// either a supervisor that waits for TIME_WAIT to drain or an explicit brief
// outage during restart.
func ListenTCPWithReuse(addr string) (net.Listener, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var controlErr error
			err := c.Control(func(fd uintptr) {
				controlErr = setReuseAddr(fd)
			})
			if err != nil {
				return err
			}
			return controlErr
		},
	}
	return lc.Listen(context.Background(), "tcp", addr)
}
