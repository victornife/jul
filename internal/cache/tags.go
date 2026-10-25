// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"sort"
	"strings"
)

// Bounds for origin grouping-tag ingestion and indexing (#621, ADR 0027 §2).
const (
	MaxTagsPerEntry      = 32
	MaxTagLength         = 128
	MaxTagBytesPerEntry  = 1024
	MaxRawTagHeaderBytes = 4096
	MaxTotalTagLinks     = 65536
	MaxSelectorTags      = 16
)

// Reasons an origin response's tag metadata caused cache storage to be skipped,
// reported via jul_cache_tag_ingest_skipped_total{reason}.
const (
	TagSkipInvalid    = "invalid"
	TagSkipOversize   = "oversize"
	TagSkipIndexLimit = "index_limit"
)

var (
	errEmptyTag      = errors.New("tag must not be empty")
	errTagTooLong    = fmt.Errorf("tag exceeds maximum length of %d bytes", MaxTagLength)
	errInvalidTagChr = errors.New("tag must contain only visible ASCII grouping characters (0x21..0x7e excluding ',', ';', quotes, and whitespace)")
	errCredentialTag = errors.New("tag appears to contain credential or secret material")
)

// ValidateTag checks whether tag is a syntactically valid, non-credential
// grouping tag (ADR 0027 §2.2).
func ValidateTag(tag string) error {
	if len(tag) == 0 {
		return errEmptyTag
	}
	if len(tag) > MaxTagLength {
		return errTagTooLong
	}
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		if c < 0x21 || c > 0x7e {
			return errInvalidTagChr
		}
		switch c {
		case ',', ';', '"', '\'':
			return errInvalidTagChr
		}
	}
	if looksLikeCredentialTag(tag) {
		return errCredentialTag
	}
	return nil
}

// looksLikeCredentialTag rejects tags that resemble bearer tokens, JWTs, API
// keys, or key=value credential pairs so origin misconfiguration cannot index
// or expose secret material as grouping tags.
func looksLikeCredentialTag(tag string) bool {
	lower := strings.ToLower(tag)
	for _, prefix := range []string{
		"bearer:", "bearer_", "bearer-",
		"basic:", "basic_", "basic-",
		"token=", "token:", "access_token=", "refresh_token=",
		"secret=", "secret:", "client_secret=",
		"password=", "passwd=", "pwd=",
		"apikey=", "api_key=", "api-key=",
		"authorization=", "authorization:",
		"cookie=", "set-cookie=", "session=", "sessionid=", "session_id=",
		"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
		"sk-", "akia",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	// Reject compact JWS/JWT-shaped strings (eyJ... with two dots).
	if strings.HasPrefix(tag, "eyJ") && strings.Count(tag, ".") >= 2 && len(tag) >= 24 {
		return true
	}
	return false
}

// canonicalizeTagHeaders normalizes configured header names with
// textproto.CanonicalMIMEHeaderKey and removes empty/duplicate entries.
func canonicalizeTagHeaders(headers []string) []string {
	if len(headers) == 0 {
		return nil
	}
	out := make([]string, 0, len(headers))
	seen := make(map[string]struct{}, len(headers))
	for _, raw := range headers {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		canon := textproto.CanonicalMIMEHeaderKey(trimmed)
		lower := strings.ToLower(canon)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		out = append(out, canon)
	}
	return out
}

// extractOriginTags parses and validates grouping tags from the configured
// origin response headers (ADR 0027 §2.2).
//
// Returns:
//   - tags: sorted, deduplicated slice of valid tags
//   - present: true if at least one configured tag header line was present on h
//   - skipReason: non-empty ("invalid" or "oversize") when a present tag header
//     violated syntax, credential-safety, or per-entry size/count bounds.
func extractOriginTags(h http.Header, tagHeaders []string) (tags []string, present bool, skipReason string) {
	if len(h) == 0 || len(tagHeaders) == 0 {
		return nil, false, ""
	}
	var rawBytes int
	var collected []string
	seen := make(map[string]struct{})
	var totalTagBytes int

	for _, hdrName := range tagHeaders {
		lines := h.Values(hdrName)
		if len(lines) == 0 {
			continue
		}
		present = true
		for _, line := range lines {
			rawBytes += len(line)
			if rawBytes > MaxRawTagHeaderBytes {
				return nil, true, TagSkipOversize
			}
			trimmedLine := strings.TrimSpace(line)
			if trimmedLine == "" {
				// An explicitly empty header line (e.g. "Cache-Tag: ") contributes
				// zero tags and is valid (used on 304 responses to clear tags).
				continue
			}
			// Reject leading, trailing, or consecutive commas such as "a,,b" or ",a".
			parts := strings.Split(trimmedLine, ",")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part == "" {
					return nil, true, TagSkipInvalid
				}
				fields := strings.Fields(part)
				for _, tok := range fields {
					if err := ValidateTag(tok); err != nil {
						return nil, true, TagSkipInvalid
					}
					if _, exists := seen[tok]; exists {
						continue
					}
					seen[tok] = struct{}{}
					collected = append(collected, tok)
					if len(collected) > MaxTagsPerEntry {
						return nil, true, TagSkipOversize
					}
					totalTagBytes += len(tok)
					if totalTagBytes > MaxTagBytesPerEntry {
						return nil, true, TagSkipOversize
					}
				}
			}
		}
	}
	if len(collected) > 1 {
		sort.Strings(collected)
	}
	return collected, present, ""
}

// stripConfiguredTagHeaders removes configured origin tag headers from h.
func stripConfiguredTagHeaders(h http.Header, tagHeaders []string) {
	if len(h) == 0 || len(tagHeaders) == 0 {
		return
	}
	for _, name := range tagHeaders {
		h.Del(name)
	}
}

// stripOriginTagHeadersFromResponse unconditionally removes configured tag
// headers from liveHeader when tag stripping is enabled.
func stripOriginTagHeadersFromResponse(liveHeader, _ http.Header, tagHeaders []string) {
	stripConfiguredTagHeaders(liveHeader, tagHeaders)
}

// mergeTagSets computes the sorted, deduplicated union of tag slices, capped at
// MaxTagsPerEntry and MaxTagBytesPerEntry.
func mergeTagSets(sets ...[]string) []string {
	return mergeTagSetsBounded(MaxTagsPerEntry, MaxTagBytesPerEntry, sets...)
}

// mergeBaseTagSets computes the sorted, deduplicated union of already-validated
// variant tag slices for a base resource without truncating at the per-entry
// MaxTagsPerEntry cap.
func mergeBaseTagSets(sets ...[]string) []string {
	return mergeTagSetsBounded(0, 0, sets...)
}

func mergeTagSetsBounded(maxCount, maxBytes int, sets ...[]string) []string {
	var out []string
	seen := make(map[string]struct{})
	totalBytes := 0
	for _, s := range sets {
		for _, t := range s {
			if _, ok := seen[t]; ok {
				continue
			}
			if (maxCount > 0 && len(out)+1 > maxCount) || (maxBytes > 0 && totalBytes+len(t) > maxBytes) {
				break
			}
			seen[t] = struct{}{}
			out = append(out, t)
			totalBytes += len(t)
		}
	}
	if len(out) > 1 {
		sort.Strings(out)
	}
	return out
}
