// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"bufio"
	"log/slog"
	"net"
	"strings"
	"time"

	"jul/internal/clientaddr"
	"jul/internal/logthrottle"
	"jul/internal/proxyproto"
)

// proxyHeaderTimeout bounds how long a connection may take to deliver its PROXY
// header. A balancer writes it immediately; anything slower is holding an accept
// slot without having identified itself.
const proxyHeaderTimeout = 10 * time.Second

// proxyProtoLogInterval throttles the diagnostics, whose rate is chosen by
// whoever connects.
const proxyProtoLogInterval = 10 * time.Second

// proxyProtocolModeForAddr returns the configured mode for addr, or "" when no
// block on it ingests a header. Validation requires the blocks to agree.
func (s *Server) proxyProtocolModeForAddr(addr string) string {
	for i := range s.cfg.Servers {
		if srv := &s.cfg.Servers[i]; srv.Listen == addr && strings.TrimSpace(srv.ProxyProtocol) != "" {
			return srv.ProxyProtocol
		}
	}
	return ""
}

// proxyProtocolTrustForAddr renders the trust set governing an inbound header
// as a stable string for the bind fingerprint.
func (s *Server) proxyProtocolTrustForAddr(addr string) string {
	for i := range s.cfg.Servers {
		srv := &s.cfg.Servers[i]
		if srv.Listen != addr || strings.TrimSpace(srv.ProxyProtocol) == "" || srv.ClientAddress == nil {
			continue
		}
		return strings.Join(srv.ClientAddress.TrustedProxies, ",")
	}
	return ""
}

// proxyProtoPolicyForAddr returns the trusted-proxy policy governing an inbound// PROXY header on addr, or nil when the listener does not ingest one.
//
// It reuses client_address.trusted_proxies rather than introducing a second
// trust list: both answer the same question — which peers may assert a client
// address — and two lists would let them disagree. Validation requires the list
// to be non-empty whenever proxy_protocol is set, and requires every block
// sharing the listener to agree.
func (s *Server) proxyProtoPolicyForAddr(addr string) (*clientaddr.Policy, error) {
	for i := range s.cfg.Servers {
		srv := &s.cfg.Servers[i]
		if srv.Listen != addr || !strings.EqualFold(strings.TrimSpace(srv.ProxyProtocol), "in") {
			continue
		}
		var trusted []string
		if srv.ClientAddress != nil {
			trusted = srv.ClientAddress.TrustedProxies
		}
		// Empty, non-nil header list: this policy answers only "is the peer a
		// declared balancer", never which forwarding header to read.
		return clientaddr.NewPolicy(trusted, []string{}, 0)
	}
	return nil, nil
}

// proxyProtoListener consumes a PROXY-protocol header from every accepted
// connection and reports the advertised address as the connection's peer.
//
// It wraps the raw TCP listener *before* the TLS wrap, because the header is
// plaintext framing that precedes the ClientHello. The advertised address
// becomes Boundary A for everything above it (ADR 0016 §6c), so the ordinary
// client_address derivation then runs unchanged: a balancer that also forwards
// an X-Forwarded-For chain composes with this rather than competing with it.
//
// The header is an assertion, so the socket peer must itself be a declared
// trusted proxy. A connection from anywhere else is closed rather than served
// on its own address: a listener with proxy_protocol enabled declares that all
// traffic arrives via the balancer, and degrading would let a direct client
// bypass the requirement by sending no header.
type proxyProtoListener struct {
	net.Listener
	trusted *clientaddr.Policy
	log     *slog.Logger
	limiter logthrottle.Limiter
}

// Accept returns the next connection that carried a valid header from a trusted
// peer. Rejected connections are closed and skipped rather than surfaced as
// accept errors, which would tear down the listener.
func (l *proxyProtoListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if admitted := l.admit(conn); admitted != nil {
			return admitted, nil
		}
	}
}

// admit validates one connection, returning nil when it must be dropped.
func (l *proxyProtoListener) admit(conn net.Conn) net.Conn {
	if !l.trusted.Trusts(clientaddr.PeerFromRemoteAddr(conn.RemoteAddr().String())) {
		l.warn("http: proxy-protocol connection refused from an untrusted peer")
		_ = conn.Close()
		return nil
	}
	// The reader is retained: it holds whatever bytes were buffered past the
	// header, which are the start of the ClientHello or request line.
	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(proxyHeaderTimeout))
	src, err := proxyproto.ReadHeader(br)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		l.warn("http: proxy-protocol header rejected")
		_ = conn.Close()
		return nil
	}
	remote := conn.RemoteAddr()
	if src != nil {
		remote = src
	}
	return &proxyProtoConn{Conn: conn, buf: br, remote: remote}
}

func (l *proxyProtoListener) warn(msg string) {
	if l.log != nil && l.limiter.Allow(proxyProtoLogInterval) {
		l.log.Warn(msg, "addr", l.Listener.Addr().String())
	}
}

// proxyProtoConn serves the bytes buffered past the header and reports the
// advertised peer.
type proxyProtoConn struct {
	net.Conn
	buf    *bufio.Reader
	remote net.Addr
}

func (c *proxyProtoConn) Read(b []byte) (int, error) { return c.buf.Read(b) }

func (c *proxyProtoConn) RemoteAddr() net.Addr { return c.remote }
