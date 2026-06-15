package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
)

func setupTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "index.html"), "<h1>home</h1>")
	mustWrite(t, filepath.Join(dir, "assets", "app.css"), "body{color:red}")
	mustWrite(t, filepath.Join(dir, ".secret"), "topsecret")
	mustWrite(t, filepath.Join(dir, "sub", "index.html"), "<h1>sub</h1>")
	return dir
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newStatic(t *testing.T, loc config.LocationConfig) http.Handler {
	t.Helper()
	h, err := NewStatic(config.ServerConfig{}, loc)
	if err != nil {
		t.Fatalf("NewStatic: %v", err)
	}
	if c, ok := h.(interface{ Close() error }); ok {
		t.Cleanup(func() { _ = c.Close() })
	}
	return h
}

func get(h http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStaticIndexAndMIME(t *testing.T) {
	dir := setupTree(t)
	h := newStatic(t, config.LocationConfig{Root: dir, Index: []string{"index.html"}})

	rec := get(h, "http://h/", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "home") {
		t.Fatalf("GET / = %d %q", rec.Code, rec.Body.String())
	}

	rec = get(h, "http://h/assets/app.css", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET css = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("css content-type = %q", ct)
	}
}

func TestStaticTraversalBlocked(t *testing.T) {
	dir := setupTree(t)
	h := newStatic(t, config.LocationConfig{Root: dir})

	for _, p := range []string{"http://h/../../etc/passwd", "http://h/..%2f..%2fetc/passwd"} {
		rec := get(h, p, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("traversal %s = %d, want 404", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "root:") {
			t.Errorf("traversal %s leaked system file", p)
		}
	}
}

func TestStaticHiddenRejected(t *testing.T) {
	dir := setupTree(t)
	h := newStatic(t, config.LocationConfig{Root: dir})
	if rec := get(h, "http://h/.secret", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("hidden file = %d, want 404", rec.Code)
	}

	hAllow := newStatic(t, config.LocationConfig{Root: dir, AllowHidden: true})
	if rec := get(hAllow, "http://h/.secret", nil); rec.Code != http.StatusOK {
		t.Fatalf("hidden file with AllowHidden = %d, want 200", rec.Code)
	}
}

func TestStaticTryFilesSPA(t *testing.T) {
	dir := setupTree(t)
	h := newStatic(t, config.LocationConfig{
		Root:     dir,
		Index:    []string{"index.html"},
		TryFiles: []string{"$uri", "$uri/", "/index.html"},
	})
	// Missing path falls back to /index.html (SPA behavior).
	rec := get(h, "http://h/app/route", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "home") {
		t.Fatalf("SPA fallback = %d %q", rec.Code, rec.Body.String())
	}
	// Existing nested dir resolves via $uri/ to its index.
	rec = get(h, "http://h/sub", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "sub") {
		t.Fatalf("dir index = %d %q", rec.Code, rec.Body.String())
	}
}

func TestStaticConditional304(t *testing.T) {
	dir := setupTree(t)
	h := newStatic(t, config.LocationConfig{Root: dir})

	rec := get(h, "http://h/index.html", nil)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	rec2 := get(h, "http://h/index.html", map[string]string{"If-None-Match": etag})
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("revalidation = %d, want 304", rec2.Code)
	}
}

func TestStaticRange(t *testing.T) {
	dir := setupTree(t)
	h := newStatic(t, config.LocationConfig{Root: dir})
	rec := get(h, "http://h/index.html", map[string]string{"Range": "bytes=0-3"})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range = %d, want 206", rec.Code)
	}
	if rec.Body.Len() != 4 {
		t.Fatalf("range body len = %d, want 4", rec.Body.Len())
	}
}

func TestStaticDirectoryListing(t *testing.T) {
	dir := setupTree(t)
	// No index in this location so the dir listing kicks in.
	h := newStatic(t, config.LocationConfig{Root: dir, Index: []string{"none.html"}, DirectoryListing: true})
	rec := get(h, "http://h/assets/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "app.css") {
		t.Fatalf("listing missing entry: %q", rec.Body.String())
	}
}

func TestStaticMethodNotAllowed(t *testing.T) {
	dir := setupTree(t)
	h := newStatic(t, config.LocationConfig{Root: dir})
	req := httptest.NewRequest(http.MethodPost, "http://h/index.html", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}
}

func TestStaticPrecompressedSidecar(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.js"), "console.log('plain')")
	// The sidecar bytes are served verbatim with Content-Encoding: gzip, so
	// they need not be valid gzip for this handler-level test.
	mustWrite(t, filepath.Join(dir, "app.js.gz"), "GZIP-BYTES")

	h, err := NewStaticWithOptions(config.ServerConfig{}, config.LocationConfig{Root: dir}, StaticOptions{
		Precompressed: true,
		Encoders:      []string{"gzip"},
	})
	if err != nil {
		t.Fatalf("NewStaticWithOptions: %v", err)
	}
	if c, ok := h.(interface{ Close() error }); ok {
		t.Cleanup(func() { _ = c.Close() })
	}

	// Accept-Encoding: gzip serves the sidecar.
	rec := get(h, "http://h/app.js", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	if rec.Body.String() != "GZIP-BYTES" {
		t.Fatalf("sidecar body = %q", rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatal("missing Vary: Accept-Encoding")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content-type should come from the original resource, got %q", ct)
	}

	// Without Accept-Encoding the uncompressed file is served.
	rec = get(h, "http://h/app.js", nil)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("plain request must not be encoded")
	}
	if rec.Body.String() != "console.log('plain')" {
		t.Fatalf("plain body = %q", rec.Body.String())
	}

	// A Range request bypasses the sidecar.
	rec = get(h, "http://h/app.js", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-3"})
	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Fatal("Range request must not serve the precompressed sidecar")
	}
}
