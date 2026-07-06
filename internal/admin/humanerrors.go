// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "strings"

// validationErrorResponse is the v2 structured error shape for config
// validation failures. The UI renders these directly as human-readable
// guidance.
type validationErrorResponse struct {
	OK      bool              `json:"ok"`
	Message string            `json:"message,omitempty"`
	Errors  []validationError `json:"errors,omitempty"`
}

type validationError struct {
	Code     string `json:"code"`
	Path     string `json:"path,omitempty"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
	Severity string `json:"severity"` // error | warning
}

// humanizeErr maps a raw backend validation error into one or more structured
// validationError entries. The "code" field lets the UI theme the message
// (e.g. red for unknown_upstream, yellow for likely-misconfiguration); the
// "path" field carries the offending config location (e.g.
// servers[0].locations[1]) so a large config's errors stay locatable.
//
// The backend joins every problem with newlines (errors.Join), so each line is
// treated as one issue. A leading dot/bracket path token of the form
// "servers[0].locations[1]: message" is lifted into Path, keeping the human
// summary clean.
//
// This is intentionally heuristic: the backend (config/validate.go) is the
// source of truth for correctness; this layer is for presentation only.
func humanizeErr(raw string) []validationError {
	// TODO: expand this table as new validation messages are added.
	// Each entry should produce a clear, actionable human sentence.
	mappings := []struct {
		token string
		err   validationError
	}{
		{
			token: "no upstream named",
			err: validationError{
				Code:     "unknown_upstream",
				Summary:  "Upstream reference points to a pool that does not exist.",
				Detail:   "Create the upstream in the config or choose an existing one.",
				Severity: "error",
			},
		},
		{
			token: "listen address",
			err: validationError{
				Code:     "bad_listen",
				Summary:  "A server listen address is malformed or conflicts with another.",
				Detail:   "Use host:port syntax. Check that no two server blocks bind to the same address.",
				Severity: "error",
			},
		},
		{
			token: "tls is enabled but",
			err: validationError{
				Code:     "tls_misconfig",
				Summary:  "TLS is enabled but required files or settings are missing.",
				Detail:   "Provide both cert and key, or enable ACME, and ensure the files exist.",
				Severity: "error",
			},
		},
		{
			token: "client_max_body_size",
			err: validationError{
				Code:     "bad_size",
				Summary:  "A byte-size value is malformed.",
				Detail:   "Use a number followed by b, k, kb, m, mb, g, or gb (e.g. 64m).",
				Severity: "error",
			},
		},
		{
			token: "duration",
			err: validationError{
				Code:     "bad_duration",
				Summary:  "A duration value is malformed.",
				Detail:   "Use Go duration syntax: 30s, 5m, 1h, 1h30m.",
				Severity: "error",
			},
		},
		{
			token: "rate limit rate",
			err: validationError{
				Code:     "bad_rate",
				Summary:  "Rate-limit parameters are invalid.",
				Detail:   "Rate must be > 0. Burst must be >= rate.",
				Severity: "error",
			},
		},
		{
			token: "acme account",
			err: validationError{
				Code:     "acme_misconfig",
				Summary:  "ACME settings are incomplete or invalid.",
				Detail:   "Check the email, provider URL, and accepted terms.",
				Severity: "error",
			},
		},
		{
			token: "static root",
			err: validationError{
				Code:     "bad_static_root",
				Summary:  "A static location's root directory could not be opened.",
				Detail:   "Check that the root path exists and is readable by the server.",
				Severity: "error",
			},
		},
		{
			token: "invalid proxy_pass",
			err: validationError{
				Code:     "bad_proxy_pass",
				Summary:  "A proxy_pass value is not a valid URL or upstream name.",
				Detail:   "Use http(s)://host:port or http://upstream-name.",
				Severity: "error",
			},
		},
		{
			token: "plugins:",
			err: validationError{
				Code:     "plugin_build",
				Summary:  "A WASM plugin failed to load or compile.",
				Detail:   "Check the plugin's path and that the module is valid for this build.",
				Severity: "error",
			},
		},
		{
			token: "secrets:",
			err: validationError{
				Code:     "secret_resolve",
				Summary:  "A secret reference could not be resolved.",
				Detail:   "Check that ${env:NAME} / ${file:/path} references resolve to a readable value.",
				Severity: "error",
			},
		},
		{
			token: "waf:",
			err: validationError{
				Code:     "waf_build",
				Summary:  "A WAF policy failed to compile.",
				Detail:   "Check the SecLang directives, rule files, and CRS assets.",
				Severity: "error",
			},
		},
		{
			token: "auth:",
			err: validationError{
				Code:     "auth_build",
				Summary:  "An authentication policy failed to initialise.",
				Detail:   "Check the auth type and its files (e.g. htpasswd path, JWKS / issuer settings).",
				Severity: "error",
			},
		},
	}

	var out []validationError
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		path, msg := splitPath(line)
		matched := false
		for _, m := range mappings {
			if containsFold(line, m.token) {
				e := m.err
				e.Path = path
				out = append(out, e)
				matched = true
				break // one issue per line; first matching rule wins
			}
		}
		if !matched {
			// Unknown error: preserve the (path-stripped) message so the UI
			// still shows something rather than swallowing it.
			out = append(out, validationError{
				Code:     "unknown",
				Path:     path,
				Summary:  msg,
				Severity: "error",
			})
		}
	}
	if len(out) == 0 {
		out = append(out, validationError{
			Code:     "unknown",
			Summary:  raw,
			Severity: "error",
		})
	}
	return out
}

// splitPath separates a leading config path token (e.g.
// "servers[0].locations[1]") from the rest of a validation message of the form
// "path: message". It returns ("", line) when the line has no such prefix. A
// qualifying path must contain at least one [index] segment, which distinguishes
// a structural location from a bare subsystem prefix like "waf:" or "auth:".
func splitPath(line string) (path, msg string) {
	i := pathPrefixLen(line)
	if i == 0 || i >= len(line) || line[i] != ':' {
		return "", line
	}
	prefix := line[:i]
	if !strings.Contains(prefix, "[") {
		return "", line
	}
	return prefix, strings.TrimSpace(line[i+1:])
}

// pathPrefixLen returns the byte length of the leading config path token in
// line, or 0 if line does not start with one. A token is an identifier
// optionally indexed by [N], followed by zero or more .identifier[N] segments.
func pathPrefixLen(line string) int {
	i := 0
	for {
		start := i
		if i < len(line) && isIdentStart(line[i]) {
			i++
			for i < len(line) && isIdentByte(line[i]) {
				i++
			}
		}
		if i == start {
			return 0 // expected an identifier segment
		}
		if i < len(line) && line[i] == '[' {
			j := i + 1
			for j < len(line) && line[j] >= '0' && line[j] <= '9' {
				j++
			}
			if j == i+1 || j >= len(line) || line[j] != ']' {
				return 0 // malformed index ⇒ not a path
			}
			i = j + 1
		}
		if i < len(line) && line[i] == '.' {
			i++
			continue
		}
		return i
	}
}

func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentByte(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

func containsFold(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexFold(s, substr) >= 0)
}

func indexFold(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if lower(s[i+j]) != lower(substr[j]) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func lower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
