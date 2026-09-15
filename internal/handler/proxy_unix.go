// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
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
type originalProxyHostKey struct{}

func withOriginalProxyHost(ctx context.Context, host string) context.Context {
	return context.WithValue(ctx, originalProxyHostKey{}, host)
}

func originalProxyHost(ctx context.Context) string {
	host, _ := ctx.Value(originalProxyHostKey{}).(string)
	return host
}

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
		if req.Host == "" {
			req.Host = originalProxyHost(req.Context())
		}
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
