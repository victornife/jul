// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package respwriter

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// wsFrameTracker tracks RFC 6455 frame boundaries on a byte stream so Jul can:
//  1. know whether a WebSocket stream is currently between frames (safe to
//     insert a Close control frame) or mid-frame; and
//  2. detect when a peer has sent a complete, RFC 6455-valid Close control
//     frame (opcode 0x8).
type wsFrameTracker struct {
	hdrBuf           [14]byte
	hdrLen           int
	remainingPayload uint64
	curOpcode        byte
	curFin           bool
	curMasked        bool
	curMaskKey       [4]byte
	curMaskOffset    int
	closePayload     [125]byte
	closePayloadLen  int
	expectMasked     bool
	corrupted        bool
	sawCloseFrame    bool
}

func (t *wsFrameTracker) atFrameBoundary() bool {
	return !t.corrupted && t.hdrLen == 0 && t.remainingPayload == 0
}

func (t *wsFrameTracker) neededHeaderBytes() int {
	if t.hdrLen < 2 {
		return 2
	}
	n := 2
	len7 := t.hdrBuf[1] & 0x7f
	switch len7 {
	case 126:
		n += 2
	case 127:
		n += 8
	}
	if (t.hdrBuf[1] & 0x80) != 0 {
		n += 4
	}
	return n
}

// bytesToNextBoundary returns how many prefix bytes of p belong to the
// currently in-flight frame (or len(p) if the frame extends beyond p). When
// already at a frame boundary it returns 0.
func (t *wsFrameTracker) bytesToNextBoundary(p []byte) int {
	if t.corrupted || t.atFrameBoundary() || len(p) == 0 {
		return 0
	}
	cp := *t
	for i := 0; i < len(p); i++ {
		cp.feed(p[i : i+1])
		if cp.atFrameBoundary() || cp.corrupted {
			return i + 1
		}
	}
	return len(p)
}

func isValidWireCloseCode(code uint16) bool {
	if code < 1000 || code >= 5000 {
		return false
	}
	if code >= 3000 {
		return true
	}
	switch code {
	case 1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011, 1012, 1013, 1014:
		return true
	default:
		// 1004, 1005, 1006, 1015, and unassigned 1016-2999 must not appear on the wire (RFC 6455 §7.4.1).
		return false
	}
}

func isValidClosePayload(body []byte) bool {
	if len(body) == 0 {
		return true
	}
	if len(body) == 1 {
		return false
	}
	code := binary.BigEndian.Uint16(body[:2])
	if !isValidWireCloseCode(code) {
		return false
	}
	return utf8.Valid(body[2:])
}

func (t *wsFrameTracker) feed(p []byte) {
	if t.corrupted {
		return
	}
	for len(p) > 0 {
		if t.remainingPayload > 0 {
			take := uint64(len(p))
			if take > t.remainingPayload {
				take = t.remainingPayload
			}
			if t.curOpcode == 0x8 {
				for i := 0; i < int(take); i++ {
					b := p[i]
					if t.curMasked {
						b ^= t.curMaskKey[t.curMaskOffset&3]
						t.curMaskOffset++
					}
					if t.closePayloadLen < len(t.closePayload) {
						t.closePayload[t.closePayloadLen] = b
						t.closePayloadLen++
					}
				}
			}
			t.remainingPayload -= take
			p = p[int(take):]
			if t.remainingPayload == 0 {
				if t.curOpcode == 0x8 && t.curFin {
					if !isValidClosePayload(t.closePayload[:t.closePayloadLen]) {
						t.corrupted = true
						return
					}
					t.sawCloseFrame = true
				}
				t.hdrLen = 0
			}
			continue
		}

		need := t.neededHeaderBytes()
		for t.hdrLen < need && len(p) > 0 {
			t.hdrBuf[t.hdrLen] = p[0]
			t.hdrLen++
			p = p[1:]
			need = t.neededHeaderBytes()
		}
		if t.hdrLen < need {
			return
		}

		b0 := t.hdrBuf[0]
		b1 := t.hdrBuf[1]
		t.curFin = (b0 & 0x80) != 0
		t.curOpcode = b0 & 0x0f
		t.curMasked = (b1 & 0x80) != 0
		if t.expectMasked != t.curMasked {
			// RFC 6455 §5.1: client-to-server frames MUST be masked and
			// server-to-client frames MUST NOT be masked.
			t.corrupted = true
			return
		}
		// Control frames (opcode >= 0x8) must never set RSV1, RSV2, or RSV3
		// (RFC 6455 §5.2, RFC 7692 §6.1), and opcodes 0x3..0x7 / 0xB..0xF are reserved.
		if (t.curOpcode >= 0x8 && (b0&0x70) != 0) || (t.curOpcode >= 0x3 && t.curOpcode <= 0x7) || t.curOpcode > 0x0a {
			t.corrupted = true
			return
		}
		if t.curMasked {
			copy(t.curMaskKey[:], t.hdrBuf[need-4:need])
			t.curMaskOffset = 0
		}
		len7 := b1 & 0x7f
		var payloadLen uint64
		switch len7 {
		case 126:
			payloadLen = uint64(binary.BigEndian.Uint16(t.hdrBuf[2:4]))
			// RFC 6455 §5.2: minimal length encoding requires 16-bit extended length >= 126.
			if payloadLen < 126 {
				t.corrupted = true
				return
			}
		case 127:
			payloadLen = binary.BigEndian.Uint64(t.hdrBuf[2:10])
			// RFC 6455 §5.2: MSB must be 0 and minimal length encoding requires 64-bit length > 65535.
			if (payloadLen&(uint64(1)<<63)) != 0 || payloadLen <= 0xffff {
				t.corrupted = true
				return
			}
		default:
			payloadLen = uint64(len7)
		}
		// Control frames (opcode >= 8) cannot have payload > 125 or be fragmented (RFC 6455 §5.5).
		// Close frames (opcode 8) cannot have a 1-byte payload (RFC 6455 §5.5.1).
		if t.curOpcode >= 0x8 && (!t.curFin || payloadLen > 125 || (t.curOpcode == 0x8 && payloadLen == 1)) {
			t.corrupted = true
			return
		}
		if t.curOpcode == 0x8 {
			t.closePayloadLen = 0
		}
		t.remainingPayload = payloadLen
		if t.remainingPayload == 0 {
			if t.curOpcode == 0x8 && t.curFin {
				t.sawCloseFrame = true
			}
			t.hdrLen = 0
		}
	}
}

func buildUnmaskedCloseFrame(code uint16) []byte {
	var frame [4]byte
	frame[0] = 0x88 // FIN=1, opcode=8 (Close)
	frame[1] = 0x02 // MASK=0, payload len=2
	binary.BigEndian.PutUint16(frame[2:4], code)
	return frame[:]
}

func buildMaskedCloseFrame(code uint16) []byte {
	var frame [8]byte
	frame[0] = 0x88 // FIN=1, opcode=8 (Close)
	frame[1] = 0x82 // MASK=1, payload len=2
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		mask = [4]byte{0x37, 0xfa, 0x21, 0x3d}
	}
	copy(frame[2:6], mask[:])
	var payload [2]byte
	binary.BigEndian.PutUint16(payload[:], code)
	frame[6] = payload[0] ^ mask[0]
	frame[7] = payload[1] ^ mask[1]
	return frame[:]
}

type upgradedSession struct {
	ex            *ActiveExchange
	rawConn       net.Conn
	origBRW       *bufio.ReadWriter
	wrappedClient *upgradedClientConn
	wrappedRW     *bufio.ReadWriter

	mu                      sync.Mutex
	cond                    *sync.Cond
	back                    *upgradedBackConn
	isWebSocket             bool
	httpHeaderDone          bool
	httpHeaderBuf           []byte
	closingRequested        bool
	closeCode               uint16
	graceDeadline           time.Time
	boundDeadline           time.Time
	hardTimer               *time.Timer
	deadlineChangedCh       chan struct{}
	closeSentToClient       bool
	closeReceivedFromClient bool
	handshakeCompleted      atomic.Bool
	forceClosed             atomic.Bool
	closedCh                chan struct{}
	closeOnce               sync.Once
	initiateOnce            sync.Once
}

func newUpgradedSession(ex *ActiveExchange, conn net.Conn, brw *bufio.ReadWriter, back *upgradedBackConn, isWS bool) *upgradedSession {
	s := &upgradedSession{
		ex:                ex,
		rawConn:           conn,
		origBRW:           brw,
		back:              back,
		isWebSocket:       isWS,
		deadlineChangedCh: make(chan struct{}, 1),
		closedCh:          make(chan struct{}),
	}
	s.cond = sync.NewCond(&s.mu)
	// Seed buffered bytes that bufio.Reader may already hold after the HTTP request header.
	var preRead []byte
	if brw != nil && brw.Reader != nil && brw.Reader.Buffered() > 0 {
		peek, _ := brw.Peek(brw.Reader.Buffered())
		if len(peek) > 0 {
			preRead = append([]byte(nil), peek...)
			_, _ = brw.Discard(len(peek))
		}
	}
	s.wrappedClient = &upgradedClientConn{
		Conn:    conn,
		session: s,
		preRead: preRead,
	}
	s.wrappedClient.c2sTracker.expectMasked = true
	s.wrappedRW = bufio.NewReadWriter(bufio.NewReader(s.wrappedClient), bufio.NewWriter(s.wrappedClient))
	return s
}

func (s *upgradedSession) attachBackend(back *upgradedBackConn, isWS bool) {
	s.mu.Lock()
	s.back = back
	if isWS {
		s.isWebSocket = true
	}
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *upgradedSession) attachSessionToBack(back *upgradedBackConn) {
	if back == nil {
		return
	}
	s.mu.Lock()
	s.back = back
	if back.isWebSocket {
		s.isWebSocket = true
	}
	s.mu.Unlock()
}

func (s *upgradedSession) clientConn() net.Conn {
	return s.wrappedClient
}

func (s *upgradedSession) clientReadWriter() *bufio.ReadWriter {
	return s.wrappedRW
}

func (s *upgradedSession) completedCleanHandshake() bool {
	return s.handshakeCompleted.Load()
}

func (s *upgradedSession) waitClosed() {
	<-s.closedCh
}

func (s *upgradedSession) markClosed() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		back := s.back
		s.cond.Broadcast()
		s.mu.Unlock()
		if back != nil {
			back.releaseHold()
			_ = back.closeUnderlying()
		}
		_ = s.rawConn.Close()
		close(s.closedCh)
		if s.ex != nil {
			s.ex.markClosed()
		}
	})
}

func (s *upgradedSession) currentDeadlines() (grace, bound time.Time) {
	s.mu.Lock()
	grace = s.graceDeadline
	bound = s.boundDeadline
	s.mu.Unlock()
	return grace, bound
}

func (s *upgradedSession) initiateClose(grace, bound time.Time, code uint16) {
	if code == 0 {
		code = WSClosePolicyViolation
	}
	s.mu.Lock()
	shortened := false
	if s.graceDeadline.IsZero() || grace.Before(s.graceDeadline) {
		s.graceDeadline = grace
		shortened = true
	}
	if s.boundDeadline.IsZero() || bound.Before(s.boundDeadline) {
		s.boundDeadline = bound
		shortened = true
	}
	if s.closeCode == 0 {
		s.closeCode = code
	}
	effGrace := s.graceDeadline
	effBound := s.boundDeadline
	effCode := s.closeCode
	if s.hardTimer != nil && shortened {
		s.hardTimer.Reset(maxDuration(time.Until(effBound), 0))
	}
	s.closingRequested = true
	s.cond.Broadcast()
	s.mu.Unlock()

	if shortened {
		_ = s.rawConn.SetReadDeadline(effGrace)
		select {
		case s.deadlineChangedCh <- struct{}{}:
		default:
		}
	}

	s.initiateOnce.Do(func() {
		go s.runCloseSequence(effCode)
	})
}

func (s *upgradedSession) runCloseSequence(code uint16) {
	// Hard upper-bound watchdog: guarantees both transport legs are closed by bound,
	// and can be rearmed if a subsequent shutdown/listener-removal call shortens bound (#562, F5).
	s.mu.Lock()
	bound := s.boundDeadline
	hardTimer := time.AfterFunc(maxDuration(time.Until(bound), 0), func() {
		s.forceClosed.Store(true)
		s.markClosed()
	})
	s.hardTimer = hardTimer
	s.mu.Unlock()
	defer hardTimer.Stop()

	// Wait briefly if 101 response headers are still being flushed to the client.
	s.mu.Lock()
	for !s.httpHeaderDone && !s.forceClosed.Load() && time.Now().Before(s.graceDeadline) {
		waitDur := minDuration(time.Until(s.graceDeadline), 10*time.Millisecond)
		if waitDur <= 0 {
			break
		}
		s.mu.Unlock()
		time.Sleep(waitDur)
		s.mu.Lock()
	}
	isWS := s.isWebSocket
	back := s.back
	grace := s.graceDeadline
	s.mu.Unlock()

	// Non-WebSocket opaque 101 upgrade (#562): never fabricate a WebSocket frame;
	// close both legs immediately as a forced fallback.
	if !isWS {
		s.forceClosed.Store(true)
		s.markClosed()
		return
	}

	// WebSocket upgrade:
	// 1. Send a masked RFC 6455 Close frame to the upstream leg (when at a frame
	//    boundary) and close the upstream reader so backend->client copying stops.
	if back != nil {
		back.sendMaskedCloseAndClose(code, grace)
	}

	// 2. Wait up to the graceful window for the server->client leg to reach a
	//    WebSocket frame boundary, then send an unmasked Close frame to the client.
	grace, _ = s.currentDeadlines()
	_ = s.rawConn.SetWriteDeadline(grace)
	sentClose := s.wrappedClient.trySendClientCloseAtBoundary(code, grace)
	if !sentClose {
		// Could not safely insert Close frame within grace (e.g. mid-frame stall or slow peer).
		// Wait until current bound (which may be shortened by DrainAll) and count as forced.
		for {
			_, curBound := s.currentDeadlines()
			wait := time.Until(curBound)
			if wait <= 0 {
				break
			}
			timer := time.NewTimer(wait)
			select {
			case <-s.closedCh:
				timer.Stop()
				return
			case <-s.deadlineChangedCh:
				timer.Stop()
			case <-timer.C:
			}
		}
		s.forceClosed.Store(true)
		s.markClosed()
		return
	}

	// 3. Close frame was sent to the client. Bound client reads by grace so we wait
	//    up to the graceful window for the client's reciprocal Close control frame.
	grace, _ = s.currentDeadlines()
	_ = s.rawConn.SetReadDeadline(grace)
	if time.Until(grace) > 0 {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
	waitLoop:
		for {
			if s.handshakeCompleted.Load() {
				break waitLoop
			}
			curGrace, _ := s.currentDeadlines()
			if !time.Now().Before(curGrace) {
				break waitLoop
			}
			select {
			case <-s.closedCh:
				break waitLoop
			case <-s.deadlineChangedCh:
				curGrace, _ = s.currentDeadlines()
				_ = s.rawConn.SetReadDeadline(curGrace)
			case <-ticker.C:
			}
		}
	}
	if !s.handshakeCompleted.Load() {
		s.forceClosed.Store(true)
	}
	s.markClosed()
}

type upgradedClientConn struct {
	net.Conn
	session *upgradedSession

	readMu     sync.Mutex
	preRead    []byte
	c2sTracker wsFrameTracker

	writeMu    sync.Mutex
	s2cTracker wsFrameTracker
}

func (c *upgradedClientConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	if len(c.preRead) > 0 {
		n := copy(p, c.preRead)
		c.preRead = c.preRead[n:]
		c.inspectClientBytes(p[:n])
		return n, nil
	}
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.inspectClientBytes(p[:n])
	}
	if err != nil {
		// If Jul already initiated Close and did not receive a reciprocal Close
		// control frame before EOF/error, this is NOT a clean WebSocket close
		// handshake (RFC 6455 §7.1.4).
		c.session.mu.Lock()
		sent := c.session.closeSentToClient
		rcvd := c.session.closeReceivedFromClient
		c.session.mu.Unlock()
		if sent && !rcvd {
			c.session.forceClosed.Store(true)
		}
		c.session.markClosed()
	}
	return n, err
}

func (c *upgradedClientConn) inspectClientBytes(b []byte) {
	c.session.mu.Lock()
	isWS := c.session.isWebSocket
	c.session.mu.Unlock()
	if !isWS {
		return
	}
	c.c2sTracker.feed(b)
	if c.c2sTracker.corrupted {
		c.session.mu.Lock()
		closing := c.session.closingRequested
		c.session.mu.Unlock()
		if closing {
			c.session.forceClosed.Store(true)
			c.session.markClosed()
		}
		return
	}
	if c.c2sTracker.sawCloseFrame {
		c.session.mu.Lock()
		c.session.closeReceivedFromClient = true
		sent := c.session.closeSentToClient
		closing := c.session.closingRequested
		c.session.mu.Unlock()
		if sent && !c.session.forceClosed.Load() {
			c.session.handshakeCompleted.Store(true)
			// Only tear down the upstream leg inside Read when Jul itself is
			// retiring the session; for a peer-originated Close handshake, let
			// Read return the reciprocal Close bytes so ReverseProxy forwards
			// them to the backend first (#562, F5).
			if closing {
				c.session.markClosed()
			}
		}
	}
}

func (c *upgradedClientConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	s := c.session
	s.mu.Lock()
	if (s.closingRequested && s.closeSentToClient) || s.forceClosed.Load() {
		s.mu.Unlock()
		return 0, net.ErrClosed
	}
	hdrDone := s.httpHeaderDone
	s.mu.Unlock()

	totalWritten := 0
	if !hdrDone {
		s.mu.Lock()
		prevLen := len(s.httpHeaderBuf)
		s.httpHeaderBuf = append(s.httpHeaderBuf, p...)
		idx := bytes.Index(s.httpHeaderBuf, []byte("\r\n\r\n"))
		if idx < 0 {
			s.mu.Unlock()
			return c.Conn.Write(p)
		}
		hdrEndInP := (idx + 4) - prevLen
		if hdrEndInP < 0 {
			hdrEndInP = 0
		}
		if hdrEndInP > len(p) {
			hdrEndInP = len(p)
		}
		hdrText := string(s.httpHeaderBuf[:idx+4])
		if strings.Contains(strings.ToLower(hdrText), "upgrade: websocket") {
			s.isWebSocket = true
		}
		s.httpHeaderDone = true
		s.httpHeaderBuf = nil
		s.cond.Broadcast()
		s.mu.Unlock()

		if hdrEndInP > 0 {
			n, err := c.Conn.Write(p[:hdrEndInP])
			totalWritten += n
			if err != nil {
				return totalWritten, err
			}
			p = p[hdrEndInP:]
		}
		if len(p) == 0 {
			return totalWritten, nil
		}
	}

	s.mu.Lock()
	isWS := s.isWebSocket
	closing := s.closingRequested
	code := s.closeCode
	s.mu.Unlock()

	if !isWS {
		if closing {
			return totalWritten, net.ErrClosed
		}
		n, err := c.Conn.Write(p)
		return totalWritten + n, err
	}

	// If close has been requested while a frame is mid-transmission, write only
	// the remaining bytes of the in-flight frame so we reach the frame boundary,
	// then immediately send the Close frame.
	if closing {
		if c.s2cTracker.atFrameBoundary() {
			c.sendCloseLocked(code)
			return totalWritten, net.ErrClosed
		}
		need := c.s2cTracker.bytesToNextBoundary(p)
		if need > 0 {
			n, err := c.Conn.Write(p[:need])
			if n > 0 {
				c.s2cTracker.feed(p[:n])
				totalWritten += n
			}
			if c.s2cTracker.atFrameBoundary() {
				c.sendCloseLocked(code)
			}
			if err != nil {
				return totalWritten, err
			}
		}
		return totalWritten, net.ErrClosed
	}

	// Normal WebSocket server->client write: write frame-by-frame when multiple
	// frames are in p so a concurrent initiateClose can interpose cleanly at a
	// frame boundary.
	for len(p) > 0 {
		s.mu.Lock()
		if s.closingRequested {
			code = s.closeCode
			s.mu.Unlock()
			if c.s2cTracker.atFrameBoundary() {
				c.sendCloseLocked(code)
				return totalWritten, net.ErrClosed
			}
		} else {
			s.mu.Unlock()
		}

		chunkLen := len(p)
		if !c.s2cTracker.atFrameBoundary() {
			if b := c.s2cTracker.bytesToNextBoundary(p); b > 0 && b < chunkLen {
				chunkLen = b
			}
		}
		n, err := c.Conn.Write(p[:chunkLen])
		if n > 0 {
			c.s2cTracker.feed(p[:n])
			totalWritten += n
			p = p[n:]
			if c.s2cTracker.corrupted {
				s.forceClosed.Store(true)
				s.handshakeCompleted.Store(false)
			} else if c.s2cTracker.sawCloseFrame {
				s.mu.Lock()
				s.closeSentToClient = true
				rcvd := s.closeReceivedFromClient
				s.mu.Unlock()
				if rcvd && !s.forceClosed.Load() {
					s.handshakeCompleted.Store(true)
				}
			}
			if c.s2cTracker.atFrameBoundary() {
				s.mu.Lock()
				s.cond.Broadcast()
				s.mu.Unlock()
			}
		}
		if err != nil {
			return totalWritten, err
		}
	}
	return totalWritten, nil
}

func (c *upgradedClientConn) sendCloseLocked(code uint16) bool {
	s := c.session
	s.mu.Lock()
	if s.closeSentToClient {
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()

	if !c.s2cTracker.atFrameBoundary() {
		return false
	}
	frame := buildUnmaskedCloseFrame(code)
	n, err := c.Conn.Write(frame)
	if err != nil || n != len(frame) {
		return false
	}
	s.mu.Lock()
	s.closeSentToClient = true
	rcvd := s.closeReceivedFromClient
	s.mu.Unlock()
	if rcvd && !s.forceClosed.Load() {
		s.handshakeCompleted.Store(true)
	}
	return true
}

func (c *upgradedClientConn) trySendClientCloseAtBoundary(code uint16, grace time.Time) bool {
	for {
		c.writeMu.Lock()
		c.session.mu.Lock()
		hdrDone := c.session.httpHeaderDone
		alreadySent := c.session.closeSentToClient
		forced := c.session.forceClosed.Load()
		deadline := c.session.graceDeadline
		if deadline.IsZero() {
			deadline = grace
		}
		c.session.mu.Unlock()

		if alreadySent {
			c.writeMu.Unlock()
			return true
		}
		if forced {
			c.writeMu.Unlock()
			return false
		}
		if hdrDone && c.s2cTracker.atFrameBoundary() {
			ok := c.sendCloseLocked(code)
			c.writeMu.Unlock()
			return ok
		}
		c.writeMu.Unlock()

		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (c *upgradedClientConn) Close() error {
	c.session.markClosed()
	return c.Conn.Close()
}

type upgradedBackConn struct {
	io.ReadWriteCloser
	ex          *ActiveExchange
	isWebSocket bool

	writeMu    sync.Mutex
	c2sTracker wsFrameTracker
	closing    atomic.Bool
	closeSent  atomic.Bool
	holdCh     chan struct{}
	holdOnce   sync.Once
	closedOnce sync.Once
	closeErr   error
}

func (b *upgradedBackConn) Read(p []byte) (int, error) {
	if b.closing.Load() {
		<-b.holdCh
		return 0, io.EOF
	}
	n, err := b.ReadWriteCloser.Read(p)
	if err != nil && b.closing.Load() {
		<-b.holdCh
	}
	return n, err
}

func (b *upgradedBackConn) Write(p []byte) (int, error) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	if b.closing.Load() {
		// Discard post-retirement client-to-backend writes while allowing the
		// client Read loop to continue reading the client's reciprocal Close frame.
		return len(p), nil
	}
	if b.isWebSocket {
		b.c2sTracker.expectMasked = true
		b.c2sTracker.feed(p)
	}
	return b.ReadWriteCloser.Write(p)
}

func (b *upgradedBackConn) sendMaskedCloseAndClose(code uint16, grace time.Time) {
	b.closing.Store(true)
	b.writeMu.Lock()
	if b.isWebSocket && !b.closeSent.Load() && b.c2sTracker.atFrameBoundary() {
		if c, ok := b.ReadWriteCloser.(net.Conn); ok {
			_ = c.SetWriteDeadline(grace)
		}
		frame := buildMaskedCloseFrame(code)
		_, _ = b.ReadWriteCloser.Write(frame)
		b.closeSent.Store(true)
	}
	b.writeMu.Unlock()
	_ = b.closeUnderlying()
}

func (b *upgradedBackConn) releaseHold() {
	if b.holdCh != nil {
		b.holdOnce.Do(func() {
			close(b.holdCh)
		})
	}
}

func (b *upgradedBackConn) closeUnderlying() error {
	b.closedOnce.Do(func() {
		b.closeErr = b.ReadWriteCloser.Close()
	})
	return b.closeErr
}

func (b *upgradedBackConn) Close() error {
	b.releaseHold()
	return b.closeUnderlying()
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
