package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// Validate checks a Config for correctness and rejects features reserved for
// future versions. It returns a joined error describing every problem found so
// that a single run surfaces all issues rather than just the first.
func Validate(c *Config) error {
	var errs []error

	// Reject reserved-but-unimplemented features explicitly.
	if len(c.Mail) > 0 {
		errs = append(errs, errors.New("[[mail]] (mail proxy) is reserved for a future version and not supported in v1"))
	}

	// Validate WASM plugin declarations and index them for reference checks
	// below. Whether the "wasmplugins" build tag is actually compiled in is
	// reported at plugin-set build time, mirroring compression/grpc/otel.
	errs = append(errs, validatePlugins(c.Plugins)...)

	if len(c.Servers) == 0 {
		errs = append(errs, errors.New("at least one [[servers]] block is required"))
	}

	if c.Global.RedactMinSecretLength < 0 {
		errs = append(errs, fmt.Errorf("global: redact_min_secret_length must be >= 0, got %d", c.Global.RedactMinSecretLength))
	}

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
		if srv.RedirectHTTPS != 0 && srv.RedirectHTTPS != 301 && srv.RedirectHTTPS != 308 {
			errs = append(errs, fmt.Errorf("%s: redirect_https must be 301 or 308, got %d", where, srv.RedirectHTTPS))
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

	if c.Admin.Enabled {
		if strings.TrimSpace(c.Admin.Listen) == "" {
			errs = append(errs, errors.New("[admin] enabled but 'listen' is empty"))
		}
		if c.Admin.HistoryKeep < 0 {
			errs = append(errs, errors.New("[admin] 'history_keep' must not be negative"))
		}
		if c.Admin.AuditLogRotateMaxMB < 0 {
			errs = append(errs, errors.New("[admin] 'audit_log_rotate_max_mb' must not be negative"))
		}
		if c.Admin.AuditLogRotateKeep < 0 {
			errs = append(errs, errors.New("[admin] 'audit_log_rotate_keep' must not be negative"))
		}
	}

	errs = append(errs, validateCompression(c.Compression)...)
	errs = append(errs, validateRateLimit(c.RateLimit, "[rate_limit]", false)...)
	errs = append(errs, validateWAF(c.WAF, "[waf]")...)
	errs = append(errs, validateTracing(c.Observability.Tracing)...)
	errs = append(errs, validateAccessLog(c.Observability.AccessLog)...)
	errs = append(errs, validateStreams(c.Streams, upstreamNames)...)

	return errors.Join(errs...)
}

// validateStreams checks the [[stream]] L4 proxy listeners. It validates
// structure only (addresses, protocol, target references, mode keywords);
// whether L4 proxying is compiled in is reported at startup by
// internal/stream.Check, which fails a lean build that declares any stream.
func validateStreams(streams []StreamServer, upstreamNames map[string]int) []error {
	var errs []error
	seen := map[string]int{}
	for i, st := range streams {
		where := fmt.Sprintf("stream[%d]", i)
		if strings.TrimSpace(st.Listen) == "" {
			errs = append(errs, fmt.Errorf("%s: 'listen' is required", where))
		}
		proto := strings.ToLower(strings.TrimSpace(st.Protocol))
		switch proto {
		case "", "tcp", "udp":
		default:
			errs = append(errs, fmt.Errorf("%s: invalid protocol %q (want tcp or udp)", where, st.Protocol))
		}
		if proto == "" {
			proto = "tcp"
		}
		// One listener may not be claimed by two stream blocks of the same proto.
		if st.Listen != "" {
			key := proto + "/" + st.Listen
			seen[key]++
			if seen[key] == 2 {
				errs = append(errs, fmt.Errorf("%s: duplicate %s listener %q", where, proto, st.Listen))
			}
		}
		// A stream must have at least one target: a default proxy_pass and/or
		// SNI routes.
		if strings.TrimSpace(st.ProxyPass) == "" && len(st.SNIRoutes) == 0 {
			errs = append(errs, fmt.Errorf("%s: 'proxy_pass' or 'sni_routes' is required", where))
		}
		if strings.TrimSpace(st.ProxyPass) != "" {
			errs = append(errs, validateStreamTarget(st.ProxyPass, upstreamNames, where+".proxy_pass")...)
		}
		for host, target := range st.SNIRoutes {
			if strings.TrimSpace(host) == "" {
				errs = append(errs, fmt.Errorf("%s.sni_routes: empty SNI host key", where))
			}
			errs = append(errs, validateStreamTarget(target, upstreamNames, fmt.Sprintf("%s.sni_routes[%q]", where, host))...)
		}
		// SNI routing, TLS passthrough, and the PROXY protocol are TCP-only in v1.
		if proto == "udp" {
			if len(st.SNIRoutes) > 0 {
				errs = append(errs, fmt.Errorf("%s: 'sni_routes' is only supported for tcp streams", where))
			}
			if st.TLSPassthrough {
				errs = append(errs, fmt.Errorf("%s: 'tls_passthrough' is only supported for tcp streams", where))
			}
			if strings.TrimSpace(st.ProxyProtocol) != "" {
				errs = append(errs, fmt.Errorf("%s: 'proxy_protocol' is only supported for tcp streams", where))
			}
		}
		switch strings.ToLower(strings.TrimSpace(st.ProxyProtocol)) {
		case "", "in", "out", "both":
		default:
			errs = append(errs, fmt.Errorf("%s: invalid proxy_protocol %q (want in, out, or both)", where, st.ProxyProtocol))
		}
		if st.ConnectTimeout < 0 {
			errs = append(errs, fmt.Errorf("%s: 'connect_timeout' must not be negative", where))
		}
		if st.IdleTimeout < 0 {
			errs = append(errs, fmt.Errorf("%s: 'idle_timeout' must not be negative", where))
		}
		if st.MaxUDPSessions < 0 {
			errs = append(errs, fmt.Errorf("%s: 'max_udp_sessions' must not be negative", where))
		}
	}
	return errs
}

// validateStreamTarget checks that a stream target is either a known upstream
// name or a literal host:port address.
func validateStreamTarget(target string, upstreamNames map[string]int, where string) []error {
	target = strings.TrimSpace(target)
	if target == "" {
		return []error{fmt.Errorf("%s: target is empty", where)}
	}
	if _, ok := upstreamNames[target]; ok {
		return nil
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		return []error{fmt.Errorf("%s: target %q is neither a known upstream nor a host:port address", where, target)}
	}
	return nil
}

// validateCompression checks the [compression] block. It validates structure
// only: the encoders allow-list, level range, and MIME types. Whether "br" and
// "zstd" are actually compiled in is reported at middleware construction with a
// "not compiled in this build" error, since that depends on build tags.
func validateCompression(c CompressionConfig) []error {
	if !c.Enabled {
		return nil
	}
	var errs []error
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
	hasFile := false
	for _, s := range c.Sinks {
		switch s {
		case "stdout", "syslog":
		case "file":
			hasFile = true
		default:
			errs = append(errs, fmt.Errorf("[observability.access_log] unknown sink %q (want stdout|file|syslog)", s))
		}
	}
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

// validateHealthCheck checks an upstream's active health-check block. It assumes
// defaults have been applied, so it validates the effective values: the probe
// type, that HTTP probes carry a path, positive interval/timeout with timeout
// strictly below the interval (so a probe finishes before the next round), and
// thresholds of at least one. It is a no-op when nil or disabled.
func validateHealthCheck(h *HealthCheckConfig, where string) []error {
	if h == nil || !h.Enabled {
		return nil
	}
	var errs []error
	switch h.Type {
	case "http":
		if strings.TrimSpace(h.Path) == "" {
			errs = append(errs, fmt.Errorf("%s: 'path' is required for http health checks", where))
		}
		for _, code := range h.ExpectStatus {
			if code < 100 || code > 599 {
				errs = append(errs, fmt.Errorf("%s: invalid expect_status %d (want 100-599)", where, code))
			}
		}
	case "tcp":
		// No path/status semantics for raw TCP connect probes.
	default:
		errs = append(errs, fmt.Errorf("%s: invalid type %q (want http or tcp)", where, h.Type))
	}
	if h.Interval <= 0 {
		errs = append(errs, fmt.Errorf("%s: interval must be greater than 0", where))
	}
	if h.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("%s: timeout must be greater than 0", where))
	}
	if h.Interval > 0 && h.Timeout > 0 && h.Timeout >= h.Interval {
		errs = append(errs, fmt.Errorf("%s: timeout (%s) must be less than interval (%s)", where, h.Timeout.Std(), h.Interval.Std()))
	}
	if h.HealthyThreshold < 1 {
		errs = append(errs, fmt.Errorf("%s: healthy_threshold must be at least 1", where))
	}
	if h.UnhealthyThreshold < 1 {
		errs = append(errs, fmt.Errorf("%s: unhealthy_threshold must be at least 1", where))
	}
	return errs
}

// discoveryEnabled reports whether a discovery block selects a non-static
// provider (so the upstream's backend set is resolved dynamically rather than
// read from the static Servers list).
func discoveryEnabled(d *DiscoveryConfig) bool {
	if d == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "", "static":
		return false
	default:
		return true
	}
}

// validateDiscovery checks an upstream's dynamic discovery block. It validates
// the provider type and the per-provider required fields. It is a no-op when nil
// or static. The "consul"/"kubernetes" types are accepted here regardless of
// build tags: a build lacking the provider rejects the config at startup/reload
// (the discoverer factory errors), the same model as other gated features.
func validateDiscovery(d *DiscoveryConfig, where string) []error {
	if d == nil {
		return nil
	}
	var errs []error
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "", "static":
		return nil
	case "dns":
		if strings.TrimSpace(d.Target) == "" {
			errs = append(errs, fmt.Errorf("%s: 'target' is required for dns discovery (want host:port)", where))
		} else if _, port, err := net.SplitHostPort(d.Target); err != nil || strings.TrimSpace(port) == "" {
			errs = append(errs, fmt.Errorf("%s: dns target %q must be host:port (A/AAAA records carry no port)", where, d.Target))
		}
	case "dns_srv":
		if strings.TrimSpace(d.Target) == "" {
			errs = append(errs, fmt.Errorf("%s: 'target' is required for dns_srv discovery (the SRV name)", where))
		}
	case "consul":
		if d.Consul == nil || strings.TrimSpace(d.Consul.Service) == "" {
			errs = append(errs, fmt.Errorf("%s: consul discovery requires [consul] with a 'service'", where))
		}
		if d.Consul != nil && strings.TrimSpace(d.Consul.Address) != "" {
			if u, err := url.Parse(d.Consul.Address); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs = append(errs, fmt.Errorf("%s: consul address %q must be an http(s) URL", where, d.Consul.Address))
			}
		}
	case "kubernetes":
		if d.Kubernetes == nil || strings.TrimSpace(d.Kubernetes.Service) == "" || strings.TrimSpace(d.Kubernetes.Namespace) == "" {
			errs = append(errs, fmt.Errorf("%s: kubernetes discovery requires [kubernetes] with 'namespace' and 'service'", where))
		}
		if d.Kubernetes != nil && strings.TrimSpace(d.Kubernetes.APIServer) != "" {
			if u, err := url.Parse(d.Kubernetes.APIServer); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs = append(errs, fmt.Errorf("%s: kubernetes api_server %q must be an http(s) URL", where, d.Kubernetes.APIServer))
			}
		}
	default:
		errs = append(errs, fmt.Errorf("%s: invalid type %q (want static|dns|dns_srv|consul|kubernetes)", where, d.Type))
	}
	if d.Refresh < 0 {
		errs = append(errs, fmt.Errorf("%s: refresh must not be negative", where))
	}
	return errs
}

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
		errs = append(errs, fmt.Errorf("%s: challenge \"dns-01\" requires a build with DNS provider support (the \"acme_dns\" tag), which is not available in this build; use http-01 or tls-alpn-01", where))
	default:
		errs = append(errs, fmt.Errorf("%s: invalid challenge %q (want http-01 | tls-alpn-01)", where, a.Challenge))
	}
	if len(a.Domains) == 0 && len(serverNames) == 0 {
		errs = append(errs, fmt.Errorf("%s: no domains to certify (set tls.acme.domains or the server's server_names)", where))
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

// validateLocation checks a single location for a valid, unambiguous action and
// that any referenced resources are well-formed.
func validateLocation(loc LocationConfig, where string, upstreamNames map[string]int, plugins map[string]PluginConfig) []error {
	var errs []error

	// Count configured actions to catch conflicts (e.g. root + proxy_pass).
	var actions []string
	if loc.Root != "" {
		actions = append(actions, "root")
	}
	if loc.ProxyPass != "" {
		actions = append(actions, "proxy_pass")
	}
	if loc.FastCGIPass != "" {
		actions = append(actions, "fastcgi_pass")
	}
	if loc.UWSGIPass != "" {
		actions = append(actions, "uwsgi_pass")
	}
	// redirect and return combine into a single redirect action: when both are
	// set, return is the redirect's status code (see router.redirectHandler), so
	// they are not in conflict. A bare return (no target) is its own action.
	switch {
	case loc.Redirect != "" && loc.Return != 0:
		actions = append(actions, "redirect")
		if loc.Return < 300 || loc.Return > 399 {
			errs = append(errs, fmt.Errorf("%s: return %d cannot be combined with a redirect target (use a 3xx status)", where, loc.Return))
		}
	case loc.Redirect != "":
		actions = append(actions, "redirect")
	case loc.Return != 0:
		actions = append(actions, "return")
	}
	if loc.Deny {
		actions = append(actions, "deny")
	}
	if loc.GRPCTranscode != nil {
		actions = append(actions, "grpc_transcode")
	}
	if loc.Plugin != "" {
		actions = append(actions, "plugin")
	}
	if len(actions) > 1 {
		errs = append(errs, fmt.Errorf("%s: conflicting actions %v (set exactly one)", where, actions))
	}

	if loc.ProxyPass != "" {
		errs = append(errs, validateProxyPass(loc.ProxyPass, where, upstreamNames)...)
	}
	// gRPC passthrough (grpc = true) is a flavor of proxy_pass, not a standalone
	// action; it requires proxy_pass to name the gRPC backend. Whether the build
	// can serve it (the "grpc" tag) is reported at handler build time.
	if loc.GRPC && loc.ProxyPass == "" {
		errs = append(errs, fmt.Errorf("%s: grpc = true requires proxy_pass (the gRPC backend)", where))
	}
	if loc.GRPCTranscode != nil {
		errs = append(errs, validateGRPCTranscode(loc.GRPCTranscode, where+".grpc_transcode", upstreamNames)...)
	}

	// Handler plugin action must reference a "handler" plugin; per-location
	// middleware plugins must reference "middleware" plugins.
	if loc.Plugin != "" {
		errs = append(errs, validatePluginRef(plugins, loc.Plugin, "handler", where+".plugin")...)
	}
	for k, name := range loc.Plugins {
		errs = append(errs, validatePluginRef(plugins, name, "middleware", fmt.Sprintf("%s.plugins[%d]", where, k))...)
	}

	for k, rw := range loc.Rewrites {
		if _, err := regexp.Compile(rw.Pattern); err != nil {
			errs = append(errs, fmt.Errorf("%s.rewrites[%d]: invalid pattern %q: %v", where, k, rw.Pattern, err))
		}
		switch rw.Flag {
		case "", "last", "break", "redirect", "permanent":
		default:
			errs = append(errs, fmt.Errorf("%s.rewrites[%d]: invalid flag %q (want last|break|redirect|permanent)", where, k, rw.Flag))
		}
	}

	if loc.Cache && loc.Root != "" {
		errs = append(errs, fmt.Errorf("%s: cache applies to proxy/fastcgi responses, not static 'root' locations", where))
	}
	if loc.RateLimit != nil {
		errs = append(errs, validateRateLimit(*loc.RateLimit, where+".rate_limit", true)...)
	}
	if loc.Auth != nil {
		errs = append(errs, validateAuth(loc.Auth, where+".auth")...)
	}
	return errs
}

// validateGRPCTranscode checks a gRPC-JSON transcoding block. The target must be
// a known upstream or a host:port, and exactly one descriptor source
// (descriptor_set file or use_reflection) must be configured. The descriptor
// file's existence is checked here; parsing it (which needs the protobuf runtime
// compiled in with the "grpc" tag) happens at handler build time.
func validateGRPCTranscode(g *GRPCTranscodeConfig, where string, upstreamNames map[string]int) []error {
	if g == nil {
		return nil
	}
	var errs []error
	if strings.TrimSpace(g.Target) == "" {
		errs = append(errs, fmt.Errorf("%s: target is required (upstream name or host:port)", where))
	} else if !strings.Contains(g.Target, ":") && upstreamNames[g.Target] == 0 {
		errs = append(errs, fmt.Errorf("%s: target %q is neither a known upstream nor a host:port", where, g.Target))
	}
	switch {
	case g.DescriptorSet == "" && !g.UseReflection:
		errs = append(errs, fmt.Errorf("%s: set exactly one of descriptor_set or use_reflection", where))
	case g.DescriptorSet != "" && g.UseReflection:
		errs = append(errs, fmt.Errorf("%s: descriptor_set and use_reflection are mutually exclusive", where))
	}
	if g.DescriptorSet != "" {
		if info, err := os.Stat(g.DescriptorSet); err != nil {
			errs = append(errs, fmt.Errorf("%s: descriptor_set %q: %v", where, g.DescriptorSet, err))
		} else if info.IsDir() {
			errs = append(errs, fmt.Errorf("%s: descriptor_set %q is a directory, not a file", where, g.DescriptorSet))
		}
	}
	switch strings.ToLower(strings.TrimSpace(g.StreamMode)) {
	case "", "ndjson", "sse":
	default:
		errs = append(errs, fmt.Errorf("%s: stream_mode %q must be \"ndjson\" or \"sse\"", where, g.StreamMode))
	}
	if g.MaxMessageSize.Bytes() < 0 {
		errs = append(errs, fmt.Errorf("%s: max_message_size must not be negative", where))
	}
	return errs
}

// validatePlugins checks each [plugins.NAME] declaration: a valid type, exactly
// one module source (path or inline), a readable path when given, sane limits,
// and that the fetch capability comes with an allowed-hosts allowlist. Whether
// the "wasm" build tag is compiled in is reported when the plugin set is built.
func validatePlugins(plugins map[string]PluginConfig) []error {
	var errs []error
	for name, p := range plugins {
		where := fmt.Sprintf("[plugins.%s]", name)
		if strings.TrimSpace(name) == "" {
			errs = append(errs, errors.New("[plugins]: plugin name must not be empty"))
		}
		switch strings.TrimSpace(p.Type) {
		case "", "middleware", "handler":
		default:
			errs = append(errs, fmt.Errorf("%s: invalid type %q (want middleware|handler)", where, p.Type))
		}
		hasPath := strings.TrimSpace(p.Path) != ""
		hasInline := strings.TrimSpace(p.Inline) != ""
		switch {
		case hasPath && hasInline:
			errs = append(errs, fmt.Errorf("%s: set exactly one of path or inline, not both", where))
		case !hasPath && !hasInline:
			errs = append(errs, fmt.Errorf("%s: a module source is required (set path or inline)", where))
		}
		if hasPath {
			if info, err := os.Stat(p.Path); err != nil {
				errs = append(errs, fmt.Errorf("%s: path %q: %v", where, p.Path, err))
			} else if info.IsDir() {
				errs = append(errs, fmt.Errorf("%s: path %q is a directory, not a .wasm file", where, p.Path))
			}
		}
		if p.MemoryLimit.Bytes() < 0 {
			errs = append(errs, fmt.Errorf("%s: memory_limit must not be negative", where))
		}
		if p.Timeout.Std() < 0 {
			errs = append(errs, fmt.Errorf("%s: timeout must not be negative", where))
		}
		if p.Fetch && len(p.AllowedHosts) == 0 {
			errs = append(errs, fmt.Errorf("%s: fetch is enabled but allowed_hosts is empty (an allowlist is required)", where))
		}
		if p.MaxRequestBody.Bytes() < 0 {
			errs = append(errs, fmt.Errorf("%s: max_request_body must not be negative", where))
		}
		if p.MaxResponseBody.Bytes() < 0 {
			errs = append(errs, fmt.Errorf("%s: max_response_body must not be negative", where))
		}
		if p.MaxFetchResponse.Bytes() < 0 {
			errs = append(errs, fmt.Errorf("%s: max_fetch_response must not be negative", where))
		}
		if p.FetchTimeout.Std() < 0 {
			errs = append(errs, fmt.Errorf("%s: fetch_timeout must not be negative", where))
		}
		if p.KVMaxEntries < 0 {
			errs = append(errs, fmt.Errorf("%s: kv_max_entries must not be negative", where))
		}
		if p.KVMaxBytes.Bytes() < 0 {
			errs = append(errs, fmt.Errorf("%s: kv_max_bytes must not be negative", where))
		}
	}
	return errs
}

// validatePluginRef checks that a referenced plugin name exists and has the
// expected type (middleware or handler).
func validatePluginRef(plugins map[string]PluginConfig, name, wantType, where string) []error {
	if strings.TrimSpace(name) == "" {
		return []error{fmt.Errorf("%s: plugin name must not be empty", where)}
	}
	p, ok := plugins[name]
	if !ok {
		return []error{fmt.Errorf("%s: references unknown plugin %q (declare it under [plugins.%s])", where, name, name)}
	}
	got := strings.TrimSpace(p.Type)
	if got == "" {
		got = "middleware"
	}
	if got != wantType {
		return []error{fmt.Errorf("%s: plugin %q has type %q but a %q plugin is required here", where, name, got, wantType)}
	}
	return nil
}

// validateProxyPass checks the proxy_pass target form and, for upstream
// references, that the named upstream exists.
func validateProxyPass(pass, where string, upstreamNames map[string]int) []error {
	u, err := url.Parse(pass)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return []error{fmt.Errorf("%s: invalid proxy_pass %q (want http(s)://host:port or http://upstream-name)", where, pass)}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return []error{fmt.Errorf("%s: proxy_pass scheme %q must be http or https", where, u.Scheme)}
	}
	// A host without a port that is not a known upstream and not an IP looks
	// like an upstream reference typo.
	if !strings.Contains(u.Host, ":") && upstreamNames[u.Host] == 0 && !strings.Contains(u.Host, ".") {
		return []error{fmt.Errorf("%s: proxy_pass references unknown upstream %q", where, u.Host)}
	}
	// Reject embedded credentials so secrets are not stored in the config file
	// and do not leak through search projections.
	if u.User != nil {
		return []error{fmt.Errorf("%s: proxy_pass must not contain credentials (use headers or TLS for authentication)", where)}
	}
	return nil
}

func validateMatch(m MatchConfig, where string) error {
	switch m.Type {
	case "exact", "prefix", "regex":
	case "":
		return fmt.Errorf("%s: match.type is required (exact|prefix|regex)", where)
	default:
		return fmt.Errorf("%s: invalid match.type %q (want exact|prefix|regex)", where, m.Type)
	}
	if strings.TrimSpace(m.Path) == "" {
		return fmt.Errorf("%s: match.path is required", where)
	}
	if m.Type == "regex" {
		if _, err := regexp.Compile(m.Path); err != nil {
			return fmt.Errorf("%s: invalid match regex %q: %v", where, m.Path, err)
		}
	}
	return nil
}
