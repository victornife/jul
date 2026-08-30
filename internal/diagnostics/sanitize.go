// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package diagnostics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	redacted        = "[REDACTED]"
	maxResultString = 4096
)

var (
	secretKeyPattern    = regexp.MustCompile(`(?i)(authorization|proxy[-_]?authorization|cookie|set[-_]?cookie|token|password|passwd|secret|api[-_]?key|private[-_]?key|client[-_]?secret|credential)`)
	errorKeyPattern     = regexp.MustCompile(`(?i)^(error|errors|reason|panic|cause)$`)
	credentialPattern   = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	assignmentPattern   = regexp.MustCompile(`(?i)\b(token|password|passwd|secret|api[-_]?key|client[-_]?secret)\s*([:=])\s*("[^"]*"|'[^']*'|[^\s,;]+)`)
	queryPattern        = regexp.MustCompile(`(?i)([?&](?:token|password|passwd|secret|api[-_]?key|client[-_]?secret)=)[^&#\s]+`)
	cookiePattern       = regexp.MustCompile(`(?i)((?:set-)?cookie\s*[:=]\s*)[^\r\n]+`)
	userinfoPattern     = regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@`)
	privateKeyPattern   = regexp.MustCompile(`(?s)-----BEGIN [^-\r\n]*PRIVATE KEY-----.*?-----END [^-\r\n]*PRIVATE KEY-----`)
	urlPattern          = regexp.MustCompile(`(?i)\b(?:https?|grpc|tcp|udp)://[^\s"'<>]+`)
	windowsPathPattern  = regexp.MustCompile(`(?i)\b[A-Z]:[\\/](?:[^\\/\s"'():]+[\\/])*[^\\/\s"'():]*`)
	uncPathPattern      = regexp.MustCompile(`\\\\[^\\\s"'():]+\\[^\\\s"'():]+(?:\\[^\\\s"'():]+)*`)
	unixPathPattern     = regexp.MustCompile(`(^|[\s"'(=])/(?:[^/\s"'():]+/)*[^/\s"'():]*`)
)

// SanitizeResult applies structural and string-level redaction to a result.
func SanitizeResult(result Result) Result {
	result.Message = SanitizeString(result.Message)
	result.Remediation = SanitizeString(result.Remediation)
	result.Docs = SanitizeString(result.Docs)
	if result.Evidence != nil {
		clean := make(map[string]any, len(result.Evidence))
		for key, value := range result.Evidence {
			clean[key] = sanitizeValue(key, value)
		}
		result.Evidence = clean
	}
	return result
}

// SanitizeJSON parses one JSON value, redacts secret-bearing object keys and
// common credential forms in string values, and emits deterministic indented
// JSON. It rejects trailing values instead of silently sanitizing only a prefix.
func SanitizeJSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return json.MarshalIndent(sanitizeValue("", value), "", "  ")
}

// SanitizeString defensively removes common credential forms from diagnostic
// text. Collectors must still exclude secrets structurally; this is the final
// layer for errors returned by libraries and operating systems.
func SanitizeString(value string) string {
	if value == "" {
		return ""
	}
	value = privateKeyPattern.ReplaceAllString(value, redacted)
	value = credentialPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := strings.Fields(match)
		if len(parts) == 0 {
			return redacted
		}
		return parts[0] + " " + redacted
	})
	value = assignmentPattern.ReplaceAllString(value, `$1$2`+redacted)
	value = queryPattern.ReplaceAllString(value, `$1`+redacted)
	value = cookiePattern.ReplaceAllString(value, `$1`+redacted)
	value = userinfoPattern.ReplaceAllString(value, "://"+redacted+"@")
	return boundString(value)
}

// SanitizeErrorString additionally removes complete URLs and absolute paths
// from operating-system/library errors. Error evidence often embeds configured
// filesystem or endpoint values even when the calling collector excludes them.
func SanitizeErrorString(value string) string {
	value = SanitizeString(value)
	value = urlPattern.ReplaceAllString(value, "[URL REDACTED]")
	value = windowsPathPattern.ReplaceAllString(value, "[PATH REDACTED]")
	value = uncPathPattern.ReplaceAllString(value, "[PATH REDACTED]")
	value = unixPathPattern.ReplaceAllString(value, `${1}[PATH REDACTED]`)
	return boundString(value)
}

func sanitizeValue(key string, value any) any {
	if secretKeyPattern.MatchString(key) {
		return redacted
	}
	if errorKeyPattern.MatchString(strings.TrimSpace(key)) {
		return sanitizeErrorValue(value)
	}
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return SanitizeString(typed)
	case error:
		return SanitizeErrorString(typed.Error())
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			out[i] = SanitizeString(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = sanitizeValue("", item)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for childKey, item := range typed {
			if secretKeyPattern.MatchString(childKey) {
				out[childKey] = redacted
			} else if errorKeyPattern.MatchString(strings.TrimSpace(childKey)) {
				out[childKey] = SanitizeErrorString(item)
			} else {
				out[childKey] = SanitizeString(item)
			}
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, item := range typed {
			out[childKey] = sanitizeValue(childKey, item)
		}
		return out
	default:
		return typed
	}
}

func sanitizeErrorValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return SanitizeErrorString(typed)
	case error:
		return SanitizeErrorString(typed.Error())
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			out[i] = SanitizeErrorString(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = sanitizeErrorValue(item)
		}
		return out
	default:
		return typed
	}
}

func boundString(value string) string {
	if len(value) <= maxResultString {
		return value
	}
	return fmt.Sprintf("%s... [truncated %d bytes]", value[:maxResultString], len(value)-maxResultString)
}
