// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func alpnExtension(protocols ...string) []byte {
	var list []byte
	for _, protocol := range protocols {
		list = append(list, byte(len(protocol)))
		list = append(list, protocol...)
	}
	return append(binary.BigEndian.AppendUint16(nil, uint16(len(list))), list...)
}

func helloExtension(kind uint16, body []byte) []byte {
	ext := binary.BigEndian.AppendUint16(nil, kind)
	ext = binary.BigEndian.AppendUint16(ext, uint16(len(body)))
	return append(ext, body...)
}

// alpnHello builds a structurally valid, deterministic TLS 1.2 ClientHello.
func alpnHello(host string, extensions ...[]byte) []byte {
	body := append([]byte{1, 0, 0, 0, 3, 3}, make([]byte, 32)...)
	body = append(body, 0, 0, 2, 0x13, 1, 1, 0)
	var exts []byte
	if host != "" {
		name := binary.BigEndian.AppendUint16([]byte{0}, uint16(len(host)))
		name = append(name, host...)
		sni := binary.BigEndian.AppendUint16(nil, uint16(len(name)))
		exts = append(exts, helloExtension(0, append(sni, name...))...)
	}
	for _, ext := range extensions {
		exts = append(exts, ext...)
	}
	body = binary.BigEndian.AppendUint16(body, uint16(len(exts)))
	body = append(body, exts...)
	n := len(body) - 4
	body[1], body[2], body[3] = byte(n>>16), byte(n>>8), byte(n)
	return appendHandshakeRecord(nil, []byte{3, 1}, body)
}

func TestALPNExtension(t *testing.T) {
	for _, tt := range []struct {
		name string
		body []byte
		want []string
	}{
		{"single", alpnExtension("h2"), []string{"h2"}},
		{"multiple", alpnExtension("http/1.1", "h2"), []string{"http/1.1", "h2"}},
		{"opaque exact bytes", alpnExtension("H2", "x,h2", "\x00\xff"), []string{"H2", "x,h2", "\x00\xff"}},
		{"repeated", alpnExtension("h2", "h2"), []string{"h2", "h2"}},
		{"maximum length", alpnExtension(strings.Repeat("x", 255)), []string{strings.Repeat("x", 255)}},
		{"empty list", alpnExtension(), nil},
		{"empty identifier", alpnExtension("h2", ""), nil},
		{"missing list length", []byte{0}, nil},
		{"wrong list length", []byte{0, 2, 2, 'h', '2'}, nil},
		{"truncated final identifier", []byte{0, 6, 2, 'h', '2', 3, 'a', 'b'}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseALPNExtension(tt.body); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("offers = %q, want %q", got, tt.want)
			}
		})
	}
	protocols := make([]string, tlsALPNMaxProtocols+1)
	for i := range protocols {
		protocols[i] = "p"
	}
	if got := parseALPNExtension(alpnExtension(protocols[:tlsALPNMaxProtocols]...)); len(got) != tlsALPNMaxProtocols {
		t.Fatalf("exact count cap: got %d", len(got))
	}
	if got := parseALPNExtension(alpnExtension(protocols...)); got != nil {
		t.Fatal("accepted offers above count cap")
	}
}

func TestALPNPrereadFragmentationAndReplay(t *testing.T) {
	ext := helloExtension(16, alpnExtension("h2", "http/1.1"))
	hello := alpnHello("shared.example", ext)
	// Exercise every two-record cut, including handshake, extension and token headers.
	for cut := 1; cut < len(hello)-5; cut++ {
		wire := fragmentClientHello(t, hello, cut)
		wire = append(wire, []byte("subsequent payload\x00\xff")...)
		br := bufio.NewReaderSize(iotest.OneByteReader(bytes.NewReader(wire)), tlsInspectMax)
		got := peekClientHello(br, true)
		if got.serverName != "shared.example" || !reflect.DeepEqual(got.protocols, []string{"h2", "http/1.1"}) {
			t.Fatalf("cut %d: result = %+v", cut, got)
		}
		replay, err := io.ReadAll(br)
		if err != nil || !bytes.Equal(replay, wire) {
			t.Fatalf("cut %d: changed bytes: %v", cut, err)
		}
	}
}

func TestALPNMalformedAndLegacyCompatibility(t *testing.T) {
	valid := helloExtension(16, alpnExtension("h2"))
	for _, extensions := range [][][]byte{
		{helloExtension(16, []byte{0, 1, 0})},
		{valid, valid},
		{helloExtension(16, []byte{0, 6, 2, 'h', '2', 3, 'a', 'b'})},
	} {
		wire := alpnHello("legacy.example", extensions...)
		br := bufio.NewReaderSize(bytes.NewReader(wire), tlsInspectMax)
		if got := peekSNI(br); got != "legacy.example" {
			t.Fatalf("legacy SNI changed: %q", got)
		}
		if got := peekClientHello(br, true); got.serverName != "" || len(got.protocols) != 0 {
			t.Fatalf("partial malformed result: %+v", got)
		}
	}
	validWire := alpnHello("", valid)
	if got := peekClientHello(bufio.NewReaderSize(bytes.NewReader(validWire), tlsInspectMax), true); got.serverName != "" || !reflect.DeepEqual(got.protocols, []string{"h2"}) {
		t.Fatalf("no SNI: %+v", got)
	}
	for end := 0; end < len(validWire); end++ {
		br := bufio.NewReaderSize(bytes.NewReader(validWire[:end]), tlsInspectMax)
		if got := peekClientHello(br, true); got.serverName != "" || len(got.protocols) != 0 {
			t.Fatalf("truncation %d accepted", end)
		}
	}
	br := bufio.NewReaderSize(io.MultiReader(bytes.NewReader(validWire[:len(validWire)-1]), timeoutSNIReader{}), tlsInspectMax)
	if got := peekClientHello(br, true); len(got.protocols) != 0 {
		t.Fatal("timeout accepted")
	}
	if got := peekClientHello(bufio.NewReader(bytes.NewBufferString("GET / HTTP/1.1\r\n")), true); len(got.protocols) != 0 {
		t.Fatal("non-TLS accepted")
	}
}

func TestALPNInspectBounds(t *testing.T) {
	ext := helloExtension(16, alpnExtension("h2"))
	base := alpnHello("bounded.example", ext)
	padding := tlsClientHelloMax - (len(base) - 5) - 4
	exact := alpnHello("bounded.example", ext, helloExtension(21, make([]byte, padding)))
	if len(exact)-5 != tlsClientHelloMax {
		t.Fatal("bad byte-cap fixture")
	}
	for _, wire := range [][]byte{base, exact} {
		got := peekClientHello(bufio.NewReaderSize(bytes.NewReader(wire), tlsInspectMax), true)
		if got.serverName != "bounded.example" || !reflect.DeepEqual(got.protocols, []string{"h2"}) {
			t.Fatalf("valid bounded hello: %+v", got)
		}
	}
	sizes := make([]int, tlsClientHelloMaxRecords-1)
	for i := range sizes {
		sizes[i] = 1
	}
	atRecordCap := fragmentClientHello(t, base, sizes...)
	if got := peekClientHello(bufio.NewReaderSize(bytes.NewReader(atRecordCap), tlsInspectMax), true); len(got.protocols) != 1 {
		t.Fatal("rejected exact record cap")
	}
	for _, wire := range [][]byte{
		fragmentClientHello(t, base, append(sizes, 1)...),
		alpnHello("bounded.example", ext, helloExtension(21, make([]byte, padding+1))),
		{0x16, 3, 1, 0x40, 1},
		{0x16, 3, 1, 0, 0},
		{0x16, 4, 0, 0, 1, 0},
		{0x17, 3, 3, 0, 1, 0},
	} {
		if got := peekClientHello(bufio.NewReaderSize(bytes.NewReader(wire), tlsInspectMax), true); got.serverName != "" || len(got.protocols) != 0 {
			t.Fatalf("accepted invalid/capped input: %+v", got)
		}
	}
}

func FuzzPeekALPN(f *testing.F) {
	valid := alpnHello("shared.example", helloExtension(16, alpnExtension("h2", "http/1.1")))
	// Positive assertion prevents a malformed seed from silently missing ALPN branches.
	if got := peekClientHello(bufio.NewReaderSize(bytes.NewReader(valid), tlsInspectMax), true); len(got.protocols) != 2 {
		f.Fatal("invalid ALPN seed")
	}
	f.Add(valid)
	f.Add(fragmentClientHelloForFuzz(valid, 2))
	f.Add(alpnHello("", helloExtension(16, alpnExtension("\x00\xff", "x,h2"))))
	f.Add(alpnHello("shared.example", helloExtension(16, []byte{0, 1, 0})))
	f.Add(alpnHello("shared.example", helloExtension(16, alpnExtension("h2")), helloExtension(16, alpnExtension("http/1.1"))))
	f.Add([]byte("not TLS"))
	f.Fuzz(func(t *testing.T, data []byte) {
		br := bufio.NewReaderSize(bytes.NewReader(data), tlsInspectMax)
		got := peekClientHello(br, true)
		if len(got.protocols) > tlsALPNMaxProtocols {
			t.Fatal("offer count exceeded")
		}
		for _, protocol := range got.protocols {
			if len(protocol) == 0 || len(protocol) > tlsALPNMaxProtocolLen {
				t.Fatal("invalid offer length")
			}
		}
		if again := peekClientHello(br, true); !reflect.DeepEqual(got, again) {
			t.Fatal("nondeterministic inspection")
		}
		out, err := io.ReadAll(br)
		if err != nil || !bytes.Equal(data, out) {
			t.Fatalf("preread changed input: %v", err)
		}
	})
}

// Protocol-level rejection cases keep the new shared inspector's existing TLS
// envelope guards covered, including a legal legacy no-extension hello.
func TestALPNClientHelloEnvelopeRejections(t *testing.T) {
	base := alpnHello("shared.example", helloExtension(16, alpnExtension("h2")))[5:]
	patchLengths := func(b []byte, extensions bool) []byte {
		n := len(b) - 4
		b[1], b[2], b[3] = byte(n>>16), byte(n>>8), byte(n)
		if extensions {
			binary.BigEndian.PutUint16(b[45:47], uint16(len(b)-47))
		}
		return b
	}
	cases := []struct {
		name string
		edit func([]byte) []byte
	}{
		{"not ClientHello", func(b []byte) []byte { b[0] = 2; return b }},
		{"incorrect handshake length", func(b []byte) []byte { b[3]++; return b }},
		{"undefined hello version", func(b []byte) []byte { b[4] = 4; return b }},
		{"oversized session identifier", func(b []byte) []byte { b[38] = 33; return b }},
		{"empty compression vector", func(b []byte) []byte { b[43] = 0; return b }},
		{"truncated compression vector", func(b []byte) []byte { b[43] = 255; return b }},
		{"legacy no extensions", func(b []byte) []byte { return patchLengths(b[:45], false) }},
		{"incorrect extension total", func(b []byte) []byte { b[46]++; return b }},
		{"oversized extension value", func(b []byte) []byte { b[49], b[50] = 255, 255; return b }},
		{"partial extension header", func(b []byte) []byte { return patchLengths(append(b, 0), true) }},
		{"duplicate SNI", func(b []byte) []byte {
			n := int(binary.BigEndian.Uint16(b[49:51]))
			return patchLengths(append(b, b[47:51+n]...), true)
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.edit(append([]byte(nil), base...))
			for _, inspectALPN := range []bool{false, true} {
				if got := parseClientHello(b, inspectALPN); got.serverName != "" || len(got.protocols) != 0 {
					t.Fatalf("invalid envelope gave partial routing: %+v", got)
				}
			}
		})
	}
	wire := appendHandshakeRecord(nil, []byte{3, 1}, base[:1])
	wire = appendHandshakeRecord(wire, []byte{3, 1}, make([]byte, tlsRecordMax))
	if got := peekClientHello(bufio.NewReaderSize(bytes.NewReader(wire), tlsInspectMax), true); got.serverName != "" || len(got.protocols) != 0 {
		t.Fatal("handshake buffer cap exceeded")
	}
}
