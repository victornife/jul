// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"slices"
	"strings"

	"jul/internal/admin"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/observability"
	"jul/internal/storagefs"
)

// Jul-owned storage categories (#437). This is the closed label set of
// jul_storage_bytes and the display order of the Console storage cards.
var storageCategoryOrder = []string{"cache", "access_log", "audit_log", "config", "config_history", "plugin_upload", "acme"}

type storageTarget struct {
	category string
	path     string
}

// startupStorageTargets are the restart-bound locations: the disk cache and
// ACME certificate cache are created once at startup, and the configuration
// file (plus its managed-baseline marker beside it) is written only under
// managed authority.
func startupStorageTargets(cfg *config.Config, configPath string, managed bool) []storageTarget {
	var out []storageTarget
	if cfg.Cache.Enabled {
		if p := strings.TrimSpace(cfg.Cache.DiskPath); p != "" {
			out = append(out, storageTarget{"cache", p})
		}
	}
	if managed && configPath != "" {
		out = append(out, storageTarget{"config", configPath})
	}
	seen := map[string]bool{}
	for _, s := range cfg.Servers {
		if s.TLS == nil || s.TLS.ACME == nil || !s.TLS.ACME.Enabled {
			continue
		}
		p := strings.TrimSpace(s.TLS.ACME.CacheDir)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, storageTarget{"acme", p})
	}
	return out
}

// liveStorageTargets are the hot-applied locations read from the serving
// generation: the access-log file sink.
func liveStorageTargets(cfg *config.Config) []storageTarget {
	if cfg == nil {
		return nil
	}
	al := cfg.Observability.AccessLog
	if !al.IsEnabled() || !slices.Contains(al.Sinks, "file") {
		return nil
	}
	if p := strings.TrimSpace(al.File); p != "" {
		return []storageTarget{{"access_log", p}}
	}
	return nil
}

// storageProbe builds the storage headroom projection on demand. Every input
// is read at call time so hot-applied paths follow the serving generation.
type storageProbe struct {
	startup []storageTarget
	live    func() *config.Config
	admin   func() []admin.StorageTarget
	cache   *cache.Cache
	audit   func() (failures uint64, failing, ok bool)
	hints   storagefs.Hints
	// probe is storagefs.Probe; tests substitute a deterministic filesystem.
	probe func([]string) []storagefs.Result
}

// stateSeverity orders states for picking a category's worst location.
var stateSeverity = map[storagefs.State]int{
	storagefs.StateOK:          0,
	storagefs.StateUnavailable: 1,
	storagefs.StateError:       2,
	storagefs.StateLow:         3,
	storagefs.StateCritical:    4,
}

type storageReading struct {
	result storagefs.Result
	state  storagefs.State
	ratio  *float64
}

func worseReading(a, b storageReading) bool {
	if sa, sb := stateSeverity[a.state], stateSeverity[b.state]; sa != sb {
		return sa > sb
	}
	return a.ratio != nil && b.ratio != nil && *a.ratio < *b.ratio
}

func (p *storageProbe) targets() []storageTarget {
	out := append([]storageTarget(nil), p.startup...)
	if p.live != nil {
		out = append(out, liveStorageTargets(p.live())...)
	}
	if p.admin != nil {
		for _, t := range p.admin() {
			out = append(out, storageTarget{t.Category, t.Path})
		}
	}
	return out
}

func (p *storageProbe) headroom() []observability.StorageHeadroom {
	targets := p.targets()
	if len(targets) == 0 {
		return nil
	}
	paths := make([]string, len(targets))
	for i, t := range targets {
		paths[i] = t.path
	}
	probe := p.probe
	if probe == nil {
		probe = storagefs.Probe
	}
	results := probe(paths)

	// A category with several locations (one ACME cache per server block)
	// reports its worst one.
	worst := map[string]storageReading{}
	for i, t := range targets {
		state, ratio := p.hints.Classify(results[i])
		r := storageReading{result: results[i], state: state, ratio: ratio}
		if prev, ok := worst[t.category]; !ok || worseReading(r, prev) {
			worst[t.category] = r
		}
	}

	out := make([]observability.StorageHeadroom, 0, len(worst))
	for _, cat := range storageCategoryOrder {
		r, ok := worst[cat]
		if !ok {
			continue
		}
		h := observability.StorageHeadroom{
			Category:        cat,
			State:           string(r.state),
			Reason:          string(r.result.Reason),
			AvailableRatio:  r.ratio,
			PendingCreation: r.result.ViaParent,
		}
		if r.result.Reason == storagefs.ReasonNone {
			if r.result.HaveAvailable {
				v := float64(r.result.Available)
				h.AvailableBytes = &v
			}
			if r.result.HaveTotal {
				v := float64(r.result.Total)
				h.TotalBytes = &v
			}
			for _, other := range storageCategoryOrder {
				if o, ok := worst[other]; ok && other != cat && o.result.Group == r.result.Group {
					h.SharedWith = append(h.SharedWith, other)
				}
			}
			slices.Sort(h.SharedWith)
		}
		p.overlayWriteHealth(&h)
		out = append(out, h)
	}
	return out
}

// overlayWriteHealth adds the owning subsystem's own write-failure signal,
// for the subsystems that track one.
func (p *storageProbe) overlayWriteHealth(h *observability.StorageHeadroom) {
	var (
		failures float64
		failing  bool
	)
	switch h.Category {
	case "cache":
		n, f, ok := p.cache.DiskWriteHealth()
		if !ok {
			return
		}
		failures, failing = float64(n), f
	case "access_log":
		n, f := observability.AccessLogFileWriteHealth.Snapshot()
		failures, failing = float64(n), f
	case "audit_log":
		if p.audit == nil {
			return
		}
		n, f, ok := p.audit()
		if !ok {
			return
		}
		failures, failing = float64(n), f
	default:
		return
	}
	h.WriteFailures = &failures
	h.WritesFailing = failing
}

// storageHintsView converts the advisory thresholds for the stats projection.
func storageHintsView(h storagefs.Hints) observability.StorageHints {
	return observability.StorageHints{LowRatio: h.LowRatio, CriticalRatio: h.CriticalRatio}
}
