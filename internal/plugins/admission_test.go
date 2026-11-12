// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bytes"
	"errors"
	"io"
	"math"
	"testing"
	"testing/iotest"

	"jul/internal/inspection"
)

// meter is a byte budget shared by its charges, recording live and peak
// usage and every size request, so tests can assert when bytes were held.
type meter struct {
	budget, live, peak uint64
	requests           []uint64
	refused            int
}

var errMeterFull = &inspection.AdmissionError{Outcome: inspection.ByteSaturated}

func (m *meter) charge(n uint64) (byteCharge, error) {
	c := &meterCharge{m: m}
	if err := c.Resize(n); err != nil {
		return nil, err
	}
	return c, nil
}

type meterCharge struct {
	m        *meter
	n        uint64
	released bool
}

func (c *meterCharge) Resize(n uint64) error {
	if c.released {
		return &inspection.AdmissionError{Outcome: inspection.LeaseClosed}
	}
	c.m.requests = append(c.m.requests, n)
	if n > c.n && c.m.live-c.n+n > c.m.budget {
		c.m.refused++
		return errMeterFull
	}
	c.m.live = c.m.live - c.n + n
	c.m.peak = max(c.m.peak, c.m.live)
	c.n = n
	return nil
}

func (c *meterCharge) Release() error {
	if !c.released {
		c.m.live -= c.n
		c.n, c.released = 0, true
	}
	return nil
}

// TestChargedBufGrowHoldsBothArrays: growth charges old+new before the new
// array exists, settles on the surviving capacity, and a refused growth
// leaves the buffer, its contents and its charge unchanged.
func TestChargedBufGrowHoldsBothArrays(t *testing.T) {
	m := &meter{budget: 1 << 20}
	cb, err := newChargedBuf(m.charge, 8)
	if err != nil {
		t.Fatal(err)
	}
	cb.b = append(cb.b, "abcdefgh"...)
	if err := cb.grow(32); err != nil {
		t.Fatal(err)
	}
	if want := []uint64{8, 40, 32}; !slicesEqual(m.requests, want) {
		t.Fatalf("charge requests %v, want %v (old+new before the copy)", m.requests, want)
	}
	if m.peak != 40 || m.live != 32 || cap(cb.b) != 32 || string(cb.b) != "abcdefgh" {
		t.Fatalf("peak=%d live=%d cap=%d %q", m.peak, m.live, cap(cb.b), cb.b)
	}
	// Shortening the slice refunds nothing: capacity stays charged.
	cb.b = cb.b[:1]
	if m.live != 32 {
		t.Fatalf("shortening refunded: live=%d", m.live)
	}

	// Refused growth: nothing is allocated and nothing changes.
	m.budget = 32 + 63
	before := cb.b
	if err := cb.grow(64); !inspection.IsOverload(err) {
		t.Fatalf("grow err = %v, want overload", err)
	}
	if &cb.b[:1][0] != &before[:1][0] || cap(cb.b) != 32 || m.live != 32 {
		t.Fatalf("failed growth changed the buffer: cap=%d live=%d", cap(cb.b), m.live)
	}
	cb.release()
	cb.release()
	if m.live != 0 || cb.b != nil {
		t.Fatalf("release: live=%d", m.live)
	}
}

func TestChargedBufOverflowIsRefused(t *testing.T) {
	cb := &chargedBuf{b: make([]byte, 0, 4), c: &fakeCharge{n: 4}}
	if _, ok := addCap(-1, 1); ok {
		t.Fatal("negative capacity accepted")
	}
	if _, err := newChargedBuf(freeCharge, -1); !errors.Is(err, errChargeOverflow) {
		t.Fatalf("negative capacity: %v", err)
	}
	if err := cb.grow(2); err != nil || cap(cb.b) != 4 {
		t.Fatalf("shrinking grow: %v cap=%d", err, cap(cb.b))
	}
	if _, err := readBounded(bytes.NewReader(nil), -1, math.MaxInt, freeCharge); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("limit MaxInt: %v", err)
	}
	if _, err := readBounded(bytes.NewReader(nil), -1, -1, freeCharge); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("negative limit: %v", err)
	}
}

func slicesEqual(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNextBodyCap(t *testing.T) {
	cases := []struct{ c, want, ceiling, n int }{
		{512, 4097, 4097, 1024},
		{4096, 4097, 4097, 4097},
		{2048, 3000, 8193, 3000}, // stops once at the declared length
		{3000, 3000, 8193, 6000}, // dishonest: keep doubling
		{5000, 8193, 8193, 8193}, // beyond half: jump to the ceiling
		{8193, 8193, 8193, 8193}, // never past ceiling
		{math.MaxInt / 2, math.MaxInt, math.MaxInt, math.MaxInt - 1}, // no overflow
	}
	for _, c := range cases {
		if got := nextBodyCap(c.c, c.want, c.ceiling); got != c.n {
			t.Errorf("nextBodyCap(%d,%d,%d) = %d, want %d", c.c, c.want, c.ceiling, got, c.n)
		}
	}
}

// TestReadBoundedRefusedAtEveryStep runs the same unknown-length upload
// against every budget from zero to enough: each run either succeeds with
// exactly its capacity charged, or fails as overload with everything
// refunded. No run returns a partial body as complete.
func TestReadBoundedRefusedAtEveryStep(t *testing.T) {
	const limit = 5000
	data := bytes.Repeat([]byte("q"), 3000)
	var succeeded, refused int
	for budget := uint64(0); budget <= 2*(limit+1); budget += 7 {
		m := &meter{budget: budget}
		cb, err := readBounded(iotest.OneByteReader(bytes.NewReader(data)), -1, limit, m.charge)
		switch {
		case err == nil:
			succeeded++
			if !bytes.Equal(cb.b, data) || m.live != uint64(cap(cb.b)) {
				t.Fatalf("budget %d: %d bytes, live=%d cap=%d", budget, len(cb.b), m.live, cap(cb.b))
			}
			cb.release()
		case inspection.IsOverload(err):
			refused++
			if cb != nil {
				t.Fatalf("budget %d: buffer returned with error", budget)
			}
		default:
			t.Fatalf("budget %d: %v", budget, err)
		}
		if m.live != 0 || m.peak > budget {
			t.Fatalf("budget %d: live=%d peak=%d", budget, m.live, m.peak)
		}
	}
	if succeeded == 0 || refused == 0 {
		t.Fatalf("succeeded=%d refused=%d: sweep did not cover both outcomes", succeeded, refused)
	}
}

// TestReadBoundedDeclaredLengths: an honest length allocates once (capped
// by initialBodyBuffer), a declared oversize body is refused before any
// read or charge, and a dishonest short declaration still stops at limit+1.
func TestReadBoundedDeclaredLengths(t *testing.T) {
	m := &meter{budget: 1 << 20}
	cb, err := readBounded(bytes.NewReader(make([]byte, 100)), 100, 1000, m.charge)
	if err != nil || len(cb.b) != 100 || cap(cb.b) != 101 || !slicesEqual(m.requests, []uint64{101}) {
		t.Fatalf("honest: err=%v len=%d cap=%d requests=%v", err, len(cb.b), cap(cb.b), m.requests)
	}
	cb.release()

	m = &meter{budget: 1 << 20}
	body := &countingBody{r: bytes.NewReader(make([]byte, 2000))}
	if _, err := readBounded(body, 2000, 1000, m.charge); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("declared oversize: %v", err)
	}
	if body.n.Load() > 1001 || m.live != 0 {
		t.Fatalf("declared oversize read %d bytes, live=%d", body.n.Load(), m.live)
	}

	m = &meter{budget: 1 << 20}
	body = &countingBody{r: bytes.NewReader(make([]byte, 5000))}
	if _, err := readBounded(body, 10, 1000, m.charge); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("dishonest: %v", err)
	}
	if body.n.Load() > 1001 || m.peak > 2*1001 || m.live != 0 {
		t.Fatalf("dishonest read %d bytes, peak=%d live=%d", body.n.Load(), m.peak, m.live)
	}
}

// FuzzReadBoundedCharged: arbitrary chunking, read errors, limits and
// budgets never produce a false-complete or over-limit body, negative or
// leaked usage, or usage beyond the budget.
func FuzzReadBoundedCharged(f *testing.F) {
	f.Add([]byte("hello"), uint8(1), uint16(10), uint16(100), int16(-1), int16(-1))
	f.Add(bytes.Repeat([]byte("x"), 3000), uint8(7), uint16(2999), uint16(9000), int16(-1), int16(3000))
	f.Add(bytes.Repeat([]byte("y"), 600), uint8(255), uint16(1000), uint16(700), int16(300), int16(-1))
	f.Fuzz(func(t *testing.T, data []byte, chunk uint8, limit, budget uint16, errAt, declared int16) {
		if len(data) > 1<<14 {
			data = data[:1<<14]
		}
		m := &meter{budget: uint64(budget)}
		var r io.Reader = &chunkReader{data: data, chunk: max(1, int(chunk)), errAt: int(errAt)}
		cb, err := readBounded(r, int64(declared), int(limit), m.charge)
		if m.peak > m.budget {
			t.Fatalf("peak %d over budget %d", m.peak, m.budget)
		}
		if err != nil {
			if cb != nil || m.live != 0 {
				t.Fatalf("error %v left buffer=%v live=%d", err, cb != nil, m.live)
			}
			return
		}
		complete := errAt < 0 || int(errAt) >= len(data)
		if !complete || len(cb.b) > int(limit) || !bytes.Equal(cb.b, data) {
			t.Fatalf("accepted %d bytes of %d (limit %d, errAt %d)", len(cb.b), len(data), limit, errAt)
		}
		if m.live != uint64(cap(cb.b)) {
			t.Fatalf("live %d != cap %d", m.live, cap(cb.b))
		}
		cb.release()
		if m.live != 0 {
			t.Fatalf("leak %d", m.live)
		}
	})
}

// chunkReader returns data in chunks, failing once errAt bytes are read.
type chunkReader struct {
	data  []byte
	off   int
	chunk int
	errAt int
}

var errChunk = errors.New("read failed")

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.errAt >= 0 && r.off >= r.errAt && r.errAt < len(r.data) {
		return 0, errChunk
	}
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := min(len(p), r.chunk, len(r.data)-r.off)
	if r.errAt >= 0 && r.errAt < len(r.data) {
		n = min(n, r.errAt-r.off)
	}
	copy(p, r.data[r.off:r.off+n])
	r.off += n
	return n, nil
}
