//go:build ignore

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// fault-dnsd is a tiny authoritative DNS responder for #422 DNS fault evidence.
// It answers A queries for one decoy name and is meant to run on 127.0.0.1:53
// inside an unshared network namespace, never on a shared resolver.
//
// Its behavior is read from -state on every query, so a harness flips modes
// with a file write instead of a restart:
//
//	ok 127.0.0.2,127.0.0.3   answer with these A records (TTL -ttl)
//	servfail                  answer SERVFAIL
//	nxdomain                  answer NXDOMAIN
//	drop                      read the query and never answer (resolver timeout)
//
// Every query is appended to -log as "unix_ms qtype mode", which is the query
// load evidence (queries per refresh, retries during failure).
//
//	go run scripts/fault-dnsd.go -name jul-decoy.test -state /tmp/dns.state -log /tmp/dns.log
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:53", "UDP listen address")
	name := flag.String("name", "jul-decoy.test", "decoy name answered")
	state := flag.String("state", "", "mode file (see package doc)")
	queryLog := flag.String("log", "", "query log file")
	ttl := flag.Uint("ttl", 5, "TTL of answers, seconds")
	flag.Parse()

	fqdn, err := dnsmessage.NewName(strings.TrimSuffix(*name, ".") + ".")
	if err != nil {
		log.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	var lf *os.File
	if *queryLog != "" {
		if lf, err = os.OpenFile(*queryLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	var mu sync.Mutex
	buf := make([]byte, 1500)
	for {
		n, peer, err := pc.ReadFrom(buf)
		if err != nil {
			log.Fatal(err)
		}
		var p dnsmessage.Parser
		hdr, err := p.Start(buf[:n])
		if err != nil {
			continue
		}
		q, err := p.Question()
		if err != nil {
			continue
		}
		mode, ips := readState(*state)
		if lf != nil {
			mu.Lock()
			fmt.Fprintf(lf, "%d %s %s\n", time.Now().UnixMilli(), q.Type, mode)
			mu.Unlock()
		}
		if mode == "drop" {
			continue
		}
		resp := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: hdr.ID, Response: true, Authoritative: true, RecursionDesired: hdr.RecursionDesired},
			Questions: []dnsmessage.Question{q},
		}
		switch {
		case mode == "servfail":
			resp.RCode = dnsmessage.RCodeServerFailure
		case mode == "nxdomain" || !strings.EqualFold(q.Name.String(), fqdn.String()):
			resp.RCode = dnsmessage.RCodeNameError
		case q.Type == dnsmessage.TypeA:
			for _, ip := range ips {
				resp.Answers = append(resp.Answers, dnsmessage.Resource{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: uint32(*ttl)},
					Body:   &dnsmessage.AResource{A: ip.As4()},
				})
			}
		}
		// AAAA and other types for the decoy get NOERROR with no answers.
		out, err := resp.Pack()
		if err != nil {
			continue
		}
		_, _ = pc.WriteTo(out, peer)
	}
}

func readState(path string) (string, []netip.Addr) {
	if path == "" {
		return "servfail", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "servfail", nil
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return "servfail", nil
	}
	mode := fields[0]
	var ips []netip.Addr
	if mode == "ok" && len(fields) > 1 {
		for _, s := range strings.Split(fields[1], ",") {
			if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
				ips = append(ips, a)
			}
		}
	}
	return mode, ips
}
