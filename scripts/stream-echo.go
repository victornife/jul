//go:build ignore

// Tiny TCP echo server for L4 stream burn-in testing.
// Usage: go run scripts/stream-echo.go -port 55432
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
)

func main() {
	port := flag.String("port", "55432", "Port to listen on")
	flag.Parse()

	ln, err := net.Listen("tcp", ":"+*port)
	if err != nil {
		fmt.Printf("listen error: %v\n", err)
		os.Exit(1)
	}
	defer ln.Close()
	fmt.Printf("TCP echo listening on :%s\n", *port)

	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Printf("accept error: %v\n", err)
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			_, _ = io.Copy(c, c)
		}(conn)
	}
}
