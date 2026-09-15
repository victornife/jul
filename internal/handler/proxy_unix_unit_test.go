// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/upstream"
)

func TestUnixProxyContextHelpers(t *testing.T) {
	ctx := context.Background()
	if got := originalProxyHost(ctx); got != "" {
		t.Fatalf("original host without value = %q", got)
	}
	if target, ok := unixDialTarget(ctx); ok || target != "" {
		t.Fatalf("empty unix target = %q/%v", target, ok)
	}

	ctx = withOriginalProxyHost(ctx, "edge.example")
	if got := originalProxyHost(ctx); got != "edge.example" {
		t.Fatalf("original host = %q", got)
	}
	ctx = context.WithValue(ctx, unixDialTargetKey{}, "/tmp/backend.sock")
	if target, ok := unixDialTarget(ctx); !ok || target != "/tmp/backend.sock" {
		t.Fatalf("unix target = %q/%v", target, ok)
	}
	ctx = context.WithValue(context.Background(), unixDialTargetKey{}, "")
	if _, ok := unixDialTarget(ctx); ok {
		t.Fatal("empty unix target should not be usable")
	}
}

func TestUnixDialErrorRedactsAndUnwraps(t *testing.T) {
	cause := errors.New("dial /private/secret.sock: permission denied")
	err := unixDialError{err: cause}
	if got := err.Error(); got != "unix upstream dial failed" || strings.Contains(got, "secret") {
		t.Fatalf("redacted error = %q", got)
	}
	if !errors.Is(err, cause) {
		t.Fatal("unix dial error does not unwrap its cause")
	}
}

func TestPrepareProxyAttemptRejectsInvalidBackendShapes(t *testing.T) {
	req := &http.Request{URL: &url.URL{Scheme: "http", Host: "logical"}}

	cases := []struct {
		name string
		b    upstream.Attempt
		tls  bool
		want string
	}{
		{name: "nil backend", b: upstream.Attempt{}, want: "backend is nil"},
		{name: "tcp without URL", b: upstream.Attempt{Backend: &upstream.Backend{Network: upstream.NetworkTCP, Address: "127.0.0.1:80"}}, want: "no HTTP URL"},
		{name: "unix non-http scheme", b: upstream.Attempt{Backend: &upstream.Backend{Network: upstream.NetworkUnix, Address: "/tmp/app.sock"}}, want: "requires plaintext http"},
		{name: "unsupported network", b: upstream.Attempt{Backend: &upstream.Backend{Network: "sctp", Address: "backend"}}, want: "unsupported backend network"},
		{name: "tls cannot use unix", b: unixAttempt(t, "/tmp/app.sock", "http"), tls: true, want: "cannot satisfy an https route"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clone := req.Clone(context.Background())
			_, _, err := prepareProxyAttempt(clone, tc.b, tc.tls)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestPrepareProxyAttemptTCPAndTLS(t *testing.T) {
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:    "secure",
		Servers: []config.UpstreamServer{{Address: "127.0.0.1:8443", Weight: 1}},
	}, "https")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	req := &http.Request{URL: &url.URL{Scheme: "http", Host: "logical"}}
	out, label, err := prepareProxyAttempt(req, upstream.Attempt{Backend: pool.Backends()[0]}, true)
	if err != nil {
		t.Fatalf("prepare TCP TLS attempt: %v", err)
	}
	if out.URL.Scheme != "https" || out.URL.Host != "127.0.0.1:8443" || label != "127.0.0.1:8443" {
		t.Fatalf("prepared TCP attempt = scheme=%q host=%q label=%q", out.URL.Scheme, out.URL.Host, label)
	}
}

func TestPrepareProxyAttemptUnixPreservesExplicitHost(t *testing.T) {
	attempt := unixAttempt(t, "/tmp/app.sock", "http")
	req := &http.Request{
		URL:  &url.URL{Scheme: "http", Host: "logical"},
		Host: "explicit.example",
	}
	out, label, err := prepareProxyAttempt(req, attempt, false)
	if err != nil {
		t.Fatalf("prepare Unix attempt: %v", err)
	}
	if out.Host != "explicit.example" {
		t.Fatalf("Host = %q", out.Host)
	}
	if target, ok := unixDialTarget(out.Context()); !ok || target != "/tmp/app.sock" {
		t.Fatalf("dial target = %q/%v", target, ok)
	}
	if !strings.HasPrefix(out.URL.Host, "jul-unix-") || !strings.HasSuffix(out.URL.Host, ".invalid") {
		t.Fatalf("opaque pool host = %q", out.URL.Host)
	}
	if strings.Contains(out.URL.Host, "/tmp/app.sock") || strings.Contains(label, "/tmp/app.sock") {
		t.Fatalf("path leaked through pool host/label: %q / %q", out.URL.Host, label)
	}
}

func TestValidateHTTPPoolNetworkAndDirectSyntaxBranches(t *testing.T) {
	if err := validateHTTPPoolNetwork("http", nil); err != nil {
		t.Fatalf("nil pool: %v", err)
	}
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:    "local",
		Servers: []config.UpstreamServer{{Address: "unix:/tmp/app.sock", Weight: 1}},
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()
	if err := validateHTTPPoolNetwork("http", pool); err != nil {
		t.Fatalf("plaintext Unix pool: %v", err)
	}
	if err := validateHTTPPoolNetwork("https", pool); err == nil || !strings.Contains(err.Error(), "plaintext http only") {
		t.Fatalf("https Unix validation = %v", err)
	}

	for _, raw := range []string{"http://unix:/tmp/app.sock", " HTTPS://UNIX:/tmp/app.sock "} {
		if err := rejectDirectUnixProxyPass(raw); err == nil || !strings.Contains(err.Error(), "[[upstreams]]") {
			t.Fatalf("direct syntax %q error = %v", raw, err)
		}
	}
	if err := rejectDirectUnixProxyPass("http://named-upstream"); err != nil {
		t.Fatalf("normal proxy_pass rejected: %v", err)
	}
}

func unixAttempt(t *testing.T, address, scheme string) upstream.Attempt {
	t.Helper()
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:    "test-unix",
		Servers: []config.UpstreamServer{{Address: "unix:" + address, Weight: 1}},
	}, scheme)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return upstream.Attempt{Backend: pool.Backends()[0]}
}
