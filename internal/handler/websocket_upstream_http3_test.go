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
	"sync"
	"sync/atomic"
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

func TestWebSocketHTTP3PendingDialWaiters(t *testing.T) {
	dialCtx, cancelDial := context.WithCancel(context.Background())
	connection := &websocketHTTP3Connection{
		address:    "backend",
		ready:      make(chan struct{}),
		dialCancel: cancelDial,
	}
	transport := &websocketHTTP3Transport{
		connections: map[string]*websocketHTTP3Connection{"backend": connection},
	}
	defer transport.Close()

	waitCtx, cancelWait := context.WithCancelCause(context.Background())
	waitErr := errors.New("waiting request canceled")
	cancelWait(waitErr)
	if _, err := transport.connection(waitCtx, "backend"); !errors.Is(err, waitErr) {
		t.Fatalf("waiting request error = %v, want %v", err, waitErr)
	}
	if dialCtx.Err() != nil {
		t.Fatal("canceling a waiter canceled the shared dial")
	}

	dialErr := errors.New("backend setup failed")
	connection.err = dialErr
	close(connection.ready)
	if _, err := transport.connection(context.Background(), "backend"); !errors.Is(err, dialErr) {
		t.Fatalf("shared dial error = %v, want %v", err, dialErr)
	}
}

func TestWebSocketHTTP3UpstreamStreamLimitCancellation(t *testing.T) {
	transport, address := newHTTP3UpstreamTestTransport(t, 1, http.HandlerFunc(echoHTTP3WebSocket))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := transport.RoundTrip(http3WebSocketRequest(ctx, address, "/ws"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("healthy sibling status = %d, want 200", response.StatusCode)
	}

	waitCtx, cancelWait := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelWait()
	_, err = transport.RoundTrip(http3WebSocketRequest(waitCtx, address, "/ws"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked stream error = %v, want deadline exceeded", err)
	}
	transport.mu.Lock()
	connection := transport.connections[address]
	active := 0
	if connection != nil {
		active = connection.active
	}
	transport.mu.Unlock()
	if connection == nil || active != 1 {
		t.Fatalf("active streams after canceled open = %d, want the healthy sibling only", active)
	}
	stream, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatal("WebSocket response is not duplex")
	}
	echoWebSocketBytes(t, stream, stream)
}

func TestWebSocketHTTP3FailedHandshakesPreserveSibling(t *testing.T) {
	blocked := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blocked" {
			close(blocked)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		echoHTTP3WebSocket(w, r)
	})
	transport, address := newHTTP3UpstreamTestTransport(t, 0, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	response, err := transport.RoundTrip(http3WebSocketRequest(ctx, address, "/ws"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("healthy sibling status = %d, want 200", response.StatusCode)
	}

	invalid := http3WebSocketRequest(ctx, address, "/ws")
	invalid.Header.Set("X-Invalid", "bad\x00value")
	if _, err := transport.RoundTrip(invalid); err == nil || !strings.Contains(err.Error(), "invalid HTTP header") {
		t.Fatalf("invalid-header handshake error = %v", err)
	}

	transport.readTimeout = 2 * time.Second
	result := make(chan error, 1)
	go func() {
		_, err := transport.RoundTrip(http3WebSocketRequest(ctx, address, "/blocked"))
		result <- err
	}()
	select {
	case <-blocked:
	case err := <-result:
		t.Fatalf("handshake finished before reaching the backend: %v", err)
	case <-ctx.Done():
		t.Fatal("backend never received the blocked handshake")
	}
	select {
	case err := <-result:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("blocked handshake error = %v, want a read timeout", err)
		}
	case <-ctx.Done():
		t.Fatal("blocked handshake did not time out")
	}

	transport.mu.Lock()
	connection := transport.connections[address]
	active := 0
	if connection != nil {
		active = connection.active
	}
	transport.mu.Unlock()
	if connection == nil || active != 1 {
		t.Fatalf("active streams after failed handshakes = %d, want the healthy sibling only", active)
	}
	stream, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatal("WebSocket response is not duplex")
	}
	echoWebSocketBytes(t, stream, stream)
}

func TestWebSocketHTTP3IdleConnectionLifecycle(t *testing.T) {
	for _, retire := range []bool{false, true} {
		name := "eviction permits reconnect"
		if retire {
			name = "retirement closes idle connection"
		}
		t.Run(name, func(t *testing.T) {
			transport, address := newHTTP3UpstreamTestTransport(t, 0, http.HandlerFunc(echoHTTP3WebSocket))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			response, err := transport.RoundTrip(http3WebSocketRequest(ctx, address, "/ws"))
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK {
				response.Body.Close()
				t.Fatalf("initial WebSocket status = %d, want 200", response.StatusCode)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			transport.mu.Lock()
			connection := transport.connections[address]
			var active int
			var timer *time.Timer
			if connection != nil {
				active = connection.active
				timer = connection.idleTimer
			}
			if !retire && timer != nil {
				timer.Stop()
			}
			transport.mu.Unlock()
			if connection == nil || active != 0 || timer == nil {
				t.Fatalf("released connection has active=%d idle timer=%v", active, timer)
			}

			if retire {
				if err := transport.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				transport.closeIdle(connection)
			}
			select {
			case <-connection.conn.Context().Done():
			case <-ctx.Done():
				t.Fatal("idle QUIC connection was not closed")
			}
			response, err = transport.RoundTrip(http3WebSocketRequest(ctx, address, "/ws"))
			if retire {
				if err == nil {
					response.Body.Close()
					t.Fatal("retired transport accepted a new stream")
				}
				return
			}
			if err != nil {
				t.Fatalf("reconnect after idle eviction: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("replacement WebSocket status = %d, want 200", response.StatusCode)
			}
			transport.mu.Lock()
			replacement := transport.connections[address]
			transport.mu.Unlock()
			if replacement == nil || replacement == connection {
				t.Fatal("idle eviction did not establish a replacement connection")
			}
			stream, ok := response.Body.(io.ReadWriteCloser)
			if !ok {
				t.Fatal("replacement WebSocket response is not duplex")
			}
			echoWebSocketBytes(t, stream, stream)
		})
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
		ProxyPass:           "https://inventory",
		ProxyConnectTimeout: config.Duration(time.Second),
	}, map[string]config.UpstreamConfig{
		"inventory": {
			Name:                      "inventory",
			WebSocketUpstreamProtocol: "http3",
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

func TestWebSocketHTTP3UpstreamReplacesDrainingConnection(t *testing.T) {
	for _, limit := range []int{0, 1} {
		t.Run(fmt.Sprintf("max_connections=%d", limit), func(t *testing.T) {
			testWebSocketHTTP3UpstreamReplacesDrainingConnection(t, limit, false)
		})
	}
	t.Run("retirement wakes capacity waiter", func(t *testing.T) {
		testWebSocketHTTP3UpstreamReplacesDrainingConnection(t, 1, true)
	})
}

func testWebSocketHTTP3UpstreamReplacesDrainingConnection(t *testing.T, limit int, retireWhileWaiting bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pki := newBackendPKI(t)
	cert := pki.issue(t, "inventory", []string{"inventory.internal"}, nil)
	roots := x509.NewCertPool()
	roots.AddCert(pki.cert)
	listener, err := quic.ListenAddr("127.0.0.1:0", http3.ConfigureTLSConfig(&tls.Config{
		Certificates: []tls.Certificate{cert},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	baseTransport, err := newHTTP3WebSocketUpstreamTransport(config.LocationConfig{
		Resilience: &config.LocationResilienceConfig{MaxConnectionsPerBackend: limit},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := baseTransport.(*websocketHTTP3Transport)
	transport.tlsConfig = &tls.Config{RootCAs: roots, ServerName: "inventory.internal", NextProtos: []string{http3.NextProtoH3}}
	type acceptedConnection struct {
		conn   *quic.Conn
		server *http3.Server
	}
	accepted := make(chan acceptedConnection, 2)
	acceptDone := make(chan struct{})
	var servers []*http3.Server
	var serving sync.WaitGroup
	var requests atomic.Int32
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept(ctx)
			if err != nil {
				return
			}
			server := &http3.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				echoHTTP3WebSocket(w, r)
			})}
			servers = append(servers, server)
			serving.Add(1)
			go func() {
				defer serving.Done()
				_ = server.ServeQUICConn(conn)
			}()
			select {
			case accepted <- acceptedConnection{conn, server}:
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		<-acceptDone
		for _, server := range servers {
			if err := server.Close(); err != nil {
				t.Errorf("close HTTP/3 backend: %v", err)
			}
		}
		if err := transport.Close(); err != nil {
			t.Errorf("close HTTP/3 transport: %v", err)
		}
		done := make(chan struct{})
		go func() { serving.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("HTTP/3 backend connections did not stop")
		}
	})
	address := listener.Addr().String()
	duplexBody := func(response *http.Response, err error) io.ReadWriteCloser {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("backend handshake: %d", response.StatusCode)
		}
		body, ok := response.Body.(io.ReadWriteCloser)
		if !ok {
			t.Fatalf("backend body is not duplex: %T", response.Body)
		}
		t.Cleanup(func() { _ = body.Close() })
		return body
	}
	open := func() io.ReadWriteCloser {
		t.Helper()
		return duplexBody(transport.RoundTrip(http3WebSocketRequest(ctx, address, "/ws")))
	}
	first := open()
	var old acceptedConnection
	select {
	case old = <-accepted:
	case <-ctx.Done():
		t.Fatal("first backend connection not accepted")
	}
	oldConnection := transport.connections[address]
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- old.server.Shutdown(ctx) }()
	for {
		probe, err := oldConnection.client.OpenRequestStream(ctx)
		if err != nil {
			if ctx.Err() != nil || old.conn.Context().Err() != nil || errors.Is(err, net.ErrClosed) {
				t.Fatalf("waiting for GOAWAY: %v", err)
			}
			break
		}
		probe.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		probe.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
	}
	if _, err := transport.RoundTrip(http3WebSocketRequest(ctx, address, "/ws")); !errors.Is(err, errWebSocketH3StreamUnavailable) {
		t.Fatalf("draining connection error: %v", err)
	}
	if requests.Load() != 1 || transport.connections[address] != nil {
		t.Fatalf("failed attempt was replayed or draining connection retained: requests=%d", requests.Load())
	}
	echoWebSocketBytes(t, first, first)
	var replacement io.ReadWriteCloser
	if limit == 1 {
		waitCtx, stopWaiting := context.WithTimeout(ctx, 100*time.Millisecond)
		response, err := transport.RoundTrip(http3WebSocketRequest(waitCtx, address, "/ws"))
		stopWaiting()
		if response != nil {
			_ = response.Body.Close()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("at-capacity handshake error=%v, want request deadline", err)
		}
		echoWebSocketBytes(t, first, first)
		type result struct {
			response *http.Response
			err      error
		}
		pending := make(chan result, 1)
		go func() {
			response, err := transport.RoundTrip(http3WebSocketRequest(ctx, address, "/ws"))
			pending <- result{response, err}
		}()
		select {
		case <-accepted:
			t.Fatal("dial exceeded cap while the draining tunnel was still open")
		case got := <-pending:
			if got.response != nil {
				_ = got.response.Body.Close()
			}
			t.Fatalf("handshake did not wait for connection capacity: %v", got.err)
		case <-time.After(100 * time.Millisecond):
		}
		echoWebSocketBytes(t, first, first)
		if retireWhileWaiting {
			if err := transport.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-pending:
				if got.response != nil {
					_ = got.response.Body.Close()
				}
				if got.err == nil || !strings.Contains(got.err.Error(), "closed") {
					t.Fatalf("retired transport waiter error: %v", got.err)
				}
			case <-ctx.Done():
				t.Fatal("retirement did not wake the connection-capacity waiter")
			}
			echoWebSocketBytes(t, first, first)
			_ = first.Close()
			return
		}
		_ = first.Close()
		select {
		case got := <-pending:
			replacement = duplexBody(got.response, got.err)
		case <-ctx.Done():
			t.Fatal("handshake did not resume after connection capacity freed")
		}
	} else {
		replacement = open()
	}
	select {
	case next := <-accepted:
		if next.conn == old.conn {
			t.Fatal("new handshake reused the draining connection")
		}
	case <-ctx.Done():
		t.Fatal("replacement connection not accepted")
	}
	echoWebSocketBytes(t, replacement, replacement)
	_ = first.Close()
	select {
	case <-old.conn.Context().Done():
	case <-ctx.Done():
		t.Fatal("evicted connection was not closed after its last tunnel")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("graceful backend shutdown: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("graceful backend shutdown did not finish")
	}
	echoWebSocketBytes(t, replacement, replacement)
}

func newHTTP3UpstreamTestTransport(t *testing.T, maxStreams int64, handler http.Handler) (*websocketHTTP3Transport, string) {
	t.Helper()
	pki := newBackendPKI(t)
	cert := pki.issue(t, "inventory", []string{"inventory.internal"}, nil)
	roots := x509.NewCertPool()
	roots.AddCert(pki.cert)
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http3.Server{
		TLSConfig:  http3.ConfigureTLSConfig(&tls.Config{Certificates: []tls.Certificate{cert}}),
		QUICConfig: &quic.Config{MaxIncomingStreams: maxStreams},
		Handler:    handler,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(packetConn) }()
	transport := &websocketHTTP3Transport{
		connections:    make(map[string]*websocketHTTP3Connection),
		tlsConfig:      &tls.Config{RootCAs: roots, ServerName: "inventory.internal", NextProtos: []string{http3.NextProtoH3}},
		connectTimeout: 5 * time.Second,
	}
	t.Cleanup(func() {
		if err := transport.Close(); err != nil {
			t.Errorf("close HTTP/3 transport: %v", err)
		}
		if err := server.Close(); err != nil {
			t.Errorf("close HTTP/3 backend: %v", err)
		}
		_ = packetConn.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("HTTP/3 backend did not stop")
		}
	})
	return transport, packetConn.LocalAddr().String()
}

func http3WebSocketRequest(ctx context.Context, address, path string) *http.Request {
	request := (&http.Request{
		Method: http.MethodConnect,
		Proto:  "websocket",
		URL:    &url.URL{Scheme: "https", Host: address, Path: path},
		Host:   address,
		Header: make(http.Header),
	}).WithContext(ctx)
	request.Header.Set("Sec-WebSocket-Version", "13")
	return request
}

func echoHTTP3WebSocket(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	writer := websocketFlushingWriter{w}
	for {
		var header [6]byte
		if _, err := io.ReadFull(r.Body, header[:]); err != nil {
			return
		}
		length := int(header[1] & 0x7f)
		if header[1]&0x80 == 0 || length >= 126 {
			return
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(r.Body, payload); err != nil {
			return
		}
		for i := range payload {
			payload[i] ^= header[2+i%4]
		}
		frame := append([]byte{header[0], byte(length)}, payload...)
		if _, err := writer.Write(frame); err != nil || header[0]&0xf == 8 {
			return
		}
	}
}

func newWebSocketHTTP3Upstream(t *testing.T, handlers ...http.Handler) (http.Handler, *backendPKI, chan string, func()) {
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
			if len(handlers) > 0 {
				handlers[0].ServeHTTP(w, r)
			} else {
				echoHTTP3WebSocket(w, r)
			}
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
		ProxyPass:        "https://inventory",
		ProxyReadTimeout: config.Duration(5 * time.Second),
		ProxySendTimeout: config.Duration(5 * time.Second),
		BackendTLS: &config.BackendTLSConfig{
			CAMode: "file_only", CAFile: pki.caPath, ServerName: "inventory.internal",
			ClientCert: clientCertPath, ClientKey: clientKeyPath,
		},
	}
	upstreams := map[string]config.UpstreamConfig{
		"inventory": {
			Name:                      "inventory",
			WebSocketUpstreamProtocol: "http3",
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
	want := append([]byte{0x81, byte(len(payload))}, payload...)
	echo := make([]byte, len(want))
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("read echoed WebSocket frame: %v", err)
	}
	if !bytes.Equal(echo, want) {
		t.Fatalf("echoed frame=%x, want unmasked server frame %x", echo, want)
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
	want := append([]byte{0x81, byte(len(payload))}, payload...)
	echo := make([]byte, 0, len(want))
	for len(echo) < len(want) {
		response, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("read H2 WebSocket echo: %v", err)
		}
		if data, ok := response.(*http2.DataFrame); ok && data.StreamID == streamID {
			echo = append(echo, data.Data()...)
		}
	}
	if !bytes.Equal(echo, want) {
		t.Fatalf("H2 echoed frame=%x, want unmasked server frame %x", echo, want)
	}
}
