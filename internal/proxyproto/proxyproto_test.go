// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package proxyproto

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"testing"
)

func TestV2RoundTrip(t *testing.T) {
	src := &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 4567}
	dst := &net.TCPAddr{IP: net.ParseIP("10.9.8.7"), Port: 89}
	var buf bytes.Buffer
	if err := WriteV2(&buf, src, dst); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadHeader(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got == nil || got.String() != "10.1.2.3:4567" {
		t.Errorf("round-trip src = %v, want 10.1.2.3:4567", got)
	}
}

func TestV2IPv6RoundTrip(t *testing.T) {
	src := &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 443}
	dst := &net.TCPAddr{IP: net.ParseIP("2001:db8::2"), Port: 80}
	var buf bytes.Buffer
	if err := WriteV2(&buf, src, dst); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadHeader(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got == nil || got.String() != "[2001:db8::1]:443" {
		t.Errorf("round-trip src = %v, want [2001:db8::1]:443", got)
	}
}

func TestV1Parse(t *testing.T) {
	for _, tt := range []struct {
		name string
		line string
		want string
	}{
		{name: "tcp4", line: "PROXY TCP4 1.2.3.4 5.6.7.8 1111 2222\r\n", want: "1.2.3.4:1111"},
		{name: "tcp6", line: "PROXY TCP6 2001:db8::1 2001:db8::2 1111 2222\r\n", want: "[2001:db8::1]:1111"},
		{name: "unknown keeps the real peer", line: "PROXY UNKNOWN\r\n", want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadHeader(bufio.NewReader(bytes.NewReader([]byte(tt.line))))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if tt.want == "" {
				if got != nil {
					t.Fatalf("addr = %v, want nil", got)
				}
				return
			}
			if got == nil || got.String() != tt.want {
				t.Errorf("addr = %v, want %s", got, tt.want)
			}
		})
	}
}

func TestMalformedHeadersAreRejected(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   []byte
	}{
		{name: "not a proxy header", in: []byte("GET / HTTP/1.1\r\n")},
		{name: "truncated prefix", in: []byte("PROXY ")},
		{name: "bare LF", in: []byte("PROXY UNKNOWN\n")},
		{name: "bad transport", in: []byte("PROXY SCTP 1.2.3.4 5.6.7.8 1 2\r\n")},
		{name: "bad source address", in: []byte("PROXY TCP4 not-an-ip 5.6.7.8 1 2\r\n")},
		{name: "bad destination address", in: []byte("PROXY TCP4 1.2.3.4 not-an-ip 1 2\r\n")},
		{name: "bad destination port", in: []byte("PROXY TCP4 1.2.3.4 5.6.7.8 1 nope\r\n")},
		{name: "TCP4 with IPv6", in: []byte("PROXY TCP4 2001:db8::1 2001:db8::2 1 2\r\n")},
		{name: "TCP6 with IPv4", in: []byte("PROXY TCP6 1.2.3.4 5.6.7.8 1 2\r\n")},
		{name: "wrong field count", in: []byte("PROXY TCP4 1.2.3.4 5.6.7.8\r\n")},
		{name: "port out of range", in: []byte("PROXY TCP4 1.2.3.4 5.6.7.8 99999 2\r\n")},
		{name: "v2 bad version", in: append(append([]byte{}, v2Signature...), 0x11, 0x11, 0x00, 0x0C)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ReadHeader(bufio.NewReader(bytes.NewReader(tt.in))); err == nil {
				t.Fatal("malformed header was accepted")
			}
		})
	}
}

func TestV1LengthBoundary(t *testing.T) {
	valid := []byte("PROXY UNKNOWN " + strings.Repeat("x", 92) + "\r\n")
	if len(valid) != v1MaxLen {
		t.Fatalf("test header length = %d, want %d", len(valid), v1MaxLen)
	}
	if addr, err := ReadHeader(bufio.NewReader(bytes.NewReader(valid))); err != nil || addr != nil {
		t.Fatalf("maximum-size header: addr=%v err=%v", addr, err)
	}

	for _, tt := range []struct {
		name string
		in   []byte
	}{
		{name: "one byte over", in: []byte("PROXY UNKNOWN " + strings.Repeat("x", 93) + "\r\n")},
		{name: "huge no newline", in: []byte("PROXY UNKNOWN " + strings.Repeat("x", 64<<10))},
		{name: "newline far beyond limit", in: []byte("PROXY UNKNOWN " + strings.Repeat("x", 64<<10) + "\r\n")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &countingReader{r: bytes.NewReader(tt.in), maxChunk: 7}
			_, err := ReadHeader(bufio.NewReaderSize(r, 16))
			if err == nil {
				t.Fatal("oversized header was accepted")
			}
			if r.n > v1MaxLen+16 {
				t.Fatalf("parser read %d bytes before rejection, want at most %d", r.n, v1MaxLen+16)
			}
		})
	}
}

func TestV1FragmentedInput(t *testing.T) {
	line := []byte("PROXY TCP4 192.0.2.10 198.51.100.20 1234 443\r\nrest")
	for _, chunk := range []int{1, 2, 3, 7} {
		t.Run(string(rune('0'+chunk)), func(t *testing.T) {
			r := &countingReader{r: bytes.NewReader(line), maxChunk: chunk}
			br := bufio.NewReaderSize(r, 16)
			addr, err := ReadHeader(br)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if addr.String() != "192.0.2.10:1234" {
				t.Fatalf("addr = %v", addr)
			}
			got, _ := br.ReadString('t')
			if got != "rest" {
				t.Fatalf("payload = %q, want rest", got)
			}
		})
	}
}

func TestV1PartialReadError(t *testing.T) {
	r := &terminalReader{data: []byte("PROXY TCP4 192.0.2.1")}
	if _, err := ReadHeader(bufio.NewReaderSize(r, 16)); err == nil {
		t.Fatal("partial header was accepted")
	}
}

type countingReader struct {
	r        *bytes.Reader
	maxChunk int
	n        int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if len(p) > r.maxChunk {
		p = p[:r.maxChunk]
	}
	n, err := r.r.Read(p)
	r.n += n
	return n, err
}

type terminalReader struct {
	data []byte
}

func (r *terminalReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, &timeoutError{}
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

type timeoutError struct{}

func (*timeoutError) Error() string   { return "timeout" }
func (*timeoutError) Timeout() bool   { return true }
func (*timeoutError) Temporary() bool { return true }

// FuzzReadHeader exercises the v1/v2 parser against adversarial input. The
// oracle is that it never panics and never returns both a nil error and an
// address it has not validated.
func FuzzReadHeader(f *testing.F) {
	f.Add([]byte("PROXY TCP4 192.168.1.1 10.0.0.1 443 80\r\n"))
	f.Add([]byte("PROXY TCP6 2001:db8::1 2001:db8::2 443 80\r\n"))
	f.Add([]byte("PROXY UNKNOWN\r\n"))
	f.Add(append(append([]byte{}, v2Signature...), 0x21, 0x00, 0x00, 0x00))
	f.Add([]byte("PROXY "))
	f.Add([]byte("PROXY UNKNOWN " + strings.Repeat("x", v1MaxLen) + "\r\n"))
	f.Add([]byte("PROXY UNKNOWN " + strings.Repeat("x", v1MaxLen)))
	f.Add([]byte("PROXY    TCP4 192.0.2.1 198.51.100.1 0 65535\r\n"))
	f.Add(append(append([]byte{}, v2Signature...), 0x21, 0x11, 0x00, 0x0C, 0x7f, 0x00, 0x00, 0x01))
	f.Add([]byte(""))
	f.Add([]byte("GET / HTTP/1.1\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		br := bufio.NewReaderSize(bytes.NewReader(data), 256)
		addr, err := ReadHeader(br)
		if err != nil {
			return
		}
		// A nil address is the documented LOCAL/UNKNOWN signal. Anything else
		// must be a usable TCP address, never a half-built one.
		if addr == nil {
			return
		}
		tcp, ok := addr.(*net.TCPAddr)
		if !ok {
			t.Fatalf("addr type = %T, want *net.TCPAddr", addr)
		}
		if tcp.IP == nil {
			t.Fatal("accepted a header with no source address")
		}
		if tcp.Port < 0 || tcp.Port > 65535 {
			t.Fatalf("accepted port %d", tcp.Port)
		}
	})
}
