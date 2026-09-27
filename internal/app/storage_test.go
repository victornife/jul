// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"jul/internal/admin"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/observability"
	"jul/internal/storagefs"
)

func acmeServer(dir string, enabled bool) config.ServerConfig {
	return config.ServerConfig{TLS: &config.TLSConfig{ACME: &config.ACMEConfig{Enabled: enabled, CacheDir: dir}}}
}

func TestStartupStorageTargets(t *testing.T) {
	cfg := &config.Config{
		Cache: config.CacheConfig{Enabled: true, DiskPath: " /var/cache/jul "},
		Servers: []config.ServerConfig{
			{},
			acmeServer("/a/certs", true),
			acmeServer("/a/certs", true),
			acmeServer("/b/certs", true),
			acmeServer("/c/certs", false),
			acmeServer(" ", true),
		},
	}
	got := startupStorageTargets(cfg, "/etc/jul/jul.toml", true)
	want := []storageTarget{
		{"cache", "/var/cache/jul"},
		{"config", "/etc/jul/jul.toml"},
		{"acme", "/a/certs"},
		{"acme", "/b/certs"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}

	// file_owned never writes the config file; a disabled cache owns no disk;
	// a memory-only cache has no disk path.
	cfg.Cache.Enabled = false
	cfg.Servers = nil
	if got := startupStorageTargets(cfg, "/etc/jul/jul.toml", false); len(got) != 0 {
		t.Fatalf("file_owned/disabled targets = %v", got)
	}
	cfg.Cache = config.CacheConfig{Enabled: true}
	if got := startupStorageTargets(cfg, "", true); len(got) != 0 {
		t.Fatalf("memory-only cache / no config path targets = %v", got)
	}
}

func TestLiveStorageTargets(t *testing.T) {
	if got := liveStorageTargets(nil); got != nil {
		t.Fatalf("nil config = %v", got)
	}
	cfg := &config.Config{}
	cfg.Observability.AccessLog = config.AccessLogConfig{Sinks: []string{"stdout"}, File: "/logs/a.log"}
	if got := liveStorageTargets(cfg); got != nil {
		t.Fatalf("stdout-only sink = %v", got)
	}
	cfg.Observability.AccessLog.Sinks = []string{"stdout", "file"}
	if got := liveStorageTargets(cfg); !reflect.DeepEqual(got, []storageTarget{{"access_log", "/logs/a.log"}}) {
		t.Fatalf("file sink = %v", got)
	}
	cfg.Observability.AccessLog.Enabled = config.Bool(false)
	if got := liveStorageTargets(cfg); got != nil {
		t.Fatalf("disabled access log = %v", got)
	}
	cfg.Observability.AccessLog.Enabled = nil
	cfg.Observability.AccessLog.File = "  "
	if got := liveStorageTargets(cfg); got != nil {
		t.Fatalf("empty file = %v", got)
	}
}

// fakeFS answers Probe from a fixed table keyed by path.
func fakeFS(table map[string]storagefs.Result) func([]string) []storagefs.Result {
	return func(paths []string) []storagefs.Result {
		out := make([]storagefs.Result, len(paths))
		for i, p := range paths {
			r, ok := table[p]
			if !ok {
				r = storagefs.Result{Reason: storagefs.ReasonStatFailed, Group: -1}
			}
			out[i] = r
		}
		return out
	}
}

func capacity(avail, total uint64, group int) storagefs.Result {
	return storagefs.Result{Usage: storagefs.Usage{Available: avail, HaveAvailable: true, Total: total, HaveTotal: true}, Group: group}
}

func TestStorageProbeProjection(t *testing.T) {
	live := &config.Config{}
	live.Observability.AccessLog = config.AccessLogConfig{Sinks: []string{"file"}, File: "/data/logs/access.log"}
	p := &storageProbe{
		startup: []storageTarget{{"config", "/etc/jul.toml"}, {"acme", "/data/certs-a"}, {"acme", "/other/certs-b"}},
		live:    func() *config.Config { return live },
		admin: func() []admin.StorageTarget {
			return []admin.StorageTarget{{Category: "audit_log", Path: "/secret/home/alice/audit.jsonl"}, {Category: "plugin_upload", Path: "/plugins"}}
		},
		audit: func() (uint64, bool, bool) { return 3, true, true },
		hints: storagefs.DefaultHints,
		probe: fakeFS(map[string]storagefs.Result{
			"/data/logs/access.log":          capacity(80, 1000, 0),
			"/etc/jul.toml":                  capacity(500, 1000, 1),
			"/data/certs-a":                  capacity(80, 1000, 0),
			"/other/certs-b":                 capacity(30, 1000, 2),
			"/secret/home/alice/audit.jsonl": {Usage: storagefs.Usage{Available: 5, HaveAvailable: true}, ViaParent: true, Group: 3},
			"/plugins":                       {Reason: storagefs.ReasonPermissionDenied, Group: -1},
		}),
	}
	got := p.headroom()
	byCat := map[string]observability.StorageHeadroom{}
	var order []string
	for _, h := range got {
		byCat[h.Category] = h
		order = append(order, h.Category)
	}
	if want := []string{"access_log", "audit_log", "config", "plugin_upload", "acme"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("category order = %v, want %v", order, want)
	}

	al := byCat["access_log"]
	if al.State != "low" || al.AvailableBytes == nil || *al.AvailableBytes != 80 || *al.TotalBytes != 1000 || *al.AvailableRatio != 0.08 {
		t.Fatalf("access_log = %+v", al)
	}
	if al.WriteFailures == nil {
		t.Fatal("access_log must carry its sink's write-failure count")
	}
	// acme reports its worst location (critical, 3%), which is on its own
	// filesystem, so the sibling ACME location on access_log's filesystem
	// does not make them "shared".
	acme := byCat["acme"]
	if acme.State != "critical" || *acme.AvailableRatio != 0.03 || acme.SharedWith != nil {
		t.Fatalf("acme = %+v", acme)
	}
	if al.SharedWith != nil {
		t.Fatalf("access_log sharedWith = %v, want none", al.SharedWith)
	}

	audit := byCat["audit_log"]
	if audit.State != "unavailable" || audit.AvailableRatio != nil || audit.TotalBytes != nil || !audit.PendingCreation {
		t.Fatalf("audit_log without a total = %+v", audit)
	}
	if audit.WriteFailures == nil || *audit.WriteFailures != 3 || !audit.WritesFailing {
		t.Fatalf("audit_log write health = %+v", audit)
	}
	if pu := byCat["plugin_upload"]; pu.State != "error" || pu.Reason != "permission_denied" || pu.AvailableBytes != nil || pu.SharedWith != nil || pu.WriteFailures != nil {
		t.Fatalf("plugin_upload = %+v", pu)
	}
	if c := byCat["config"]; c.State != "ok" || c.WriteFailures != nil {
		t.Fatalf("config = %+v", c)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"/data", "/etc", "/secret", "alice", "/plugins", "/other", "certs"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("projection leaks %q: %s", leak, raw)
		}
	}
}

func TestStorageProbeSharedFilesystem(t *testing.T) {
	p := &storageProbe{
		startup: []storageTarget{{"cache", "/d/cache"}, {"config", "/d/jul.toml"}, {"acme", "/e/certs"}},
		hints:   storagefs.DefaultHints,
		probe: fakeFS(map[string]storagefs.Result{
			"/d/cache": capacity(500, 1000, 0), "/d/jul.toml": capacity(500, 1000, 0), "/e/certs": capacity(1, 2, 1),
		}),
	}
	got := p.headroom()
	if !reflect.DeepEqual(got[0].SharedWith, []string{"config"}) || !reflect.DeepEqual(got[1].SharedWith, []string{"cache"}) || got[2].SharedWith != nil {
		t.Fatalf("sharedWith = %v / %v / %v", got[0].SharedWith, got[1].SharedWith, got[2].SharedWith)
	}
	// A cache category without a live cache has no disk write-health signal.
	if got[0].WriteFailures != nil {
		t.Fatalf("cache without a disk tier reported write health: %+v", got[0])
	}
}

func TestStorageProbeWriteHealthOverlays(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.New(config.CacheConfig{Enabled: true, DiskPath: filepath.Join(dir, "cache")}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	p := &storageProbe{
		startup: []storageTarget{{"cache", filepath.Join(dir, "cache")}},
		admin:   func() []admin.StorageTarget { return []admin.StorageTarget{{Category: "audit_log", Path: dir}} },
		audit:   func() (uint64, bool, bool) { return 0, false, false },
		cache:   c,
		hints:   storagefs.DefaultHints,
	}
	got := p.headroom()
	if len(got) != 2 {
		t.Fatalf("headroom = %+v", got)
	}
	if got[0].Category != "cache" || got[0].WriteFailures == nil || *got[0].WriteFailures != 0 || got[0].WritesFailing {
		t.Fatalf("cache write health = %+v", got[0])
	}
	if got[1].Category != "audit_log" || got[1].WriteFailures != nil {
		t.Fatalf("audit without a durable sink = %+v", got[1])
	}
	// Real filesystem: the values are real, not fabricated.
	if got[0].Reason == "" && (got[0].AvailableBytes == nil || got[0].TotalBytes == nil || *got[0].TotalBytes <= 0) {
		t.Fatalf("real probe produced no capacity: %+v", got[0])
	}
	if _, err := os.Stat(filepath.Join(dir, "cache")); err != nil {
		t.Fatal(err)
	}

	p.audit = nil
	if h := p.headroom(); h[1].WriteFailures != nil {
		t.Fatalf("nil audit hook = %+v", h[1])
	}
}

func TestStorageProbeNoTargets(t *testing.T) {
	p := &storageProbe{live: func() *config.Config { return nil }}
	if got := p.headroom(); got != nil {
		t.Fatalf("no targets = %v", got)
	}
}

func TestStorageHintsView(t *testing.T) {
	if got := storageHintsView(storagefs.DefaultHints); got.LowRatio != 0.10 || got.CriticalRatio != 0.05 {
		t.Fatalf("hints = %+v", got)
	}
}

func TestWorseReadingTieBreaks(t *testing.T) {
	lo, hi := 0.2, 0.5
	a := storageReading{state: storagefs.StateOK, ratio: &lo}
	b := storageReading{state: storagefs.StateOK, ratio: &hi}
	if !worseReading(a, b) || worseReading(b, a) {
		t.Fatal("lower ratio must be worse within one state")
	}
	if worseReading(storageReading{state: storagefs.StateOK}, storageReading{state: storagefs.StateOK}) {
		t.Fatal("ratio-less equal states are not worse")
	}
	if !worseReading(storageReading{state: storagefs.StateLow}, storageReading{state: storagefs.StateError}) {
		t.Fatal("known low headroom outranks an unreadable location")
	}
}
