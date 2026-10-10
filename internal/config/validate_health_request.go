// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"
)

const (
	MaxHealthHeaders          = 32
	MaxHealthHeaderNameBytes  = 128
	MaxHealthHeaderValueBytes = 4096
	MaxHealthHeaderBytes      = 16 << 10
	MaxHealthHostBytes        = 253
)

// validateHealthRequest checks routing identity and bounded HTTP-only fields.
// Errors never echo values: a health header can contain a resolved credential.
func validateHealthRequest(h *HealthCheckConfig, where string) []error {
	var errs []error
	if h.Host != "" {
		if h.Type != "http" && h.Type != "grpc" {
			errs = append(errs, fmt.Errorf("%s.host: only applies to http/grpc probes", where))
		}
		if !validHealthAuthority(h.Host) {
			errs = append(errs, fmt.Errorf("%s.host: want an ASCII hostname or IP with optional port, at most %d bytes", where, MaxHealthHostBytes))
		}
	}
	if len(h.Headers) > 0 && h.Type != "http" {
		errs = append(errs, fmt.Errorf("%s.headers: only applies to http probes", where))
	}
	if len(h.Headers) > MaxHealthHeaders {
		errs = append(errs, fmt.Errorf("%s.headers: at most %d entries", where, MaxHealthHeaders))
	}
	seen := make(map[string]bool, len(h.Headers))
	total := 0
	for name, value := range h.Headers {
		total += len(name) + len(value)
		canonical := http.CanonicalHeaderKey(name)
		switch {
		case len(name) > MaxHealthHeaderNameBytes || !httpguts.ValidHeaderFieldName(name):
			errs = append(errs, fmt.Errorf("%s.headers: invalid or oversized header name", where))
		case IsHopByHopHeaderName(canonical) || canonical == "Content-Length" || canonical == "Host":
			errs = append(errs, fmt.Errorf("%s.headers: forbidden header %s; configure Host through host", where, canonical))
		case seen[canonical]:
			errs = append(errs, fmt.Errorf("%s.headers: duplicate header name ignoring case", where))
		}
		seen[canonical] = true
		if len(value) > MaxHealthHeaderValueBytes || !httpguts.ValidHeaderFieldValue(value) {
			errs = append(errs, fmt.Errorf("%s.headers: invalid or oversized header value", where))
		}
	}
	if total > MaxHealthHeaderBytes {
		errs = append(errs, fmt.Errorf("%s.headers: combined names and values exceed %d bytes", where, MaxHealthHeaderBytes))
	}
	return errs
}

func validHealthAuthority(s string) bool {
	if len(s) > MaxHealthHostBytes || strings.ContainsAny(s, "/\\?#@% \t\r\n") {
		return false
	}
	u, err := url.Parse("http://" + s)
	if err != nil || u.Host != s || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if strings.Contains(s, ":") {
		if strings.HasSuffix(s, ":") {
			return false
		}
		if strings.Contains(host, ":") {
			if net.ParseIP(host) == nil || !strings.HasPrefix(s, "[") {
				return false
			}
		}
		if port := u.Port(); port != "" {
			p, err := strconv.Atoi(port)
			if err != nil || p < 1 || p > 65535 {
				return false
			}
		}
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			valid := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-'
			if !valid {
				return false
			}
		}
	}
	return true
}
