// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"strconv"
	"strings"
)

// This file contains the small reusable validators for public scalar and enum
// values. Cross-field and feature-specific rules remain in the focused
// validators that own those concepts; these helpers intentionally avoid a
// reflection-driven rules engine.

func validateOptionalEnum(path, value string, allowed ...string) error {
	if value == "" {
		return nil
	}
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("%s: invalid value %q; expected %s", path, value, humanList(allowed))
}

func humanList(values []string) string {
	switch len(values) {
	case 0:
		return "a supported value"
	case 1:
		return values[0]
	case 2:
		return values[0] + " or " + values[1]
	default:
		return strings.Join(values[:len(values)-1], ", ") + ", or " + values[len(values)-1]
	}
}

func validateWorkerThreads(value string) error {
	if value == "" || value == "auto" {
		return nil
	}
	// Keep the public grammar canonical and deterministic. strconv.Atoi accepts
	// forms such as "+1" that the documentation never advertised, while runtime
	// consumers historically treated malformed values as "auto".
	if strings.TrimSpace(value) != value || value[0] < '1' || value[0] > '9' {
		return fmt.Errorf("[global].worker_threads: invalid value %q; expected auto or a positive integer", value)
	}
	for _, r := range value[1:] {
		if r < '0' || r > '9' {
			return fmt.Errorf("[global].worker_threads: invalid value %q; expected auto or a positive integer", value)
		}
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return fmt.Errorf("[global].worker_threads: invalid value %q; expected auto or a positive integer", value)
	}
	return nil
}

func validateNonNegativeDuration(path string, value Duration) error {
	if value < 0 {
		return fmt.Errorf("%s: %s must be greater than or equal to 0", path, value.Std())
	}
	return nil
}

func validateNonNegativeSize(path string, value Size) error {
	if value < 0 {
		return fmt.Errorf("%s: %d must be greater than or equal to 0 bytes", path, value.Bytes())
	}
	return nil
}

func validateNonNegativeInt(path string, value int) error {
	if value < 0 {
		return fmt.Errorf("%s: %d must be greater than or equal to 0", path, value)
	}
	return nil
}

func validateHTTPStatus(path string, value int, zeroAllowed bool) error {
	if value == 0 && zeroAllowed {
		return nil
	}
	if value < 100 || value > 599 {
		return fmt.Errorf("%s: %d must be a valid HTTP status between 100 and 599", path, value)
	}
	return nil
}

func validateGlobalValues(c GlobalConfig) []error {
	var errs []error
	if err := validateOptionalEnum("[global].log_level", c.LogLevel, "debug", "info", "warn", "error"); err != nil {
		errs = append(errs, err)
	}
	if err := validateOptionalEnum("[global].log_format", c.LogFormat, "text", "json"); err != nil {
		errs = append(errs, err)
	}
	if err := validateWorkerThreads(c.WorkerThreads); err != nil {
		errs = append(errs, err)
	}
	if err := validateNonNegativeDuration("[global].shutdown_timeout", c.ShutdownTimeout); err != nil {
		errs = append(errs, err)
	}
	if err := validateNonNegativeDuration("[global].reload_timeout", c.ReloadTimeout); err != nil {
		errs = append(errs, err)
	}
	if err := validateNonNegativeInt("[global].redact_min_secret_length", c.RedactMinSecretLength); err != nil {
		errs = append(errs, err)
	}
	return errs
}

func validateUpstreamValues(c UpstreamConfig, where string) []error {
	var errs []error
	if err := validateNonNegativeInt(where+".max_fails", c.MaxFails); err != nil {
		errs = append(errs, err)
	}
	if err := validateNonNegativeDuration(where+".fail_timeout", c.FailTimeout); err != nil {
		errs = append(errs, err)
	}
	for i, server := range c.Servers {
		if err := validateNonNegativeInt(fmt.Sprintf("%s.servers[%d].weight", where, i), server.Weight); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func validateServerValues(c ServerConfig, where string) []error {
	var errs []error
	checks := []struct {
		path  string
		value Duration
	}{
		{where + ".read_header_timeout", c.ReadHeaderTimeout},
		{where + ".read_timeout", c.ReadTimeout},
		{where + ".write_timeout", c.WriteTimeout},
		{where + ".idle_timeout", c.IdleTimeout},
	}
	for _, check := range checks {
		if err := validateNonNegativeDuration(check.path, check.value); err != nil {
			errs = append(errs, err)
		}
	}
	for _, check := range []struct {
		path  string
		value Size
	}{
		{where + ".client_max_body_size", c.ClientMaxBodySize},
		{where + ".max_header_bytes", c.MaxHeaderBytes},
	} {
		if err := validateNonNegativeSize(check.path, check.value); err != nil {
			errs = append(errs, err)
		}
	}
	if c.RedirectHTTPS != 0 && c.RedirectHTTPS != 301 && c.RedirectHTTPS != 308 {
		errs = append(errs, fmt.Errorf("%s.redirect_https must be 301 or 308 (0 disables redirect), got %d", where, c.RedirectHTTPS))
	}
	return errs
}

func validateLocationValues(c LocationConfig, where string) []error {
	var errs []error
	for _, check := range []struct {
		path  string
		value Duration
	}{
		{where + ".proxy_connect_timeout", c.ProxyConnectTimeout},
		{where + ".proxy_read_timeout", c.ProxyReadTimeout},
		{where + ".proxy_send_timeout", c.ProxySendTimeout},
	} {
		if err := validateNonNegativeDuration(check.path, check.value); err != nil {
			errs = append(errs, err)
		}
	}
	if err := validateNonNegativeInt(where+".proxy_retries", c.ProxyRetries); err != nil {
		errs = append(errs, err)
	}
	if err := validateNonNegativeSize(where+".client_max_body_size", c.ClientMaxBodySize); err != nil {
		errs = append(errs, err)
	}
	if err := validateHTTPStatus(where+".return", c.Return, true); err != nil {
		errs = append(errs, err)
	}
	return errs
}

func validateCacheValues(c CacheConfig) []error {
	var errs []error
	for _, check := range []struct {
		path  string
		value Size
	}{
		{"[cache].memory_max_size", c.MemoryMaxSize},
		{"[cache].disk_max_size", c.DiskMaxSize},
	} {
		if err := validateNonNegativeSize(check.path, check.value); err != nil {
			errs = append(errs, err)
		}
	}
	for _, check := range []struct {
		path  string
		value Duration
	}{
		{"[cache].default_ttl", c.DefaultTTL},
		{"[cache].stale_while_revalidate", c.StaleWhileRevalidate},
		{"[cache].stale_if_error", c.StaleIfError},
	} {
		if err := validateNonNegativeDuration(check.path, check.value); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func validateAdminValues(c AdminConfig) []error {
	var errs []error
	for _, check := range []struct {
		path  string
		value int
	}{
		{"[admin].history_keep", c.HistoryKeep},
		{"[admin].max_event_conns", c.MaxEventConns},
		{"[admin].audit_log_rotate_max_mb", c.AuditLogRotateMaxMB},
		{"[admin].audit_log_rotate_keep", c.AuditLogRotateKeep},
		{"[admin].plugin_upload_max_size", c.PluginUploadMaxSize},
	} {
		if err := validateNonNegativeInt(check.path, check.value); err != nil {
			errs = append(errs, err)
		}
	}
	// Negative admin request-rate limits are intentionally valid and disable the
	// corresponding limiter. Zero means omitted/default; positive values enforce
	// a per-minute limit. This is a documented exception to non-negative integer
	// validation rather than an accidental fallback.
	return errs
}
