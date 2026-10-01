// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

// A UDP stream can never relay a group member's reply, so multicast and
// broadcast backends are rejected, whether literal or listed in an upstream
// (#511).
func TestValidateRejectsUDPGroupBackends(t *testing.T) {
	base := func(target string, servers ...string) *Config {
		c := &Config{Servers: []ServerConfig{{Listen: "127.0.0.1:80"}}}
		if len(servers) > 0 {
			up := UpstreamConfig{Name: "grp"}
			for _, s := range servers {
				up.Servers = append(up.Servers, UpstreamServer{Address: s, Weight: 1})
			}
			c.Upstreams = []UpstreamConfig{up}
		}
		c.Streams = []StreamServer{{Listen: "0.0.0.0:1900", Protocol: "udp", ProxyPass: target}}
		return c
	}
	for name, c := range map[string]*Config{
		"ipv4 multicast (SSDP)":   base("239.255.255.250:1900"),
		"ipv4 multicast low":      base("224.0.0.1:5000"),
		"limited broadcast":       base("255.255.255.255:9"),
		"ipv6 multicast":          base("[ff02::c]:1900"),
		"ipv6 multicast zoned":    base("[ff02::fb%eth0]:5353"),
		"upstream with multicast": base("grp", "10.0.0.5:1900", "239.1.2.3:1900"),
	} {
		t.Run(name, func(t *testing.T) {
			err := Validate(c)
			if err == nil || !strings.Contains(err.Error(), "multicast or broadcast") {
				t.Fatalf("Validate() = %v, want a multicast/broadcast rejection", err)
			}
		})
	}
	for name, c := range map[string]*Config{
		"unicast literal":  base("10.0.0.5:1900"),
		"unicast upstream": base("grp", "10.0.0.5:1900", "[2001:db8::1]:1900"),
		"hostname":         base("devices.example.internal:1900"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(c); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}

	// TCP streams are not affected: a TCP connect to a group address simply
	// fails, and the relay's reply rule does not apply.
	c := base("239.255.255.250:1900")
	c.Streams[0].Protocol = "tcp"
	if err := Validate(c); err != nil && strings.Contains(err.Error(), "multicast or broadcast") {
		t.Fatalf("a TCP stream was rejected by the UDP group rule: %v", err)
	}
}
