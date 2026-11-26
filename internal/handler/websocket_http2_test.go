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
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"jul/internal/config"
	"jul/internal/websocketx"
)

func TestHTTP2ExtendedConnectProxiesToHTTP1WebSocket(t *testing.T) {
	if !runtimeHTTP2ExtendedConnectEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHTTP2ExtendedConnectProxiesToHTTP1WebSocket$")
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
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !websocketx.ValidExtendedRequest(r) {
			http.Error(w, "invalid Extended CONNECT", http.StatusBadRequest)
			return
		}
		proxy.ServeHTTP(w, websocketx.AsApplicationRequest(r))
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

	mask := [4]byte{1, 2, 3, 4}
	payload := bytes.Repeat([]byte("j"), 32<<10)
	clientFrame := []byte{0x82, 0xFE, byte(len(payload) >> 8), byte(len(payload)), mask[0], mask[1], mask[2], mask[3]}
	for i, value := range payload {
		clientFrame = append(clientFrame, value^mask[i&3])
	}
	for len(clientFrame) > 0 {
		n := min(len(clientFrame), 16<<10)
		if err := framer.WriteData(1, false, clientFrame[:n]); err != nil {
			t.Fatalf("write client WebSocket frame: %v", err)
		}
		clientFrame = clientFrame[n:]
	}
	echo := make([]byte, 0, len(payload)+4)
	for len(echo) < len(payload)+4 {
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
	if len(echo) != len(payload)+4 || echo[0] != 0x82 || echo[1] != 126 ||
		binary.BigEndian.Uint16(echo[2:4]) != uint16(len(payload)) ||
		!bytes.Equal(echo[4:], payload) {
		t.Fatalf("backend echo frame header=%x payload length=%d", echo[:min(len(echo), 4)], len(echo)-4)
	}
	if err := framer.WriteRSTStream(1, http2.ErrCodeCancel); err != nil {
		t.Fatalf("cancel Extended CONNECT stream: %v", err)
	}
	select {
	case <-backendClosed:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled H2 stream did not close the backend WebSocket")
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
		if length == 126 {
			var extendedLength [2]byte
			if _, err := io.ReadFull(rw, extendedLength[:]); err != nil {
				return
			}
			length = int(binary.BigEndian.Uint16(extendedLength[:]))
		} else if length == 127 {
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
