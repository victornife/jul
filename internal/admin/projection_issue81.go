// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

// LifecycleFieldProjection is a value-free field classification derived from
// the canonical lifecycle registry. It deliberately contains no configured
// before/after values.
type LifecycleFieldProjection struct {
	Class       string `json:"class"`
	Subsystem   string `json:"subsystem"`
	Reason      string `json:"reason"`
	Conditional bool   `json:"conditional,omitempty"`
}

// GlobalSettingsProjection is the complete non-secret guided-editor surface for
// [global]. Legacy global.access_log/global.error_log and all secret-bearing
// configuration are intentionally absent.
type GlobalSettingsProjection struct {
	WorkerThreads         string                              `json:"worker_threads"`
	LogLevel              string                              `json:"log_level"`
	LogFormat             string                              `json:"log_format"`
	ShutdownTimeout       string                              `json:"shutdown_timeout"`
	ReloadTimeout         string                              `json:"reload_timeout"`
	RedactMinSecretLength int                                 `json:"redact_min_secret_length"`
	Lifecycle             map[string]LifecycleFieldProjection `json:"lifecycle"`
}

// CompleteCompressionProjection retains every editable compression value even
// while compression is disabled, so dormant values round-trip unchanged.
type CompleteCompressionProjection struct {
	Enabled       bool     `json:"enabled"`
	Encoders      []string `json:"encoders"`
	Level         int      `json:"level"`
	MinSize       string   `json:"min_size"`
	Types         []string `json:"types"`
	Precompressed bool     `json:"precompressed"`
}

// GlobalRateLimitProjection is intentionally distinct from the per-location
// RateLimitProjection. max_conns is listener-global and never gains route-level
// semantics through this contract.
type GlobalRateLimitProjection struct {
	Enabled  bool   `json:"enabled"`
	Rate     int    `json:"rate"`
	Burst    int    `json:"burst"`
	Key      string `json:"key"`
	MaxConns int    `json:"max_conns"`
}

// CompleteCacheProjection preserves every current [cache] schema field. The
// memory_max compatibility alias remains for one release while the canonical
// memory_max_size name is adopted by the guided editor.
type CompleteCacheProjection struct {
	Enabled              bool   `json:"enabled"`
	MemoryMaxSize        string `json:"memory_max_size"`
	MemoryMax            string `json:"memory_max,omitempty"`
	DiskPath             string `json:"disk_path"`
	DiskMaxSize          string `json:"disk_max_size"`
	DefaultTTL           string `json:"default_ttl"`
	StaleWhileRevalidate string `json:"stale_while_revalidate"`
	StaleIfError         string `json:"stale_if_error"`
}

// ServerLimitsProjection is the smallest adjacent safe projection needed to
// seed server_set_limits without hard-coded defaults. It contains no route
// action/upstream detail and no secret-bearing fields.
type ServerLimitsProjection struct {
	Listen            string   `json:"listen"`
	ServerNames       []string `json:"server_names,omitempty"`
	ClientMaxBodySize string   `json:"client_max_body_size"`
	ReadTimeout       string   `json:"read_timeout"`
	WriteTimeout      string   `json:"write_timeout"`
	IdleTimeout       string   `json:"idle_timeout"`
}

// issue81TrafficControlsProjection is the v2 wire response for the existing
// Global & Traffic Controls area. Keeping it separate from the legacy internal
// projection avoids widening the per-location rate-limit contract.
type issue81TrafficControlsProjection struct {
	Global      *GlobalSettingsProjection      `json:"global"`
	Compression *CompleteCompressionProjection `json:"compression"`
	RateLimit   *GlobalRateLimitProjection     `json:"rate_limit"`
	Cache       *CompleteCacheProjection       `json:"cache"`
	Servers     []ServerLimitsProjection       `json:"servers"`
	Tracing     *TracingProjection             `json:"tracing,omitempty"`
	AccessLog   *AccessLogProjection           `json:"access_log,omitempty"`
}

func lifecycleFieldProjection(path string) LifecycleFieldProjection {
	entry, ok := lifecycle.Lookup(path)
	if !ok {
		return LifecycleFieldProjection{
			Class:     "unclassified",
			Subsystem: "unknown",
			Reason:    "No lifecycle disposition is registered; apply must fail closed.",
		}
	}
	return LifecycleFieldProjection{
		Class:       entry.Class.String(),
		Subsystem:   string(entry.Subsystem),
		Reason:      entry.Reason,
		Conditional: entry.Conditional,
	}
}

func projectGlobalSettings(c *config.Config) *GlobalSettingsProjection {
	workerThreads := strings.TrimSpace(c.Global.WorkerThreads)
	if workerThreads == "" {
		workerThreads = "auto"
	}
	return &GlobalSettingsProjection{
		WorkerThreads:         workerThreads,
		LogLevel:              c.Global.LogLevel,
		LogFormat:             c.Global.LogFormat,
		ShutdownTimeout:       string(mustMarshal(c.Global.ShutdownTimeout.MarshalText())),
		ReloadTimeout:         string(mustMarshal(c.Global.ReloadTimeout.MarshalText())),
		RedactMinSecretLength: c.Global.RedactMinSecretLength,
		Lifecycle: map[string]LifecycleFieldProjection{
			"worker_threads":           lifecycleFieldProjection("global.worker_threads"),
			"log_level":                lifecycleFieldProjection("global.log_level"),
			"log_format":               lifecycleFieldProjection("global.log_format"),
			"shutdown_timeout":         lifecycleFieldProjection("global.shutdown_timeout"),
			"reload_timeout":           lifecycleFieldProjection("global.reload_timeout"),
			"redact_min_secret_length": lifecycleFieldProjection("global.redact_min_secret_length"),
		},
	}
}

func projectCompressionSettings(c *config.Config) *CompleteCompressionProjection {
	return &CompleteCompressionProjection{
		Enabled:       c.Compression.IsEnabled(),
		Encoders:      append([]string{}, c.Compression.Encoders...),
		Level:         c.Compression.Level,
		MinSize:       string(mustMarshal(c.Compression.MinSize.MarshalText())),
		Types:         append([]string{}, c.Compression.Types...),
		Precompressed: c.Compression.Precompressed,
	}
}

func projectGlobalRateLimit(c *config.Config) *GlobalRateLimitProjection {
	return &GlobalRateLimitProjection{
		Enabled:  c.RateLimit.Enabled,
		Rate:     c.RateLimit.Rate,
		Burst:    c.RateLimit.Burst,
		Key:      c.RateLimit.Key,
		MaxConns: c.RateLimit.MaxConns,
	}
}

func projectCacheSettings(c *config.Config) *CompleteCacheProjection {
	memory := string(mustMarshal(c.Cache.MemoryMaxSize.MarshalText()))
	return &CompleteCacheProjection{
		Enabled:              c.Cache.Enabled,
		MemoryMaxSize:        memory,
		MemoryMax:            memory,
		DiskPath:             c.Cache.DiskPath,
		DiskMaxSize:          string(mustMarshal(c.Cache.DiskMaxSize.MarshalText())),
		DefaultTTL:           string(mustMarshal(c.Cache.DefaultTTL.MarshalText())),
		StaleWhileRevalidate: string(mustMarshal(c.Cache.StaleWhileRevalidate.MarshalText())),
		StaleIfError:         string(mustMarshal(c.Cache.StaleIfError.MarshalText())),
	}
}

func projectServerLimits(c *config.Config) []ServerLimitsProjection {
	out := make([]ServerLimitsProjection, 0, len(c.Servers))
	for i := range c.Servers {
		srv := &c.Servers[i]
		out = append(out, ServerLimitsProjection{
			Listen:            srv.Listen,
			ServerNames:       append([]string(nil), srv.ServerNames...),
			ClientMaxBodySize: string(mustMarshal(srv.ClientMaxBodySize.MarshalText())),
			ReadTimeout:       string(mustMarshal(srv.ReadTimeout.MarshalText())),
			WriteTimeout:      string(mustMarshal(srv.WriteTimeout.MarshalText())),
			IdleTimeout:       string(mustMarshal(srv.IdleTimeout.MarshalText())),
		})
	}
	return out
}

func projectIssue81TrafficControls(c *config.Config) issue81TrafficControlsProjection {
	legacy := projectTrafficControls(c)
	return issue81TrafficControlsProjection{
		Global:      projectGlobalSettings(c),
		Compression: projectCompressionSettings(c),
		RateLimit:   projectGlobalRateLimit(c),
		Cache:       projectCacheSettings(c),
		Servers:     projectServerLimits(c),
		Tracing:     legacy.Tracing,
		AccessLog:   legacy.AccessLog,
	}
}
