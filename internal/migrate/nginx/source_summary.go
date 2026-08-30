// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxAssessmentSummaryRunes = 160

// safeDirectiveSummary produces one bounded representation for both human and
// JSON reports. It deliberately favors omission over accidentally echoing an
// authentication token, cookie, URL password, private-key path or source code.
func safeDirectiveSummary(name string, params []string) string {
	name = sanitizeSummaryToken(name)
	lower := strings.ToLower(name)

	switch {
	case lower == "proxy_set_header" || lower == "add_header":
		if len(params) == 0 {
			return name
		}
		header := sanitizeSummaryToken(params[0])
		if len(params) == 1 || sensitiveHeaderName(header) {
			return boundedSummary(name + " " + header + " <redacted>")
		}
		return boundedSummary(name + " " + header + " <value-omitted>")

	case lower == "proxy_pass":
		if len(params) == 0 {
			return name
		}
		return boundedSummary(name + " " + redactURLUserinfo(params[0]))

	case lower == "listen" || lower == "server_name" || lower == "location" || lower == "gzip" || lower == "real_ip_header" || lower == "real_ip_recursive":
		if len(params) == 0 {
			return name
		}
		return boundedSummary(name + " " + sanitizeSummaryToken(params[0]))

	case lower == "return":
		if len(params) == 0 {
			return name
		}
		// The numeric status is useful; redirect/body values may contain signed
		// URLs or application data and are intentionally omitted.
		return boundedSummary(name + " " + sanitizeSummaryToken(params[0]) + " <target-omitted>")

	case lower == "include":
		return name + " <path-omitted>"

	case lower == "ssl_certificate", lower == "ssl_certificate_key", lower == "auth_basic_user_file", lower == "env", strings.Contains(lower, "lua"):
		return name + " <redacted>"

	case lower == "set_real_ip_from":
		if len(params) == 0 {
			return name
		}
		return boundedSummary(name + " " + sanitizeSummaryToken(params[0]))

	default:
		if len(params) == 0 {
			return name
		}
		return name + " <arguments-omitted>"
	}
}

func sensitiveHeaderName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "authorization" || lower == "proxy-authorization" || lower == "cookie" || lower == "set-cookie" {
		return true
	}
	return strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "api-key") || strings.Contains(lower, "apikey")
}

func redactURLUserinfo(raw string) string {
	raw = sanitizeSummaryToken(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		if at := strings.LastIndex(raw, "@"); at >= 0 {
			if scheme := strings.Index(raw, "://"); scheme >= 0 && scheme < at {
				return raw[:scheme+3] + "<redacted-userinfo>@" + raw[at+1:]
			}
		}
		return "<target-omitted>"
	}
	if u.User != nil {
		u.User = url.User("<redacted-userinfo>")
	}
	return boundedSummary(u.String())
}

func sanitizeSummaryToken(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return boundedSummary(strings.Join(strings.Fields(value), " "))
}

func boundedSummary(value string) string {
	if utf8.RuneCountInString(value) <= maxAssessmentSummaryRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxAssessmentSummaryRunes-1]) + "…"
}
