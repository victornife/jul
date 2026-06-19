//go:build stream

package stream

import (
	"errors"
	"net"
	"sync/atomic"
	"time"

	"jul/internal/upstream"
)

// udpSession tracks one client's datagram flow to a fixed backend. UDP is
// connectionless, so JUL keys sessions by client address and forwards datagrams
// to a per-session backend socket, relaying replies back to the client. A
// session is reaped once no datagram flows in either direction for the idle
// timeout.
type udpSession struct {
	backend  net.Conn
	pool     *upstream.Pool
	b        *upstream.Backend
	lastSeen atomic.Int64 // unix nano
}

// serveUDP reads client datagrams and forwards them to each client's backend
// session, creating sessions on demand.
func (l *listener) serveUDP() {
	buf := make([]byte, 64*1024)
	for {
		n, clientAddr, err := l.udpLn.ReadFromUDP(buf)
		if err != nil {
			if isClosedConn(err) {
				return
			}
			l.server.log.Warn("stream: udp read failed", "addr", l.addr, "error", err)
			time.Sleep(20 * time.Millisecond)
			continue
		}
		sess := l.udpSessionFor(clientAddr)
		if sess == nil {
			continue
		}
		sess.lastSeen.Store(time.Now().UnixNano())
		if _, werr := sess.backend.Write(buf[:n]); werr != nil {
			l.closeUDPSession(clientAddr.String(), sess)
			continue
		}
		l.server.addBytes("udp", "up", int64(n))
	}
}

// udpSessionFor returns the existing session for clientAddr or creates one,
// dialing a backend and starting its downstream relay goroutine.
func (l *listener) udpSessionFor(clientAddr *net.UDPAddr) *udpSession {
	key := clientAddr.String()
	l.udpMu.Lock()
	if sess, ok := l.udpSessions[key]; ok {
		l.udpMu.Unlock()
		return sess
	}
	r := l.route.Load()
	if r.defaultPool == nil {
		l.udpMu.Unlock()
		return nil
	}
	backend, b, err := l.dialBackend(r.defaultPool, "udp", r.connectTimeout)
	if err != nil {
		l.udpMu.Unlock()
		l.server.log.Warn("stream: dial udp backend failed", "addr", l.addr, "error", err)
		return nil
	}
	sess := &udpSession{backend: backend, pool: r.defaultPool, b: b}
	sess.lastSeen.Store(time.Now().UnixNano())
	l.udpSessions[key] = sess
	l.udpMu.Unlock()

	l.server.connDelta("udp", 1)
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.udpDownstream(clientAddr, sess, r.idleTimeout)
	}()
	return sess
}

// udpDownstream relays backend replies to the client and reaps the session once
// it is idle in both directions for the idle timeout.
func (l *listener) udpDownstream(clientAddr *net.UDPAddr, sess *udpSession, idle time.Duration) {
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	// Wake periodically so a session kept alive only by client->backend traffic
	// (no replies) is still reaped once both directions go idle.
	checkEvery := idle
	if checkEvery > 15*time.Second {
		checkEvery = 15 * time.Second
	}
	buf := make([]byte, 64*1024)
	for {
		_ = sess.backend.SetReadDeadline(time.Now().Add(checkEvery))
		n, err := sess.backend.Read(buf)
		if n > 0 {
			sess.lastSeen.Store(time.Now().UnixNano())
			if _, werr := l.udpLn.WriteToUDP(buf[:n], clientAddr); werr != nil {
				break
			}
			l.server.addBytes("udp", "down", int64(n))
		}
		if err != nil {
			if isTimeout(err) {
				if time.Since(time.Unix(0, sess.lastSeen.Load())) < idle {
					continue // still active via the client side
				}
			}
			break
		}
	}
	l.closeUDPSession(clientAddr.String(), sess)
}

// closeUDPSession removes and tears down a session exactly once.
func (l *listener) closeUDPSession(key string, sess *udpSession) {
	l.udpMu.Lock()
	cur, ok := l.udpSessions[key]
	if !ok || cur != sess {
		l.udpMu.Unlock()
		return
	}
	delete(l.udpSessions, key)
	l.udpMu.Unlock()

	_ = sess.backend.Close()
	sess.pool.Release(sess.b)
	l.server.connDelta("udp", -1)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
