// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"fmt"
	"jul/internal/config"
	"strings"
	"testing"
	"time"
)

func TestPlainExpiresDurationGrammar(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		seconds int64
	}{
		{"0", 0}, {"0s", 0}, {"42", 42}, {"+1h", 3600}, {"-1h30m", -5400}, {"1y2M3w4d5h6m7s", 38898367}, {"2d", 172800}, {"1h 30m", 5400}, {"\"-1h 30m\"", -5400}, {"'42'", 42}, {"1s2", 3}, {"1m 2", 62}, {"1h ", 3600},
	} {
		d, ok := parseExpires([]string{tc.raw})
		if !ok || d == nil || int64(d.Std()/time.Second) != tc.seconds {
			t.Fatalf("%q: %v %v", tc.raw, d, ok)
		}
	}
	for _, p := range [][]string{nil, {"modified", "1h"}, {"@12h"}, {"-"}, {"1ms"}, {"1.5s"}, {"1s1m"}, {"1h1h"}, {"$value"}, {"epoch"}, {"max"}, {"999999999999999999999y"}, {"9999999999y"}, {"1", "2"}, {"1x"}, {"1 2m"}, {"1s 2s"}, {"h"}, {"1\t2"}} {
		if _, ok := parseExpires(p); ok {
			t.Fatalf("accepted %v", p)
		}
	}
	if d, ok := parseExpires([]string{"off"}); !ok || d != nil {
		t.Fatal("off")
	}
}

func TestIdiomsScopeAndSourceOrder(t *testing.T) {
	cfg, rep := translate(t, `http {
 expires 1h;
 types {video/mp2t ts; text/html html;}
 default_type application/octet-stream;
 gzip on;
 gzip_types application/json;
 server {
  listen 8080;
  location /inherit { }
  location /negative { expires -1; }
  location /zero { expires 0; types {}; default_type text/plain; }
  location /off { expires off; }
  location /custom { types {application/x-custom foo;} }
  client_max_body_size 4m;
  root /tmp;
  index main.html;
 }
 }`)
	if len(rep.Skipped) != 0 {
		t.Fatal(rep.Skipped)
	}
	s := onlyServer(t, cfg)
	if s.ClientMaxBodySize != 4<<20 || strings.Join(cfg.Compression.Types, ",") != "text/html,application/json" {
		t.Fatal(s.ClientMaxBodySize, cfg.Compression.Types)
	}
	for _, loc := range s.Locations {
		if loc.Root != "/tmp" || len(loc.Index) != 1 || loc.Index[0] != "main.html" {
			t.Fatal(loc)
		}
	}
	locs := s.Locations
	if locs[0].Expires == nil || locs[0].Expires.Std() != time.Hour {
		t.Fatal("inherit")
	}
	if locs[1].Expires == nil || locs[1].Expires.Std() != -time.Second {
		t.Fatal("negative")
	}
	if locs[2].Expires == nil || *locs[2].Expires != 0 || len(*locs[2].MIME.Types) != 0 {
		t.Fatal("zero/empty map")
	}
	if locs[3].Expires != nil {
		t.Fatal("off")
	}
	if len(*locs[4].MIME.Types) != 1 || (*locs[4].MIME.Types)[".foo"] != "application/x-custom" {
		t.Fatal("replacement")
	}
	encoded, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := config.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.Validate(parsed); err != nil {
		t.Fatal(err)
	}
}

func TestIdiomsBlockingBoundaries(t *testing.T) {
	for _, directive := range []string{
		"expires modified 1h;", "expires @12h;", "expires $value;", "expires max;", "expires epoch;",
		"types { text/plain .ts; }", "default_type $value;",
		"client_max_body_size 0;", "proxy_buffering on;", "proxy_http_version 1.1;",
		"proxy_set_header Upgrade $http_upgrade;", "proxy_set_header Connection keep-alive;",
	} {
		cfg, rep := translate(t, "http {expires 1h;server {listen 8080;location / {proxy_pass http://127.0.0.1:9000;"+directive+"}}}")
		if len(rep.Skipped) == 0 {
			t.Fatalf("missing report: %s", directive)
		}
		if strings.HasPrefix(directive, "expires") && cfg.Servers[0].Locations[0].Expires != nil {
			t.Fatal("unsupported override retained inherited expires")
		}
	}
}

func TestExactWebSocketAndUnbufferedProxy(t *testing.T) {
	cfg, rep := translate(t, `http {server {listen 8080;location / {
 proxy_pass http://127.0.0.1:9000;
 proxy_http_version 1.1;
 proxy_set_header Upgrade $http_upgrade;
 proxy_set_header Connection "upgrade";
 proxy_buffering off;
 }}}`)
	if len(rep.Skipped) != 0 {
		t.Fatal(rep.Skipped)
	}
	loc := onlyServer(t, cfg).Locations[0]
	if loc.ProxyBuffering == nil || *loc.ProxyBuffering {
		t.Fatal("buffering")
	}
}

func TestNGINXDefaultMIMEAndInvalidValues(t *testing.T) {
	cfg, rep := translate(t, `http {server {listen 8080;default_type application/x-default;location / {root /tmp;}}}`)
	if len(rep.Skipped) != 0 || (*cfg.Servers[0].MIME.Types)[".html"] != "text/html" || len(*cfg.Servers[0].MIME.Types) != 3 {
		t.Fatal(rep, cfg)
	}
	if validMediaType("$variable") || validMediaType("no-slash") || validMediaType("text/plain\r\nX:bad") {
		t.Fatal("invalid media type")
	}
}

func TestIdiomAssessmentBoundaries(t *testing.T) {
	for _, context := range []string{"http", "server", "location"} {
		for _, tc := range []struct {
			directive string
			class     AssessmentClass
		}{
			{"expires 1h;", AssessmentSupported}, {"expires -1;", AssessmentSupported}, {"expires 0;", AssessmentSupported}, {"expires off;", AssessmentSupported},
			{"expires modified 1h;", AssessmentBlocking}, {"expires @12h;", AssessmentBlocking}, {"expires $value;", AssessmentBlocking},
			{"types {};", AssessmentSupported}, {"types {video/mp2t TS;}", AssessmentSupported}, {"types {bad x;}", AssessmentBlocking},
			{"types {text/plain;}", AssessmentBlocking}, {"types {text/plain ts; video/mp2t ts;}", AssessmentSupported},
			{"types {text/plain " + strings.Repeat("a", 64) + ";}", AssessmentBlocking},
			{"default_type application/octet-stream;", AssessmentSupported}, {"default_type $value;", AssessmentBlocking}, {"default_type text/plain extra;", AssessmentBlocking},
		} {
			h, s, l := "", "", ""
			switch context {
			case "http":
				h = tc.directive
			case "server":
				s = tc.directive
			case "location":
				l = tc.directive
			}
			a, _ := assessString(t, "http {"+h+"server {listen 8080;"+s+"location / {root /tmp;"+l+"}}}")
			found := false
			for _, result := range a.Results {
				if string(result.Context) == context && strings.HasPrefix(tc.directive, result.Directive+" ") {
					found = true
					if result.Class != tc.class {
						t.Fatalf("%s %s: %s want %s", context, tc.directive, result.Class, tc.class)
					}
					if result.Provenance == nil || result.Provenance.Start.Line < 1 {
						t.Fatal("missing source provenance")
					}
					if tc.class == AssessmentSupported && len(result.TargetMappings) == 0 {
						t.Fatal("missing target mapping")
					}
				}
			}
			if !found {
				t.Fatalf("missing %s", tc.directive)
			}
		}
	}
	for _, tc := range []struct {
		directive string
		context   string
		class     AssessmentClass
	}{
		{"client_max_body_size 4m;", "server", AssessmentSupported}, {"client_max_body_size 0;", "server", AssessmentBlocking}, {"client_max_body_size nope;", "server", AssessmentBlocking},
		{"gzip_types *;", "http", AssessmentSupported}, {"gzip_types application/json;", "http", AssessmentSupported}, {"gzip_types;", "http", AssessmentBlocking}, {"gzip_types $variable;", "http", AssessmentBlocking}, {"gzip_types text/*;", "http", AssessmentBlocking}, {"gzip_types */*;", "http", AssessmentBlocking},
		{"proxy_buffering off;", "location", AssessmentSupported}, {"proxy_buffering on;", "location", AssessmentBlocking},
	} {
		h, s, l := "", "", ""
		switch tc.context {
		case "http":
			h = tc.directive
		case "server":
			s = tc.directive
		case "location":
			l = tc.directive
		}
		a, _ := assessString(t, "http {"+h+"server {listen 8080;"+s+"location / {proxy_pass http://127.0.0.1:9000;"+l+"}}}")
		found := false
		for _, r := range a.Results {
			if r.Directive == strings.Fields(tc.directive)[0] || r.Directive == strings.TrimSuffix(tc.directive, ";") {
				found = true
				if r.Class != tc.class {
					t.Fatalf("%s: %s", tc.directive, r.Class)
				}
			}
		}
		if !found {
			t.Fatal(tc.directive)
		}
	}
}

func TestWebSocketRecognitionRequiresAllSiblings(t *testing.T) {
	trio := []string{"proxy_http_version 1.1;", "proxy_set_header Upgrade $http_upgrade;", "proxy_set_header Connection upgrade;"}
	variants := []string{
		strings.Join(trio, ""),
		trio[0] + trio[1],
		trio[1] + trio[2],
		trio[0] + trio[2],
		strings.Replace(strings.Join(trio, ""), "1.1", "1.0", 1),
		strings.Replace(strings.Join(trio, ""), "$http_upgrade", "websocket", 1),
		strings.Replace(strings.Join(trio, ""), "Connection upgrade", "Connection keep-alive", 1),
		strings.Join(trio, "") + trio[0],
		strings.Join(trio, "") + trio[1],
		strings.Join(trio, "") + "proxy_set_header Upgrade;",
	}
	for i, directives := range variants {
		a, _ := assessString(t, "http {server {listen 8080;location / {proxy_pass http://127.0.0.1:9000;"+directives+"}}}")
		for _, r := range a.Results {
			if r.Code == "NGX_LOCATION_WEBSOCKET" {
				want := AssessmentBlocking
				if i == 0 {
					want = AssessmentInformational
				}
				if r.Class != want {
					t.Fatalf("variant %d: %s", i, r.Class)
				}
			}
		}
	}
}

func TestMIMEBlockBoundsAndRepeatedLocalMappings(t *testing.T) {
	cfg, rep := translate(t, `http {types {video/mp2t ts;}types {text/plain ts;application/json json;}
 server {listen 8080;location / {root /tmp;}}}`)
	if len(rep.Skipped) != 0 || (*cfg.MIME.Types)[".ts"] != "text/plain" || (*cfg.MIME.Types)[".json"] != "application/json" {
		t.Fatal("last mapping must win", rep, cfg.MIME)
	}
	var source strings.Builder
	source.WriteString("http {types {text/plain ")
	for i := 0; i < 4097; i++ {
		fmt.Fprintf(&source, "ext%d ", i)
	}
	source.WriteString(";}server {listen 8080;location / {root /tmp;}}}")
	a, _ := assessString(t, source.String())
	if !a.HasBlocking() {
		t.Fatal("unbounded MIME source table accepted")
	}
}

func FuzzPlainExpires(f *testing.F) {
	for _, seed := range []string{"off", "0", "-1h30m", "1y2M3w4d5h6m7s", "modified", "@12h", "$value", "999999999999999999999"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			return
		}
		d, ok := parseExpires([]string{raw})
		if !ok || d == nil {
			return
		}
		if d.Std()%time.Second != 0 {
			t.Fatal("fractional expiration escaped the importer")
		}
		text, err := d.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip config.Duration
		if err = roundTrip.UnmarshalText(text); err != nil || roundTrip != *d {
			t.Fatal("target duration cannot round-trip", err)
		}
	})
}

func TestQuotedPlainDurationAndMIME(t *testing.T) {
	cfg, rep := translate(t, `http {expires "-1h 30m";types {"text/plain" "TS";video/mp2t ts;}default_type "application/octet-stream";server {listen 8080;location / {root /tmp;}}}`)
	if len(rep.Skipped) != 0 || cfg.Servers[0].Locations[0].Expires.Std() != -90*time.Minute || (*cfg.MIME.Types)[".ts"] != "video/mp2t" || cfg.MIME.DefaultType != "application/octet-stream" {
		t.Fatal(rep, cfg)
	}
}

func TestRepeatedGZIPTypes(t *testing.T) {
	for _, tc := range []struct {
		directives, want string
	}{
		{"gzip_types TEXT/HTML application/json;gzip_types application/json text/plain;", "text/html,application/json,text/plain"},
		{"gzip_types application/json;gzip_types *;gzip_types text/plain;", "*"},
		{"gzip_types * application/json;", "*"},
	} {
		cfg, rep := translate(t, "http {gzip on;"+tc.directives+"server {listen 8080;location / {return 200;}}}")
		if len(rep.Skipped) != 0 || strings.Join(cfg.Compression.Types, ",") != tc.want {
			t.Fatal(rep, cfg.Compression.Types)
		}
	}
}
