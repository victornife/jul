// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func ccHeader(values ...string) http.Header {
	h := http.Header{}
	for _, v := range values {
		h.Add("Cache-Control", v)
	}
	return h
}

// TestParseCacheControlDirectiveMatrix pins the parser against the whole shape
// of Cache-Control it can receive: case, whitespace, duplicates, quoting,
// multiple field lines, and every way a delta-seconds argument can be unusable.
func TestParseCacheControlDirectiveMatrix(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		flags  []string
		absent []string
		secs   map[string]time.Duration
	}{
		{
			name:   "simple",
			header: ccHeader("public, max-age=60"),
			flags:  []string{"public", "max-age"},
			absent: []string{"private", "no-store"},
			secs:   map[string]time.Duration{"max-age": 60 * time.Second},
		},
		{
			name:   "token case is insensitive",
			header: ccHeader("PUBLIC, Max-Age=60, No-Cache"),
			flags:  []string{"public", "max-age", "no-cache"},
			secs:   map[string]time.Duration{"max-age": 60 * time.Second},
		},
		{
			name:   "surrounding whitespace",
			header: ccHeader("   public ,\t max-age = 60  "),
			flags:  []string{"public", "max-age"},
			secs:   map[string]time.Duration{"max-age": 60 * time.Second},
		},
		{
			name:   "multiple field lines merge",
			header: ccHeader("public", "max-age=60", "must-revalidate"),
			flags:  []string{"public", "max-age", "must-revalidate"},
			secs:   map[string]time.Duration{"max-age": 60 * time.Second},
		},
		{
			name:   "duplicate delta takes the smallest",
			header: ccHeader("max-age=600, max-age=60"),
			flags:  []string{"max-age"},
			secs:   map[string]time.Duration{"max-age": 60 * time.Second},
		},
		{
			name:   "duplicate across field lines takes the smallest",
			header: ccHeader("max-age=60", "max-age=5"),
			secs:   map[string]time.Duration{"max-age": 5 * time.Second},
		},
		{
			name:   "quoted delta value",
			header: ccHeader(`max-age="60"`),
			secs:   map[string]time.Duration{"max-age": 60 * time.Second},
		},
		{
			name:   "field-qualified no-cache with an embedded comma stays one directive",
			header: ccHeader(`no-cache="Set-Cookie, X-Token", max-age=60`),
			flags:  []string{"no-cache", "max-age"},
			absent: []string{"x-token"},
			secs:   map[string]time.Duration{"max-age": 60 * time.Second},
		},
		{
			name:   "malformed delta resolves to zero, not absent",
			header: ccHeader("max-age=abc"),
			flags:  []string{"max-age"},
			secs:   map[string]time.Duration{"max-age": 0},
		},
		{
			name:   "negative delta resolves to zero",
			header: ccHeader("max-age=-5"),
			secs:   map[string]time.Duration{"max-age": 0},
		},
		{
			name:   "empty delta resolves to zero",
			header: ccHeader("max-age="),
			secs:   map[string]time.Duration{"max-age": 0},
		},
		{
			name:   "overflowing delta clamps upward",
			header: ccHeader("max-age=99999999999999999999999"),
			secs:   map[string]time.Duration{"max-age": maxDeltaSeconds * time.Second},
		},
		{
			name:   "in-range but oversized delta clamps upward",
			header: ccHeader("max-age=999999999999"),
			secs:   map[string]time.Duration{"max-age": maxDeltaSeconds * time.Second},
		},
		{
			name:   "negative overflow resolves to zero",
			header: ccHeader("max-age=-99999999999999999999999"),
			secs:   map[string]time.Duration{"max-age": 0},
		},
		{
			name:   "unknown extension is recorded but harmless",
			header: ccHeader("immutable, surrogate-control=nonsense, max-age=60"),
			flags:  []string{"immutable", "surrogate-control", "max-age"},
			secs:   map[string]time.Duration{"max-age": 60 * time.Second},
		},
		{
			name:   "empty and stray commas are skipped",
			header: ccHeader(",, public ,,, max-age=60 ,"),
			flags:  []string{"public", "max-age"},
			absent: []string{""},
		},
		{
			name:   "no header at all",
			header: http.Header{},
			absent: []string{"public", "max-age", "no-store"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := parseCacheControl(tc.header, "Cache-Control")
			for _, name := range tc.flags {
				if !cc.has(name) {
					t.Errorf("directive %q missing", name)
				}
			}
			for _, name := range tc.absent {
				if cc.has(name) {
					t.Errorf("directive %q should be absent", name)
				}
			}
			for name, want := range tc.secs {
				got, ok := cc.delta(name)
				if !ok {
					t.Errorf("delta %q missing", name)
					continue
				}
				if got != want {
					t.Errorf("delta %q = %v, want %v", name, got, want)
				}
			}
		})
	}
}

// TestParseCacheControlIsTotal proves the parser terminates and produces a value
// for adversarial input rather than panicking or looping. It complements, and
// does not replace, the explicit matrix above.
func TestParseCacheControlIsTotal(t *testing.T) {
	inputs := []string{
		`"`, `""`, `=`, `=,=`, `a="`, `a="\`, `a="\"`, `\`,
		strings.Repeat("a,", 1000),
		strings.Repeat(`x="y,`, 500),
		"max-age=" + strings.Repeat("9", 400),
		"\x00\x01\x02", "max-age=6 0", "max-age=+60", "max-age=0x10",
	}
	for _, in := range inputs {
		cc := parseCacheControl(ccHeader(in), "Cache-Control")
		if cc.flags == nil || cc.secs == nil {
			t.Fatalf("parse(%q) produced an unusable value", in)
		}
		for name, d := range cc.secs {
			if d < 0 {
				t.Errorf("parse(%q): delta %q = %v, must never be negative", in, name, d)
			}
		}
	}
}

func TestRequestPolicyMatrix(t *testing.T) {
	cases := []struct {
		name     string
		cc       []string
		pragma   string
		noStore  bool
		validate bool
	}{
		{name: "no directives"},
		{name: "no-store", cc: []string{"no-store"}, noStore: true},
		{name: "no-cache", cc: []string{"no-cache"}, validate: true},
		{name: "max-age=0", cc: []string{"max-age=0"}, validate: true},
		{name: "max-age=60 does not force validation", cc: []string{"max-age=60"}},
		{name: "malformed max-age is treated as zero", cc: []string{"max-age=abc"}, validate: true},
		{name: "negative max-age is treated as zero", cc: []string{"max-age=-1"}, validate: true},
		{name: "mixed case", cc: []string{"No-Cache"}, validate: true},
		{name: "duplicate no-store", cc: []string{"no-store, no-store"}, noStore: true},
		{name: "both no-store and no-cache", cc: []string{"no-store, no-cache"}, noStore: true, validate: true},
		{name: "multiple field lines", cc: []string{"no-cache", "no-store"}, noStore: true, validate: true},
		{name: "pragma applies without Cache-Control", pragma: "no-cache", validate: true},
		{
			// RFC 9111 §5.4: Pragma is the HTTP/1.0 spelling and is ignored the
			// moment the request speaks the modern header, even to say
			// something unrelated.
			name: "pragma is ignored when Cache-Control is present", cc: []string{"max-age=60"}, pragma: "no-cache",
		},
		{name: "unsupported min-fresh is not honored", cc: []string{"min-fresh=30"}},
		{name: "unsupported max-stale is not honored", cc: []string{"max-stale=30"}},
		{name: "unsupported only-if-cached is not honored", cc: []string{"only-if-cached"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://x/", nil)
			for _, v := range tc.cc {
				r.Header.Add("Cache-Control", v)
			}
			if tc.pragma != "" {
				r.Header.Set("Pragma", tc.pragma)
			}
			p := parseRequestPolicy(r)
			if p.NoStore != tc.noStore {
				t.Errorf("NoStore = %v, want %v", p.NoStore, tc.noStore)
			}
			if p.MustValidate != tc.validate {
				t.Errorf("MustValidate = %v, want %v", p.MustValidate, tc.validate)
			}
		})
	}
}

func TestResponsePolicyMatrix(t *testing.T) {
	cases := []struct {
		name  string
		cc    []string
		check func(t *testing.T, p responsePolicy)
	}{
		{
			name: "no-store",
			cc:   []string{"no-store"},
			check: func(t *testing.T, p responsePolicy) {
				if !p.NoStore {
					t.Error("NoStore not set")
				}
			},
		},
		{
			name: "private",
			cc:   []string{"private"},
			check: func(t *testing.T, p responsePolicy) {
				if !p.Private || p.sharedAuthReuse() || p.sharedAuthStorable() {
					t.Errorf("private must not permit shared reuse or storage: %+v", p)
				}
			},
		},
		{
			name: "field-qualified private is still private",
			cc:   []string{`private="Set-Cookie"`},
			check: func(t *testing.T, p responsePolicy) {
				if !p.Private {
					t.Error("field-qualified private must be treated as private")
				}
			},
		},
		{
			name: "public permits shared reuse and storage for authenticated requests",
			cc:   []string{"public, max-age=60"},
			check: func(t *testing.T, p responsePolicy) {
				if !p.sharedAuthReuse() || !p.sharedAuthStorable() {
					t.Errorf("public must permit both: %+v", p)
				}
			},
		},
		{
			name: "s-maxage permits shared reuse and storage",
			cc:   []string{"s-maxage=60"},
			check: func(t *testing.T, p responsePolicy) {
				if p.SMaxAge != 60*time.Second || !p.HasSMaxAge {
					t.Errorf("s-maxage = %v/%v", p.SMaxAge, p.HasSMaxAge)
				}
				if !p.sharedAuthReuse() || !p.sharedAuthStorable() {
					t.Errorf("s-maxage must permit both: %+v", p)
				}
			},
		},
		{
			name: "must-revalidate permits authenticated reuse but not authenticated storage",
			cc:   []string{"must-revalidate"},
			check: func(t *testing.T, p responsePolicy) {
				if !p.revalidationRequired() {
					t.Error("revalidationRequired not set")
				}
				if !p.sharedAuthReuse() {
					t.Error("RFC 9111 §3.5 lists must-revalidate as a reuse permission")
				}
				if p.sharedAuthStorable() {
					t.Error("must-revalidate must not authorize publishing an authenticated response")
				}
			},
		},
		{
			name: "proxy-revalidate binds shared caches",
			cc:   []string{"proxy-revalidate"},
			check: func(t *testing.T, p responsePolicy) {
				if !p.ProxyRevalidate || !p.revalidationRequired() {
					t.Errorf("proxy-revalidate not honored: %+v", p)
				}
			},
		},
		{
			name: "bare response permits neither reuse nor storage for authenticated requests",
			cc:   nil,
			check: func(t *testing.T, p responsePolicy) {
				if p.sharedAuthReuse() || p.sharedAuthStorable() {
					t.Errorf("a response with no directives must not be shared with an authenticated request: %+v", p)
				}
			},
		},
		{
			name: "no-cache",
			cc:   []string{"no-cache"},
			check: func(t *testing.T, p responsePolicy) {
				if !p.NoCache {
					t.Error("NoCache not set")
				}
			},
		},
		{
			name: "field-qualified no-cache is whole-representation validation",
			cc:   []string{`no-cache="X-Private"`},
			check: func(t *testing.T, p responsePolicy) {
				if !p.NoCache {
					t.Error("a field-qualified no-cache must require full validation")
				}
			},
		},
		{
			name: "stale-while-revalidate and stale-if-error",
			cc:   []string{"max-age=10, stale-while-revalidate=20, stale-if-error=30"},
			check: func(t *testing.T, p responsePolicy) {
				if !p.HasSWR || p.SWR != 20*time.Second {
					t.Errorf("swr = %v/%v", p.SWR, p.HasSWR)
				}
				if !p.HasSIE || p.SIE != 30*time.Second {
					t.Errorf("sie = %v/%v", p.SIE, p.HasSIE)
				}
			},
		},
		{
			name: "explicit zero stale-if-error is distinguishable from absent",
			cc:   []string{"stale-if-error=0"},
			check: func(t *testing.T, p responsePolicy) {
				if !p.HasSIE || p.SIE != 0 {
					t.Errorf("an explicit zero must be recorded as present: %+v", p)
				}
			},
		},
		{
			name: "contradictory public and private both record, private wins on storage",
			cc:   []string{"public, private"},
			check: func(t *testing.T, p responsePolicy) {
				if !p.Public || !p.Private {
					t.Errorf("both directives should parse: %+v", p)
				}
			},
		},
		{
			name: "contradictory no-store and max-age",
			cc:   []string{"no-store, max-age=600"},
			check: func(t *testing.T, p responsePolicy) {
				if !p.NoStore {
					t.Error("no-store must survive a contradictory max-age")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := ccHeader(tc.cc...)
			tc.check(t, parseResponsePolicy(h))
		})
	}
}

// TestFreshnessPrecedence pins the TTL precedence chain and every way it can be
// short-circuited, using a fixed clock so no assertion depends on wall time.
func TestFreshnessPrecedence(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	date := now.Format(http.TimeFormat)
	c := &Cache{defaultTTL: 30 * time.Second}

	cases := []struct {
		name    string
		header  http.Header
		wantTTL time.Duration
		wantOK  bool
	}{
		{
			name:    "s-maxage outranks max-age and Expires",
			header:  http.Header{"Cache-Control": {"s-maxage=10, max-age=20"}, "Expires": {now.Add(time.Hour).Format(http.TimeFormat)}, "Date": {date}},
			wantTTL: 10 * time.Second, wantOK: true,
		},
		{
			name:    "max-age outranks Expires",
			header:  http.Header{"Cache-Control": {"max-age=20"}, "Expires": {now.Add(time.Hour).Format(http.TimeFormat)}, "Date": {date}},
			wantTTL: 20 * time.Second, wantOK: true,
		},
		{
			name:    "Expires is measured against the origin Date, not Jul's clock",
			header:  http.Header{"Expires": {now.Add(-30 * time.Minute).Format(http.TimeFormat)}, "Date": {now.Add(-time.Hour).Format(http.TimeFormat)}},
			wantTTL: 30 * time.Minute, wantOK: true,
		},
		{
			name:   "Expires already in the past is uncacheable",
			header: http.Header{"Expires": {now.Add(-time.Minute).Format(http.TimeFormat)}, "Date": {date}},
		},
		{
			name:   "unparseable Expires means already expired",
			header: http.Header{"Expires": {"0"}, "Date": {date}},
		},
		{
			name:    "no explicit freshness falls back to default_ttl",
			header:  http.Header{},
			wantTTL: 30 * time.Second, wantOK: true,
		},
		{name: "max-age=0 is uncacheable", header: ccHeader("max-age=0")},
		{name: "malformed max-age is uncacheable", header: ccHeader("max-age=oops")},
		{name: "s-maxage=0 is uncacheable", header: ccHeader("s-maxage=0")},
		{name: "no-store is uncacheable", header: ccHeader("no-store, max-age=600")},
		{name: "private is uncacheable in a shared cache", header: ccHeader("private, max-age=600")},
		{
			name:    "no-cache is storable with the default lifetime",
			header:  ccHeader("no-cache"),
			wantTTL: 30 * time.Second, wantOK: true,
		},
		{
			name:    "no-cache with max-age=0 is still storable",
			header:  ccHeader("no-cache, max-age=0"),
			wantTTL: 30 * time.Second, wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ttl, _, ok := c.freshness(http.StatusOK, tc.header, parseResponsePolicy(tc.header), now)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && ttl != tc.wantTTL {
				t.Errorf("ttl = %v, want %v", ttl, tc.wantTTL)
			}
		})
	}
}

// TestFreshnessStaleWindowIsZeroWhenRevalidationIsMandatory proves the global
// stale_while_revalidate setting cannot give a stale window to a response the
// origin said must never be served stale.
func TestFreshnessStaleWindowIsZeroWhenRevalidationIsMandatory(t *testing.T) {
	now := time.Now()
	c := &Cache{defaultTTL: time.Minute, swr: time.Minute}

	for _, cc := range []string{"must-revalidate", "proxy-revalidate", "no-cache"} {
		h := ccHeader(cc + ", max-age=60")
		_, swr, ok := c.freshness(http.StatusOK, h, parseResponsePolicy(h), now)
		if !ok {
			t.Fatalf("%s: expected storable", cc)
		}
		if swr != 0 {
			t.Errorf("%s: stale window = %v, want 0", cc, swr)
		}
	}

	h := ccHeader("max-age=60")
	if _, swr, _ := c.freshness(http.StatusOK, h, parseResponsePolicy(h), now); swr != time.Minute {
		t.Errorf("an ordinary response keeps the configured stale window, got %v", swr)
	}
}

// TestStaleOnErrorWindowContract pins which setting wins when the origin and
// Jul's configuration disagree about serving stale after a failed validation.
func TestStaleOnErrorWindowContract(t *testing.T) {
	c := &Cache{sif: time.Minute}
	cases := []struct {
		name  string
		entry *Entry
		want  time.Duration
	}{
		{"global setting applies by default", &Entry{}, time.Minute},
		{"explicit response window replaces the global one", &Entry{HasStaleIfError: true, StaleIfError: 10 * time.Second}, 10 * time.Second},
		{"explicit zero response window disables it", &Entry{HasStaleIfError: true}, 0},
		{"must-revalidate forbids stale reuse outright", &Entry{MustRevalidate: true}, 0},
		{
			"must-revalidate outranks an explicit stale-if-error",
			&Entry{MustRevalidate: true, HasStaleIfError: true, StaleIfError: time.Hour},
			0,
		},
		{"the global setting does not override response no-cache", &Entry{RequiresValidation: true}, 0},
		{
			"an explicit stale-if-error does apply to a no-cache response",
			&Entry{RequiresValidation: true, HasStaleIfError: true, StaleIfError: 10 * time.Second},
			10 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.staleOnErrorWindow(tc.entry); got != tc.want {
				t.Errorf("staleOnErrorWindow = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestInitialAgeCorrection proves a response that already spent time in an
// upstream cache is not served for its full lifetime again here.
func TestInitialAgeCorrection(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{"no metadata", http.Header{}, 0},
		{"Date 60s in the past", http.Header{"Date": {now.Add(-time.Minute).Format(http.TimeFormat)}}, time.Minute},
		{"Age header alone", http.Header{"Age": {"90"}}, 90 * time.Second},
		{
			"the larger of apparent age and Age wins",
			http.Header{"Date": {now.Add(-time.Minute).Format(http.TimeFormat)}, "Age": {"300"}},
			5 * time.Minute,
		},
		{"a Date in the future never makes an entry younger", http.Header{"Date": {now.Add(time.Hour).Format(http.TimeFormat)}}, 0},
		{"a negative Age is ignored", http.Header{"Age": {"-5"}}, 0},
		{"a malformed Age is ignored", http.Header{"Age": {"soon"}}, 0},
		{"an unparseable Date is ignored", http.Header{"Date": {"whenever"}}, 0},
		{"an overflowing Age is clamped", http.Header{"Age": {"99999999999999999999"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := initialAge(tc.header, now); got != tc.want {
				t.Errorf("initialAge = %v, want %v", got, tc.want)
			}
		})
	}
}
