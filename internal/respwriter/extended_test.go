// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package respwriter

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
