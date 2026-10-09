// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl
package policy

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

var key = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))

func encoded(c map[string]string) []byte { b, _ := json.Marshal(c); return b }
func config(extra map[string]string) map[string]string {
	c := map[string]string{"key.current": key, "key.previous": base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("p", 32)))}
	for k, v := range extra {
		c[k] = v
	}
	return c
}
func mustPolicy(t *testing.T, c map[string]string) *Policy {
	t.Helper()
	p, err := Parse(encoded(c))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func mustSign(t *testing.T, p *Policy, path, kid string, exp int64) string {
	t.Helper()
	s, err := p.Sign(path, kid, exp)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func change(s, k, v string) string {
	u, _ := url.Parse(s)
	q := u.Query()
	q.Set(k, v)
	u.RawQuery = q.Encode()
	return u.String()
}
func TestValidation(t *testing.T) {
	p := mustPolicy(t, config(nil))
	link := mustSign(t, p, "/media/movie.mp4", "current", 1100)
	if !strings.Contains(link, "PJFTcdY9okGN1A96DD4vghV6Amndmp7PW75xh183_uk") {
		t.Fatal("independent HMAC framing vector changed")
	}
	for _, tc := range []struct {
		name, method, uri string
		now               int64
		want              string
	}{
		{"valid", "GET", link, 1000, ""}, {"head", "HEAD", link, 1000, ""}, {"expiry inclusive", "GET", link, 1100, ""},
		{"expired", "GET", link, 1101, "expired"}, {"tampered path", "GET", strings.Replace(link, "movie", "other", 1), 1000, "signature"},
		{"tampered expiry", "GET", change(link, "exp", "1101"), 1000, "signature"}, {"unknown kid", "GET", change(link, "kid", "unknown"), 1000, "key"},
		{"kid is signed", "GET", change(link, "kid", "previous"), 1000, "signature"}, {"post", "POST", link, 1000, "method"},
		{"short sig", "GET", change(link, "sig", "x"), 1000, "signature"}, {"bad base64", "GET", change(link, "sig", strings.Repeat("!", 43)), 1000, "signature"},
		{"missing", "GET", "/media/movie.mp4", 1000, "parameters"}, {"duplicate", "GET", link + "&exp=1100", 1000, "parameters"},
		{"unsigned query", "GET", link + "&download=another", 1000, "parameters"}, {"bad query", "GET", link + "&bad=%zz", 1000, "parameters"},
		{"negative exp", "GET", change(link, "exp", "-1"), 1000, "expiry"}, {"leading zero", "GET", change(link, "exp", "01100"), 1000, "expiry"},
		{"plus exp", "GET", change(link, "exp", "+1100"), 1000, "expiry"}, {"overflow", "GET", change(link, "exp", "9223372036854775808"), 1000, "expiry"},
		{"bad uri", "GET", "//example/other", 1000, "uri"}, {"negative clock", "GET", link, -1, "clock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Validate(tc.method, tc.uri, tc.now); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	rotated := mustSign(t, p, "/media/movie.mp4", "previous", 1100)
	if got := p.Validate("GET", rotated, 1000); got != "" {
		t.Fatal(got)
	}
	removed := mustPolicy(t, map[string]string{"key.current": key})
	if got := removed.Validate("GET", rotated, 1000); got != "key" {
		t.Fatal(got)
	}
	far := mustSign(t, p, "/media/movie.mp4", "current", 9223372036854775807)
	if p.Validate("GET", far, 1000) != "lifetime" {
		t.Fatal("unbounded expiry")
	}
	if p.Validate("GET", far, 9223372036854775807) != "" {
		t.Fatal("overflow at max clock")
	}
}
func TestSkewAndCustomParams(t *testing.T) {
	p := mustPolicy(t, config(map[string]string{"clock_skew_seconds": "5", "max_ttl_seconds": "60", "exp_param": "expires", "sig_param": "signature", "kid_param": "keyid"}))
	link := mustSign(t, p, "/media/a%20b.mp4", "current", 1000)
	for _, tc := range []struct {
		now  int64
		want string
	}{{1005, ""}, {1006, "expired"}, {940, ""}, {939, "lifetime"}} {
		if got := p.Validate("HEAD", link, tc.now); got != tc.want {
			t.Fatalf("now %d: %s", tc.now, got)
		}
	}
	if p.Validate("GET", link+"&exp=1000", 1000) != "parameters" {
		t.Fatal("alternate parameter accepted")
	}
}
func TestConfigRejects(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("null"), []byte("{}"), []byte("[]"), []byte(`{"key.current":42}`), []byte("{"), []byte(strings.Repeat("x", 32769))} {
		if _, err := Parse(raw); err == nil {
			t.Fatal("invalid raw accepted")
		}
	}
	for _, extra := range []map[string]string{
		{"key.current": "short"}, {"key.current": key + "="}, {"key.current": base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 65)))},
		{"key.bad.id": key}, {"key.": key}, {"key." + strings.Repeat("x", 65): key}, {"unknown": "secret"},
		{"clock_skew_seconds": "-1"}, {"clock_skew_seconds": "301"}, {"clock_skew_seconds": "x"}, {"clock_skew_seconds": "01"},
		{"max_ttl_seconds": "0"}, {"max_ttl_seconds": "604801"}, {"exp_param": "sig"}, {"kid_param": "sig"}, {"exp_param": "kid"},
		{"exp_param": ""}, {"sig_param": "bad&name"}, {"kid_param": strings.Repeat("x", 33)},
	} {
		if _, err := Parse(encoded(config(extra))); err == nil {
			t.Fatalf("accepted %v", extra)
		}
	}
	c := config(nil)
	for i := 0; i < 9; i++ {
		c["key."+strings.Repeat("x", i+1)] = key
	}
	if _, err := Parse(encoded(c)); err == nil {
		t.Fatal("accepted too many keys")
	}
}
func TestCanonicalPathAndSigningRejects(t *testing.T) {
	p := mustPolicy(t, config(nil))
	for _, path := range []string{"", "relative", "https://host/path", "//host/path", "/a//b", "/a/../b", "/a/./b", "/a%2Fb", "/a%5Cb", "/a%252Fb", "/a%0Ab", "/a%09b", "/a%7Fb", "/a%00b", "/a\\b", "/a#fragment", "/a\r", "/a%zz", "/" + strings.Repeat("a", 8192)} {
		if _, ok := canonicalPath(path); ok {
			t.Errorf("accepted %q", path)
		}
		if _, err := p.Sign(path, "current", 1000); err == nil {
			t.Errorf("signed %q", path)
		}
	}
	for _, path := range []string{"/a?", "/a?x=1"} {
		if _, err := p.Sign(path, "current", 1000); err == nil {
			t.Fatal("signed query")
		}
	}
	if _, err := p.Sign("/a", "unknown", 1000); err == nil {
		t.Fatal("signed unknown key")
	}
	if _, err := p.Sign("/a", "current", 0); err == nil {
		t.Fatal("signed nonpositive expiry")
	}
}
func FuzzValidate(f *testing.F) {
	p, _ := Parse(encoded(config(nil)))
	f.Add("GET", "/media/demo.mp4?exp=1&kid=current&sig=bad", int64(1000))
	f.Fuzz(func(t *testing.T, method, uri string, now int64) { _ = p.Validate(method, uri, now) })
}
