// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"jul/internal/websocketx"
)

type websocketRoundTripFunc func(*http.Request) (*http.Response, error)

func (f websocketRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type websocketTestBackend struct {
	closed bool
}

func (*websocketTestBackend) Read([]byte) (int, error)    { return 0, io.EOF }
func (*websocketTestBackend) Write(p []byte) (int, error) { return len(p), nil }
func (b *websocketTestBackend) Close() error {
	b.closed = true
	return nil
}

func extendedWebSocketTestRequest() *http.Request {
	r := httptest.NewRequest(http.MethodConnect, "https://example.test/ws", nil)
	r.Proto, r.ProtoMajor = "HTTP/2.0", 2
	r.Header[":protocol"] = []string{"websocket"}
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Protocol", "chat")
	r.Header.Set("Sec-WebSocket-Extensions", "permessage-deflate")
	return websocketx.AsApplicationRequest(r)
}

func websocketTestResponse(r *http.Request, body io.ReadCloser) *http.Response {
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + websocketGUID))
	header := make(http.Header)
	header.Set("Connection", "Upgrade")
	header.Set("Upgrade", "websocket")
	header.Set("Sec-WebSocket-Accept", base64.StdEncoding.EncodeToString(sum[:]))
	return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: header, Body: body}
}

func TestExtendedWebSocketRejectsHandshakeFailures(t *testing.T) {
	upstreamErr := errors.New("backend unavailable")
	for _, tt := range []struct {
		name           string
		invalidRequest bool
		missingRewrite bool
		transportErr   error
		response       func(*http.Response)
		readOnlyBody   bool
		customHandler  bool
		wantStatus     int
		wantMessage    string
		wantClosed     bool
		wantHandler    bool
	}{
		{name: "invalid request", invalidRequest: true, wantStatus: http.StatusBadRequest, wantMessage: "invalid WebSocket"},
		{name: "missing rewrite", missingRewrite: true, wantStatus: http.StatusBadGateway, wantMessage: "route is unavailable"},
		{name: "transport error", transportErr: upstreamErr, wantStatus: http.StatusBadGateway, wantMessage: "handshake failed"},
		{name: "transport error uses handler", transportErr: upstreamErr, customHandler: true, wantStatus: http.StatusBadGateway, wantMessage: "custom upstream error", wantHandler: true},
		{name: "canceled transport", transportErr: context.Canceled, customHandler: true, wantStatus: http.StatusOK},
		{name: "backend refusal", response: func(r *http.Response) { r.StatusCode = http.StatusForbidden }, wantStatus: http.StatusBadGateway, wantMessage: "did not accept", wantClosed: true},
		{name: "unoffered protocol", response: func(r *http.Response) { r.Header.Set("Sec-WebSocket-Protocol", "admin") }, wantStatus: http.StatusBadGateway, wantMessage: "unoffered WebSocket subprotocol", wantClosed: true},
		{name: "unoffered extension", response: func(r *http.Response) { r.Header.Set("Sec-WebSocket-Extensions", "other") }, wantStatus: http.StatusBadGateway, wantMessage: "unoffered WebSocket extension", wantClosed: true},
		{name: "read-only backend", readOnlyBody: true, wantStatus: http.StatusBadGateway, wantMessage: "stream is unavailable", wantClosed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			backend := &websocketTestBackend{}
			called := false
			h := &proxyHandler{ReverseProxy: &httputil.ReverseProxy{
				Rewrite: func(pr *httputil.ProxyRequest) {
					pr.SetURL(&url.URL{Scheme: "http", Host: "backend"})
				},
				Transport: websocketRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					if tt.transportErr != nil {
						return nil, tt.transportErr
					}
					var body io.ReadCloser = backend
					if tt.readOnlyBody {
						body = struct{ io.ReadCloser }{backend}
					}
					response := websocketTestResponse(r, body)
					if tt.response != nil {
						tt.response(response)
					}
					return response, nil
				}),
			}}
			if tt.missingRewrite {
				h.Rewrite = nil
			}
			if tt.customHandler {
				h.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
					called = true
					http.Error(w, "custom upstream error", http.StatusBadGateway)
				}
			}
			request := extendedWebSocketTestRequest()
			if tt.invalidRequest {
				request = httptest.NewRequest(http.MethodGet, "https://example.test/ws", nil)
			}
			recorder := httptest.NewRecorder()
			h.serveExtendedWebSocket(recorder, request)
			if recorder.Code != tt.wantStatus || !strings.Contains(recorder.Body.String(), tt.wantMessage) {
				t.Errorf("response status=%d body=%q, want %d containing %q", recorder.Code, recorder.Body.String(), tt.wantStatus, tt.wantMessage)
			}
			if tt.transportErr == context.Canceled && recorder.Body.Len() != 0 {
				t.Errorf("canceled request wrote a response: %q", recorder.Body.String())
			}
			if backend.closed != tt.wantClosed || called != tt.wantHandler {
				t.Errorf("backend closed=%t, error handler called=%t; want %t, %t", backend.closed, called, tt.wantClosed, tt.wantHandler)
			}
		})
	}
}

type websocketControlWriter struct {
	*httptest.ResponseRecorder
	duplexErr     error
	flushErr      error
	flushes       int
	readDeadline  time.Time
	writeDeadline time.Time
}

func (w *websocketControlWriter) EnableFullDuplex() error { return w.duplexErr }
func (w *websocketControlWriter) FlushError() error {
	w.flushes++
	return w.flushErr
}
func (w *websocketControlWriter) SetReadDeadline(deadline time.Time) error {
	w.readDeadline = deadline
	return nil
}
func (w *websocketControlWriter) SetWriteDeadline(deadline time.Time) error {
	w.writeDeadline = deadline
	return nil
}

func TestExtendedWebSocketFrontendSetupFailuresCloseBackend(t *testing.T) {
	failure := errors.New("response writer failure")
	for _, tt := range []struct {
		name       string
		protoMajor int
		duplexErr  error
		flushErr   error
		wantStatus int
		wantLog    string
	}{
		{name: "duplex unavailable", protoMajor: 2, duplexErr: failure, wantStatus: http.StatusInternalServerError},
		{name: "response flush fails", protoMajor: 2, flushErr: failure, wantStatus: http.StatusOK, wantLog: "response flush failed"},
		{name: "HTTP/3 stream unavailable", protoMajor: 3, wantStatus: http.StatusOK, wantLog: "stream setup failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			backend := &websocketTestBackend{}
			var logs bytes.Buffer
			h := &proxyHandler{
				ReverseProxy: &httputil.ReverseProxy{
					Rewrite: func(pr *httputil.ProxyRequest) {
						pr.SetURL(&url.URL{Scheme: "http", Host: "backend"})
					},
					Transport: websocketRoundTripFunc(func(r *http.Request) (*http.Response, error) {
						return websocketTestResponse(r, backend), nil
					}),
				},
				logger: slog.New(slog.NewTextHandler(&logs, nil)),
			}
			request := extendedWebSocketTestRequest()
			request.ProtoMajor = tt.protoMajor
			if tt.protoMajor == 3 {
				request.Proto = "websocket"
			}
			writer := &websocketControlWriter{
				ResponseRecorder: httptest.NewRecorder(), duplexErr: tt.duplexErr, flushErr: tt.flushErr,
			}
			h.serveExtendedWebSocket(writer, request)
			if writer.Code != tt.wantStatus || !backend.closed {
				t.Errorf("status=%d backend closed=%t, want %d and closed", writer.Code, backend.closed, tt.wantStatus)
			}
			if tt.wantLog != "" && !strings.Contains(logs.String(), tt.wantLog) {
				t.Errorf("log=%q, want %q", logs.String(), tt.wantLog)
			}
		})
	}
}

type websocketWriterFunc func([]byte) (int, error)

func (f websocketWriterFunc) Write(p []byte) (int, error) { return f(p) }

type websocketDeadlineWriter struct {
	deadline time.Time
}

func (*websocketDeadlineWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *websocketDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestExtendedWebSocketFrontendWriteAndCloseErrors(t *testing.T) {
	failure := errors.New("write or flush failed")
	for _, tt := range []struct {
		name        string
		write       websocketWriterFunc
		flushErr    error
		wantErr     error
		wantFlushes int
	}{
		{name: "short write", write: func(p []byte) (int, error) { return len(p) - 1, nil }, wantErr: io.ErrShortWrite},
		{name: "write error", write: func([]byte) (int, error) { return 0, failure }, wantErr: failure},
		{name: "flush error", write: func(p []byte) (int, error) { return len(p), nil }, flushErr: failure, wantErr: failure, wantFlushes: 1},
		{name: "success flushes", write: func(p []byte) (int, error) { return len(p), nil }, wantFlushes: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := &websocketControlWriter{ResponseRecorder: httptest.NewRecorder(), flushErr: tt.flushErr}
			front := &extendedWebSocketFrontend{writer: tt.write, controller: http.NewResponseController(w)}
			_, err := front.Write([]byte("frame"))
			if !errors.Is(err, tt.wantErr) || w.flushes != tt.wantFlushes {
				t.Errorf("write error=%v flushes=%d, want %v and %d", err, w.flushes, tt.wantErr, tt.wantFlushes)
			}
		})
	}

	w := &websocketControlWriter{ResponseRecorder: httptest.NewRecorder()}
	closeCalls := 0
	front := &extendedWebSocketFrontend{
		controller: http.NewResponseController(w),
		closeFn:    func() error { closeCalls++; return failure },
	}
	deadline := time.Now().Add(time.Second)
	if err := front.SetWriteDeadline(deadline); err != nil || !w.writeDeadline.Equal(deadline) {
		t.Fatalf("controller deadline=%v, error=%v", w.writeDeadline, err)
	}
	for range 2 {
		if err := front.Close(); !errors.Is(err, failure) {
			t.Errorf("close error=%v, want %v", err, failure)
		}
	}
	if closeCalls != 1 || w.readDeadline.IsZero() || w.writeDeadline.IsZero() {
		t.Errorf("close calls=%d, read deadline=%v, write deadline=%v", closeCalls, w.readDeadline, w.writeDeadline)
	}
	writer := &websocketDeadlineWriter{}
	front = &extendedWebSocketFrontend{writer: writer}
	if err := front.SetWriteDeadline(deadline); err != nil || !writer.deadline.Equal(deadline) {
		t.Errorf("writer deadline=%v, error=%v", writer.deadline, err)
	}
	front.writer = io.Discard
	if err := front.SetWriteDeadline(deadline); !errors.Is(err, http.ErrNotSupported) {
		t.Errorf("unsupported deadline error=%v", err)
	}
}

func TestExtendedWebSocketUpstreamHandshakeIsH1AndRejectsBadAccept(t *testing.T) {
	var upstreamRequest *http.Request
	h := &proxyHandler{
		ReverseProxy: &httputil.ReverseProxy{
			Transport: websocketRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				upstreamRequest = r.Clone(context.Background())
				return &http.Response{
					StatusCode: http.StatusSwitchingProtocols,
					Header: http.Header{
						"Connection":           {"Upgrade"},
						"Upgrade":              {"websocket"},
						"Sec-WebSocket-Accept": {"not-the-computed-accept"},
					},
					Body: io.NopCloser(strings.NewReader("")),
				}, nil
			}),
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(&url.URL{Scheme: "http", Host: "backend"})
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				http.Error(w, "upstream handshake failed", http.StatusBadGateway)
			},
		},
	}

	wireRequest := &http.Request{
		Method:     http.MethodConnect,
		Proto:      "HTTP/2.0",
		ProtoMajor: 2,
		Header:     make(http.Header),
		URL:        &url.URL{Path: "/ws"},
		Host:       "example.test",
	}
	wireRequest.Header[":protocol"] = []string{"websocket"}
	wireRequest.Header.Set("Sec-WebSocket-Version", "13")
	wireRequest.Header.Set("Sec-WebSocket-Protocol", "chat")
	if !websocketx.ValidExtendedRequest(wireRequest) {
		t.Fatal("test request is not a valid Extended CONNECT")
	}
	request := websocketx.AsApplicationRequest(wireRequest)
	recorder := httptest.NewRecorder()
	h.serveExtendedWebSocket(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", recorder.Code)
	}
	if upstreamRequest == nil {
		t.Fatal("upstream request was not sent")
	}
	if upstreamRequest.Method != http.MethodGet {
		t.Fatalf("upstream method=%q, want GET", upstreamRequest.Method)
	}
	if upstreamRequest.Header.Get("Connection") != "Upgrade" ||
		upstreamRequest.Header.Get("Upgrade") != "websocket" ||
		upstreamRequest.Header.Get("Sec-WebSocket-Version") != "13" {
		t.Fatalf("upstream handshake headers=%v", upstreamRequest.Header)
	}
	key, err := base64.StdEncoding.DecodeString(upstreamRequest.Header.Get("Sec-WebSocket-Key"))
	if err != nil || len(key) != 16 {
		t.Fatalf("generated upstream WebSocket key=%q, decoded bytes=%d, err=%v",
			upstreamRequest.Header.Get("Sec-WebSocket-Key"), len(key), err)
	}
	if len(upstreamRequest.Header[":protocol"]) != 0 {
		t.Fatal("HTTP/2 pseudo-header leaked to the HTTP/1.1 backend")
	}
}

func TestExtendedWebSocketWaitsForBackendAcceptance(t *testing.T) {
	h := &proxyHandler{
		ReverseProxy: &httputil.ReverseProxy{
			Transport: websocketRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("denied")),
				}, nil
			}),
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(&url.URL{Scheme: "http", Host: "backend"})
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				http.Error(w, "upstream handshake failed", http.StatusBadGateway)
			},
		},
	}
	request := httptest.NewRequest(http.MethodConnect, "https://example.test/ws", nil)
	request.Proto, request.ProtoMajor = "HTTP/2.0", 2
	request.Header[":protocol"] = []string{"websocket"}
	request.Header.Set("Sec-WebSocket-Version", "13")
	request = websocketx.AsApplicationRequest(request)

	recorder := httptest.NewRecorder()
	h.serveExtendedWebSocket(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502 before Extended CONNECT success", recorder.Code)
	}
}

func TestWebSocketNegotiationCannotAddUnrequestedProtocolOrExtension(t *testing.T) {
	request := make(http.Header)
	request.Set("Sec-WebSocket-Protocol", "chat, superchat")
	selected := make(http.Header)
	selected.Set("Sec-WebSocket-Protocol", "chat")
	if !validSelectedSubprotocol(request, selected) {
		t.Fatal("offered subprotocol should be accepted")
	}

	selected.Set("Sec-WebSocket-Protocol", "admin")
	if validSelectedSubprotocol(request, selected) {
		t.Fatal("unoffered subprotocol must be rejected")
	}
	selected = make(http.Header)
	selected.Set("Sec-WebSocket-Extensions", "permessage-deflate")
	if validSelectedExtensions(request, selected) {
		t.Fatal("unoffered extension must be rejected")
	}
	request.Set("Sec-WebSocket-Extensions", "permessage-deflate")
	if !validSelectedExtensions(request, selected) {
		t.Fatal("offered extension should be accepted")
	}
	selected.Set("Sec-WebSocket-Extensions", "x-unoffered")
	if validSelectedExtensions(request, selected) {
		t.Fatal("extension name not offered by the client must be rejected")
	}
	request.Set("Sec-WebSocket-Extensions", `permessage-deflate; note="x,y"`)
	selected.Set("Sec-WebSocket-Extensions", `permessage-deflate; server_no_context_takeover`)
	if !validSelectedExtensions(request, selected) {
		t.Fatal("offered extension with negotiated parameters should be accepted")
	}
	selected.Set("Sec-WebSocket-Extensions", `permessage-deflate; note="unterminated`)
	if validSelectedExtensions(request, selected) {
		t.Fatal("malformed selected extension must be rejected")
	}
}

func TestWebSocketNegotiationRejectsMalformedSelections(t *testing.T) {
	request := make(http.Header)
	request.Set("Sec-WebSocket-Protocol", "chat, superchat")
	for _, selected := range [][]string{{""}, {"chat, superchat"}, {"chat", "superchat"}} {
		response := make(http.Header)
		response["Sec-Websocket-Protocol"] = selected
		if validSelectedSubprotocol(request, response) {
			t.Errorf("accepted malformed selected protocols %q", selected)
		}
	}
	for _, tt := range []struct {
		offered  string
		selected string
		want     bool
	}{
		{`permessage-deflate; note="unterminated`, "permessage-deflate", false},
		{`permessage-deflate; note="x\",y", x-custom`, "x-custom", true},
		{"permessage-deflate,, x-custom", "permessage-deflate", false},
		{"permessage-deflate", "bad@extension", false},
		{"permessage-deflate", "", false},
	} {
		t.Run(tt.offered+"/"+tt.selected, func(t *testing.T) {
			req, res := make(http.Header), make(http.Header)
			req.Set("Sec-WebSocket-Extensions", tt.offered)
			res.Set("Sec-WebSocket-Extensions", tt.selected)
			if got := validSelectedExtensions(req, res); got != tt.want {
				t.Errorf("valid extensions = %t, want %t", got, tt.want)
			}
		})
	}
	if validBackendWebSocketResponse(nil, "key") {
		t.Fatal("nil backend response was accepted")
	}
}

func TestCopyWebSocketResponseHeadersRemovesHandshakeAndHopHeaders(t *testing.T) {
	source := make(http.Header)
	source.Set("Connection", "Upgrade, X-Remove")
	source.Set("Upgrade", "websocket")
	source.Set("X-Remove", "hop-by-hop")
	source.Set("Sec-WebSocket-Accept", "backend-only")
	source.Set("Sec-WebSocket-Key", "backend-only")
	source.Set("Content-Length", "7")
	source[":protocol"] = []string{"websocket"}
	source.Add("X-Allowed", "one")
	source.Add("X-Allowed", "two")
	destination := make(http.Header)
	copyWebSocketResponseHeaders(destination, source)
	if got := destination.Values("X-Allowed"); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("end-to-end headers = %v", destination)
	}
	for _, name := range []string{"Connection", "Upgrade", "X-Remove", "Sec-WebSocket-Accept", "Sec-WebSocket-Key", "Content-Length", ":protocol"} {
		if len(destination.Values(name)) != 0 || len(destination[name]) != 0 {
			t.Errorf("forwarded forbidden header %q: %v", name, destination)
		}
	}
}

func TestExtendedFrontendStreamRejectsUnsupportedProtocol(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	writer := httptest.NewRecorder()
	if stream, err := extendedFrontendStream(writer, request, http.NewResponseController(writer)); err == nil || stream != nil {
		t.Fatalf("unsupported protocol stream=%v error=%v", stream, err)
	}
}

type websocketErrorBackend struct {
	err error
}

func (b *websocketErrorBackend) Read([]byte) (int, error)  { return 0, b.err }
func (*websocketErrorBackend) Write(p []byte) (int, error) { return len(p), nil }
func (*websocketErrorBackend) Close() error                { return nil }

func TestRelayExtendedWebSocketReportsUnexpectedBackendErrors(t *testing.T) {
	client, front := net.Pipe()
	defer client.Close()
	failure := errors.New("backend read failed")
	if err := relayExtendedWebSocket(front, &websocketErrorBackend{err: failure}); !errors.Is(err, failure) {
		t.Fatalf("relay error=%v, want %v", err, failure)
	}
}

func TestRelayExtendedWebSocketStopsWhenBackendCloses(t *testing.T) {
	client, front := net.Pipe()
	defer client.Close()
	backendPeer, backend := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- relayExtendedWebSocket(front, backend) }()

	if err := backendPeer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if _, err := client.Read(one[:]); err == nil {
		t.Fatal("frontend remained open after the backend closed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("relay after backend close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after the backend closed")
	}
}
