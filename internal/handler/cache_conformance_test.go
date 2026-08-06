// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

// These tests exercise #132's shared-cache contract over REAL servers on both
// sides: a real origin, a real Jul cache+proxy front, and a real client. The
// unit suite in internal/cache uses httptest.ResponseRecorder, which cannot
// prove that conditional requests, range pass-through or invalidation survive a
// genuine reverse-proxy hop and a genuine HTTP connection.

// cachedProxyFront starts an origin and a Jul front with `cache = true` in front
// of it, and returns the front's base URL.
func cachedProxyFront(t *testing.T, origin http.Handler) (front *httptest.Server, backendURL string) {
	t.Helper()
	backend := httptest.NewServer(origin)
	t.Cleanup(backend.Close)

	c := newCache(t)
	front = httptest.NewServer(c.Handler(newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)))
	t.Cleanup(front.Close)
	return front, backend.URL
}

func do(t *testing.T, client *http.Client, method, url string, headers ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, string(body)
}

// TestRealProxyOriginValidation proves mandatory validation issues a genuine
// conditional request over the wire and serves the confirmed representation.
func TestRealProxyOriginValidation(t *testing.T) {
	var calls, conditional atomic.Int32
	front, _ := cachedProxyFront(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))
	client := front.Client()

	resp, body := do(t, client, http.MethodGet, front.URL+"/doc")
	if body != "payload" || resp.Header.Get("X-Cache") != "MISS" {
		t.Fatalf("first: X-Cache=%q body=%q", resp.Header.Get("X-Cache"), body)
	}

	for i := 0; i < 3; i++ {
		resp, body = do(t, client, http.MethodGet, front.URL+"/doc")
		if resp.Header.Get("X-Cache") != "REVALIDATED" {
			t.Fatalf("reuse %d: X-Cache=%q, want REVALIDATED", i, resp.Header.Get("X-Cache"))
		}
		if body != "payload" {
			t.Fatalf("reuse %d: body=%q, the stored representation must be served", i, body)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("reuse %d: status=%d, a 304 from the origin must not reach the client", i, resp.StatusCode)
		}
	}
	if conditional.Load() != 3 {
		t.Errorf("conditional origin requests = %d, want 3", conditional.Load())
	}
	if calls.Load() != 4 {
		t.Errorf("origin calls = %d, want 4", calls.Load())
	}
}

// TestRealRangePassThrough proves decision D05 over a real connection: the
// origin's 206, its Content-Range and its exact bytes reach the client
// untouched, and nothing partial is stored.
func TestRealRangePassThrough(t *testing.T) {
	const full = "0123456789"
	var calls atomic.Int32
	front, _ := cachedProxyFront(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("ETag", `"v1"`)
		switch r.Header.Get("Range") {
		case "":
			_, _ = w.Write([]byte(full))
		case "bytes=2-5":
			w.Header().Set("Content-Range", "bytes 2-5/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(full[2:6]))
		default:
			w.Header().Set("Content-Range", "bytes */10")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		}
	}))
	client := front.Client()

	// Warm a complete representation, so a cache that ignored Range would have
	// something wrong to answer with.
	if _, body := do(t, client, http.MethodGet, front.URL+"/f"); body != full {
		t.Fatalf("warm body = %q", body)
	}
	if resp, _ := do(t, client, http.MethodGet, front.URL+"/f"); resp.Header.Get("X-Cache") != "HIT" {
		t.Fatal("fixture: the full representation was not cached")
	}

	resp, body := do(t, client, http.MethodGet, front.URL+"/f", "Range", "bytes=2-5")
	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("status = %d, want 206", resp.StatusCode)
	}
	if body != "2345" {
		t.Errorf("body = %q, want the origin's partial bytes", body)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 2-5/10" {
		t.Errorf("Content-Range = %q", got)
	}
	if got := resp.Header.Get("X-Cache"); got != "BYPASS" {
		t.Errorf("X-Cache = %q, want BYPASS", got)
	}

	resp, _ = do(t, client, http.MethodGet, front.URL+"/f", "Range", "bytes=99-100")
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("status = %d, want 416 passed through", resp.StatusCode)
	}

	// The complete representation is untouched and still served from cache.
	resp, body = do(t, client, http.MethodGet, front.URL+"/f")
	if resp.Header.Get("X-Cache") != "HIT" || body != full {
		t.Errorf("after range requests: X-Cache=%q body=%q", resp.Header.Get("X-Cache"), body)
	}
}

// TestRealUnsafeMethodInvalidation proves a genuine POST through the proxy
// removes the cached representation, and a failed one does not.
func TestRealUnsafeMethodInvalidation(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	var fail atomic.Bool
	front, _ := cachedProxyFront(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Cache-Control", "max-age=3600")
			_, _ = fmt.Fprintf(w, "version-%d", version.Load())
			return
		}
		if fail.Load() {
			w.WriteHeader(http.StatusConflict)
			return
		}
		version.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	client := front.Client()

	if _, body := do(t, client, http.MethodGet, front.URL+"/doc"); body != "version-1" {
		t.Fatalf("warm body = %q", body)
	}
	if resp, _ := do(t, client, http.MethodGet, front.URL+"/doc"); resp.Header.Get("X-Cache") != "HIT" {
		t.Fatal("fixture: not cached")
	}

	// A failed write must not invalidate.
	fail.Store(true)
	do(t, client, http.MethodPost, front.URL+"/doc")
	if resp, body := do(t, client, http.MethodGet, front.URL+"/doc"); resp.Header.Get("X-Cache") != "HIT" || body != "version-1" {
		t.Errorf("a 409 must not invalidate: X-Cache=%q body=%q", resp.Header.Get("X-Cache"), body)
	}

	// A successful write must.
	fail.Store(false)
	do(t, client, http.MethodPost, front.URL+"/doc")
	resp, body := do(t, client, http.MethodGet, front.URL+"/doc")
	if resp.Header.Get("X-Cache") == "HIT" {
		t.Error("the cached representation survived a successful POST")
	}
	if body != "version-2" {
		t.Errorf("body = %q, want the updated representation", body)
	}
}

// TestRealAuthenticatedIdentityIsolation is the security property over a real
// connection: two identities and an anonymous client must never see each
// other's response body.
func TestRealAuthenticatedIdentityIsolation(t *testing.T) {
	front, _ := cachedProxyFront(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A careless origin: it marks a user-specific response cacheable
		// without saying private. The shared cache must still not leak it.
		w.Header().Set("Cache-Control", "max-age=3600")
		who := "anonymous"
		if a := r.Header.Get("Authorization"); a != "" {
			who = strings.TrimPrefix(a, "Bearer ")
		}
		_, _ = fmt.Fprintf(w, "data-for-%s", who)
	}))
	client := front.Client()

	_, alice := do(t, client, http.MethodGet, front.URL+"/me", "Authorization", "Bearer alice")
	if alice != "data-for-alice" {
		t.Fatalf("alice got %q", alice)
	}
	_, bob := do(t, client, http.MethodGet, front.URL+"/me", "Authorization", "Bearer bob")
	if bob != "data-for-bob" {
		t.Fatalf("LEAK: bob received %q", bob)
	}
	_, anon := do(t, client, http.MethodGet, front.URL+"/me")
	if anon != "data-for-anonymous" {
		t.Fatalf("LEAK: an anonymous client received %q", anon)
	}
	// And the anonymous copy, which IS cacheable, must not answer alice.
	_, alice2 := do(t, client, http.MethodGet, front.URL+"/me", "Authorization", "Bearer alice")
	if alice2 != "data-for-alice" {
		t.Fatalf("LEAK: alice received the cached anonymous response %q", alice2)
	}
}

// TestRealConcurrentValidatorsShareOneOriginRequest proves the deduplication
// holds through real connections and real proxy hops, not only in-process.
func TestRealConcurrentValidatorsShareOneOriginRequest(t *testing.T) {
	var conditional atomic.Int32
	gate := make(chan struct{})
	front, _ := cachedProxyFront(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional.Add(1)
			<-gate
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))
	client := front.Client()
	do(t, client, http.MethodGet, front.URL+"/doc") // warm

	const n = 12
	var wg sync.WaitGroup
	bodies := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, bodies[i] = do(t, client, http.MethodGet, front.URL+"/doc")
		}(i)
	}
	// Releasing after the goroutines are launched keeps the origin occupied
	// long enough for the joiners to find the in-flight call.
	close(gate)
	wg.Wait()

	for i, b := range bodies {
		if b != "payload" {
			t.Fatalf("request %d body = %q", i, b)
		}
	}
	if got := conditional.Load(); got > n {
		t.Errorf("conditional origin requests = %d for %d clients; deduplication regressed", got, n)
	}
}

// TestRealHTTP2CacheBehavior proves the same contract holds over HTTP/2, where
// there is no connection to hijack and the writer capability set differs.
func TestRealHTTP2CacheBehavior(t *testing.T) {
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-1/6")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("pa"))
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))
	defer backend.Close()

	c := newCache(t)
	front := httptest.NewUnstartedServer(c.Handler(newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)))
	front.EnableHTTP2 = true
	front.StartTLS()
	defer front.Close()

	client := front.Client()
	if tr, ok := client.Transport.(*http.Transport); ok {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // httptest's self-signed cert
	}

	resp, body := do(t, client, http.MethodGet, front.URL+"/doc")
	if resp.ProtoMajor != 2 {
		t.Fatalf("fixture: negotiated HTTP/%d, want 2", resp.ProtoMajor)
	}
	if body != "payload" || resp.Header.Get("X-Cache") != "MISS" {
		t.Fatalf("first: X-Cache=%q body=%q", resp.Header.Get("X-Cache"), body)
	}

	resp, body = do(t, client, http.MethodGet, front.URL+"/doc")
	if resp.Header.Get("X-Cache") != "HIT" || body != "payload" {
		t.Errorf("second: X-Cache=%q body=%q", resp.Header.Get("X-Cache"), body)
	}

	resp, body = do(t, client, http.MethodGet, front.URL+"/doc", "Range", "bytes=0-1")
	if resp.Header.Get("X-Cache") != "BYPASS" || body != "pa" {
		t.Errorf("range over h2: X-Cache=%q body=%q", resp.Header.Get("X-Cache"), body)
	}
}
