// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package handler

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"jul/internal/config"
	"jul/internal/respwriter"
	"jul/internal/websocketx"
)

type websocketUnwrapWriter struct {
	http.ResponseWriter
}

func (w websocketUnwrapWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestHTTP3ExtendedFrontendStreamReportsSetupErrors(t *testing.T) {
	request := httptest.NewRequest(http.MethodConnect, "https://example.test/ws", nil)
	request.Proto, request.ProtoMajor = "websocket", 3
	request.Body = nil
	writer := &websocketControlWriter{ResponseRecorder: httptest.NewRecorder()}
	wrapped := websocketUnwrapWriter{ResponseWriter: writer}
	if stream, err := extendedFrontendStream(wrapped, request, http.NewResponseController(wrapped)); stream != nil || err == nil {
		t.Fatalf("missing stream=%v error=%v", stream, err)
	}
	request.Body = io.NopCloser(strings.NewReader("frame"))
	stream, err := extendedFrontendStream(wrapped, request, http.NewResponseController(wrapped))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	data := make([]byte, 5)
	if _, err := io.ReadFull(stream, data); err != nil || string(data) != "frame" {
		t.Fatalf("stream read=%q error=%v", data, err)
	}
	if _, err := stream.Write(data); err != nil || writer.Body.String() != "frame" || writer.flushes != 1 {
		t.Fatalf("wrapped stream write=%q flushes=%d error=%v", writer.Body.String(), writer.flushes, err)
	}
}

func TestHTTP3ExtendedConnectProxiesToHTTP1WebSocket(t *testing.T) {
	testHTTP3ExtendedConnect(t, false, false)
}

func TestHTTP3ExtendedConnectWithdrawalStopsDataAfterClose(t *testing.T) {
	testHTTP3ExtendedConnect(t, true, false)
}

func TestHTTP3UpstreamWithdrawalStopsDataAfterClose(t *testing.T) {
	testHTTP3ExtendedConnect(t, true, true)
}

type websocketFlushingWriter struct{ http.ResponseWriter }

func (w websocketFlushingWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if err == nil {
		err = http.NewResponseController(w.ResponseWriter).Flush()
	}
	return n, err
}

func testHTTP3ExtendedConnect(t *testing.T, withdraw, nativeBackend bool) {
	backendClosed := make(chan struct{})
	afterCloseDone := make(chan struct{})
	finishAfterClose := sync.OnceFunc(func() { close(afterCloseDone) })
	var retirement *websocketRetirementBackend
	if withdraw {
		retirement = newWebsocketRetirementBackend()
	}
	var proxy http.Handler
	var backendRemote chan string
	if nativeBackend {
		var closeBackend func()
		proxy, _, backendRemote, closeBackend = newWebSocketHTTP3Upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/ws" {
				echoHTTP3WebSocket(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = http.NewResponseController(w).Flush()
			retirement.serveFrames(r.Body, websocketFlushingWriter{w})
			<-afterCloseDone
			close(backendClosed)
		}))
		defer closeBackend()
		defer finishAfterClose()
	} else {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if retirement != nil {
				retirement.serve(w, r)
				close(backendClosed)
			} else {
				serveWebSocketEcho(w, r, func() { close(backendClosed) })
			}
		}))
		defer backend.Close()
		var err error
		proxy, err = NewProxy(context.Background(), config.ServerConfig{},
			config.LocationConfig{ProxyPass: backend.URL}, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = proxy.(*proxyHandler).Close() })
	registry := respwriter.NewExchangeRegistry()
	registry.Grace = time.Second
	registry.Bound = 2 * time.Second
	registry.SetInitialGeneration(1)
	resumeSibling := make(chan struct{})
	remote := make(chan string, 2)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote <- r.RemoteAddr
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
		writer, request, _, release := registry.Admit(w, websocketx.AsApplicationRequest(r), respwriter.EntryCoord{
			GenID: 1, ListenAddr: "test", RouteID: "extended-ws",
		})
		defer release()
		proxy.ServeHTTP(writer, request)
	})

	certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert := certServer.TLS.Certificates[0]
	certServer.Close()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	tlsConfig := http3.ConfigureTLSConfig(&tls.Config{
		Certificates: []tls.Certificate{cert},
	})

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http3.Server{TLSConfig: tlsConfig, Handler: handler}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(packetConn) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = packetConn.Close()
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second):
			t.Error("HTTP/3 server did not stop")
		}
	})

	transport := &http3.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"},
	}
	t.Cleanup(func() { _ = transport.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	requestURL := &url.URL{
		Scheme: "https",
		Host:   packetConn.LocalAddr().String(),
		Path:   "/ws",
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodConnect, requestURL.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Proto = "websocket"
	request.Header.Set("Sec-WebSocket-Version", "13")
	if nativeBackend {
		request.Header.Set("Sec-WebSocket-Protocol", "chat")
	}
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
	defer pipeWriter.Close()

	var response *http.Response
	select {
	case result := <-responseCh:
		if result.err != nil {
			t.Fatalf("HTTP/3 Extended CONNECT: %v", result.err)
		}
		response = result.response
	case <-ctx.Done():
		t.Fatalf("HTTP/3 Extended CONNECT did not complete: %v", ctx.Err())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ProtoMajor != 3 {
		t.Fatalf("response status/protocol=%d/%s, want 200/HTTP3", response.StatusCode, response.Proto)
	}
	waitForExtendedExchangeCount(t, registry, 1)

	mask := [4]byte{1, 2, 3, 4}
	payload := bytes.Repeat([]byte("j"), 32<<10)
	frame := []byte{0x82, 0xFE, byte(len(payload) >> 8), byte(len(payload)), mask[0], mask[1], mask[2], mask[3]}
	if withdraw {
		payload = []byte("j")
		frame = []byte{0x82, 0x81, mask[0], mask[1], mask[2], mask[3]}
	}
	for i, value := range payload {
		frame = append(frame, value^mask[i&3])
	}
	for len(frame) > 0 {
		n := min(len(frame), 1024)
		if _, err := pipeWriter.Write(frame[:n]); err != nil {
			t.Fatalf("write client WebSocket frame: %v", err)
		}
		frame = frame[n:]
		time.Sleep(time.Millisecond)
	}
	echoSize := len(payload) + 4
	if withdraw {
		echoSize = len(payload) + 2
	}
	echo := make([]byte, echoSize)
	if _, err := io.ReadFull(response.Body, echo); err != nil {
		t.Fatalf("read backend echo: %v", err)
	}
	if !withdraw && (echo[0] != 0x82 || echo[1] != 126 ||
		binary.BigEndian.Uint16(echo[2:4]) != uint16(len(payload)) ||
		!bytes.Equal(echo[4:], payload)) {
		t.Fatalf("backend echo frame header=%x payload length=%d", echo[:4], len(echo)-4)
	}
	if withdraw {
		if !bytes.Equal(echo, []byte{0x82, 1, 'j'}) {
			t.Fatalf("unexpected pre-retirement echo: %x", echo)
		}
		var backendSibling io.ReadWriteCloser
		if nativeBackend {
			proxy := proxy.(*proxyHandler)
			transport := proxy.websocket.(*websocketHTTP3Transport)
			backendRequest := http3WebSocketRequest(ctx, transport.pool.Backends()[0].URL.Host, "/sibling")
			backendRequest.Header.Set("Sec-WebSocket-Protocol", "chat")
			sibling, err := transport.RoundTrip(backendRequest)
			if err != nil {
				t.Fatal(err)
			}
			defer sibling.Body.Close()
			if sibling.StatusCode != http.StatusOK {
				t.Fatalf("backend sibling status=%d", sibling.StatusCode)
			}
			backendSibling = sibling.Body.(io.ReadWriteCloser)
			if first, second := <-backendRemote, <-backendRemote; first != second {
				t.Fatalf("backend sibling opened another QUIC connection: %q != %q", first, second)
			}
		}
		siblingRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.Scheme+"://"+requestURL.Host+"/healthy", nil)
		if err != nil {
			t.Fatal(err)
		}
		sibling, err := transport.RoundTrip(siblingRequest)
		if err != nil {
			t.Fatal(err)
		}
		defer sibling.Body.Close()
		if first, second := <-remote, <-remote; first != second {
			t.Fatalf("sibling did not use the same H3 connection: %q != %q", first, second)
		}
		waitForExtendedExchangeCount(t, registry, 2)
		wait := registry.PublishGeneration(1, 2, []respwriter.EntryTransition{
			{FromRouteID: "extended-ws", ToRouteID: "extended-ws", Retire: true, Mandatory: true},
			{FromRouteID: "healthy", FromLocationIndex: 1, ToRouteID: "healthy", ToLocationIndex: 1, KeepOpen: true},
		})
		closeFrame := make([]byte, 4)
		if _, err := io.ReadFull(response.Body, closeFrame); err != nil {
			t.Fatalf("read H3 retirement Close: %v", err)
		}
		if !bytes.Equal(closeFrame, []byte{0x88, 2, 0x03, 0xf0}) {
			t.Fatalf("H3 retirement Close=%x", closeFrame)
		}
		func() {
			defer finishAfterClose()
			retirement.afterClose(t)
		}()
		if _, err := pipeWriter.Write(maskedRetirementTestFrames()); err != nil {
			t.Fatalf("send client post-Close frames: %v", err)
		}
		results := wait()
		var retired bool
		for _, result := range results {
			if result.RouteID == "extended-ws" {
				retired = result.ForcedCount == 0 && result.CompletedWithinBound && result.CollateralCount == 0
			}
		}
		if !retired {
			t.Fatalf("H3 withdrawal=%+v", results)
		}
		extra, _ := io.ReadAll(response.Body)
		if len(extra) != 0 {
			t.Fatalf("H3 forwarded data after Close: %x", extra)
		}
		retirement.verify(t)
		if backendSibling != nil {
			echoWebSocketBytes(t, backendSibling, backendSibling)
		}
		close(resumeSibling)
		body, err := io.ReadAll(sibling.Body)
		if err != nil || string(body) != "healthy" {
			t.Fatalf("healthy sibling body=%q error=%v", body, err)
		}
	}
	cancel()
	select {
	case <-backendClosed:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled H3 stream did not close the backend WebSocket")
	}
	waitForExtendedExchangeCount(t, registry, 0)
}
