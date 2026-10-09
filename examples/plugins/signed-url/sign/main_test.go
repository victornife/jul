// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl
package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken") }
func TestSigner(t *testing.T) {
	key := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	getenv := func(string) string { return key }
	now := func() time.Time { return time.Unix(1000, 0) }
	var out, stderr bytes.Buffer
	if run([]string{"-path", "/media/movie.mp4", "-kid", "current", "-ttl", "300"}, &out, &stderr, getenv, now) != 0 || !strings.Contains(out.String(), "exp=1300") || !strings.Contains(out.String(), "sig=") {
		t.Fatal("valid signer failed", stderr.String())
	}
	for _, args := range [][]string{{"-bad"}, {"-ttl", "0"}, {"-ttl", "86401"}, {"extra"}, {"-path", "/a/../b"}, {"-kid", "bad.id"}} {
		if run(args, &out, &stderr, getenv, now) == 0 {
			t.Fatal("invalid signer succeeded", args)
		}
	}
	if run(nil, &out, &stderr, func(string) string { return "bad" }, now) == 0 {
		t.Fatal("invalid secret accepted")
	}
	if run(nil, brokenWriter{}, &stderr, getenv, now) == 0 {
		t.Fatal("output failure ignored")
	}
}
