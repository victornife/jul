// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package doctor implements the read-only local jul doctor diagnostic command.
// It consumes authoritative configuration validation and preflight APIs while
// keeping network-capable checks opt-in.
package doctor

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"jul/internal/config"
	"jul/internal/diagnostics"
)

const (
	defaultTotalTimeout    = 20 * time.Second
	defaultPerCheckTimeout = 5 * time.Second
)

// Options configures one local diagnostic run.
type Options struct {
	ConfigPath      string
	CheckNetwork    bool
	TotalTimeout    time.Duration
	PerCheckTimeout time.Duration
	Product         string
	Version         string
	Commit          string
	BuildProfile    string
	Capabilities    map[string]bool
}

// Run evaluates the fixed local check registry and always returns a report,
// including when configuration loading or a later check fails.
func Run(ctx context.Context, options Options) diagnostics.Report {
	if options.ConfigPath == "" {
		options.ConfigPath = "server.toml"
	}
	if options.TotalTimeout <= 0 {
		options.TotalTimeout = defaultTotalTimeout
	}
	if options.PerCheckTimeout <= 0 {
		options.PerCheckTimeout = defaultPerCheckTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, options.TotalTimeout)
	defer cancel()

	session := &session{options: options}
	return (diagnostics.Runner{PerCheckTimeout: options.PerCheckTimeout}).Run(
		runCtx,
		"local",
		filepath.Base(options.ConfigPath),
		session.registry(),
	)
}

// ConfigMetadata is the safe configuration projection used by doctor and
// support bundles. It contains only bounded counts and enabled-state metadata;
// no addresses, paths, hostnames, routes, tokens or configuration values.
type ConfigMetadata struct {
	Authority          string          `json:"authority"`
	AdminEnabled       bool            `json:"admin_enabled"`
	AdminRBACEnabled   bool            `json:"admin_rbac_enabled"`
	AdminAuthenticated bool            `json:"admin_authenticated"`
	Servers            int             `json:"servers"`
	Listeners          int             `json:"listeners"`
	Routes              int             `json:"routes"`
	Upstreams           int             `json:"upstreams"`
	Backends             int             `json:"backends"`
	Streams              int             `json:"streams"`
	Plugins              int             `json:"plugins"`
	Capabilities         map[string]bool `json:"capabilities,omitempty"`
}

// SafeConfigMetadata creates the bounded metadata projection.
func SafeConfigMetadata(cfg *config.Config, capabilities map[string]bool) ConfigMetadata {
	metadata := ConfigMetadata{Capabilities: cloneCapabilities(capabilities)}
	if cfg == nil {
		return metadata
	}
	metadata.Authority = cfg.Global.ConfigAuthority
	if metadata.Authority == "" {
		metadata.Authority = "file_owned"
	}
	metadata.AdminEnabled = cfg.Admin.Enabled
	metadata.AdminRBACEnabled = cfg.Admin.RBAC.Enabled
	metadata.AdminAuthenticated = adminHasUsableCredential(cfg.Admin, time.Now())
	metadata.Servers = len(cfg.Servers)
	metadata.Listeners = countListeners(cfg)
	metadata.Upstreams = len(cfg.Upstreams)
	metadata.Streams = len(cfg.Streams)
	metadata.Plugins = len(cfg.Plugins)
	for _, server := range cfg.Servers {
		metadata.Routes += len(server.Locations)
	}
	for _, upstream := range cfg.Upstreams {
		metadata.Backends += len(upstream.Servers)
	}
	return metadata
}

func adminHasUsableCredential(admin config.AdminConfig, now time.Time) bool {
	if strings.TrimSpace(admin.Token) != "" {
		return true
	}
	if !admin.RBAC.Enabled {
		return false
	}
	for _, principal := range admin.RBAC.Principals {
		if principal.Disabled || strings.TrimSpace(principal.Token) == "" {
			continue
		}
		if !principal.ExpiresAt.IsZero() && !principal.ExpiresAt.After(now) {
			continue
		}
		return true
	}
	return false
}

func cloneCapabilities(input map[string]bool) map[string]bool {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]bool, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}