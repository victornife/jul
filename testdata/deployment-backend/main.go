// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// A native AF_UNIX HTTP test backend, including on supported Windows runners.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
)

func main() {
	endpoint := flag.String("unix", "", "owned fixture socket path")
	ready := flag.String("ready-file", "", "owned fixture readiness file")
	flag.Parse()
	if *endpoint == "" || *ready == "" {
		log.Fatal("both fixture paths are required")
	}
	if _, err := os.Lstat(*endpoint); !os.IsNotExist(err) {
		log.Fatal("refusing to replace an existing socket")
	}
	listener, err := net.Listen("unix", *endpoint)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	message, err := json.Marshal(map[string]any{"network": "unix", "pid": os.Getpid()})
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*ready, message, 0o600); err != nil {
		log.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := "Jul CI Unix backend\n"
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("X-Backend-Host", url.PathEscape(r.Host))
		_, _ = w.Write([]byte(body))
	})
	if err := http.Serve(listener, handler); err != nil {
		log.Fatal(err)
	}
}
