// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"jul/internal/config"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestExpirationStatusesAndSigns(t *testing.T) {
	for code := 100; code <= 599; code++ {
		want := code == 200 || code == 201 || code == 204 || code == 206 ||
			code == 301 || code == 302 || code == 303 || code == 304 || code == 307 || code == 308
		if expirationStatus(code) != want {
			t.Fatalf("status %d", code)
		}
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 999, time.FixedZone("offset", 3600))
	for _, tc := range []struct {
		duration    time.Duration
		cc, expires string
	}{
		{time.Hour, "max-age=3600", "Fri, 09 Oct 2026 12:00:00 GMT"},
		{0, "max-age=0", "Fri, 09 Oct 2026 11:00:00 GMT"},
		{-time.Second, "no-cache", "Fri, 09 Oct 2026 10:59:59 GMT"},
	} {
		h := http.Header{"Cache-Control": {"private", "no-store"}, "Expires": {"old", "older"}}
		applyExpiration(h, tc.duration, now)
		if !reflect.DeepEqual(h.Values("Cache-Control"), []string{tc.cc}) || !reflect.DeepEqual(h.Values("Expires"), []string{tc.expires}) {
			t.Fatalf("duration %s: %v", tc.duration, h)
		}
	}
}

func TestExpirationPolicyOrderingAndCaptureIsolation(t *testing.T) {
	duration := config.Duration(time.Hour)
	captured := http.Header{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private")
		captured = w.Header().Clone()
		w.WriteHeader(200)
		w.WriteHeader(500)
	})
	ops := []config.ResponseHeaderOp{{Op: "add", Name: "Cache-Control", Value: configString("public")}}
	h := ResponsePolicyWithExpires(ops, nil, &duration)(inner)
	duration = config.Duration(-time.Hour) // compiled policy owns its value
	rec := httptest.NewRecorder()
	before := time.Now().UTC().Truncate(time.Second)
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	after := time.Now().UTC().Truncate(time.Second)
	exp, err := http.ParseTime(rec.Header().Get("Expires"))
	if err != nil || exp.Before(before.Add(time.Hour)) || exp.After(after.Add(time.Hour)) {
		t.Fatalf("expires %v: %v", exp, err)
	}
	if got := rec.Header().Values("Cache-Control"); !reflect.DeepEqual(got, []string{"max-age=3600", "public"}) {
		t.Fatal(got)
	}
	if captured.Get("Expires") != "" || captured.Get("Cache-Control") != "private" {
		t.Fatal(captured)
	}
}

func configString(v string) *string { return &v }

func TestExpirationSkippedResponsesAndImplicitWrite(t *testing.T) {
	duration := config.Duration(0)
	for _, tc := range []struct {
		code      int
		generated bool
	}{{101, false}, {404, false}, {204, true}, {200, false}} {
		h := ResponsePolicyWithExpires(nil, nil, &duration)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.generated {
				markGeneratedResponse(r)
			}
			w.Header().Set("Cache-Control", "origin")
			if tc.code == 200 {
				_, _ = w.Write([]byte("body"))
			} else {
				w.WriteHeader(tc.code)
			}
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		want := "origin"
		if tc.code == 200 {
			want = "max-age=0"
		}
		if got := rec.Header().Get("Cache-Control"); got != want {
			t.Fatalf("%+v: %q", tc, got)
		}
	}
	if ResponsePolicyWithExpires(nil, nil, nil) != nil {
		t.Fatal("empty policy")
	}
}
