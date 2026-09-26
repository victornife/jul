// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package affinity is the rendezvous_v1 key-to-backend mapping behind
// strategy = "consistent_hash" (ADR 0021).
//
// Everything that decides where a key lands lives here, and nothing here
// depends on process state: no seeded hash, no map iteration, no pointer
// identity, no floating point and no slice position. The same key and the same
// set of backend identities and weights rank identically across restarts,
// reloads, reordered configuration and platforms. The golden vectors in
// testdata/rendezvous_v1.json freeze that mapping; changing any function in
// this file changes which backend holds a user's session, so a different
// mapping must be a new algorithm name, never an edit to this one.
package affinity

import (
	"math/bits"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// Algorithm is the only mapping this package implements.
const Algorithm = "rendezvous_v1"

// MaxKeyBytes bounds a header or cookie key. A longer value is treated as
// invalid rather than truncated: truncation would silently collide every key
// sharing a long prefix, such as tokens with a common header.
const MaxKeyBytes = 256

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// Sum hashes key material or a backend identity to 64 bits: FNV-1a over the
// bytes, finalized with the MurmurHash3 fmix64 avalanche. FNV-1a alone mixes
// short inputs such as IPv4 text poorly; fmix64 is a bijection, so it adds
// dispersion without adding collisions.
func Sum(s string) uint64 {
	h := uint64(fnvOffset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return fmix64(h)
}

// SumBytes is Sum over a byte slice, for callers that build key material in a
// stack buffer.
func SumBytes(b []byte) uint64 {
	h := uint64(fnvOffset64)
	for _, c := range b {
		h ^= uint64(c)
		h *= fnvPrime64
	}
	return fmix64(h)
}

// Score is the rendezvous weight of one backend for one key. Both inputs are
// already Sum outputs, so the XOR keeps every backend's score distinct for a
// given key and fmix64 decorrelates scores across backends.
func Score(key, backend uint64) uint64 { return fmix64(key ^ backend) }

func fmix64(h uint64) uint64 {
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

// Candidate is one backend as the ranking sees it.
type Candidate struct {
	// Identity is the canonical backend identity (see Identity). It breaks an
	// exact score tie, which is what keeps the order total without falling back
	// to slice position.
	Identity string
	// Sum is Sum(Identity), precomputed because a backend is ranked for every
	// keyed request and its identity does not change.
	Sum uint64
	// Weight is the backend's weight, at least 1.
	Weight int
}

// Better reports whether a ranks above b for key. It is the whole ordering:
// the selected backend is the eligible candidate no other candidate is Better
// than, and a retry takes the best of what remains.
//
// Weighted rendezvous (Schindelhauer and Schomaker's logarithmic method) ranks
// by weight / -ln(u), where u is the score mapped to (0,1). That puts a key on
// a backend with probability exactly weight / total weight, and a weight change
// or a departure moves only the keys that must move. The comparison is made in
// integers — w_a * L_b against w_b * L_a with L a fixed-point -log2(u) — so it
// is bit-for-bit reproducible where math.Log, FMA contraction and assembly
// implementations are not.
//
// Equal weights skip the logarithm. L is monotonically non-increasing in the
// score and ties fall back to the raw score, so ranking by score alone is the
// same order the weighted comparison produces, not an approximation of it.
func Better(key uint64, a, b Candidate) bool {
	sa, sb := Score(key, a.Sum), Score(key, b.Sum)
	if a.Weight != b.Weight {
		la, lb := negLog2(sa), negLog2(sb)
		// a wins when w_a/L_a > w_b/L_b, i.e. w_a*L_b > w_b*L_a, compared in
		// 128 bits because a weight is an unbounded positive int.
		ahi, alo := bits.Mul64(uint64(clampWeight(a.Weight)), lb)
		bhi, blo := bits.Mul64(uint64(clampWeight(b.Weight)), la)
		if ahi != bhi {
			return ahi > bhi
		}
		if alo != blo {
			return alo > blo
		}
	}
	if sa != sb {
		return sa > sb
	}
	return a.Identity < b.Identity
}

func clampWeight(w int) int {
	if w < 1 {
		return 1
	}
	return w
}

// negLog2Frac is the number of fractional bits in negLog2's fixed point.
const negLog2Frac = 32

// log2TableBits sizes the interpolation table: 2^10 segments keep the linear
// interpolation error of log2 below 2^-22, far finer than anything a weight
// ratio can distinguish, in a 8 KiB table.
const log2TableBits = 10

// log2Table[i] is log2(1 + i/2^log2TableBits) with negLog2Frac fractional
// bits. It is computed at start-up by the exact integer algorithm below, so it
// is identical on every platform; there are no floating-point literals to
// round differently.
var log2Table = func() (t [1<<log2TableBits + 1]uint64) {
	for i := range t {
		t[i] = log2Exact(uint64(1<<log2TableBits+i)) - log2TableBits<<negLog2Frac
	}
	return t
}()

// log2Exact returns log2(x) for x >= 1 with negLog2Frac fractional bits by
// repeated squaring of the normalized mantissa, the textbook bit-by-bit binary
// logarithm. Each step is an integer multiply and shift.
func log2Exact(x uint64) uint64 {
	msb := uint64(63 - bits.LeadingZeros64(x))
	// m is the mantissa in Q2.62: value m / 2^62 in [1, 2).
	m := x << (63 - msb) >> 1
	var frac uint64
	for i := 0; i < negLog2Frac; i++ {
		hi, lo := bits.Mul64(m, m)
		m = hi<<2 | lo>>62 // m*m / 2^62, in [1, 4)
		frac <<= 1
		if m >= 1<<63 { // square is at least 2
			frac |= 1
			m >>= 1
		}
	}
	return msb<<negLog2Frac | frac
}

// negLog2 returns -log2(u) for u = (h|1) / 2^64 as an unsigned fixed-point
// number with negLog2Frac fractional bits. It is never zero, so it can be a
// divisor in the comparison it feeds.
//
// The integer part is the position of the leading one; the fraction is a
// linear interpolation in log2Table on the next log2TableBits mantissa bits.
// Interpolating a monotone table with non-negative slopes is monotone, which
// Better's equal-weight fast path relies on.
func negLog2(h uint64) uint64 {
	x := h | 1
	lz := uint64(bits.LeadingZeros64(x))
	m := x << lz // leading one at bit 63
	i := (m >> (63 - log2TableBits)) & (1<<log2TableBits - 1)
	r := (m << (log2TableBits + 1)) >> (64 - negLog2Frac)
	lo, hi := log2Table[i], log2Table[i+1]
	frac := lo + ((hi-lo)*r)>>negLog2Frac
	return 64<<negLog2Frac - ((63-lz)<<negLog2Frac + frac)
}

// Identity returns the canonical identity rendezvous_v1 hashes for a backend.
//
// It names where a backend is reached, not which workload answers there, and
// it deliberately excludes the scheme (fixed per pool) and any provider logical
// ID: a Kubernetes pod replaced at the same address keeps its keys, and a
// backend whose address really changes is a new identity, so only its own share
// of keys moves.
//
//   - TCP "host:port": an IP literal is reduced to its canonical text
//     (IPv4-mapped IPv6 unmapped, IPv6 compressed per RFC 5952, zone kept
//     because it selects a different interface); a hostname is lowercased with
//     any trailing dot removed; the port is written as a plain decimal.
//   - Unix "unix:<path>": the configured path byte-for-byte. Paths are
//     platform-specific already, so no lexical cleaning is attempted.
//
// An address that does not split into host and port is lowercased as a whole,
// which is still stable, just not further normalized.
func Identity(network, address string) string {
	if network == "unix" {
		return "unix:" + address
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return strings.ToLower(address)
	}
	if p, perr := strconv.ParseUint(port, 10, 16); perr == nil {
		port = strconv.FormatUint(p, 10)
	}
	if a, aerr := netip.ParseAddr(host); aerr == nil {
		zone := a.Zone()
		a = a.Unmap()
		if zone != "" && a.Is6() {
			a = a.WithZone(zone)
		}
		return net.JoinHostPort(a.String(), port)
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return net.JoinHostPort(host, port)
}

// NewCandidate builds a Candidate from a backend's network, address and weight.
func NewCandidate(network, address string, weight int) Candidate {
	id := Identity(network, address)
	return Candidate{Identity: id, Sum: Sum(id), Weight: clampWeight(weight)}
}

// Rank returns the indices of candidates in rendezvous order for key: the
// preferred backend first, then each fallback in turn. It allocates and is
// meant for tools, tests and diagnostics; request selection uses Better
// directly over the eligible set.
func Rank(key uint64, candidates []Candidate) []int {
	order := make([]int, len(candidates))
	for i := range order {
		order[i] = i
	}
	// Insertion sort: candidate lists here are small and the comparison is the
	// contract, so a stable, obviously-correct sort is preferable to sort.Slice
	// with a closure over a non-strict comparator.
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && Better(key, candidates[order[j]], candidates[order[j-1]]); j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	return order
}
