//go:build stream

package stream

import (
	"context"
	"net"
	"syscall"
)

// ListenTCPWithReuse opens a TCP listener with SO_REUSEADDR set. This allows
// rapid restarts (e.g. during development or reload) to reuse the socket even
// when the previous process is still in TIME_WAIT.
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
