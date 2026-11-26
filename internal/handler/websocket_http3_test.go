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
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"jul/internal/config"
	"jul/internal/respwriter"
	"jul/internal/websocketx"
)

func TestHTTP3ExtendedConnectProxiesToHTTP1WebSocket(t *testing.T) {
	backendClosed := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveWebSocketEcho(w, r, func() { close(backendClosed) })
	}))
	defer backend.Close()

	proxy, err := NewProxy(context.Background(), config.ServerConfig{},
		config.LocationConfig{ProxyPass: backend.URL}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.(*proxyHandler).Close() })
	registry := respwriter.NewExchangeRegistry()
	registry.SetInitialGeneration(1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	echo := make([]byte, len(payload)+4)
	if _, err := io.ReadFull(response.Body, echo); err != nil {
		t.Fatalf("read backend echo: %v", err)
	}
	if echo[0] != 0x82 || echo[1] != 126 ||
		binary.BigEndian.Uint16(echo[2:4]) != uint16(len(payload)) ||
		!bytes.Equal(echo[4:], payload) {
		t.Fatalf("backend echo frame header=%x payload length=%d", echo[:4], len(echo)-4)
	}
	cancel()
	select {
	case <-backendClosed:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled H3 stream did not close the backend WebSocket")
	}
	waitForExtendedExchangeCount(t, registry, 0)
}
