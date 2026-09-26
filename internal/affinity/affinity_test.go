// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package affinity

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/rendezvous_v1.json from the current implementation")

// goldenFile is the frozen rendezvous_v1 mapping. It is the contract: if this
// test fails, keys have moved between backends, which is a user-visible
// session reset. Fix the code, or introduce a new algorithm name.
const goldenFile = "testdata/rendezvous_v1.json"

type goldenBackend struct {
	Network string `json:"network"`
	Address string `json:"address"`
	Weight  int    `json:"weight"`
}

type goldenSet struct {
	Name     string          `json:"name"`
	Backends []goldenBackend `json:"backends"`
	// Rankings maps key material to the backend order by index.
	Rankings map[string][]int `json:"rankings"`
}

type goldenDoc struct {
	Algorithm  string            `json:"algorithm"`
	Sums       map[string]string `json:"sums"`
	Identities map[string]string `json:"identities"`
	NegLog2    map[string]string `json:"neg_log2"`
	Sets       []goldenSet       `json:"sets"`
}

var goldenKeys = []string{
	"203.0.113.7", "198.51.100.23", "2001:db8::1", "2001:db8:85a3::8a2e:370:7334",
	"tenant-a", "tenant-b", "tenant-c", "Tenant-A", "session=abc", "x",
	"0123456789abcdef0123456789abcdef", "ümlaut", "user:42", "user:43",
}

var goldenIdentityInputs = [][2]string{
	{"tcp", "10.0.0.1:8080"},
	{"tcp", "10.0.0.1:08080"},
	{"tcp", "[::ffff:10.0.0.1]:8080"},
	{"tcp", "[2001:DB8:0:0::1]:443"},
	{"tcp", "[fe80::1%eth0]:80"},
	{"tcp", "API.Example.COM.:80"},
	{"tcp", "api.example.com:80"},
	{"tcp", "no-port"},
	{"unix", "/run/app.sock"},
	{"unix", "/run//app.sock"},
}

func goldenSets() []goldenSet {
	return []goldenSet{
		{Name: "equal-4", Backends: []goldenBackend{
			{"tcp", "10.0.0.1:80", 1}, {"tcp", "10.0.0.2:80", 1}, {"tcp", "10.0.0.3:80", 1}, {"tcp", "10.0.0.4:80", 1},
		}},
		{Name: "weighted-3", Backends: []goldenBackend{
			{"tcp", "app-a.internal:9000", 1}, {"tcp", "app-b.internal:9000", 3}, {"tcp", "app-c.internal:9000", 6},
		}},
		{Name: "mixed-networks", Backends: []goldenBackend{
			{"tcp", "[2001:db8::10]:8443", 2}, {"unix", "/run/php/fpm.sock", 2}, {"tcp", "192.0.2.10:8443", 5},
		}},
	}
}

func candidatesOf(bs []goldenBackend) []Candidate {
	out := make([]Candidate, len(bs))
	for i, b := range bs {
		out[i] = NewCandidate(b.Network, b.Address, b.Weight)
	}
	return out
}

func buildGolden() goldenDoc {
	doc := goldenDoc{
		Algorithm:  Algorithm,
		Sums:       map[string]string{},
		Identities: map[string]string{},
		NegLog2:    map[string]string{},
	}
	for _, k := range goldenKeys {
		doc.Sums[k] = strconv.FormatUint(Sum(k), 16)
	}
	for _, in := range goldenIdentityInputs {
		doc.Identities[in[0]+" "+in[1]] = Identity(in[0], in[1])
	}
	for _, h := range []uint64{0, 1, 2, 3, 1 << 32, 1 << 63, math.MaxUint64, 0x9e3779b97f4a7c15} {
		doc.NegLog2[strconv.FormatUint(h, 16)] = strconv.FormatUint(negLog2(h), 16)
	}
	for _, set := range goldenSets() {
		cands := candidatesOf(set.Backends)
		set.Rankings = map[string][]int{}
		for _, k := range goldenKeys {
			set.Rankings[k] = Rank(Sum(k), cands)
		}
		doc.Sets = append(doc.Sets, set)
	}
	return doc
}

func TestRendezvousV1GoldenVectors(t *testing.T) {
	got := buildGolden()
	if *update {
		b, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(goldenFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenFile, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	var want goldenDoc
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if want.Algorithm != Algorithm {
		t.Fatalf("golden algorithm %q, implementation %q", want.Algorithm, Algorithm)
	}
	for k, v := range want.Sums {
		if g := got.Sums[k]; g != v {
			t.Errorf("Sum(%q) = %s, frozen %s", k, g, v)
		}
	}
	for k, v := range want.Identities {
		if g := got.Identities[k]; g != v {
			t.Errorf("Identity(%s) = %q, frozen %q", k, g, v)
		}
	}
	for k, v := range want.NegLog2 {
		if g := got.NegLog2[k]; g != v {
			t.Errorf("negLog2(0x%s) = %s, frozen %s", k, g, v)
		}
	}
	if len(want.Sets) != len(got.Sets) {
		t.Fatalf("golden has %d sets, implementation %d", len(want.Sets), len(got.Sets))
	}
	for i, ws := range want.Sets {
		gs := got.Sets[i]
		for k, order := range ws.Rankings {
			if fmt.Sprint(gs.Rankings[k]) != fmt.Sprint(order) {
				t.Errorf("set %s key %q: ranking %v, frozen %v", ws.Name, k, gs.Rankings[k], order)
			}
		}
	}
}

// The mapping must not depend on the order backends are listed in: reversing
// and shuffling the candidate slice must rank the same identities in the same
// order.
func TestRankIsIndependentOfInputOrder(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, set := range goldenSets() {
		cands := candidatesOf(set.Backends)
		for _, k := range goldenKeys {
			ref := identitiesInOrder(cands, Rank(Sum(k), cands))
			for trial := 0; trial < 20; trial++ {
				shuffled := append([]Candidate(nil), cands...)
				rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
				got := identitiesInOrder(shuffled, Rank(Sum(k), shuffled))
				if fmt.Sprint(got) != fmt.Sprint(ref) {
					t.Fatalf("set %s key %q: order %v after shuffle, want %v", set.Name, k, got, ref)
				}
			}
		}
	}
}

func identitiesInOrder(c []Candidate, order []int) []string {
	out := make([]string, len(order))
	for i, idx := range order {
		out[i] = c[idx].Identity
	}
	return out
}

func TestIdentityNormalization(t *testing.T) {
	cases := []struct{ network, in, want string }{
		{"tcp", "10.0.0.1:8080", "10.0.0.1:8080"},
		{"tcp", "10.0.0.1:08080", "10.0.0.1:8080"},
		{"tcp", "[::ffff:10.0.0.1]:8080", "10.0.0.1:8080"},
		{"tcp", "[2001:DB8:0:0::1]:443", "[2001:db8::1]:443"},
		{"tcp", "[fe80::1%eth0]:80", "[fe80::1%eth0]:80"},
		{"tcp", "API.Example.COM.:80", "api.example.com:80"},
		{"tcp", "api.example.com:http", "api.example.com:http"},
		{"tcp", "No-Port", "no-port"},
		{"unix", "/run/app.sock", "unix:/run/app.sock"},
	}
	for _, c := range cases {
		if got := Identity(c.network, c.in); got != c.want {
			t.Errorf("Identity(%s, %q) = %q, want %q", c.network, c.in, got, c.want)
		}
	}
}

// negLog2 must be positive (it is a divisor) and monotone non-increasing: the
// equal-weight fast path in Better is only equivalent to the weighted path
// because of it.
func TestNegLog2PositiveAndMonotone(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	prevH, prevL := uint64(0), negLog2(0)
	if prevL != 64<<negLog2Frac {
		t.Fatalf("negLog2(0) = %x, want exactly 64.0", prevL)
	}
	hs := []uint64{1, 2, 3, 4, 1 << 32, 1<<63 - 1, 1 << 63, math.MaxUint64 - 1, math.MaxUint64}
	for i := 0; i < 5000; i++ {
		hs = append(hs, rng.Uint64())
	}
	for _, h := range hs {
		if negLog2(h) == 0 {
			t.Fatalf("negLog2(%x) = 0", h)
		}
	}
	// Sorted ascending sweep, checking monotonicity between neighbours.
	sorted := append([]uint64(nil), hs...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	for _, h := range sorted {
		l := negLog2(h)
		if h >= prevH && l > prevL {
			t.Fatalf("negLog2 not monotone: negLog2(%x)=%x > negLog2(%x)=%x", h, l, prevH, prevL)
		}
		prevH, prevL = h, l
	}
	// Accuracy against the float reference, to well within the Q32 step.
	for _, h := range hs[:200] {
		want := -math.Log2(float64(h|1) / math.Exp2(64))
		got := float64(negLog2(h)) / math.Exp2(negLog2Frac)
		if math.Abs(got-want) > 1e-6 {
			t.Fatalf("negLog2(%x) = %g, reference %g", h, got, want)
		}
	}
}

// With equal weights the fast path (score order) must agree with the weighted
// comparison everywhere, including when the weight is not 1.
func TestEqualWeightFastPathMatchesWeightedOrder(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for i := 0; i < 20000; i++ {
		key := rng.Uint64()
		w := 1 + rng.IntN(1000)
		a := Candidate{Identity: "a", Sum: rng.Uint64(), Weight: w}
		b := Candidate{Identity: "b", Sum: rng.Uint64(), Weight: w}
		fast := Better(key, a, b)
		sa, sb := Score(key, a.Sum), Score(key, b.Sum)
		la, lb := negLog2(sa), negLog2(sb)
		var slow bool
		switch {
		case la != lb:
			slow = la < lb
		case sa != sb:
			slow = sa > sb
		default:
			slow = a.Identity < b.Identity
		}
		if fast != slow {
			t.Fatalf("key %x weight %d: fast %v, weighted %v", key, w, fast, slow)
		}
	}
}

func TestBetterIsATotalOrder(t *testing.T) {
	c := Candidate{Identity: "same", Sum: 7, Weight: 1}
	if Better(1, c, c) {
		t.Fatal("a candidate ranks above itself")
	}
	a := Candidate{Identity: "a", Sum: 9, Weight: 2}
	b := Candidate{Identity: "b", Sum: 9, Weight: 2}
	if Better(1, a, b) == Better(1, b, a) {
		t.Fatal("identity tie-break is not antisymmetric")
	}
	// Weights below one are treated as one, the pool's own normalization.
	z := Candidate{Identity: "z", Sum: 11, Weight: 0}
	o := Candidate{Identity: "z", Sum: 11, Weight: 1}
	for key := uint64(0); key < 100; key++ {
		other := Candidate{Identity: "o", Sum: key * 31, Weight: 3}
		if Better(key, z, other) != Better(key, o, other) {
			t.Fatalf("weight 0 and weight 1 rank differently for key %d", key)
		}
	}
}

// Distribution: equal weights spread keys evenly; weights spread them in
// proportion. Tolerances are several standard deviations wide for the sample
// size, so the test is deterministic in practice (fixed seed) and still
// catches a biased mapping.
func TestDistributionFollowsWeights(t *testing.T) {
	const keys = 200000
	for _, tc := range []struct {
		name    string
		weights []int
	}{
		{"equal-8", []int{1, 1, 1, 1, 1, 1, 1, 1}},
		{"weighted-1-2-3-4", []int{1, 2, 3, 4}},
		{"weighted-1-9", []int{1, 9}},
		{"weighted-large", []int{100, 300, 600}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cands := make([]Candidate, len(tc.weights))
			total := 0
			for i, w := range tc.weights {
				cands[i] = NewCandidate("tcp", fmt.Sprintf("10.1.0.%d:80", i+1), w)
				total += w
			}
			counts := make([]int, len(cands))
			for k := 0; k < keys; k++ {
				counts[best(Sum("key-"+strconv.Itoa(k)), cands)]++
			}
			for i, w := range tc.weights {
				want := float64(keys) * float64(w) / float64(total)
				if dev := math.Abs(float64(counts[i])-want) / want; dev > 0.03 {
					t.Errorf("backend %d weight %d: %d keys, want ~%.0f (%.1f%% off)", i, w, counts[i], want, dev*100)
				}
			}
		})
	}
}

func best(key uint64, c []Candidate) int {
	b := 0
	for i := 1; i < len(c); i++ {
		if Better(key, c[i], c[b]) {
			b = i
		}
	}
	return b
}

// Minimal disruption, measured: adding a backend moves only keys onto it
// (about 1/(N+1) of them); removing one moves only its own keys; a weight
// change moves keys only to or from the changed backend.
func TestMembershipChangesRemapMinimally(t *testing.T) {
	const keys = 100000
	mk := func(n int) []Candidate {
		c := make([]Candidate, n)
		for i := range c {
			c[i] = NewCandidate("tcp", fmt.Sprintf("10.2.0.%d:80", i+1), 1)
		}
		return c
	}
	assign := func(c []Candidate) []string {
		out := make([]string, keys)
		for k := range out {
			out[k] = c[best(Sum("k"+strconv.Itoa(k)), c)].Identity
		}
		return out
	}
	for _, n := range []int{2, 8, 32} {
		before := assign(mk(n))
		// Add one.
		grown := assign(mk(n + 1))
		added := mk(n + 1)[n].Identity
		moved := 0
		for k := range before {
			if before[k] != grown[k] {
				moved++
				if grown[k] != added {
					t.Fatalf("n=%d add: key moved between surviving backends %s -> %s", n, before[k], grown[k])
				}
			}
		}
		want := float64(keys) / float64(n+1)
		if math.Abs(float64(moved)-want)/want > 0.05 {
			t.Errorf("n=%d add: moved %d keys, want ~%.0f", n, moved, want)
		}
		t.Logf("n=%d add one: %.2f%% keys remapped (ideal %.2f%%)", n, 100*float64(moved)/keys, 100/float64(n+1))

		// Remove the first.
		shrunk := assign(mk(n)[1:])
		removed := mk(n)[0].Identity
		moved = 0
		for k := range before {
			if before[k] != shrunk[k] {
				moved++
				if before[k] != removed {
					t.Fatalf("n=%d remove: key on a surviving backend moved %s -> %s", n, before[k], shrunk[k])
				}
			}
		}
		t.Logf("n=%d remove one: %.2f%% keys remapped (ideal %.2f%%)", n, 100*float64(moved)/keys, 100/float64(n))

		// Double one backend's weight.
		heavier := mk(n)
		heavier[0].Weight = 2
		reweighted := assign(heavier)
		moved = 0
		for k := range before {
			if before[k] != reweighted[k] {
				moved++
				if reweighted[k] != heavier[0].Identity {
					t.Fatalf("n=%d weight up: key moved to an unchanged backend %s", n, reweighted[k])
				}
			}
		}
		idealUp := 2/float64(n+1) - 1/float64(n)
		t.Logf("n=%d weight 1->2 on one: %.2f%% keys remapped (ideal %.2f%%)", n, 100*float64(moved)/keys, 100*idealUp)
		if math.Abs(float64(moved)/keys-idealUp)/idealUp > 0.08 {
			t.Errorf("n=%d weight up: moved %.2f%%, want ~%.2f%%", n, 100*float64(moved)/keys, 100*idealUp)
		}
	}
}

func TestRankPutsBestFirstAndIsAPermutation(t *testing.T) {
	cands := candidatesOf(goldenSets()[1].Backends)
	for _, k := range goldenKeys {
		order := Rank(Sum(k), cands)
		if order[0] != best(Sum(k), cands) {
			t.Fatalf("key %q: Rank[0]=%d, best=%d", k, order[0], best(Sum(k), cands))
		}
		seen := map[int]bool{}
		for _, i := range order {
			seen[i] = true
		}
		if len(seen) != len(cands) {
			t.Fatalf("key %q: Rank is not a permutation: %v", k, order)
		}
	}
	if len(Rank(1, nil)) != 0 {
		t.Fatal("Rank of nothing is not empty")
	}
}

func TestSumBytesMatchesSum(t *testing.T) {
	for _, k := range goldenKeys {
		if Sum(k) != SumBytes([]byte(k)) {
			t.Fatalf("Sum and SumBytes disagree for %q", k)
		}
	}
}
