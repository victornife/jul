// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"jul/internal/config"
)

func applyRouteRateLimit(c *config.Config, req patchRequest) (string, error) {
	loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
	if err != nil {
		return "", err
	}
	p := req.RateLimit
	if p == nil {
		return "", fmt.Errorf("route_set_rate_limit: rate_limit payload is required")
	}
	if p.MaxConns != nil {
		return "", fmt.Errorf("route_set_rate_limit: max_conns is valid only for rate_limit_global_set")
	}

	// route_set_rate_limit predates sparse global patches and remains a complete
	// route-policy replacement. Missing enabled therefore retains its legacy
	// zero-value meaning (disabled), while missing burst/key retain the existing
	// defaulting behavior for an enabled policy.
	enabled := p.Enabled != nil && *p.Enabled
	if enabled {
		rate := 0
		if p.Rate != nil {
			rate = *p.Rate
		}
		if rate <= 0 {
			return "", fmt.Errorf("route_set_rate_limit: rate must be > 0")
		}
		burst := 0
		if p.Burst != nil {
			burst = *p.Burst
		}
		if burst <= 0 {
			burst = rate
		}
		key := ""
		if p.Key != nil {
			key = strings.TrimSpace(*p.Key)
		}
		if key == "" {
			key = "ip"
		}
		if !config.ValidRateKey(key) {
			return "", fmt.Errorf("route_set_rate_limit: invalid key %q", key)
		}

		if loc.RateLimit == nil {
			loc.RateLimit = &config.RateLimitConfig{}
		}
		loc.RateLimit.Enabled = true
		loc.RateLimit.Rate = rate
		loc.RateLimit.Burst = burst
		loc.RateLimit.Key = key
	} else if loc.RateLimit != nil {
		loc.RateLimit.Enabled = false
	}

	if loc.RateLimit != nil && loc.RateLimit.Enabled {
		return fmt.Sprintf("route %s%s rate limit set (%d req/s, burst %d, key %s)",
			req.Listen, req.Path, loc.RateLimit.Rate, loc.RateLimit.Burst, loc.RateLimit.Key), nil
	}
	return fmt.Sprintf("route %s%s rate limit disabled", req.Listen, req.Path), nil
}

func applyGlobalSet(c *config.Config, p *globalPatch) (string, error) {
	if p == nil {
		return "", fmt.Errorf("global_set: global payload is required")
	}
	if !hasGlobalFields(p) {
		return "", fmt.Errorf("global_set: at least one field is required")
	}

	next := c.Global
	changed := make([]string, 0, 6)

	if p.WorkerThreads != nil {
		value, err := canonicalWorkerThreads(*p.WorkerThreads)
		if err != nil {
			return "", fmt.Errorf("global_set: worker_threads: %w", err)
		}
		if next.WorkerThreads != value {
			changed = append(changed, "worker_threads")
		}
		next.WorkerThreads = value
	}
	if p.LogLevel != nil {
		value := strings.TrimSpace(*p.LogLevel)
		switch value {
		case "debug", "info", "warn", "error":
		default:
			return "", fmt.Errorf("global_set: log_level must be debug, info, warn, or error")
		}
		if next.LogLevel != value {
			changed = append(changed, "log_level")
		}
		next.LogLevel = value
	}
	if p.LogFormat != nil {
		value := strings.TrimSpace(*p.LogFormat)
		switch value {
		case "text", "json":
		default:
			return "", fmt.Errorf("global_set: log_format must be text or json")
		}
		if next.LogFormat != value {
			changed = append(changed, "log_format")
		}
		next.LogFormat = value
	}
	if p.ShutdownTimeout != nil {
		value, err := positiveDuration("shutdown_timeout", *p.ShutdownTimeout)
		if err != nil {
			return "", fmt.Errorf("global_set: %w", err)
		}
		if next.ShutdownTimeout != value {
			changed = append(changed, "shutdown_timeout")
		}
		next.ShutdownTimeout = value
	}
	if p.ReloadTimeout != nil {
		value, err := positiveDuration("reload_timeout", *p.ReloadTimeout)
		if err != nil {
			return "", fmt.Errorf("global_set: %w", err)
		}
		if next.ReloadTimeout != value {
			changed = append(changed, "reload_timeout")
		}
		next.ReloadTimeout = value
	}
	if p.RedactMinSecretLength != nil {
		if *p.RedactMinSecretLength < 0 {
			return "", fmt.Errorf("global_set: redact_min_secret_length must be >= 0")
		}
		if next.RedactMinSecretLength != *p.RedactMinSecretLength {
			changed = append(changed, "redact_min_secret_length")
		}
		next.RedactMinSecretLength = *p.RedactMinSecretLength
	}

	c.Global = next
	return summarizeChangedFields("global", changed), nil
}

func applyCompressionSet(c *config.Config, p *compressionPatch) (string, error) {
	if p == nil {
		return "", fmt.Errorf("compression_set: compression payload is required")
	}
	if !hasCompressionFields(p) {
		return "", fmt.Errorf("compression_set: at least one field is required")
	}

	before := canonicalCompression(c.Compression)
	next := c.Compression

	if p.Enabled != nil {
		next.Enabled = config.Bool(*p.Enabled)
	}
	if p.Encoders != nil {
		if err := validateCompressionEncoders(*p.Encoders); err != nil {
			return "", fmt.Errorf("compression_set: encoders: %w", err)
		}
		next.Encoders = append([]string(nil), (*p.Encoders)...)
	}
	if p.Level != nil {
		if *p.Level < 0 || *p.Level > 11 {
			return "", fmt.Errorf("compression_set: level must be between 0 and 11")
		}
		next.Level = *p.Level
	}
	if p.MinSize != nil {
		var value config.Size
		if err := value.UnmarshalText([]byte(*p.MinSize)); err != nil {
			return "", fmt.Errorf("compression_set: min_size: %w", err)
		}
		next.MinSize = value
	}
	if p.Types != nil {
		if err := validateCompressionTypes(*p.Types); err != nil {
			return "", fmt.Errorf("compression_set: types: %w", err)
		}
		next.Types = append([]string(nil), (*p.Types)...)
	}
	if p.Precompressed != nil {
		next.Precompressed = *p.Precompressed
	}

	// Enabling must not activate invalid dormant settings. Defaults are resolved
	// exactly as config.Parse resolves them, while build-tag availability remains
	// the authoritative runtime preflight performed after the complete batch.
	after := canonicalCompression(next)
	if after.IsEnabled() {
		if err := validateCompressionEncoders(after.Encoders); err != nil {
			return "", fmt.Errorf("compression_set: encoders: %w", err)
		}
		if after.Level < 0 || after.Level > 11 {
			return "", fmt.Errorf("compression_set: level must be between 0 and 11")
		}
		if err := validateCompressionTypes(after.Types); err != nil {
			return "", fmt.Errorf("compression_set: types: %w", err)
		}
	}

	changed := make([]string, 0, 6)
	if p.Enabled != nil && before.IsEnabled() != after.IsEnabled() {
		changed = append(changed, "enabled")
	}
	if p.Encoders != nil && !slices.Equal(before.Encoders, after.Encoders) {
		changed = append(changed, "encoders")
	}
	if p.Level != nil && before.Level != after.Level {
		changed = append(changed, "level")
	}
	if p.MinSize != nil && before.MinSize != after.MinSize {
		changed = append(changed, "min_size")
	}
	if p.Types != nil && !slices.Equal(before.Types, after.Types) {
		changed = append(changed, "types")
	}
	if p.Precompressed != nil && before.Precompressed != after.Precompressed {
		changed = append(changed, "precompressed")
	}

	c.Compression = next
	return summarizeChangedFields("compression", changed), nil
}

func applyGlobalRateLimitSet(c *config.Config, p *rateLimitPatch) (string, error) {
	if p == nil {
		return "", fmt.Errorf("rate_limit_global_set: rate_limit payload is required")
	}
	if !hasRateLimitFields(p) {
		return "", fmt.Errorf("rate_limit_global_set: at least one field is required")
	}

	before := canonicalRateLimit(c.RateLimit)
	next := c.RateLimit

	if p.Enabled != nil {
		next.Enabled = *p.Enabled
	}
	if p.Key != nil {
		value := strings.TrimSpace(*p.Key)
		if !config.ValidRateKey(value) {
			return "", fmt.Errorf("rate_limit_global_set: invalid key %q", value)
		}
		next.Key = value
	}
	if p.Rate != nil {
		if *p.Rate < 0 {
			return "", fmt.Errorf("rate_limit_global_set: rate must be >= 0")
		}
		next.Rate = *p.Rate
	}
	if p.Burst != nil {
		if *p.Burst < 0 {
			return "", fmt.Errorf("rate_limit_global_set: burst must be >= 0")
		}
		next.Burst = *p.Burst
	}
	if p.MaxConns != nil {
		if *p.MaxConns < 0 {
			return "", fmt.Errorf("rate_limit_global_set: max_conns must be >= 0")
		}
		next.MaxConns = *p.MaxConns
	}

	after := canonicalRateLimit(next)
	if after.Enabled {
		if after.Rate <= 0 {
			return "", fmt.Errorf("rate_limit_global_set: enabled policy requires rate > 0")
		}
		if after.Burst < after.Rate {
			return "", fmt.Errorf("rate_limit_global_set: burst (%d) must be >= rate (%d)", after.Burst, after.Rate)
		}
		if !config.ValidRateKey(after.Key) {
			return "", fmt.Errorf("rate_limit_global_set: invalid key %q", after.Key)
		}
	}

	changed := make([]string, 0, 5)
	if p.Enabled != nil && before.Enabled != after.Enabled {
		changed = append(changed, "enabled")
	}
	if p.Key != nil && before.Key != after.Key {
		changed = append(changed, "key")
	}
	if p.Rate != nil && before.Rate != after.Rate {
		changed = append(changed, "rate")
	}
	if p.Burst != nil && before.Burst != after.Burst {
		changed = append(changed, "burst")
	}
	if p.MaxConns != nil && before.MaxConns != after.MaxConns {
		changed = append(changed, "max_conns")
	}

	c.RateLimit = next
	return summarizeChangedFields("rate limit", changed), nil
}

func hasGlobalFields(p *globalPatch) bool {
	return p.WorkerThreads != nil || p.LogLevel != nil || p.LogFormat != nil ||
		p.ShutdownTimeout != nil || p.ReloadTimeout != nil || p.RedactMinSecretLength != nil
}

func hasCompressionFields(p *compressionPatch) bool {
	return p.Enabled != nil || p.Encoders != nil || p.Level != nil ||
		p.MinSize != nil || p.Types != nil || p.Precompressed != nil
}

func hasRateLimitFields(p *rateLimitPatch) bool {
	return p.Enabled != nil || p.Key != nil || p.Rate != nil || p.Burst != nil || p.MaxConns != nil
}

func summarizeChangedFields(scope string, changed []string) string {
	if len(changed) == 0 {
		return scope + " fields changed: none"
	}
	return scope + " fields changed: " + strings.Join(changed, ", ")
}

func canonicalWorkerThreads(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "auto" {
		return value, nil
	}
	if value == "" || value[0] < '1' || value[0] > '9' {
		return "", fmt.Errorf("expected auto or a positive integer")
	}
	for _, r := range value[1:] {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("expected auto or a positive integer")
		}
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return "", fmt.Errorf("expected auto or a positive integer")
	}
	return value, nil
}

func positiveDuration(name, raw string) (config.Duration, error) {
	var value config.Duration
	if err := value.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if value.Std() <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil
}

func validateCompressionEncoders(values []string) error {
	for _, value := range values {
		if strings.TrimSpace(value) != value || value == "" {
			return fmt.Errorf("encoder entries must be non-empty canonical names")
		}
		switch value {
		case "gzip", "br", "zstd":
		default:
			return fmt.Errorf("invalid encoder %q (want gzip|br|zstd)", value)
		}
	}
	return nil
}

func validateCompressionTypes(values []string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("MIME type entries must be non-empty")
		}
	}
	return nil
}

func canonicalCompression(in config.CompressionConfig) config.CompressionConfig {
	out := in
	out.Encoders = append([]string(nil), in.Encoders...)
	out.Types = append([]string(nil), in.Types...)
	if in.Enabled == nil {
		enabled := len(in.Encoders) > 0 || len(in.Types) > 0 || in.MinSize > 0 || in.Level != 0 || in.Precompressed
		out.Enabled = config.Bool(enabled)
	} else {
		out.Enabled = config.Bool(*in.Enabled)
	}
	if out.IsEnabled() {
		if len(out.Encoders) == 0 {
			out.Encoders = []string{"gzip"}
		}
		if out.MinSize == 0 {
			out.MinSize = config.Size(1 << 10)
		}
		if len(out.Types) == 0 {
			out.Types = config.DefaultCompressionTypes()
		}
	}
	return out
}

func canonicalRateLimit(in config.RateLimitConfig) config.RateLimitConfig {
	out := in
	if out.Enabled {
		if out.Key == "" {
			out.Key = "ip"
		}
		if out.Burst == 0 {
			out.Burst = out.Rate
		}
	}
	return out
}
