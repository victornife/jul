// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Minimal Go HTTP app served behind Jul via proxy_pass.
//
// Dependency-free: uses only the standard library. Go's net/http server
// handles concurrent / keep-alive connections out of the box, so it works
// cleanly behind Jul's reverse proxy.
//
// Run it with (from this folder so it is outside the main module build):
//
//	go run ./examples/go-proxy/app.go
package main

import (
	"fmt"
	"log"
	"net/http"
)

const addr = "127.0.0.1:3033"

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "Hello from a Go app behind Jul (proxy_pass over HTTP)!")
	})

	log.Printf("Serving on http://%s (Ctrl+C to stop)", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
