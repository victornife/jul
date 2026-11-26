// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"jul/internal/config"
	"jul/internal/respwriter"
	"jul/internal/websocketx"
)

func TestHTTP2ExtendedConnectProxiesToHTTP1WebSocket(t *testing.T) {
	testHTTP2ExtendedConnect(t, false, false)
}

func TestHTTP2ExtendedConnectWithdrawalKeepsSibling(t *testing.T) {
	testHTTP2ExtendedConnect(t, true, false)
}

func TestHTTP2ExtendedConnectBlockedWithdrawalKeepsSibling(t *testing.T) {
	testHTTP2ExtendedConnect(t, true, true)
}

type websocketBlockedWriter struct {
	http.ResponseWriter
	bytes   int
	blocked chan struct{}
	once    sync.Once
}

func (w *websocketBlockedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *websocketBlockedWriter) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *websocketBlockedWriter) Write(p []byte) (int, error) {
	if w.bytes+len(p) > 65535 {
		w.once.Do(func() { close(w.blocked) })
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func testHTTP2ExtendedConnect(t *testing.T, withdraw, blocked bool) {
	t.Helper()
	if !runtimeHTTP2ExtendedConnectEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		args := []string{"-test.run=^" + t.Name() + "$"}
		// The child must write its counters where go test collects the parent's profile.
		if coverDir := flag.Lookup("test.gocoverdir"); coverDir != nil && coverDir.Value.String() != "" {
			args = append(args, "-test.gocoverdir="+coverDir.Value.String())
		}
		cmd := exec.CommandContext(ctx, os.Args[0], args...)
		cmd.Env = withHTTP2ExtendedConnectEnabled(os.Environ())
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("H2 Extended CONNECT subprocess failed: %v\n%s", err, output)
		}
		return
	}
	backendClosed := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveWebSocketEcho(w, r, func() { close(backendClosed) })
	}))
	defer backend.Close()

	var proxyLogs bytes.Buffer
	proxy, err := NewProxy(context.Background(), config.ServerConfig{},
		config.LocationConfig{ProxyPass: backend.URL}, nil, nil,
		slog.New(slog.NewTextHandler(&proxyLogs, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.(*proxyHandler).Close() })
	registry := respwriter.NewExchangeRegistry()
	registry.Grace = 200 * time.Millisecond
	registry.Bound = 400 * time.Millisecond
	registry.SetInitialGeneration(1)
	resumeSibling := make(chan struct{})
	blockedWrite := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthy" {
			writer, request, _, release := registry.Admit(w, r, respwriter.EntryCoord{
				GenID: 1, ListenAddr: "test", RouteID: "healthy", LocationIndex: 1,
			})
			defer release()
			writer.WriteHeader(http.StatusOK)
			_ = http.NewResponseController(writer).Flush()
			select {
			case <-resumeSibling:
				_, _ = io.WriteString(writer, "healthy")
			case <-request.Context().Done():
			}
			return
		}
		if !websocketx.ValidExtendedRequest(r) {
			http.Error(w, "invalid Extended CONNECT", http.StatusBadRequest)
			return
		}
		if blocked {
			w = &websocketBlockedWriter{ResponseWriter: w, blocked: blockedWrite}
		}
		writer, request, _, release := registry.Admit(w, websocketx.AsApplicationRequest(r), respwriter.EntryCoord{
			GenID: 1, ListenAddr: "test", RouteID: "extended-ws",
		})
		defer release()
		proxy.ServeHTTP(writer, request)
	})
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	serverURL := server.URL
	host := strings.TrimPrefix(serverURL, "https://")
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", host, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"h2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("read server SETTINGS: %v", err)
		}
		settings, ok := frame.(*http2.SettingsFrame)
		if !ok || settings.IsAck() {
			continue
		}
		var advertised bool
		if err := settings.ForeachSetting(func(setting http2.Setting) error {
			if setting.ID == http2.SettingEnableConnectProtocol && setting.Val == 1 {
				advertised = true
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !advertised {
			t.Fatal("server did not advertise ENABLE_CONNECT_PROTOCOL")
		}
		if err := framer.WriteSettingsAck(); err != nil {
			t.Fatal(err)
		}
		break
	}

	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	for _, field := range []hpack.HeaderField{
		{Name: ":method", Value: http.MethodConnect},
		{Name: ":protocol", Value: "websocket"},
		{Name: ":scheme", Value: "https"},
		{Name: ":path", Value: "/ws"},
		{Name: ":authority", Value: host},
		{Name: "sec-websocket-version", Value: "13"},
	} {
		if err := encoder.WriteField(field); err != nil {
			t.Fatal(err)
		}
	}
	if err := framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID: 1, EndHeaders: true, BlockFragment: block.Bytes(),
	}); err != nil {
		t.Fatal(err)
	}
	status := ""
	responseHeaders := make(http.Header)
	for status == "" {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("read Extended CONNECT response: %v", err)
		}
		if headers, ok := frame.(*http2.HeadersFrame); ok && headers.StreamID == 1 {
			headerBlock := append([]byte(nil), headers.HeaderBlockFragment()...)
			ended := headers.HeadersEnded()
			for !ended {
				next, err := framer.ReadFrame()
				if err != nil {
					t.Fatalf("read response CONTINUATION: %v", err)
				}
				continuation, ok := next.(*http2.ContinuationFrame)
				if !ok || continuation.StreamID != 1 {
					t.Fatalf("unexpected frame while reading headers: %T", next)
				}
				headerBlock = append(headerBlock, continuation.HeaderBlockFragment()...)
				ended = continuation.HeadersEnded()
			}
			decoder := hpack.NewDecoder(4096, func(field hpack.HeaderField) {
				if field.Name == ":status" {
					status = field.Value
					return
				}
				responseHeaders.Add(field.Name, field.Value)
			})
			if _, err := decoder.Write(headerBlock); err != nil {
				t.Fatal(err)
			}
		}
	}
	if status != "200" {
		t.Fatalf("Extended CONNECT status=%s headers=%v, want 200", status, responseHeaders)
	}
	waitForExtendedExchangeCount(t, registry, 1)

	mask := [4]byte{1, 2, 3, 4}
	payload := bytes.Repeat([]byte("j"), 32<<10)
	clientFrame := []byte{0x82, 0xFE, byte(len(payload) >> 8), byte(len(payload)), mask[0], mask[1], mask[2], mask[3]}
	for i, value := range payload {
		clientFrame = append(clientFrame, value^mask[i&3])
	}
	if blocked {
		clientFrame = bytes.Repeat(clientFrame, 4)
	}
	for len(clientFrame) > 0 {
		n := min(len(clientFrame), 16<<10)
		if err := framer.WriteData(1, false, clientFrame[:n]); err != nil {
			t.Fatalf("write client WebSocket frame: %v", err)
		}
		clientFrame = clientFrame[n:]
	}
	echo := make([]byte, 0, len(payload)+4)
	wantEcho := len(payload) + 4
	if blocked {
		wantEcho = 65535 // Exhaust this stream's initial HTTP/2 flow-control window.
	}
	for len(echo) < wantEcho {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("read backend echo: %v; proxy logs: %s", err, proxyLogs.String())
		}
		data, ok := frame.(*http2.DataFrame)
		if !ok || data.StreamID != 1 || len(data.Data()) == 0 {
			continue
		}
		echo = append(echo, data.Data()...)
	}
	if len(echo) != wantEcho || echo[0] != 0x82 || echo[1] != 126 ||
		binary.BigEndian.Uint16(echo[2:4]) != uint16(len(payload)) ||
		!bytes.Equal(echo[4:len(payload)+4], payload) {
		t.Fatalf("backend echo frame header=%x payload length=%d", echo[:min(len(echo), 4)], len(echo)-4)
	}
	if withdraw {
		if blocked {
			// Restore only connection credit; stream 1 must remain blocked.
			if err := framer.WriteWindowUpdate(0, uint32(len(echo))); err != nil {
				t.Fatal(err)
			}
			select {
			case <-blockedWrite:
			case <-time.After(time.Second):
				t.Fatal("tunnel writer never reached the exhausted stream window")
			}
		}
		block.Reset()
		for _, field := range []hpack.HeaderField{
			{Name: ":method", Value: http.MethodGet}, {Name: ":scheme", Value: "https"},
			{Name: ":path", Value: "/healthy"}, {Name: ":authority", Value: host},
		} {
			if err := encoder.WriteField(field); err != nil {
				t.Fatal(err)
			}
		}
		if err := framer.WriteHeaders(http2.HeadersFrameParam{
			StreamID: 3, EndHeaders: true, EndStream: true, BlockFragment: block.Bytes(),
		}); err != nil {
			t.Fatal(err)
		}
		for {
			frame, err := framer.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			if headers, ok := frame.(*http2.HeadersFrame); ok && headers.StreamID == 3 {
				break
			}
		}
		waitForExtendedExchangeCount(t, registry, 2)
		start := time.Now()
		wait := registry.PublishGeneration(1, 2, []respwriter.EntryTransition{
			{FromRouteID: "extended-ws", ToRouteID: "extended-ws", Retire: true, Mandatory: true},
			{FromRouteID: "healthy", FromLocationIndex: 1, ToRouteID: "healthy", ToLocationIndex: 1, KeepOpen: true},
		})
		closeFrame := make([]byte, 0, 4)
		for !blocked && len(closeFrame) < 4 {
			frame, err := framer.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			if data, ok := frame.(*http2.DataFrame); ok && data.StreamID == 1 {
				closeFrame = append(closeFrame, data.Data()...)
			}
			if reset, ok := frame.(*http2.RSTStreamFrame); ok && reset.StreamID == 1 {
				t.Fatal("H2 retirement reset the stream before sending Close")
			}
		}
		if !blocked && (closeFrame[0] != 0x88 || closeFrame[1] != 2 || binary.BigEndian.Uint16(closeFrame[2:4]) != 1008) {
			t.Fatalf("retirement Close=%x, want code 1008", closeFrame)
		}
		results := wait()
		if elapsed := time.Since(start); elapsed > registry.Bound+200*time.Millisecond {
			t.Fatalf("H2 withdrawal elapsed=%s, bound=%s", elapsed, registry.Bound)
		}
		var retired bool
		for _, result := range results {
			if result.RouteID == "extended-ws" {
				retired = result.ForcedCount == 1 && result.CompletedWithinBound && result.CollateralCount == 0
			}
		}
		if !retired {
			t.Fatalf("noncooperating client retirement=%+v", results)
		}
		close(resumeSibling)
		var body []byte
		for string(body) != "healthy" {
			frame, err := framer.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			if reset, ok := frame.(*http2.RSTStreamFrame); ok && reset.StreamID == 3 {
				t.Fatal("retirement reset the healthy H2 sibling")
			}
			if data, ok := frame.(*http2.DataFrame); ok && data.StreamID == 3 {
				body = append(body, data.Data()...)
			}
		}
	} else if err := framer.WriteRSTStream(1, http2.ErrCodeCancel); err != nil {
		t.Fatalf("cancel Extended CONNECT stream: %v", err)
	}
	select {
	case <-backendClosed:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled H2 stream did not close the backend WebSocket")
	}
	waitForExtendedExchangeCount(t, registry, 0)
}

func waitForExtendedExchangeCount(t *testing.T, registry *respwriter.ExchangeRegistry, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for registry.ActiveCount() != want {
		if time.Now().After(deadline) {
			t.Fatalf("active exchange count=%d, want %d", registry.ActiveCount(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func withHTTP2ExtendedConnectEnabled(env []string) []string {
	out := make([]string, 0, len(env)+1)
	var debugSettings []string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if key != "GODEBUG" || !ok {
			out = append(out, entry)
			continue
		}
		for _, setting := range strings.Split(value, ",") {
			settingKey, _, hasValue := strings.Cut(strings.TrimSpace(setting), "=")
			if hasValue && settingKey != "http2xconnect" {
				debugSettings = append(debugSettings, strings.TrimSpace(setting))
			}
		}
	}
	debugSettings = append(debugSettings, "http2xconnect=1")
	return append(out, "GODEBUG="+strings.Join(debugSettings, ","))
}

func runtimeHTTP2ExtendedConnectEnabled() bool {
	for _, setting := range strings.Split(os.Getenv("GODEBUG"), ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(setting), "=")
		if ok && key == "http2xconnect" {
			return value == "1"
		}

	}
	return false
}

func serveWebSocketEcho(w http.ResponseWriter, r *http.Request, onClose ...func()) {
	if r.Method != http.MethodGet ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		http.Error(w, "invalid WebSocket handshake", http.StatusBadRequest)
		return
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
		return
	}
	defer func() {
		_ = conn.Close()
		if len(onClose) > 0 {
			onClose[0]()
		}
	}()
	_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
	if err := rw.Flush(); err != nil {
		return
	}
	for {
		var header [2]byte
		if _, err := io.ReadFull(rw, header[:]); err != nil {
			return
		}
		length := int(header[1] & 0x7f)
		switch length {
		case 126:
			var extendedLength [2]byte
			if _, err := io.ReadFull(rw, extendedLength[:]); err != nil {
				return
			}
			length = int(binary.BigEndian.Uint16(extendedLength[:]))
		case 127:
			return
		}
		payload := make([]byte, length)
		if header[1]&0x80 != 0 {
			var mask [4]byte
			if _, err := io.ReadFull(rw, mask[:]); err != nil {
				return
			}
			if _, err := io.ReadFull(rw, payload); err != nil {
				return
			}
			for i := range payload {
				payload[i] ^= mask[i&3]
			}
		} else if _, err := io.ReadFull(rw, payload); err != nil {
			return
		}
		if length >= 126 {
			if _, err := rw.Write([]byte{header[0], 126, byte(length >> 8), byte(length)}); err != nil {
				return
			}
		} else if _, err := rw.Write([]byte{header[0], byte(len(payload))}); err != nil {
			return
		}
		if _, err := rw.Write(payload); err != nil {
			return
		}
		if err := rw.Flush(); err != nil {
			return
		}
	}

}
