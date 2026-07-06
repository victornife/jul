// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"io"
	"net"
	"testing"
	"time"

	"jul/internal/config"
)

// benchBackend starts a TCP echo backend for benchmarks.
func benchBackend() (addr string, stop func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _, _ = io.Copy(c, c); _ = c.Close() }(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

// benchAddr reserves an ephemeral TCP or UDP port.
func benchAddr(netType string) string {
	if netType == "tcp" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			panic(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		return addr
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	return addr
}

// benchSetup starts a TCP echo backend and a stream server proxying to it.
func benchSetup(b *testing.B) (proxyAddr, backendAddr string, stop func()) {
	backend, stopBackend := benchBackend()
	addr := benchAddr("tcp")
	s := NewServer(Options{Logger: discardLogger()})
	if err := s.Reload([]config.StreamServer{{
		Listen:    addr,
		Protocol:  "tcp",
		ProxyPass: backend,
	}}, nil); err != nil {
		b.Fatalf("reload: %v", err)
	}
	return addr, backend, func() {
		_ = s.Close()
		stopBackend()
	}
}

// BenchmarkTCPPassthrough measures the raw L4 relay overhead for a small
// payload: connect, write 64 bytes, read 64 bytes, close.
func BenchmarkTCPPassthrough(b *testing.B) {
	proxyAddr, _, stop := benchSetup(b)
	defer stop()

	payload := make([]byte, 64)
	for i := range payload {
		payload[i] = byte(i)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c, err := net.Dial("tcp", proxyAddr)
		if err != nil {
			b.Fatalf("dial: %v", err)
		}
		if _, err := c.Write(payload); err != nil {
			b.Fatalf("write: %v", err)
		}
		if _, err := io.ReadFull(c, payload); err != nil {
			b.Fatalf("read: %v", err)
		}
		_ = c.Close()
	}
}

// BenchmarkTCPParallel measures L4 relay throughput under concurrent
// connections.
func BenchmarkTCPParallel(b *testing.B) {
	proxyAddr, _, stop := benchSetup(b)
	defer stop()

	payload := make([]byte, 64)
	for i := range payload {
		payload[i] = byte(i)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c, err := net.Dial("tcp", proxyAddr)
			if err != nil {
				b.Fatalf("dial: %v", err)
			}
			if _, err := c.Write(payload); err != nil {
				b.Fatalf("write: %v", err)
			}
			if _, err := io.ReadFull(c, payload); err != nil {
				b.Fatalf("read: %v", err)
			}
			_ = c.Close()
		}
	})
}

// BenchmarkUDPRelay measures the per-datagram relay cost for UDP.
func BenchmarkUDPRelay(b *testing.B) {
	backend, stopBackend := benchUDPEcho()
	defer stopBackend()
	addr := benchAddr("udp")

	s := NewServer(Options{Logger: discardLogger()})
	if err := s.Reload([]config.StreamServer{{
		Listen:         addr,
		Protocol:       "udp",
		ProxyPass:      backend,
		MaxUDPSessions: 1024,
	}}, nil); err != nil {
		b.Fatalf("reload: %v", err)
	}
	defer s.Close()

	serverAddr, _ := net.ResolveUDPAddr("udp", addr)
	payload := make([]byte, 64)
	for i := range payload {
		payload[i] = byte(i)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		c, err := net.DialUDP("udp", nil, serverAddr)
		if err != nil {
			b.Fatalf("dial udp: %v", err)
		}
		defer c.Close()
		buf := make([]byte, 64)
		for pb.Next() {
			if _, err := c.Write(payload); err != nil {
				b.Fatalf("write: %v", err)
			}
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, err := c.Read(buf); err != nil {
				b.Fatalf("read: %v", err)
			}
		}
	})
}

// benchUDPEcho starts a UDP echo backend for benchmarks.
func benchUDPEcho() (addr string, stop func()) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	uc := pc.(*net.UDPConn)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, a, err := uc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = uc.WriteToUDP(buf[:n], a)
		}
	}()
	return uc.LocalAddr().String(), func() { _ = uc.Close() }
}
