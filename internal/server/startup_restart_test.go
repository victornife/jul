// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"testing"

	"jul/internal/config"
)

// TestCacheRestartRequired pins the hot-apply policy for the response cache:
// the cache is built once at startup, so any change to [cache] requires a
// restart, while an unchanged block hot-applies without issue.
func TestCacheRestartRequired(t *testing.T) {
	base := func(mutate func(*config.CacheConfig)) *config.Config {
		c := &config.Config{}
		c.Cache = config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: config.Size(64 << 20),
			DiskPath:      "/var/cache/jul",
			DiskMaxSize:   config.Size(512 << 20),
		}
		if mutate != nil {
			mutate(&c.Cache)
		}
		return c
	}

	t.Run("unchanged cache hot-applies", func(t *testing.T) {
		if _, need := CacheRestartRequired(base(nil), base(nil)); need {
			t.Fatal("an unchanged [cache] block must not require a restart")
		}
	})

	t.Run("enabling cache requires restart", func(t *testing.T) {
		off := &config.Config{}
		if _, need := CacheRestartRequired(off, base(nil)); !need {
			t.Fatal("enabling cache must require a restart")
		}
	})

	t.Run("changing memory capacity requires restart", func(t *testing.T) {
		next := base(func(c *config.CacheConfig) { c.MemoryMaxSize = config.Size(128 << 20) })
		if _, need := CacheRestartRequired(base(nil), next); !need {
			t.Fatal("changing memory_max_size must require a restart")
		}
	})

	t.Run("changing disk path requires restart", func(t *testing.T) {
		next := base(func(c *config.CacheConfig) { c.DiskPath = "/new/path" })
		if _, need := CacheRestartRequired(base(nil), next); !need {
			t.Fatal("changing disk_path must require a restart")
		}
	})

	t.Run("changing default TTL requires restart", func(t *testing.T) {
		next := base(func(c *config.CacheConfig) { c.DefaultTTL = config.Duration(60e9) })
		if _, need := CacheRestartRequired(base(nil), next); !need {
			t.Fatal("changing default_ttl must require a restart")
		}
	})

	t.Run("restart message is non-empty", func(t *testing.T) {
		off := &config.Config{}
		msg, need := CacheRestartRequired(off, base(nil))
		if !need {
			t.Fatal("enabling cache must require a restart")
		}
		if msg == "" {
			t.Fatal("restart reason must not be empty")
		}
	})
}

// TestEgressRestartRequired pins the hot-apply policy for the egress allow-list:
// the dial policy is built once at startup, so any change requires a restart.
func TestEgressRestartRequired(t *testing.T) {
	base := func(mutate func(*config.EgressConfig)) *config.Config {
		c := &config.Config{}
		c.Egress = config.EgressConfig{
			Enabled: true,
			Allow:   []string{"idp.example.com", "10.0.0.0/8"},
		}
		if mutate != nil {
			mutate(&c.Egress)
		}
		return c
	}

	t.Run("unchanged egress hot-applies", func(t *testing.T) {
		if _, need := EgressRestartRequired(base(nil), base(nil)); need {
			t.Fatal("an unchanged [egress] block must not require a restart")
		}
	})

	t.Run("enabling egress requires restart", func(t *testing.T) {
		off := &config.Config{}
		if _, need := EgressRestartRequired(off, base(nil)); !need {
			t.Fatal("enabling egress must require a restart")
		}
	})

	t.Run("adding an allow entry requires restart", func(t *testing.T) {
		next := base(func(e *config.EgressConfig) {
			e.Allow = append(e.Allow, "auth.internal")
		})
		if _, need := EgressRestartRequired(base(nil), next); !need {
			t.Fatal("adding an allow entry must require a restart")
		}
	})

	t.Run("removing an allow entry requires restart", func(t *testing.T) {
		next := base(func(e *config.EgressConfig) { e.Allow = []string{"idp.example.com"} })
		if _, need := EgressRestartRequired(base(nil), next); !need {
			t.Fatal("removing an allow entry must require a restart")
		}
	})

	t.Run("disabling egress requires restart", func(t *testing.T) {
		next := base(func(e *config.EgressConfig) { e.Enabled = false })
		if _, need := EgressRestartRequired(base(nil), next); !need {
			t.Fatal("disabling egress must require a restart")
		}
	})

	t.Run("restart message is non-empty", func(t *testing.T) {
		off := &config.Config{}
		msg, need := EgressRestartRequired(off, base(nil))
		if !need {
			t.Fatal("enabling egress must require a restart")
		}
		if msg == "" {
			t.Fatal("restart reason must not be empty")
		}
	})
}

// TestAdminRestartRequired pins the hot-apply policy for the admin server:
// the admin listener is built once at startup (copying AdminConfig by value),
// so any change requires a restart. Token rotation is the critical security case.
func TestAdminRestartRequired(t *testing.T) {
	base := func(mutate func(*config.AdminConfig)) *config.Config {
		c := &config.Config{}
		c.Admin = config.AdminConfig{
			Enabled:     true,
			Listen:      "127.0.0.1:9090",
			Token:       "secret-token",
			HistoryDir:  "./jul-data/config-history",
			HistoryKeep: 50,
		}
		if mutate != nil {
			mutate(&c.Admin)
		}
		return c
	}

	t.Run("unchanged admin hot-applies", func(t *testing.T) {
		if _, need := AdminRestartRequired(base(nil), base(nil)); need {
			t.Fatal("an unchanged [admin] block must not require a restart")
		}
	})

	t.Run("token rotation requires restart", func(t *testing.T) {
		next := base(func(a *config.AdminConfig) { a.Token = "new-token" })
		if _, need := AdminRestartRequired(base(nil), next); !need {
			t.Fatal("rotating the admin token must require a restart")
		}
	})

	t.Run("changing listen address requires restart", func(t *testing.T) {
		next := base(func(a *config.AdminConfig) { a.Listen = "127.0.0.1:9191" })
		if _, need := AdminRestartRequired(base(nil), next); !need {
			t.Fatal("changing the admin listen address must require a restart")
		}
	})

	t.Run("changing history_keep requires restart", func(t *testing.T) {
		next := base(func(a *config.AdminConfig) { a.HistoryKeep = 100 })
		if _, need := AdminRestartRequired(base(nil), next); !need {
			t.Fatal("changing history_keep must require a restart")
		}
	})

	t.Run("changing rate limits requires restart", func(t *testing.T) {
		next := base(func(a *config.AdminConfig) { a.RateLimitWritePerMin = 30 })
		if _, need := AdminRestartRequired(base(nil), next); !need {
			t.Fatal("changing admin rate limits must require a restart")
		}
	})

	t.Run("changing plugin upload enabled requires restart", func(t *testing.T) {
		next := base(func(a *config.AdminConfig) { a.PluginUploadEnabled = config.Bool(true) })
		if _, need := AdminRestartRequired(base(nil), next); !need {
			t.Fatal("enabling plugin upload must require a restart")
		}
	})

	t.Run("nil vs explicit bool pointer detected correctly", func(t *testing.T) {
		a := &config.Config{}
		b := &config.Config{}
		if _, need := AdminRestartRequired(a, b); need {
			t.Fatal("two zero-value admin configs must not require a restart")
		}
		b.Admin.PluginUploadEnabled = config.Bool(false)
		if _, need := AdminRestartRequired(a, b); !need {
			t.Fatal("nil vs explicit false must require a restart")
		}
	})

	t.Run("restart message is non-empty", func(t *testing.T) {
		next := base(func(a *config.AdminConfig) { a.Token = "new-token" })
		msg, need := AdminRestartRequired(base(nil), next)
		if !need {
			t.Fatal("token rotation must require a restart")
		}
		if msg == "" {
			t.Fatal("restart reason must not be empty")
		}
	})
}

// TestMetricsRestartRequired pins the hot-apply policy for the Prometheus
// metrics configuration: the registry and its host-label setting are built once
// at startup, so any change requires a restart.
func TestMetricsRestartRequired(t *testing.T) {
	base := func(hostLabel bool) *config.Config {
		c := &config.Config{}
		c.Observability.Metrics.HostLabel = hostLabel
		return c
	}

	t.Run("unchanged metrics hot-applies", func(t *testing.T) {
		if _, need := MetricsRestartRequired(base(false), base(false)); need {
			t.Fatal("an unchanged [observability.metrics] block must not require a restart")
		}
	})

	t.Run("enabling host_label requires restart", func(t *testing.T) {
		if _, need := MetricsRestartRequired(base(false), base(true)); !need {
			t.Fatal("enabling host_label must require a restart")
		}
	})

	t.Run("disabling host_label requires restart", func(t *testing.T) {
		if _, need := MetricsRestartRequired(base(true), base(false)); !need {
			t.Fatal("disabling host_label must require a restart")
		}
	})

	t.Run("restart message is non-empty", func(t *testing.T) {
		msg, need := MetricsRestartRequired(base(false), base(true))
		if !need {
			t.Fatal("enabling host_label must require a restart")
		}
		if msg == "" {
			t.Fatal("restart reason must not be empty")
		}
	})
}
