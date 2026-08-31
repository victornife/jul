// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"jul/internal/clientaddr"
)

// Validate checks a Config for correctness and rejects features reserved for
// future versions. It returns a joined error describing every problem found so
// that a single run surfaces all issues rather than just the first.
func Validate(c *Config) error {
	var errs []error

	// Validate WASM plugin declarations and index them for reference checks
	// below. Whether the "wasmplugins" build tag is actually compiled in is
	// reported at plugin-set build time, mirroring compression/grpc/otel.
	errs = append(errs, validatePlugins(c.Plugins)...)

	if len(c.Servers) == 0 {
		errs = append(errs, errors.New("at least one [[servers]] block is required"))
	}

	errs = append(errs, validateGlobalValues(c.Global)...)

	errs = append(errs, validateEgress(c.Egress)...)

	// Index upstream names for proxy_pass reference checks and detect dups.
	upstreamNames := map[string]int{}
	for i, up := range c.Upstreams {
		where := fmt.Sprintf("upstreams[%d]", i)
		if strings.TrimSpace(up.Name) == "" {
			errs = append(errs, fmt.Errorf("%s: upstream 'name' is required", where))
		} else {
			upstreamNames[up.Name]++
			if upstreamNames[up.Name] == 2 {
				errs = append(errs, fmt.Errorf("duplicate upstream name %q", up.Name))
			}
		}
		switch up.Strategy {
		case "", "round_robin", "weighted_round_robin", "least_conn":
		default:
			errs = append(errs, fmt.Errorf("%s: invalid strategy %q (want round_robin|weighted_round_robin|least_conn)", where, up.Strategy))
		}
		if len(up.Servers) == 0 && !discoveryEnabled(up.Discovery) {
			errs = append(errs, fmt.Errorf("%s: upstream %q has no servers", where, up.Name))
		}
		for j, s := range up.Servers {
			if strings.TrimSpace(s.Address) == "" {
				errs = append(errs, fmt.Errorf("%s.servers[%d]: address is required", where, j))
			}
		}
		errs = append(errs, validateUpstreamValues(up, where)...)
		errs = append(errs, validateBackendTLS(up.BackendTLS, where+".backend_tls")...)
		errs = append(errs, validateResilience(up.Resilience, where+".resilience", c.Global.ShutdownTimeout.Std())...)
		errs = append(errs, validateCircuitThresholds(up, where)...)
		errs = append(errs, validateHealthCheck(up.HealthCheck, where+".health_check")...)
		errs = append(errs, validateUnixBackends(up, where)...)
		errs = append(errs, validateDiscovery(up.Discovery, where+".discovery")...)
	}

	// Detect listen addresses used by both a TLS and a non-TLS server block,
	// which cannot share one listener. Separately track ACME vs static TLS per
	// address: a single listener uses one certificate provider, so it may not
	// mix automatic (ACME) and static certificates in v1.
	tlsByAddr := map[string]bool{}
	plainByAddr := map[string]bool{}
	acmeByAddr := map[string]bool{}
	staticTLSByAddr := map[string]bool{}

	// routeIDLocations tracks every location declaring a route_id so a
	// duplicate can be reported naming every conflicting location, not just
	// the second one seen (ADR 0019 §4: "duplicate is a validation error
	// naming both locations").
	routeIDLocations := map[string][]string{}

	for i, srv := range c.Servers {
		where := fmt.Sprintf("servers[%d]", i)
		errs = append(errs, validateServerValues(srv, where)...)
		if strings.TrimSpace(srv.Listen) == "" {
			errs = append(errs, fmt.Errorf("%s: 'listen' is required", where))
		} else {
			if srv.TLS != nil && srv.TLS.Enabled {
				tlsByAddr[srv.Listen] = true
				if srv.TLS.ACME != nil && srv.TLS.ACME.Enabled {
					acmeByAddr[srv.Listen] = true
				} else {
					staticTLSByAddr[srv.Listen] = true
				}
			} else {
				plainByAddr[srv.Listen] = true
			}
		}

		for j, loc := range srv.Locations {
			locWhere := fmt.Sprintf("%s.locations[%d]", where, j)
			errs = append(errs, validateMatch(loc.Match, srv, locWhere)...)
			errs = append(errs, validateRouteID(loc.RouteID, locWhere)...)
			if loc.RouteID != nil {
				routeIDLocations[*loc.RouteID] = append(routeIDLocations[*loc.RouteID], locWhere)
			}
			if loc.RequireClientCert && (srv.TLS == nil || !srv.TLS.ClientAuth.Active()) {
				errs = append(errs, fmt.Errorf("%s: require_client_cert needs the server's tls.client_auth enabled (mode request or require)", locWhere))
			}
			if loc.WAF != nil {
				errs = append(errs, validateWAF(*loc.WAF, locWhere+".waf")...)
			}
			errs = append(errs, validateBackendTLS(loc.BackendTLS, locWhere+".backend_tls")...)
			if loc.BackendTLS != nil && !locationUsesTLSBackend(loc) {
				// A policy that could never apply is a misconfiguration, not a
				// harmless extra: the operator believes the backend is verified.
				errs = append(errs, fmt.Errorf("%s.backend_tls: the backend is not reached over TLS; use an https:// proxy_pass, or grpc_transcode.tls = true", locWhere))
			}
			errs = append(errs, validateLocationResilience(loc.Resilience, loc.ProxyRetries, locWhere+".resilience")...)
			errs = append(errs, validateResponsePolicy(loc, locWhere)...)
			errs = append(errs, validateLocation(loc, locWhere, upstreamNames, c.Plugins)...)
		}

		// Server-level middleware plugins must name middleware plugins.
		for k, name := range srv.Plugins {
			errs = append(errs, validatePluginRef(c.Plugins, name, "middleware", fmt.Sprintf("%s.plugins[%d]", where, k))...)
		}

		if srv.TLS != nil && srv.TLS.Enabled {
			acmeOn := srv.TLS.ACME != nil && srv.TLS.ACME.Enabled
			if acmeOn {
				// With ACME the cert/key are obtained automatically, so static
				// paths must not be set; a stray cert/key signals a mistake.
				if strings.TrimSpace(srv.TLS.Cert) != "" || strings.TrimSpace(srv.TLS.Key) != "" {
					errs = append(errs, fmt.Errorf("%s: tls.acme is enabled, so 'tls.cert'/'tls.key' must not be set", where))
				}
				errs = append(errs, validateACME(srv.TLS.ACME, srv.ServerNames, where+".tls.acme")...)
			} else {
				if strings.TrimSpace(srv.TLS.Cert) == "" {
					errs = append(errs, fmt.Errorf("%s: tls enabled but 'tls.cert' is empty", where))
				}
				if strings.TrimSpace(srv.TLS.Key) == "" {
					errs = append(errs, fmt.Errorf("%s: tls enabled but 'tls.key' is empty", where))
				}
			}
			if v := strings.TrimSpace(srv.TLS.MinVersion); v != "" && v != "1.2" && v != "1.3" {
				errs = append(errs, fmt.Errorf("%s: invalid tls.min_version %q (want 1.2 or 1.3)", where, v))
			}
			errs = append(errs, validateClientAuth(srv.TLS.ClientAuth, where+".tls.client_auth")...)
		}
		if srv.TLS != nil && srv.TLS.ClientAuth.Active() && !srv.TLS.Enabled {
			errs = append(errs, fmt.Errorf("%s: tls.client_auth requires tls.enabled = true", where))
		}
		errs = append(errs, validateHTTP3(srv.HTTP3, srv.TLS, where)...)
		errs = append(errs, validateClientAddress(srv.ClientAddress, where+".client_address")...)
		errs = append(errs, validateProxyProtocol(srv, where)...)
	}

	for addr := range tlsByAddr {
		if plainByAddr[addr] {
			errs = append(errs, fmt.Errorf("listen %q is used by both TLS and non-TLS server blocks; they cannot share a listener", addr))
		}
		if acmeByAddr[addr] && staticTLSByAddr[addr] {
			errs = append(errs, fmt.Errorf("listen %q mixes ACME and static TLS server blocks; a listener uses one certificate provider", addr))
		}
	}

	// route_id is unique across the whole document, not merely within one
	// server block, so it is checked once here rather than per-location.
	duplicateIDs := make([]string, 0, len(routeIDLocations))
	for id := range routeIDLocations {
		duplicateIDs = append(duplicateIDs, id)
	}
	sort.Strings(duplicateIDs)
	for _, id := range duplicateIDs {
		locs := routeIDLocations[id]
		if len(locs) > 1 {
			errs = append(errs, fmt.Errorf("duplicate route_id %q at %s", id, strings.Join(locs, " and ")))
		}
	}

	errs = append(errs, validateACMEConsistency(c.Servers)...)
	errs = append(errs, validateClientAddressConsistency(c.Servers)...)
	errs = append(errs, validateProxyProtocolConsistency(c.Servers)...)

	errs = append(errs, validateAdminValues(c.Admin)...)
	errs = append(errs, validateCacheValues(c.Cache)...)

	if c.Admin.Enabled {
		if strings.TrimSpace(c.Admin.Listen) == "" {
			errs = append(errs, errors.New("[admin] enabled but 'listen' is empty"))
		}
		if c.Admin.PluginUploadEnabled != nil && !*c.Admin.PluginUploadEnabled {
			// upload explicitly disabled; skip max-size validation entirely.
		} else if c.Admin.PluginUploadMaxSize <= 0 {
			errs = append(errs, errors.New("[admin] 'plugin_upload_max_size' must be positive when upload is enabled"))
		}
		errs = append(errs, validateRBAC(c.Admin.RBAC, c.Admin.Token)...)
		errs = append(errs, validateAdminTLS(c.Admin.TLS)...)
	}

	errs = append(errs, validateCompression(c.Compression)...)
	errs = append(errs, validateRateLimit(c.RateLimit, "[rate_limit]", false)...)
	errs = append(errs, validateWAF(c.WAF, "[waf]")...)
	errs = append(errs, validateTracing(c.Observability.Tracing)...)
	errs = append(errs, validateAccessLog(c.Observability.AccessLog)...)
	errs = append(errs, validateStreams(c.Streams, upstreamNames)...)
	errs = append(errs, validateStreamResilience(c)...)

	return errors.Join(errs...)
}

// validateStreams and validateStreamTarget live in validate_backends.go.

// validateCompression checks the [compression] block. It validates structure
// only: the encoders allow-list, level range, and MIME types. Whether "br" and
// "zstd" are actually compiled in is reported at middleware construction with a
// "not compiled in this build" error, since that depends on build tags.
func validateCompression(c CompressionConfig) []error {
	var errs []error
	if err := validateNonNegativeSize("[compression].min_size", c.MinSize); err != nil {
		errs = append(errs, err)
	}
	if !c.IsEnabled() {
		return errs
	}
	if len(c.Encoders) == 0 {
		errs = append(errs, errors.New("[compression] enabled but no encoders are configured"))
	}
	for _, e := range c.Encoders {
		switch e {
		case "gzip", "br", "zstd":
		default:
			errs = append(errs, fmt.Errorf("[compression] invalid encoder %q (want gzip|br|zstd)", e))
		}
	}
	if c.Level < 0 || c.Level > 11 {
		errs = append(errs, fmt.Errorf("[compression] level %d out of range (0 selects the encoder default; max 11)", c.Level))
	}
	if len(c.Types) == 0 {
		errs = append(errs, errors.New("[compression] enabled but no MIME types are configured"))
	}
	return errs
}

// validateTracing checks the [observability.tracing] block. It validates the
// configuration only; whether the `otel` build tag is compiled in is reported
// when the tracer is constructed at startup, since that depends on build tags.
func validateTracing(c TracingConfig) []error {
	if !c.Enabled {
		return nil
	}
	var errs []error
	switch c.Exporter {
	case "", "otlp-grpc", "otlp-http":
	default:
		errs = append(errs, fmt.Errorf("[observability.tracing] invalid exporter %q (want otlp-grpc|otlp-http)", c.Exporter))
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		errs = append(errs, errors.New("[observability.tracing] enabled but 'endpoint' is empty"))
	}
	if c.SampleRatio < 0 || c.SampleRatio > 1 {
		errs = append(errs, fmt.Errorf("[observability.tracing] sample_ratio %g out of range (want 0..1)", c.SampleRatio))
	}
	return errs
}

// validateAccessLog checks the [observability.access_log] block. It validates
// the configuration only; whether a given sink can actually be opened (for
// example syslog, which is unavailable on Windows) is reported when the sinks
// are constructed at startup, since that depends on the platform.
func validateAccessLog(c AccessLogConfig) []error {
	var errs []error
	if c.IsEnabled() && c.Sinks != nil && len(c.Sinks) == 0 {
		errs = append(errs, errors.New("[observability.access_log] enabled=true requires at least one sink; omit sinks for the stdout default or set enabled=false"))
	}
	hasFile := false
	seen := make(map[string]struct{}, len(c.Sinks))
	for _, s := range c.Sinks {
		if _, ok := seen[s]; ok {
			errs = append(errs, fmt.Errorf("[observability.access_log] duplicate sink %q", s))
			continue
		}
		seen[s] = struct{}{}
		switch s {
		case "stdout", "syslog":
		case "file":
			hasFile = true
		default:
			errs = append(errs, fmt.Errorf("[observability.access_log] unknown sink %q (want stdout|file|syslog)", s))
		}
	}
	// Dormant settings are still validated so re-enabling cannot activate a
	// configuration that was never safe. Resource opening itself is skipped by
	// preflight/runtime while disabled.
	if hasFile && strings.TrimSpace(c.File) == "" {
		errs = append(errs, errors.New("[observability.access_log] sink \"file\" requires 'file' (path)"))
	}
	switch c.Format {
	case "", "text", "json":
	default:
		errs = append(errs, fmt.Errorf("[observability.access_log] invalid format %q (want text|json)", c.Format))
	}
	if c.RotateMaxMB < 0 {
		errs = append(errs, fmt.Errorf("[observability.access_log] rotate_max_mb %d must be >= 0", c.RotateMaxMB))
	}
	if c.RotateKeep < 0 {
		errs = append(errs, fmt.Errorf("[observability.access_log] rotate_keep %d must be >= 0", c.RotateKeep))
	}
	return errs
}

// validateHTTP3 checks a server block's HTTP/3 settings. HTTP/3 runs over QUIC,
// which mandates TLS, so an enabled block requires TLS to be enabled on the same
// server block (static cert/key or ACME). where labels the enclosing server. It
// is a no-op when HTTP/3 is absent or disabled. Whether the binary can actually
// serve HTTP/3 (the http3 build tag) is checked at startup, not here, so a
// configuration targeting an http3 build still validates in any build.
func validateHTTP3(h *HTTP3Config, tlsCfg *TLSConfig, where string) []error {
	if h == nil || !h.Enabled {
		return nil
	}
	var errs []error
	if tlsCfg == nil || !tlsCfg.Enabled {
		errs = append(errs, fmt.Errorf("%s: http3 requires TLS on the same server block (HTTP/3 runs over QUIC, which mandates TLS)", where))
	}
	if h.AltSvcMaxAge < 0 {
		errs = append(errs, fmt.Errorf("%s: http3.alt_svc_max_age %d must be >= 0", where, h.AltSvcMaxAge))
	}
	return errs
}

// validateRateLimit checks a rate-limit policy. where labels the error source;
// perLocation rejects fields that only make sense at global (listener) scope.
// It assumes defaults have been applied (key "ip", burst = rate when omitted).
func validateRateLimit(c RateLimitConfig, where string, perLocation bool) []error {
	if !c.Enabled {
		return nil
	}
	var errs []error
	if c.Rate <= 0 {
		errs = append(errs, fmt.Errorf("%s enabled but rate must be > 0", where))
	}
	if c.Burst < c.Rate {
		errs = append(errs, fmt.Errorf("%s burst (%d) must be >= rate (%d)", where, c.Burst, c.Rate))
	}
	if !ValidRateKey(c.Key) {
		errs = append(errs, fmt.Errorf("%s invalid key %q (want ip | header:<Name> | jwt:<claim>)", where, c.Key))
	}
	if perLocation && c.MaxConns != 0 {
		errs = append(errs, fmt.Errorf("%s max_conns is listener-global and not allowed on a location override", where))
	} else if c.MaxConns < 0 {
		errs = append(errs, fmt.Errorf("%s max_conns must be >= 0", where))
	}
	return errs
}

// validateWAF checks a WAF policy (global or per-location override). It assumes
// defaults have been applied (mode "block", block status 403, body limit). It
// validates the keyword fields and the embedded-CRS paranoia range; the SecLang
// rules themselves are validated by the WAF engine at build time. where labels
// the error source.
func validateWAF(c WAFConfig, where string) []error {
	if !c.Enabled {
		return nil
	}
	var errs []error
	if c.Mode != "block" && c.Mode != "detect" {
		errs = append(errs, fmt.Errorf("%s invalid mode %q (want block or detect)", where, c.Mode))
	}
	if c.BlockStatus < 100 || c.BlockStatus > 599 {
		errs = append(errs, fmt.Errorf("%s block_status must be a valid HTTP status (100–599), got %d", where, c.BlockStatus))
	}
	if c.Paranoia < 0 || c.Paranoia > 4 {
		errs = append(errs, fmt.Errorf("%s paranoia must be between 1 and 4, got %d", where, c.Paranoia))
	}
	if c.Paranoia != 0 && !c.CRSEnabled {
		errs = append(errs, fmt.Errorf("%s paranoia applies only when crs_enabled = true", where))
	}
	if !c.CRSEnabled && len(c.DirectivesFiles) == 0 && strings.TrimSpace(c.InlineRules) == "" {
		errs = append(errs, fmt.Errorf("%s enabled but has no rules; set crs_enabled, directives_files, or inline_rules", where))
	}
	// Validate inline_rules for dangerous directives that would subvert WAF policy.
	if ir := strings.TrimSpace(c.InlineRules); ir != "" {
		for i, line := range strings.Split(ir, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			upper := strings.ToUpper(line)
			// SecRuleEngine from inline_rules would silently override the configured mode.
			// SecDefaultAction would conflict with the block_status default or CRS setup.
			if strings.HasPrefix(upper, "SECRULEENGINE") {
				errs = append(errs, fmt.Errorf("%s inline_rules line %d: SecRuleEngine is not allowed (use mode to control engine state)", where, i+1))
			}
			if strings.HasPrefix(upper, "SECDEFAULTACTION") {
				errs = append(errs, fmt.Errorf("%s inline_rules line %d: SecDefaultAction is not allowed (use block_status or crs_enabled to control defaults)", where, i+1))
			}
		}
	}

	if c.RequestBodyLimit < 0 {
		errs = append(errs, fmt.Errorf("%s request_body_limit must be >= 0", where))
	}
	return errs
}

// ValidRateKey reports whether a rate-limit key spec is well-formed.
func ValidRateKey(k string) bool {
	switch {
	case k == "ip":
		return true
	case strings.HasPrefix(k, "header:") && len(k) > len("header:"):
		return true
	case strings.HasPrefix(k, "jwt:") && len(k) > len("jwt:"):
		return true
	}
	return false
}

// validateAuth checks a per-location auth block. It assumes defaults have been
// applied (Basic realm, JWT algorithms). where labels the error source.
func validateAuth(a *AuthConfig, where string) []error {
	var errs []error
	for _, cidr := range a.Allow {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid allow CIDR %q: %v", where, cidr, err))
		}
	}
	for _, cidr := range a.Deny {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid deny CIDR %q: %v", where, cidr, err))
		}
	}
	// At most one credential method may be configured per location so the
	// decision path is unambiguous.
	methods := 0
	if a.Basic != nil {
		methods++
	}
	if a.JWT != nil {
		methods++
	}
	if a.ForwardAuth != nil {
		methods++
	}
	if methods > 1 {
		errs = append(errs, fmt.Errorf("%s: at most one of basic, jwt, forward_auth may be set", where))
	}
	// Reject an auth block that enforces nothing: with no CIDR allow/deny gate
	// and no credential method, the authenticator falls through and permits
	// every request, so an enabled-looking "auth = {}" would silently allow
	// traffic while the Console reports the location as protected.
	if methods == 0 && len(a.Allow) == 0 && len(a.Deny) == 0 {
		errs = append(errs, fmt.Errorf("%s: auth is configured but enforces nothing; set a CIDR allow/deny list or one of basic, jwt, forward_auth", where))
	}
	if a.Basic != nil && strings.TrimSpace(a.Basic.File) == "" {
		errs = append(errs, fmt.Errorf("%s.basic: 'file' is required", where))
	}
	if a.JWT != nil {
		if u, err := url.Parse(a.JWT.JWKSURL); err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s.jwt: jwks_url %q must be an https URL", where, a.JWT.JWKSURL))
		}
		for _, alg := range a.JWT.Algorithms {
			if !validJWTAlg(alg) {
				errs = append(errs, fmt.Errorf("%s.jwt: unsupported or insecure algorithm %q", where, alg))
			}
		}
		errs = append(errs, validateAuthTimeout(a.JWT.Timeout, where+".jwt")...)
	}
	if a.ForwardAuth != nil {
		if u, err := url.Parse(a.ForwardAuth.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s.forward_auth: url %q must be an http(s) URL", where, a.ForwardAuth.URL))
		}
		errs = append(errs, validateAuthTimeout(a.ForwardAuth.Timeout, where+".forward_auth")...)
	}
	return errs
}

// authDependencyTimeoutCeiling bounds how long an auth dependency may hold a
// client request open before the request is denied. It is generous, because its
// job is to catch a typo rather than to express an opinion, but it is finite:
// an unbounded auth call is a request that never resolves either way.
const authDependencyTimeoutCeiling = 60 * time.Second

func validateAuthTimeout(d Duration, where string) []error {
	v := d.Std()
	if v < 0 || v > authDependencyTimeoutCeiling {
		return []error{fmt.Errorf("%s: timeout must be between 0s and %s", where, authDependencyTimeoutCeiling)}
	}
	return nil
}

// validJWTAlg reports whether alg is an accepted asymmetric signing algorithm.
// The symmetric "none" algorithm is always rejected to prevent token forgery.
func validJWTAlg(alg string) bool {
	switch alg {
	case "RS256", "RS384", "RS512",
		"ES256", "ES384", "ES512",
		"PS256", "PS384", "PS512":
		return true
	}
	return false
}

// validateHealthCheck, discoveryEnabled, and validateDiscovery live in
// validate_backends.go.

// validateACME checks an enabled ACME block. serverNames is the enclosing
// server's server_names, used to confirm there is at least one domain to
// request a certificate for (Domains defaults to server_names). where labels
// the error source. It assumes defaults have been applied.
func validateACME(a *ACMEConfig, serverNames []string, where string) []error {
	if a == nil || !a.Enabled {
		return nil
	}
	var errs []error
	if strings.TrimSpace(a.Email) == "" {
		errs = append(errs, fmt.Errorf("%s: 'email' is required for ACME account registration", where))
	}
	switch a.CA {
	case "letsencrypt", "letsencrypt-staging":
	default:
		if u, err := url.Parse(a.CA); err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s: invalid ca %q (want letsencrypt | letsencrypt-staging | https://<directory-url>)", where, a.CA))
		}
	}
	switch a.Challenge {
	case "http-01", "tls-alpn-01":
	case "dns-01":
		errs = append(errs, fmt.Errorf("%s: challenge \"dns-01\" is reserved for a future release and not implemented; use http-01 or tls-alpn-01", where))
	default:
		errs = append(errs, fmt.Errorf("%s: invalid challenge %q (want http-01 | tls-alpn-01)", where, a.Challenge))
	}
	if strings.TrimSpace(a.DNSProvider) != "" {
		errs = append(errs, fmt.Errorf("%s: dns_provider is reserved for a future DNS-01 release and not implemented; remove it", where))
	}
	if len(a.Domains) == 0 && len(serverNames) == 0 {
		errs = append(errs, fmt.Errorf("%s: no domains to certify (set tls.acme.domains or the server's server_names)", where))
	}
	return errs
}

// validateACMEConsistency rejects divergent issuer settings across ACME-enabled
// server blocks. A single autocert manager is built once at startup and shared
// by every ACME block, so its email, CA, challenge, cache directory and OCSP
// stapling are taken from the first enabled block; conflicting values in later
// blocks would be silently ignored. Requiring them to match makes the runtime
// behaviour predictable and surfaces typos at config time.
func validateACMEConsistency(servers []ServerConfig) []error {
	var errs []error
	var ref *ACMEConfig
	var refWhere string
	for i := range servers {
		srv := &servers[i]
		if srv.TLS == nil || !srv.TLS.Enabled || srv.TLS.ACME == nil || !srv.TLS.ACME.Enabled {
			continue
		}
		a := srv.TLS.ACME
		where := fmt.Sprintf("servers[%d].tls.acme", i)
		if ref == nil {
			ref, refWhere = a, where
			continue
		}
		if a.Email != ref.Email {
			errs = append(errs, fmt.Errorf("%s: email %q differs from %s email %q; all ACME server blocks share one issuer and must agree", where, a.Email, refWhere, ref.Email))
		}
		if a.CA != ref.CA {
			errs = append(errs, fmt.Errorf("%s: ca %q differs from %s ca %q; all ACME server blocks share one issuer and must agree", where, a.CA, refWhere, ref.CA))
		}
		if a.Challenge != ref.Challenge {
			errs = append(errs, fmt.Errorf("%s: challenge %q differs from %s challenge %q; all ACME server blocks share one challenge type and must agree", where, a.Challenge, refWhere, ref.Challenge))
		}
		if a.CacheDir != ref.CacheDir {
			errs = append(errs, fmt.Errorf("%s: cache_dir %q differs from %s cache_dir %q; all ACME server blocks share one certificate cache and must agree", where, a.CacheDir, refWhere, ref.CacheDir))
		}
		if a.OCSPStaplingEnabled() != ref.OCSPStaplingEnabled() {
			errs = append(errs, fmt.Errorf("%s: ocsp_stapling differs from %s; all ACME server blocks share one staple setting and must agree", where, refWhere))
		}
	}
	return errs
}

// validateClientAddress checks one [servers.client_address] block. Entries are
// validated with the same parser the runtime policy compiles with, so a
// configuration that validates is a configuration that can be published.
func validateClientAddress(ca *ClientAddressConfig, where string) []error {
	if ca == nil {
		return nil
	}
	var errs []error
	for i, raw := range ca.TrustedProxies {
		if _, err := clientaddr.ParsePrefix(raw); err != nil {
			errs = append(errs, fmt.Errorf("%s.trusted_proxies[%d]: %v", where, i, err))
		}
	}
	seen := map[string]bool{}
	for i, name := range ca.ForwardedHeaders {
		path := fmt.Sprintf("%s.forwarded_headers[%d]", where, i)
		if name == "" {
			errs = append(errs, fmt.Errorf("%s: invalid value %q; expected %s or %s", path, name, clientaddr.HeaderForwarded, clientaddr.HeaderXFF))
			continue
		}
		if err := validateOptionalEnum(path, name, clientaddr.HeaderForwarded, clientaddr.HeaderXFF); err != nil {
			errs = append(errs, err)
			continue
		}
		if seen[name] {
			errs = append(errs, fmt.Errorf("%s: duplicate header %q; each forwarding header may be listed once", path, name))
			continue
		}
		seen[name] = true
	}
	if err := validateNonNegativeInt(where+".max_hops", ca.MaxHops); err != nil {
		errs = append(errs, err)
	} else if ca.MaxHops > clientaddr.MaxHopsLimit {
		errs = append(errs, fmt.Errorf("%s.max_hops: %d must be at most %d (0 selects the default of %d)", where, ca.MaxHops, clientaddr.MaxHopsLimit, clientaddr.DefaultMaxHops))
	}
	// Whether a header can be believed depends on the proxy overwriting it, which
	// Jul cannot observe. Trusting a proxy therefore requires naming the headers
	// it authors rather than inheriting a default.
	if len(ca.TrustedProxies) > 0 && ca.ForwardedHeaders == nil {
		errs = append(errs, fmt.Errorf("%s.forwarded_headers: required when trusted_proxies is set; list the headers your proxy overwrites on every request (%q, %q), or [] to keep peer-only identity",
			where, clientaddr.HeaderXFF, clientaddr.HeaderForwarded))
	}
	return errs
}

// validateProxyProtocol checks the [[servers]] PROXY-protocol setting.
//
// The header supplies Boundary A for the listener, so it is only meaningful
// from a peer permitted to assert an address: the check reuses
// client_address.trusted_proxies rather than introducing a second trust list
// that could disagree with it. HTTP/3 is rejected on the same listener because
// QUIC negotiates TLS inside the transport and carries no plaintext framing to
// prepend a header to; making that a hard error keeps a listener from deriving
// identity two different ways depending on the protocol a client negotiated.
func validateProxyProtocol(srv ServerConfig, where string) []error {
	mode := strings.ToLower(strings.TrimSpace(srv.ProxyProtocol))
	if mode == "" {
		return nil
	}
	var errs []error
	if mode != "in" {
		errs = append(errs, fmt.Errorf("%s.proxy_protocol: invalid value %q; an HTTP listener only ingests a header (%q), emitting one is a backend concern", where, srv.ProxyProtocol, "in"))
		return errs
	}
	if srv.ClientAddress == nil || len(srv.ClientAddress.TrustedProxies) == 0 {
		errs = append(errs, fmt.Errorf("%s.proxy_protocol: requires client_address.trusted_proxies; a PROXY header is an assertion, so the balancers permitted to make it must be named", where))
	}
	if srv.HTTP3 != nil && srv.HTTP3.Enabled {
		errs = append(errs, fmt.Errorf("%s.proxy_protocol: cannot be combined with http3 on one listener; QUIC carries no PROXY framing, so the two protocols would derive the client address differently", where))
	}
	return errs
}

// validateProxyProtocolConsistency rejects divergent proxy_protocol settings
// across server blocks sharing a listen address. The header is consumed by the
// listener before any block is selected, so a listener has exactly one setting.
func validateProxyProtocolConsistency(servers []ServerConfig) []error {
	type ref struct{ where, mode string }
	first := map[string]ref{}
	var errs []error
	for i := range servers {
		addr := CanonicalListenAddr(servers[i].Listen)
		if addr == "" {
			continue
		}
		current := ref{
			where: fmt.Sprintf("servers[%d].proxy_protocol", i),
			mode:  strings.ToLower(strings.TrimSpace(servers[i].ProxyProtocol)),
		}
		prev, ok := first[addr]
		if !ok {
			first[addr] = current
			continue
		}
		if prev.mode != current.mode {
			errs = append(errs, fmt.Errorf("%s: %q differs from %s %q; the PROXY header is consumed by the listener before the Host header selects a server block, so every block sharing listen %q must agree",
				current.where, current.mode, prev.where, prev.mode, addr))
		}
	}
	return errs
}

// CanonicalListenAddr renders a listen address as the key that identifies the
// socket it binds.
//
// It is the single definition of "these two blocks share a listener", used by
// configuration validation, the handler factory and the server's own listener
// set. Those three grouped listeners independently before, and disagreed about
// whitespace — which matters because the listener-scoped security rules, above
// all the identical-`client_address` requirement, are only as strong as the
// grouping they are checked against (ADR 0016 §3).
//
// The address is otherwise left alone: resolving ":443" against "0.0.0.0:443"
// would need the bind-time interface set, and two such blocks collide at bind()
// rather than serving divergent policies.
func CanonicalListenAddr(addr string) string { return strings.TrimSpace(addr) }

// validateClientAddressConsistency rejects divergent client_address policies// across server blocks that share a listen address. The canonical client is
// derived per listen address, before the router reads the Host header, so a
// listener has exactly one policy: allowing blocks to disagree would let the
// configuration claim a stricter policy than the one actually applied. Blocks
// are compared by effective policy, so omitting the block on one sibling while
// another declares real trust is rejected, while spelling out a default that
// the sibling omits is not.
func validateClientAddressConsistency(servers []ServerConfig) []error {
	type policyRef struct{ where, policy string }
	first := map[string]policyRef{}
	var errs []error
	for i := range servers {
		addr := CanonicalListenAddr(servers[i].Listen)
		if addr == "" {
			continue
		}
		current := policyRef{
			where:  fmt.Sprintf("servers[%d].client_address", i),
			policy: canonicalClientAddress(servers[i].ClientAddress),
		}
		ref, ok := first[addr]
		if !ok {
			first[addr] = current
			continue
		}
		if ref.policy != current.policy {
			errs = append(errs, fmt.Errorf("%s: %s differs from %s %s; client identity is derived per listen address before the Host header selects a server block, so every block sharing listen %q must declare the same policy",
				current.where, current.policy, ref.where, ref.policy, addr))
		}
	}
	return errs
}

// canonicalClientAddress renders the effective policy of a client_address block
// as a stable string: prefixes normalized, sorted and deduplicated, defaults
// applied. Entries that fail their own validation are kept verbatim so the
// consistency error stays readable while the entry error is reported too.
func canonicalClientAddress(ca *ClientAddressConfig) string {
	trusted := []string{}
	headers := clientaddr.DefaultForwardedHeaders()
	maxHops := clientaddr.DefaultMaxHops
	if ca != nil {
		seen := map[string]bool{}
		for _, raw := range ca.TrustedProxies {
			entry := strings.TrimSpace(raw)
			if prefix, err := clientaddr.ParsePrefix(raw); err == nil {
				entry = prefix.String()
			}
			if seen[entry] {
				continue
			}
			seen[entry] = true
			trusted = append(trusted, entry)
		}
		sort.Strings(trusted)
		if ca.ForwardedHeaders != nil {
			headers = append([]string{}, ca.ForwardedHeaders...)
		}
		if ca.MaxHops > 0 {
			maxHops = ca.MaxHops
		}
	}
	return fmt.Sprintf("{trusted_proxies=[%s] forwarded_headers=[%s] max_hops=%d}",
		strings.Join(trusted, " "), strings.Join(headers, " "), maxHops)
}

// validateClientAuth checks a tls.client_auth block. The mode must be one of
// none|request|require; request and require require a readable CA bundle file
// to verify presented client certificates against; an optional crl_file, when
// set, must also be a readable file. where labels the error source.
func validateClientAuth(ca *ClientAuthConfig, where string) []error {
	if ca == nil {
		return nil
	}
	var errs []error
	mode := strings.TrimSpace(ca.Mode)
	switch mode {
	case "", "none", "request", "require":
	default:
		errs = append(errs, fmt.Errorf("%s: invalid mode %q (want none | request | require)", where, ca.Mode))
	}
	if mode == "request" || mode == "require" {
		if strings.TrimSpace(ca.CAFile) == "" {
			errs = append(errs, fmt.Errorf("%s: ca_file is required for mode %q", where, mode))
		} else if fi, err := os.Stat(ca.CAFile); err != nil {
			errs = append(errs, fmt.Errorf("%s: ca_file %q is not readable: %v", where, ca.CAFile, err))
		} else if fi.IsDir() {
			errs = append(errs, fmt.Errorf("%s: ca_file %q is a directory, want a PEM file", where, ca.CAFile))
		}
	}
	if f := strings.TrimSpace(ca.CRLFile); f != "" {
		if fi, err := os.Stat(f); err != nil {
			errs = append(errs, fmt.Errorf("%s: crl_file %q is not readable: %v", where, f, err))
		} else if fi.IsDir() {
			errs = append(errs, fmt.Errorf("%s: crl_file %q is a directory, want a PEM/DER file", where, f))
		}
	}
	if fc := strings.ToLower(strings.TrimSpace(ca.ForwardCertificate)); fc != "" && fc != "none" {
		if fc != "leaf" && fc != "chain" {
			errs = append(errs, fmt.Errorf("%s: invalid forward_certificate %q (want none, leaf or chain)", where, ca.ForwardCertificate))
		} else if !ca.Active() {
			errs = append(errs, fmt.Errorf("%s: forward_certificate needs a client certificate to forward; set mode to request or require", where))
		}
	}
	return errs
}

// validateAdminTLS checks the optional [admin.tls] block (#336): enabled/cert/
// key/min_version mirror servers.*.tls, and client_auth reuses
// validateClientAuth directly. Structural transitions (enabled/min_version/
// client_auth) remain restart-required; cert/key content is validated during
// admin preflight. The admin API has no backend to forward a client
// certificate to, so forward_certificate must stay unset/"none" here even
// though the field exists on the shared ClientAuthConfig type.
func validateAdminTLS(t *AdminTLSConfig) []error {
	if t == nil || !t.Enabled {
		return nil
	}
	var errs []error
	if strings.TrimSpace(t.Cert) == "" {
		errs = append(errs, errors.New("[admin.tls] enabled but 'cert' is empty"))
	}
	if strings.TrimSpace(t.Key) == "" {
		errs = append(errs, errors.New("[admin.tls] enabled but 'key' is empty"))
	}
	if v := strings.TrimSpace(t.MinVersion); v != "" && v != "1.2" && v != "1.3" {
		errs = append(errs, fmt.Errorf("[admin.tls]: invalid min_version %q (want 1.2 or 1.3)", t.MinVersion))
	}
	errs = append(errs, validateClientAuth(t.ClientAuth, "admin.tls.client_auth")...)
	if t.ClientAuth != nil {
		if fc := strings.ToLower(strings.TrimSpace(t.ClientAuth.ForwardCertificate)); fc != "" && fc != "none" {
			errs = append(errs, errors.New("admin.tls.client_auth: forward_certificate must be \"none\": the admin API has no backend to forward a client certificate to"))
		}
	}
	return errs
}

// validateEgress checks the optional [egress] outbound allow-list. When enabled
// it must list at least one destination, and every entry must be a CIDR, a bare
// IP, an exact hostname, or a leading-dot suffix (".example.com"). When disabled
// the block is ignored entirely.
func validateEgress(e EgressConfig) []error {
	if !e.Enabled {
		return nil
	}
	var errs []error
	var n int
	for i, raw := range e.Allow {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		n++
		if strings.Contains(entry, "://") {
			errs = append(errs, fmt.Errorf("egress.allow[%d]: %q must be a host, IP, or CIDR, not a URL", i, entry))
			continue
		}
		if _, _, err := net.ParseCIDR(entry); err == nil {
			continue
		}
		if net.ParseIP(entry) != nil {
			continue
		}
		if strings.ContainsAny(entry, "/ \t") {
			errs = append(errs, fmt.Errorf("egress.allow[%d]: invalid entry %q (want a host, IP, or CIDR)", i, entry))
			continue
		}
		if host := strings.TrimPrefix(entry, "."); host == "" || strings.HasPrefix(host, ".") {
			errs = append(errs, fmt.Errorf("egress.allow[%d]: invalid host %q", i, entry))
		}
	}
	if n == 0 {
		errs = append(errs, errors.New("egress: enabled but 'allow' is empty; add at least one host, IP, or CIDR (or set enabled = false)"))
	}
	return errs
}

// validateLocation and the location-referenced validators (validateGRPCTranscode,
// validatePlugins, validatePluginRef, validateProxyPass, validateMatch) live in
// validate_location.go.
