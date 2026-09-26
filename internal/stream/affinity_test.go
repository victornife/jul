// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"jul/internal/affinity"
	"jul/internal/config"
)

func rankFor(servers []config.UpstreamServer, material string) []string {
	cands := make([]affinity.Candidate, len(servers))
	for i, s := range servers {
		cands[i] = affinity.NewCandidate("tcp", s.Address, s.Weight)
	}
	out := []string{}
	for _, i := range affinity.Rank(affinity.Sum(material), cands) {
		out = append(out, servers[i].Address)
	}
	return out
}

// TCP places each connection once, at establishment, by the canonical client
// address — here asserted through a trusted PROXY header so several clients
// can be exercised from one test host.
func TestStreamConsistentHashTCPByClientAddress(t *testing.T) {
	var servers []config.UpstreamServer
	names := map[string]string{}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("b%d", i)
		a, stop := tcpAnnounce(t, id)
		t.Cleanup(stop)
		servers = append(servers, config.UpstreamServer{Address: a, Weight: 1})
		names[a] = id
	}
	ups := map[string]config.UpstreamConfig{"db": {
		Name: "db", Strategy: "consistent_hash", Hash: &config.HashConfig{Key: "client_ip"},
		Servers: servers, MaxFails: 1,
	}}
	addr := freeTCPAddr(t)
	var mu sync.Mutex
	var outcomes []string
	s := newTestServer(t, Hooks{OnAffinityKey: func(pool, status string) {
		mu.Lock()
		defer mu.Unlock()
		outcomes = append(outcomes, pool+"/"+status)
	}})
	if err := s.Reload([]config.StreamServer{{
		Listen: addr, Protocol: "tcp", ProxyPass: "db", ProxyProtocol: "in",
		TrustedProxies: []string{"127.0.0.1", "::1"},
	}}, ups); err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, client := range []string{"203.0.113.7", "198.51.100.23", "192.0.2.200", "203.0.113.8"} {
		want := names[rankFor(servers, client)[0]]
		for j := 0; j < 2; j++ {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			_, _ = fmt.Fprintf(c, "PROXY TCP4 %s 10.0.0.1 %d 5432\r\n", client, 40000+j)
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			buf := make([]byte, 16)
			n, err := c.Read(buf)
			_ = c.Close()
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got := string(buf[:n]); got != want {
				t.Fatalf("client %s connection %d reached %s, want %s", client, j, got, want)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(outcomes) != 8 || outcomes[0] != "db/hashed" {
		t.Fatalf("key outcomes = %v, want one hashed outcome per connection", outcomes)
	}
}

// A keyed TCP connection whose preferred backend refuses falls through to the
// next backend in its rendezvous order, never re-dialing the refused one.
func TestStreamConsistentHashTCPFallsThroughRankedOrder(t *testing.T) {
	live, stop := tcpAnnounce(t, "live")
	t.Cleanup(stop)
	dead := freeTCPAddr(t) // reserved and released: nothing listens
	servers := []config.UpstreamServer{{Address: dead, Weight: 1}, {Address: live, Weight: 1}}
	ups := map[string]config.UpstreamConfig{"db": {
		Name: "db", Strategy: "consistent_hash", Hash: &config.HashConfig{Key: "client_ip"},
		Servers: servers, MaxFails: 5,
	}}
	var client string
	for i := 1; i < 255; i++ {
		client = fmt.Sprintf("198.51.100.%d", i)
		if rankFor(servers, client)[0] == dead {
			break
		}
	}
	addr := freeTCPAddr(t)
	s := newTestServer(t, Hooks{})
	if err := s.Reload([]config.StreamServer{{
		Listen: addr, Protocol: "tcp", ProxyPass: "db", ProxyProtocol: "in",
		TrustedProxies: []string{"127.0.0.1", "::1"},
	}}, ups); err != nil {
		t.Fatalf("reload: %v", err)
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = fmt.Fprintf(c, "PROXY TCP4 %s 10.0.0.1 40000 5432\r\n", client)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != "live" {
		t.Fatalf("read %q %v, want the live backend after the preferred one refused", buf[:n], err)
	}
}

// UDP selects once at session creation; every datagram of the session goes to
// the same backend, and the choice is the client address's rendezvous winner.
func TestStreamConsistentHashUDPSelectsAtSessionCreation(t *testing.T) {
	var servers []config.UpstreamServer
	names := map[string]string{}
	for i := 0; i < 3; i++ {
		tag := fmt.Sprintf("u%d", i)
		a, stop := udpAnnounce(t, tag)
		t.Cleanup(stop)
		servers = append(servers, config.UpstreamServer{Address: a, Weight: 1})
		names[a] = tag
	}
	ups := map[string]config.UpstreamConfig{"dns": {
		Name: "dns", Strategy: "consistent_hash", Hash: &config.HashConfig{Key: "client_ip"},
		Servers: servers, MaxFails: 1,
	}}
	addr := freeUDPAddr(t)
	s := newTestServer(t, Hooks{})
	if err := s.Reload([]config.StreamServer{{Listen: addr, Protocol: "udp", ProxyPass: "dns"}}, ups); err != nil {
		t.Fatalf("reload: %v", err)
	}
	want := names[rankFor(servers, "127.0.0.1")[0]]
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, 16)
	for i := 0; i < 5; i++ {
		_, _ = c.Write([]byte("q"))
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("datagram %d: %v", i, err)
		}
		if got := string(buf[:n]); got != want {
			t.Fatalf("datagram %d reached %s, want %s", i, got, want)
		}
	}
	// One established session, one selection.
	if sessions := udpSessionBackends(s, addr); len(sessions) != 1 {
		t.Fatalf("%d UDP sessions, want 1", len(sessions))
	}
}
