// Package app holds the composition-root wiring helpers for the jul command.
//
// These functions are the pure, dependency-light pieces of the server's
// composition root (see cmd/jul/main.go): scope keys, listener-set derivation,
// upstream indexing, reload fan-in, and the runtime preflight. Extracting them
// into an importable package lets the wiring be unit-tested directly instead of
// only through a full process boot, following through on ADR-0007's plan to make
// the composition root testable (Finding CQ-2).
package app

import (
	"context"
	"fmt"
	"strings"

	"jul/internal/auth"
	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/waf"
)

// RateKeyKind maps a rate-limit key spec to its kind label for metrics, keeping
// cardinality bounded (the raw client value is never used as a label).
func RateKeyKind(spec string) string {
	switch {
	case strings.HasPrefix(spec, "header:"):
		return "header"
	case strings.HasPrefix(spec, "jwt:"):
		return "jwt"
	default:
		return "ip"
	}
}

// AuthScope builds a stable identity for a location's auth policy, used to map a
// pre-built Authenticator back to the location during router construction.
func AuthScope(srv config.ServerConfig, loc config.LocationConfig) string {
	return srv.Listen + "|" + strings.Join(srv.ServerNames, ",") + "|" + loc.Match.Path
}

// WAFScope builds a stable identity for a location's WAF policy, used to map a
// pre-built Firewall back to the location during router construction.
func WAFScope(srv config.ServerConfig, loc config.LocationConfig) string {
	return srv.Listen + "|" + strings.Join(srv.ServerNames, ",") + "|" + loc.Match.Path
}

// EffectiveWAF resolves the WAF policy that applies to a location: its own [waf]
// override when present, otherwise the global [waf] policy. The bool reports
// whether an enabled policy applies (so the caller builds a firewall).
func EffectiveWAF(c *config.Config, loc config.LocationConfig) (config.WAFConfig, bool) {
	if loc.WAF != nil {
		return *loc.WAF, loc.WAF.Enabled
	}
	return c.WAF, c.WAF.Enabled
}

// UniqueListenAddrs returns the distinct listen addresses across server blocks.
func UniqueListenAddrs(servers []config.ServerConfig) []string {
	seen := map[string]struct{}{}
	var addrs []string
	for _, srv := range servers {
		if srv.Listen == "" {
			continue
		}
		if _, ok := seen[srv.Listen]; ok {
			continue
		}
		seen[srv.Listen] = struct{}{}
		addrs = append(addrs, srv.Listen)
	}
	return addrs
}

// AddrServesTLS reports whether any server block on addr enables TLS. It marks
// plain HTTP listeners, where ACME HTTP-01 challenge responses are mounted.
func AddrServesTLS(servers []config.ServerConfig, addr string) bool {
	for _, srv := range servers {
		if srv.Listen == addr && srv.TLS != nil && srv.TLS.Enabled {
			return true
		}
	}
	return false
}

// IndexUpstreams builds a name -> upstream lookup table.
func IndexUpstreams(ups []config.UpstreamConfig) map[string]config.UpstreamConfig {
	m := make(map[string]config.UpstreamConfig, len(ups))
	for _, u := range ups {
		m[u.Name] = u
	}
	return m
}

// MergeReload fans multiple reload sources into one channel.
func MergeReload(ctx context.Context, sources ...<-chan struct{}) <-chan struct{} {
	out := make(chan struct{}, 1)
	for _, src := range sources {
		if src == nil {
			continue
		}
		go func(in <-chan struct{}) {
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-in:
					if !ok {
						return
					}
					select {
					case out <- struct{}{}:
					default:
					}
				}
			}
		}(src)
	}
	return out
}

// ValidateRuntimeConfig performs the full runtime preflight for a configuration:
// it clones the config, runs the structural validation, and dry-runs the
// build-tag-gated subsystems (WAF, auth, compression) so an edit that a lean
// build cannot serve — or that a compiled build would reject — fails here,
// before anything is written, keeping admin "apply" truthful.
func ValidateRuntimeConfig(c *config.Config) error {
	wafExtra := func(clone *config.Config) error {
		if err := waf.Check(clone); err != nil {
			return err
		}
		if waf.Compiled {
			for i := range clone.Servers {
				for j := range clone.Servers[i].Locations {
					loc := clone.Servers[i].Locations[j]
					wcfg, ok := EffectiveWAF(clone, loc)
					if !ok {
						continue
					}
					if _, err := waf.New(wcfg, waf.Options{}); err != nil {
						return fmt.Errorf("waf: %w", err)
					}
				}
			}
		}
		authExtra := func(c2 *config.Config) error {
			for i := range c2.Servers {
				for j := range c2.Servers[i].Locations {
					loc := c2.Servers[i].Locations[j]
					if loc.Auth == nil {
						continue
					}
					if _, err := auth.New(*loc.Auth, auth.Options{}); err != nil {
						return fmt.Errorf("auth: %w", err)
					}
				}
			}
			return nil
		}
		if err := authExtra(clone); err != nil {
			return err
		}
		// Dry-run the compression middleware so a configured encoder that is not
		// compiled into this build (br/zstd behind their tags) fails the
		// preflight here, before the config file is written, instead of only at
		// the asynchronous reload — keeping admin "apply" truthful: a rejected
		// build never reports success. Mirrors the WAF/auth dry-runs above.
		if clone.Compression.Enabled {
			if _, err := middleware.NewCompression(middleware.CompressionOptions{
				Encoders: clone.Compression.Encoders,
				Level:    clone.Compression.Level,
				MinSize:  clone.Compression.MinSize.Bytes(),
				Types:    clone.Compression.Types,
			}); err != nil {
				return fmt.Errorf("compression: %w", err)
			}
		}
		return nil
	}
	return config.PreflightClone(c, wafExtra)
}
