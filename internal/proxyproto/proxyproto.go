// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package proxyproto parses and emits HAProxy PROXY-protocol headers.
//
// The header preserves the real client address across a proxy hop. It is an
// assertion made by whoever opened the socket, never a kernel fact, so a caller
// must establish that the peer is a declared proxy before believing the result
// (ADR 0016 §6b). This package performs no trust decision of its own.
//
// It is shared by the L4 stream listeners and by HTTP listeners that ingest a
// header from a TCP load balancer, so both boundaries parse identical bytes
// identically. Only the TCP4/TCP6 transports are handled; LOCAL and UNKNOWN
// connections yield a nil address, signalling the caller to keep the real peer.
//
// Reference: https://www.haproxy.org/download/2.8/doc/proxy-protocol.txt
package proxyproto

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

var v2Signature = []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}

const v1MaxLen = 108 // including trailing CRLF

// ReadHeader consumes a PROXY protocol header (v1 text or v2 binary) from br
// and returns the advertised source address. For LOCAL/UNKNOWN headers it
// returns a nil address, signaling the caller to keep the real peer address.
// It returns an error when no valid header is present, since a listener that
// calls this is configured to require one.
func ReadHeader(br *bufio.Reader) (net.Addr, error) {
	sig, err := br.Peek(12)
	if err != nil {
		return nil, fmt.Errorf("proxy protocol: read signature: %w", err)
	}
	if bytes.Equal(sig, v2Signature) {
		return readV2(br)
	}
	if bytes.HasPrefix(sig, []byte("PROXY ")) {
		return readV1(br)
	}
	return nil, errors.New("proxy protocol: missing or malformed header")
}

// readV1 parses the text "PROXY TCP4 src dst sport dport\r\n" form.
func readV1(br *bufio.Reader) (net.Addr, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("proxy protocol v1: read line: %w", err)
	}
	if len(line) > v1MaxLen {
		return nil, errors.New("proxy protocol v1: header too long")
	}
	line = line[:len(line)-1] // drop \n
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	fields := bytes.Fields([]byte(line))
	if len(fields) < 2 || string(fields[0]) != "PROXY" {
		return nil, errors.New("proxy protocol v1: malformed header")
	}
	switch string(fields[1]) {
	case "UNKNOWN":
		return nil, nil
	case "TCP4", "TCP6":
		if len(fields) != 6 {
			return nil, errors.New("proxy protocol v1: wrong field count")
		}
		ip := net.ParseIP(string(fields[2]))
		if ip == nil {
			return nil, errors.New("proxy protocol v1: bad source address")
		}
		port, err := strconv.Atoi(string(fields[4]))
		if err != nil || port < 0 || port > 65535 {
			return nil, errors.New("proxy protocol v1: bad source port")
		}
		return &net.TCPAddr{IP: ip, Port: port}, nil
	default:
		return nil, fmt.Errorf("proxy protocol v1: unsupported transport %q", fields[1])
	}
}

// readV2 parses the binary v2 header.
func readV2(br *bufio.Reader) (net.Addr, error) {
	hdr := make([]byte, 16)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return nil, fmt.Errorf("proxy protocol v2: read header: %w", err)
	}
	verCmd := hdr[12]
	if verCmd>>4 != 0x2 {
		return nil, errors.New("proxy protocol v2: bad version")
	}
	famProto := hdr[13]
	length := binary.BigEndian.Uint16(hdr[14:16])
	addrBlock := make([]byte, length)
	if _, err := io.ReadFull(br, addrBlock); err != nil {
		return nil, fmt.Errorf("proxy protocol v2: read addresses: %w", err)
	}
	// LOCAL command (health checks): no real client address.
	if verCmd&0x0F == 0x0 {
		return nil, nil
	}
	switch famProto >> 4 {
	case 0x1: // AF_INET
		if len(addrBlock) < 12 {
			return nil, errors.New("proxy protocol v2: short IPv4 block")
		}
		ip := net.IP(addrBlock[0:4])
		port := binary.BigEndian.Uint16(addrBlock[8:10])
		return &net.TCPAddr{IP: ip, Port: int(port)}, nil
	case 0x2: // AF_INET6
		if len(addrBlock) < 36 {
			return nil, errors.New("proxy protocol v2: short IPv6 block")
		}
		ip := net.IP(addrBlock[0:16])
		port := binary.BigEndian.Uint16(addrBlock[32:34])
		return &net.TCPAddr{IP: ip, Port: int(port)}, nil
	default: // AF_UNSPEC or AF_UNIX: no usable TCP address
		return nil, nil
	}
}

// WriteV2 emits a v2 binary PROXY header describing the src->dst TCP flow.
// When either address is not a TCP address it emits a LOCAL header so the
// backend still sees a well-formed PROXY preamble.
func WriteV2(w io.Writer, src, dst net.Addr) error {
	srcTCP, sok := src.(*net.TCPAddr)
	dstTCP, dok := dst.(*net.TCPAddr)
	buf := make([]byte, 0, 52)
	buf = append(buf, v2Signature...)

	if !sok || !dok {
		// LOCAL, UNSPEC, zero address length.
		buf = append(buf, 0x20, 0x00, 0x00, 0x00)
		_, err := w.Write(buf)
		return err
	}

	s4 := srcTCP.IP.To4()
	d4 := dstTCP.IP.To4()
	if s4 != nil && d4 != nil {
		buf = append(buf, 0x21, 0x11) // PROXY, AF_INET + STREAM
		buf = binary.BigEndian.AppendUint16(buf, 12)
		buf = append(buf, s4...)
		buf = append(buf, d4...)
		buf = binary.BigEndian.AppendUint16(buf, uint16(srcTCP.Port))
		buf = binary.BigEndian.AppendUint16(buf, uint16(dstTCP.Port))
	} else {
		buf = append(buf, 0x21, 0x21) // PROXY, AF_INET6 + STREAM
		buf = binary.BigEndian.AppendUint16(buf, 36)
		buf = append(buf, srcTCP.IP.To16()...)
		buf = append(buf, dstTCP.IP.To16()...)
		buf = binary.BigEndian.AppendUint16(buf, uint16(srcTCP.Port))
		buf = binary.BigEndian.AppendUint16(buf, uint16(dstTCP.Port))
	}
	_, err := w.Write(buf)
	return err
}
