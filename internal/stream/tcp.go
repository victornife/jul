// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"bufio"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"jul/internal/clientaddr"
	"jul/internal/proxyproto"
	"jul/internal/upstream"
)

// diagLogInterval is the shortest gap between two diagnostics of one kind from
// one listener. A refused connection, a rejected header and an unmatched route
// are all triggered by whoever connects, so each line is a heartbeat rather
// than a per-connection record. It matches the interval the HTTP boundary uses
// for the same reason.
const diagLogInterval = 10 * time.Second

// serveTCP accepts connections until the listener is closed and relays each to
// a backend in its own goroutine.
func (l *listener) serveTCP() {
	for {
		conn, err := l.tcpLn.Accept()
		if err != nil {
			if isClosedConn(err) {
				return
			}
			l.server.log.Warn("stream: accept failed", "addr", l.addr, "error", err)
			time.Sleep(20 * time.Millisecond) // guard against a hot error loop
			continue
		}
		l.wg.Add(1)
		go func(c net.Conn) {
			defer l.wg.Done()
			l.handleTCP(c)
		}(conn)
	}
}

// handleTCP performs the per-connection sequence: optional inbound PROXY header,
// optional SNI route selection, backend dial, optional outbound PROXY header,
// and full-duplex relay.
func (l *listener) handleTCP(client net.Conn) {
	s := l.server
	defer client.Close()
	r := l.route.Load()

	s.connDelta("tcp", 1)
	defer s.connDelta("tcp", -1)

	// Buffer large enough to peek a whole TLS ClientHello for SNI routing.
	br := bufio.NewReaderSize(client, tlsRecordMax+512)

	clientAddr := client.RemoteAddr()
	if r.proxyIn {
		// The header is an assertion, so only a declared proxy may make it. A
		// listener in "in" mode states that all traffic arrives via that proxy,
		// so an untrusted peer is refused rather than degraded to its own
		// address: degrading would let a direct client bypass the requirement
		// simply by sending no header.
		if !r.trustedProxies.Trusts(clientaddr.PeerFromRemoteAddr(clientAddr.String())) {
			if l.proxyLog.Allow(diagLogInterval) {
				s.log.Warn("stream: proxy-protocol connection refused from an untrusted peer", "addr", l.addr)
			}
			return
		}
		_ = client.SetReadDeadline(time.Now().Add(r.connectTimeout))
		src, err := proxyproto.ReadHeader(br)
		if err != nil {
			if l.proxyLog.Allow(diagLogInterval) {
				s.log.Warn("stream: proxy-protocol header rejected", "addr", l.addr, "error", err)
			}
			return
		}
		if src != nil {
			clientAddr = src
		}
		_ = client.SetReadDeadline(time.Time{})
	}

	pool := r.defaultPool
	if len(r.sniPools) > 0 {
		_ = client.SetReadDeadline(time.Now().Add(r.connectTimeout))
		host := peekSNI(br)
		_ = client.SetReadDeadline(time.Time{})
		switch {
		case host != "" && r.sniPools[strings.ToLower(host)] != nil:
			pool = r.sniPools[strings.ToLower(host)]
		case r.sniPools["*"] != nil:
			pool = r.sniPools["*"]
		}
	}
	if pool == nil {
		if l.routeLog.Allow(diagLogInterval) {
			s.log.Warn("stream: no backend route for connection", "addr", l.addr)
		}
		return
	}

	// Admission is taken once the pool is known and held for the connection's
	// whole life: at L4 the connection *is* the unit of work, so
	// max_active_requests means concurrent connections here.
	//
	// The wait is bounded by the server context and by pending_timeout, not by a
	// request context — a TCP client has no way to signal that it gave up short
	// of closing, and a closed socket is only observed once we try to read it.
	release, err := pool.Admission().Admit(s.ctx, nil)
	if err != nil {
		// There is no status code to send on a raw socket; closing is the only
		// signal L4 has. The rejection is still counted.
		s.dialFailure("tcp", upstream.ClassifyAdmissionError(err))
		if l.routeLog.Allow(diagLogInterval) {
			s.log.Warn("stream: connection rejected", "addr", l.addr, "upstream", pool.Name(), "error", err)
		}
		return
	}
	defer release()

	backend, b, err := l.dialBackend(pool, "tcp", r.connectTimeout)
	if err != nil {
		// Counted and logged (throttled once known-down) inside dialBackend, so
		// a broken backend plus ordinary connection volume cannot flood the log.
		return
	}
	defer pool.Release(b)
	defer backend.Close()

	if r.proxyOut {
		if err := proxyproto.WriteV2(backend, clientAddr, client.LocalAddr()); err != nil {
			s.log.Warn("stream: write proxy-protocol header", "addr", l.addr, "error", err)
			return
		}
	}

	l.relayTCP(client, backend, br, r.idleTimeout)
}

// relayTCP copies data in both directions until either side closes, then tears
// down both connections to unblock the other copy.
func (l *listener) relayTCP(client, backend net.Conn, clientReader io.Reader, idle time.Duration) {
	var once sync.Once
	teardown := func() {
		_ = client.Close()
		_ = backend.Close()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		l.copyStream(backend, clientReader, client, idle, "up")
		once.Do(teardown)
	}()
	go func() {
		defer wg.Done()
		l.copyStream(client, backend, backend, idle, "down")
		once.Do(teardown)
	}()
	wg.Wait()
}

// copyStream copies src->dst, refreshing the idle read deadline on srcConn each
// iteration and reporting relayed bytes. It returns when either side errors.
func (l *listener) copyStream(dst io.Writer, src io.Reader, srcConn net.Conn, idle time.Duration, dir string) {
	buf := make([]byte, 32*1024)
	for {
		if idle > 0 {
			_ = srcConn.SetReadDeadline(time.Now().Add(idle))
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
			l.server.addBytes(l.proto, dir, int64(n))
		}
		if rerr != nil {
			return
		}
	}
}
