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

// startHTTP3 opens a UDP listener on addr and serves HTTP/3 (QUIC) there using
// handler. getCert is the same dynamic certificate source the TCP/TLS listener
// uses, so static and ACME certificates — including hot reloads — apply to
// HTTP/3 identically without a separate refresh path. onConn, when non-nil, is
// called with +1 when a QUIC connection opens and -1 when it closes, backing the
// jul_http3_connections gauge. Connections are accepted and served in a
// background goroutine until Close.
func startHTTP3(addr string, getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error), handler http.Handler, onConn func(int64), log *slog.Logger) (h3Listener, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve udp address: %w", err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen udp: %w", err)
	}

	// QUIC mandates TLS 1.3; ConfigureTLSConfig adds the h3 ALPN. The certificate
	// source is shared with the TCP/TLS listener so reloads apply to h3 too.
	tlsConf := http3.ConfigureTLSConfig(&tls.Config{
		GetCertificate: getCert,
		MinVersion:     tls.VersionTLS13,
	})

	ln, err := quic.ListenEarly(udpConn, tlsConf, nil)
	if err != nil {
		_ = udpConn.Close()
		return nil, fmt.Errorf("listen quic: %w", err)
	}

	inst := &h3Conn{
		server: &http3.Server{Handler: handler},
		ln:     ln,
		udp:    udpConn,
	}
	go inst.acceptLoop(onConn, log)
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
