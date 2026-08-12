// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Source loads a Config from some backing store. v1 ships a single TOML-backed
// implementation; future versions may add an NGINX-syntax parser that emits the
// same Config structs without touching the rest of the server.
type Source interface {
	// Load reads and decodes the configuration. It does not validate.
	Load() (*Config, error)
	// ReadRaw returns the raw configuration bytes as stored by the source.
	// For file-backed sources this is the on-disk bytes, preserving comments
	// and formatting, so digest comparisons match the bytes an admin editor
	// actually wrote (R11-03).
	ReadRaw() ([]byte, error)
	// Name identifies the source for logging (e.g. the file path).
	Name() string
}

// TOMLSource loads configuration from a TOML file on disk.
type TOMLSource struct {
	Path string
}

// NewTOMLSource returns a Source backed by the TOML file at path.
func NewTOMLSource(path string) *TOMLSource { return &TOMLSource{Path: path} }

// Name returns the file path.
func (s *TOMLSource) Name() string { return s.Path }

// ReadRaw returns the raw TOML bytes from disk.
func (s *TOMLSource) ReadRaw() ([]byte, error) {
	return os.ReadFile(s.Path)
}

// Load reads and decodes the TOML file into a Config, applying defaults.
func (s *TOMLSource) Load() (*Config, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", s.Path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", s.Path, err)
	}
	return cfg, nil
}

// Parse decodes TOML bytes into a Config and applies defaults. Unknown fields
// are rejected so typos cannot silently become no-op configuration. The only
// compatibility rewrite performed before strict decoding is the historical
// singular server_name field, which is normalized to server_names and is never
// emitted by Marshal. Parse does not run semantic validation; callers should run
// Validate separately.
func Parse(data []byte) (*Config, error) {
	normalized, err := normalizeDeprecatedTOML(data)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	var cfg Config
	decoder := toml.NewDecoder(bytes.NewReader(normalized)).DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		var missing *toml.StrictMissingError
		if errors.As(err, &missing) {
			return nil, fmt.Errorf("parse config: %s", strings.TrimSpace(missing.String()))
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

// normalizeDeprecatedTOML preserves only explicitly supported compatibility
// aliases without weakening strict decoding for the rest of the document.
// server_name was historically accepted by examples and tests; its value is
// converted into the canonical server_names list before the Config is decoded.
func normalizeDeprecatedTOML(data []byte) ([]byte, error) {
	if !bytes.Contains(data, []byte("server_name")) {
		return data, nil
	}

	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		// Preserve the original bytes so the strict decoder below reports the
		// original syntax or type error with its contextual line information.
		return data, nil
	}

	changed := false
	normalizeServer := func(index int, srv map[string]any) error {
		legacy, ok := srv["server_name"]
		if !ok {
			return nil
		}
		if _, exists := srv["server_names"]; exists {
			return fmt.Errorf("servers[%d]: cannot set both deprecated server_name and canonical server_names", index)
		}
		name, ok := legacy.(string)
		if !ok {
			return fmt.Errorf("servers[%d].server_name must be a string", index)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("servers[%d].server_name must not be empty", index)
		}
		srv["server_names"] = []string{name}
		delete(srv, "server_name")
		changed = true
		return nil
	}

	switch servers := doc["servers"].(type) {
	case []map[string]any:
		for i, srv := range servers {
			if err := normalizeServer(i, srv); err != nil {
				return nil, err
			}
		}
	case []any:
		for i, raw := range servers {
			srv, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if err := normalizeServer(i, srv); err != nil {
				return nil, err
			}
		}
	}

	if !changed {
		return data, nil
	}
	canonical, err := toml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("normalize deprecated server_name: %w", err)
	}
	return canonical, nil
}

// Marshal encodes a Config back to TOML. Custom types (Duration, Size,
// UpstreamServer) round-trip via their TextMarshaler implementations. Note that
// comments and original formatting are not preserved.
func Marshal(c *Config) ([]byte, error) {
	// Direct struct callers can leave access_log.sinks nil to express the same
	// omitted/default state as TOML without a sinks key. go-toml encodes a nil
	// slice as `sinks = []`, which would turn that omission into an explicit
	// empty list and fail validation when the canonical output is parsed again.
	// Canonicalize only the shallow copy so Marshal and Clone preserve the
	// documented default stdout sink without mutating the caller's config.
	canonical := *c
	canonical.Observability = c.Observability
	if canonical.Observability.AccessLog.Sinks == nil {
		canonical.Observability.AccessLog.Sinks = []string{"stdout"}
	}

	data, err := toml.Marshal(&canonical)
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return data, nil
}

// applyDefaults fills in conservative defaults for unset fields.
func (c *Config) applyDefaults() {
	if c.Global.LogLevel == "" {
		c.Global.LogLevel = "info"
	}
	if c.Global.LogFormat == "" {
		c.Global.LogFormat = "text"
	}
	if c.Global.ShutdownTimeout == 0 {
		c.Global.ShutdownTimeout = Duration(30 * time.Second)
	}
	// Zero or omitted reload_timeout defaults to 10s. Explicitly setting zero
	// behaves the same as omitting the field (unbounded reload is not supported
	// to prevent accidentally unbounded stalls in production).
	if c.Global.ReloadTimeout == 0 {
		c.Global.ReloadTimeout = Duration(10 * time.Second)
	}

	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.ReadHeaderTimeout == 0 {
			srv.ReadHeaderTimeout = Duration(10 * time.Second)
		}
		if srv.IdleTimeout == 0 {
			srv.IdleTimeout = Duration(60 * time.Second)
		}
		if srv.ClientMaxBodySize == 0 {
			srv.ClientMaxBodySize = Size(1 << 20) // 1 MiB
		}
		if srv.MaxHeaderBytes == 0 {
			srv.MaxHeaderBytes = Size(1 << 20) // 1 MiB
		}
		applyACMEDefaults(srv)
		applyHTTP3Defaults(srv)
	}

	for i := range c.Upstreams {
		up := &c.Upstreams[i]
		if up.Strategy == "" {
			up.Strategy = "round_robin"
		}
		if up.MaxFails == 0 {
			up.MaxFails = 3
		}
		if up.FailTimeout == 0 {
			up.FailTimeout = Duration(10 * time.Second)
		}
		for j := range up.Servers {
			if up.Servers[j].Weight == 0 {
				up.Servers[j].Weight = 1
			}
		}
		applyHealthCheckDefaults(up.HealthCheck)
		applyDiscoveryDefaults(up.Discovery)
	}

	for i := range c.Streams {
		st := &c.Streams[i]
		if strings.TrimSpace(st.Protocol) == "" {
			st.Protocol = "tcp"
		}
		if st.ConnectTimeout == 0 {
			st.ConnectTimeout = Duration(10 * time.Second)
		}
		if st.IdleTimeout == 0 {
			st.IdleTimeout = Duration(5 * time.Minute)
		}
		if st.MaxUDPSessions == 0 {
			st.MaxUDPSessions = 10000
		}
	}

	if c.Admin.Enabled && c.Admin.Listen == "" {
		c.Admin.Listen = "127.0.0.1:9090"
	}
	if c.Admin.Enabled && c.Admin.Console == nil {
		enabled := true
		c.Admin.Console = &enabled
	}
	if c.Admin.Enabled {
		if c.Admin.HistoryDir == "" {
			c.Admin.HistoryDir = "./jul-data/config-history"
		}
		if c.Admin.HistoryKeep == 0 {
			c.Admin.HistoryKeep = 50
		}
		// Admin API rate-limit defaults (Console v2 Milestone 1.6). A zero value
		// means "unset" and adopts the default; an explicit negative value
		// disables that limiter. Read limits are generous so legitimate console
		// polling never trips them; mutations and the high-impact apply path are
		// stricter; SSE streams are connection-capped.
		if c.Admin.RateLimitReadPerMin == 0 {
			c.Admin.RateLimitReadPerMin = 240
		}
		if c.Admin.RateLimitWritePerMin == 0 {
			c.Admin.RateLimitWritePerMin = 60
		}
		if c.Admin.RateLimitApplyPerMin == 0 {
			c.Admin.RateLimitApplyPerMin = 30
		}
		if c.Admin.MaxEventConns == 0 {
			c.Admin.MaxEventConns = 4
		}
		// Plugin upload defaults. Upload defaults to disabled for security.
		if c.Admin.PluginUploadEnabled == nil {
			c.Admin.PluginUploadEnabled = boolPtrAdmin(false)
		}
		if c.Admin.PluginUploadDir == "" {
			c.Admin.PluginUploadDir = "./jul-data/plugins"
		}
		if *c.Admin.PluginUploadEnabled && c.Admin.PluginUploadMaxSize == 0 {
			c.Admin.PluginUploadMaxSize = 32
		}
		// Durable audit-sink rotation defaults (P3-08). Only meaningful when a
		// durable path is configured; a zero value adopts the default. Audit
		// retention favors keeping the trail, so the backup count is generous
		// and age-based deletion is left disabled.
		if c.Admin.AuditLogFile != "" {
			if c.Admin.AuditLogRotateMaxMB == 0 {
				c.Admin.AuditLogRotateMaxMB = 100
			}
			if c.Admin.AuditLogRotateKeep == 0 {
				c.Admin.AuditLogRotateKeep = 14
			}
		}
		// RBAC defaults (P3-01). RBAC is disabled by default. When enabled,
		// default_role defaults to "admin" for the legacy compatibility principal.
		if c.Admin.RBAC.Enabled && c.Admin.RBAC.DefaultRole == "" {
			c.Admin.RBAC.DefaultRole = "admin"
		}
	}

	if c.Cache.Enabled {
		if c.Cache.MemoryMaxSize == 0 {
			c.Cache.MemoryMaxSize = Size(64 << 20) // 64 MiB
		}
		if c.Cache.DiskPath != "" && c.Cache.DiskMaxSize == 0 {
			c.Cache.DiskMaxSize = Size(512 << 20) // 512 MiB
		}
	}

	// Resolve compression enabled state:
	//   - Explicit enabled=true  → always active.
	//   - Explicit enabled=false → always inactive, even when other settings are
	//     present (operator explicitly opted out).
	//   - Omitted (nil)          → auto-detect: active when the block carries any
	//     non-zero encoder, type, size, or level setting; an empty [compression]
	//     block or an absent block has no settings and resolves to disabled.
	if c.Compression.Enabled == nil {
		hasSettings := len(c.Compression.Encoders) > 0 ||
			len(c.Compression.Types) > 0 ||
			c.Compression.MinSize > 0 ||
			c.Compression.Level != 0 ||
			c.Compression.Precompressed
		c.Compression.Enabled = Bool(hasSettings)
	}
	if *c.Compression.Enabled {
		if len(c.Compression.Encoders) == 0 {
			c.Compression.Encoders = []string{"gzip"}
		}
		if c.Compression.MinSize == 0 {
			c.Compression.MinSize = Size(1 << 10) // 1 KiB
		}
		if len(c.Compression.Types) == 0 {
			c.Compression.Types = defaultCompressionTypes()
		}
	}

	applyRateLimitDefaults(&c.RateLimit)
	applyWAFDefaults(&c.WAF)

	if c.Observability.Tracing.Enabled {
		t := &c.Observability.Tracing
		if t.Exporter == "" {
			t.Exporter = "otlp-grpc"
		}
		if t.ServiceName == "" {
			t.ServiceName = "jul"
		}
		// A zero ratio means "unset" here and defaults to full sampling; users
		// who want less set an explicit fraction in (0,1].
		if t.SampleRatio == 0 {
			t.SampleRatio = 1.0
		}
	}

	// Access-log defaults preserve the v1 default-on contract. A nil slice means
	// the key was omitted and defaults to stdout; a non-nil empty slice is kept
	// intact so validation can reject `enabled = true` with `sinks = []` rather
	// than silently turning it back into stdout. Disabled blocks retain dormant
	// settings so a later re-enable is deterministic.
	al := &c.Observability.AccessLog
	if al.Sinks == nil {
		al.Sinks = []string{"stdout"}
	}
	if al.Format == "" {
		al.Format = "text"
	}
	if al.RotateMaxMB == 0 {
		al.RotateMaxMB = 100
	}
	if al.RotateKeep == 0 {
		al.RotateKeep = 7
	}
	for i := range c.Servers {
		for j := range c.Servers[i].Locations {
			applyRateLimitDefaults(c.Servers[i].Locations[j].RateLimit)
			applyAuthDefaults(c.Servers[i].Locations[j].Auth)
			applyWAFDefaults(c.Servers[i].Locations[j].WAF)
		}
	}

	// WASM plugin defaults: middleware type, 16 MiB memory cap, 100ms per-call
	// timeout. Map values are not addressable, so reassign the copy.
	for name, p := range c.Plugins {
		if p.Type == "" {
			p.Type = "middleware"
		}
		if p.MemoryLimit == 0 {
			p.MemoryLimit = Size(16 << 20)
		}
		if p.Timeout == 0 {
			p.Timeout = Duration(100 * time.Millisecond)
		}
		if p.MaxRequestBody == 0 {
			p.MaxRequestBody = Size(1 << 20) // 1 MiB
		}
		if p.MaxResponseBody == 0 {
			p.MaxResponseBody = Size(8 << 20) // 8 MiB
		}
		if p.FetchTimeout == 0 {
			p.FetchTimeout = Duration(5 * time.Second)
		}
		if p.MaxFetchResponse == 0 {
			p.MaxFetchResponse = Size(1 << 20) // 1 MiB
		}
		if p.KVMaxEntries == 0 {
			p.KVMaxEntries = 1024
		}
		if p.KVMaxBytes == 0 {
			p.KVMaxBytes = Size(1 << 20) // 1 MiB
		}
		c.Plugins[name] = p
	}
}

// applyRateLimitDefaults fills in defaults for an enabled rate-limit policy
// (global or per-location override). It is a no-op when nil or disabled.
func applyRateLimitDefaults(rl *RateLimitConfig) {
	if rl == nil || !rl.Enabled {
		return
	}
	if rl.Key == "" {
		rl.Key = "ip"
	}
	if rl.Burst == 0 {
		rl.Burst = rl.Rate
	}
}

// applyWAFDefaults fills in defaults for a WAF policy: the enforcement mode, the
// block status, and the request-body buffer limit. It is a no-op when nil or
// disabled so a disabled block does not acquire surprising defaults.
func applyWAFDefaults(w *WAFConfig) {
	if w == nil || !w.Enabled {
		return
	}
	if w.Mode == "" {
		w.Mode = "block"
	}
	if w.BlockStatus == 0 {
		w.BlockStatus = 403
	}
	if w.RequestBodyLimit == 0 {
		w.RequestBodyLimit = Size(128 << 10) // 128 KiB
	}
}

// defaultJWTAlgorithms is the asymmetric signing-algorithm allow-list applied
// when a JWT auth block omits one. The symmetric "none" algorithm is never
// included and is always rejected during validation.
func defaultJWTAlgorithms() []string {
	return []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "PS256", "PS384", "PS512"}
}

// applyAuthDefaults fills in defaults for a per-location auth block: the Basic
// realm and the JWT algorithm allow-list. It is a no-op when nil.
func applyAuthDefaults(a *AuthConfig) {
	if a == nil {
		return
	}
	if a.Basic != nil && a.Basic.Realm == "" {
		a.Basic.Realm = "Restricted"
	}
	if a.JWT != nil && len(a.JWT.Algorithms) == 0 {
		a.JWT.Algorithms = defaultJWTAlgorithms()
	}
}

// applyHealthCheckDefaults fills in defaults for an upstream's active
// health-check block: probe type, interval/timeout, thresholds, and the
// expected HTTP status set. It is a no-op when nil or disabled so a disabled
// block does not acquire surprising defaults.
func applyHealthCheckDefaults(h *HealthCheckConfig) {
	if h == nil || !h.Enabled {
		return
	}
	if h.Type == "" {
		h.Type = "http"
	}
	if h.Interval == 0 {
		h.Interval = Duration(5 * time.Second)
	}
	if h.Timeout == 0 {
		h.Timeout = Duration(2 * time.Second)
	}
	if h.HealthyThreshold == 0 {
		h.HealthyThreshold = 2
	}
	if h.UnhealthyThreshold == 0 {
		h.UnhealthyThreshold = 3
	}
	if h.Type == "http" && len(h.ExpectStatus) == 0 {
		h.ExpectStatus = []int{200}
	}
}

// applyDiscoveryDefaults fills in defaults for an upstream's dynamic discovery
// block: a normalized lowercase type, a 30s refresh interval, the default
// Consul API address, and Consul passing-only health filtering. It is a no-op
// when nil or static so a disabled block acquires no surprising defaults.
func applyDiscoveryDefaults(d *DiscoveryConfig) {
	if d == nil {
		return
	}
	d.Type = strings.ToLower(strings.TrimSpace(d.Type))
	if d.Type == "" || d.Type == "static" {
		return
	}
	if d.Refresh == 0 {
		d.Refresh = Duration(30 * time.Second)
	}
	if d.Type == "consul" && d.Consul != nil {
		if strings.TrimSpace(d.Consul.Address) == "" {
			d.Consul.Address = "http://127.0.0.1:8500"
		}
		if d.Consul.PassingOnly == nil {
			passing := true
			d.Consul.PassingOnly = &passing
		}
	}
}

// applyACMEDefaults fills in defaults for an enabled ACME block on a server.
// Enabling ACME implies TLS is on (ACME is a certificate source), so it also
// promotes TLS.Enabled — a [servers.tls.acme] block alone is enough, no
// separate `enabled = true` on [servers.tls] required. Domains default to the
// block's server_names; the CA defaults to Let's Encrypt's staging endpoint so
// production rate limits are never consumed by accident. It is a no-op when
// ACME is absent or disabled.
func applyACMEDefaults(srv *ServerConfig) {
	if srv.TLS == nil || srv.TLS.ACME == nil || !srv.TLS.ACME.Enabled {
		return
	}
	srv.TLS.Enabled = true
	a := srv.TLS.ACME
	if a.CA == "" {
		a.CA = "letsencrypt-staging"
	}
	if a.Challenge == "" {
		a.Challenge = "http-01"
	}
	if a.CacheDir == "" {
		a.CacheDir = "./jul-data/certs"
	}
	if len(a.Domains) == 0 {
		a.Domains = append([]string(nil), srv.ServerNames...)
	}
	if a.OCSPStapling == nil {
		on := true
		a.OCSPStapling = &on
	}
}

// defaultAltSvcMaxAge is the Alt-Svc max-age applied to an enabled HTTP/3 block
// that does not set one. It is named so the linter can tell an operator-written
// value from a defaulted one.
const defaultAltSvcMaxAge = 86400

// applyHTTP3Defaults fills in defaults for an enabled HTTP/3 block on a server.
// It is a no-op when HTTP/3 is absent or disabled.
func applyHTTP3Defaults(srv *ServerConfig) {
	if srv.HTTP3 == nil || !srv.HTTP3.Enabled {
		return
	}
	if srv.HTTP3.AltSvcMaxAge == 0 {
		srv.HTTP3.AltSvcMaxAge = defaultAltSvcMaxAge
	}
}

// defaultCompressionTypes is the MIME allow-list applied when compression is
// enabled without an explicit list.
func defaultCompressionTypes() []string {
	return DefaultCompressionTypes()
}

// DefaultCompressionTypes returns the default MIME allow-list used when
// compression is enabled without an explicit list. It is exported so callers
// (e.g. the setup wizard) can build a self-describing CompressionConfig without
// relying on applyDefaults.
func DefaultCompressionTypes() []string {
	return []string{
		"text/*",
		"application/json",
		"application/javascript",
		"application/xml",
		"application/wasm",
		"image/svg+xml",
	}
}

// Clone returns a deep copy of the configuration by round-tripping through
// TOML. This is used by Preflight so secret expansion can operate on a
// temporary copy without mutating the raw (on-disk / admin-facing) config.
func (c *Config) Clone() (*Config, error) {
	data, err := Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("clone config: %w", err)
	}
	return Parse(data)
}

// PreflightClone clones the config, resolves secret references on the clone if
// needed, and runs structural validation so that checks which inspect files or
// URLs (e.g. ca_file, jwks_url) work correctly when the value comes from a
// secret reference. Optional extra validators (e.g. a dry-run WAF build) are
// run against the expanded clone. The original config is never modified and
// the live redaction registry is untouched.
func PreflightClone(c *Config, extra ...func(*Config) error) error {
	expanded, _, _, err := Resolve(c)
	if err != nil {
		return err
	}
	if err := Validate(expanded); err != nil {
		return err
	}
	for _, fn := range extra {
		if err := fn(expanded); err != nil {
			return err
		}
	}
	return nil
}

func boolPtrAdmin(b bool) *bool { return &b }
