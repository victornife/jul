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
}

func TestResolveCacheCollectorLockAndLockTimeout(t *testing.T) {
	t.Run("consistent effective lock and timeout across cached locations", func(t *testing.T) {
		tr := &translator{}
		col := &cacheCollector{
			locLocks: []cachedLocationLock{
				{lock: true, lockLine: 1, timeout: config.Duration(3 * time.Second), timeoutLine: 2},
				{lock: true, lockLine: 1, timeout: config.Duration(3 * time.Second), timeoutLine: 2},
			},
		}
		res := tr.resolveCacheCollector(col)
		if !res.hasLock || !res.lock || !res.hasLockTimeout || res.lockTimeout.Std() != 3*time.Second {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("http scope inherited by cached locations with default 5s timeout", func(t *testing.T) {
		src := `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  proxy_cache_lock on;
  server {
    listen 8080;
    location /a {
      proxy_cache z;
      proxy_pass http://127.0.0.1:9000;
    }
    location /b {
      proxy_cache z;
      proxy_pass http://127.0.0.1:9000;
    }
    location /uncached {
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`
		cfg, rep := translate(t, src)
		if len(rep.Skipped) != 0 {
			t.Fatalf("unexpected skips: %+v", rep.Skipped)
		}
		if !cfg.Cache.Lock || cfg.Cache.LockTimeout.Std() != 5*time.Second {
			t.Fatalf("translated cache = %+v, want Lock=true LockTimeout=5s", cfg.Cache)
		}
		assessment, _ := assessString(t, src)
		if assessment.HasBlocking() {
			t.Fatalf("unexpected blocking findings: %+v", assessment.Results)
		}
		if !hasAssessmentCode(assessment, "NGX_HTTP_CACHE_LOCK") {
			t.Fatalf("missing NGX_HTTP_CACHE_LOCK in %+v", assessment.Results)
		}
	})

	t.Run("server scope inherited by cached locations with explicit timeout and uncached location", func(t *testing.T) {
		src := `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  server {
    listen 8080;
    proxy_cache_lock on;
    proxy_cache_lock_timeout 6s;
    location /cached {
      proxy_cache z;
      proxy_pass http://127.0.0.1:9000;
    }
    location /explicit-off {
      proxy_cache off;
      proxy_cache_lock off;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`
		cfg, rep := translate(t, src)
		if len(rep.Skipped) != 0 {
			t.Fatalf("unexpected skips: %+v", rep.Skipped)
		}
		if !cfg.Cache.Lock || cfg.Cache.LockTimeout.Std() != 6*time.Second {
			t.Fatalf("translated cache = %+v, want Lock=true LockTimeout=6s", cfg.Cache)
		}
		assessment, _ := assessString(t, src)
		if assessment.HasBlocking() || !hasAssessmentCode(assessment, "NGX_SERVER_CACHE_LOCK") || !hasAssessmentCode(assessment, "NGX_SERVER_CACHE_LOCK_TIMEOUT") {
			t.Fatalf("unexpected assessment results: %+v", assessment.Results)
		}
	})

	t.Run("location override of http off to on in sole cached location", func(t *testing.T) {
		src := `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  proxy_cache_lock off;
  proxy_cache_lock_timeout 9s;
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
	})

	t.Run("location override of http on to off in sole cached location leaves lock disabled with note for timeout", func(t *testing.T) {
		src := `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  proxy_cache_lock on;
  proxy_cache_lock_timeout 4s;
  server {
    listen 8080;
    location / {
      proxy_cache z;
      proxy_cache_lock off;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`
		cfg, rep := translate(t, src)
		if cfg.Cache.Lock || cfg.Cache.LockTimeout != 0 {
			t.Fatalf("expected lock and lock_timeout unset when effective lock is off, got %+v", cfg.Cache)
		}
		if !hasNote(rep, "effective proxy_cache_lock is off") {
			t.Fatalf("expected note when timeout is present with effective lock off, got %+v", rep.Notes)
		}
	})

	t.Run("one cached location lock on and another cached location omitted default off is blocking conflict", func(t *testing.T) {
		src := `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  server {
    listen 8080;
    location /locked {
      proxy_cache z;
      proxy_cache_lock on;
      proxy_pass http://127.0.0.1:9000;
    }
    location /unlocked-by-default {
      proxy_cache z;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`
		cfg, rep := translate(t, src)
		if cfg.Cache.Lock || cfg.Cache.LockTimeout != 0 {
			t.Fatalf("expected global lock left untranslated on mixed effective lock, got %+v", cfg.Cache)
		}
		if !hasSkip(rep, "effective proxy_cache_lock") {
			t.Fatalf("expected skip for mixed effective lock, got %+v", rep.Skipped)
		}
		assessment, _ := assessString(t, src)
		if !assessment.HasBlocking() || !hasAssessmentCode(assessment, "NGX_CACHE_LOCK_CONFLICT") {
			t.Fatalf("expected blocking NGX_CACHE_LOCK_CONFLICT finding, got %+v", assessment.Results)
		}
	})

	t.Run("http lock on with one cached location overriding timeout and another inheriting 5s default is blocking conflict", func(t *testing.T) {
		src := `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  proxy_cache_lock on;
  server {
    listen 8080;
    location /fast-timeout {
      proxy_cache z;
      proxy_cache_lock_timeout 2s;
      proxy_pass http://127.0.0.1:9000;
    }
    location /default-5s-timeout {
      proxy_cache z;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`
		cfg, rep := translate(t, src)
		if cfg.Cache.Lock || cfg.Cache.LockTimeout != 0 {
			t.Fatalf("expected global lock and timeout left untranslated on mixed effective timeout, got %+v", cfg.Cache)
		}
		if !hasSkip(rep, "effective proxy_cache_lock_timeout") {
			t.Fatalf("expected skip for mixed effective timeout, got %+v", rep.Skipped)
		}
		assessment, _ := assessString(t, src)
		if !assessment.HasBlocking() || !hasAssessmentCode(assessment, "NGX_CACHE_LOCK_CONFLICT") {
			t.Fatalf("expected blocking NGX_CACHE_LOCK_CONFLICT finding, got %+v", assessment.Results)
		}
	})

	t.Run("http lock on with one cached location explicitly setting 5s and another inheriting 5s default is compatible", func(t *testing.T) {
		src := `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  proxy_cache_lock on;
  server {
    listen 8080;
    location /explicit-5s {
      proxy_cache z;
      proxy_cache_lock_timeout 5s;
      proxy_pass http://127.0.0.1:9000;
    }
    location /implicit-5s {
      proxy_cache z;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`
		cfg, rep := translate(t, src)
		if len(rep.Skipped) != 0 {
			t.Fatalf("unexpected skips: %+v", rep.Skipped)
		}
		if !cfg.Cache.Lock || cfg.Cache.LockTimeout.Std() != 5*time.Second {
			t.Fatalf("expected Lock=true LockTimeout=5s, got %+v", cfg.Cache)
		}
	})

	t.Run("proxy_cache_lock_age and invalid lock directives block at http server and location scopes", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			src  string
			code string
		}{
			{
				name: "http lock_age",
				src:  "http { proxy_cache_lock_age 5s; server { listen 8080; location / { return 200; } } }",
				code: "NGX_HTTP_CACHE_LOCK_AGE",
			},
			{
				name: "server lock_age",
				src:  "http { server { listen 8080; proxy_cache_lock_age 5s; location / { return 200; } } }",
				code: "NGX_SERVER_CACHE_LOCK_AGE",
			},
			{
				name: "location lock_age",
				src:  "http { server { listen 8080; location / { proxy_cache_lock_age 5s; return 200; } } }",
				code: "NGX_LOCATION_CACHE_LOCK_AGE",
			},
			{
				name: "http proxy_cache_max_range_offset",
				src:  "http { proxy_cache_max_range_offset 1m; server { listen 8080; location / { return 200; } } }",
				code: "NGX_HTTP_CACHE_MAX_RANGE_OFFSET",
			},
			{
				name: "server proxy_cache_max_range_offset",
				src:  "http { server { listen 8080; proxy_cache_max_range_offset 1m; location / { return 200; } } }",
				code: "NGX_SERVER_CACHE_MAX_RANGE_OFFSET",
			},
			{
				name: "location proxy_cache_max_range_offset",
				src:  "http { server { listen 8080; location / { proxy_cache_max_range_offset 1m; return 200; } } }",
				code: "NGX_LOCATION_CACHE_MAX_RANGE_OFFSET",
			},
			{
				name: "http slice",
				src:  "http { slice 1m; server { listen 8080; location / { return 200; } } }",
				code: "NGX_HTTP_SLICE",
			},
			{
				name: "server slice",
				src:  "http { server { listen 8080; slice 1m; location / { return 200; } } }",
				code: "NGX_SERVER_SLICE",
			},
			{
				name: "location slice",
				src:  "http { server { listen 8080; location / { slice 1m; return 200; } } }",
				code: "NGX_LOCATION_SLICE",
			},
			{
				name: "invalid lock and timeout",
				src: `http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  server {
    listen 8080;
    location / {
      proxy_cache;
      proxy_cache z;
      proxy_cache_lock invalid;
      proxy_cache_lock_timeout 0s;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`,
				code: "NGX_LOCATION_CACHE_LOCK",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cfg, _ := translate(t, tc.src)
				if cfg.Cache.Lock || cfg.Cache.LockTimeout != 0 {
					t.Fatalf("expected lock unset on invalid/blocking input, got %+v", cfg.Cache)
				}
				invAssess, _ := assessString(t, tc.src)
				if !invAssess.HasBlocking() || !hasAssessmentCode(invAssess, tc.code) {
					t.Fatalf("expected blocking %s in %+v", tc.code, invAssess.Results)
				}
			})
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
