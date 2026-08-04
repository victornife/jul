// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// http3Compiled reports whether this binary includes HTTP/3 support. It is true
// only in builds with the http3 tag, which link the quic-go dependency.
const http3Compiled = true

// h3Conn is a running HTTP/3 listener: the QUIC listener, the quic-go HTTP/3
// server that handles accepted connections, and the UDP socket they share.
type h3Conn struct {
	server *http3.Server
	ln     *quic.EarlyListener
	udp    *net.UDPConn

	onConn func(int64)
	log    *slog.Logger

	// activateOnce ensures the accept loop starts at most once.
	activateOnce sync.Once
}

// Close gracefully drains in-flight HTTP/3 requests (bounded by ctx, which the
// server lifecycle derives from the configured shutdown_timeout), stops
// accepting new QUIC connections, and releases the UDP socket.
func (c *h3Conn) Close(ctx context.Context) error {
	_ = c.server.Shutdown(ctx) // GOAWAY + drain; marks the server closed
	err := c.ln.Close()        // unblock acceptLoop
	_ = c.udp.Close()          // release the socket
	return err
}

// Activate starts accepting QUIC connections and serving HTTP/3. It is safe to
// call multiple times; the accept loop runs once.
func (c *h3Conn) Activate() error {
	var err error
	c.activateOnce.Do(func() {
		go c.acceptLoop(c.onConn, c.log)
	})
	return err
}

// startHTTP3 creates and immediately activates an HTTP/3 listener. It exists
// for direct tests that need a running h3 endpoint without going through the
// server lifecycle. Prefer newStagedHTTP3WithTLS inside the listener build path.
func startHTTP3(addr string, getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error), handler http.Handler, onConn func(int64), log *slog.Logger) (h3Listener, error) {
	h3, err := newStagedHTTP3(addr, getCert, handler, onConn, log)
	if err != nil {
		return nil, err
	}
	_ = h3.Activate()
	return h3, nil
}

// newStagedHTTP3 is the certificate-only compatibility seam used by focused
// HTTP/3 tests. Production listener construction must use
// newStagedHTTP3WithTLS so QUIC receives the complete server TLS policy,
// including client-auth mode, CA pool and additional peer verification.
func newStagedHTTP3(addr string, getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error), handler http.Handler, onConn func(int64), log *slog.Logger) (h3Listener, error) {
	return newStagedHTTP3WithTLS(addr, &tls.Config{GetCertificate: getCert}, handler, onConn, log)
}

// newStagedHTTP3WithTLS opens a UDP listener on addr and prepares HTTP/3 (QUIC)
// serving there using handler, but does not start accepting connections. The
// accept loop is started by Activate.
//
// tlsTemplate is the fully prepared TLS policy used by the sibling TCP listener.
// It is cloned before QUIC-specific changes so the caller's config remains
// immutable. HTTP/3 mandates TLS 1.3; every other relevant policy field is
// preserved, including GetCertificate, ClientAuth, ClientCAs,
// VerifyPeerCertificate and VerifyConnection.
func newStagedHTTP3WithTLS(addr string, tlsTemplate *tls.Config, handler http.Handler, onConn func(int64), log *slog.Logger) (h3Listener, error) {
	if tlsTemplate == nil {
		return nil, errors.New("http3 requires a TLS configuration")
	}

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve udp address: %w", err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen udp: %w", err)
	}

	// Clone the complete TCP listener policy before applying QUIC's mandatory
	// TLS 1.3 floor and h3 ALPN. This keeps server-level mTLS, CA, SAN and CRL
	// verification equivalent across TCP TLS and QUIC handshakes.
	h3TLS := tlsTemplate.Clone()
	if h3TLS.MaxVersion != 0 && h3TLS.MaxVersion < tls.VersionTLS13 {
		_ = udpConn.Close()
		return nil, errors.New("http3 requires TLS 1.3 but the configured maximum TLS version is lower")
	}
	h3TLS.MinVersion = tls.VersionTLS13
	tlsConf := http3.ConfigureTLSConfig(h3TLS)

	ln, err := quic.ListenEarly(udpConn, tlsConf, nil)
	if err != nil {
		_ = udpConn.Close()
		return nil, fmt.Errorf("listen quic: %w", err)
	}

	inst := &h3Conn{
		server: &http3.Server{Handler: handler},
		ln:     ln,
		udp:    udpConn,
		onConn: onConn,
		log:    log,
	}
	return inst, nil
}

// acceptLoop accepts QUIC connections and serves each with the HTTP/3 server,
// adjusting the connection gauge around each connection's lifetime. It returns
// when the listener is closed (Accept then errors).
func (c *h3Conn) acceptLoop(onConn func(int64), log *slog.Logger) {
	for {
		conn, err := c.ln.Accept(context.Background())
		if err != nil {
			return // listener closed
		}
		if onConn != nil {
			onConn(1)
		}
		go func() {
			if onConn != nil {
				defer onConn(-1)
			}
			if err := c.server.ServeQUICConn(conn); err != nil &&
				!errors.Is(err, http.ErrServerClosed) {
				log.Debug("http3 connection ended", "error", err)
			}
		}()
	}
}
