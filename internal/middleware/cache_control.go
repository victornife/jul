// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"net/http"
	"strings"
)

// cacheControlNoTransform reports whether Cache-Control contains the
// case-insensitive no-transform directive. Commas inside quoted extension
// values are ignored so an unrelated extension cannot accidentally disable
// transformation.
func cacheControlNoTransform(h http.Header) bool {
	for _, value := range h.Values("Cache-Control") {
		start := 0
		quoted := false
		escaped := false
		for i := 0; i <= len(value); i++ {
			if i == len(value) || (!quoted && value[i] == ',') {
				part := strings.TrimSpace(value[start:i])
				if eq := strings.IndexByte(part, '='); eq >= 0 {
					part = strings.TrimSpace(part[:eq])
				}
				if strings.EqualFold(part, "no-transform") {
					return true
				}
				start = i + 1
				continue
			}

			switch value[i] {
			case '\\':
				if quoted && !escaped {
					escaped = true
					continue
				}
			case '"':
				if !escaped {
					quoted = !quoted
				}
			}
			escaped = false
		}
	}
	return false
}
