package handler

import (
	"io"
	"jul/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"

	"testing"
)

// TestProxyChunkedResponse verifies that a real backend that streams a
// chunked Transfer-Encoding response is forwarded correctly by the proxy.
// This is an integration-style test because it exercises the full proxy
// round-trip against a real httptest.Server backend.
func TestProxyChunkedResponse(t *testing.T) {
	// Backend that deliberately sends a chunked response via Flusher.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("backend does not support Flusher")
			return
		}
		for _, chunk := range []string{"chunk-1\n", "chunk-2\n", "chunk-3\n"} {
			_, _ = w.Write([]byte(chunk))
			fl.Flush()
		}
	}))
	defer backend.Close()

	h := newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)
	front := httptest.NewServer(h)
	defer front.Close()

	resp, err := http.Get(front.URL + "/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", resp.Header.Get("Content-Type"))
	}

	body, _ := io.ReadAll(resp.Body)
	want := "chunk-1\nchunk-2\nchunk-3\n"
	if string(body) != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

// TestProxyRequestBodyStreaming verifies that a large POST body is forwarded
// correctly from the edge to a real upstream backend.
func TestProxyRequestBodyStreaming(t *testing.T) {
	var gotBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	h := newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)
	front := httptest.NewServer(h)
	defer front.Close()

	payload := strings.Repeat("x", 1<<20) // 1 MB
	resp, err := http.Post(front.URL+"/upload", "text/plain", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if gotBody != payload {
		t.Errorf("body length mismatch: got %d, want %d", len(gotBody), len(payload))
	}
}

// TestProxyUpstreamHeadersRewrite verifies that the proxy correctly rewrites
// Host and adds X-Forwarded-* headers when talking to a real upstream.
func TestProxyUpstreamHeadersRewrite(t *testing.T) {
	var gotHost, gotXFF, gotXFP, gotXReal string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotXFF = r.Header.Get("X-Forwarded-For")
		gotXFP = r.Header.Get("X-Forwarded-Proto")
		gotXReal = r.Header.Get("X-Real-IP")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	loc := config.LocationConfig{
		ProxyPass: backend.URL,
		Headers: map[string]string{
			"Host":      "$host",
			"X-Real-IP": "$remote_addr",
		},
	}
	h := newProxy(t, loc, nil)
	front := httptest.NewServer(h)
	defer front.Close()

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/api", nil)
	req.Host = "edge.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if gotHost != "edge.example" {
		t.Errorf("Host forwarded = %q, want edge.example", gotHost)
	}
	if gotXFF == "" {
		t.Error("X-Forwarded-For not set")
	}
	if gotXFP != "http" {
		t.Errorf("X-Forwarded-Proto = %q, want http", gotXFP)
	}
	if gotXReal == "" {
		t.Error("X-Real-IP not set")
	}
}
