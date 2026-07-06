// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"errors"
	"math"
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
//
// Session creation must not exceed the per-listener cap and must not hold udpMu
// across the (potentially slow) backend dial, which would stall teardown of
// other sessions and shutdown. The lock is held only to read/admit/install map
// state; the dial happens unlocked, guarded by a per-key pending entry so that
// concurrent datagrams for the same new client dial exactly one backend
// (singleflight) instead of racing to create duplicate sessions.
func (l *listener) udpSessionFor(clientAddr *net.UDPAddr) *udpSession {
	key := clientAddr.String()
	l.udpMu.Lock()
	if sess, ok := l.udpSessions[key]; ok {
		l.udpMu.Unlock()
		return sess
	}
	if p, ok := l.udpPending[key]; ok {
		// Another goroutine is dialing this exact client; wait for its
		// result without holding the lock instead of dialing a second time.
		l.udpMu.Unlock()
		<-p.done
		if p.sess != nil {
			return p.sess
		}
		return nil
	}
	r := l.route.Load()
	if r.defaultPool == nil {
		l.udpMu.Unlock()
		return nil
	}
	// Enforce the session cap before reserving a slot. A reclaimed idle
	// victim is detached from the map under the lock and torn down after
	// unlocking (no backend I/O under udpMu).
	victim, victimKey, ok := l.admitUDPLocked(r.maxUDPSessions, r.idleTimeout, time.Now().UnixNano())
	if !ok {
		l.udpMu.Unlock()
		l.server.udpRejected()
		return nil
	}
	p := &udpPending{done: make(chan struct{})}
	l.udpPending[key] = p
	l.udpMu.Unlock()

	if victim != nil {
		_ = victim.backend.Close()
		victim.pool.Release(victim.b)
		l.server.connDelta("udp", -1)
		l.server.udpEvicted("lru")
		l.server.log.Debug("stream: udp session evicted at cap", "addr", l.addr, "client", victimKey)
	}

	backend, b, err := l.dialBackend(r.defaultPool, "udp", r.connectTimeout)

	l.udpMu.Lock()
	delete(l.udpPending, key)
	if err != nil {
		l.udpMu.Unlock()
		close(p.done) // p.sess stays nil: signal failure to any waiters
		l.server.log.Warn("stream: dial udp backend failed", "addr", l.addr, "error", err)
		return nil
	}
	sess := &udpSession{backend: backend, pool: r.defaultPool, b: b}
	sess.lastSeen.Store(time.Now().UnixNano())
	l.udpSessions[key] = sess
	l.udpMu.Unlock()

	p.sess = sess
	close(p.done)

	l.server.connDelta("udp", 1)
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.udpDownstream(clientAddr, sess, r.idleTimeout)
	}()
	return sess
}

// udpPending coordinates concurrent creation of the session for one client key
// so the backend is dialed exactly once. The creator closes done after setting
// sess (success) or leaving it nil (dial failed); waiters read sess after done.
type udpPending struct {
	done chan struct{}
	sess *udpSession
}

// admitUDPLocked enforces the per-listener UDP session cap. It must be called
// with udpMu held. When below the cap it admits unconditionally. At the cap it
// reclaims the least-recently-seen session, but only if that session is already
// idle past idleTimeout, so an established active flow is never displaced by a
// newcomer (a source-address flood is rejected rather than evicting live
// sessions). A reclaimed victim is detached from the session map here and
// returned for teardown by the caller after it releases the lock. ok=false
// means the cap is reached and nothing is reclaimable: reject the new client.
func (l *listener) admitUDPLocked(maxSessions int, idle time.Duration, now int64) (victim *udpSession, victimKey string, ok bool) {
	if maxSessions <= 0 {
		return nil, "", true
	}
	if len(l.udpSessions)+len(l.udpPending) < maxSessions {
		return nil, "", true
	}
	var oldest *udpSession
	var oldestKey string
	oldestSeen := int64(math.MaxInt64)
	for k, s := range l.udpSessions {
		if ls := s.lastSeen.Load(); ls < oldestSeen {
			oldestSeen, oldest, oldestKey = ls, s, k
		}
	}
	if oldest == nil || now-oldestSeen < idle.Nanoseconds() {
		return nil, "", false
	}
	delete(l.udpSessions, oldestKey)
	return oldest, oldestKey, true
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
	idleReaped := false
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
				idleReaped = true
			}
			break
		}
	}
	if idleReaped {
		l.server.udpEvicted("idle")
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
