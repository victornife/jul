// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"net/http"
	"strings"
)

// canonicalPath removes dot segments (RFC 3986 §5.2.4) and merges repeated
// slashes in a decoded, rooted request path. Location selection and every
// handler behind it must see the same path: otherwise `/public/../admin`
// selects the `/` location while a static root or an upstream resolves it to
// `/admin`, bypassing whatever the `/admin` location enforces. A trailing
// slash is preserved, and `..` above the root stays at the root. Paths that are
// not rooted (`*`, authority-form CONNECT) are returned unchanged.
func canonicalPath(p string) string {
	if p == "" || p[0] != '/' || !needsCanonical(p) {
		return p
	}
	segs := strings.Split(p[1:], "/")
	out := make([]string, 0, len(segs))
	trailing := false
	for i, s := range segs {
		last := i == len(segs)-1
		switch s {
		case "", ".":
			trailing = last
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			trailing = last
		default:
			out = append(out, s)
			trailing = false
		}
	}
	res := "/" + strings.Join(out, "/")
	if trailing && len(out) > 0 {
		res += "/"
	}
	return res
}

func needsCanonical(p string) bool {
	return strings.Contains(p, "//") || strings.Contains(p, "/./") || strings.Contains(p, "/../") ||
		strings.HasSuffix(p, "/.") || strings.HasSuffix(p, "/..")
}

// canonicalizeRequest rewrites req's path in place to its canonical form. When
// the path changes, RawPath is cleared so the forwarded request target is the
// re-escaped canonical path rather than the original dot-segment form.
func canonicalizeRequest(req *http.Request) {
	if c := canonicalPath(req.URL.Path); c != req.URL.Path {
		req.URL.Path = c
		req.URL.RawPath = ""
	}
}
