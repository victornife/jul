// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

// lintExposedWithoutTimeouts reports listeners reachable from other hosts that
// leave every slow-peer bound unset (#511): no read_timeout or write_timeout
// on the server, and no proxy_read_timeout or proxy_send_timeout on any of its
// locations. read_header_timeout and client_max_body_size have defaults; these
// do not, so a slow body upload or a stalled backend holds a connection
// indefinitely.
func lintExposedWithoutTimeouts(c *Config) []Diagnostic {
	var diags []Diagnostic
	for i, srv := range c.Servers {
		if IsLoopbackListen(srv.Listen) || srv.ReadTimeout > 0 || srv.WriteTimeout > 0 || srv.SendTimeout > 0 {
			continue
		}
		bounded := false
		for _, loc := range srv.Locations {
			if loc.ProxyReadTimeout > 0 || loc.ProxySendTimeout > 0 || (loc.SendTimeout != nil && *loc.SendTimeout > 0) {
				bounded = true
				break
			}
		}
		if bounded {
			continue
		}
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    fmt.Sprintf("servers[%d] (listen %q)", i, srv.Listen),
			Message:  "listener is reachable from other hosts and sets no read_timeout, write_timeout, send_timeout, proxy_read_timeout or proxy_send_timeout; a slow client or stalled backend can hold a connection indefinitely",
			Hint:     "set send_timeout for stalled downstream writes, proxy_read_timeout/proxy_send_timeout on proxied locations (inactivity bounds, safe for streams), and read_timeout where no route takes long uploads; see docs/core-http.md#recommended-limits-for-internet-facing-listeners",
		})
	}
	return diags
}

// LintSource reports findings visible only in the configuration text, because
// defaulting erases them from the parsed Config. `jul lint` appends them to
// Lint's findings.
//
// Today that is one case (#511): `[compression] enabled = true` with an explicit
// `encoders = []` is silently defaulted to ["gzip"], which an operator may have
// meant as "no encoders".
func LintSource(data []byte) []Diagnostic {
	var presence struct {
		Compression struct {
			Enabled  *bool     `toml:"enabled"`
			Encoders *[]string `toml:"encoders"`
		} `toml:"compression"`
	}
	if err := toml.Unmarshal(data, &presence); err != nil {
		return nil // Parse reports decode errors
	}
	comp := presence.Compression
	if comp.Enabled != nil && *comp.Enabled && comp.Encoders != nil && len(*comp.Encoders) == 0 {
		return []Diagnostic{{
			Severity: SeverityWarning,
			Field:    "[compression].encoders",
			Message:  "encoders = [] with compression enabled is treated as [\"gzip\"], not as no encoders",
			Hint:     "list the encoders you want (e.g. [\"gzip\"]), or set [compression].enabled = false to turn compression off",
		}}
	}
	return nil
}
