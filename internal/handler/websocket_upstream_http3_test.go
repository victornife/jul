// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package handler

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"jul/internal/config"
	"jul/internal/websocketx"
)

func TestWebSocketHTTP3UpstreamFromHTTP1(t *testing.T) {
	proxy, _, _, closeBackend := newWebSocketHTTP3Upstream(t)
	defer closeBackend()
	server := httptest.NewServer(proxy)
	defer server.Close()

	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	_, err = fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: chat\r\n\r\n", strings.TrimPrefix(server.URL, "http://"), key)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("HTTP/1.1 status = %q, want 101", status)
	}
	headers := make(http.Header)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			t.Fatalf("invalid response header %q", line)
		}
		headers.Add(name, strings.TrimSpace(value))
	}
	if headers.Get("Sec-WebSocket-Accept") != websocketAccept(key) || headers.Get("Sec-WebSocket-Protocol") != "chat" {
		t.Fatalf("HTTP/1.1 handshake headers = %v", headers)
	}
	echoWebSocketBytes(t, conn, reader)
}

func TestWebSocketHTTP3UpstreamFromHTTP2(t *testing.T) {
	if !runtimeHTTP2ExtendedConnectEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWebSocketHTTP3UpstreamFromHTTP2$")
		cmd.Env = withHTTP2ExtendedConnectEnabled(os.Environ())
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("H2 Extended CONNECT subprocess failed: %v\n%s", err, output)
		}
		return
	}

	proxy, _, _, closeBackend := newWebSocketHTTP3Upstream(t)
	defer closeBackend()
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

	host := strings.TrimPrefix(server.URL, "https://")
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
	framer := startHTTP2WebSocketClient(t, conn)
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	writeHTTP2Headers(t, framer, encoder, &block, 1, []hpack.HeaderField{
		{Name: ":method", Value: http.MethodConnect},
		{Name: ":protocol", Value: "websocket"},
		{Name: ":scheme", Value: "https"},
		{Name: ":path", Value: "/ws"},
		{Name: ":authority", Value: host},
		{Name: "sec-websocket-version", Value: "13"},
		{Name: "sec-websocket-protocol", Value: "chat"},
	})
	status, responseHeaders := readHTTP2WebSocketResponse(t, framer, 1)
	if status != "200" || responseHeaders.Get("sec-websocket-protocol") != "chat" {
		t.Fatalf("H2 response status=%s headers=%v, want 200 and chat", status, responseHeaders)
	}
	echoWebSocketHTTP2(t, framer, 1)
	_ = framer.WriteRSTStream(1, http2.ErrCodeCancel)
}

func TestWebSocketHTTP3UpstreamFromHTTP3(t *testing.T) {
	proxy, pki, backendRemote, closeBackend := newWebSocketHTTP3Upstream(t)
	defer closeBackend()

	cert := pki.issue(t, "front", []string{"front.internal"}, nil)
	tlsConfig := http3.ConfigureTLSConfig(&tls.Config{Certificates: []tls.Certificate{cert}})
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http3.Server{
		TLSConfig: tlsConfig,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !websocketx.ValidExtendedRequest(r) {
				http.Error(w, "invalid Extended CONNECT", http.StatusBadRequest)
				return
			}
			proxy.ServeHTTP(w, websocketx.AsApplicationRequest(r))
		}),
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(packetConn) }()
	defer func() {
		_ = server.Close()
		_ = packetConn.Close()
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second):
			t.Error("HTTP/3 frontend server did not stop")
		}
	}()

	roots := x509.NewCertPool()
	roots.AddCert(pki.cert)
	transport := &http3.Transport{TLSClientConfig: &tls.Config{
		RootCAs: roots, ServerName: "front.internal",
	}}
	defer transport.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	requestURL := (&url.URL{Scheme: "https", Host: packetConn.LocalAddr().String(), Path: "/ws"}).String()
	open := func() (*http.Response, *io.PipeWriter) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodConnect, requestURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Proto = "websocket"
		request.Header.Set("Sec-WebSocket-Version", "13")
		request.Header.Set("Sec-WebSocket-Protocol", "chat")
		pipeReader, pipeWriter := io.Pipe()
		request.Body = pipeReader
		request.ContentLength = -1
		responseCh := make(chan struct {
			response *http.Response
			err      error
		}, 1)
		go func() {
			response, err := transport.RoundTrip(request)
			responseCh <- struct {
				response *http.Response
				err      error
			}{response: response, err: err}
		}()
		select {
		case result := <-responseCh:
			if result.err != nil {
				t.Fatalf("HTTP/3 Extended CONNECT: %v", result.err)
			}
			return result.response, pipeWriter
		case <-ctx.Done():
			t.Fatalf("HTTP/3 Extended CONNECT did not complete: %v", ctx.Err())
			return nil, pipeWriter
		}
	}
	first, firstWriter := open()
	second, secondWriter := open()
	defer first.Body.Close()
	defer second.Body.Close()
	defer firstWriter.Close()
	defer secondWriter.Close()
	for i, response := range []*http.Response{first, second} {
		if response.StatusCode != http.StatusOK || response.Header.Get("Sec-WebSocket-Protocol") != "chat" {
			t.Fatalf("H3 response %d status=%d headers=%v, want 200 and chat", i+1, response.StatusCode, response.Header)
		}
	}
	if firstRemote, secondRemote := <-backendRemote, <-backendRemote; firstRemote != secondRemote {
		t.Fatalf("backend QUIC connection was not reused: remote addresses %q and %q", firstRemote, secondRemote)
	}

	retirer, ok := proxy.(interface{ Close() error })
	if !ok {
		t.Fatal("proxy does not expose generation retirement")
	}
	if err := retirer.Close(); err != nil {
		t.Fatalf("retire proxy generation: %v", err)
	}
	_ = first.Body.Close()
	_ = firstWriter.Close()
	if _, err := proxy.(*proxyHandler).websocket.RoundTrip(&http.Request{
		Method: http.MethodConnect,
		Proto:  "websocket",
		URL:    &url.URL{Scheme: "https", Host: "inventory.internal"},
		Header: make(http.Header),
	}); err == nil {
		t.Fatal("retired generation accepted a new WebSocket stream")
	}
	echoWebSocketBytes(t, secondWriter, second.Body)
}

func TestWebSocketHTTP3UpstreamRequiresExtendedConnectSetting(t *testing.T) {
	certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert := certServer.TLS.Certificates[0]
	certServer.Close()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	listener, err := quic.ListenAddr("127.0.0.1:0",
		http3.ConfigureTLSConfig(&tls.Config{Certificates: []tls.Certificate{cert}}), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := listener.Accept(ctx)
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.CloseWithError(0, "test complete")
		control, err := conn.OpenUniStream()
		if err != nil {
			serverDone <- err
			return
		}
		// HTTP/3 control stream type, SETTINGS frame type, empty settings.
		_, err = control.Write([]byte{0x00, 0x04, 0x00})
		if err != nil {
			serverDone <- err
			return
		}
		_ = control.Close()
		<-conn.Context().Done()
		serverDone <- nil
	}()

	transport := &websocketHTTP3Transport{
		connections:    make(map[string]*websocketHTTP3Connection),
		tlsConfig:      &tls.Config{RootCAs: roots, ServerName: "example.com", NextProtos: []string{http3.NextProtoH3}},
		connectTimeout: 3 * time.Second,
	}
	defer transport.Close()
	request, err := http.NewRequest(http.MethodConnect, "https://"+listener.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Proto = "websocket"
	_, err = transport.RoundTrip(request)
	if !errors.Is(err, errExtendedConnectUnsupported) {
		t.Fatalf("RoundTrip error = %v, want Extended CONNECT refusal", err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("minimal HTTP/3 settings peer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP/3 settings peer did not close")
	}
}

func TestWebSocketHTTP3UpstreamRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
	}{
		{name: "not extended connect", req: httptest.NewRequest(http.MethodGet, "https://inventory.internal/ws", nil)},
		{name: "missing URL", req: &http.Request{Method: http.MethodConnect, Proto: "websocket"}},
		{name: "non-HTTPS URL", req: &http.Request{
			Method: http.MethodConnect, Proto: "websocket", URL: &url.URL{Scheme: "http", Host: "inventory.internal"},
		}},
		{name: "request body", req: &http.Request{
			Method: http.MethodConnect, Proto: "websocket",
			URL:  &url.URL{Scheme: "https", Host: "inventory.internal"},
			Body: io.NopCloser(strings.NewReader("tunnel bytes")),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &websocketHTTP3Transport{connections: make(map[string]*websocketHTTP3Connection)}
			if _, err := transport.RoundTrip(tt.req); err == nil {
				t.Fatal("invalid handshake request unexpectedly succeeded")
			}
		})
	}

	transport := &websocketHTTP3Transport{closed: true, connections: make(map[string]*websocketHTTP3Connection)}
	request := &http.Request{
		Method: http.MethodConnect,
		Proto:  "websocket",
		URL:    &url.URL{Scheme: "https", Host: "inventory.internal"},
		Header: make(http.Header),
	}
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("closed transport accepted a request")
	}
}

func TestWebSocketHTTP3CloseIdleConnection(t *testing.T) {
	connection := &websocketHTTP3Connection{address: "backend", active: 1}
	transport := &websocketHTTP3Transport{
		connections: map[string]*websocketHTTP3Connection{"backend": connection},
	}
	transport.closeIdle(connection)
	if transport.connections["backend"] != connection {
		t.Fatal("active connection was retired as idle")
	}

	connection.active = 0
	transport.closeIdle(connection)
	if _, ok := transport.connections["backend"]; ok {
		t.Fatal("idle connection remained in the pool")
	}
}

func TestWebSocketHTTP3CloseCancelsPendingDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	connection := &websocketHTTP3Connection{
		address:    "backend",
		dialCancel: cancel,
		idleTimer:  timer,
	}
	transport := &websocketHTTP3Transport{
		connections: map[string]*websocketHTTP3Connection{"backend": connection},
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("pending dial context error = %v, want canceled", ctx.Err())
	}
	if len(transport.connections) != 0 {
		t.Fatalf("closed transport retained connections: %v", transport.connections)
	}
}

func TestWebSocketHTTP3UpstreamBoundsBlockedUDP(t *testing.T) {
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			if _, _, err := packetConn.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	defer func() {
		_ = packetConn.Close()
		<-done
	}()

	transport := &websocketHTTP3Transport{
		connections:    make(map[string]*websocketHTTP3Connection),
		tlsConfig:      &tls.Config{ServerName: "inventory.internal", NextProtos: []string{http3.NextProtoH3}},
		connectTimeout: 100 * time.Millisecond,
	}
	defer transport.Close()
	request := &http.Request{
		Method: http.MethodConnect,
		Proto:  "websocket",
		URL:    &url.URL{Scheme: "https", Host: packetConn.LocalAddr().String()},
		Header: make(http.Header),
	}
	started := time.Now()
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("blocked UDP peer unexpectedly established an HTTP/3 stream")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("blocked UDP setup took %s, want a bounded failure", elapsed)
	}
}

func TestWebSocketHTTP3UpstreamRejectsUntrustedBackend(t *testing.T) {
	pki := newBackendPKI(t)
	backendCert := pki.issue(t, "inventory", []string{"inventory.internal"}, nil)
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend := &http3.Server{
		TLSConfig: http3.ConfigureTLSConfig(&tls.Config{Certificates: []tls.Certificate{backendCert}}),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}
	backendDone := make(chan error, 1)
	go func() { backendDone <- backend.Serve(packetConn) }()
	defer func() {
		_ = backend.Close()
		_ = packetConn.Close()
		select {
		case <-backendDone:
		case <-time.After(2 * time.Second):
			t.Error("untrusted HTTP/3 backend did not stop")
		}
	}()

	proxy, err := NewProxy(context.Background(), config.ServerConfig{}, config.LocationConfig{
		ProxyPass:                 "https://inventory",
		WebSocketUpstreamProtocol: "http3",
		ProxyConnectTimeout:       config.Duration(time.Second),
	}, map[string]config.UpstreamConfig{
		"inventory": {
			Name: "inventory",
			Servers: []config.UpstreamServer{{
				Address: packetConn.LocalAddr().String(),
				Weight:  1,
			}},
		},
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proxy.(*proxyHandler).Close() }()

	request := httptest.NewRequest(http.MethodGet, "http://frontend.internal/ws", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	request.Header.Set("Sec-WebSocket-Version", "13")
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusSwitchingProtocols || recorder.Code == http.StatusOK {
		t.Fatalf("untrusted backend returned success status %d", recorder.Code)
	}
}

func newWebSocketHTTP3Upstream(t *testing.T) (http.Handler, *backendPKI, chan string, func()) {
	t.Helper()
	pki := newBackendPKI(t)
	backendCert := pki.issue(t, "inventory", []string{"inventory.internal"}, nil)
	clientCert := pki.issue(t, "jul-client", []string{"jul.client"}, nil)
	clientCertPath, clientKeyPath := writePair(t, t.TempDir(), clientCert)
	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(pki.cert)

	backendClosed := make(chan struct{}, 4)
	backendRemote := make(chan string, 4)
	backend := &http3.Server{
		TLSConfig: http3.ConfigureTLSConfig(&tls.Config{
			Certificates: []tls.Certificate{backendCert},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    clientCAs,
		}),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodConnect || r.Proto != "websocket" ||
				len(r.Header.Values("Sec-WebSocket-Key")) != 0 ||
				len(r.Header.Values("Connection")) != 0 ||
				len(r.Header.Values("Upgrade")) != 0 ||
				r.Header.Get("Sec-WebSocket-Protocol") != "chat" ||
				r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
				http.Error(w, "invalid H3 WebSocket handshake", http.StatusBadRequest)
				return
			}
			backendRemote <- r.RemoteAddr
			w.Header().Set("Sec-WebSocket-Protocol", "chat")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			_, _ = io.Copy(w, r.Body)
			backendClosed <- struct{}{}
		}),
	}
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backendDone := make(chan error, 1)
	go func() { backendDone <- backend.Serve(packetConn) }()

	location := config.LocationConfig{
		ProxyPass:                 "https://inventory",
		WebSocketUpstreamProtocol: "http3",
		ProxyReadTimeout:          config.Duration(5 * time.Second),
		ProxySendTimeout:          config.Duration(5 * time.Second),
		BackendTLS: &config.BackendTLSConfig{
			CAMode: "file_only", CAFile: pki.caPath, ServerName: "inventory.internal",
			ClientCert: clientCertPath, ClientKey: clientKeyPath,
		},
	}
	upstreams := map[string]config.UpstreamConfig{
		"inventory": {
			Name: "inventory",
			Servers: []config.UpstreamServer{{
				Address: packetConn.LocalAddr().String(),
				Weight:  1,
			}},
		},
	}
	proxy, err := NewProxy(context.Background(), config.ServerConfig{}, location, upstreams, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	closeBackend := func() {
		_ = proxy.(*proxyHandler).Close()
		_ = backend.Close()
		_ = packetConn.Close()
		select {
		case <-backendDone:
		case <-time.After(2 * time.Second):
			t.Error("HTTP/3 backend server did not stop")
		}
		select {
		case <-backendClosed:
		case <-time.After(2 * time.Second):
			t.Error("backend WebSocket stream did not close")
		}
	}
	return proxy, pki, backendRemote, closeBackend
}

func echoWebSocketBytes(t *testing.T, writer io.Writer, reader io.Reader) {
	t.Helper()
	mask := [4]byte{1, 2, 3, 4}
	payload := []byte("ok")
	frame := []byte{0x81, 0x82, mask[0], mask[1], mask[2], mask[3]}
	for i, value := range payload {
		frame = append(frame, value^mask[i&3])
	}
	if _, err := writer.Write(frame); err != nil {
		t.Fatalf("write client WebSocket frame: %v", err)
	}
	echo := make([]byte, len(frame))
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("read echoed WebSocket frame: %v", err)
	}
	if !bytes.Equal(echo, frame) {
		t.Fatalf("echoed frame=%x, want %x", echo, frame)
	}
}

func startHTTP2WebSocketClient(t *testing.T, conn net.Conn) *http2.Framer {
	t.Helper()
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
		return framer
	}
}

func writeHTTP2Headers(t *testing.T, framer *http2.Framer, encoder *hpack.Encoder, block *bytes.Buffer, streamID uint32, fields []hpack.HeaderField) {
	t.Helper()
	block.Reset()
	for _, field := range fields {
		if err := encoder.WriteField(field); err != nil {
			t.Fatal(err)
		}
	}
	if err := framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID: streamID, EndHeaders: true, BlockFragment: block.Bytes(),
	}); err != nil {
		t.Fatal(err)
	}
}

func readHTTP2WebSocketResponse(t *testing.T, framer *http2.Framer, streamID uint32) (string, http.Header) {
	t.Helper()
	status := ""
	headers := make(http.Header)
	for status == "" {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("read H2 Extended CONNECT response: %v", err)
		}
		response, ok := frame.(*http2.HeadersFrame)
		if !ok || response.StreamID != streamID {
			continue
		}
		block := append([]byte(nil), response.HeaderBlockFragment()...)
		ended := response.HeadersEnded()
		for !ended {
			next, err := framer.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			continuation, ok := next.(*http2.ContinuationFrame)
			if !ok || continuation.StreamID != streamID {
				t.Fatalf("unexpected H2 frame in response headers: %T", next)
			}
			block = append(block, continuation.HeaderBlockFragment()...)
			ended = continuation.HeadersEnded()
		}
		decoder := hpack.NewDecoder(4096, func(field hpack.HeaderField) {
			if field.Name == ":status" {
				status = field.Value
			} else {
				headers.Add(field.Name, field.Value)
			}
		})
		if _, err := decoder.Write(block); err != nil {
			t.Fatal(err)
		}
	}
	return status, headers
}

func echoWebSocketHTTP2(t *testing.T, framer *http2.Framer, streamID uint32) {
	t.Helper()
	mask := [4]byte{1, 2, 3, 4}
	payload := []byte("ok")
	frame := []byte{0x81, 0x82, mask[0], mask[1], mask[2], mask[3]}
	for i, value := range payload {
		frame = append(frame, value^mask[i&3])
	}
	if err := framer.WriteData(streamID, false, frame); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 0, len(frame))
	for len(echo) < len(frame) {
		response, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("read H2 WebSocket echo: %v", err)
		}
		if data, ok := response.(*http2.DataFrame); ok && data.StreamID == streamID {
			echo = append(echo, data.Data()...)
		}
	}
	if !bytes.Equal(echo, frame) {
		t.Fatalf("H2 echoed frame=%x, want %x", echo, frame)
	}
}
