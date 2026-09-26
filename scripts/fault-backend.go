//go:build ignore

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// fault-backend is the HTTP backend for #422 host-fault evidence. It listens on
// every -listen address (repeatable, e.g. 127.0.0.2:19181 inside a network
// namespace) and names itself in X-Backend so placement is observable.
//
//	/blob?kb=N   N KiB body, Cache-Control: public, max-age=600 (cache fill)
//	/slow?ms=N   sleep N ms first
//	anything     small JSON body
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type listens []string

func (l *listens) String() string     { return strings.Join(*l, ",") }
func (l *listens) Set(s string) error { *l = append(*l, s); return nil }

func main() {
	var addrs listens
	flag.Var(&addrs, "listen", "listen address (repeatable)")
	flag.Parse()
	if len(addrs) == 0 {
		addrs = listens{"127.0.0.1:19181"}
	}
	chunk := []byte(strings.Repeat("jul-fault-backend-", 57)[:1024])
	var wg sync.WaitGroup
	for _, a := range addrs {
		addr := a
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Backend", addr)
			if strings.Contains(r.URL.Path, "/slow") {
				ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
			if strings.Contains(r.URL.Path, "/blob") {
				kb, _ := strconv.Atoi(r.URL.Query().Get("kb"))
				if kb <= 0 || kb > 4096 {
					kb = 64
				}
				w.Header().Set("Cache-Control", "public, max-age=600")
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", strconv.Itoa(kb*1024))
				for i := 0; i < kb; i++ {
					if _, err := w.Write(chunk); err != nil {
						return
					}
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, "{\"backend\":%q,\"path\":%q}\n", addr, r.URL.Path)
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			log.Printf("fault-backend listening on %s", addr)
			log.Fatal(srv.ListenAndServe())
		}()
	}
	wg.Wait()
}
