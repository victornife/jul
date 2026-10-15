// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"testing"
	"time"

	"jul/internal/config"
)

func TestParseCacheZonePath(t *testing.T) {
	tests := []struct {
		name       string
		params     []string
		wantOK     bool
		wantName   string
		wantPath   string
		wantMax    config.Size
		wantHasMax bool
	}{
		{"missing", nil, false, "", "", 0, false},
		{"empty path", []string{"", "keys_zone=z:10m"}, false, "", "", 0, false},
		{"variable path", []string{"$var", "keys_zone=z:10m"}, false, "", "", 0, false},
		{"no keys_zone", []string{"/var/cache", "levels=1:2"}, false, "", "", 0, false},
		{"keys_zone missing colon", []string{"/var/cache", "keys_zone=zonly"}, false, "", "", 0, false},
		{"keys_zone empty name", []string{"/var/cache", "keys_zone=:10m"}, false, "", "", 0, false},
		{"bad max_size", []string{"/var/cache", "keys_zone=z:10m", "max_size=nope"}, false, "", "", 0, false},
		{"valid without max_size", []string{"/var/cache", "keys_zone=z:10m"}, true, "z", "/var/cache", 0, false},
		{"valid with max_size", []string{"/var/cache", "keys_zone=z:10m", "max_size=100m"}, true, "z", "/var/cache", config.Size(100 << 20), true},
		{"ignores unknown tuning params", []string{"/var/cache", "levels=1:2", "keys_zone=z:10m", "inactive=60m", "use_temp_path=off"}, true, "z", "/var/cache", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def, name, ok := parseCacheZonePath(tt.params)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if name != tt.wantName || def.path != tt.wantPath || def.hasMaxSize != tt.wantHasMax || def.maxSize != tt.wantMax {
				t.Errorf("got name=%q def=%+v, want name=%q path=%q max=%v hasMax=%v", name, def, tt.wantName, tt.wantPath, tt.wantMax, tt.wantHasMax)
			}
		})
	}
}

func TestParseSimpleCacheValidTime(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		wantOK bool
		want   time.Duration
	}{
		{"empty", nil, false, 0},
		{"bare time", []string{"10m"}, true, 10 * time.Minute},
		{"bad time", []string{"nope"}, false, 0},
		{"default codes", []string{"200", "302", "5m"}, true, 5 * time.Minute},
		{"single default code", []string{"200", "1m"}, true, time.Minute},
		{"any keyword rejected", []string{"any", "1m"}, false, 0},
		{"non-default code rejected", []string{"404", "1m"}, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := parseSimpleCacheValidTime(tt.params)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && d.Std() != tt.want {
				t.Errorf("duration = %s, want %s", d.Std(), tt.want)
			}
		})
	}
}

func TestResolveCacheCollectorSingleZoneAndTTL(t *testing.T) {
	tr := &translator{}
	col := &cacheCollector{
		zones: map[string]cacheZoneDef{"z": {path: "/var/cache", maxSize: config.Size(1 << 20), hasMaxSize: true}},
		uses:  []cacheUse{{zone: "z", line: 5}, {zone: "z", line: 9}},
		ttls:  []cacheTTLCandidate{{ttl: config.Duration(time.Minute), line: 5}, {ttl: config.Duration(time.Minute), line: 9}},
	}
	res := tr.resolveCacheCollector(col)
	if !res.hasZone || res.zoneName != "z" || res.def.path != "/var/cache" {
		t.Fatalf("got %+v", res)
	}
	if !res.hasTTL || res.ttl.Std() != time.Minute {
		t.Fatalf("ttl = %+v", res)
	}
	if len(tr.report.Skipped) != 0 {
		t.Errorf("expected no skips, got %+v", tr.report.Skipped)
	}
}

func TestResolveCacheCollectorUndeclaredZoneConflict(t *testing.T) {
	tr := &translator{}
	col := &cacheCollector{
		zones: map[string]cacheZoneDef{},
		uses:  []cacheUse{{zone: "missing", line: 7}},
	}
	res := tr.resolveCacheCollector(col)
	if res.hasZone {
		t.Fatalf("expected no zone resolved, got %+v", res)
	}
	if len(tr.report.Skipped) != 1 || tr.report.Skipped[0].Line != 7 {
		t.Fatalf("expected one skip at line 7, got %+v", tr.report.Skipped)
	}
	if !hasSkip(&tr.report, "cache zone") {
		t.Errorf("expected a cache zone finding, got %+v", tr.report.Skipped)
	}
}

func TestResolveCacheCollectorMultiZoneConflict(t *testing.T) {
	tr := &translator{}
	col := &cacheCollector{
		zones: map[string]cacheZoneDef{"a": {path: "/a"}, "b": {path: "/b"}},
		uses:  []cacheUse{{zone: "a", line: 1}, {zone: "b", line: 2}},
	}
	res := tr.resolveCacheCollector(col)
	if res.hasZone {
		t.Fatalf("expected no zone resolved, got %+v", res)
	}
	if len(tr.report.Skipped) != 2 {
		t.Fatalf("expected two skips (one per use), got %+v", tr.report.Skipped)
	}
	for _, f := range tr.report.Skipped {
		if !hasSkip(&tr.report, "process-wide cache zone") {
			t.Errorf("expected the multi-zone reason, got %q", f.Reason)
		}
	}
}

func TestResolveCacheCollectorConflictingTTLNote(t *testing.T) {
	tr := &translator{}
	col := &cacheCollector{
		ttls: []cacheTTLCandidate{{ttl: config.Duration(time.Minute), line: 1}, {ttl: config.Duration(2 * time.Minute), line: 2}},
	}
	res := tr.resolveCacheCollector(col)
	if res.hasTTL {
		t.Fatalf("expected no TTL resolved, got %+v", res)
	}
	if !hasNote(&tr.report, "conflicting proxy_cache_valid") {
		t.Errorf("expected a conflicting-TTL note, got %+v", tr.report.Notes)
	}
}

func TestApplyHTTPCacheConfig(t *testing.T) {
	t.Run("no zone leaves cache untouched", func(t *testing.T) {
		tr := &translator{}
		out := &config.Config{}
		tr.applyHTTPCacheConfig(out)
		if out.Cache.Enabled {
			t.Errorf("expected cache disabled, got %+v", out.Cache)
		}
	})
	t.Run("zone enables cache with disk fields", func(t *testing.T) {
		tr := &translator{httpCache: httpCacheResolution{
			hasZone:  true,
			zoneName: "z",
			def:      cacheZoneDef{path: "/var/cache", maxSize: config.Size(5 << 20), hasMaxSize: true},
			hasTTL:   true,
			ttl:      config.Duration(30 * time.Second),
		}}
		out := &config.Config{}
		tr.applyHTTPCacheConfig(out)
		if !out.Cache.Enabled || out.Cache.DiskPath != "/var/cache" || out.Cache.DiskMaxSize != config.Size(5<<20) || out.Cache.DefaultTTL.Std() != 30*time.Second {
			t.Errorf("got %+v", out.Cache)
		}
	})
	t.Run("zone without max size or ttl", func(t *testing.T) {
		tr := &translator{httpCache: httpCacheResolution{hasZone: true, zoneName: "z", def: cacheZoneDef{path: "/var/cache"}}}
		out := &config.Config{}
		tr.applyHTTPCacheConfig(out)
		if !out.Cache.Enabled || out.Cache.DiskMaxSize != 0 || out.Cache.DefaultTTL != 0 {
			t.Errorf("got %+v", out.Cache)
		}
	})
	t.Run("zone with lock and lock_timeout", func(t *testing.T) {
		tr := &translator{httpCache: httpCacheResolution{
			hasZone:        true,
			zoneName:       "z",
			def:            cacheZoneDef{path: "/var/cache"},
			hasLock:        true,
			lock:           true,
			hasLockTimeout: true,
			lockTimeout:    config.Duration(4 * time.Second),
		}}
		out := &config.Config{}
		tr.applyHTTPCacheConfig(out)
		if !out.Cache.Lock || out.Cache.LockTimeout.Std() != 4*time.Second {
			t.Errorf("got %+v", out.Cache)
		}
	})
	t.Run("lock_timeout without lock enabled leaves timeout unset with note", func(t *testing.T) {
		tr := &translator{httpCache: httpCacheResolution{
			hasZone:        true,
			zoneName:       "z",
			def:            cacheZoneDef{path: "/var/cache"},
			hasLockTimeout: true,
			lockTimeout:    config.Duration(4 * time.Second),
		}}
		out := &config.Config{}
		tr.applyHTTPCacheConfig(out)
		if out.Cache.Lock || out.Cache.LockTimeout != 0 {
			t.Errorf("expected lock and lock_timeout unset, got %+v", out.Cache)
		}
		if !hasNote(&tr.report, "proxy_cache_lock_timeout was left unset") {
			t.Errorf("expected note for lock_timeout without lock, got %+v", tr.report.Notes)
		}
	})
}

func TestResolveCacheCollectorLockAndLockTimeout(t *testing.T) {
	t.Run("consistent lock and lock_timeout", func(t *testing.T) {
		tr := &translator{}
		col := &cacheCollector{
			locks:        []cacheLockCandidate{{enabled: true, line: 1}, {enabled: true, line: 2}},
			lockTimeouts: []cacheLockTimeoutCandidate{{timeout: config.Duration(3 * time.Second), line: 1}, {timeout: config.Duration(3 * time.Second), line: 2}},
		}
		res := tr.resolveCacheCollector(col)
		if !res.hasLock || !res.lock || !res.hasLockTimeout || res.lockTimeout.Std() != 3*time.Second {
			t.Fatalf("got %+v", res)
		}
	})
	t.Run("conflicting lock and lock_timeout emit notes", func(t *testing.T) {
		tr := &translator{}
		col := &cacheCollector{
			locks:        []cacheLockCandidate{{enabled: true, line: 1}, {enabled: false, line: 2}},
			lockTimeouts: []cacheLockTimeoutCandidate{{timeout: config.Duration(3 * time.Second), line: 1}, {timeout: config.Duration(6 * time.Second), line: 2}},
		}
		res := tr.resolveCacheCollector(col)
		if res.hasLock || res.hasLockTimeout {
			t.Fatalf("expected neither resolved on conflict, got %+v", res)
		}
		if !hasNote(&tr.report, "conflicting proxy_cache_lock") {
			t.Errorf("missing conflicting lock note: %+v", tr.report.Notes)
		}
		if !hasNote(&tr.report, "conflicting proxy_cache_lock_timeout") {
			t.Errorf("missing conflicting lock_timeout note: %+v", tr.report.Notes)
		}
	})
	t.Run("translate and assess end-to-end location lock directives", func(t *testing.T) {
		src := `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  server {
    listen 8080;
    location / {
      proxy_cache z;
      proxy_cache_lock on;
      proxy_cache_lock_timeout 4s;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`
		cfg, rep := translate(t, src)
		if len(rep.Skipped) != 0 {
			t.Fatalf("unexpected skips: %+v", rep.Skipped)
		}
		if !cfg.Cache.Lock || cfg.Cache.LockTimeout.Std() != 4*time.Second {
			t.Fatalf("translated cache = %+v, want Lock=true LockTimeout=4s", cfg.Cache)
		}
		assessment, _ := assessString(t, src)
		var sawLock, sawTimeout bool
		for _, r := range assessment.Results {
			if r.Code == "NGX_LOCATION_CACHE_LOCK" && r.Class == AssessmentApproximated {
				sawLock = true
			}
			if r.Code == "NGX_LOCATION_CACHE_LOCK_TIMEOUT" && r.Class == AssessmentApproximated {
				sawTimeout = true
			}
		}
		if !sawLock || !sawTimeout {
			t.Fatalf("missing lock assessment results: %+v", assessment.Results)
		}

		invalidSrc := `
http {
  server {
    listen 8080;
    location / {
      proxy_cache_lock invalid;
      proxy_cache_lock_timeout 0s;
      proxy_cache_lock_age 5s;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`
		invAssess, _ := assessString(t, invalidSrc)
		if !invAssess.HasBlocking() {
			t.Fatalf("expected blocking findings for invalid lock directives and proxy_cache_lock_age: %+v", invAssess.Results)
		}
	})
}

func TestApplyProxyCache(t *testing.T) {
	tr := &translator{httpCache: httpCacheResolution{hasZone: true, zoneName: "z"}}
	tests := []struct {
		name string
		arg  string
		want bool
	}{
		{"matching zone", "z", true},
		{"empty", "", false},
		{"off", "off", false},
		{"variable", "$var", false},
		{"non-matching zone", "other", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc := &config.LocationConfig{}
			tr.applyProxyCache(loc, tt.arg)
			if loc.Cache != tt.want {
				t.Errorf("loc.Cache = %v, want %v", loc.Cache, tt.want)
			}
		})
	}
	t.Run("no resolved zone never enables cache", func(t *testing.T) {
		unresolved := &translator{}
		loc := &config.LocationConfig{}
		unresolved.applyProxyCache(loc, "z")
		if loc.Cache {
			t.Errorf("expected cache to stay disabled with no resolved zone")
		}
	})
}
