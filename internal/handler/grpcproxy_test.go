// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"jul/internal/config"
	"jul/internal/middleware"
)

// rawCodec is a transparent gRPC codec that carries opaque byte payloads, so the
// test client and the echo backend exchange messages without a .proto schema.
// The passthrough proxy never touches the payload, so this exercises the real
// HTTP/2 path end to end.
type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) {
	switch b := v.(type) {
	case []byte:
		return b, nil
	case *[]byte:
		return *b, nil
	}
	return nil, fmt.Errorf("rawCodec: unexpected type %T", v)
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	p, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("rawCodec: unexpected type %T", v)
	}
	*p = append((*p)[:0], data...)
	return nil
}

func (rawCodec) Name() string { return "raw" }

func grpcTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// echoStream echoes every received frame back to the caller and sets a trailer,
// so a test can assert that both streaming frames and trailers survive the
// passthrough proxy.
func echoStream(_ any, stream grpc.ServerStream) error {
	stream.SetTrailer(metadata.Pairs("x-echo-trailer", "ok"))
	for {
		var msg []byte
		if err := stream.RecvMsg(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := stream.SendMsg(&msg); err != nil {
			return err
		}
	}
}

// startGRPCEcho starts a cleartext (h2c) gRPC server that echoes any method and
// returns its address.
func startGRPCEcho(t testing.TB) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer(
		grpc.UnknownServiceHandler(echoStream),
		grpc.ForceServerCodec(rawCodec{}),
	)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

// startGRPCProxyFront builds the gRPC passthrough handler for backendAddr and
// serves it over h2c on a fresh listener, returning the front address and the
// stream counter the handler increments per forwarded call.
func startGRPCProxyFront(t testing.TB, backendAddr string) (string, *atomic.Int64) {
	t.Helper()
	var streams atomic.Int64
	loc := config.LocationConfig{
		ProxyPass: "http://" + backendAddr,
		GRPC:      true,
	}
	h, err := NewGRPCProxy(context.Background(), config.ServerConfig{}, loc, map[string]config.UpstreamConfig{}, nil, grpcTestLogger(), func() { streams.Add(1) })
	if err != nil {
		t.Fatalf("NewGRPCProxy: %v", err)
	}

	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen front: %v", err)
	}
	// Serve prior-knowledge cleartext HTTP/2 (h2c) via the standard library's
	// Protocols negotiation, matching how the real server enables h2c.
	srv := &http.Server{Handler: h}
	var proto http.Protocols
	proto.SetHTTP1(true)
	proto.SetUnencryptedHTTP2(true)
	srv.Protocols = &proto
	go func() { _ = srv.Serve(front) }()
	t.Cleanup(func() { _ = srv.Close() })
	return front.Addr().String(), &streams
}

func dialGRPC(t testing.TB, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(
		"passthrough:///"+addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestLongLivedNativeGRPCReloadAndShutdown(t *testing.T) {
	for _, phase := range []string{"reload_and_forced_retirement", "shutdown"} {
		t.Run(phase, func(t *testing.T) {
			backend := startGRPCEcho(t)
			proxy, err := NewGRPCProxy(context.Background(), config.ServerConfig{}, config.LocationConfig{ProxyPass: "http://" + backend, GRPC: true}, nil, nil, grpcTestLogger(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closer, ok := proxy.(io.Closer); ok {
					_ = closer.Close()
				}
			})
			fixture := newLongLivedServer(t, proxy)
			connection := dialGRPC(t, fixture.address)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, "/echo.Echo/Stream")
			if err != nil {
				t.Fatal(err)
			}
			echo := func(value string) {
				t.Helper()
				payload := []byte(value)
				if err := stream.SendMsg(&payload); err != nil {
					t.Fatal(err)
				}
				var received []byte
				if err := stream.RecvMsg(&received); err != nil {
					t.Fatal(err)
				}
				if string(received) != value {
					t.Fatalf("echo=%q, want %q", received, value)
				}
			}
			echo("before")
			if phase == "shutdown" {
				fixture.stop()
				payload := []byte("after shutdown")
				if err := stream.SendMsg(&payload); err == nil {
					var received []byte
					if err := stream.RecvMsg(&received); err == nil {
						t.Fatal("native gRPC stream still alive after shutdown grace")
					}
				}
			} else {
				fixture.reload(t)
				echo("after reload")
				fixture.waitRetired(t)
				echo("after forced handler CloseIdleConnections")
			}
		})
	}
}

func TestGRPCProxyZeroBackendDiscoveryNoPanic(t *testing.T) {
	// A discovery-backed gRPC upstream can have zero backends at build time;
	// building the passthrough handler must not panic (regression:
	// NewGRPCProxy previously indexed pool.Backends()[0]).
	loc := config.LocationConfig{ProxyPass: "http://disco", GRPC: true}
	ups := map[string]config.UpstreamConfig{
		"disco": {
			Name:      "disco",
			Discovery: &config.DiscoveryConfig{Type: "dns", Target: "svc.internal:50051"},
		},
	}
	if _, err := NewGRPCProxy(context.Background(), config.ServerConfig{}, loc, ups, nil, grpcTestLogger(), nil); err != nil {
		t.Fatalf("NewGRPCProxy with empty discovery pool: %v", err)
	}
}

func TestGRPCProxyUnary(t *testing.T) {
	backend := startGRPCEcho(t)
	front, streams := startGRPCProxyFront(t, backend)
	conn := dialGRPC(t, front)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := []byte("hello gRPC")
	var reply []byte
	var trailer metadata.MD
	if err := conn.Invoke(ctx, "/echo.Echo/Unary", req, &reply, grpc.Trailer(&trailer)); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if string(reply) != string(req) {
		t.Errorf("reply = %q, want %q", reply, req)
	}
	// Trailers (grpc-status lives here too) must survive the passthrough.
	if got := trailer.Get("x-echo-trailer"); len(got) != 1 || got[0] != "ok" {
		t.Errorf("trailer x-echo-trailer = %v, want [ok]", got)
	}
	if streams.Load() != 1 {
		t.Errorf("stream counter = %d, want 1", streams.Load())
	}
}

func TestGRPCProxyServerStream(t *testing.T) {
	backend := startGRPCEcho(t)
	front, _ := startGRPCProxyFront(t, backend)
	conn := dialGRPC(t, front)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	desc := &grpc.StreamDesc{StreamName: "Stream", ServerStreams: true, ClientStreams: true}
	cs, err := conn.NewStream(ctx, desc, "/echo.Echo/Stream")
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}

	want := []string{"one", "two", "three"}
	for _, m := range want {
		msg := []byte(m)
		if err := cs.SendMsg(&msg); err != nil {
			t.Fatalf("send %q: %v", m, err)
		}
		// Each frame must come back before the next is sent: a buffering proxy
		// would deadlock here, proving frames flush incrementally.
		var reply []byte
		if err := cs.RecvMsg(&reply); err != nil {
			t.Fatalf("recv: %v", err)
		}
		if string(reply) != m {
			t.Errorf("reply = %q, want %q", reply, m)
		}
	}
	if err := cs.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
	var tail []byte
	if err := cs.RecvMsg(&tail); !errors.Is(err, io.EOF) {
		t.Errorf("final recv = %v, want EOF", err)
	}
}

func TestNewGRPCTransportScheme(t *testing.T) {
	loc := config.LocationConfig{}

	h2cT := newGRPCTransport(loc, false, nil)
	if !h2cT.AllowHTTP {
		t.Error("h2c transport should set AllowHTTP for cleartext HTTP/2")
	}
	if h2cT.DialTLSContext == nil {
		t.Error("h2c transport should dial via DialTLSContext")
	}

	tlsT := newGRPCTransport(loc, true, nil)
	if tlsT.AllowHTTP {
		t.Error("TLS transport must not set AllowHTTP")
	}
	if tlsT.DialTLSContext == nil {
		t.Error("TLS transport should set DialTLSContext for the connect timeout")
	}
}

func TestGRPCProxyHeaderForwardingAndVariableExpansion(t *testing.T) {
	var capturedMD metadata.MD
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer(
		grpc.UnknownServiceHandler(func(srv any, stream grpc.ServerStream) error {
			if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
				capturedMD = md.Copy()
			}
			return echoStream(srv, stream)
		}),
		grpc.ForceServerCodec(rawCodec{}),
	)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	loc := config.LocationConfig{
		ProxyPass: "http://" + lis.Addr().String(),
		GRPC:      true,
		Headers: map[string]string{
			"Host":        "override.grpc.internal",
			"X-Real-IP":   "$remote_addr",
			"X-Peer-IP":   "$realip_remote_addr",
			"X-Chain":     "$proxy_add_x_forwarded_for",
			"X-Orig-Host": "$host",
			"X-Proto":     "$scheme",
			"X-SSL-Info":  "$ssl_client_cn/$ssl_client_verify",
			"X-Static":    "static-token",
		},
	}
	h, err := NewGRPCProxy(context.Background(), config.ServerConfig{}, loc, map[string]config.UpstreamConfig{}, nil, grpcTestLogger(), nil)
	if err != nil {
		t.Fatalf("NewGRPCProxy: %v", err)
	}

	id := &middleware.PeerCertIdentity{
		Verified:  true,
		SubjectDN: "CN=grpc-client,O=Jul",
		CN:        "grpc-client",
		Raw:       []byte{0x30, 0x82, 0x01, 0x0a},
		Chain:     [][]byte{{0x30, 0x82, 0x01, 0x0a}, {0x30, 0x82, 0x02, 0x0b}},
	}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(middleware.WithPeerCertIdentity(r.Context(), id))
		h.ServeHTTP(w, r)
	})

	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen front: %v", err)
	}
	srv := &http.Server{Handler: wrapped}
	var proto http.Protocols
	proto.SetHTTP1(true)
	proto.SetUnencryptedHTTP2(true)
	srv.Protocols = &proto
	go func() { _ = srv.Serve(front) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn := dialGRPC(t, front.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Inject a spoofed client-cert header from the client; setCanonicalXForwardedWithVars
	// must overwrite it with Jul's own RFC 9440 assertion from PeerCertIdentity.
	ctx = metadata.AppendToOutgoingContext(ctx,
		"x-forwarded-client-cert", "spoofed-xfcc",
		"client-cert", ":c3Bvb2ZlZA==:",
	)

	req := []byte("ping")
	var reply []byte
	if err := conn.Invoke(ctx, "/echo.Echo/Unary", req, &reply); err != nil {
		t.Fatalf("invoke: %v", err)
	}

	assertMD := func(key, want string) {
		t.Helper()
		vals := capturedMD.Get(key)
		if len(vals) != 1 || vals[0] != want {
			t.Errorf("metadata[%q] = %v, want [%q]", key, vals, want)
		}
	}

	assertMD(":authority", "override.grpc.internal")
	assertMD("x-real-ip", "127.0.0.1")
	assertMD("x-peer-ip", "127.0.0.1")
	assertMD("x-chain", "127.0.0.1")
	assertMD("x-orig-host", front.Addr().String())
	assertMD("x-proto", "http")
	assertMD("x-ssl-info", "grpc-client/SUCCESS")
	assertMD("x-static", "static-token")
	assertMD("x-forwarded-for", "127.0.0.1")
	assertMD("x-forwarded-proto", "http")
	assertMD("client-cert", certItem(id.Raw))
	if got := capturedMD.Get("x-forwarded-client-cert"); len(got) != 0 {
		t.Errorf("x-forwarded-client-cert was not stripped: %v", got)
	}
}

func TestGRPCProxyRewriteParityWithLegacy(t *testing.T) {
	legacyRewrite := func(pr *httputil.ProxyRequest, target *url.URL, headers map[string]string) {
		pr.SetURL(target)
		pr.SetXForwarded()
		client, peer := forwardedAddrs(pr.In)
		if chain := forwardedChain(client, peer); chain != "" {
			pr.Out.Header.Set("X-Forwarded-For", chain)
		}
		for _, h := range certAssertionHeaders {
			pr.Out.Header.Del(h)
		}
		if id := middleware.PeerCertIdentityFrom(pr.In.Context()); id != nil && len(id.Raw) > 0 {
			pr.Out.Header.Set("Client-Cert", certItem(id.Raw))
			if len(id.Chain) > 0 {
				items := make([]string, 0, len(id.Chain))
				for _, der := range id.Chain {
					items = append(items, certItem(der))
				}
				pr.Out.Header.Set("Client-Cert-Chain", strings.Join(items, ", "))
			}
		}
		for name, tmpl := range headers {
			val := tmpl
			if strings.Contains(tmpl, "$") {
				c, p := forwardedAddrs(pr.In)
				scheme := "http"
				if pr.In.TLS != nil {
					scheme = "https"
				}
				pairs := []string{
					"$proxy_add_x_forwarded_for", forwardedChain(c, p),
					"$realip_remote_addr", p,
					"$remote_addr", c,
					"$host", pr.In.Host,
					"$scheme", scheme,
				}
				if id := middleware.PeerCertIdentityFrom(pr.In.Context()); id != nil {
					pairs = append(pairs,
						"$ssl_client_s_dn", id.SubjectDN,
						"$ssl_client_i_dn", id.IssuerDN,
						"$ssl_client_cn", id.CN,
						"$ssl_client_serial", id.Serial,
						"$ssl_client_fingerprint", id.Fingerprint,
						"$ssl_client_san", id.SANs,
						"$ssl_client_verify", "SUCCESS",
					)
				} else {
					pairs = append(pairs,
						"$ssl_client_s_dn", "",
						"$ssl_client_i_dn", "",
						"$ssl_client_cn", "",
						"$ssl_client_serial", "",
						"$ssl_client_fingerprint", "",
						"$ssl_client_san", "",
						"$ssl_client_verify", "NONE",
					)
				}
				val = strings.NewReplacer(pairs...).Replace(tmpl)
			}
			if strings.EqualFold(name, "Host") {
				pr.Out.Host = val
				continue
			}
			pr.Out.Header.Set(name, val)
		}
	}

	target := &url.URL{Scheme: "https", Path: ""}
	headerSets := []map[string]string{
		nil,
		{
			"host":              "$host",
			"x-real-ip":         "$remote_addr",
			"X-Peer":            "$realip_remote_addr",
			"X-Forwarded-For":   "edge-$proxy_add_x_forwarded_for",
			"X-Scheme":          "$scheme",
			"X-SSL-Summary":     "$ssl_client_s_dn|$ssl_client_i_dn|$ssl_client_cn|$ssl_client_serial|$ssl_client_fingerprint|$ssl_client_san|$ssl_client_verify",
			"X-Literal-Unknown": "token-$unknown-$host",
		},
	}

	certID := &middleware.PeerCertIdentity{
		Verified:    true,
		SubjectDN:   "CN=svc,O=Jul",
		IssuerDN:    "CN=RootCA",
		CN:          "svc",
		Serial:      "99",
		Fingerprint: "deadbeef",
		SANs:        "svc.internal",
		Raw:         []byte{1, 2, 3},
		Chain:       [][]byte{{1, 2, 3}, {4, 5, 6}},
	}

	for _, withTLS := range []bool{false, true} {
		for _, withCert := range []bool{false, true} {
			for _, hdrs := range headerSets {
				compiled := compileProxyHeaders(hdrs)
				makeReq := func() (*http.Request, *http.Request) {
					in := httptest.NewRequest(http.MethodPost, "http://grpc.edge.example:8443/pkg.Svc/Method", nil)
					in.Host = "grpc.edge.example:8443"
					in.RemoteAddr = "203.0.113.9:40000"
					if withTLS {
						in.TLS = &tls.ConnectionState{}
					}
					if withCert {
						in = in.WithContext(middleware.WithPeerCertIdentity(in.Context(), certID))
					}
					in.Header.Set("Forwarded", "for=1.2.3.4")
					in.Header.Set("X-Forwarded-For", "1.2.3.4")
					in.Header.Set("Client-Cert", ":spoof:")
					in.Header.Set("Client-Cert-Chain", ":spoofchain:")
					in.Header.Set("X-Forwarded-Client-Cert", "By=spoof")
					in.Header.Set("Grpc-Timeout", "5S")
					out := in.Clone(in.Context())
					return in, out
				}

				inNew, outNew := makeReq()
				prNew := &httputil.ProxyRequest{In: inNew, Out: outNew}
				prNew.SetURL(target)
				vars := proxyVarContext{in: prNew.In}
				setCanonicalXForwardedWithVars(prNew, &vars)
				applyCompiledProxyHeaders(prNew, compiled, &vars)

				inOld, outOld := makeReq()
				prOld := &httputil.ProxyRequest{In: inOld, Out: outOld}
				legacyRewrite(prOld, target, hdrs)

				if outNew.Host != outOld.Host {
					t.Fatalf("Out.Host = %q, want %q", outNew.Host, outOld.Host)
				}
				if !reflect.DeepEqual(outNew.Header, outOld.Header) {
					t.Fatalf("Out.Header mismatch:\n got: %#v\nwant: %#v", outNew.Header, outOld.Header)
				}
			}
		}
	}
}
