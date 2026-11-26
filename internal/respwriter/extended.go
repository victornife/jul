// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package respwriter

import (
	"io"
	"sync"
	"time"
)

type extendedTunnel struct {
	ex    *ActiveExchange
	front io.ReadWriteCloser
	back  io.ReadWriteCloser

	mu             sync.Mutex
	frontFrames    wsFrameTracker
	closing        bool
	frontCloseSent bool
	closeCode      uint16
	done           chan struct{}
	changed        chan struct{}
	closeOnce      sync.Once
}

type extendedTunnelFrontend struct {
	tunnel *extendedTunnel
}

func (w *extendedTunnelFrontend) Read(p []byte) (int, error) {
	return w.tunnel.front.Read(p)
}

func (w *extendedTunnelFrontend) Write(p []byte) (int, error) {
	return w.tunnel.writeFront(p)
}

func (w *extendedTunnelFrontend) Close() error {
	return w.tunnel.close()
}

func (t *extendedTunnel) writeFront(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closing && !t.frontCloseSent {
		if !t.frontFrames.atFrameBoundary() {
			n := t.frontFrames.bytesToNextBoundary(p)
			if n == 0 {
				written, err := t.front.Write(p)
				if written > 0 {
					t.frontFrames.feed(p[:written])
				}
				return written, err
			}
			written, err := t.front.Write(p[:n])
			if written > 0 {
				t.frontFrames.feed(p[:written])
			}
			if err != nil || written != n {
				if err == nil {
					err = io.ErrShortWrite
				}
				return written, err
			}
			if t.frontFrames.atFrameBoundary() {
				if err = t.sendFrontCloseLocked(); err != nil {
					return n, err
				}
			}
			rest, restErr := t.front.Write(p[n:])
			if rest > 0 {
				t.frontFrames.feed(p[n : n+rest])
			}
			t.notify()
			return n + rest, restErr
		}
		if err := t.sendFrontCloseLocked(); err != nil {
			return 0, err
		}
	}
	n, err := t.front.Write(p)
	if n > 0 {
		t.frontFrames.feed(p[:n])
	}
	t.notify()
	return n, err
}

func (t *extendedTunnel) sendFrontCloseLocked() error {
	if t.frontCloseSent || !t.frontFrames.atFrameBoundary() {
		return nil
	}
	frame := buildUnmaskedCloseFrame(t.closeCode)
	n, err := t.front.Write(frame)
	if err == nil && n == len(frame) {
		t.frontCloseSent = true
		t.notify()
		return nil
	}
	if err == nil {
		err = io.ErrShortWrite
	}
	return err
}

func (t *extendedTunnel) initiateClose(grace, _ time.Time, code uint16) {
	t.mu.Lock()
	start := !t.closing
	if start {
		t.closing = true
		t.closeCode = code
		if t.closeCode == 0 {
			t.closeCode = WSClosePolicyViolation
		}
		if setter, ok := t.front.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = setter.SetWriteDeadline(grace)
		}
	}
	closeCode := t.closeCode
	t.mu.Unlock()
	if start {
		go func() {
			t.mu.Lock()
			_ = t.sendFrontCloseLocked()
			t.mu.Unlock()
			if back, ok := t.back.(*upgradedBackConn); ok {
				back.sendMaskedClose(closeCode, grace)
			} else if t.back != nil {
				if setter, ok := t.back.(interface{ SetWriteDeadline(time.Time) error }); ok {
					_ = setter.SetWriteDeadline(grace)
				}
				_, _ = t.back.Write(buildMaskedCloseFrame(closeCode))
			}
			t.notify()
		}()
	}
	t.notify()
}

func (t *extendedTunnel) drain(ex *ActiveExchange, grace, bound time.Time) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if t.cleanCloseReceived() {
			if handshake := ex.upgradedHandshake.Load(); handshake != nil {
				handshake.Store(true)
			}
			_ = t.close()
			return
		}
		if time.Until(grace) <= 0 {
			ex.forcedClose.Store(true)
			ex.triggerStreamAbort(false)
			_ = t.close()
			break
		}
		select {
		case <-t.done:
			return
		case <-ex.doneCh:
			return
		case <-t.changed:
		case <-ticker.C:
		}
	}
	if wait := time.Until(bound); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-t.done:
		case <-ex.doneCh:
		case <-timer.C:
			ex.triggerStreamAbort(false)
		}
	}
}

func (t *extendedTunnel) cleanCloseReceived() bool {
	t.mu.Lock()
	frontClosed := t.frontFrames.sawCloseFrame
	t.mu.Unlock()
	back, ok := t.back.(*upgradedBackConn)
	return frontClosed && ok && back.peerCloseSeen()
}

func (t *extendedTunnel) notify() {
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

func (t *extendedTunnel) close() error {
	var frontErr, backErr error
	t.closeOnce.Do(func() {
		close(t.done)
		if t.back != nil {
			backErr = t.back.Close()
		}
		if t.front != nil {
			frontErr = t.front.Close()
		}
	})
	if backErr != nil {
		return backErr
	}
	return frontErr
}
