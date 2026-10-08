// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"jul/internal/config"
	"jul/internal/respwriter"
)

type timeoutPipeListener struct {
	connection net.Conn
	accepted   bool
	closed     chan struct{}
	closeOnce  sync.Once
}

func (listener *timeoutPipeListener) Accept() (net.Conn, error) {
	if !listener.accepted {
		listener.accepted = true
		return listener.connection, nil
	}
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *timeoutPipeListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

func (listener *timeoutPipeListener) Addr() net.Addr { return listener.connection.LocalAddr() }

type timeoutPipeConnection struct {
	net.Conn
	writes chan error
}

func (connection *timeoutPipeConnection) NetConn() net.Conn { return connection.Conn }

func (connection *timeoutPipeConnection) Write(body []byte) (int, error) {
	count, err := connection.Conn.Write(body)
	connection.writes <- err
	return count, err
}

func sendTimeoutPipeServer(t *testing.T, handler http.Handler) (net.Conn, *bufio.Reader, <-chan error) {
	t.Helper()
	serverConnection, client := net.Pipe()
	connection := &timeoutPipeConnection{Conn: respwriter.WithSendTimeoutConnection(serverConnection), writes: make(chan error, 32)}
	listener := &timeoutPipeListener{connection: connection, closed: make(chan struct{})}
	configuration := &config.Config{Servers: []config.ServerConfig{{Listen: "pipe", SendTimeout: config.Duration(100 * time.Millisecond), Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "."}}}}}
	router, err := New(configuration, map[string]Builder{ActionStatic: func(config.ServerConfig, config.LocationConfig) (http.Handler, error) { return handler, nil }}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: router.For("pipe"), ConnContext: respwriter.SendTimeoutContext, ConnState: respwriter.SendTimeoutConnState}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close(); <-done })
	go func() {
		_, _ = io.WriteString(client, "GET / HTTP/1.1\r\nHost: example.test\r\nConnection: close\r\n\r\n")
	}()
	return client, bufio.NewReader(client), connection.writes
}

func TestSendTimeoutBoundsFinalTrailers(t *testing.T) {
	client, reader, writes := sendTimeoutPipeServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Trailer", "X-Final")
		_, _ = io.WriteString(writer, "first\n")
		writer.Header().Set("X-Final", "done")
	}))
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, len("first\n"))
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	if err := <-writes; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-writes:
		if err == nil {
			t.Fatal("final trailers completed while the client stopped reading")
		}
	case <-time.After(time.Second):
		_ = client.Close()
		t.Fatal("final chunk/trailers escaped the configured inactivity timeout")
	}
}

func TestSendTimeoutBoundsInformationalHeaders(t *testing.T) {
	_, _, writes := sendTimeoutPipeServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Link", "</style.css>; rel=preload")
		writer.WriteHeader(http.StatusEarlyHints)
	}))
	select {
	case err := <-writes:
		if err == nil {
			t.Fatal("informational headers completed while the client did not read")
		}
	case <-time.After(time.Second):
		t.Fatal("103 headers escaped the configured inactivity timeout")
	}
}

func TestSendTimeoutAllowsPartialProgressWithinOneWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		result := make(chan error, 1)
		stopReading := make(chan struct{})
		defer close(stopReading)
		started := time.Now()
		client, _, _ := sendTimeoutPipeServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, err := writer.Write([]byte(strings.Repeat("x", 32*1024)))
			result <- err
		}))
		go func() {
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			buffer := make([]byte, 1024)
			for {
				select {
				case <-stopReading:
					return
				case <-ticker.C:
				}
				if _, err := client.Read(buffer); err != nil {
					return
				}
			}
		}()
		select {
		case err := <-result:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("steady 20ms reader was cut inside one Write: %v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(started) <= 100*time.Millisecond {
				t.Fatal("large-write probe did not outlast its inactivity interval")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("progressing response failed to complete")
		}
	})
}

func sendTimeoutTestServer(t *testing.T, protocol string, timeout time.Duration, handler http.Handler) (*httptest.Server, *http.Client) {
	t.Helper()
	configuration := &config.Config{Servers: []config.ServerConfig{{
		Listen: "test", SendTimeout: config.Duration(timeout),
		Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "."}},
	}}}
	router, err := New(configuration, map[string]Builder{ActionStatic: func(config.ServerConfig, config.LocationConfig) (http.Handler, error) { return handler, nil }}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(router.For("test"))
	server.Listener = respwriter.WithSendTimeoutListener(server.Listener)
	server.Config.ConnContext = respwriter.SendTimeoutContext
	server.Config.ConnState = respwriter.SendTimeoutConnState
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(protocol == "h2")
	protocols.SetUnencryptedHTTP2(protocol == "h2c")
	server.Config.Protocols = protocols
	if protocol == "h2" {
		server.EnableHTTP2 = true
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 10 * time.Second
	if protocol == "h2c" {
		clientProtocols := new(http.Protocols)
		clientProtocols.SetUnencryptedHTTP2(true)
		transport := &http.Transport{Protocols: clientProtocols}
		t.Cleanup(transport.CloseIdleConnections)
		client = &http.Client{Transport: transport, Timeout: 10 * time.Second}
	}
	return server, client
}

func TestSendTimeoutStopsStalledReadersAcrossProtocols(t *testing.T) {
	for _, protocol := range []string{"http1", "h2", "h2c"} {
		t.Run(protocol, func(t *testing.T) {
			result := make(chan error, 1)
			server, client := sendTimeoutTestServer(t, protocol, 300*time.Millisecond, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				controller := http.NewResponseController(writer)
				writer.WriteHeader(http.StatusOK)
				if err := controller.Flush(); err != nil {
					result <- err
					return
				}
				chunk := []byte(strings.Repeat("x", 32*1024))
				for request.Context().Err() == nil {
					if _, err := writer.Write(chunk); err != nil {
						result <- err
						return
					}
					if err := controller.Flush(); err != nil {
						result <- err
						return
					}
				}
				result <- request.Context().Err()
			}))
			response, err := client.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if (protocol == "http1" && response.ProtoMajor != 1) || (protocol != "http1" && response.ProtoMajor != 2) {
				t.Fatalf("unexpected protocol: %s", response.Proto)
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("stalled reader ended without a write failure")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("stalled reader was not stopped by the inactivity deadline")
			}
		})
	}
}

func TestSendTimeoutAllowsProgressingStreamsAcrossProtocols(t *testing.T) {
	for _, protocol := range []string{"http1", "h2", "h2c"} {
		t.Run(protocol, func(t *testing.T) {
			server, client := sendTimeoutTestServer(t, protocol, 400*time.Millisecond, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				controller := http.NewResponseController(writer)
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				for event := 0; event < 10; event++ {
					select {
					case <-request.Context().Done():
						return
					case <-ticker.C:
					}
					if _, err := fmt.Fprintf(writer, "data: %d\n\n", event); err != nil {
						return
					}
					if err := controller.Flush(); err != nil {
						return
					}
				}
			}))
			response, err := client.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || strings.Count(string(body), "data:") != 10 {
				t.Fatalf("progressing stream was cut: events=%d err=%v", strings.Count(string(body), "data:"), err)
			}
		})
	}
}

func TestSendTimeoutDoesNotExpireBetweenWrites(t *testing.T) {
	for _, protocol := range []string{"http1", "h2", "h2c"} {
		t.Run(protocol, func(t *testing.T) {
			server, client := sendTimeoutTestServer(t, protocol, 100*time.Millisecond, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				controller := http.NewResponseController(writer)
				if _, err := io.WriteString(writer, "first\n"); err != nil {
					return
				}
				if err := controller.Flush(); err != nil {
					return
				}
				select {
				case <-request.Context().Done():
					return
				case <-time.After(300 * time.Millisecond):
				}
				_, _ = io.WriteString(writer, "second\n")
				_ = controller.Flush()
			}))
			response, err := client.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != "first\nsecond\n" {
				t.Fatalf("non-writing gap cut the stream: body=%q err=%v", body, err)
			}
		})
	}
}

type timeoutResponseRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (writer *timeoutResponseRecorder) SetWriteDeadline(deadline time.Time) error {
	writer.deadlines = append(writer.deadlines, deadline)
	return nil
}

func TestSendTimeoutSelectionAndGeneration(t *testing.T) {
	zero := config.Duration(0)
	override := config.Duration(2 * time.Second)
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80", SendTimeout: config.Duration(time.Second),
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "exact", Path: "/inherit"}, Root: "."},
			{Match: config.MatchConfig{Type: "exact", Path: "/override"}, Root: ".", SendTimeout: &override},
			{Match: config.MatchConfig{Type: "exact", Path: "/disabled"}, Root: ".", SendTimeout: &zero},
		},
	}, {
		Listen: "127.0.0.1:80", ServerNames: []string{"other.example"}, SendTimeout: config.Duration(3 * time.Second),
		Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "."}},
	}}}
	original := testRouter(t, cfg)
	cfg.Servers[0].SendTimeout = config.Duration(4 * time.Second)
	replacement := testRouter(t, cfg)
	for _, test := range []struct {
		name   string
		router *Router
		host   string
		path   string
		want   time.Duration
	}{
		{"inherit", original, "default.example", "/inherit", time.Second},
		{"override", original, "default.example", "/override", 2 * time.Second},
		{"disabled", original, "default.example", "/disabled", 0},
		{"unmatched", original, "default.example", "/missing", time.Second},
		{"vhost", original, "other.example", "/", 3 * time.Second},
		{"replacement", replacement, "default.example", "/inherit", 4 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &timeoutResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
			before := time.Now()
			test.router.For("127.0.0.1:80").ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "http://"+test.host+test.path, nil))
			if test.want == 0 {
				if len(writer.deadlines) != 0 {
					t.Fatalf("disabled timeout set deadlines: %v", writer.deadlines)
				}
				return
			}
			if len(writer.deadlines) == 0 || writer.deadlines[0].Before(before.Add(test.want)) || writer.deadlines[0].After(time.Now().Add(test.want)) {
				t.Fatalf("deadlines=%v, want interval %v", writer.deadlines, test.want)
			}
		})
	}
}

func TestSendTimeoutKeepsListenerAbsoluteDeadlineAcrossVhosts(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{
		{Listen: "test", WriteTimeout: config.Duration(time.Second), Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "."}}},
		{Listen: "test", ServerNames: []string{"other.example"}, SendTimeout: config.Duration(time.Hour), Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "."}}},
	}}
	writer := &timeoutResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()
	testRouter(t, cfg).For("test").ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "http://other.example/", nil))
	if len(writer.deadlines) == 0 {
		t.Fatal("no deadlines set")
	}
	for _, deadline := range writer.deadlines {
		if deadline.IsZero() || deadline.Before(before.Add(time.Second)) || deadline.After(time.Now().Add(time.Second)) {
			t.Fatalf("listener absolute deadline lost: %v", deadline)
		}
	}
}

// echoBuilder returns a handler that writes a tag identifying the matched
// location path, so tests can assert which route was selected.
func echoBuilder(_ config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
	tag := loc.Match.Path
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Matched", tag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(tag))
	}), nil
}

func testRouter(t *testing.T, cfg *config.Config) *Router {
	t.Helper()
	builders := map[string]Builder{
		ActionStatic: echoBuilder,
		ActionProxy:  echoBuilder,
	}
	r, err := New(cfg, builders, echoBuilder, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func do(t *testing.T, r *Router, addr, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	r.For(addr).ServeHTTP(rec, req)
	return rec
}

func TestHostRouting(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{
		{
			Listen:      "127.0.0.1:80",
			ServerNames: []string{"a.example.com"},
			Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/a"}},
		},
		{
			Listen:      "127.0.0.1:80",
			ServerNames: []string{"b.example.com"},
			Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/b"}},
		},
	}}
	r := testRouter(t, cfg)

	if got := do(t, r, "127.0.0.1:80", "a.example.com", "/").Header().Get("X-Matched"); got != "/" {
		t.Fatalf("host a matched %q", got)
	}
	// Unknown host falls back to the first (default) server, which still serves.
	if rec := do(t, r, "127.0.0.1:80", "unknown.example.com", "/"); rec.Code != http.StatusOK {
		t.Fatalf("unknown host status = %d", rec.Code)
	}
}

func TestWildcardHost(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{
		{
			Listen:      "127.0.0.1:80",
			ServerNames: []string{"fallback"},
			Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/f"}},
		},
		{
			Listen:      "127.0.0.1:80",
			ServerNames: []string{"*.example.com"},
			Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "exact", Path: "/"}, Root: "/wild"}},
		},
	}}
	r := testRouter(t, cfg)
	rec := do(t, r, "127.0.0.1:80", "api.example.com", "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("wildcard host status = %d", rec.Code)
	}
}

func TestLocationPrecedence(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/root"},
			{Match: config.MatchConfig{Type: "prefix", Path: "/api/"}, Root: "/api"},
			{Match: config.MatchConfig{Type: "exact", Path: "/api/health"}, Root: "/exact"},
			{Match: config.MatchConfig{Type: "regex", Path: `\.png$`}, Root: "/png"},
		},
	}}}
	r := testRouter(t, cfg)

	cases := map[string]string{
		"/api/health":   "/api/health", // exact wins
		"/api/users":    "/api/",       // longest prefix
		"/index.html":   "/",           // root fallback
		"/img/logo.png": `\.png$`,      // regex (no non-root prefix matches)
	}
	for path, want := range cases {
		got := do(t, r, "127.0.0.1:80", "h", path).Header().Get("X-Matched")
		if got != want {
			t.Errorf("path %s matched %q, want %q", path, got, want)
		}
	}
}

func TestRewriteRedirect(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{{
			Match:    config.MatchConfig{Type: "prefix", Path: "/"},
			Root:     "/root",
			Rewrites: []config.RewriteConfig{{Pattern: "^/old/(.*)$", Replacement: "/new/$1", Flag: "redirect"}},
		}},
	}}}
	r := testRouter(t, cfg)

	rec := do(t, r, "127.0.0.1:80", "h", "/old/page")
	if rec.Code != http.StatusFound {
		t.Fatalf("redirect status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/new/page" {
		t.Fatalf("redirect Location = %q, want /new/page", loc)
	}
}

// TestRewriteRedirectStaysOnOrigin: a rewrite that moves request path bytes to
// the front of a relative target must not yield a scheme-relative Location.
func TestRewriteRedirectStaysOnOrigin(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{{
			Match: config.MatchConfig{Type: "prefix", Path: "/"},
			Root:  "/root",
			Rewrites: []config.RewriteConfig{
				{Pattern: "^/go/(.*)$", Replacement: "/$1", Flag: "permanent"},
				{Pattern: "^/ext$", Replacement: "https://example.org/x", Flag: "redirect"},
			},
		}},
	}}}
	r := testRouter(t, cfg)
	for target, want := range map[string]string{
		"/go/%5Cevil.example/x":   "/evil.example/x",
		"/go/%2F%5Cevil.example/": "/evil.example/",
		"/go/page":                "/page",
		"/ext":                    "https://example.org/x",
	} {
		if loc := do(t, r, "127.0.0.1:80", "h", target).Header().Get("Location"); loc != want {
			t.Errorf("%s: Location = %q, want %q", target, loc, want)
		}
	}
}

func TestRewriteInternal(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{{
			Match:    config.MatchConfig{Type: "prefix", Path: "/"},
			Root:     "/root",
			Rewrites: []config.RewriteConfig{{Pattern: "^/foo$", Replacement: "/bar", Flag: "break"}},
		}},
	}}}
	r := testRouter(t, cfg)

	// Internal rewrite should not redirect; handler still runs (200).
	rec := do(t, r, "127.0.0.1:80", "h", "/foo")
	if rec.Code != http.StatusOK {
		t.Fatalf("internal rewrite status = %d, want 200", rec.Code)
	}
}

func TestDenyAndRedirectActions(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/blocked"}, Deny: true},
			{Match: config.MatchConfig{Type: "exact", Path: "/go"}, Redirect: "https://example.com", Return: 301},
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/root"},
		},
	}}}
	r := testRouter(t, cfg)

	if rec := do(t, r, "127.0.0.1:80", "h", "/blocked/x"); rec.Code != http.StatusForbidden {
		t.Fatalf("deny status = %d, want 403", rec.Code)
	}
	rec := do(t, r, "127.0.0.1:80", "h", "/go")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "https://example.com" {
		t.Fatalf("redirect action = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRedirectHTTPS(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen:        "127.0.0.1:80",
		ServerNames:   []string{"secure.example.com"},
		RedirectHTTPS: 308,
		Locations:     []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/root"}},
	}}}
	r := testRouter(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "http://secure.example.com:80/path?q=1", nil)
	req.Host = "secure.example.com:80"
	rec := httptest.NewRecorder()
	r.For("127.0.0.1:80").ServeHTTP(rec, req)

	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want 308", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://secure.example.com/path?q=1" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestBodyLimitEnforced(t *testing.T) {
	// drainBuilder reads the whole body so MaxBytesReader can trip the limit.
	drainBuilder := func(_ config.ServerConfig, _ config.LocationConfig) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := io.ReadAll(r.Body); err != nil {
				http.Error(w, "413 Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusOK)
		}), nil
	}
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen:            "127.0.0.1:80",
		ClientMaxBodySize: config.Size(8),
		Locations:         []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/root"}},
	}}}
	builders := map[string]Builder{ActionStatic: drainBuilder}
	r, err := New(cfg, builders, drainBuilder, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Under the limit -> 200.
	req := httptest.NewRequest(http.MethodPost, "http://h/", strings.NewReader("12345"))
	rec := httptest.NewRecorder()
	r.For("127.0.0.1:80").ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("small body = %d, want 200", rec.Code)
	}

	// Over the 8-byte limit -> 413.
	req = httptest.NewRequest(http.MethodPost, "http://h/", strings.NewReader("0123456789ABCDEF"))
	rec = httptest.NewRecorder()
	r.For("127.0.0.1:80").ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body = %d, want 413", rec.Code)
	}
}

// TestActionRegistryOverridesBuiltin proves every action — including the
// router's built-in config actions — dispatches through the one registry, so a
// caller-supplied builder for "redirect" takes precedence over the built-in.
func TestActionRegistryOverridesBuiltin(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "exact", Path: "/go"}, Redirect: "https://example.com", Return: 301},
		},
	}}}
	custom := func(_ config.ServerConfig, _ config.LocationConfig) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}), nil
	}
	r, err := New(cfg, map[string]Builder{ActionRedirect: custom}, nil, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if rec := do(t, r, "127.0.0.1:80", "h", "/go"); rec.Code != http.StatusTeapot {
		t.Fatalf("override builder not used: status = %d, want 418", rec.Code)
	}
}

// TestActionFallbackWhenUnregistered proves the built-in actions are present
// even with a nil builders map, and that a content action with no registered
// builder and no fallback uses the default notImplemented (501) builder — the
// uniform fallback path shared by every action.
func TestActionFallbackWhenUnregistered(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/blocked"}, Deny: true},
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/root"},
		},
	}}}
	r, err := New(cfg, nil, nil, nil, nil) // no content builders, no fallback
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Built-in deny still works without the caller registering it.
	if rec := do(t, r, "127.0.0.1:80", "h", "/blocked/x"); rec.Code != http.StatusForbidden {
		t.Fatalf("built-in deny status = %d, want 403", rec.Code)
	}
	// Unregistered static action falls back to 501.
	if rec := do(t, r, "127.0.0.1:80", "h", "/page"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("unregistered action status = %d, want 501", rec.Code)
	}
}
