//go:build ignore

// Track 2 — Tiny Go backend for burn-in validation.
// Serves JSON API responses on :8081; handles high concurrency.
// Usage: go run scripts/burn-in-backend.go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
)

func main() {
	http.HandleFunc("/", handler)
	fmt.Println("Backend listening on http://127.0.0.1:8081")
	if err := http.ListenAndServe("127.0.0.1:8081", nil); err != nil {
		fmt.Printf("server error: %v\n", err)
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "max-age=10")
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "ok",
		"path":      r.URL.Path,
		"goroutine": runtime.NumGoroutine(),
	})
}
