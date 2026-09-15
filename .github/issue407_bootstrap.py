from pathlib import Path


def replace_once(path, old, new):
    p = Path(path)
    s = p.read_text()
    count = s.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected exactly one match, found {count}")
    p.write_text(s.replace(old, new, 1))


replace_once(
    "internal/handler/proxy.go",
    """\tpool, basePath, scheme, err := resolvePool(ctx, loc, upstreams, reg)
\tif err != nil {
\t\treturn nil, err
\t}

\t// The backend trust policy""",
    """\tpool, basePath, scheme, err := resolvePool(ctx, loc, upstreams, reg)
\tif err != nil {
\t\treturn nil, err
\t}
\tif err := validateHTTPPoolNetwork(scheme, pool); err != nil {
\t\treturn nil, err
\t}

\t// The backend trust policy""",
)

replace_once(
    "internal/handler/proxy.go",
    """func resolvePool(ctx context.Context, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry) (*upstream.Pool, string, string, error) {
\tu, err := url.Parse(loc.ProxyPass)""",
    """func resolvePool(ctx context.Context, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry) (*upstream.Pool, string, string, error) {
\tif err := rejectDirectUnixProxyPass(loc.ProxyPass); err != nil {
\t\treturn nil, "", "", err
\t}
\tu, err := url.Parse(loc.ProxyPass)""",
)

replace_once(
    "internal/handler/proxy.go",
    """\t\tif t.tlsBackend && (b.URL == nil || b.URL.Scheme != "https") {
\t\t\t// Fail closed rather than downgrade. Reaching here would mean a
\t\t\t// backend entered the pool with a different scheme than the route
\t\t\t// was configured with. It is terminal: every retry would face the
\t\t\t// same misconfiguration, and none of them may downgrade either.
\t\t\treturn upstream.AttemptResult{
\t\t\t\tErr:      fmt.Errorf("backend %s is not https but the route is: refusing to downgrade", b.Address),
\t\t\t\tTerminal: true,
\t\t\t}
\t\t}
\t\tout.URL.Scheme = b.URL.Scheme
\t\tout.URL.Host = b.URL.Host
""",
    """\t\tvar backendLabel string
\t\tout, backendLabel, err = prepareProxyAttempt(out, b, t.tlsBackend)
\t\tif err != nil {
\t\t\treturn upstream.AttemptResult{Err: err, Terminal: true}
\t\t}
""",
)

replace_once(
    "internal/handler/proxy.go",
    """\t\taspan.SetString("upstream.backend", b.URL.Host)
\t\taspan.SetInt("retry.attempt", int64(n))""",
    """\t\taspan.SetString("upstream.backend", backendLabel)
\t\taspan.SetString("upstream.network", b.Network)
\t\taspan.SetInt("retry.attempt", int64(n))""",
)

replace_once(
    "internal/handler/proxy.go",
    """\t\t\tif t.pool.MarkSuccess(b) && t.log != nil {
\t\t\t\tt.log.Info("proxy backend recovered", "upstream", t.pool.Name(), "backend", b.URL.Host)
\t\t\t}""",
    """\t\t\tif t.pool.MarkSuccess(b) && t.log != nil {
\t\t\t\tt.log.Info("proxy backend recovered", "upstream", t.pool.Name(), "backend", backendLabel)
\t\t\t}""",
)

replace_once(
    "internal/handler/proxy.go",
    """\tdial := dialer.DialContext
\tif pool != nil {""",
    """\tdial := func(ctx context.Context, network, addr string) (net.Conn, error) {
\t\tif target, ok := unixDialTarget(ctx); ok {
\t\t\tc, err := dialer.DialContext(ctx, upstream.NetworkUnix, target)
\t\t\tif err != nil {
\t\t\t\treturn nil, unixDialError{err: err}
\t\t\t}
\t\t\treturn c, nil
\t\t}
\t\treturn dialer.DialContext(ctx, network, addr)
\t}
\tif pool != nil {""",
)

old_validate = """func validateUnixBackends(up UpstreamConfig, where string) []error {
\tif up.HealthCheck == nil || !up.HealthCheck.Enabled || up.HealthCheck.Type != "http" {
\t\treturn nil
\t}
\tvar errs []error
\tfor i, s := range up.Servers {
\t\tif strings.HasPrefix(s.Address, "unix:") {
\t\t\terrs = append(errs, fmt.Errorf("%s.servers[%d]: health_check.type = \\\"http\\\" cannot probe the unix socket %q; use type = \\\"tcp\\\"", where, i, s.Address))
\t\t}
\t}
\treturn errs
}"""
new_validate = """func validateUnixBackends(up UpstreamConfig, where string) []error {
\tvar errs []error
\tfor i, s := range up.Servers {
\t\tif !strings.HasPrefix(s.Address, "unix:") {
\t\t\tcontinue
\t\t}
\t\tpath := strings.TrimPrefix(s.Address, "unix:")
\t\tif strings.TrimSpace(path) == "" {
\t\t\terrs = append(errs, fmt.Errorf("%s.servers[%d]: unix socket path is empty (want unix:/path/to/socket.sock)", where, i))
\t\t}
\t\tif up.BackendTLS != nil {
\t\t\terrs = append(errs, fmt.Errorf("%s.backend_tls: cannot be used with unix socket backend %q; HTTP over unix sockets is plaintext only", where, s.Address))
\t\t}
\t\tif up.HealthCheck != nil && up.HealthCheck.Enabled && up.HealthCheck.Type == "http" {
\t\t\terrs = append(errs, fmt.Errorf("%s.servers[%d]: health_check.type = \\\"http\\\" cannot probe the unix socket %q; use type = \\\"tcp\\\"", where, i, s.Address))
\t\t}
\t}
\treturn errs
}"""
replace_once("internal/config/validate_backends.go", old_validate, new_validate)

Path("internal/handler/proxy_unix.go").write_text(r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"jul/internal/upstream"
)

// unixDialTargetKey carries a selected Unix dial address from backend selection
// to http.Transport.DialContext. The value is derived only from trusted static
// configuration; request data can never influence it.
type unixDialTargetKey struct{}

func unixDialTarget(ctx context.Context) (string, bool) {
	path, ok := ctx.Value(unixDialTargetKey{}).(string)
	return path, ok && path != ""
}

// unixDialError deliberately hides the filesystem path from error strings and
// trace/log payloads while preserving the underlying error for classification.
type unixDialError struct{ err error }

func (e unixDialError) Error() string { return "unix upstream dial failed" }
func (e unixDialError) Unwrap() error { return e.err }

// prepareProxyAttempt separates HTTP authority from dial identity. TCP keeps the
// existing URL semantics. Unix uses a stable opaque URL host solely as
// net/http's connection-pool key, while DialContext receives the real socket
// path through request context. Request.Host is intentionally untouched, so the
// current named-upstream Host contract (incoming Host unless explicitly
// overridden) remains unchanged.
func prepareProxyAttempt(req *http.Request, b upstream.Attempt, tlsBackend bool) (*http.Request, string, error) {
	if b.Backend == nil {
		return req, "", fmt.Errorf("selected upstream backend is nil")
	}
	if tlsBackend && (b.Network != upstream.NetworkTCP || b.Scheme() != "https" || b.URL == nil) {
		return req, "", fmt.Errorf("backend network %s cannot satisfy an https route: refusing to downgrade", b.Network)
	}

	switch b.Network {
	case upstream.NetworkTCP:
		if b.URL == nil {
			return req, "", fmt.Errorf("tcp backend has no HTTP URL")
		}
		req.URL.Scheme = b.Scheme()
		req.URL.Host = b.URL.Host
		return req, b.URL.Host, nil
	case upstream.NetworkUnix:
		if b.Scheme() != "http" {
			return req, "", fmt.Errorf("unix HTTP backend requires plaintext http, got %q", b.Scheme())
		}
		key := unixHTTPPoolKey(b.Identity())
		req.URL.Scheme = "http"
		req.URL.Host = key
		req = req.WithContext(context.WithValue(req.Context(), unixDialTargetKey{}, b.Address))
		return req, "unix:" + strings.TrimSuffix(strings.TrimPrefix(key, "jul-unix-"), ".invalid"), nil
	default:
		return req, "", fmt.Errorf("unsupported backend network %q", b.Network)
	}
}

// unixHTTPPoolKey is deliberately opaque: distinct Unix backends must have
// distinct net/http connection-pool identities, but the filesystem path must
// not become an HTTP authority, trace attribute, metric label, or forwarded
// header. Each key is stable for one backend identity within a handler generation.
func unixHTTPPoolKey(id upstream.BackendIdentity) string {
	sum := sha256.Sum256([]byte(id.Scheme + "\x00" + id.Network + "\x00" + id.Address))
	return "jul-unix-" + hex.EncodeToString(sum[:8]) + ".invalid"
}

// validateHTTPPoolNetwork rejects unsupported HTTPS-over-Unix before a handler
// generation is published. Plain HTTP pools may mix TCP and Unix backends; the
// selected backend's Network controls the dial on every attempt.
func validateHTTPPoolNetwork(scheme string, pool *upstream.Pool) error {
	if pool == nil {
		return nil
	}
	for _, b := range pool.Backends() {
		if b.Network == upstream.NetworkUnix && scheme != "http" {
			return fmt.Errorf("upstream %q contains a unix socket backend, which supports plaintext http only; use proxy_pass = %q", pool.Name(), "http://"+pool.Name())
		}
	}
	return nil
}

// rejectDirectUnixProxyPass keeps one public socket grammar. Unix HTTP is
// expressed by a named upstream whose server is unix:/path; filesystem syntax is
// never embedded in proxy_pass.
func rejectDirectUnixProxyPass(raw string) error {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(lower, "http://unix:") || strings.HasPrefix(lower, "https://unix:") {
		return fmt.Errorf("direct unix proxy_pass is unsupported; define [[upstreams]] servers = [\"unix:/path/to/socket.sock\"] and use proxy_pass = \"http://<upstream-name>\"")
	}
	return nil
}
''')

Path("internal/handler/proxy_unix_test.go").write_text(r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"jul/internal/config"
)

func startUnixHTTPBackend(t *testing.T, h http.Handler) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("AF_UNIX runtime fixture is not portable on Windows CI")
	}
	path := filepath.Join(t.TempDir(), "backend.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: h}
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(done)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
		<-done
	})
	return path
}

func unixProxy(t *testing.T, name, path string, loc config.LocationConfig) http.Handler {
	t.Helper()
	ups := map[string]config.UpstreamConfig{
		name: {Name: name, Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: "unix:" + path, Weight: 1}}},
	}
	if loc.ProxyPass == "" {
		loc.ProxyPass = "http://" + name
	}
	return newProxy(t, loc, ups)
}

func TestProxyUnixHTTPPreservesHostAndForwardsBody(t *testing.T) {
	var gotHost, gotBody, gotQuery string
	path := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotQuery = r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, "unix-ok")
	}))
	h := unixProxy(t, "local-app", path, config.LocationConfig{})

	req := httptest.NewRequest(http.MethodPost, "http://edge.example/items?q=one", strings.NewReader("payload"))
	req.Host = "public.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "unix-ok" {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	if gotHost != "public.example" {
		t.Fatalf("Host = %q, want incoming Host", gotHost)
	}
	if gotBody != "payload" || gotQuery != "q=one" {
		t.Fatalf("body/query = %q/%q", gotBody, gotQuery)
	}
}

func TestProxyUnixHTTPExplicitHostOverride(t *testing.T) {
	var gotHost string
	path := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	h := unixProxy(t, "local-app", path, config.LocationConfig{Headers: map[string]string{"Host": "app.internal"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge.example/", nil))
	if gotHost != "app.internal" {
		t.Fatalf("Host = %q, want explicit override", gotHost)
	}
}

func TestProxyUnixHTTPMultipleSocketsAreIsolated(t *testing.T) {
	a := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "A") }))
	b := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "B") }))
	ups := map[string]config.UpstreamConfig{
		"pool": {Name: "pool", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: "unix:" + a, Weight: 1}, {Address: "unix:" + b, Weight: 1}}},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)
	counts := map[string]int{}
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
		counts[rec.Body.String()]++
	}
	if counts["A"] != 10 || counts["B"] != 10 {
		t.Fatalf("socket isolation/round robin counts = %#v, want 10/10", counts)
	}
}

func TestProxyUnixHTTPConcurrentSocketIsolation(t *testing.T) {
	a := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "A") }))
	b := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "B") }))
	ups := map[string]config.UpstreamConfig{
		"pool": {Name: "pool", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: "unix:" + a, Weight: 1}, {Address: "unix:" + b, Weight: 1}}},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)
	var mu sync.Mutex
	counts := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
			mu.Lock()
			counts[rec.Body.String()]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if counts["A"]+counts["B"] != 40 || counts["A"] == 0 || counts["B"] == 0 {
		t.Fatalf("concurrent socket results = %#v", counts)
	}
}

func TestProxyUnixHTTPMixedTCPRetry(t *testing.T) {
	path := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "unix-live") }))
	ups := map[string]config.UpstreamConfig{
		"pool": {Name: "pool", Strategy: "round_robin", MaxFails: 1, Servers: []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}, {Address: "unix:" + path, Weight: 1}}},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "unix-live" {
		t.Fatalf("mixed retry = %d %q", rec.Code, rec.Body.String())
	}
}

func TestProxyUnixHTTPRejectsDirectSyntax(t *testing.T) {
	_, _, _, err := resolvePool(context.Background(), config.LocationConfig{ProxyPass: "http://unix:/tmp/app.sock"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "[[upstreams]]") {
		t.Fatalf("direct unix proxy_pass error = %v", err)
	}
}

func TestProxyUnixHTTPRejectsHTTPSBeforeTraffic(t *testing.T) {
	ups := map[string]config.UpstreamConfig{
		"local-app": {Name: "local-app", Servers: []config.UpstreamServer{{Address: "unix:/tmp/not-required-to-exist.sock", Weight: 1}}},
	}
	_, err := NewProxy(context.Background(), config.ServerConfig{}, config.LocationConfig{ProxyPass: "https://local-app"}, ups, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "plaintext http only") {
		t.Fatalf("https unix error = %v", err)
	}
}

func TestProxyUnixHTTPGatewayErrorDoesNotExposePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret-sentinel.sock")
	h := unixProxy(t, "local-app", path, config.LocationConfig{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	if rec.Code < 500 {
		t.Fatalf("status = %d, want gateway failure", rec.Code)
	}
	if strings.Contains(rec.Body.String(), path) || strings.Contains(rec.Body.String(), "secret-sentinel") {
		t.Fatalf("client error leaked unix path: %q", rec.Body.String())
	}
}
''')

Path("internal/config/validate_unix_http_test.go").write_text(r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

func TestValidateUnixBackendsRejectsEmptyPath(t *testing.T) {
	errs := validateUnixBackends(UpstreamConfig{Servers: []UpstreamServer{{Address: "unix:"}}}, "upstreams[0]")
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "path is empty") {
		t.Fatalf("errors = %v", errs)
	}
}

func TestValidateUnixBackendsRejectsBackendTLS(t *testing.T) {
	errs := validateUnixBackends(UpstreamConfig{Servers: []UpstreamServer{{Address: "unix:/run/app.sock"}}, BackendTLS: &BackendTLSConfig{}}, "upstreams[0]")
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "plaintext only") {
		t.Fatalf("errors = %v", errs)
	}
}

func TestValidateUnixBackendsRejectsHTTPHealth(t *testing.T) {
	errs := validateUnixBackends(UpstreamConfig{Servers: []UpstreamServer{{Address: "unix:/run/app.sock"}}, HealthCheck: &HealthCheckConfig{Enabled: true, Type: "http"}}, "upstreams[0]")
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "use type = \\"tcp\\"") {
		t.Fatalf("errors = %v", errs)
	}
}

func TestValidateUnixBackendsAllowsConnectHealth(t *testing.T) {
	errs := validateUnixBackends(UpstreamConfig{Servers: []UpstreamServer{{Address: "unix:/run/app.sock"}}, HealthCheck: &HealthCheckConfig{Enabled: true, Type: "tcp"}}, "upstreams[0]")
	if len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
}
''')
