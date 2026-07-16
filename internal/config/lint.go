// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"errors"
	"fmt"
	"net"

	"github.com/pelletier/go-toml/v2"
)

// Severity classifies a Diagnostic. Validate produces hard errors that block
// startup; Lint produces warnings that flag risky-but-valid configurations.
type Severity int

const (
	// SeverityWarning marks an advisory finding that does not block startup.
	SeverityWarning Severity = iota
	// SeverityError marks a finding that makes the configuration invalid.
	SeverityError
)

// String returns the lowercase label for a severity ("warning" or "error").
func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	default:
		return "warning"
	}
}

// MarshalJSON emits the severity as its lowercase string label ("warning" or
// "error") so the CLI JSON contract is stable and self-describing rather than an
// opaque enum ordinal.
func (s Severity) MarshalJSON() ([]byte, error) {
	return []byte(`"` + s.String() + `"`), nil
}

// Diagnostic is a single finding produced by Lint. Field locates the offending
// block (e.g. "servers[0].tls"), Message states the problem, and Hint suggests
// a fix. The JSON field names are lowercase and stable so the `jul lint -json`
// output can be consumed by automation.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Field    string   `json:"field,omitempty"`
	Message  string   `json:"message"`
	Hint     string   `json:"hint,omitempty"`
}

// Lint inspects a (defaulted) Config for best-practice and security issues that
// are valid but probably unintended. It complements Validate, which rejects
// outright-invalid configurations; Lint only returns warnings. Each rule is
// conservative to avoid false positives.
func Lint(c *Config) []Diagnostic {
	var diags []Diagnostic

	// A server with no locations answers every request with 404. An HTTPS
	// redirector (redirect_https set) legitimately has no locations.
	for i, srv := range c.Servers {
		if len(srv.Locations) == 0 && srv.RedirectHTTPS == 0 {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("servers[%d] (listen %q)", i, srv.Listen),
				Message:  "server has no locations; every request will return 404",
				Hint:     "add a [[servers.locations]] block, or set redirect_https for an HTTP->HTTPS redirector",
			})
		}

		// Duplicate location matches: the later block is unreachable because the
		// router selects the first equivalent match.
		seen := map[string]int{}
		for j, loc := range srv.Locations {
			key := loc.Match.Type + "\x00" + loc.Match.Path
			if first, ok := seen[key]; ok {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Field:    fmt.Sprintf("servers[%d].locations[%d]", i, j),
					Message:  fmt.Sprintf("duplicate match of locations[%d] (%s %q); this block is unreachable", first, loc.Match.Type, loc.Match.Path),
					Hint:     "remove the duplicate or change its match",
				})
			} else {
				seen[key] = j
			}

			// Directory listing leaks file names and structure.
			if loc.DirectoryListing {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Field:    fmt.Sprintf("servers[%d].locations[%d]", i, j),
					Message:  "directory_listing is enabled; it exposes file names to clients",
					Hint:     "disable directory_listing in production unless a browsable index is intended",
				})
			}
		}

		// TLS without an explicit minimum version relies on the runtime default.
		if srv.TLS != nil && srv.TLS.Enabled && srv.TLS.MinVersion == "" {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("servers[%d].tls", i),
				Message:  "tls.min_version is not set; the runtime default applies",
				Hint:     `set min_version = "1.3" for the strongest protocol, or "1.2" for broader compatibility`,
			})
		}
	}

	// An admin listener reachable off-loopback without a token is unauthenticated
	// remote control of the server.
	if c.Admin.Enabled && c.Admin.Token == "" && !isLoopbackListen(c.Admin.Listen) {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "[admin]",
			Message:  fmt.Sprintf("admin listener %q is not loopback and has no token; it is unauthenticated", c.Admin.Listen),
			Hint:     "set [admin].token, or bind listen to 127.0.0.1",
		})
	}

	// Literal secrets in sensitive fields. Prefer a ${env:NAME} or ${file:/path}
	// reference (SEC-1) so the secret is not committed in the config file and is
	// redacted from logs. The lint never echoes the value itself.
	if c.Admin.Enabled && c.Admin.Token != "" && !containsSecretRef(c.Admin.Token) {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "[admin].token",
			Message:  "admin token is a literal value in the config file",
			Hint:     `reference a secret instead, e.g. token = "${env:JUL_ADMIN_TOKEN}" or "${file:/run/secrets/admin-token}"`,
		})
	}
	for i := range c.Upstreams {
		d := c.Upstreams[i].Discovery
		if d == nil {
			continue
		}
		if d.Consul != nil && d.Consul.Token != "" && !containsSecretRef(d.Consul.Token) {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("upstreams[%d].discovery.consul.token", i),
				Message:  "Consul ACL token is a literal value in the config file",
				Hint:     `reference a secret instead, e.g. token = "${env:CONSUL_TOKEN}"`,
			})
		}
		if d.Kubernetes != nil && d.Kubernetes.Token != "" && !containsSecretRef(d.Kubernetes.Token) {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("upstreams[%d].discovery.kubernetes.token", i),
				Message:  "Kubernetes bearer token is a literal value in the config file",
				Hint:     `reference a secret instead, e.g. token = "${file:/var/run/secrets/kubernetes.io/serviceaccount/token}"`,
			})
		}
	}

	// Compression is a cheap, broadly beneficial default.
	if !c.Compression.IsEnabled() {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "[compression]",
			Message:  "response compression is disabled",
			Hint:     "set [compression].enabled = true to reduce bandwidth for text responses",
		})
	}

	// Legacy [global] log-destination fields are not consumed by the current
	// runtime; the [observability.access_log] block is the correct path.
	if c.Global.AccessLog != "" {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "[global].access_log",
			Message:  "this field is not consumed; use [observability.access_log] instead",
			Hint:     "set sinks = [\"file\"] and file = \"<path>\" under [observability.access_log]",
		})
	}
	if c.Global.ErrorLog != "" {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "[global].error_log",
			Message:  "this field is not consumed; the structured logger writes to stderr via [global].log_format",
			Hint:     "remove error_log; redirect stderr in your process supervisor instead",
		})
	}

	return diags
}

// isLoopbackListen reports whether a listen address binds only the loopback
// interface. An empty host or a wildcard (0.0.0.0/::) is treated as exposed.
func isLoopbackListen(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	case "", "0.0.0.0", "::":
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// FormatError renders a configuration load/parse error for humans. When the
// error wraps a go-toml decode error it returns the library's annotated excerpt
// pointing at the offending line and column; otherwise it returns the plain
// message.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		row, col := de.Position()
		return fmt.Sprintf("%s (line %d, column %d):\n%s", err.Error(), row, col, de.String())
	}
	return err.Error()
}
