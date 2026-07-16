// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"bytes"
	"testing"
)

func TestServeDirValidates(t *testing.T) {
	c := ServeDir("/var/www", ":8080")
	if err := Validate(c); err != nil {
		t.Fatalf("ServeDir config failed validation: %v", err)
	}
	if len(c.Servers) != 1 || c.Servers[0].Listen != ":8080" {
		t.Fatalf("unexpected server: %+v", c.Servers)
	}
	loc := c.Servers[0].Locations[0]
	if loc.Root != "/var/www" || loc.Match.Type != "prefix" || loc.Match.Path != "/" {
		t.Errorf("unexpected location: %+v", loc)
	}
	if len(loc.Index) == 0 || loc.Index[0] != "index.html" {
		t.Errorf("expected index.html default, got %v", loc.Index)
	}
	if !c.Compression.IsEnabled() {
		t.Error("expected compression enabled in zero-config defaults")
	}
}

func TestServeDirDefaultListen(t *testing.T) {
	c := ServeDir("/srv", "")
	if c.Servers[0].Listen != DefaultZeroConfigListen {
		t.Errorf("listen = %q, want %q", c.Servers[0].Listen, DefaultZeroConfigListen)
	}
}

func TestProxyTargetValidates(t *testing.T) {
	cases := map[string]string{
		":3000":                "http://127.0.0.1:3000",
		"127.0.0.1:3000":       "http://127.0.0.1:3000",
		"localhost:3000":       "http://localhost:3000",
		"http://backend:9000":  "http://backend:9000",
		"https://api.host:443": "https://api.host:443",
	}
	for in, wantPass := range cases {
		c := ProxyTarget(in, ":8080")
		if err := Validate(c); err != nil {
			t.Errorf("ProxyTarget(%q) failed validation: %v", in, err)
			continue
		}
		if got := c.Servers[0].Locations[0].ProxyPass; got != wantPass {
			t.Errorf("ProxyTarget(%q) proxy_pass = %q, want %q", in, got, wantPass)
		}
	}
}

func TestNormalizeProxyTarget(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"  :3000  ":     "http://127.0.0.1:3000",
		"host:1":        "http://host:1",
		"http://x":      "http://x",
		"https://x:2":   "https://x:2",
		"unix:///tmp/x": "unix:///tmp/x",
	}
	for in, want := range cases {
		if got := normalizeProxyTarget(in); got != want {
			t.Errorf("normalizeProxyTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMarshalIsIdempotent(t *testing.T) {
	for _, c := range []*Config{ServeDir("/srv", ":8080"), ProxyTarget(":3000", ":80")} {
		first, err := Marshal(c)
		if err != nil {
			t.Fatalf("first marshal: %v", err)
		}
		reparsed, err := Parse(first)
		if err != nil {
			t.Fatalf("reparse: %v", err)
		}
		second, err := Marshal(reparsed)
		if err != nil {
			t.Fatalf("second marshal: %v", err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("marshal not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
		}
	}
}

func TestFormatErrorAnnotatesTOML(t *testing.T) {
	_, err := Parse([]byte("servers = [\n"))
	if err == nil {
		t.Fatal("expected a parse error for malformed TOML")
	}
	got := FormatError(err)
	if !bytes.Contains([]byte(got), []byte("line ")) {
		t.Errorf("FormatError did not include a line reference:\n%s", got)
	}
}

func TestFormatErrorPlainPassthrough(t *testing.T) {
	if got := FormatError(nil); got != "" {
		t.Errorf("FormatError(nil) = %q, want empty", got)
	}
}
