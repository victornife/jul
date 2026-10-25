// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"jul/internal/config"
)

// SelectorKind identifies one of the supported cache invalidation selector
// modes (#621, ADR 0027 §1).
type SelectorKind string

const (
	SelectorExactURL SelectorKind = "exact_url"
	SelectorPrefix   SelectorKind = "prefix"
	SelectorGlob     SelectorKind = "glob"
	SelectorTag      SelectorKind = "tag"
	SelectorHost     SelectorKind = "host"
	SelectorKey      SelectorKind = "key"
	SelectorAll      SelectorKind = "all"
)

// TagMatchMode specifies how multi-tag selectors combine tags.
type TagMatchMode string

const (
	TagMatchAny TagMatchMode = "any"
	TagMatchAll TagMatchMode = "all"
)

// Bounds for selector inputs and glob complexity (ADR 0027 §1).
const (
	MaxSelectorHostBytes    = 255
	MaxSelectorURIBytes     = 2048
	MaxSelectorPatternBytes = 512
	MaxGlobSegments         = 32
	MaxGlobSingleWildcards  = 8
	MaxGlobMultiWildcards   = 2
)

// Selector is the caller-supplied cache invalidation selector before
// normalization.
type Selector struct {
	Kind     SelectorKind `json:"kind"`
	Host     string       `json:"host,omitempty"`
	URL      string       `json:"url,omitempty"`
	URI      string       `json:"uri,omitempty"`
	Prefix   string       `json:"prefix,omitempty"`
	Pattern  string       `json:"pattern,omitempty"`
	Tags     []string     `json:"tags,omitempty"`
	TagMatch TagMatchMode `json:"tag_match,omitempty"`
	Key      string       `json:"key,omitempty"`
	RouteID  string       `json:"route_id,omitempty"`
	All      bool         `json:"all,omitempty"`
}

// NormalizedSelector is a validated, canonicalized selector ready for preview,
// fence registration, or execution.
type NormalizedSelector struct {
	Kind                SelectorKind
	Host                string
	URI                 string
	Prefix              string
	Pattern             string
	Tags                []string
	TagMatch            TagMatchMode
	Key                 string
	KeyMethod           string
	RouteID             string
	BroadScope          bool
	ExplicitDefaultPort bool
	globSegments        []string
	digest              [32]byte
}

// SelectorError reports a deterministic validation error on a selector field.
type SelectorError struct {
	Field   string
	Message string
}

func (e *SelectorError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("invalid cache selector (%s): %s", e.Field, e.Message)
	}
	return "invalid cache selector: " + e.Message
}

func selErr(field, msg string) error {
	return &SelectorError{Field: field, Message: msg}
}

// ParseSelector validates and canonicalizes in against optional active config
// cfg (used when in.RouteID is specified to resolve authoritative server_names
// and route path scope).
func ParseSelector(in Selector, cfg *config.Config) (NormalizedSelector, error) {
	kind := SelectorKind(strings.ToLower(strings.TrimSpace(string(in.Kind))))
	routeID := strings.TrimSpace(in.RouteID)

	var routeHosts []string
	var routePath string
	var routeType string
	if routeID != "" {
		if cfg == nil {
			return NormalizedSelector{}, selErr("route_id", "route_id resolution requires an active server configuration")
		}
		found := false
		for i := range cfg.Servers {
			srv := &cfg.Servers[i]
			for j := range srv.Locations {
				loc := &srv.Locations[j]
				if loc.RouteID != nil && *loc.RouteID == routeID {
					found = true
					for _, sn := range srv.ServerNames {
						nh, err := normalizeSelectorHost(sn)
						if err == nil && !strings.HasPrefix(nh, "*.") && nh != "*" {
							routeHosts = append(routeHosts, nh)
						}
					}
					routePath = loc.Match.Path
					routeType = loc.Match.Type
					if routeType == "" {
						routeType = "prefix"
					}
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return NormalizedSelector{}, selErr("route_id", fmt.Sprintf("unknown route_id %q in active configuration", routeID))
		}
	}

	rawHost := strings.TrimSpace(in.Host)
	if rawHost == "" && len(routeHosts) == 1 {
		rawHost = routeHosts[0]
	}

	var out NormalizedSelector
	out.Kind = kind
	out.RouteID = routeID

	switch kind {
	case SelectorExactURL:
		if in.All || in.Prefix != "" || in.Pattern != "" || len(in.Tags) > 0 || in.Key != "" {
			return NormalizedSelector{}, selErr("kind", "exact_url cannot be combined with prefix, pattern, tags, key, or all")
		}
		host, reqURI, defaultPort, err := parseExactURLInputs(rawHost, in.URL, in.URI)
		if err != nil {
			return NormalizedSelector{}, err
		}
		if err := checkRouteHostAllowed(host, routeID, routeHosts); err != nil {
			return NormalizedSelector{}, err
		}
		out.Host = host
		out.URI = reqURI
		out.ExplicitDefaultPort = defaultPort

	case SelectorPrefix:
		if in.All || in.URL != "" || in.URI != "" || in.Pattern != "" || len(in.Tags) > 0 || in.Key != "" {
			return NormalizedSelector{}, selErr("kind", "prefix selector cannot be combined with url, uri, pattern, tags, key, or all")
		}
		host, defaultPort, err := parseRequiredHost(rawHost, "host")
		if err != nil {
			return NormalizedSelector{}, err
		}
		if err := checkRouteHostAllowed(host, routeID, routeHosts); err != nil {
			return NormalizedSelector{}, err
		}
		prefix := in.Prefix
		if prefix == "" && routePath != "" && routePath != "/" && routeType == "prefix" {
			prefix = routePath
		}
		normPrefix, err := validateSelectorPrefix(prefix)
		if err != nil {
			return NormalizedSelector{}, err
		}
		out.Host = host
		out.Prefix = normPrefix
		out.ExplicitDefaultPort = defaultPort

	case SelectorGlob:
		if in.All || in.URL != "" || in.URI != "" || in.Prefix != "" || len(in.Tags) > 0 || in.Key != "" {
			return NormalizedSelector{}, selErr("kind", "glob selector cannot be combined with url, uri, prefix, tags, key, or all")
		}
		host, defaultPort, err := parseRequiredHost(rawHost, "host")
		if err != nil {
			return NormalizedSelector{}, err
		}
		if err := checkRouteHostAllowed(host, routeID, routeHosts); err != nil {
			return NormalizedSelector{}, err
		}
		normPattern, segs, err := validateSelectorGlob(in.Pattern)
		if err != nil {
			return NormalizedSelector{}, err
		}
		out.Host = host
		out.Pattern = normPattern
		out.globSegments = segs
		out.ExplicitDefaultPort = defaultPort

	case SelectorTag:
		if in.All || in.URL != "" || in.URI != "" || in.Prefix != "" || in.Pattern != "" || in.Key != "" {
			return NormalizedSelector{}, selErr("kind", "tag selector cannot be combined with url, uri, prefix, pattern, key, or all")
		}
		host, defaultPort, err := parseRequiredHost(rawHost, "host")
		if err != nil {
			return NormalizedSelector{}, err
		}
		if err := checkRouteHostAllowed(host, routeID, routeHosts); err != nil {
			return NormalizedSelector{}, err
		}
		tags, mode, err := validateSelectorTags(in.Tags, in.TagMatch)
		if err != nil {
			return NormalizedSelector{}, err
		}
		out.Host = host
		out.Tags = tags
		out.TagMatch = mode
		out.ExplicitDefaultPort = defaultPort

	case SelectorHost:
		if in.All || in.URL != "" || in.URI != "" || in.Prefix != "" || in.Pattern != "" || len(in.Tags) > 0 || in.Key != "" {
			return NormalizedSelector{}, selErr("kind", "host selector cannot be combined with url, uri, prefix, pattern, tags, key, or all")
		}
		host, defaultPort, err := parseRequiredHost(rawHost, "host")
		if err != nil {
			return NormalizedSelector{}, err
		}
		if err := checkRouteHostAllowed(host, routeID, routeHosts); err != nil {
			return NormalizedSelector{}, err
		}
		out.Host = host
		out.BroadScope = true
		out.ExplicitDefaultPort = defaultPort

	case SelectorKey:
		if in.All || rawHost != "" || in.URL != "" || in.URI != "" || in.Prefix != "" || in.Pattern != "" || len(in.Tags) > 0 {
			return NormalizedSelector{}, selErr("kind", "key selector cannot be combined with host, url, uri, prefix, pattern, tags, or all")
		}
		method, host, reqURI, defaultPort, err := validateSelectorRawKey(in.Key)
		if err != nil {
			return NormalizedSelector{}, err
		}
		out.Key = keyFor(method, host, reqURI)
		out.KeyMethod = method
		out.Host = host
		out.URI = reqURI
		out.ExplicitDefaultPort = defaultPort

	case SelectorAll:
		if !in.All {
			return NormalizedSelector{}, selErr("all", "all selector requires all = true")
		}
		if rawHost != "" || in.URL != "" || in.URI != "" || in.Prefix != "" || in.Pattern != "" || len(in.Tags) > 0 || in.Key != "" || routeID != "" {
			return NormalizedSelector{}, selErr("all", "all selector cannot be combined with host, url, uri, prefix, pattern, tags, key, or route_id")
		}
		out.BroadScope = true

	default:
		return NormalizedSelector{}, selErr("kind", "kind must be one of exact_url, prefix, glob, tag, host, key, or all")
	}

	out.digest = computeSelectorDigest(out)
	return out, nil
}

func checkRouteHostAllowed(host, routeID string, routeHosts []string) error {
	if routeID == "" {
		return nil
	}
	if len(routeHosts) == 0 {
		return selErr("route_id", fmt.Sprintf("route %q has no explicit non-wildcard server_names; specify host without route_id or configure explicit server_names", routeID))
	}
	for _, rh := range routeHosts {
		if rh == host {
			return nil
		}
	}
	return selErr("host", fmt.Sprintf("host %q does not belong to route %q server_names", host, routeID))
}

func parseRequiredHost(rawHost, field string) (host string, explicitDefaultPort bool, err error) {
	if strings.TrimSpace(rawHost) == "" {
		return "", false, selErr(field, "host is required and must not be empty")
	}
	return normalizeSelectorHostWithPortFlag(rawHost, field)
}

func normalizeSelectorHost(raw string) (string, error) {
	h, _, err := normalizeSelectorHostWithPortFlag(raw, "host")
	return h, err
}

func normalizeSelectorHostWithPortFlag(raw, field string) (host string, explicitDefaultPort bool, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false, selErr(field, "host must not be empty")
	}
	if s != raw {
		return "", false, selErr(field, "host must not have leading or trailing whitespace")
	}
	if len(s) > MaxSelectorHostBytes {
		return "", false, selErr(field, fmt.Sprintf("host exceeds maximum length of %d bytes", MaxSelectorHostBytes))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c >= 0x7f || c == '/' || c == '\\' || c == '?' || c == '#' || c == '@' {
			return "", false, selErr(field, "host must be a valid authority (hostname or [IPv6] with optional :port) without scheme, path, query, or userinfo")
		}
	}
	lower := strings.ToLower(s)
	var hostPart, portPart string
	if strings.HasPrefix(lower, "[") {
		closeIdx := strings.IndexByte(lower, ']')
		if closeIdx <= 1 {
			return "", false, selErr(field, "malformed IPv6 host literal")
		}
		ipStr := lower[1:closeIdx]
		if net.ParseIP(ipStr) == nil {
			return "", false, selErr(field, "invalid IPv6 address in host")
		}
		rest := lower[closeIdx+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return "", false, selErr(field, "invalid characters after IPv6 literal")
			}
			portPart = rest[1:]
			if portPart == "" {
				return "", false, selErr(field, "empty port in host authority")
			}
		}
	} else {
		if strings.Count(lower, ":") > 1 {
			return "", false, selErr(field, "unbracketed IPv6 literal or multiple colons in host")
		}
		if colon := strings.IndexByte(lower, ':'); colon >= 0 {
			hostPart = lower[:colon]
			portPart = lower[colon+1:]
			if portPart == "" {
				return "", false, selErr(field, "empty port in host authority")
			}
		} else {
			hostPart = lower
		}
		if hostPart == "" || hostPart == "*" || strings.Contains(hostPart, "*") {
			return "", false, selErr(field, "host must be a concrete hostname or IP address (wildcards are not permitted)")
		}
		if strings.HasPrefix(hostPart, ".") || strings.HasSuffix(hostPart, ".") || strings.Contains(hostPart, "..") {
			return "", false, selErr(field, "hostname contains invalid leading, trailing, or consecutive dots")
		}
		for i := 0; i < len(hostPart); i++ {
			c := hostPart[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' {
				continue
			}
			return "", false, selErr(field, fmt.Sprintf("hostname contains invalid character %q", c))
		}
	}
	if portPart != "" {
		portNum, pErr := strconv.Atoi(portPart)
		if pErr != nil || portNum < 1 || portNum > 65535 || strconv.Itoa(portNum) != portPart {
			return "", false, selErr(field, "port must be a decimal integer between 1 and 65535")
		}
		if portNum == 80 || portNum == 443 {
			explicitDefaultPort = true
		}
	}
	return lower, explicitDefaultPort, nil
}

func parseExactURLInputs(rawHost, rawURL, rawURI string) (host, reqURI string, explicitDefaultPort bool, err error) {
	if rawURL != "" && rawURI != "" {
		return "", "", false, selErr("url", "specify either url or host+uri, not both")
	}
	if rawURL != "" {
		if len(rawURL) > MaxSelectorURIBytes+MaxSelectorHostBytes+16 {
			return "", "", false, selErr("url", "url exceeds maximum length")
		}
		if strings.Contains(rawURL, "#") {
			return "", "", false, selErr("url", "url must not contain a #fragment")
		}
		schemeEnd := strings.Index(rawURL, "://")
		if schemeEnd <= 0 {
			return "", "", false, selErr("url", "url must start with http:// or https://")
		}
		scheme := strings.ToLower(rawURL[:schemeEnd])
		if scheme != "http" && scheme != "https" {
			return "", "", false, selErr("url", fmt.Sprintf("unsupported scheme %q; only http and https are supported", scheme))
		}
		afterScheme := rawURL[schemeEnd+3:]
		if afterScheme == "" {
			return "", "", false, selErr("url", "url is missing host authority")
		}
		var urlHostRaw, uriPart string
		slashIdx := strings.IndexByte(afterScheme, '/')
		qIdx := strings.IndexByte(afterScheme, '?')
		splitIdx := slashIdx
		if qIdx >= 0 && (splitIdx < 0 || qIdx < splitIdx) {
			return "", "", false, selErr("url", "url query must follow a '/' path separator")
		}
		if splitIdx < 0 {
			urlHostRaw = afterScheme
			uriPart = "/"
		} else {
			urlHostRaw = afterScheme[:splitIdx]
			uriPart = afterScheme[splitIdx:]
		}
		parsedHost, defPort, hErr := normalizeSelectorHostWithPortFlag(urlHostRaw, "url")
		if hErr != nil {
			return "", "", false, hErr
		}
		if rawHost != "" {
			explicitHost, _, ehErr := normalizeSelectorHostWithPortFlag(rawHost, "host")
			if ehErr != nil {
				return "", "", false, ehErr
			}
			if explicitHost != parsedHost {
				return "", "", false, selErr("host", fmt.Sprintf("host %q does not match url authority %q", explicitHost, parsedHost))
			}
		}
		validURI, uErr := validateRawRequestURI(uriPart, "url")
		if uErr != nil {
			return "", "", false, uErr
		}
		return parsedHost, validURI, defPort, nil
	}

	if rawURI == "" {
		return "", "", false, selErr("uri", "exact_url requires either url or host+uri")
	}
	parsedHost, defPort, hErr := parseRequiredHost(rawHost, "host")
	if hErr != nil {
		return "", "", false, hErr
	}
	validURI, uErr := validateRawRequestURI(rawURI, "uri")
	if uErr != nil {
		return "", "", false, uErr
	}
	return parsedHost, validURI, defPort, nil
}

func validateRawRequestURI(raw, field string) (string, error) {
	if raw == "" {
		return "", selErr(field, "path must not be empty")
	}
	if len(raw) > MaxSelectorURIBytes {
		return "", selErr(field, fmt.Sprintf("URI exceeds maximum length of %d bytes", MaxSelectorURIBytes))
	}
	if raw[0] != '/' {
		return "", selErr(field, "URI must start with '/'")
	}
	if strings.IndexByte(raw, '#') >= 0 {
		return "", selErr(field, "URI must not contain a #fragment")
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c <= 0x20 || c >= 0x7f || c == '\\' {
			return "", selErr(field, "URI contains forbidden control, whitespace, non-ASCII, or backslash characters")
		}
		if c == '%' {
			if i+2 >= len(raw) || !isHexByte(raw[i+1]) || !isHexByte(raw[i+2]) {
				return "", selErr(field, "URI contains malformed percent-encoding")
			}
			hi := fromHexByte(raw[i+1])
			lo := fromHexByte(raw[i+2])
			decoded := (hi << 4) | lo
			if decoded <= 0x1f || decoded == 0x7f || decoded == '\\' {
				return "", selErr(field, "URI contains percent-encoded control or backslash byte")
			}
		}
	}
	pathOnly := raw
	if q := strings.IndexByte(raw, '?'); q >= 0 {
		pathOnly = raw[:q]
	}
	if hasDotSegment(pathOnly) {
		return "", selErr(field, "path must not contain '.' or '..' traversal segments")
	}
	return raw, nil
}

func hasDotSegment(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return true
	}
	segs := strings.Split(path[1:], "/")
	for _, seg := range segs {
		if seg == "." || seg == ".." {
			return true
		}
		lower := strings.ToLower(seg)
		if lower == "%2e" || lower == "%2e." || lower == ".%2e" || lower == "%2e%2e" {
			return true
		}
	}
	return false
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func fromHexByte(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func validateSelectorPrefix(raw string) (string, error) {
	if raw == "" {
		return "", selErr("prefix", "prefix is required and must start with '/'")
	}
	if len(raw) > MaxSelectorPatternBytes {
		return "", selErr("prefix", fmt.Sprintf("prefix exceeds maximum length of %d bytes", MaxSelectorPatternBytes))
	}
	if raw == "/" {
		return "", selErr("prefix", "prefix '/' matches the entire host; use kind = 'host' for whole-hostname invalidation")
	}
	if strings.IndexByte(raw, '?') >= 0 {
		return "", selErr("prefix", "prefix must not include a query string ('?'); prefix selectors match all query variants on matching paths")
	}
	if strings.IndexByte(raw, '*') >= 0 {
		return "", selErr("prefix", "prefix must not contain '*' wildcards; use kind = 'glob' for wildcard patterns")
	}
	if strings.Contains(raw, "//") {
		return "", selErr("prefix", "prefix must not contain consecutive '//' separators")
	}
	if _, err := validateRawRequestURI(raw, "prefix"); err != nil {
		return "", err
	}
	return raw, nil
}

func validateSelectorGlob(raw string) (string, []string, error) {
	if raw == "" {
		return "", nil, selErr("pattern", "pattern is required and must start with '/'")
	}
	if len(raw) > MaxSelectorPatternBytes {
		return "", nil, selErr("pattern", fmt.Sprintf("pattern exceeds maximum length of %d bytes", MaxSelectorPatternBytes))
	}
	if raw[0] != '/' {
		return "", nil, selErr("pattern", "pattern must start with '/'")
	}
	if raw == "/" || raw == "/**" {
		return "", nil, selErr("pattern", fmt.Sprintf("pattern %q is not a bounded path glob; use kind = 'host' for whole-hostname invalidation", raw))
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c <= 0x20 || c >= 0x7f || c == '\\' {
			return "", nil, selErr("pattern", "pattern contains forbidden control, whitespace, non-ASCII, or backslash characters")
		}
		switch c {
		case '?', '#', '[', ']', '{', '}', '^', '$', '(', ')', '|', '+':
			return "", nil, selErr("pattern", fmt.Sprintf("pattern contains unsupported metacharacter %q; only '*' (single segment) and '**' (standalone multi-segment) are supported", c))
		case '%':
			if i+2 >= len(raw) || !isHexByte(raw[i+1]) || !isHexByte(raw[i+2]) {
				return "", nil, selErr("pattern", "pattern contains malformed percent-encoding")
			}
			hi := fromHexByte(raw[i+1])
			lo := fromHexByte(raw[i+2])
			decoded := (hi << 4) | lo
			if decoded <= 0x1f || decoded == 0x7f || decoded == '\\' {
				return "", nil, selErr("pattern", "pattern contains percent-encoded control or backslash byte")
			}
		}
	}
	if hasDotSegment(raw) {
		return "", nil, selErr("pattern", "pattern must not contain '.' or '..' traversal segments")
	}
	segs := strings.Split(raw[1:], "/")
	if len(segs) > MaxGlobSegments {
		return "", nil, selErr("pattern", fmt.Sprintf("pattern has %d path segments, exceeding maximum of %d", len(segs), MaxGlobSegments))
	}
	singleCount := 0
	multiCount := 0
	for _, seg := range segs {
		if seg == "**" {
			multiCount++
			if multiCount > MaxGlobMultiWildcards {
				return "", nil, selErr("pattern", fmt.Sprintf("pattern exceeds maximum of %d '**' wildcards", MaxGlobMultiWildcards))
			}
			continue
		}
		if strings.Contains(seg, "**") {
			return "", nil, selErr("pattern", "'**' wildcard must be a standalone path segment (e.g. '/articles/**/comments')")
		}
		stars := strings.Count(seg, "*")
		singleCount += stars
		if singleCount > MaxGlobSingleWildcards {
			return "", nil, selErr("pattern", fmt.Sprintf("pattern exceeds maximum of %d '*' wildcards", MaxGlobSingleWildcards))
		}
	}
	return raw, segs, nil
}

func validateSelectorTags(rawTags []string, rawMode TagMatchMode) ([]string, TagMatchMode, error) {
	if len(rawTags) == 0 {
		return nil, "", selErr("tags", "at least one tag is required for tag selector")
	}
	if len(rawTags) > MaxSelectorTags {
		return nil, "", selErr("tags", fmt.Sprintf("tags count %d exceeds maximum of %d", len(rawTags), MaxSelectorTags))
	}
	seen := make(map[string]struct{}, len(rawTags))
	out := make([]string, 0, len(rawTags))
	for i, t := range rawTags {
		trimmed := strings.TrimSpace(t)
		if trimmed != t {
			return nil, "", selErr(fmt.Sprintf("tags[%d]", i), "tag must not contain leading or trailing whitespace")
		}
		if err := ValidateTag(t); err != nil {
			return nil, "", selErr(fmt.Sprintf("tags[%d]", i), err.Error())
		}
		if _, exists := seen[t]; !exists {
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	mode := TagMatchMode(strings.ToLower(strings.TrimSpace(string(rawMode))))
	if mode == "" {
		mode = TagMatchAny
	}
	if mode != TagMatchAny && mode != TagMatchAll {
		return nil, "", selErr("tag_match", "tag_match must be 'any' or 'all'")
	}
	return out, mode, nil
}

func validateSelectorRawKey(rawKey string) (method, host, reqURI string, explicitDefaultPort bool, err error) {
	if rawKey == "" {
		return "", "", "", false, selErr("key", "key is required and must have format METHOD\\nhost\\nrequest_uri")
	}
	parts := strings.Split(rawKey, "\n")
	if len(parts) != 3 {
		return "", "", "", false, selErr("key", "key must have exact format METHOD\\nhost\\nrequest_uri")
	}
	method = parts[0]
	if method != http.MethodGet && method != http.MethodHead {
		return "", "", "", false, selErr("key", "key method must be GET or HEAD")
	}
	host, explicitDefaultPort, err = normalizeSelectorHostWithPortFlag(parts[1], "key")
	if err != nil {
		return "", "", "", false, err
	}
	if host != parts[1] {
		return "", "", "", false, selErr("key", "key host must be lowercased")
	}
	reqURI, err = validateRawRequestURI(parts[2], "key")
	if err != nil {
		return "", "", "", false, err
	}
	return method, host, reqURI, explicitDefaultPort, nil
}

func computeSelectorDigest(s NormalizedSelector) [32]byte {
	h := sha256.New()
	writePart := func(parts ...string) {
		for _, p := range parts {
			_, _ = fmt.Fprintf(h, "%d:%s\n", len(p), p)
		}
	}
	writePart(
		string(s.Kind),
		s.Host,
		s.URI,
		s.Prefix,
		s.Pattern,
		string(s.TagMatch),
		s.Key,
		s.RouteID,
	)
	writePart(strconv.Itoa(len(s.Tags)))
	writePart(s.Tags...)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Digest returns the deterministic SHA-256 digest of the canonical selector.
func (s NormalizedSelector) Digest() [32]byte { return s.digest }

// DigestHex returns the lowercase hex SHA-256 digest of the canonical selector.
func (s NormalizedSelector) DigestHex() string { return hex.EncodeToString(s.digest[:]) }

// RedactedSummary returns a secret-safe Selector projection suitable for
// retention in the operation ledger, audit log, and external responses.
// Query parameter values on exact_url and key selectors are replaced with
// "?<redacted>" so credentials or tokens accidentally passed in a URL query
// never enter retained state or logs.
func (s NormalizedSelector) RedactedSummary() Selector {
	out := Selector{
		Kind:     s.Kind,
		Host:     s.Host,
		Prefix:   s.Prefix,
		Pattern:  s.Pattern,
		TagMatch: s.TagMatch,
		RouteID:  s.RouteID,
		All:      s.Kind == SelectorAll,
	}
	if len(s.Tags) > 0 {
		out.Tags = append([]string(nil), s.Tags...)
	}
	if s.URI != "" {
		out.URI = redactURIQuery(s.URI)
	}
	if s.Key != "" {
		out.Key = s.KeyMethod + "\n" + s.Host + "\n" + redactURIQuery(s.URI)
	}
	return out
}

func redactURIQuery(uri string) string {
	if q := strings.IndexByte(uri, '?'); q >= 0 {
		return uri[:q] + "?<redacted>"
	}
	return uri
}

// MatchesResource reports whether s selects a cached resource identified by
// normalized host, raw requestURI (path + optional ?query), and sorted tags.
func (s NormalizedSelector) MatchesResource(host, requestURI string, tags []string) bool {
	switch s.Kind {
	case SelectorAll:
		return true
	case SelectorHost:
		return host == s.Host
	case SelectorExactURL, SelectorKey:
		return host == s.Host && requestURI == s.URI
	case SelectorPrefix:
		if host != s.Host {
			return false
		}
		path := stripQueryFromURI(requestURI)
		return matchSegmentPrefix(s.Prefix, path)
	case SelectorGlob:
		if host != s.Host {
			return false
		}
		path := stripQueryFromURI(requestURI)
		return matchAnchoredGlob(s.globSegments, path)
	case SelectorTag:
		if host != s.Host || len(tags) == 0 {
			return false
		}
		return matchTags(s.Tags, s.TagMatch, tags)
	default:
		return false
	}
}

func stripQueryFromURI(requestURI string) string {
	if q := strings.IndexByte(requestURI, '?'); q >= 0 {
		return requestURI[:q]
	}
	return requestURI
}

// matchSegmentPrefix implements ADR 0027 §1.2 segment-bounded path prefix
// matching:
//   - prefix ending with "/" (e.g. "/articles/") matches any path starting with
//     "/articles/".
//   - prefix not ending with "/" (e.g. "/articles") matches "/articles" exact
//     or "/articles/..." where the next byte is literal '/'.
//
// Because requestURI keeps "%2F" percent-encoded, "/articles" never matches
// "/articles%2F42" or "/articles-archive".
func matchSegmentPrefix(prefix, path string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if strings.HasSuffix(prefix, "/") {
		return true
	}
	return len(path) == len(prefix) || path[len(prefix)] == '/'
}

// matchAnchoredGlob matches path (starting with '/') against pre-split pattern
// segments (ADR 0027 §1.3).
func matchAnchoredGlob(patternSegs []string, path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	pathSegs := strings.Split(path[1:], "/")
	pIdx, sIdx := 0, 0
	starPIdx, starSIdx := -1, -1

	for sIdx < len(pathSegs) {
		if pIdx < len(patternSegs) && patternSegs[pIdx] == "**" {
			starPIdx = pIdx
			starSIdx = sIdx
			pIdx++
			continue
		}
		if pIdx < len(patternSegs) && matchSingleGlobSegment(patternSegs[pIdx], pathSegs[sIdx]) {
			pIdx++
			sIdx++
			continue
		}
		if starPIdx >= 0 {
			pIdx = starPIdx + 1
			starSIdx++
			sIdx = starSIdx
			continue
		}
		return false
	}

	for pIdx < len(patternSegs) && patternSegs[pIdx] == "**" {
		pIdx++
	}
	return pIdx == len(patternSegs)
}

// matchSingleGlobSegment matches one path segment (no '/') against a segment
// pattern that may contain '*' wildcards.
func matchSingleGlobSegment(pattern, segment string) bool {
	if pattern == "*" {
		return true
	}
	if strings.IndexByte(pattern, '*') < 0 {
		return pattern == segment
	}
	pIdx, sIdx := 0, 0
	starIdx, matchIdx := -1, 0
	for sIdx < len(segment) {
		if pIdx < len(pattern) && pattern[pIdx] == '*' {
			starIdx = pIdx
			matchIdx = sIdx
			pIdx++
			continue
		}
		if pIdx < len(pattern) && pattern[pIdx] == segment[sIdx] {
			pIdx++
			sIdx++
			continue
		}
		if starIdx >= 0 {
			pIdx = starIdx + 1
			matchIdx++
			sIdx = matchIdx
			continue
		}
		return false
	}
	for pIdx < len(pattern) && pattern[pIdx] == '*' {
		pIdx++
	}
	return pIdx == len(pattern)
}

func matchTags(wanted []string, mode TagMatchMode, entryTags []string) bool {
	if len(wanted) == 0 || len(entryTags) == 0 {
		return false
	}
	if mode == TagMatchAll {
		for _, w := range wanted {
			if !containsSortedOrLinear(entryTags, w) {
				return false
			}
		}
		return true
	}
	for _, w := range wanted {
		if containsSortedOrLinear(entryTags, w) {
			return true
		}
	}
	return false
}

func containsSortedOrLinear(tags []string, target string) bool {
	for _, t := range tags {
		if t == target {
			return true
		}
	}
	return false
}

func parseBaseKeyParts(k string) (method, host, requestURI string, isVariant bool, ok bool) {
	basePart := k
	if nul := strings.IndexByte(k, 0x00); nul >= 0 {
		basePart = k[:nul]
		isVariant = true
	}
	nl1 := strings.IndexByte(basePart, '\n')
	if nl1 <= 0 {
		return "", "", "", false, false
	}
	rest := basePart[nl1+1:]
	nl2 := strings.IndexByte(rest, '\n')
	if nl2 <= 0 {
		return "", "", "", false, false
	}
	method = basePart[:nl1]
	host = rest[:nl2]
	requestURI = rest[nl2+1:]
	if requestURI == "" || requestURI[0] != '/' {
		return "", "", "", false, false
	}
	return method, host, requestURI, isVariant, true
}

// DeriveRouteCutoverSelectors computes the deduplicated minimal set of cache
// invalidation selectors covering route or backend changes between before and
// after configurations (reusable contract for #624 / CACHE-OPS-04, ADR 0027 §6.2).
func DeriveRouteCutoverSelectors(before, after *config.Config) []NormalizedSelector {
	if before == nil && after == nil {
		return nil
	}
	changedUpstreams := changedUpstreamNames(before, after)
	var selectors []NormalizedSelector
	seen := make(map[[32]byte]struct{})
	addSel := func(s Selector) {
		ns, err := ParseSelector(s, nil)
		if err != nil {
			return
		}
		if _, ok := seen[ns.Digest()]; ok {
			return
		}
		seen[ns.Digest()] = struct{}{}
		selectors = append(selectors, ns)
	}

	collectAffectedRoutes := func(primary, secondary *config.Config) {
		if primary == nil {
			return
		}
		for i := range primary.Servers {
			srv := &primary.Servers[i]
			for j := range srv.Locations {
				loc := &srv.Locations[j]
				if !routeChangedOrUpstreamChanged(primary, srv, loc, secondary, changedUpstreams) {
					continue
				}
				emitSelectorsForRoute(srv, loc, addSel)
			}
		}
	}
	collectAffectedRoutes(before, after)
	collectAffectedRoutes(after, before)
	return selectors
}

func changedUpstreamNames(before, after *config.Config) map[string]bool {
	out := make(map[string]bool)
	if before == nil || after == nil {
		return out
	}
	beforeMap := make(map[string]config.UpstreamConfig, len(before.Upstreams))
	for _, u := range before.Upstreams {
		beforeMap[u.Name] = u
	}
	afterMap := make(map[string]config.UpstreamConfig, len(after.Upstreams))
	for _, u := range after.Upstreams {
		afterMap[u.Name] = u
		bu, ok := beforeMap[u.Name]
		if !ok || !sameUpstreamIdentity(bu, u) {
			out[u.Name] = true
		}
	}
	for name := range beforeMap {
		if _, ok := afterMap[name]; !ok {
			out[name] = true
		}
	}
	return out
}

func sameUpstreamIdentity(a, b config.UpstreamConfig) bool {
	return reflect.DeepEqual(a, b)
}

func routeChangedOrUpstreamChanged(primary *config.Config, srv *config.ServerConfig, loc *config.LocationConfig, other *config.Config, changedUpstreams map[string]bool) bool {
	if other == nil {
		return true
	}
	if up := config.ResolveLocationUpstreamName(primary, *loc); up != "" && changedUpstreams[up] {
		return true
	}
	if up := config.ResolveLocationUpstreamName(other, *loc); up != "" && changedUpstreams[up] {
		return true
	}
	for i := range other.Servers {
		osrv := &other.Servers[i]
		if osrv.Listen != srv.Listen || strings.Join(osrv.ServerNames, ",") != strings.Join(srv.ServerNames, ",") {
			continue
		}
		for j := range osrv.Locations {
			oloc := &osrv.Locations[j]
			if loc.RouteID != nil && oloc.RouteID != nil && *loc.RouteID != "" && *loc.RouteID == *oloc.RouteID {
				return !sameLocationCacheIdentity(loc, oloc)
			}
			if loc.Match.Type == oloc.Match.Type && loc.Match.Path == oloc.Match.Path &&
				loc.Match.CanonicalPredicates() == oloc.Match.CanonicalPredicates() {
				return !sameLocationCacheIdentity(loc, oloc)
			}
		}
	}
	return true
}

func sameLocationCacheIdentity(a, b *config.LocationConfig) bool {
	return reflect.DeepEqual(a, b)
}

func emitSelectorsForRoute(srv *config.ServerConfig, loc *config.LocationConfig, addSel func(Selector)) {
	if len(srv.ServerNames) == 0 {
		addSel(Selector{Kind: SelectorAll, All: true})
		return
	}
	for _, sn := range srv.ServerNames {
		nh, err := normalizeSelectorHost(sn)
		if err != nil || strings.Contains(nh, "*") {
			addSel(Selector{Kind: SelectorAll, All: true})
			return
		}
		mType := loc.Match.Type
		if mType == "" {
			mType = "prefix"
		}
		switch mType {
		case "exact":
			if loc.Match.Path == "/" || loc.Match.Path == "" {
				addSel(Selector{Kind: SelectorHost, Host: nh})
			} else {
				addSel(Selector{Kind: SelectorGlob, Host: nh, Pattern: loc.Match.Path})
			}
		case "prefix":
			if loc.Match.Path == "/" || loc.Match.Path == "" {
				addSel(Selector{Kind: SelectorHost, Host: nh})
			} else {
				addSel(Selector{Kind: SelectorPrefix, Host: nh, Prefix: loc.Match.Path})
			}
		default:
			addSel(Selector{Kind: SelectorHost, Host: nh})
		}
	}
}
