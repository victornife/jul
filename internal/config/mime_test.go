// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
	"time"
)

func TestMIMEInheritanceAndCopy(t *testing.T) {
	global := map[string]string{".ts": "video/mp2t"}
	local := map[string]string{".foo": "application/x-foo"}
	empty := map[string]string{}
	a := &MIMEConfig{Types: &global, DefaultType: "text/plain"}
	b := &MIMEConfig{DefaultType: "application/octet-stream"}
	got := ResolveMIME(nil, a, b)
	global[".ts"] = "bad"
	if (*got.Types)[".ts"] != "video/mp2t" || got.DefaultType != "application/octet-stream" {
		t.Fatal(got)
	}
	got = ResolveMIME(a, &MIMEConfig{Types: &local})
	if len(*got.Types) != 1 || (*got.Types)[".foo"] == "" || got.DefaultType != "text/plain" {
		t.Fatal(got)
	}
	got = ResolveMIME(a, &MIMEConfig{Types: &empty})
	if got.Types == nil || len(*got.Types) != 0 {
		t.Fatal(got)
	}
	if ResolveMIME(nil, nil) != nil {
		t.Fatal("empty policy must remain absent")
	}
}

func TestMIMEAndExpirationRoundTrip(t *testing.T) {
	raw := []byte(`[mime]
 default_type = "text/plain"
 types = {".ts" = "video/mp2t"}
 [[servers]]
 listen = ":8080"
 mime = {default_type = "application/octet-stream"}
 [[servers.locations]]
 match = {type = "prefix", path = "/"}
 root = "/tmp"
 expires = "0s"
 mime = {types = {}}
 `)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = Validate(cfg); err != nil {
		t.Fatal(err)
	}
	encoded, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	loc := again.Servers[0].Locations[0]
	if loc.Expires == nil || *loc.Expires != 0 || loc.MIME == nil || loc.MIME.Types == nil || len(*loc.MIME.Types) != 0 {
		t.Fatalf("presence lost: %s", encoded)
	}
	if again.MIME.Types == nil || (*again.MIME.Types)[".ts"] != "video/mp2t" {
		t.Fatal("root map lost")
	}
}

func TestMIMEValidation(t *testing.T) {
	for _, tc := range []struct{ ext, typ string }{
		{"", "text/plain"}, {"ts", "video/mp2t"}, {".TS", "video/mp2t"}, {".a/b", "text/plain"}, {".a b", "text/plain"},
		{".x", "invalid"}, {".x", "text/plain\r\nX: bad"}, {".x", strings.Repeat("a", 257)},
	} {
		table := map[string]string{tc.ext: tc.typ}
		if len(validateMIME(&MIMEConfig{Types: &table}, "test")) == 0 {
			t.Fatalf("accepted %+v", tc)
		}
	}
	if len(validateMIME(nil, "test")) != 0 {
		t.Fatal("nil invalid")
	}
	if len(validateMIME(&MIMEConfig{DefaultType: "bad"}, "test")) == 0 {
		t.Fatal("bad fallback")
	}
	table := map[string]string{}
	for i := 0; i < 4097; i++ {
		table[strings.Repeat("a", i+1)] = "text/plain"
	}
	if len(validateMIME(&MIMEConfig{Types: &table}, "test")) == 0 {
		t.Fatal("unbounded table")
	}
	table = map[string]string{".a-1_2+3": "application/x-example; version=1"}
	if errs := validateMIME(&MIMEConfig{Types: &table, DefaultType: "text/plain; charset=utf-8"}, "test"); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestExpirationValidation(t *testing.T) {
	for _, tc := range []struct {
		duration time.Duration
		cc       string
		valid    bool
	}{
		{0, "", true}, {-time.Hour, "", true}, {time.Hour, "", true}, {time.Millisecond, "", false}, {0, "public", false},
	} {
		d := Duration(tc.duration)
		loc := LocationConfig{Expires: &d, CacheControl: tc.cc, Root: "/tmp", Match: MatchConfig{Type: "prefix", Path: "/"}}
		errs := validateLocation(loc, "test", nil, nil)
		if (len(errs) == 0) != tc.valid {
			t.Fatalf("%+v: %v", tc, errs)
		}
	}
}
