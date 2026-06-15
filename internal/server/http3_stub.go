//go:build !http3

package server

import (
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
)

// http3Compiled reports whether this binary includes HTTP/3 support. It is false
// in the default build, which excludes the quic-go dependency entirely.
const http3Compiled = false

// startHTTP3 is the no-HTTP/3 stub. It is never reached for a valid run because
// CheckHTTP3 fails startup when a configuration enables HTTP/3 in this build;
// it returns a clear error defensively (e.g. if reached via a reload path).
func startHTTP3(_ string, _ func(*tls.ClientHelloInfo) (*tls.Certificate, error), _ http.Handler, _ func(int64), _ *slog.Logger) (h3Listener, error) {
	return nil, errors.New("http3 requires a build with -tags http3")
}
