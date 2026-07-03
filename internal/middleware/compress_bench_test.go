package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "jul/internal/config" // side-effect: registers gzip
)

// BenchmarkCompressionPassThrough measures the no-compression path when the
// client sends no Accept-Encoding.
func BenchmarkCompressionPassThrough(b *testing.B) {
	mw, _ := NewCompression(CompressionOptions{
		Encoders: []string{"gzip"},
		MinSize:  1024,
	})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Hello, world!")
	}))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}
}

// BenchmarkCompressionGzipSmall measures compressing a modest JSON body with
// gzip (the default encoder).
func BenchmarkCompressionGzipSmall(b *testing.B) {
	body := []byte(`{"status":"ok","data":{"id":42,"name":"test","items":[1,2,3,4,5]}}`)
	mw, _ := NewCompression(CompressionOptions{
		Encoders: []string{"gzip"},
		MinSize:  1,
		Types:    []string{"application/json"},
	})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}
}

// BenchmarkCompressionGzipLarge measures compressing a larger text response
// (simulating an HTML page or bulk JSON).
func BenchmarkCompressionGzipLarge(b *testing.B) {
	// ~8 KiB of repetitive HTML-like text — highly compressible.
	var body []byte
	for i := 0; i < 400; i++ {
		body = append(body, []byte(`<div class="item"><span>content `+fmt.Sprint(i)+`</span></div>
`)...)
	}
	mw, _ := NewCompression(CompressionOptions{
		Encoders: []string{"gzip"},
		MinSize:  1,
		Types:    []string{"text/html"},
	})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write(body)
	}))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}
}

// BenchmarkCompressionEncoderReuse measures the benefit of encoder pooling by
// hitting the same gzip pool across many requests.
func BenchmarkCompressionEncoderReuse(b *testing.B) {
	body := []byte(`{"status":"ok","count":100,"items":["a","b","c","d","e"]}`)
	mw, _ := NewCompression(CompressionOptions{
		Encoders: []string{"gzip"},
		MinSize:  1,
		Types:    []string{"application/json"},
	})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}
}
