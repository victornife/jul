// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"io"
	"net"
	"testing"
	"time"

	"jul/internal/clientaddr"
)

// wrapped binds a loopback listener behind the PROXY-protocol wrapper trusting
// the given prefixes, and returns its address plus a channel of accepted conns.
func wrapped(t *testing.T, trusted []string) (string, <-chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	policy, err := clientaddr.NewPolicy(trusted, []string{}, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	pl := &proxyProtoListener{Listener: ln, trusted: policy}
	t.Cleanup(func() { _ = pl.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := pl.Accept()
		if err != nil {
			return
		}
		accepted <- c
	}()
	return ln.Addr().String(), accepted
}

// TestProxyProtoListenerReportsTheAdvertisedPeer proves the header supplies
// Boundary A: the accepted connection reports the balancer's advertised client,
// not the socket peer, and the bytes after the header are still readable.
func TestProxyProtoListenerReportsTheAdvertisedPeer(t *testing.T) {
	addr, accepted := wrapped(t, []string{"127.0.0.0/8"})

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("PROXY TCP4 198.51.100.9 10.0.0.1 4711 443\r\nGET / HTTP/1.1\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case conn := <-accepted:
		defer conn.Close()
		if got := conn.RemoteAddr().String(); got != "198.51.100.9:4711" {
			t.Errorf("RemoteAddr = %q, want the advertised client 198.51.100.9:4711", got)
		}
		// The request bytes buffered past the header must survive the wrap.
		buf := make([]byte, 16)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := io.ReadFull(conn, buf)
		if err != nil {
			t.Fatalf("read past header: %v", err)
		}
		if string(buf[:n]) != "GET / HTTP/1.1\r\n" {
			t.Errorf("bytes past header = %q, want the request line", buf[:n])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("connection was not accepted")
	}
}

// TestProxyProtoListenerRefusesAnUntrustedPeer pins the trust boundary: the
// header is an assertion, so a peer outside the declared set is closed rather
// than served on its own address.
func TestProxyProtoListenerRefusesAnUntrustedPeer(t *testing.T) {
	addr, accepted := wrapped(t, []string{"10.0.0.0/8"}) // loopback is not trusted

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("PROXY TCP4 198.51.100.9 10.0.0.1 4711 443\r\n"))

	select {
	case conn := <-accepted:
		conn.Close()
		t.Fatalf("untrusted peer was accepted as %s", conn.RemoteAddr())
	case <-time.After(300 * time.Millisecond):
	}
	// The listener must still be serving: a refusal drops one connection, it
	// does not tear down the accept loop.
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("refused connection stayed open")
	}
}

// TestProxyProtoListenerRefusesAMalformedHeader covers the other rejection
// path: a trusted peer that does not speak the protocol.
func TestProxyProtoListenerRefusesAMalformedHeader(t *testing.T) {
	addr, accepted := wrapped(t, []string{"127.0.0.0/8"})

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"))

	select {
	case conn := <-accepted:
		conn.Close()
		t.Fatal("a connection with no PROXY header was accepted")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestProxyProtoListenerKeepsThePeerForLocalHeaders covers the LOCAL/UNKNOWN
// case, which a balancer emits for its own health checks: the connection is
// served, on the real socket peer.
func TestProxyProtoListenerKeepsThePeerForLocalHeaders(t *testing.T) {
	addr, accepted := wrapped(t, []string{"127.0.0.0/8"})

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("PROXY UNKNOWN\r\n"))

	select {
	case conn := <-accepted:
		defer conn.Close()
		host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
		if err != nil || host != "127.0.0.1" {
			t.Errorf("RemoteAddr = %q, want the real loopback peer", conn.RemoteAddr())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("LOCAL header connection was not accepted")
	}
}
