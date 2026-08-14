// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"errors"
	"fmt"
	"net"

	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"jul/internal/clientaddr"
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
		if srv.AccessLog != "" {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("servers[%d].access_log", i),
				Message:  "this field is deprecated and ignored; use [observability.access_log] instead",
				Hint:     "move sink selection and file paths to [observability.access_log]",
			})
		}
		if srv.ErrorLog != "" {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("servers[%d].error_log", i),
				Message:  "this field is deprecated and ignored; structured process logs write to stderr",
				Hint:     "remove error_log and route stderr with the process supervisor",
			})
		}
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

		// A trusted-proxy entry that covers the whole address space lets any
		// client assert any address, which is the spoofing case the policy
		// exists to prevent.
		if srv.ClientAddress != nil {
			for j, raw := range srv.ClientAddress.TrustedProxies {
				prefix, err := clientaddr.ParsePrefix(raw)
				if err != nil || prefix.Bits() != 0 {
					continue
				}
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Field:    fmt.Sprintf("servers[%d].client_address.trusted_proxies[%d]", i, j),
					Message:  fmt.Sprintf("%q trusts every client, so any request may assert its own client address", raw),
					Hint:     "list only the addresses of proxies you operate; trusted_proxies is a security boundary",
				})
			}
			// Believing a header the proxy does not write is the same as believing
			// the client: the proxy forwards whatever the client sent.
			for j, name := range srv.ClientAddress.ForwardedHeaders {
				if name != clientaddr.HeaderForwarded || len(srv.ClientAddress.TrustedProxies) == 0 {
					continue
				}
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Field:    fmt.Sprintf("servers[%d].client_address.forwarded_headers[%d]", i, j),
					Message:  "RFC 7239 Forwarded is believed, but most proxies never write it and pass a client-supplied value through unchanged",
					Hint:     fmt.Sprintf("keep it only if your proxy overwrites Forwarded on every request; otherwise list %q alone", clientaddr.HeaderXFF),
				})
			}
		}
	}

	diags = append(diags, lintBackendTLS(c)...)
	diags = append(diags, lintDiscoveryTrust(c)...)
	diags = append(diags, listenerScopedDiagnostics(c)...)

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
	// Warn when RBAC principal tokens are literal values (SEC-1). Principal
	// tokens are high-entropy secrets and should be sourced from the environment
	// or a file to avoid committing them to the config file.
	for i, p := range c.Admin.RBAC.Principals {
		if p.Token != "" && !containsSecretRef(p.Token) {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("[admin.rbac.principals[%d]].token", i),
				Message:  fmt.Sprintf("principal %q token is a literal value in the config file", p.Name),
				Hint:     `reference a secret instead, e.g. token = "${env:JUL_ALICE_TOKEN}" or "${file:/run/secrets/alice-token}"`,
			})
		}
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

// lintBackendTLS reports outbound-trust findings.
//
// insecure_skip_verify is a SeverityError rather than a warning, so `jul lint`
// exits 1 even without -strict: the field turns a verified backend connection
// into an unverified one, and a deployment should not reach production with it
// by accident. Validate still accepts it, because a field that exists to opt
// into an insecure mode cannot be a validation rejection (ADR 0016 §8).
func lintBackendTLS(c *Config) []Diagnostic {
	var diags []Diagnostic

	insecure := func(field string, cfg *BackendTLSConfig) {
		if cfg == nil || !cfg.InsecureSkipVerify {
			return
		}
		diags = append(diags, Diagnostic{
			Severity: SeverityError,
			Field:    field,
			Message:  "backend certificate verification is disabled; the connection is encrypted but the peer is not authenticated",
			Hint:     "remove insecure_skip_verify and configure ca_file with ca_mode, or set server_name to the name the certificate actually carries",
		})
	}

	tlsUpstreams := upstreamsReachedOverTLS(c)
	for i := range c.Upstreams {
		up := &c.Upstreams[i]
		where := fmt.Sprintf("upstreams[%d].backend_tls", i)
		insecure(where, up.BackendTLS)

		// An active health probe inherits the pool's scheme, so an https pool
		// is probed over TLS and verified against the system roots alone. A
		// private-CA backend therefore needs a policy — and will need one
		// before the health path adopts the same trust as live traffic.
		if up.HealthCheck != nil && up.HealthCheck.Enabled && tlsUpstreams[up.Name] && up.BackendTLS == nil {
			probeType := strings.ToLower(strings.TrimSpace(up.HealthCheck.Type))
			if probeType == "" || probeType == "http" {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Field:    fmt.Sprintf("upstreams[%d].health_check", i),
					Message:  "this pool is probed over https with no [upstreams.backend_tls] policy, so probes verify against the system roots only",
					Hint:     "add a backend_tls block with the backend's trust roots; a private-CA backend will otherwise be reported unhealthy",
				})
			}
		}
	}

	for i := range c.Servers {
		for j := range c.Servers[i].Locations {
			loc := &c.Servers[i].Locations[j]
			where := fmt.Sprintf("servers[%d].locations[%d].backend_tls", i, j)
			insecure(where, loc.BackendTLS)

			// Both blocks may legitimately exist; the location's wins for this
			// route. Say so, because a silent override of a security policy is
			// exactly the kind of thing an operator should be told about.
			if loc.BackendTLS == nil {
				continue
			}
			if name, ok := upstreamRefOf(loc.ProxyPass); ok && upstreamHasBackendTLS(c, name) {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Field:    where,
					Message:  fmt.Sprintf("this location and upstream %q both define backend_tls; the location's policy applies to this route", name),
					Hint:     "remove one of the two blocks unless the override is intended",
				})
			}
		}
	}
	return diags
}

// lintDiscoveryTrust reports control-plane trust findings (Boundary F).
//
// A discovery provider is the authority a backend address comes from, so the
// safety of Boundary D rests on it: a poisoned registry answer is only harmless
// because the address it supplies never becomes an identity. Disabling
// verification on that channel therefore gets the same SeverityError treatment
// as disabling it on a backend (ADR 0016 §14), rather than the silence it had
// while it lived outside the model.
func lintDiscoveryTrust(c *Config) []Diagnostic {
	var diags []Diagnostic
	for i := range c.Upstreams {
		d := c.Upstreams[i].Discovery
		if d == nil {
			continue
		}
		where := fmt.Sprintf("upstreams[%d].discovery", i)
		if k := d.Kubernetes; k != nil && k.InsecureSkipTLSVerify {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Field:    where + ".kubernetes.insecure_skip_tls_verify",
				Message:  "API server certificate verification is disabled; any host that can answer as the API server can choose this pool's backend addresses",
				Hint:     "remove it and set ca_file, or rely on the mounted service-account CA when running in-cluster",
			})
		}
		cs := d.Consul
		if cs == nil {
			continue
		}
		if cs.TLS != nil && cs.TLS.InsecureSkipVerify {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Field:    where + ".consul.tls.insecure_skip_verify",
				Message:  "Consul agent certificate verification is disabled; any host that can answer as the agent can choose this pool's backend addresses",
				Hint:     "remove it and set ca_file with ca_mode, or set server_name to the name the certificate actually carries",
			})
		}
		// The ACL token is a bearer credential: over plaintext it is readable by
		// anything on the path, and replayable against the agent.
		addr := strings.TrimSpace(cs.Address)
		if strings.TrimSpace(cs.Token) != "" && (addr == "" || strings.HasPrefix(strings.ToLower(addr), "http://")) {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    where + ".consul.token",
				Message:  "an ACL token is sent over plaintext HTTP, so it is readable and replayable by anything on the network path",
				Hint:     "use an https address for consul.address, with [upstreams.discovery.consul.tls] if the agent uses a private CA",
			})
		}
	}
	return diags
}

// upstreamsReachedOverTLS returns the names of pools referenced by at least one
// https (or TLS transcoding) route.
func upstreamsReachedOverTLS(c *Config) map[string]bool {
	out := map[string]bool{}
	for i := range c.Servers {
		for j := range c.Servers[i].Locations {
			loc := &c.Servers[i].Locations[j]
			if loc.GRPCTranscode != nil && loc.GRPCTranscode.TLS {
				out[loc.GRPCTranscode.Target] = true
			}
			if name, ok := upstreamRefOf(loc.ProxyPass); ok && locationUsesTLSBackend(*loc) {
				out[name] = true
			}
		}
	}
	return out
}

// upstreamRefOf returns the upstream name a proxy_pass refers to.
func upstreamRefOf(proxyPass string) (string, bool) {
	if strings.TrimSpace(proxyPass) == "" {
		return "", false
	}
	u, err := url.Parse(proxyPass)
	if err != nil || u.Host == "" {
		return "", false
	}
	return u.Host, true
}

func upstreamHasBackendTLS(c *Config, name string) bool {
	for i := range c.Upstreams {
		if c.Upstreams[i].Name == name && c.Upstreams[i].BackendTLS != nil {
			return true
		}
	}
	return false
}

// listenerScopedDiagnostics reports server blocks that share a listen address
// but declare different values for a field the listener resolves once.
//
// These fields are read from a single block when the socket binds, so a
// divergent value in a sibling block is silently discarded — the configuration
// parses, validates and lints clean while describing behaviour the server does
// not have. Validation already rejects the cross-server inconsistencies whose
// consequence was understood (TLS mixed with plaintext, ACME mixed with static
// certificates, divergent ACME issuers, and — as a security boundary — a
// divergent client_address). These fields have the same property and were
// simply never covered.
//
// It is a warning, not an error: the first-wins behaviour is pre-existing and
// some configurations depend on it working exactly as it does. Promotion to an
// error can follow evidence.
//
// client_max_body_size is deliberately absent: the router applies the *matched*
// virtual host's limit per request, so two blocks on one listener may
// legitimately differ. Being listed under [[servers]] does not make a field
// listener-scoped.
func listenerScopedDiagnostics(c *Config) []Diagnostic {
	type declaration struct {
		index int
		value string
	}
	// first[addr][field] is the block that wins for that address.
	first := map[string]map[string]declaration{}
	var diags []Diagnostic

	for i := range c.Servers {
		srv := &c.Servers[i]
		addr := strings.TrimSpace(srv.Listen)
		if addr == "" {
			continue
		}
		byField, ok := first[addr]
		if !ok {
			byField = map[string]declaration{}
			first[addr] = byField
		}
		for _, f := range listenerScopedFields(srv) {
			if f.value == "" || f.value == f.defaultValue {
				// Omitted, or indistinguishable from omitted: Parse applies
				// defaults before Lint sees the configuration, so a block that
				// never mentioned the field carries the default value. Treating
				// "equals the default" as "no opinion" costs one under-warned
				// case — a block that spells the default out while a sibling
				// sets something else — and avoids warning about fields the
				// operator never wrote, which would be far worse.
				continue
			}
			winner, seen := byField[f.name]
			if !seen {
				byField[f.name] = declaration{index: i, value: f.value}
				continue
			}
			if winner.value == f.value {
				continue
			}
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("servers[%d].%s", i, f.name),
				Message: fmt.Sprintf("ignored; listen %q already takes %s from servers[%d] (%s), so %s here has no effect",
					addr, f.name, winner.index, winner.value, f.value),
				Hint: f.hint,
			})
		}
	}
	return diags
}

// listenerScopedField is one field the listener resolves once per address.
type listenerScopedField struct {
	name  string
	value string // "" when the block leaves it unset
	// defaultValue is what Parse fills in for an omitted field, so the linter
	// can tell "the operator wrote this" from "the loader supplied it".
	defaultValue string
	hint         string
}

// listenerScopedFields returns a block's explicit listener-scoped values.
//
// The set mirrors the paths the lifecycle registry classifies as
// new_listener_only or bind-bound — the fields frozen when a socket binds —
// which is what makes the lint and the lifecycle classification agree about
// what "listener-scoped" means.
func listenerScopedFields(srv *ServerConfig) []listenerScopedField {
	const timeoutHint = "move the value to the first server block on this listen address, or give this block its own address"
	out := []listenerScopedField{
		{name: "read_header_timeout", value: durationValue(srv.ReadHeaderTimeout), defaultValue: (10 * time.Second).String(), hint: timeoutHint},
		{name: "read_timeout", value: durationValue(srv.ReadTimeout), hint: timeoutHint},
		{name: "write_timeout", value: durationValue(srv.WriteTimeout), hint: timeoutHint},
		{name: "idle_timeout", value: durationValue(srv.IdleTimeout), defaultValue: (60 * time.Second).String(), hint: timeoutHint},
		{name: "max_header_bytes", value: sizeValue(srv.MaxHeaderBytes), defaultValue: strconv.Itoa(1 << 20), hint: timeoutHint},
	}
	// h2c and http3.enabled are any-wins rather than first-wins: one block
	// enabling either turns it on for the whole address. A block that leaves it
	// off is therefore not overridden so much as overruled, which is worth the
	// same warning for a different reason.
	if srv.H2C {
		out = append(out, listenerScopedField{
			name:  "h2c",
			value: "true",
			hint:  "h2c applies to the whole listen address; a sibling block cannot opt out of it",
		})
	}
	if srv.HTTP3 != nil && srv.HTTP3.Enabled {
		out = append(out, listenerScopedField{
			name:  "http3.enabled",
			value: "true",
			hint:  "HTTP/3 applies to the whole listen address; a sibling block cannot opt out of it",
		})
		if srv.HTTP3.AltSvcMaxAge > 0 {
			out = append(out, listenerScopedField{
				name:         "http3.alt_svc_max_age",
				value:        strconv.Itoa(srv.HTTP3.AltSvcMaxAge),
				defaultValue: strconv.Itoa(defaultAltSvcMaxAge),
				hint:         "the Alt-Svc max-age is taken from the first HTTP/3-enabled block on this listen address",
			})
		}
	}
	return out
}

// durationValue renders a configured duration, or "" when it is unset.
func durationValue(d Duration) string {
	if d <= 0 {
		return ""
	}
	return d.Std().String()
}

// sizeValue renders a configured size, or "" when it is unset.
func sizeValue(s Size) string {
	if s <= 0 {
		return ""
	}
	return strconv.FormatInt(s.Bytes(), 10)
}
