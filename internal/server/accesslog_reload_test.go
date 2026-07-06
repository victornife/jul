// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"testing"

	"jul/internal/config"
)

// TestAccessLogRestartRequired pins the hot-apply policy for the access log: the
// stdout/file/syslog sinks are built once at startup, so any change to the
// [observability.access_log] block requires a restart, while an unchanged block
// hot-applies (it is a no-op for the running sinks).
func TestAccessLogRestartRequired(t *testing.T) {
	withAccessLog := func(mutate func(al *config.AccessLogConfig)) *config.Config {
		c := &config.Config{}
		c.Observability.AccessLog = config.AccessLogConfig{
			Sinks:       []string{"stdout", "file"},
			File:        "/var/log/jul/access.log",
			Format:      "json",
			RotateMaxMB: 100,
			RotateKeep:  7,
		}
		if mutate != nil {
			mutate(&c.Observability.AccessLog)
		}
		return c
	}

	t.Run("unchanged access log hot-applies", func(t *testing.T) {
		if _, need := AccessLogRestartRequired(withAccessLog(nil), withAccessLog(nil)); need {
			t.Fatal("an unchanged access-log block must not require a restart")
		}
	})

	t.Run("adding a sink requires restart", func(t *testing.T) {
		next := withAccessLog(func(al *config.AccessLogConfig) {
			al.Sinks = []string{"stdout", "file", "syslog"}
		})
		reason, need := AccessLogRestartRequired(withAccessLog(nil), next)
		if !need {
			t.Fatal("adding a sink must require a restart")
		}
		if reason == "" {
			t.Fatal("expected a non-empty reason")
		}
	})

	t.Run("changing the file path requires restart", func(t *testing.T) {
		next := withAccessLog(func(al *config.AccessLogConfig) { al.File = "/tmp/other.log" })
		if _, need := AccessLogRestartRequired(withAccessLog(nil), next); !need {
			t.Fatal("changing the access-log file path must require a restart")
		}
	})

	t.Run("changing the format requires restart", func(t *testing.T) {
		next := withAccessLog(func(al *config.AccessLogConfig) { al.Format = "text" })
		if _, need := AccessLogRestartRequired(withAccessLog(nil), next); !need {
			t.Fatal("changing the access-log format must require a restart")
		}
	})

	t.Run("changing rotation requires restart", func(t *testing.T) {
		maxMB := withAccessLog(func(al *config.AccessLogConfig) { al.RotateMaxMB = 50 })
		if _, need := AccessLogRestartRequired(withAccessLog(nil), maxMB); !need {
			t.Fatal("changing rotate_max_mb must require a restart")
		}
		keep := withAccessLog(func(al *config.AccessLogConfig) { al.RotateKeep = 14 })
		if _, need := AccessLogRestartRequired(withAccessLog(nil), keep); !need {
			t.Fatal("changing rotate_keep must require a restart")
		}
	})

	t.Run("sink order is significant", func(t *testing.T) {
		next := withAccessLog(func(al *config.AccessLogConfig) {
			al.Sinks = []string{"file", "stdout"}
		})
		if _, need := AccessLogRestartRequired(withAccessLog(nil), next); !need {
			t.Fatal("reordering sinks changes the wired set and must require a restart")
		}
	})
}
