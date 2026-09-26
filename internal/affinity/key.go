// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package affinity

import (
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"strings"

	"jul/internal/clientaddr"
)

// Status is the bounded outcome of extracting a key from a request. It is safe
// as a metric label; the key itself never is.
type Status uint8

const (
	// StatusNone means no key was requested: the pool does not hash, or the
	// caller had nothing to extract from.
	StatusNone Status = iota
	// StatusHashed means a usable key was found and the request is placed by
	// rendezvous hashing.
	StatusHashed
	// StatusMissing means the configured header or cookie was absent or empty.
	StatusMissing
	// StatusInvalid means a value was present but unusable: longer than
	// MaxKeyBytes, repeated header lines, or a client address that is not
	// attributed to a client.
	StatusInvalid
)

// String returns the metric label for s.
func (s Status) String() string {
	switch s {
	case StatusHashed:
		return "hashed"
	case StatusMissing:
		return "missing"
	case StatusInvalid:
		return "invalid"
	default:
		return "none"
	}
}

// Key is an extracted affinity key: its 64-bit Sum and how it was obtained.
// Only the sum is kept, so the raw header or cookie value never outlives the
// extraction and cannot reach a log, a label or a trace by accident.
type Key struct {
	sum    uint64
	status Status
}

// Hashed reports whether the key places the request by rendezvous hashing.
func (k Key) Hashed() bool { return k.status == StatusHashed }

// Sum returns the key's hash. It is meaningful only when Hashed is true.
func (k Key) Sum() uint64 { return k.sum }

// Status reports how the key was obtained.
func (k Key) Status() Status { return k.status }

// KeyOf builds a hashed key from raw material. It exists for tests and tools;
// request paths go through a Source.
func KeyOf(material string) Key { return Key{sum: Sum(material), status: StatusHashed} }

// Source extracts keys of one configured kind. The zero value extracts
// nothing.
type Source struct {
	kind string
	// name is the canonical header name, or the cookie name as configured.
	name string
}

// NewSource builds a Source for a validated [upstreams.hash] key and name.
func NewSource(kind, name string) Source {
	if kind == "header" {
		name = textproto.CanonicalMIMEHeaderKey(name)
	}
	return Source{kind: kind, name: name}
}

// Kind returns the configured key kind.
func (s Source) Kind() string { return s.kind }

// Name returns the configured header or cookie name ("" for client_ip).
func (s Source) Name() string { return s.name }

// FromRequest extracts the key for an HTTP request.
//
// client_ip uses the request's canonical client identity — the same one access
// control and rate limiting use — so affinity honours trusted-proxy policy
// without a parser of its own. An identity that fell back to a proxy hop
// because the forwarding chain was unusable is invalid rather than hashed:
// hashing it would pin every client behind that proxy to one backend.
func (s Source) FromRequest(r *http.Request) Key {
	switch s.kind {
	case "client_ip":
		if id, ok := clientaddr.FromContext(r.Context()); ok && !id.Attributed() {
			return Key{status: StatusInvalid}
		}
		return addrKey(clientaddr.Client(r))
	case "header":
		return headerKey(r.Header[s.name])
	case "cookie":
		return cookieKey(r.Header["Cookie"], s.name)
	default:
		return Key{}
	}
}

// FromAddr extracts a client_ip key from a stream client address: the socket
// peer, or the PROXY-protocol source a trusted peer asserted. Other kinds do
// not apply to L4 and extract nothing, which validation already guarantees.
func (s Source) FromAddr(a net.Addr) Key {
	if s.kind != "client_ip" {
		return Key{}
	}
	var ip netip.Addr
	switch v := a.(type) {
	case *net.TCPAddr:
		ip, _ = netip.AddrFromSlice(v.IP)
	case *net.UDPAddr:
		ip, _ = netip.AddrFromSlice(v.IP)
	case nil:
	default:
		if ap, err := netip.ParseAddrPort(a.String()); err == nil {
			ip = ap.Addr()
		}
	}
	return addrKey(ip.Unmap().WithZone(""))
}

// addrKey hashes an address's canonical text, so an HTTP client_ip key and a
// header carrying the same address text land on the same backend.
func addrKey(a netip.Addr) Key {
	if !a.IsValid() {
		return Key{status: StatusInvalid}
	}
	var buf [64]byte
	return Key{sum: SumBytes(a.AppendTo(buf[:0])), status: StatusHashed}
}

// headerKey applies the header rules: exactly one field line, surrounding
// whitespace trimmed, empty treated as absent. Repeated lines are invalid
// rather than first-wins, because intermediaries disagree about which one a
// backend sees and affinity must not be steerable by duplicating a header.
func headerKey(values []string) Key {
	switch len(values) {
	case 0:
		return Key{status: StatusMissing}
	case 1:
	default:
		return Key{status: StatusInvalid}
	}
	return valueKey(strings.Trim(values[0], " \t"))
}

// cookieKey finds the first cookie named name across every Cookie header line.
// Browsers send the most specific path first, so first-wins is the value the
// application itself would read. A value wrapped in double quotes is unwrapped,
// as net/http does.
func cookieKey(lines []string, name string) Key {
	for _, line := range lines {
		for line != "" {
			var part string
			part, line, _ = strings.Cut(line, ";")
			part = strings.Trim(part, " \t")
			k, v, ok := strings.Cut(part, "=")
			if !ok || k != name {
				continue
			}
			if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
				v = v[1 : len(v)-1]
			}
			return valueKey(v)
		}
	}
	return Key{status: StatusMissing}
}

func valueKey(v string) Key {
	switch {
	case v == "":
		return Key{status: StatusMissing}
	case len(v) > MaxKeyBytes:
		return Key{status: StatusInvalid}
	}
	return Key{sum: Sum(v), status: StatusHashed}
}
