// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"fmt"
	"sort"
	"strings"

	"jul/internal/config"
)

// DiffEntry describes one configuration field whose effective value changed
// between two resolved configs, according to the lifecycle registry.
type DiffEntry struct {
	Path      string
	Class     Class
	Subsystem string
	Reason    string
	Before    any
	After     any
}

// DiffConfig compares the effective values of every registered path between
// two configs and returns the paths that differ. It is the source of truth for
// completeness checks: any registered field that changed is reported here.
func DiffConfig(before, after *config.Config) []DiffEntry {
	var out []DiffEntry
	for _, e := range Registry {
		bv := extractRegisteredValue(before, e.Path)
		av := extractRegisteredValue(after, e.Path)
		if !deepEqualValues(bv, av) {
			out = append(out, DiffEntry{
				Path:      e.Path,
				Class:     e.Class,
				Subsystem: e.Subsystem,
				Reason:    e.Reason,
				Before:    bv,
				After:     av,
			})
		}
	}
	return out
}

// extractRegisteredValue returns the effective value for any registered path.
// Startup-consumed paths reuse the fingerprint extractors; hot-reload and
// new-listener-only paths have their own canonical representations here.
func extractRegisteredValue(cfg *config.Config, path string) any {
	if v := extractValue(cfg, path); v != nil {
		return v
	}
	switch path {
	case "global.log_level":
		return cfg.Global.LogLevel
	case "global.shutdown_timeout":
		return cfg.Global.ShutdownTimeout
	case "global.reload_timeout":
		return cfg.Global.ReloadTimeout
	case "admin.enabled":
		return cfg.Admin.Enabled
	case "admin.listen":
		return cfg.Admin.Listen
	case "admin.token":
		return digestString(cfg.Admin.Token)
	case "admin.console":
		if cfg.Admin.Console == nil {
			return nil
		}
		return *cfg.Admin.Console
	case "admin.history_dir":
		return cfg.Admin.HistoryDir
	case "admin.history_keep":
		return cfg.Admin.HistoryKeep
	case "admin.rate_limit_read_per_min":
		return cfg.Admin.RateLimitReadPerMin
	case "admin.rate_limit_write_per_min":
		return cfg.Admin.RateLimitWritePerMin
	case "admin.rate_limit_apply_per_min":
		return cfg.Admin.RateLimitApplyPerMin
	case "admin.max_event_conns":
		return cfg.Admin.MaxEventConns
	case "admin.audit_log_file":
		return digestString(cfg.Admin.AuditLogFile)
	case "admin.audit_log_rotate_max_mb":
		return cfg.Admin.AuditLogRotateMaxMB
	case "admin.audit_log_rotate_keep":
		return cfg.Admin.AuditLogRotateKeep
	case "admin.plugin_upload_dir":
		return cfg.Admin.PluginUploadDir
	case "admin.plugin_upload_max_size":
		return cfg.Admin.PluginUploadMaxSize
	case "admin.plugin_upload_enabled":
		if cfg.Admin.PluginUploadEnabled == nil {
			return nil
		}
		return *cfg.Admin.PluginUploadEnabled
	case "servers.*.listen":
		return serverKeyMap(cfg, func(s *config.ServerConfig) any { return s.Listen })
	case "servers.*.server_names":
		return serverKeyMap(cfg, func(s *config.ServerConfig) any {
			names := append([]string(nil), s.ServerNames...)
			sort.Strings(names)
			return names
		})
	case "servers.*.client_max_body_size":
		return serverKeyMap(cfg, func(s *config.ServerConfig) any { return s.ClientMaxBodySize })
	case "servers.*.read_timeout":
		return serverKeyMap(cfg, func(s *config.ServerConfig) any { return s.ReadTimeout })
	case "servers.*.read_header_timeout":
		return serverKeyMap(cfg, func(s *config.ServerConfig) any { return s.ReadHeaderTimeout })
	case "servers.*.write_timeout":
		return serverKeyMap(cfg, func(s *config.ServerConfig) any { return s.WriteTimeout })
	case "servers.*.idle_timeout":
		return serverKeyMap(cfg, func(s *config.ServerConfig) any { return s.IdleTimeout })
	case "servers.*.max_header_bytes":
		return serverKeyMap(cfg, func(s *config.ServerConfig) any { return s.MaxHeaderBytes })
	case "servers.*.http3":
		return http3Fingerprint(cfg)
	case "servers.*.h2c":
		return h2cFingerprint(cfg)
	case "servers.*.locations.*.proxy_pass":
		return locationFieldMap(cfg, func(l *config.LocationConfig) any { return l.ProxyPass })
	case "servers.*.locations.*.root":
		return locationFieldMap(cfg, func(l *config.LocationConfig) any { return l.Root })
	case "servers.*.locations.*.cache":
		return locationFieldMap(cfg, func(l *config.LocationConfig) any { return l.Cache })
	case "servers.*.locations.*.rate_limit":
		return locationFieldMap(cfg, func(l *config.LocationConfig) any {
			if l.RateLimit == nil {
				return nil
			}
			return *l.RateLimit
		})
	case "servers.*.locations.*.auth":
		return locationFieldMap(cfg, func(l *config.LocationConfig) any {
			if l.Auth == nil {
				return nil
			}
			return *l.Auth
		})
	case "servers.*.locations.*.waf":
		return locationFieldMap(cfg, func(l *config.LocationConfig) any {
			if l.WAF == nil {
				return nil
			}
			return *l.WAF
		})
	case "servers.*.locations.*.plugins":
		return locationFieldMap(cfg, func(l *config.LocationConfig) any {
			cp := append([]string(nil), l.Plugins...)
			sort.Strings(cp)
			return cp
		})
	case "upstreams.*.name":
		return upstreamKeyMap(cfg, func(u *config.UpstreamConfig) any { return u.Name })
	case "upstreams.*.strategy":
		return upstreamKeyMap(cfg, func(u *config.UpstreamConfig) any { return u.Strategy })
	case "upstreams.*.servers":
		return upstreamServersMap(cfg)
	case "upstreams.*.max_fails":
		return upstreamKeyMap(cfg, func(u *config.UpstreamConfig) any { return u.MaxFails })
	case "upstreams.*.fail_timeout":
		return upstreamKeyMap(cfg, func(u *config.UpstreamConfig) any { return u.FailTimeout })
	case "upstreams.*.health_check":
		return upstreamKeyMap(cfg, func(u *config.UpstreamConfig) any {
			if u.HealthCheck == nil {
				return nil
			}
			return *u.HealthCheck
		})
	case "compression.enabled":
		return cfg.Compression.IsEnabled()
	case "compression.types":
		cp := append([]string(nil), cfg.Compression.Types...)
		sort.Strings(cp)
		return cp
	case "compression.min_length":
		return cfg.Compression.MinSize
	case "rate_limit.enabled":
		return cfg.RateLimit.Enabled
	case "rate_limit.rate":
		return cfg.RateLimit.Rate
	case "rate_limit.burst":
		return cfg.RateLimit.Burst
	case "rate_limit.max_conns":
		return cfg.RateLimit.MaxConns
	case "cache.default_ttl":
		return cfg.Cache.DefaultTTL
	case "cache.stale_while_revalidate":
		return cfg.Cache.StaleWhileRevalidate
	case "cache.stale_if_error":
		return cfg.Cache.StaleIfError
	case "waf.enabled":
		return cfg.WAF.Enabled
	case "waf.mode":
		return cfg.WAF.Mode
	case "waf.crs_enabled":
		return cfg.WAF.CRSEnabled
	case "observability.tracing.exporter":
		return cfg.Observability.Tracing.Exporter
	case "observability.tracing.service_name":
		return cfg.Observability.Tracing.ServiceName
	case "observability.tracing.insecure":
		return cfg.Observability.Tracing.Insecure
	case "observability.access_log.rotate_max_mb":
		return cfg.Observability.AccessLog.RotateMaxMB
	case "observability.access_log.rotate_keep":
		return cfg.Observability.AccessLog.RotateKeep
	case "stream.*.listen":
		return streamKeyMap(cfg, func(s *config.StreamServer) any { return s.Listen })
	case "stream.*.protocol":
		return streamProtocolFingerprint(cfg)
	case "stream.*.proxy_pass":
		return streamKeyMap(cfg, func(s *config.StreamServer) any { return s.ProxyPass })
	case "stream.*.sni_routes":
		return streamKeyMap(cfg, func(s *config.StreamServer) any { return s.SNIRoutes })
	}
	return nil
}

func serverKeyMap(cfg *config.Config, fn func(*config.ServerConfig) any) map[string]any {
	out := make(map[string]any, len(cfg.Servers))
	for i := range cfg.Servers {
		s := &cfg.Servers[i]
		out[serverKey(s)] = fn(s)
	}
	return out
}

func serverKey(s *config.ServerConfig) string {
	key := s.Listen
	if len(s.ServerNames) > 0 {
		key = s.ServerNames[0] + ":" + s.Listen
	}
	return key
}

func locationFieldMap(cfg *config.Config, fn func(*config.LocationConfig) any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(cfg.Servers))
	for i := range cfg.Servers {
		s := &cfg.Servers[i]
		locMap := make(map[string]any, len(s.Locations))
		for j := range s.Locations {
			l := &s.Locations[j]
			locMap[locationKey(l)] = fn(l)
		}
		out[serverKey(s)] = locMap
	}
	return out
}

func locationKey(l *config.LocationConfig) string {
	t := l.Match.Type
	if t == "" {
		t = "prefix"
	}
	return t + " " + l.Match.Path
}

func upstreamKeyMap(cfg *config.Config, fn func(*config.UpstreamConfig) any) map[string]any {
	out := make(map[string]any, len(cfg.Upstreams))
	for i := range cfg.Upstreams {
		u := &cfg.Upstreams[i]
		out[u.Name] = fn(u)
	}
	return out
}

func upstreamServersMap(cfg *config.Config) map[string][]map[string]any {
	out := make(map[string][]map[string]any, len(cfg.Upstreams))
	for i := range cfg.Upstreams {
		u := &cfg.Upstreams[i]
		servers := make([]map[string]any, len(u.Servers))
		for j, srv := range u.Servers {
			servers[j] = map[string]any{
				"address": srv.Address,
				"weight":  srv.Weight,
			}
		}
		sort.Slice(servers, func(a, b int) bool {
			return fmt.Sprint(servers[a]["address"]) < fmt.Sprint(servers[b]["address"])
		})
		out[u.Name] = servers
	}
	return out
}

func streamKeyMap(cfg *config.Config, fn func(*config.StreamServer) any) map[string]any {
	out := make(map[string]any, len(cfg.Streams))
	for i := range cfg.Streams {
		s := &cfg.Streams[i]
		key := normalizeStreamProtocol(s.Protocol) + "/" + strings.TrimSpace(s.Listen)
		out[key] = fn(s)
	}
	return out
}

