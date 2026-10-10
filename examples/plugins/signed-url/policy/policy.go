// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package policy defines the signed-url/v1 protocol shared by the guest and signer.
package policy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

const MaxURIBytes = 8192

// Policy is immutable after parsing; each guest instance owns its copy.
type Policy struct {
	expParam, sigParam, kidParam string
	skew, maxTTL                 int64
	keys                         map[string][]byte
}

// Parse accepts a bounded flat plugin config. Keys are unpadded base64url
// encodings of 32–64 random bytes under key.<kid>. Errors never echo secrets.
func Parse(raw []byte) (*Policy, error) {
	if len(raw) == 0 || len(raw) > 32768 {
		return nil, errors.New("invalid configuration size")
	}
	var c map[string]string
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, errors.New("configuration must be a string map")
	}
	p := &Policy{expParam: "exp", sigParam: "sig", kidParam: "kid", maxTTL: 86400, keys: make(map[string][]byte)}
	for name, value := range c {
		switch name {
		case "exp_param":
			p.expParam = value
		case "sig_param":
			p.sigParam = value
		case "kid_param":
			p.kidParam = value
		case "clock_skew_seconds", "max_ttl_seconds":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || strconv.FormatInt(n, 10) != value {
				return nil, errors.New("invalid time bound")
			}
			if name == "clock_skew_seconds" {
				p.skew = n
			} else {
				p.maxTTL = n
			}
		default:
			if !strings.HasPrefix(name, "key.") || !token(strings.TrimPrefix(name, "key."), 64) {
				return nil, errors.New("unknown config field or invalid key ID")
			}
			k, err := base64.RawURLEncoding.Strict().DecodeString(value)
			if err != nil || len(k) < 32 || len(k) > 64 || base64.RawURLEncoding.EncodeToString(k) != value {
				return nil, errors.New("keys must encode 32–64 bytes as unpadded base64url")
			}
			p.keys[strings.TrimPrefix(name, "key.")] = k
		}
	}
	if len(p.keys) == 0 || len(p.keys) > 8 {
		return nil, errors.New("configure 1–8 keys")
	}
	if p.skew < 0 || p.skew > 300 || p.maxTTL < 1 || p.maxTTL > 604800 {
		return nil, errors.New("time bounds out of range")
	}
	if !token(p.expParam, 32) || !token(p.sigParam, 32) || !token(p.kidParam, 32) || p.expParam == p.sigParam || p.expParam == p.kidParam || p.sigParam == p.kidParam {
		return nil, errors.New("parameter names must be distinct ASCII tokens")
	}
	return p, nil
}

func token(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// canonicalPath requires one unambiguous origin-form resource identity. Encoded
// slashes/backslashes, dot segments and repeated slashes are rejected so a
// downstream cleaner cannot change the authorized resource. Other query fields
// are forbidden rather than forwarded without authentication.
func canonicalPath(uri string) (*url.URL, bool) {
	if len(uri) == 0 || len(uri) > MaxURIBytes || !strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, "//") || strings.ContainsAny(uri, "\r\n\x00#") {
		return nil, false
	}
	u, err := url.ParseRequestURI(uri)
	if err != nil || u.IsAbs() || u.Host != "" {
		return nil, false
	}
	if strings.ContainsAny(u.Path, "\\\r\n\x00") || strings.Contains(u.Path, "//") {
		return nil, false
	}
	for _, c := range u.Path {
		if c < 0x20 || c == 0x7f {
			return nil, false
		}
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, false
		}
	}
	// Re-encoding a decoded path must give exactly the presented path. This
	// rejects %2f, %5c, alternate escaping and double-decoding ambiguities.
	canonical := (&url.URL{Path: u.Path}).EscapedPath()
	if u.EscapedPath() != canonical || strings.Contains(u.Path, "%") {
		return nil, false
	}
	return u, true
}

func requestMethod(method string) bool { return method == "GET" || method == "HEAD" }

// MAC signs a versioned, newline-framed preimage. GET authorizes HEAD as the
// metadata-only form of the same resource. kid is authenticated as well.
func MAC(key []byte, path, exp, kid string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte("jul-signed-url/v1\nGET\n" + path + "\n" + exp + "\n" + kid))
	return h.Sum(nil)
}

// Validate returns a fixed, value-free denial reason, or "" for success.
// Signature verification precedes expiry classification. Equality uses hmac.Equal.
func (p *Policy) Validate(method, uri string, now int64) string {
	if !requestMethod(method) {
		return "method"
	}
	u, ok := canonicalPath(uri)
	if !ok {
		return "uri"
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 3 || len(q[p.expParam]) != 1 || len(q[p.sigParam]) != 1 || len(q[p.kidParam]) != 1 {
		return "parameters"
	}
	expText, sigText, kid := q.Get(p.expParam), q.Get(p.sigParam), q.Get(p.kidParam)
	exp, err := strconv.ParseInt(expText, 10, 64)
	if err != nil || exp <= 0 || strconv.FormatInt(exp, 10) != expText {
		return "expiry"
	}
	key, ok := p.keys[kid]
	if !ok {
		return "key"
	}
	if len(sigText) != 43 {
		return "signature"
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(sigText)
	if err != nil || !hmac.Equal(MAC(key, u.EscapedPath(), expText, kid), sig) {
		return "signature"
	}
	// Subtraction only occurs between non-negative int64 values, avoiding
	// overflow at extreme attacker-controlled expiries.
	if now < 0 {
		return "clock"
	}
	if now > exp && now-exp > p.skew {
		return "expired"
	}
	if exp > now && exp-now > p.maxTTL {
		return "lifetime"
	}
	return ""
}

// Sign creates an origin-form URL. The caller chooses its public origin and
// ensures expiry satisfies its configured lifetime. The input has no query.
func (p *Policy) Sign(path, kid string, exp int64) (string, error) {
	u, ok := canonicalPath(path)
	if !ok || u.RawQuery != "" || strings.Contains(path, "?") || exp <= 0 {
		return "", errors.New("invalid path or expiry")
	}
	key, ok := p.keys[kid]
	if !ok {
		return "", errors.New("unknown key ID")
	}
	text := strconv.FormatInt(exp, 10)
	q := url.Values{p.expParam: {text}, p.kidParam: {kid}, p.sigParam: {base64.RawURLEncoding.EncodeToString(MAC(key, u.EscapedPath(), text, kid))}}
	u.RawQuery = q.Encode()
	return u.RequestURI(), nil
}
