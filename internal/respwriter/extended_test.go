// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package respwriter

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type websocketBufferStream struct {
	bytes.Buffer
	short    bool
	writeErr error
	closed   bool
}

func (s *websocketBufferStream) Write(p []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	if s.short && len(p) > 0 {
		return s.Buffer.Write(p[:len(p)-1])
	}
	return s.Buffer.Write(p)
}

func (s *websocketBufferStream) Close() error {
	s.closed = true
	return nil
}

func writeAndReadTunnel(t *testing.T, writer io.Writer, peer net.Conn, data, want []byte) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		n, err := writer.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(peer, got); err != nil {
		t.Fatalf("read tunnel data: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("tunnel bytes = %x, want %x", got, want)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write tunnel data: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel write did not finish")
	}
}

func readMaskedCloseCode(t *testing.T, frame []byte) uint16 {
	t.Helper()
	if len(frame) != 8 || frame[0] != 0x88 || frame[1] != 0x82 {
		t.Fatalf("invalid masked Close frame: %x", frame)
	}
	return binary.BigEndian.Uint16([]byte{frame[6] ^ frame[2], frame[7] ^ frame[3]})
}

func TestExtendedTunnelBlockedWriteDoesNotBlockWithdrawal(t *testing.T) {
	reg := NewExchangeRegistry()
	reg.Grace = 80 * time.Millisecond
	reg.Bound = 180 * time.Millisecond
	reg.SetInitialGeneration(1)
	client, proxyFront := net.Pipe()
	defer client.Close()
	defer proxyFront.Close()
	upstream, proxyBack := net.Pipe()
	defer upstream.Close()
	defer proxyBack.Close()
	req := httptest.NewRequest(http.MethodConnect, "http://example.test/ws", nil)
	req.ProtoMajor = 2
	_, admitted, _, release := reg.Admit(httptest.NewRecorder(), req, EntryCoord{GenID: 1, RouteID: "blocked"})
	defer release()
	back := AttachUpgradedBackend(admitted.Context(), proxyBack, http.Header{"Upgrade": {"websocket"}})
	// Hide deadlines so only the independently armed cutoff can unblock Write.
	front := AttachExtendedStream(admitted.Context(), struct{ io.ReadWriteCloser }{proxyFront}, back)
	defer front.Close()
	writeDone := make(chan error, 1)
	go func() {
		_, err := front.Write([]byte{0x82, 1, 'x'})
		release()
		writeDone <- err
	}()
	// The pipe read accepts only the header, leaving the payload write blocked.
	header := make([]byte, 2)
	if _, err := io.ReadFull(client, header); err != nil {
		t.Fatal(err)
	}
	siblingRecorder := httptest.NewRecorder()
	siblingReq := httptest.NewRequest(http.MethodGet, "http://example.test/healthy", nil)
	siblingReq.ProtoMajor = 2
	siblingWriter, sibling, siblingEx, siblingRelease := reg.Admit(siblingRecorder, siblingReq, EntryCoord{GenID: 1, RouteID: "healthy", LocationIndex: 1})
	defer siblingRelease()

	start := time.Now()
	published := make(chan func() []EntryRetirementResult, 1)
	go func() {
		published <- reg.PublishGeneration(1, 2, []EntryTransition{
			{FromRouteID: "blocked", ToRouteID: "blocked", Retire: true, Mandatory: true},
			{FromRouteID: "healthy", FromLocationIndex: 1, ToRouteID: "healthy", ToLocationIndex: 1, KeepOpen: true},
		})
	}()
	var wait func() []EntryRetirementResult
	select {
	case wait = <-published:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("withdrawal waited for the blocked tunnel write")
	}
	finished := make(chan []EntryRetirementResult, 1)
	go func() { finished <- wait() }()
	select {
	case <-finished:
	case <-time.After(reg.Bound + 200*time.Millisecond):
		t.Fatal("blocked tunnel exceeded the withdrawal bound")
	}
	if elapsed := time.Since(start); elapsed > reg.Bound+200*time.Millisecond {
		t.Fatalf("withdrawal elapsed=%s, bound=%s", elapsed, reg.Bound)
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("blocked write unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("forced cutoff did not unblock the writer")
	}
	if sibling.Context().Err() != nil || siblingEx.ShouldAbortStream() {
		t.Fatal("withdrawing the blocked tunnel aborted its healthy sibling")
	}
	if _, err := siblingWriter.Write([]byte("healthy")); err != nil || siblingRecorder.Body.String() != "healthy" {
		t.Fatalf("healthy sibling write failed: %v", err)
	}
}

func TestExtendedTunnelRetirementWaitsForFrontendFrameBoundary(t *testing.T) {
	reg := NewExchangeRegistry()
	reg.SetInitialGeneration(1)
	client, proxyFront := net.Pipe()
	defer client.Close()
	upstream, proxyBack := net.Pipe()
	defer upstream.Close()
	for _, peer := range []net.Conn{client, upstream} {
		if err := peer.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodConnect, "http://example.test/ws", nil)
	req.ProtoMajor = 2
	_, admitted, ex, release := reg.Admit(httptest.NewRecorder(), req, EntryCoord{GenID: 1})
	defer release()
	back := AttachUpgradedBackend(admitted.Context(), proxyBack, http.Header{"Upgrade": {"websocket"}})
	front := AttachExtendedStream(admitted.Context(), proxyFront, back)
	defer front.Close()
	if !ex.isUpgraded() {
		t.Fatal("extended tunnel was not recognized as an upgraded exchange")
	}

	writeAndReadTunnel(t, front, client, []byte{0x82, 4, 'a'}, []byte{0x82, 4, 'a'})
	ex.armRetirement(time.Now().Add(2*time.Second), time.Now().Add(3*time.Second), WSClosePolicyViolation, false)
	closeToBackend := make([]byte, 8)
	if _, err := io.ReadFull(upstream, closeToBackend); err != nil {
		t.Fatalf("read backend Close: %v", err)
	}
	if got := readMaskedCloseCode(t, closeToBackend); got != WSClosePolicyViolation {
		t.Fatalf("backend close code=%d, want %d", got, WSClosePolicyViolation)
	}
	writeAndReadTunnel(t, front, client, []byte{'b'}, []byte{'b'})
	remainder := []byte{'c', 'd', 0x81, 1, 'z'}
	want := append([]byte{'c', 'd'}, buildUnmaskedCloseFrame(WSClosePolicyViolation)...)
	want = append(want, 0x81, 1, 'z')
	writeAndReadTunnel(t, front, client, remainder, want)
}

func TestUpgradedBackendRetirementWaitsForClientFrameBoundary(t *testing.T) {
	upstream, proxyBack := net.Pipe()
	defer upstream.Close()
	if err := upstream.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	back := &upgradedBackConn{ReadWriteCloser: proxyBack, isWebSocket: true, holdCh: make(chan struct{})}
	defer back.Close()
	frame := []byte{0x81, 0x84, 1, 2, 3, 4, 'a' ^ 1, 'b' ^ 2, 'c' ^ 3, 'd' ^ 4}
	writeAndReadTunnel(t, back, upstream, frame[:7], frame[:7])
	back.sendMaskedClose(WSClosePolicyViolation, time.Now().Add(2*time.Second))
	writeAndReadTunnel(t, back, upstream, frame[7:8], frame[7:8])

	clientClose := buildMaskedCloseFrame(WSCloseGoingAway)
	remaining := append(append([]byte(nil), frame[8:]...), clientClose...)
	done := make(chan error, 1)
	go func() {
		n, err := back.Write(remaining)
		if err == nil && n != len(remaining) {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	got := make([]byte, len(remaining)+8)
	if _, err := io.ReadFull(upstream, got); err != nil {
		t.Fatalf("read backend frame and Close: %v", err)
	}
	if !bytes.Equal(got[:2], frame[8:]) || !bytes.Equal(got[10:], clientClose) {
		t.Fatalf("backend frame ordering = %x", got)
	}
	if code := readMaskedCloseCode(t, got[2:10]); code != WSClosePolicyViolation {
		t.Fatalf("injected close code=%d, want %d", code, WSClosePolicyViolation)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend write did not finish")
	}
	if !back.peerCloseSeen() {
		t.Fatal("client's relayed Close frame was not recorded")
	}
}

func TestExtendedTunnelForcesUnresponsivePeerAfterLateAttachment(t *testing.T) {
	reg := NewExchangeRegistry()
	reg.SetInitialGeneration(1)
	client, proxyFront := net.Pipe()
	defer client.Close()
	upstream, proxyBack := net.Pipe()
	defer upstream.Close()
	for _, peer := range []net.Conn{client, upstream} {
		if err := peer.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodConnect, "http://example.test/ws", nil)
	req.ProtoMajor = 2
	_, admitted, ex, release := reg.Admit(httptest.NewRecorder(), req, EntryCoord{GenID: 1})
	defer release()
	back := AttachUpgradedBackend(admitted.Context(), proxyBack, http.Header{"Upgrade": {"websocket"}})
	grace := time.Now().Add(300 * time.Millisecond)
	ex.armRetirement(grace, grace.Add(time.Second), 0, false)
	front := AttachExtendedStream(admitted.Context(), proxyFront, back)
	defer front.Close()

	clientClose := make([]byte, 4)
	if _, err := io.ReadFull(client, clientClose); err != nil {
		t.Fatalf("read frontend Close: %v", err)
	}
	if !bytes.Equal(clientClose, buildUnmaskedCloseFrame(WSClosePolicyViolation)) {
		t.Fatalf("frontend Close = %x", clientClose)
	}
	backendClose := make([]byte, 8)
	if _, err := io.ReadFull(upstream, backendClose); err != nil {
		t.Fatalf("read backend Close: %v", err)
	}
	if code := readMaskedCloseCode(t, backendClose); code != WSClosePolicyViolation {
		t.Fatalf("backend close code=%d", code)
	}
	select {
	case <-front.(*extendedTunnelFrontend).tunnel.done:
	case <-time.After(2 * time.Second):
		t.Fatal("unresponsive tunnel was not closed after the grace period")
	}
	if admitted.Context().Err() == nil {
		t.Fatal("unresponsive tunnel's request was not canceled")
	}
	if !ex.forcedClose.Load() || !ex.ShouldAbortStream() {
		t.Fatalf("forced=%t stream abort=%t", ex.forcedClose.Load(), ex.ShouldAbortStream())
	}
}

func TestWebSocketRetirementDoesNotInjectCloseIntoCorruptedFrames(t *testing.T) {
	t.Run("frontend", func(t *testing.T) {
		stream := &websocketBufferStream{}
		tunnel := &extendedTunnel{front: stream, changed: make(chan struct{}, 1)}
		corrupt := []byte{0x81, 0x80, 1, 2, 3, 4}
		if _, err := tunnel.writeFront(corrupt); err != nil {
			t.Fatal(err)
		}
		tunnel.closing, tunnel.closeCode = true, WSClosePolicyViolation
		if _, err := tunnel.writeFront([]byte("opaque")); err != nil {
			t.Fatal(err)
		}
		want := append(corrupt, []byte("opaque")...)
		if !bytes.Equal(stream.Bytes(), want) || tunnel.frontCloseSent {
			t.Fatalf("corrupted frontend output=%x close sent=%t", stream.Bytes(), tunnel.frontCloseSent)
		}
	})
	t.Run("backend", func(t *testing.T) {
		stream := &websocketBufferStream{}
		back := &upgradedBackConn{ReadWriteCloser: stream, isWebSocket: true}
		corrupt := []byte{0x81, 0}
		if _, err := back.Write(corrupt); err != nil {
			t.Fatal(err)
		}
		back.sendMaskedClose(WSClosePolicyViolation, time.Now().Add(time.Second))
		if _, err := back.Write([]byte("opaque")); err != nil {
			t.Fatal(err)
		}
		want := append(corrupt, []byte("opaque")...)
		if !bytes.Equal(stream.Bytes(), want) || back.closeSent.Load() {
			t.Fatalf("corrupted backend output=%x close sent=%t", stream.Bytes(), back.closeSent.Load())
		}
	})
}

func TestWebSocketRetirementReportsShortAndFailedWrites(t *testing.T) {
	failure := errors.New("write failed")
	for _, tt := range []struct {
		name     string
		short    bool
		writeErr error
		wantErr  error
	}{
		{name: "short", short: true, wantErr: io.ErrShortWrite},
		{name: "failed", writeErr: failure, wantErr: failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			frontStream := &websocketBufferStream{short: tt.short, writeErr: tt.writeErr}
			tunnel := &extendedTunnel{front: frontStream, closeCode: WSClosePolicyViolation, changed: make(chan struct{}, 1)}
			if err := tunnel.sendFrontCloseLocked(); !errors.Is(err, tt.wantErr) || tunnel.frontCloseSent {
				t.Errorf("frontend Close error=%v sent=%t", err, tunnel.frontCloseSent)
			}
			backStream := &websocketBufferStream{short: tt.short, writeErr: tt.writeErr}
			back := &upgradedBackConn{ReadWriteCloser: backStream, closeCode: WSClosePolicyViolation, closePending: true}
			if err := back.writeMaskedCloseLocked(); !errors.Is(err, tt.wantErr) || back.closeSent.Load() || !back.closePending {
				t.Errorf("backend Close error=%v sent=%t pending=%t", err, back.closeSent.Load(), back.closePending)
			}
		})
	}

	t.Run("partial frontend frame", func(t *testing.T) {
		stream := &websocketBufferStream{}
		tunnel := &extendedTunnel{front: stream, changed: make(chan struct{}, 1)}
		if _, err := tunnel.writeFront([]byte{0x82, 2, 'a'}); err != nil {
			t.Fatal(err)
		}
		tunnel.closing, stream.short = true, true
		if _, err := tunnel.writeFront([]byte{'b'}); !errors.Is(err, io.ErrShortWrite) || tunnel.frontCloseSent {
			t.Fatalf("partial frontend error=%v close sent=%t", err, tunnel.frontCloseSent)
		}
	})
	t.Run("partial backend frame", func(t *testing.T) {
		stream := &websocketBufferStream{}
		back := &upgradedBackConn{ReadWriteCloser: stream, isWebSocket: true}
		if _, err := back.Write([]byte{0x82, 0x82, 1, 2, 3, 4, 'a' ^ 1}); err != nil {
			t.Fatal(err)
		}
		back.sendMaskedClose(WSClosePolicyViolation, time.Now().Add(time.Second))
		stream.short = true
		if _, err := back.Write([]byte{'b' ^ 2}); !errors.Is(err, io.ErrShortWrite) || back.closeSent.Load() {
			t.Fatalf("partial backend error=%v close sent=%t", err, back.closeSent.Load())
		}
	})
}

func TestExtendedTunnelRetirementCompletesWebSocketCloseHandshake(t *testing.T) {
	reg := NewExchangeRegistry()
	reg.Grace = 2 * time.Second
	reg.Bound = 3 * time.Second
	reg.SetInitialGeneration(1)

	clientPeer, proxyClientSide := net.Pipe()
	defer clientPeer.Close()
	upstreamPeer, proxyBackSide := net.Pipe()
	defer upstreamPeer.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodConnect, "http://example.test/ws", nil)
	req.Proto = "HTTP/2.0"
	req.ProtoMajor = 2
	req = req.WithContext(WithExchangeRegistry(req.Context(), reg, 1))
	_, admitted, ex, release := reg.Admit(httptest.NewRecorder(), req, EntryCoord{
		GenID: 1, ListenAddr: "127.0.0.1:8080", RouteID: "extended-ws",
	})
	back := AttachUpgradedBackend(admitted.Context(), proxyBackSide, http.Header{"Upgrade": {"websocket"}})
	if again := AttachUpgradedBackend(admitted.Context(), back, http.Header{"Upgrade": {"websocket"}}); again != back {
		t.Fatal("attaching an already-owned backend changed its wrapper")
	}
	front := AttachExtendedStream(admitted.Context(), proxyClientSide, back)
	copyDone := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(back, front)
		copyDone <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(front, back)
		copyDone <- struct{}{}
	}()

	ex.armRetirement(time.Now().Add(2*time.Second), time.Now().Add(3*time.Second), WSClosePolicyViolation, false)
	if err := clientPeer.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := upstreamPeer.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	clientClose := make([]byte, 4)
	if _, err := io.ReadFull(clientPeer, clientClose); err != nil {
		t.Fatalf("read Jul close frame: %v", err)
	}
	if clientClose[0] != 0x88 || clientClose[1] != 2 || binary.BigEndian.Uint16(clientClose[2:]) != WSClosePolicyViolation {
		t.Fatalf("client-facing Close frame=%x", clientClose)
	}
	upstreamClose := make([]byte, 8)
	if _, err := io.ReadFull(upstreamPeer, upstreamClose); err != nil {
		t.Fatalf("read upstream masked Close frame: %v", err)
	}
	if upstreamClose[0] != 0x88 || upstreamClose[1]&0x80 == 0 {
		t.Fatalf("upstream Close frame is not masked: %x", upstreamClose)
	}

	if _, err := clientPeer.Write(buildMaskedCloseFrame(WSCloseGoingAway)); err != nil {
		t.Fatalf("send client Close frame: %v", err)
	}
	clientReplyAtBackend := make([]byte, 8)
	if _, err := io.ReadFull(upstreamPeer, clientReplyAtBackend); err != nil {
		t.Fatalf("read relayed client Close frame: %v", err)
	}
	if _, err := upstreamPeer.Write(buildUnmaskedCloseFrame(WSCloseGoingAway)); err != nil {
		t.Fatalf("send backend Close frame: %v", err)
	}
	backendReplyAtClient := make([]byte, 4)
	if _, err := io.ReadFull(clientPeer, backendReplyAtClient); err != nil {
		t.Fatalf("read relayed backend Close frame: %v", err)
	}
	if backendReplyAtClient[0] != 0x88 || backendReplyAtClient[1] != 2 {
		t.Fatalf("client received invalid backend Close frame: %x", backendReplyAtClient)
	}

	for range 2 {
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
			t.Fatal("tunnel copy did not stop after the close handshake")
		}
	}
	if handshake := ex.upgradedHandshake.Load(); handshake == nil || !handshake.Load() {
		t.Fatal("completed WebSocket close handshake was not recorded")
	}
	release()
}
