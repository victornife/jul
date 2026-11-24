// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme && http3 && linux

package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/middleware"
	"jul/internal/router"
	"jul/internal/upstream"
)

type dnsMatrixProtocol struct {
	name    string
	version uint16
	major   int
	alpn    string
	rsaOnly bool
}

type dnsMatrixServer struct {
	addr    string
	raw     *config.Config
	source  *stubSource
	reload  chan ReloadRequest
	release chan struct{}
	once    sync.Once
}

func runDNS01TLSMatrix(t *testing.T, manager *dnsACMEManager, settings *config.ACMEConfig, roots *x509.CertPool, ecdsaSerial, rsaSerial string) {
	t.Helper()
	t.Setenv("JUL_DNS_MATRIX_TSIG", testTSIGSecret)
	dir := t.TempDir()
	clientCA := newCA(t)
	otherCA := newNamedCA(t, "untrusted client CA")
	caFile := writePEM(t, dir, "clients.pem", clientCA.pem)
	crlFile := clientCA.writeCRL(t, dir, "clients.crl", 3)
	_, trusted := clientCA.clientCert(t, "trusted-client", 1, []string{"allowed.client"}, nil)
	_, badSAN := clientCA.clientCert(t, "wrong-san", 2, []string{"wrong.client"}, nil)
	_, revoked := clientCA.clientCert(t, "revoked", 3, []string{"allowed.client"}, nil)
	_, untrusted := otherCA.clientCert(t, "untrusted", 4, []string{"allowed.client"}, nil)
	protocols := []dnsMatrixProtocol{
		{name: "http1-tls12", version: tls.VersionTLS12, major: 1, alpn: "http/1.1"},
		{name: "http1-tls12-rsa-only", version: tls.VersionTLS12, major: 1, alpn: "http/1.1", rsaOnly: true},
		{name: "http1-tls13", version: tls.VersionTLS13, major: 1, alpn: "http/1.1"},
		{name: "http2-tls12", version: tls.VersionTLS12, major: 2, alpn: "h2"},
		{name: "http2-tls13", version: tls.VersionTLS13, major: 2, alpn: "h2"},
		{name: "http3-tls13", version: tls.VersionTLS13, major: 3, alpn: "h3"},
	}
	for _, mode := range []string{"none", "request", "require"} {
		t.Run(mode, func(t *testing.T) {
			fixture := startDNSMatrixServer(t, manager, settings, mode, "1.2", caFile, crlFile)
			addr := fixture.addr
			for _, protocol := range protocols {
				t.Run(protocol.name, func(t *testing.T) {
					serial := ecdsaSerial
					if protocol.rsaOnly {
						serial = rsaSerial
					}
					anonymous, err := dnsMatrixRequest(addr, roots, protocol, nil, "/")
					if mode == "require" {
						if err == nil {
							t.Fatal("require mode accepted an anonymous client")
						}
					} else {
						assertDNSMatrixResponse(t, anonymous, err, protocol, serial, http.StatusNoContent, "")
					}
					if mode == "none" {
						return
					}
					valid, err := dnsMatrixRequest(addr, roots, protocol, &trusted, "/protected")
					assertDNSMatrixResponse(t, valid, err, protocol, serial, http.StatusNoContent, "trusted-client")
					if mode == "request" {
						denied, err := dnsMatrixRequest(addr, roots, protocol, nil, "/protected")
						assertDNSMatrixResponse(t, denied, err, protocol, serial, http.StatusForbidden, "")
					}
					for label, cert := range map[string]*tls.Certificate{
						"untrusted": &untrusted, "revoked": &revoked, "wrong SAN": &badSAN,
					} {
						if _, err := dnsMatrixRequest(addr, roots, protocol, cert, "/"); err == nil {
							t.Errorf("%s client was accepted", label)
						}
					}
				})
			}
		})
	}
	t.Run("tls13-floor", func(t *testing.T) {
		fixture := startDNSMatrixServer(t, manager, settings, "none", "1.3", caFile, crlFile)
		if _, err := dnsMatrixRequest(fixture.addr, roots, protocols[0], nil, "/"); err == nil {
			t.Fatal("TLS 1.3-only listener accepted TLS 1.2")
		}
	})
}

func startDNSMatrixServer(t *testing.T, manager *dnsACMEManager, settings *config.ACMEConfig, mode, floor, caFile, crlFile string) *dnsMatrixServer {
	t.Helper()
	fixture := &dnsMatrixServer{addr: freePort(t), source: &stubSource{}, reload: make(chan ReloadRequest, 1), release: make(chan struct{})}
	addr := fixture.addr
	acmeSettings := *settings
	acmeSettings.Enabled = true
	acmeSettings.Domains = []string{"*.example.test", "example.test"}
	rfc := *settings.RFC2136
	rfc.TSIGSecret = "${env:JUL_DNS_MATRIX_TSIG}"
	acmeSettings.RFC2136 = &rfc
	tlsSettings := &config.TLSConfig{Enabled: true, MinVersion: floor, ACME: &acmeSettings}
	if mode != "none" {
		tlsSettings.ClientAuth = &config.ClientAuthConfig{
			Mode: mode, CAFile: caFile, CRLFile: crlFile, VerifySAN: []string{"allowed.client"},
		}
	}
	raw := &config.Config{
		Global: serverTestGlobalConfig(),
		Servers: []config.ServerConfig{{
			Listen: addr, ServerNames: []string{"example.test", "*.example.test"},
			TLS: tlsSettings, HTTP3: &config.HTTP3Config{Enabled: true},
		}},
	}
	for _, spec := range []struct {
		id, path string
		require  bool
	}{
		{"public", "/", false},
		{"protected", "/protected", mode != "none"},
		{"stream", "/stream", mode != "none"},
		{"public-stream", "/public-stream", false},
	} {
		id := spec.id
		raw.Servers[0].Locations = append(raw.Servers[0].Locations, config.LocationConfig{
			RouteID: &id, Match: config.MatchConfig{Type: "prefix", Path: spec.path},
			ProxyPass: "http://127.0.0.1:9001", RequireClientCert: spec.require,
		})
	}
	fixture.raw = raw
	fixture.source.set(raw, nil)
	candidate, err := config.NewCandidate(raw)
	if err != nil {
		t.Fatal(err)
	}
	factory := func(_ context.Context, cfg *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		verifier, err := BuildClientCertVerifier(cfg.Servers, addr)
		if err != nil {
			return nil, 0, nil, nil, err
		}
		builders := map[string]router.Builder{
			router.ActionProxy: func(_ config.ServerConfig, _ config.LocationConfig) (http.Handler, error) {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("X-SNI", r.TLS.ServerName)
					if identity := middleware.PeerCertIdentityFrom(r.Context()); identity != nil && identity.Verified {
						w.Header().Set("X-Client-Identity", identity.CN)
					}
					if r.URL.Path == "/stream" || r.URL.Path == "/public-stream" {
						w.WriteHeader(http.StatusOK)
						_, _ = io.WriteString(w, "before\n")
						_ = http.NewResponseController(w).Flush()
						select {
						case <-fixture.release:
							_, _ = io.WriteString(w, "after\n")
						case <-r.Context().Done():
						}
						return
					}
					w.WriteHeader(http.StatusNoContent)
				}), nil
			},
		}
		locMod := func(_ config.ServerConfig, loc config.LocationConfig) middleware.Middleware {
			return middleware.ClientCertWithVerifier(loc.RequireClientCert || mode == "require", middleware.ForwardCertNone, verifier)
		}
		rtr, err := router.New(cfg, builders, nil, locMod, quietLogger())
		if err != nil {
			return nil, 0, nil, nil, err
		}
		return map[string]http.Handler{addr: rtr.For(addr)}, uint64(time.Now().UnixNano()), func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	s := New(candidate.Effective, candidate.Raw, lifecycle.ComputeFingerprint(candidate.Effective), quietLogger(), factory, fixture.source, nil)
	s.ACME = manager
	s.exchanges.Grace = 150 * time.Millisecond
	s.exchanges.Bound = 400 * time.Millisecond
	ready := make(chan struct{})
	s.OnInitialGenerationReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, fixture.reload, candidate.Redaction) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("DNS-01 TLS listener failed: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("DNS-01 TLS listener did not start")
	}
	t.Cleanup(func() {
		fixture.once.Do(func() { close(fixture.release) })
		cancel()
		if err := <-done; err != nil {
			t.Errorf("DNS-01 TLS listener shutdown: %v", err)
		}
	})
	return fixture
}

func dnsMatrixSessionRequest(addr string, roots *x509.CertPool, cert tls.Certificate, cache tls.ClientSessionCache) (bool, error) {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, &tls.Config{
		RootCAs: roots, ServerName: "www.example.test", MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		NextProtos: []string{"http/1.1"}, Certificates: []tls.Certificate{cert}, ClientSessionCache: cache,
	})
	if err != nil {
		return false, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, "GET /protected HTTP/1.1\r\nHost: www.example.test\r\nConnection: close\r\n\r\n"); err != nil {
		return false, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return false, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return false, fmt.Errorf("session request status %d", resp.StatusCode)
	}
	return conn.ConnectionState().DidResume, nil
}

type dnsRenewalMTLS struct {
	fixture *dnsMatrixServer
	ca      *caFixture
	crlPath string
	cert    tls.Certificate
	cache   tls.ClientSessionCache
	quic    tls.ClientSessionCache
	streams []dnsMatrixStream
}

type dnsMatrixStream struct {
	client    *http.Client
	protected *bufio.Reader
	public    *bufio.Reader
}

func startDNSRenewalMTLS(t *testing.T, manager *dnsACMEManager, settings *config.ACMEConfig, roots *x509.CertPool) *dnsRenewalMTLS {
	t.Helper()
	dir := t.TempDir()
	ca := newCA(t)
	caFile := writePEM(t, dir, "clients.pem", ca.pem)
	crlPath := ca.writeCRL(t, dir, "clients.crl")
	_, cert := ca.clientCert(t, "renewal-client", 17, []string{"allowed.client"}, nil)
	fixture := startDNSMatrixServer(t, manager, settings, "request", "1.2", caFile, crlPath)
	state := &dnsRenewalMTLS{fixture: fixture, ca: ca, crlPath: crlPath, cert: cert, cache: tls.NewLRUClientSessionCache(4), quic: tls.NewLRUClientSessionCache(4)}
	if resumed, err := dnsMatrixSessionRequest(fixture.addr, roots, cert, state.cache); err != nil || resumed {
		t.Fatalf("initial client-auth session: resumed=%v err=%v", resumed, err)
	}
	if resumed, err := dnsMatrixSessionRequest(fixture.addr, roots, cert, state.cache); err != nil || !resumed {
		t.Fatalf("client-auth session resumption: resumed=%v err=%v", resumed, err)
	}
	if resumed, err := dnsMatrixQUICSessionRequest(fixture.addr, roots, cert, state.quic); err != nil || resumed {
		t.Fatalf("initial QUIC client-auth session: resumed=%v err=%v", resumed, err)
	}
	if resumed, err := dnsMatrixQUICSessionRequest(fixture.addr, roots, cert, state.quic); err != nil || !resumed {
		t.Fatalf("QUIC client-auth session resumption: resumed=%v err=%v", resumed, err)
	}
	for _, protocol := range []int{2, 3} {
		tlsConfig := &tls.Config{RootCAs: roots, ServerName: "www.example.test", Certificates: []tls.Certificate{cert}}
		var transport http.RoundTripper
		var closeTransport func()
		if protocol == 3 {
			h3 := &http3.Transport{TLSClientConfig: tlsConfig}
			transport = h3
			closeTransport = func() { _ = h3.Close() }
		} else {
			tcp := &http.Transport{TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true}
			transport = tcp
			closeTransport = tcp.CloseIdleConnections
		}
		t.Cleanup(closeTransport)
		client := &http.Client{Transport: transport, Timeout: 2 * time.Minute}
		open := func(path string) *bufio.Reader {
			req, err := http.NewRequest(http.MethodGet, "https://"+fixture.addr+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = "www.example.test"
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("start HTTP/%d stream: %v", protocol, err)
			}
			if resp.ProtoMajor != protocol || resp.StatusCode != http.StatusOK {
				t.Fatalf("HTTP/%d stream returned %s / %d", protocol, resp.Proto, resp.StatusCode)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			reader := bufio.NewReader(resp.Body)
			if line, err := reader.ReadString('\n'); err != nil || line != "before\n" {
				t.Fatalf("HTTP/%d initial stream: %q, %v", protocol, line, err)
			}
			return reader
		}
		state.streams = append(state.streams, dnsMatrixStream{client: client, protected: open("/stream"), public: open("/public-stream")})
	}
	return state
}

func (s *dnsRenewalMTLS) verifyAfterRenewal(t *testing.T, roots *x509.CertPool, serial string) {
	t.Helper()
	for _, protocol := range []dnsMatrixProtocol{
		{name: "http2-tls13", version: tls.VersionTLS13, major: 2, alpn: "h2"},
		{name: "http3-tls13", version: tls.VersionTLS13, major: 3, alpn: "h3"},
	} {
		resp, err := dnsMatrixRequest(s.fixture.addr, roots, protocol, &s.cert, "/protected")
		assertDNSMatrixResponse(t, resp, err, protocol, serial, http.StatusNoContent, "renewal-client")
	}
	if resumed, err := dnsMatrixSessionRequest(s.fixture.addr, roots, s.cert, s.cache); err != nil || !resumed {
		t.Fatalf("pre-renewal session after server certificate renewal: resumed=%v err=%v", resumed, err)
	}
	if resumed, err := dnsMatrixQUICSessionRequest(s.fixture.addr, roots, s.cert, s.quic); err != nil || !resumed {
		t.Fatalf("pre-renewal QUIC session after server certificate renewal: resumed=%v err=%v", resumed, err)
	}
	s.ca.writeCRL(t, "", s.crlPath, 17)
	s.fixture.source.set(s.fixture.raw, nil)
	result := make(chan ReloadResult, 1)
	s.fixture.reload <- ReloadRequest{ID: "dns01-client-revocation", Source: ReloadSourceFileWatch, Result: result}
	select {
	case outcome := <-result:
		if outcome.Outcome != ReloadAppliedLive {
			t.Fatalf("client CRL reload: %+v", outcome)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("client CRL reload did not complete")
	}
	for _, cache := range []tls.ClientSessionCache{nil, s.cache} {
		if _, err := dnsMatrixSessionRequest(s.fixture.addr, roots, s.cert, cache); err == nil {
			t.Fatal("revoked client was accepted on a fresh or resumed handshake")
		}
	}
	for _, cache := range []tls.ClientSessionCache{nil, s.quic} {
		if _, err := dnsMatrixQUICSessionRequest(s.fixture.addr, roots, s.cert, cache); err == nil {
			t.Fatal("revoked client was accepted on a fresh or resumed QUIC handshake")
		}
	}
	for _, stream := range s.streams {
		if _, err := io.ReadAll(stream.protected); err == nil {
			t.Fatal("protected in-flight stream was not retired after client revocation")
		}
		req, err := http.NewRequest(http.MethodGet, "https://"+s.fixture.addr+"/protected", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "www.example.test"
		var reused atomic.Bool
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) },
		}))
		resp, err := stream.client.Do(req)
		if err != nil {
			t.Fatalf("recheck on established connection: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || !reused.Load() {
			t.Fatalf("revoked client on HTTP/%d connection: status %d, reused=%v", resp.ProtoMajor, resp.StatusCode, reused.Load())
		}
		if _, err := io.ReadAll(stream.public); err == nil {
			t.Fatal("in-flight stream was not retired after listener-wide CRL narrowing")
		}
	}
}

func dnsMatrixQUICSessionRequest(addr string, roots *x509.CertPool, cert tls.Certificate, cache tls.ClientSessionCache) (bool, error) {
	transport := &http3.Transport{TLSClientConfig: &tls.Config{
		RootCAs: roots, ServerName: "www.example.test", Certificates: []tls.Certificate{cert},
		ClientSessionCache: cache, MinVersion: tls.VersionTLS13,
	}}
	defer transport.Close()
	req, err := http.NewRequest(http.MethodGet, "https://"+addr+"/protected", nil)
	if err != nil {
		return false, err
	}
	req.Host = "www.example.test"
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return false, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.TLS == nil {
		return false, fmt.Errorf("QUIC session request status %d", resp.StatusCode)
	}
	return resp.TLS.DidResume, nil
}

func dnsMatrixRequest(addr string, roots *x509.CertPool, protocol dnsMatrixProtocol, cert *tls.Certificate, path string) (*http.Response, error) {
	tlsConfig := &tls.Config{
		RootCAs: roots, ServerName: "www.example.test",
		MinVersion: protocol.version, MaxVersion: protocol.version,
	}
	if protocol.rsaOnly {
		tlsConfig.CipherSuites = []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}
	}
	if cert != nil {
		tlsConfig.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return cert, nil }
	}
	var transport http.RoundTripper
	var closeTransport func()
	if protocol.major == 3 {
		h3 := &http3.Transport{TLSClientConfig: tlsConfig}
		transport = h3
		closeTransport = func() { _ = h3.Close() }
	} else {
		tlsConfig.NextProtos = []string{protocol.alpn}
		tcp := &http.Transport{TLSClientConfig: tlsConfig, ForceAttemptHTTP2: protocol.major == 2, DisableKeepAlives: true}
		transport = tcp
		closeTransport = tcp.CloseIdleConnections
	}
	defer closeTransport()
	req, err := http.NewRequest(http.MethodGet, "https://"+addr+path, nil)
	if err != nil {
		return nil, err
	}
	req.Host = "www.example.test"
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp, nil
}

func assertDNSMatrixResponse(t *testing.T, resp *http.Response, err error, protocol dnsMatrixProtocol, serial string, status int, identity string) {
	t.Helper()
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != status || resp.ProtoMajor != protocol.major || resp.TLS == nil ||
		resp.TLS.Version != protocol.version || resp.TLS.NegotiatedProtocol != protocol.alpn ||
		len(resp.TLS.VerifiedChains) == 0 || len(resp.TLS.PeerCertificates) < 2 ||
		resp.TLS.PeerCertificates[0].SerialNumber.String() != serial ||
		resp.TLS.ServerName != "www.example.test" ||
		(status == http.StatusNoContent && resp.Header.Get("X-SNI") != "www.example.test") ||
		resp.Header.Get("X-Client-Identity") != identity {
		t.Fatalf("unexpected %s response: status=%d proto=%s TLS=%v SNI=%q identity=%q; expected serial %s",
			protocol.name, resp.StatusCode, resp.Proto, resp.TLS, resp.Header.Get("X-SNI"), resp.Header.Get("X-Client-Identity"), serial)
	}
	t.Logf("%s TLS=%#x ALPN=%s status=%d identity=%s", protocol.name, resp.TLS.Version, resp.TLS.NegotiatedProtocol, status, identity)
}
