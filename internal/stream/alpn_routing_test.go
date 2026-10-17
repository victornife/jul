// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

func TestALPNRuleSelection(t *testing.T) {
	pools := make([]upstream.Pool, 6)
	r := route{defaultPool: &pools[0], sniPools: map[string]*upstream.Pool{"*": &pools[1], "shared.example": &pools[2]}, alpnRoutes: []alpnRoute{
		{serverName: "*", protocols: map[string]struct{}{"h2": {}}, pool: &pools[3]},
		{serverName: "shared.example", protocols: map[string]struct{}{"http/1.1": {}}, pool: &pools[4]},
		{serverName: "shared.example", protocols: map[string]struct{}{"h2": {}}, pool: &pools[5]},
	}}
	for _, tt := range []struct {
		host   string
		offers []string
		want   int
	}{
		{"SHARED.example", []string{"h2", "http/1.1"}, 4},
		{"shared.example", []string{"http/1.1", "h2"}, 4},
		{"shared.example", []string{"h2"}, 5},
		{"other.example", []string{"h2"}, 3},
		{"", []string{"h2"}, 3},
		{"shared.example", nil, 2},
		{"shared.example", []string{"H2", "xh2", "x,h2", "h2-extra"}, 2},
		{"other.example", []string{"unknown"}, 1},
		{"", nil, 1},
	} {
		if got := r.selectPool(clientHelloInfo{serverName: tt.host, protocols: tt.offers}); got != &pools[tt.want] {
			t.Fatalf("host %q offers %q: incorrect pool", tt.host, tt.offers)
		}
	}
	r.sniPools = nil
	if r.selectPool(clientHelloInfo{}) != &pools[0] {
		t.Fatal("missing default fallback")
	}
	r.defaultPool = nil
	if r.selectPool(clientHelloInfo{}) != nil {
		t.Fatal("no-match should have no target")
	}
}

// Backend-owned keys/certificates and real TLS negotiation. Its announcement
// identifies the selected backend independently of the negotiated protocol.
func alpnTLSBackend(t *testing.T, marker byte, protocols []string) (string, *x509.Certificate) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(int64(marker)), DNSNames: []string{"shared.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, NextProtos: protocols})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := c.Write([]byte{marker}); err == nil {
					_, _ = io.Copy(c, c)
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); <-done; wg.Wait() })
	return ln.Addr().String(), cert
}

func dialALPN(t *testing.T, addr string, roots *x509.CertPool, offers []string, marker byte, negotiated string, cert *x509.Certificate) *tls.Conn {
	t.Helper()
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", addr, &tls.Config{ServerName: "shared.example", RootCAs: roots, MinVersion: tls.VersionTLS12, NextProtos: offers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	var got [1]byte
	if _, err := io.ReadFull(c, got[:]); err != nil || got[0] != marker {
		t.Fatalf("backend marker %q want %q: %v", got, marker, err)
	}
	state := c.ConnectionState()
	if state.NegotiatedProtocol != negotiated || !bytes.Equal(state.PeerCertificates[0].Raw, cert.Raw) {
		t.Fatalf("backend negotiation/certificate: %+v", state)
	}
	return c
}

func TestALPNRealTLSNegotiationReloadAndAdmission(t *testing.T) {
	a, certA := alpnTLSBackend(t, 'A', []string{"http/1.1", "h2"})
	b, certB := alpnTLSBackend(t, 'B', []string{"jul-demo/1"})
	roots := x509.NewCertPool()
	roots.AddCert(certA)
	roots.AddCert(certB)
	s := newTestServer(t, Hooks{})
	addr := freeTCPAddr(t)
	ups := map[string]config.UpstreamConfig{"app": {Name: "app", Servers: []config.UpstreamServer{{Address: a, Weight: 1}}, Resilience: &config.ResilienceConfig{MaxActiveRequests: 1}}}
	st := config.StreamServer{Listen: addr, ALPNRoutes: []config.StreamALPNRoute{{ServerName: "shared.example", Protocols: []string{"h2", "http/1.1", "jul-demo/1"}, ProxyPass: "app"}}}
	if err := s.PreflightBuild(context.Background(), []config.StreamServer{st}, ups); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload([]config.StreamServer{st}, ups); err != nil {
		t.Fatal(err)
	}
	old := dialALPN(t, addr, roots, []string{"h2", "http/1.1"}, 'A', "http/1.1", certA)
	r := s.listeners["tcp|"+addr].route.Load()
	if r.alpnRoutes[0].pool.Admission().Active() != 1 {
		t.Fatal("ALPN-selected pool did not acquire admission")
	}
	blocked, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = blocked.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = blocked.Write(alpnHello("shared.example", helloExtension(16, alpnExtension("h2"))))
	var one [1]byte
	if _, err = blocked.Read(one[:]); err == nil {
		t.Fatal("ALPN connection exceeded shared pool admission")
	}
	_ = blocked.Close()
	// Changing the caller's config after publication cannot change the snapshot.
	st.ALPNRoutes[0].Protocols[0] = "changed"
	if _, ok := r.alpnRoutes[0].protocols["h2"]; !ok {
		t.Fatal("snapshot aliases mutable config")
	}
	invalid := st
	invalid.ALPNRoutes = []config.StreamALPNRoute{{ServerName: "*", Protocols: []string{"h2"}, ProxyPass: "missing"}}
	if err := s.Reload([]config.StreamServer{invalid}, ups); err == nil {
		t.Fatal("invalid reload accepted")
	}
	if s.listeners["tcp|"+addr].route.Load() != r {
		t.Fatal("failed reload changed live snapshot")
	}
	st.ALPNRoutes = []config.StreamALPNRoute{{ServerName: "shared.example", Protocols: []string{"jul-demo/1"}, ProxyPass: b}}
	if err := s.Reload([]config.StreamServer{st}, ups); err != nil {
		t.Fatal(err)
	}
	newConn := dialALPN(t, addr, roots, []string{"jul-demo/1"}, 'B', "jul-demo/1", certB)
	for _, c := range []*tls.Conn{old, newConn} {
		payload := []byte("payload\x00\xff")
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(payload, got) {
			t.Fatalf("established relay after reload: %v", err)
		}
		_ = c.Close()
	}
	if !eventually(func() bool { return r.alpnRoutes[0].pool.Admission().Active() == 0 }) {
		t.Fatal("admission leaked")
	}
}

func TestALPNRouteBuildRejectsInvalidAndUDP(t *testing.T) {
	s := newTestServer(t, Hooks{})
	for _, st := range []config.StreamServer{
		{Listen: freeTCPAddr(t), ALPNRoutes: []config.StreamALPNRoute{{ServerName: "", Protocols: []string{"h2"}, ProxyPass: "127.0.0.1:9"}}},
		{Listen: freeTCPAddr(t), Protocol: "udp", ALPNRoutes: []config.StreamALPNRoute{{ServerName: "*", Protocols: []string{"h2"}, ProxyPass: "127.0.0.1:9"}}},
	} {
		if err := s.Reload([]config.StreamServer{st}, nil); err == nil {
			t.Fatal("invalid route accepted")
		}
	}
	if !reflect.DeepEqual(s.BoundKeys(), []string{}) {
		t.Fatal("invalid build bound listeners")
	}
}

func TestALPNTCPReplayIncludesSubsequentPayload(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	s := newTestServer(t, Hooks{})
	addr := freeTCPAddr(t)
	if err := s.Reload([]config.StreamServer{{Listen: addr, ALPNRoutes: []config.StreamALPNRoute{{ServerName: "shared.example", Protocols: []string{"h2"}, ProxyPass: backend}}}}, nil); err != nil {
		t.Fatal(err)
	}
	hello := alpnHello("shared.example", helloExtension(16, alpnExtension("h2", "http/1.1")))
	wire := append(fragmentClientHello(t, hello, 1, 1, 2, 3, 4), []byte("subsequent\x00\xffpayload")...)
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(wire); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(wire))
	if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, wire) {
		t.Fatalf("backend replay changed wire/payload: %v", err)
	}
}

func TestALPNInvalidPoolBuildIsAtomic(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	s := newTestServer(t, Hooks{})
	addr := freeTCPAddr(t)
	st := config.StreamServer{Listen: addr, ALPNRoutes: []config.StreamALPNRoute{{ServerName: "*", Protocols: []string{"h2"}, ProxyPass: backend}}}
	if err := s.Reload([]config.StreamServer{st}, nil); err != nil {
		t.Fatal(err)
	}
	before := s.listeners["tcp|"+addr].route.Load()
	st.ALPNRoutes[0].ProxyPass = "empty"
	if err := s.Reload([]config.StreamServer{st}, map[string]config.UpstreamConfig{"empty": {Name: "empty"}}); err == nil {
		t.Fatal("empty ALPN backend pool accepted")
	}
	if s.listeners["tcp|"+addr].route.Load() != before {
		t.Fatal("invalid pool replaced live route")
	}
}
