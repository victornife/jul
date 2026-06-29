package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
)

func TestCacheWriterFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := &cacheWriter{ResponseWriter: rec, limit: 1024}

	// Flush should be a no-op on httptest.ResponseRecorder (not a Flusher),
	// but must not panic.
	cw.Flush()

	// Write some data without explicit WriteHeader; it should auto-OK.
	cw.Write([]byte("hello"))
	if cw.status != http.StatusOK {
		t.Errorf("status = %d, want 200", cw.status)
	}
	if cw.buf.String() != "hello" {
		t.Errorf("buf = %q", cw.buf.String())
	}

	// WriteHeader should be idempotent.
	cw.WriteHeader(http.StatusNotFound)
	if cw.status != http.StatusOK {
		t.Errorf("status changed to %d after already written", cw.status)
	}
}

func TestCacheWriterTooBig(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := &cacheWriter{ResponseWriter: rec, limit: 4}

	cw.Write([]byte("abcd"))
	if cw.tooBig {
		t.Error("should not be tooBig at exactly limit")
	}
	cw.Write([]byte("e"))
	if !cw.tooBig {
		t.Error("should be tooBig after exceeding limit")
	}
	if cw.buf.Len() != 0 {
		t.Error("buf should be reset after tooBig")
	}
}

func TestRecorder(t *testing.T) {
	r := &recorder{limit: 1024}

	r.WriteHeader(http.StatusCreated)
	r.WriteHeader(http.StatusOK) // idempotent
	if r.status != http.StatusCreated {
		t.Errorf("status = %d, want 201", r.status)
	}

	r.Header().Set("X-Test", "val")
	if r.Header().Get("X-Test") != "val" {
		t.Error("header not set")
	}

	r.Write([]byte("body"))
	if r.body.String() != "body" {
		t.Errorf("body = %q", r.body.String())
	}

	// tooBig path
	r2 := &recorder{limit: 2}
	r2.Write([]byte("123"))
	if !r2.tooBig {
		t.Error("should be tooBig")
	}
}

func TestRequestNoStore(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"no-store", true},
		{"no-cache", false},
		{"public, no-store", true},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		if tc.header != "" {
			req.Header.Set("Cache-Control", tc.header)
		}
		got := requestNoStore(req)
		if got != tc.want {
			t.Errorf("requestNoStore(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestNotModified(t *testing.T) {
	now := time.Now().UTC().Format(http.TimeFormat)
	entry := &Entry{ETag: `"abc"`, LastModified: now}

	cases := []struct {
		name string
		inm  string
		ims  string
		want bool
	}{
		{"etag match", `"abc"`, "", true},
		{"etag mismatch", `"xyz"`, "", false},
		{"etag star", `*`, "", true},
		{"ims match", "", now, true},
		{"ims mismatch", "", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), false},
		{"neither", "", "", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		if tc.inm != "" {
			req.Header.Set("If-None-Match", tc.inm)
		}
		if tc.ims != "" {
			req.Header.Set("If-Modified-Since", tc.ims)
		}
		got := notModified(req, entry)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCCHasDirective(t *testing.T) {
	if ccHasDirective("public, max-age=60", "max-age") != true {
		t.Error("expected max-age directive found")
	}
	if ccHasDirective("public", "no-store") != false {
		t.Error("expected no-store not found")
	}
}

func TestParseList(t *testing.T) {
	got := parseList("a, b, c")
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseList[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSecs(t *testing.T) {
	if secs("60") != 60*time.Second {
		t.Errorf("secs(60) = %v", secs("60"))
	}
	if secs("-1") != 0 {
		t.Errorf("secs(-1) = %v", secs("-1"))
	}
	if secs("nope") != 0 {
		t.Errorf("secs(nope) = %v", secs("nope"))
	}
}

func TestCloneHeader(t *testing.T) {
	h := http.Header{"X": {"a", "b"}}
	c := cloneHeader(h)
	c.Set("X", "c")
	if h.Get("X") == "c" {
		t.Error("cloneHeader was shallow")
	}
}

func TestRemoveHopByHop(t *testing.T) {
	h := http.Header{}
	h.Set("Connection", "X-Custom, Keep-Alive")
	h.Set("X-Custom", "val")
	h.Set("Keep-Alive", "val")
	h.Set("Content-Type", "text/plain")
	removeHopByHop(h)
	if h.Get("X-Custom") != "" || h.Get("Keep-Alive") != "" {
		t.Error("hop-by-hop headers not removed")
	}
	if h.Get("Content-Type") != "text/plain" {
		t.Error("Content-Type should remain")
	}
}

func TestMemStoreDelAndPurge(t *testing.T) {
	m := newMemStore(1<<20, nil)
	e := &Entry{Status: 200, Body: []byte("hi"), ExpiresAt: time.Now().Add(time.Hour)}

	m.set("k", e)
	if _, ok := m.get("k"); !ok {
		t.Fatal("expected entry")
	}

	m.del("k")
	if _, ok := m.get("k"); ok {
		t.Error("expected deleted")
	}

	// Delete non-existent is a no-op.
	m.del("missing")

	m.set("a", e)
	m.set("b", e)
	m.purge()
	if _, ok := m.get("a"); ok {
		t.Error("expected purged")
	}
	if m.curBytes != 0 {
		t.Errorf("curBytes = %d, want 0", m.curBytes)
	}
}

func TestCacheDeleteAndPurge(t *testing.T) {
	cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)}
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil for enabled cache")
	}

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("origin"))
	}))
	h.ServeHTTP(rec, req)

	k := key(req)
	if _, ok := c.get(k); !ok {
		t.Fatal("expected cached entry")
	}

	c.Delete(k)
	if _, ok := c.get(k); ok {
		t.Error("expected deleted")
	}

	// Purge clears everything.
	h.ServeHTTP(httptest.NewRecorder(), req)
	c.Purge()
	if _, ok := c.get(k); ok {
		t.Error("expected purged")
	}
}

func TestDiskStoreDelAndPurge(t *testing.T) {
	d, err := newDiskStore(t.TempDir(), 1<<20, testLogger())
	if err != nil {
		t.Fatalf("newDiskStore: %v", err)
	}

	e := &Entry{Status: 200, Body: []byte("data"), ExpiresAt: time.Now().Add(time.Hour)}
	d.set("k", e)
	if _, ok := d.get("k"); !ok {
		t.Fatal("expected entry")
	}

	d.del("k")
	if _, ok := d.get("k"); ok {
		t.Error("expected deleted")
	}

	// Delete non-existent is a no-op.
	d.del("missing")

	d.set("a", e)
	d.set("b", e)
	d.purge()
	if _, ok := d.get("a"); ok {
		t.Error("expected purged")
	}
}
