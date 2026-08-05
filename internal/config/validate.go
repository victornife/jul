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
	"strings"
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
		errs = append(errs, validateHealthCheck(up.HealthCheck, where+".health_check")...)
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
			if err := validateMatch(loc.Match, locWhere); err != nil {
				errs = append(errs, err)
			}
			if loc.RequireClientCert && (srv.TLS == nil || !srv.TLS.ClientAuth.Active()) {
				errs = append(errs, fmt.Errorf("%s: require_client_cert needs the server's tls.client_auth enabled (mode request or require)", locWhere))
			}
			if loc.WAF != nil {
				errs = append(errs, validateWAF(*loc.WAF, locWhere+".waf")...)
			}
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
	}

	for addr := range tlsByAddr {
		if plainByAddr[addr] {
			errs = append(errs, fmt.Errorf("listen %q is used by both TLS and non-TLS server blocks; they cannot share a listener", addr))
		}
		if acmeByAddr[addr] && staticTLSByAddr[addr] {
			errs = append(errs, fmt.Errorf("listen %q mixes ACME and static TLS server blocks; a listener uses one certificate provider", addr))
		}
	}

	errs = append(errs, validateACMEConsistency(c.Servers)...)

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
	}

	errs = append(errs, validateCompression(c.Compression)...)
	errs = append(errs, validateRateLimit(c.RateLimit, "[rate_limit]", false)...)
	errs = append(errs, validateWAF(c.WAF, "[waf]")...)
	errs = append(errs, validateTracing(c.Observability.Tracing)...)
	errs = append(errs, validateAccessLog(c.Observability.AccessLog)...)
	errs = append(errs, validateStreams(c.Streams, upstreamNames)...)

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
	}
	if a.ForwardAuth != nil {
		if u, err := url.Parse(a.ForwardAuth.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s.forward_auth: url %q must be an http(s) URL", where, a.ForwardAuth.URL))
		}
	}
	return errs
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
