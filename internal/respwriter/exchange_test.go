// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package respwriter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWSFrameTrackerAndCloseFrames(t *testing.T) {
	var s2c wsFrameTracker
	if !s2c.atFrameBoundary() {
		t.Fatal("expected initial tracker to be at frame boundary")
	}

	// 1. Feed a 5-byte unmasked text frame in two chunks: [0x81, 0x05, "he"] then ["llo"].
	part1 := []byte{0x81, 0x05, 'h', 'e'}
	if got := s2c.bytesToNextBoundary(part1); got != 0 {
		t.Fatalf("at boundary bytesToNextBoundary = %d, want 0", got)
	}
	s2c.feed(part1)
	if s2c.atFrameBoundary() {
		t.Fatal("expected tracker to be mid-frame after partial payload")
	}
	part2 := []byte("lloEXTRA")
	if got := s2c.bytesToNextBoundary(part2); got != 3 {
		t.Fatalf("bytesToNextBoundary = %d, want 3", got)
	}
	s2c.feed(part2[:3])
	if !s2c.atFrameBoundary() {
		t.Fatal("expected tracker to return to frame boundary after completing payload")
	}

	// 2. Feed a 16-bit extended-length (126) frame and a 64-bit extended-length (127) frame.
	ext126 := make([]byte, 4+130)
	ext126[0] = 0x82
	ext126[1] = 126
	binary.BigEndian.PutUint16(ext126[2:4], 130)
	s2c.feed(ext126[:3])
	if s2c.atFrameBoundary() {
		t.Fatal("expected mid-header to not be at boundary")
	}
	s2c.feed(ext126[3:])
	if !s2c.atFrameBoundary() {
		t.Fatal("expected boundary after 126-length frame")
	}

	ext127 := make([]byte, 10+8)
	ext127[0] = 0x82
	ext127[1] = 127
	binary.BigEndian.PutUint64(ext127[2:10], 8)
	s2c.feed(ext127)
	if !s2c.atFrameBoundary() {
		t.Fatal("expected boundary after 127-length frame")
	}

	// Zero-length Close frame (0x88, 0x00).
	var zeroClose wsFrameTracker
	zeroClose.feed([]byte{0x88, 0x00})
	if !zeroClose.sawCloseFrame || !zeroClose.atFrameBoundary() {
		t.Fatal("expected zero-length Close frame to mark sawCloseFrame")
	}

	// Fragmented control frame (FIN=0, opcode=8) is corrupted per RFC 6455 §5.5.
	var badCtrl wsFrameTracker
	badCtrl.feed([]byte{0x08, 0x00})
	if !badCtrl.corrupted {
		t.Fatal("expected fragmented control frame to mark tracker corrupted")
	}
	badCtrl.feed([]byte{0x81, 0x00}) // no-op when already corrupted

	// 64-bit length with MSB set is corrupted.
	var bad127 wsFrameTracker
	badHdr := make([]byte, 10)
	badHdr[0] = 0x82
	badHdr[1] = 127
	badHdr[2] = 0x80
	bad127.feed(badHdr)
	if !bad127.corrupted {
		t.Fatal("expected MSB-set 64-bit length to mark tracker corrupted")
	}

	// 3. Feed an unmasked Close frame and verify sawCloseFrame.
	unmaskedClose := buildUnmaskedCloseFrame(WSClosePolicyViolation)
	if len(unmaskedClose) != 4 || unmaskedClose[0] != 0x88 || unmaskedClose[1] != 0x02 {
		t.Fatalf("unexpected unmasked close frame: %x", unmaskedClose)
	}
	s2c.feed(unmaskedClose)
	if !s2c.sawCloseFrame || !s2c.atFrameBoundary() {
		t.Fatal("expected sawCloseFrame=true and atFrameBoundary=true")
	}

	// 4. Verify masked Close frame on client->server tracker.
	var c2s wsFrameTracker
	c2s.expectMasked = true
	maskedClose := buildMaskedCloseFrame(WSCloseGoingAway)
	if len(maskedClose) != 8 || maskedClose[0] != 0x88 || maskedClose[1] != 0x82 {
		t.Fatalf("unexpected masked close frame: %x", maskedClose)
	}
	// Verify masking key unmasks to 1001 (0x03e9).
	mask := maskedClose[2:6]
	unmasked := []byte{maskedClose[6] ^ mask[0], maskedClose[7] ^ mask[1]}
	if got := binary.BigEndian.Uint16(unmasked); got != WSCloseGoingAway {
		t.Fatalf("unmasked close code = %d, want %d", got, WSCloseGoingAway)
	}
	c2s.feed(maskedClose)
	if !c2s.sawCloseFrame || !c2s.atFrameBoundary() {
		t.Fatal("expected masked close frame to set sawCloseFrame=true")
	}

	// 5. Unmasked frame on expectMasked tracker marks corrupted.
	var badC2S wsFrameTracker
	badC2S.expectMasked = true
	badC2S.feed(unmaskedClose)
	if !badC2S.corrupted || badC2S.atFrameBoundary() {
		t.Fatal("expected unmasked frame on expectMasked tracker to mark corrupted")
	}
}

type hijackableRecorder struct {
	hdr  http.Header
	code int
	conn net.Conn
	brw  *bufio.ReadWriter
}

func newHijackableRecorder(conn net.Conn) *hijackableRecorder {
	var r io.Reader = bytes.NewReader(nil)
	w := io.Discard
	if conn != nil {
		r = conn
		w = conn
	}
	return &hijackableRecorder{
		hdr:  make(http.Header),
		conn: conn,
		brw:  bufio.NewReadWriter(bufio.NewReader(r), bufio.NewWriter(w)),
	}
}

func (h *hijackableRecorder) Header() http.Header         { return h.hdr }
func (h *hijackableRecorder) WriteHeader(code int)        { h.code = code }
func (h *hijackableRecorder) Write(p []byte) (int, error) { return h.brw.Write(p) }
func (h *hijackableRecorder) Flush()                      { _ = h.brw.Flush() }
func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, h.brw, nil
}

func TestExchangeRegistryAtomicAdmissionAndMultiGenRetirement(t *testing.T) {
	reg := NewExchangeRegistry()
	reg.Grace = 80 * time.Millisecond
	reg.Bound = 200 * time.Millisecond
	reg.SetInitialGeneration(1)

	// 1. Admit an exchange in Generation 1 on route "dash" (0, 0).
	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/", nil)
	rec1 := newHijackableRecorder(nil)
	_, req1Admitted, ex1, rel1 := reg.Admit(rec1, req1, EntryCoord{
		GenID:         1,
		ListenAddr:    "127.0.0.1:8080",
		ServerIndex:   0,
		LocationIndex: 0,
		RouteID:       "dash",
	})
	defer rel1()

	byCoord, byID := reg.SnapshotCounts()
	if byCoord[[2]int{0, 0}] != 1 || byID["dash"] != 1 || reg.ActiveCount() != 1 {
		t.Fatalf("unexpected snapshot counts: byCoord=%v byID=%v active=%d", byCoord, byID, reg.ActiveCount())
	}

	// 2. Generation 2: uncertain change with KeepOpen=true moves "dash" to (1, 2).
	// ex1 must survive and advance its coordinates to (gen 2, srv 1, loc 2).
	waitGen2 := reg.PublishGeneration(1, 2, []EntryTransition{{
		FromServerIndex:   0,
		FromLocationIndex: 0,
		FromRouteID:       "dash",
		ToServerIndex:     1,
		ToLocationIndex:   2,
		ToRouteID:         "dash",
		Retire:            false,
		KeepOpen:          true,
		DisplayLabel:      "dash",
	}})
	resGen2 := waitGen2()
	if len(resGen2) != 1 || !resGen2[0].KeepOpen || resGen2[0].EndedExchanges != 0 {
		t.Fatalf("unexpected Gen 2 keep_open result: %+v", resGen2)
	}
	if ex1.IsRetired() || req1Admitted.Context().Err() != nil {
		t.Fatal("ex1 must remain open when KeepOpen=true in Gen 2")
	}

	// 3. Generation 3 narrows "dash" at (1, 2): must retire ex1 (originally opened in Gen 1!).
	go func() {
		<-req1Admitted.Context().Done()
		rel1()
	}()
	waitGen3 := reg.PublishGeneration(2, 3, []EntryTransition{{
		FromServerIndex:   1,
		FromLocationIndex: 2,
		FromRouteID:       "dash",
		ToServerIndex:     1,
		ToLocationIndex:   2,
		ToRouteID:         "dash",
		Retire:            true,
		Mandatory:         true,
		DisplayLabel:      "dash",
	}})
	resGen3 := waitGen3()
	if len(resGen3) != 1 || resGen3[0].EndedExchanges != 1 || !resGen3[0].CompletedWithinBound {
		t.Fatalf("expected Gen 3 to retire Gen 1 exchange saved by KeepOpen, got %+v", resGen3)
	}

	// 4. Atomic admission racing with Publish: a request that acquired Gen 2
	// before Gen 3 was published and calls Admit after Gen 3 retired "dash"
	// must immediately inherit Gen 3's retirement!
	reqLate, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/", nil)
	_, reqLateAdmitted, exLate, relLate := reg.Admit(newHijackableRecorder(nil), reqLate, EntryCoord{
		GenID:         2,
		ListenAddr:    "127.0.0.1:8080",
		ServerIndex:   1,
		LocationIndex: 2,
		RouteID:       "dash",
	})
	defer relLate()
	if !exLate.IsRetired() || reqLateAdmitted.Context().Err() == nil {
		t.Fatal("late admission from retired generation must immediately inherit retirement")
	}
}

func TestUpgradedSessionWebSocketHandshakeVsEOFAndOpaqueFallback(t *testing.T) {
	t.Run("RFC 6455 completed Close handshake counts as graceful and masks upstream Close", func(t *testing.T) {
		reg := NewExchangeRegistry()
		reg.Grace = 300 * time.Millisecond
		reg.Bound = 600 * time.Millisecond
		reg.SetInitialGeneration(1)

		clientPeer, proxyClientSide := net.Pipe()
		defer clientPeer.Close()
		upstreamPeer, proxyBackSide := net.Pipe()
		defer upstreamPeer.Close()

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/ws", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		ctx := WithExchangeRegistry(req.Context(), reg, 1)
		req = req.WithContext(ctx)

		rec := newHijackableRecorder(proxyClientSide)
		w, reqAdmitted, _, release := reg.Admit(rec, req, EntryCoord{
			GenID:         1,
			ListenAddr:    "127.0.0.1:8080",
			ServerIndex:   0,
			LocationIndex: 0,
			RouteID:       "ws-route",
		})
		resHdr := http.Header{"Upgrade": []string{"websocket"}}
		backWrapped := AttachUpgradedBackend(reqAdmitted.Context(), proxyBackSide, resHdr)

		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Fatalf("Hijack: %v", err)
		}

		// Simulate ReverseProxy writing 101 headers and running bidirectional copy.
		var pumpWg sync.WaitGroup
		pumpWg.Add(1)
		go func() {
			defer pumpWg.Done()
			defer release()
			_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			_ = brw.Flush()
			errc := make(chan error, 2)
			go func() {
				_, cErr := io.Copy(backWrapped, conn)
				errc <- cErr
			}()
			go func() {
				_, cErr := io.Copy(conn, backWrapped)
				errc <- cErr
			}()
			<-errc
		}()

		// Read 101 response headers on clientPeer.
		clientBR := bufio.NewReader(clientPeer)
		for {
			line, rErr := clientBR.ReadString('\n')
			if rErr != nil {
				t.Fatalf("read 101 header: %v", rErr)
			}
			if line == "\r\n" {
				break
			}
		}

		// Drain upstreamPeer in background and record the masked Close frame Jul sends.
		upstreamCloseCh := make(chan []byte, 1)
		go func() {
			buf := make([]byte, 32)
			n, _ := upstreamPeer.Read(buf)
			if n > 0 {
				upstreamCloseCh <- buf[:n]
			}
		}()

		// Client peer waits for Jul's unmasked 1008 Close frame, then replies with a masked Close frame.
		clientGotCloseCh := make(chan uint16, 1)
		go func() {
			var hdr [4]byte
			if _, rErr := io.ReadFull(clientBR, hdr[:]); rErr != nil {
				return
			}
			if hdr[0] == 0x88 && hdr[1] == 0x02 {
				code := binary.BigEndian.Uint16(hdr[2:4])
				clientGotCloseCh <- code
				// Reply with a masked Close frame (RFC 6455 §5.1).
				_, _ = clientPeer.Write(buildMaskedCloseFrame(code))
			}
		}()

		waitRetire := reg.PublishGeneration(1, 2, []EntryTransition{{
			FromServerIndex:   0,
			FromLocationIndex: 0,
			FromRouteID:       "ws-route",
			ToServerIndex:     0,
			ToLocationIndex:   0,
			ToRouteID:         "ws-route",
			Retire:            true,
			DisplayLabel:      "ws-route",
		}})
		results := waitRetire()
		pumpWg.Wait()

		select {
		case gotCode := <-clientGotCloseCh:
			if gotCode != WSClosePolicyViolation {
				t.Fatalf("client received Close code %d, want %d (1008)", gotCode, WSClosePolicyViolation)
			}
		case <-time.After(time.Second):
			t.Fatal("client did not receive unmasked Close frame from Jul")
		}

		select {
		case upBytes := <-upstreamCloseCh:
			if len(upBytes) < 8 || upBytes[0] != 0x88 || (upBytes[1]&0x80) == 0 {
				t.Fatalf("expected masked Close frame on upstream leg, got %x", upBytes)
			}
		case <-time.After(time.Second):
			t.Fatal("upstream did not receive masked Close frame from Jul")
		}

		if len(results) != 1 || results[0].GracefulCount != 1 || results[0].ForcedCount != 0 || !results[0].CompletedWithinBound {
			t.Fatalf("expected graceful_count=1 forced_count=0, got %+v", results)
		}
	})

	t.Run("EOF without reciprocal Close frame counts as forced (Point 3)", func(t *testing.T) {
		reg := NewExchangeRegistry()
		reg.Grace = 200 * time.Millisecond
		reg.Bound = 400 * time.Millisecond
		reg.SetInitialGeneration(1)

		clientPeer, proxyClientSide := net.Pipe()
		defer clientPeer.Close()
		upstreamPeer, proxyBackSide := net.Pipe()
		defer upstreamPeer.Close()
		go func() { _, _ = io.Copy(io.Discard, upstreamPeer) }()

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/ws", nil)
		req.Header.Set("Upgrade", "websocket")
		rec := newHijackableRecorder(proxyClientSide)
		w, reqAdmitted, _, release := reg.Admit(rec, req, EntryCoord{GenID: 1, ListenAddr: "127.0.0.1:8080", RouteID: "ws-eof"})
		backWrapped := AttachUpgradedBackend(reqAdmitted.Context(), proxyBackSide, http.Header{"Upgrade": []string{"websocket"}})
		conn, brw, _ := http.NewResponseController(w).Hijack()

		go func() {
			defer release()
			_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n")
			_ = brw.Flush()
			_, _ = io.Copy(backWrapped, conn)
		}()

		clientBR := bufio.NewReader(clientPeer)
		for {
			line, err := clientBR.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
		}
		// When Jul sends Close, client closes TCP raw (EOF) without sending a Close frame!
		go func() {
			var hdr [4]byte
			_, _ = io.ReadFull(clientBR, hdr[:])
			_ = clientPeer.Close()
		}()

		waitRetire := reg.PublishGeneration(1, 2, []EntryTransition{{
			FromRouteID: "ws-eof",
			ToRouteID:   "ws-eof",
			Retire:      true,
		}})
		results := waitRetire()
		if len(results) != 1 || results[0].GracefulCount != 0 || results[0].ForcedCount != 1 {
			t.Fatalf("Point 3: EOF without reciprocal Close frame must count as forced_count=1 graceful_count=0, got %+v", results)
		}
	})

	t.Run("Non-WebSocket opaque 101 upgrade closes immediately as forced without fabricating WS frame", func(t *testing.T) {
		reg := NewExchangeRegistry()
		reg.Grace = 150 * time.Millisecond
		reg.Bound = 300 * time.Millisecond
		reg.SetInitialGeneration(1)

		clientPeer, proxyClientSide := net.Pipe()
		defer clientPeer.Close()
		upstreamPeer, proxyBackSide := net.Pipe()
		defer upstreamPeer.Close()

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/opaque", nil)
		req.Header.Set("Upgrade", "custom-proto/1.0")
		rec := newHijackableRecorder(proxyClientSide)
		w, reqAdmitted, _, release := reg.Admit(rec, req, EntryCoord{GenID: 1, ListenAddr: "127.0.0.1:8080", RouteID: "opaque"})
		backWrapped := AttachUpgradedBackend(reqAdmitted.Context(), proxyBackSide, http.Header{"Upgrade": []string{"custom-proto/1.0"}})
		conn, brw, _ := http.NewResponseController(w).Hijack()

		go func() {
			defer release()
			_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: custom-proto/1.0\r\n\r\n")
			_ = brw.Flush()
			_, _ = io.Copy(backWrapped, conn)
		}()

		var clientRead bytes.Buffer
		doneRead := make(chan struct{})
		go func() {
			_, _ = io.Copy(&clientRead, clientPeer)
			close(doneRead)
		}()

		time.Sleep(20 * time.Millisecond)
		waitRetire := reg.PublishGeneration(1, 2, []EntryTransition{{
			FromRouteID: "opaque",
			ToRouteID:   "opaque",
			Retire:      true,
		}})
		results := waitRetire()
		<-doneRead

		if bytes.Contains(clientRead.Bytes(), []byte{0x88}) || !strings.HasSuffix(clientRead.String(), "\r\n\r\n") {
			t.Fatalf("opaque upgrade must not receive WebSocket Close frame bytes, got %x", clientRead.Bytes())
		}
		if len(results) != 1 || results[0].GracefulCount != 0 || results[0].ForcedCount != 1 {
			t.Fatalf("opaque upgrade must be counted as forced_count=1, got %+v", results)
		}
	})
}

func TestExchangeRegistryDrainSnapshotAndEdgePaths(t *testing.T) {
	// 1. Nil receiver safety and context helpers.
	var nilReg *ExchangeRegistry
	nilReg.SetInitialGeneration(1)
	if c, r := nilReg.SnapshotCounts(); len(c) != 0 || len(r) != 0 {
		t.Fatal("expected empty counts on nil registry")
	}
	if nilReg.LastRetirementResults() != nil || nilReg.ActiveCount() != 0 {
		t.Fatal("expected nil/0 on nil registry")
	}
	if res := nilReg.PublishGeneration(1, 2, nil)(); res != nil {
		t.Fatal("expected nil results on nil registry")
	}
	nilReg.DrainAddr(context.Background(), "127.0.0.1:8080")
	nilReg.DrainAll(context.Background())

	var nilEx *ActiveExchange
	if nilEx.IsRetired() || nilEx.ShouldAbortStream() || nilEx.isUpgraded() || nilEx.hasWrittenHeader() {
		t.Fatal("expected false on nil ActiveExchange")
	}
	nilEx.armRetirement(time.Now(), time.Now(), WSCloseGoingAway, true, nil)
	nilEx.finishRequest()
	nilEx.markClosed()

	ctx := context.Background()
	if WithClientCertVerifier(ctx, nil) != ctx {
		t.Fatal("expected nil verifier to return original ctx")
	}
	vFn := func(_ *tls.ConnectionState, _ bool) (bool, error) { return true, nil }
	ctxV := WithClientCertVerifier(ctx, vFn)
	if ClientCertVerifierFrom(ctxV) == nil {
		t.Fatal("expected non-nil verifier from ctx")
	}
	if WithExchangeRegistry(ctx, nil, 1) != ctx {
		t.Fatal("expected nil registry to return original ctx")
	}
	if ActiveExchangeFrom(ctx) != nil {
		t.Fatal("expected nil ActiveExchange from bare ctx")
	}

	// 2. SnapshotCounts, ActiveCount, DrainAddr, DrainAll, removed entry, and shuttingDown admission.
	reg := NewExchangeRegistry()
	reg.Grace = 40 * time.Millisecond
	reg.Bound = 80 * time.Millisecond
	reg.SetInitialGeneration(1)

	ctxReg := WithExchangeRegistry(context.Background(), reg, 1)
	if gotReg, genID := ExchangeRegistryFrom(ctxReg); gotReg != reg || genID != 1 {
		t.Fatalf("unexpected ExchangeRegistryFrom: %v, %d", gotReg, genID)
	}

	req1, _ := http.NewRequestWithContext(ctxReg, http.MethodGet, "http://example.com/a", nil)
	w1, req1Admitted, ex1, rel1 := reg.Admit(newHijackableRecorder(nil), req1, EntryCoord{
		GenID:         1,
		ListenAddr:    "127.0.0.1:9001",
		ServerIndex:   0,
		LocationIndex: 1,
		RouteID:       "r1",
	})
	if ActiveExchangeFrom(req1Admitted.Context()) != ex1 || ActiveExchangeFrom(ctxReg) != ex1 {
		t.Fatal("expected ActiveExchangeFrom to locate ex1 from both request and slot ctx")
	}
	if reg.ActiveCount() != 1 {
		t.Fatalf("expected ActiveCount=1, got %d", reg.ActiveCount())
	}
	byCoord, byRoute := reg.SnapshotCounts()
	if byCoord[[2]int{0, 1}] != 1 || byRoute["r1"] != 1 {
		t.Fatalf("unexpected SnapshotCounts: %v, %v", byCoord, byRoute)
	}

	// Flush and normal WriteHeader/Write before retirement should delegate cleanly.
	w1.WriteHeader(http.StatusAccepted)
	if _, err := w1.Write([]byte("streaming-chunk")); err != nil {
		t.Fatalf("Write before retirement: %v", err)
	}
	if fl, ok := w1.(http.Flusher); ok {
		fl.Flush()
	}

	// Retire by Removed=true in PublishGeneration.
	go func() {
		<-req1Admitted.Context().Done()
		// Attempting WriteHeader / Write / Flush after retirement sets ShouldAbortStream.
		w1.WriteHeader(http.StatusOK)
		if _, err := w1.Write([]byte("blocked")); err == nil {
			t.Errorf("expected Write after retirement to fail")
		}
		if fl, ok := w1.(http.Flusher); ok {
			fl.Flush()
		}
		rel1()
	}()
	waitRem := reg.PublishGeneration(0, 2, []EntryTransition{{
		FromServerIndex:   0,
		FromLocationIndex: 1,
		FromRouteID:       "r1",
		Removed:           true,
		Retire:            true,
		Mandatory:         true,
		DisplayLabel:      "removed-r1",
	}})
	remRes := waitRem()
	if len(remRes) != 1 || !remRes[0].Removed || remRes[0].EndedExchanges != 1 || !ex1.ShouldAbortStream() {
		t.Fatalf("unexpected removed retirement result: %+v", remRes)
	}
	if len(reg.LastRetirementResults()) != 1 {
		t.Fatal("expected LastRetirementResults to retain last result")
	}

	// 3. DrainAddr with a short deadline and subsequent admission on closedAddr.
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/b", nil)
	_, req2Admitted, _, rel2 := reg.Admit(newHijackableRecorder(nil), req2, EntryCoord{
		GenID:      2,
		ListenAddr: "127.0.0.1:9002",
		RouteID:    "r2",
	})
	go func() {
		<-req2Admitted.Context().Done()
		rel2()
	}()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer drainCancel()
	reg.DrainAddr(drainCtx, "127.0.0.1:9002")

	// Admitting on an already-closed address immediately retires the exchange.
	reqClosedAddr, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/b2", nil)
	_, _, exClosedAddr, relClosedAddr := reg.Admit(newHijackableRecorder(nil), reqClosedAddr, EntryCoord{
		GenID:      2,
		ListenAddr: "127.0.0.1:9002",
	})
	if !exClosedAddr.IsRetired() {
		t.Fatal("expected admission on closedAddr to be immediately retired")
	}
	relClosedAddr()

	// 4. DrainAll with an already-expired context.
	req3, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/c", nil)
	_, req3Admitted, _, rel3 := reg.Admit(newHijackableRecorder(nil), req3, EntryCoord{
		GenID:      2,
		ListenAddr: "127.0.0.1:9003",
	})
	go func() {
		<-req3Admitted.Context().Done()
		rel3()
	}()
	expCtx, expCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expCancel()
	reg.DrainAll(expCtx)
}

func TestUpgradedSessionPreReadMidFrameAndSlowPeerFallback(t *testing.T) {
	// 1. Pre-read buffered bytes in bufio.Reader and mid-frame server->client write when closing.
	reg := NewExchangeRegistry()
	reg.Grace = 60 * time.Millisecond
	reg.Bound = 120 * time.Millisecond
	reg.SetInitialGeneration(1)

	clientPeer, proxyClientSide := net.Pipe()
	defer clientPeer.Close()
	upstreamPeer, proxyBackSide := net.Pipe()
	defer upstreamPeer.Close()

	rec := newHijackableRecorder(proxyClientSide)
	// Pre-populate rec.brw.Reader with a masked client ping frame so newUpgradedSession seeds preRead.
	go func() {
		// Masked Close frame from client pre-buffered before hijack.
		_, _ = clientPeer.Write(buildMaskedCloseFrame(WSCloseGoingAway))
	}()
	buf := make([]byte, 1)
	_, _ = rec.brw.Peek(1)
	_ = buf

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/ws-mid", nil)
	req.Header.Set("Upgrade", "websocket")
	w, reqAdmitted, ex, release := reg.Admit(rec, req, EntryCoord{GenID: 1, ListenAddr: "127.0.0.1:8080", RouteID: "ws-mid"})
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Fatalf("Hijack: %v", err)
	}
	// Attach backend AFTER Hijack to exercise the post-hijack attachBackend branch.
	backWrapped := AttachUpgradedBackend(reqAdmitted.Context(), proxyBackSide, http.Header{"Upgrade": []string{"websocket"}})

	// Drain clientPeer in background.
	go func() {
		_, _ = io.Copy(io.Discard, clientPeer)
	}()
	go func() {
		_, _ = io.Copy(io.Discard, upstreamPeer)
	}()

	// Write 101 header in two separate chunks to exercise multi-chunk header detection.
	_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	_ = brw.Flush()
	_, _ = brw.WriteString("Upgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	_ = brw.Flush()

	// Write the first 2 bytes of a 6-byte server->client text frame (FIN=1, opcode=1, len=4, payload="abcd")
	// so s2cTracker is mid-frame!
	_, _ = conn.Write([]byte{0x81, 0x04, 'a'})

	// Read the pre-buffered client frame through conn.Read and write to backWrapped.
	readBuf := make([]byte, 16)
	if n, rErr := conn.Read(readBuf); rErr == nil && n > 0 {
		_, _ = backWrapped.Write(readBuf[:n])
	}

	// Initiate close while s2cTracker is mid-frame, then write the remaining 3 bytes + extra frame.
	ex.armRetirement(time.Now().Add(50*time.Millisecond), time.Now().Add(100*time.Millisecond), WSClosePolicyViolation, true, nil)
	_, _ = conn.Write([]byte{'b', 'c', 'd', 0x81, 0x01, 'z'})
	// Post-close writes to client and backend return cleanly/closed.
	_, _ = backWrapped.Write([]byte{0x81, 0x80, 1, 2, 3, 4})
	_, _ = backWrapped.Read(make([]byte, 8))
	_ = backWrapped.Close()
	_ = conn.Close()
	release()

	// 2. Mid-frame stall timeout in trySendClientCloseAtBoundary (cannot reach boundary within grace)
	// and H1 raw connection force-close after grace window expires on a non-mandatory exchange.
	h1A, h1B := net.Pipe()
	defer h1A.Close()
	defer h1B.Close()
	ctxH1 := SendTimeoutContext(context.Background(), h1A)
	reqH1, _ := http.NewRequestWithContext(ctxH1, http.MethodGet, "http://example.com/slow", nil)
	_, _, exH1, relH1 := reg.Admit(newHijackableRecorder(h1A), reqH1, EntryCoord{
		GenID:      1,
		ListenAddr: "127.0.0.1:8080",
		RouteID:    "slow-h1",
	})
	// Arm non-mandatory retirement so the background 40ms grace timer fires triggerStreamAbort(true).
	exH1.armRetirement(time.Now().Add(20*time.Millisecond), time.Now().Add(60*time.Millisecond), WSClosePolicyViolation, false, nil)
	time.Sleep(35 * time.Millisecond)
	if !exH1.IsRetired() {
		t.Fatal("expected grace timer to abort H1 exchange")
	}
	relH1()

	// 3. Stalled mid-frame WebSocket where boundary is never reached within grace.
	cStallPeer, cStallProxy := net.Pipe()
	defer cStallPeer.Close()
	go func() { _, _ = io.Copy(io.Discard, cStallPeer) }()
	reqStall, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/ws-stall", nil)
	reqStall.Header.Set("Upgrade", "websocket")
	wStall, _, exStall, relStall := reg.Admit(newHijackableRecorder(cStallProxy), reqStall, EntryCoord{
		GenID:      1,
		ListenAddr: "127.0.0.1:8080",
		RouteID:    "ws-stall",
	})
	connStall, brwStall, _ := http.NewResponseController(wStall).Hijack()
	_, _ = brwStall.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n")
	_ = brwStall.Flush()
	// Start a 10-byte frame and send only 1 byte of payload, then stall!
	_, _ = connStall.Write([]byte{0x81, 0x0a, 'x'})
	exStall.armRetirement(time.Now().Add(20*time.Millisecond), time.Now().Add(50*time.Millisecond), 0, true, nil)
	relStall()
}

func TestAudit653ReaddedListenerAcceptsRequests(t *testing.T) {
	reg := NewExchangeRegistry()
	reg.SetInitialGeneration(1)
	reg.DrainAddr(context.Background(), "127.0.0.1:8080")
	reg.PublishGeneration(1, 2, nil)()
	_, req, ex, release := reg.Admit(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/", nil), EntryCoord{GenID: 2, ListenAddr: "127.0.0.1:8080"})
	defer release()
	if ex.IsRetired() || req.Context().Err() != nil {
		t.Fatal("fresh request on re-added listener is immediately retired")
	}
	// Stale request from the removed listener generation (GenID: 1) remains retired.
	_, staleReq, staleEx, staleRelease := reg.Admit(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/", nil), EntryCoord{GenID: 1, ListenAddr: "127.0.0.1:8080"})
	defer staleRelease()
	if !staleEx.IsRetired() || staleReq.Context().Err() == nil {
		t.Fatal("stale request from removed listener lifetime must still be retired")
	}
}

func TestAudit653AdmissionBeyondHistoryWindow(t *testing.T) {
	reg := NewExchangeRegistry()
	reg.Grace = time.Millisecond
	reg.Bound = 2 * time.Millisecond
	reg.SetInitialGeneration(1)
	reg.PublishGeneration(1, 2, []EntryTransition{{Retire: true, Mandatory: true}})()
	for gen := uint64(2); gen < 35; gen++ {
		reg.PublishGeneration(gen, gen+1, []EntryTransition{{}})()
	}
	time.Sleep(5 * time.Millisecond)
	_, req, ex, release := reg.Admit(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/", nil), EntryCoord{GenID: 1})
	defer release()
	if !ex.IsRetired() || req.Context().Err() == nil {
		t.Fatal("request using withdrawn old generation escaped retirement after history truncation")
	}
}

func TestAudit653IncompleteRetirementDoesNotClaimComplete(t *testing.T) {
	reg := NewExchangeRegistry()
	reg.Grace = 80 * time.Millisecond
	reg.Bound = 160 * time.Millisecond
	reg.SetInitialGeneration(1)
	_, _, _, release := reg.Admit(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/", nil), EntryCoord{GenID: 1})
	defer release()
	start := time.Now()
	results := reg.PublishGeneration(1, 2, []EntryTransition{{Retire: true}})()
	t.Logf("terminal after %v, active=%d, result=%+v", time.Since(start), reg.ActiveCount(), results)
	if len(results) != 1 {
		t.Fatalf("results=%+v", results)
	}
	if results[0].Claim == "complete_withdrawal" && (!results[0].CompletedWithinBound || reg.ActiveCount() != 0) {
		t.Fatal("terminal result claims complete withdrawal while the exchange is still open")
	}
	if results[0].EndedExchanges != 0 || results[0].CompletedWithinBound {
		t.Fatalf("expected EndedExchanges=0 and CompletedWithinBound=false while exchange is still open, got %+v", results[0])
	}

	// Late admission that races with PublishGeneration and closes within bound must be
	// waited on by waitFn and counted in EndedExchanges with Claim == "complete_withdrawal".
	reg2 := NewExchangeRegistry()
	reg2.Grace = 80 * time.Millisecond
	reg2.Bound = 200 * time.Millisecond
	reg2.SetInitialGeneration(1)
	wait2 := reg2.PublishGeneration(1, 2, []EntryTransition{{
		FromRouteID: "late-r",
		ToRouteID:   "late-r",
		Retire:      true,
		Mandatory:   true,
	}})
	_, lateReq, lateEx, lateRelease := reg2.Admit(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/", nil), EntryCoord{
		GenID:   1,
		RouteID: "late-r",
	})
	if !lateEx.IsRetired() || lateReq.Context().Err() == nil {
		t.Fatal("late admission must immediately inherit retirement")
	}
	go func() {
		time.Sleep(15 * time.Millisecond)
		lateRelease()
	}()
	res2 := wait2()
	if len(res2) != 1 || res2[0].EndedExchanges != 1 || !res2[0].CompletedWithinBound || res2[0].Claim != "complete_withdrawal" {
		t.Fatalf("late admission must be waited on and counted in outcome: %+v", res2)
	}
}

func TestReaudit653OlderCompletionCannotOverwriteLatestPublish(t *testing.T) {
	reg := NewExchangeRegistry()
	reg.Grace = 20 * time.Millisecond
	reg.Bound = 60 * time.Millisecond
	reg.SetInitialGeneration(1)
	_, _, _, release := reg.Admit(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/", nil), EntryCoord{GenID: 1, RouteID: "route"})
	defer release()
	olderWait := reg.PublishGeneration(1, 2, []EntryTransition{{FromRouteID: "route", ToRouteID: "route", Retire: true}})
	reg.PublishGeneration(2, 3, []EntryTransition{{FromRouteID: "route", ToRouteID: "route"}})()
	if current := reg.LastRetirementResults(); len(current) != 0 {
		t.Fatalf("latest metadata-only publish should have no retirement: %+v", current)
	}
	release()
	_ = olderWait()
	if current := reg.LastRetirementResults(); len(current) != 0 {
		t.Fatalf("older asynchronous waiter overwrote latest publish result: %+v", current)
	}
}

func TestReaudit653NewGenerationNotRetiredBeforeRegistryPublication(t *testing.T) {
	reg := NewExchangeRegistry()
	reg.SetInitialGeneration(1)
	// ReloadPlan.Publish stores the new handler generation before calling PublishGeneration.
	_, req, ex, release := reg.Admit(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/", nil), EntryCoord{GenID: 2, RouteID: "route"})
	defer release()
	if ex.IsRetired() || req.Context().Err() != nil {
		t.Fatal("request using newly stored handler generation is spuriously aborted before matching registry publication")
	}
}

func TestReaudit653LateAdmissionImmediatelyInvalidatesCompletionStatus(t *testing.T) {
	reg := NewExchangeRegistry()
	reg.SetInitialGeneration(1)
	reg.PublishGeneration(1, 2, []EntryTransition{{FromRouteID: "route", ToRouteID: "route", Retire: true, Mandatory: true}})()
	_, _, _, release := reg.Admit(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/", nil), EntryCoord{GenID: 1, RouteID: "route"})
	defer release()
	got := reg.LastRetirementResults()
	if len(got) != 1 {
		t.Fatalf("results=%+v", got)
	}
	if got[0].CompletedWithinBound || got[0].Claim == "complete_withdrawal" {
		t.Fatalf("late active admission absent from completion status: active=%d result=%+v", reg.ActiveCount(), got[0])
	}
}

func TestThird653SamePublishRecomputeCannotRestoreStaleCompletion(t *testing.T) {
	for attempt := 0; attempt < 100; attempt++ {
		reg := NewExchangeRegistry()
		reg.SetInitialGeneration(1)
		releases := make([]func(), 0, 600)
		for n := 0; n < 600; n++ {
			_, _, _, release := reg.Admit(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/", nil), EntryCoord{GenID: 1, RouteID: "route"})
			releases = append(releases, release)
		}
		wait := reg.PublishGeneration(1, 2, []EntryTransition{{FromRouteID: "route", ToRouteID: "route", Retire: true, Mandatory: true}})
		for _, release := range releases {
			release()
		}
		wait()

		triggers := make([]func(), 24)
		for n := range triggers {
			_, _, _, triggers[n] = reg.Admit(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/other", nil), EntryCoord{GenID: 2, LocationIndex: 1, RouteID: "other"})
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, closeRequest := range triggers {
			wg.Add(1)
			go func(release func()) {
				defer wg.Done()
				<-start
				release()
			}(closeRequest)
		}
		close(start)
		runtime.Gosched()
		_, _, _, release := reg.Admit(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/", nil), EntryCoord{GenID: 1, RouteID: "route"})
		wg.Wait()
		got := reg.LastRetirementResults()
		if len(got) != 1 {
			release()
			t.Fatalf("results=%+v", got)
		}
		if got[0].CompletedWithinBound || got[0].Claim == "complete_withdrawal" {
			active := reg.ActiveCount()
			release()
			t.Fatalf("same-publish recompute restored stale completion after Admit returned: attempt=%d active=%d results=%+v", attempt, active, got)
		}
		release()
	}
}
