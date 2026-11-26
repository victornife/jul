// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf && http3

package app

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"jul/internal/config"
)

func TestWebSocketProductionPolicyHandshake(t *testing.T) {
	testWebSocketProductionPolicyHandshake(t, nil, []webSocketPolicyCase{
		{probe: "clean", status: http.StatusOK, backend: true},
		{probe: "attack", status: http.StatusForbidden},
		{probe: "response-deny", status: http.StatusForbidden, backend: true},
		{probe: "clean", status: http.StatusOK, backend: true},
	})
}

type webSocketPolicyCase struct {
	probe           string
	status          int
	backend         bool
	headers         http.Header
	responseHeaders http.Header
}

func testWebSocketProductionPolicyHandshake(t *testing.T, configure func(*config.Config), cases []webSocketPolicyCase) {
	t.Helper()
	if !strings.Contains(os.Getenv("GODEBUG"), "http2xconnect=1") {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		args := []string{"-test.run=^" + regexp.QuoteMeta(t.Name()) + "$"}
		if dir := flag.Lookup("test.gocoverdir"); dir != nil && dir.Value.String() != "" {
			args = append(args, "-test.gocoverdir="+dir.Value.String())
		}
		cmd := exec.CommandContext(ctx, os.Args[0], args...)
		cmd.Env = append(os.Environ(), "GODEBUG="+os.Getenv("GODEBUG")+",http2xconnect=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("production policy subprocess: %v\n%s", err, output)
		}
		return
	}
	dir := t.TempDir()
	caFile, certFile, keyFile, roots, clientCert, wrongCert := generateTestPKI(t, dir)
	serverCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	clientCertFile := filepath.Join(dir, "client.pem")
	clientKeyFile := filepath.Join(dir, "client.key")
	key, err := x509.MarshalPKCS8PrivateKey(clientCert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCert.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, proto := range []int{2, 3} {
		for _, mtls := range []bool{false, true} {
			t.Run(fmt.Sprintf("HTTP%d/mtls=%t", proto, mtls), func(t *testing.T) {
				var hits, connections atomic.Int32
				backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					if r.Method != http.MethodGet || r.Header.Get("Upgrade") != "websocket" || r.TLS == nil ||
						(mtls && len(r.TLS.VerifiedChains) == 0) {
						t.Error("backend did not receive a verified H1 WebSocket handshake")
						http.Error(w, "invalid handshake", http.StatusBadRequest)
						return
					}
					if r.URL.Query().Get("probe") == "backend-deny" {
						http.Error(w, "backend handshake denied", http.StatusServiceUnavailable)
						return
					}
					conn, brw, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
					_, _ = fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\nContent-Type: text/plain\r\nX-Policy: %s\r\n\r\n",
						base64.StdEncoding.EncodeToString(sum[:]), r.Header.Get("X-Policy"))
					if err := brw.Flush(); err != nil {
						return
					}
					frame := make([]byte, 8)
					if _, err := io.ReadFull(brw, frame); err != nil {
						return // A denied response-header handshake closes the backend here.
					}
					if !bytes.Equal(frame, []byte{0x82, 0x82, 1, 2, 3, 4, 'o' ^ 1, 'k' ^ 2}) {
						t.Errorf("backend frame=%x", frame)
						return
					}
					_, _ = conn.Write([]byte{0x82, 2, 'o', 'k'})
					_, _ = io.Copy(io.Discard, brw)
				}))
				backend.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}}
				if mtls {
					backend.TLS.ClientAuth, backend.TLS.ClientCAs = tls.RequireAndVerifyClientCert, roots
				}
				backend.StartTLS()
				defer backend.Close()
				cfg := config.ProxyTarget("https://secure_backend", "127.0.0.1:0")
				cfg.Upstreams = []config.UpstreamConfig{{
					Name: "secure_backend", Strategy: "round_robin",
					Servers: []config.UpstreamServer{{Address: backend.Listener.Addr().String(), Weight: 1}},
					BackendTLS: &config.BackendTLSConfig{
						CAMode: "file_only", CAFile: caFile, ServerName: "backend.internal",
						ClientCert: clientCertFile, ClientKey: clientKeyFile,
					},
				}}
				cfg.Servers[0].TLS = &config.TLSConfig{Enabled: true, Cert: certFile, Key: keyFile}
				cfg.Servers[0].Locations[0].RequireClientCert = mtls
				if mtls {
					cfg.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "request", CAFile: caFile}
				}
				cfg.Servers[0].Locations[0].WAF = &config.WAFConfig{
					Enabled: true, Mode: "block", ResponseBodyCheck: true, RequestBodyLimit: 1024,
					InlineRules: `SecRequestBodyAccess On
SecRequestBodyLimit 1024
SecResponseBodyAccess On
SecResponseBodyLimit 1024
SecResponseBodyMimeType text/plain
SecRule ARGS:probe "@streq attack" "id:43501,phase:2,deny,status:403"
SecRule RESPONSE_HEADERS:X-Policy "@streq deny" "id:43502,phase:3,deny,status:403"
SecRule RESPONSE_BODY "@contains ok" "id:43503,phase:4,deny,status:403"`,
				}
				if configure != nil {
					configure(cfg)
				}
				factory, cleanup := minimalFactory(t)
				defer cleanup()
				handlers, _, commit, _, err := factory.Prepare(context.Background(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				commit()
				serverTLS := &tls.Config{Certificates: []tls.Certificate{serverCert}}
				if mtls {
					serverTLS.ClientAuth, serverTLS.ClientCAs = tls.VerifyClientCertIfGiven, roots
				}
				var endpoint string
				var newTransport func(*tls.Config) (http.RoundTripper, func())
				if proto == 2 {
					srv := httptest.NewUnstartedServer(handlers[cfg.Servers[0].Listen])
					srv.EnableHTTP2, srv.TLS = true, serverTLS
					srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
						if state == http.StateNew {
							connections.Add(1)
						}
					}
					srv.StartTLS()
					defer srv.Close()
					endpoint = srv.URL
					newTransport = func(c *tls.Config) (http.RoundTripper, func()) {
						tr := &websocketPolicyHTTP2Client{tlsConfig: c}
						return tr, func() {
							if tr.conn != nil {
								_ = tr.conn.Close()
							}
						}
					}

				} else {
					pc, err := net.ListenPacket("udp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					srv := &http3.Server{Handler: handlers[cfg.Servers[0].Listen], TLSConfig: serverTLS,
						ConnContext: func(ctx context.Context, _ *quic.Conn) context.Context {
							connections.Add(1)
							return ctx
						},
					}
					done := make(chan error, 1)
					go func() { done <- srv.Serve(pc) }()
					defer func() { _ = srv.Close(); _ = pc.Close(); <-done }()
					endpoint = "https://" + pc.LocalAddr().String()
					newTransport = func(c *tls.Config) (http.RoundTripper, func()) {
						tr := &http3.Transport{TLSClientConfig: c}
						return tr, func() { _ = tr.Close() }
					}
				}
				tlsClient := &tls.Config{RootCAs: roots, ServerName: "app.example.test"}
				if mtls {
					tlsClient.Certificates = []tls.Certificate{clientCert}
				}
				tr, closeTransport := newTransport(tlsClient)
				defer closeTransport()
				for _, tc := range cases {
					probe := tc.probe
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					pr, pw := io.Pipe()
					req, _ := http.NewRequestWithContext(ctx, http.MethodConnect, endpoint+"/?probe="+probe, pr)
					req.ContentLength = -1
					req.Header.Set("Sec-WebSocket-Version", "13")
					for name, values := range tc.headers {
						req.Header[name] = append([]string(nil), values...)
					}
					if proto == 2 {
						req.Header.Set(":protocol", "websocket")
					} else {
						req.Proto = "websocket"
					}
					if probe == "response-deny" {
						req.Header.Set("X-Policy", "deny")
					}
					before := hits.Load()
					resp, err := tr.RoundTrip(req)
					if err != nil {
						cancel()
						_ = pw.Close()
						t.Fatalf("handshake before client frames, probe=%s: %v", probe, err)
					}
					want := tc.status
					if resp.StatusCode != want || resp.ProtoMajor != proto {
						t.Errorf("probe=%s response=%d HTTP/%d, want %d HTTP/%d", probe, resp.StatusCode, resp.ProtoMajor, want, proto)
					}
					wantHits := before
					if tc.backend {
						wantHits++
					}
					if hits.Load() != wantHits {
						t.Errorf("probe=%s backend hits=%d, want %d", probe, hits.Load(), wantHits)
					}
					for name, values := range tc.responseHeaders {
						if got := resp.Header.Get(name); got != values[0] {
							t.Errorf("probe=%s response header %s=%q, want %q", probe, name, got, values[0])
						}
					}
					if want == http.StatusOK && resp.StatusCode == http.StatusOK {
						_, err = pw.Write([]byte{0x82, 0x82, 1, 2, 3, 4, 'o' ^ 1, 'k' ^ 2})
						if err != nil {
							t.Error(err)
						}
						frame := make([]byte, 4)
						if _, err := io.ReadFull(resp.Body, frame); err != nil || !bytes.Equal(frame, []byte{0x82, 2, 'o', 'k'}) {
							t.Errorf("uninspected tunnel frame=%x error=%v", frame, err)
						}
					}
					cancel()
					_ = pw.Close()
					_ = resp.Body.Close()
				}
				if connections.Load() != 1 {
					t.Errorf("policy denial closed shared HTTP/%d connection: %d connections", proto, connections.Load())
				}
				if mtls {
					for _, certs := range [][]tls.Certificate{nil, {wrongCert}} {
						badTLS := tlsClient.Clone()
						badTLS.Certificates = certs
						if len(certs) != 0 {
							badTLS.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
								return &certs[0], nil
							}
						}
						badTransport, closeBad := newTransport(badTLS)
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						req, _ := http.NewRequestWithContext(ctx, http.MethodConnect, endpoint+"/", nil)
						req.Header.Set("Sec-WebSocket-Version", "13")
						if proto == 2 {
							req.Header.Set(":protocol", "websocket")
						} else {
							req.Proto = "websocket"
						}
						before := hits.Load()
						resp, err := badTransport.RoundTrip(req)
						if err == nil {
							_ = resp.Body.Close()
							if len(certs) != 0 {
								t.Error("TLS accepted a presented, untrusted client certificate")
							}
							want := http.StatusForbidden
							if tc := cases[0]; tc.probe == "clean" && !tc.backend {
								want = tc.status
							}
							if resp.StatusCode != want {
								t.Errorf("unverified client admitted: %d", resp.StatusCode)
							}
						} else if len(certs) == 0 {
							t.Errorf("request-mode TLS should reach route certificate denial: %v", err)
						}
						cancel()
						closeBad()
						if hits.Load() != before {
							t.Error("unverified client reached the backend")
						}
					}
				}
			})
		}
	}
}

// Use the wire client because Go 1.27's HTTP/2 transport rejects :protocol.
type websocketPolicyHTTP2Client struct {
	tlsConfig *tls.Config
	conn      *tls.Conn
	framer    *http2.Framer
	decoder   *hpack.Decoder
	streamID  uint32
}

func (c *websocketPolicyHTTP2Client) RoundTrip(req *http.Request) (*http.Response, error) {
	if c.conn == nil {
		tlsConfig := c.tlsConfig.Clone()
		tlsConfig.NextProtos = []string{"h2"}
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", req.URL.Host, tlsConfig)
		if err != nil {
			return nil, err
		}
		c.conn = conn
		c.framer = http2.NewFramer(conn, conn)
		c.decoder = hpack.NewDecoder(4096, nil)
		if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
			return nil, err
		}
		if err := c.framer.WriteSettings(); err != nil {
			return nil, err
		}
	}
	deadline, _ := req.Context().Deadline()
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	c.streamID += 2
	streamID := c.streamID - 1
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	fields := []hpack.HeaderField{
		{Name: ":method", Value: req.Method}, {Name: ":scheme", Value: "https"},
		{Name: ":authority", Value: req.URL.Host}, {Name: ":path", Value: req.URL.RequestURI()},
		{Name: ":protocol", Value: "websocket"},
	}
	for name, values := range req.Header {
		if name == ":protocol" {
			continue
		}
		for _, value := range values {
			fields = append(fields, hpack.HeaderField{Name: strings.ToLower(name), Value: value})
		}
	}
	fields = append(fields, hpack.HeaderField{Name: "accept-encoding", Value: "gzip"})
	for _, field := range fields {
		if err := encoder.WriteField(field); err != nil {
			return nil, err
		}
	}
	if err := c.framer.WriteHeaders(http2.HeadersFrameParam{StreamID: streamID, EndHeaders: true, BlockFragment: block.Bytes()}); err != nil {
		return nil, err
	}
	response := &http.Response{Header: make(http.Header), ProtoMajor: 2, Proto: "HTTP/2.0", Request: req}
	for response.StatusCode == 0 {
		frame, err := c.framer.ReadFrame()
		if err != nil {
			return nil, err
		}
		switch f := frame.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				if err := c.framer.WriteSettingsAck(); err != nil {
					return nil, err
				}
			}
		case *http2.HeadersFrame:
			if f.StreamID != streamID {
				continue
			}
			if !f.HeadersEnded() {
				return nil, fmt.Errorf("unexpected continued handshake headers")
			}
			headers, err := c.decoder.DecodeFull(f.HeaderBlockFragment())
			if err != nil {
				return nil, err
			}
			for _, field := range headers {
				if field.Name == ":status" {
					response.StatusCode, err = strconv.Atoi(field.Value)
					if err != nil {
						return nil, err
					}
				} else {
					response.Header.Add(field.Name, field.Value)
				}
			}
		case *http2.RSTStreamFrame:
			if f.StreamID == streamID {
				return nil, fmt.Errorf("handshake reset: %s", f.ErrCode)
			}
		}
	}
	body := &websocketPolicyHTTP2Body{client: c, streamID: streamID, requestBody: req.Body, uploadDone: make(chan struct{})}
	go func() {
		defer close(body.uploadDone)
		if req.Body == nil {
			return
		}
		buf := make([]byte, 1024)
		for {
			n, err := req.Body.Read(buf)
			if n > 0 {
				if err := c.framer.WriteData(streamID, false, buf[:n]); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	response.Body = body
	return response, nil
}

type websocketPolicyHTTP2Body struct {
	client      *websocketPolicyHTTP2Client
	streamID    uint32
	requestBody io.ReadCloser
	uploadDone  chan struct{}
	pending     []byte
}

func (b *websocketPolicyHTTP2Body) Read(p []byte) (int, error) {
	for len(b.pending) == 0 {
		frame, err := b.client.framer.ReadFrame()
		if err != nil {
			return 0, err
		}
		if data, ok := frame.(*http2.DataFrame); ok && data.StreamID == b.streamID {
			b.pending = append(b.pending, data.Data()...)
			if len(b.pending) == 0 && data.StreamEnded() {
				return 0, io.EOF
			}
		}
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func (b *websocketPolicyHTTP2Body) Close() error {
	if b.requestBody != nil {
		_ = b.requestBody.Close()
	}
	<-b.uploadDone
	return b.client.framer.WriteRSTStream(b.streamID, http2.ErrCodeCancel)
}
