// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/versionid"
)

func recoveryTestCache(t *testing.T) (*Cache, config.CacheConfig) {
	t.Helper()
	cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 2 << 20, DiskPath: t.TempDir(), DiskMaxSize: 16 << 20, DefaultTTL: config.Duration(10 * time.Minute)}
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, cfg
}

func seedRecoveryEntry(c *Cache, key string) {
	now := time.Now()
	c.set(key, &Entry{Status: 200, Body: []byte("old"), CreatedAt: now, ExpiresAt: now.Add(time.Hour), StaleUntil: now.Add(time.Hour)})
}

// Snapshot at the ledger commit, earlier than the existing fence-install hook.
func TestPurgeRecoveryAtCutoffCommit(t *testing.T) {
	for _, kind := range []SelectorKind{SelectorExactURL, SelectorKey, SelectorPrefix, SelectorAll} {
		t.Run(string(kind), func(t *testing.T) {
			c, cfg := recoveryTestCache(t)
			key := "GET\nexample.com\n/article?secret=value"
			seedRecoveryEntry(c, key)
			seedRecoveryEntry(c, "GET\nother.example.com\n/keep")
			raw := Selector{Kind: kind}
			switch kind {
			case SelectorExactURL:
				raw.Host, raw.URI = "example.com", "/article?secret=value"
			case SelectorKey:
				raw.Key = key
			case SelectorPrefix:
				raw.Host, raw.Prefix = "example.com", "/article"
			case SelectorAll:
				raw.All = true
			}
			sel, err := ParseSelector(raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			snap := t.TempDir()
			var fence uint64
			_, err = c.InvalidateScope(context.Background(), sel, InvalidateOptions{CommitFence: func(gen uint64, _ []string, intent PurgeRecoveryIntent) error {
				fence = gen
				// This is the redacted form durable admin records actually contain.
				identity, err := json.Marshal(intent)
				if err != nil {
					t.Fatal(err)
				}
				ledger := fmt.Sprintf(`[{"recovery_intent":%s,"response":{"fence_sequence":%d,"terminal":false,"selector":{"kind":%q,"host":"example.com","uri":"/article?<redacted>","key":"GET\nexample.com\n/article?<redacted>","prefix":"/article"}}}]`, identity, gen, kind)
				if err := os.WriteFile(filepath.Join(cfg.DiskPath, diskPurgeLedgerFile), []byte(ledger), 0600); err != nil {
					t.Fatal(err)
				}
				copyCacheDirSnapshot(t, cfg.DiskPath, snap)
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(snap, diskPurgeIntentsFile)); err != nil {
				t.Fatal(err)
			}
			cfg.DiskPath = snap
			restarted, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, hit := restarted.get(key); hit {
				t.Fatal("committed target survived restart")
			}
			if !restarted.WasPurgeFenceReconciledOnStartup(fence) {
				t.Fatal("complete intent was not reconciled")
			}
			if kind != SelectorAll {
				if _, hit := restarted.get("GET\nother.example.com\n/keep"); !hit {
					t.Fatal("unrelated entry lost")
				}
			}
		})
	}
}

func TestPurgeRecoveryKeyPreservesOtherMethod(t *testing.T) {
	c, cfg := recoveryTestCache(t)
	get, head := "GET\nexample.com\n/article", "HEAD\nexample.com\n/article"
	seedRecoveryEntry(c, get)
	seedRecoveryEntry(c, head)
	sel, err := ParseSelector(Selector{Kind: SelectorKey, Key: get}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap := t.TempDir()
	c.onAfterFenceInstall = func(NormalizedSelector, uint64) { copyCacheDirSnapshot(t, cfg.DiskPath, snap) }
	if _, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, hit := c.get(head); !hit {
		t.Fatal("live purge removed HEAD")
	}
	cfg.DiskPath = snap
	restarted, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, hit := restarted.get(get); hit {
		t.Fatal("GET survived recovery")
	}
	if _, hit := restarted.get(head); !hit {
		t.Fatal("key recovery broadened to HEAD")
	}
}

func TestPurgeIntentByteCapacityPreservesUnrelatedDisk(t *testing.T) {
	c, _ := recoveryTestCache(t)
	key := "GET\nother.example.com\n/keep"
	seedRecoveryEntry(c, key)
	ids := make([]string, 4096)
	for i := range ids {
		ids[i] = hashKey(fmt.Sprint(i))
	}
	var rejected bool
	for i := 1; i <= 4; i++ {
		err := c.disk.recordPurgeIntent(PurgeRecoveryIntent{FenceGen: uint64(i), Kind: SelectorTag, Host: "example.com", Tags: []string{"tag"}, MatchedResourceIDs: ids})
		if err != nil {
			rejected = true
			break
		}
	}
	if !rejected {
		t.Fatal("expected byte capacity rejection")
	}
	if _, err := os.Stat(filepath.Join(c.disk.dir, hashKey(key))); err != nil {
		t.Fatalf("capacity pressure destroyed unrelated disk entry: %v", err)
	}
	if c.disk.unrecoverableInvalidation.Load() {
		t.Fatal("capacity pressure disabled disk")
	}
	before, err := os.ReadFile(filepath.Join(c.disk.dir, diskPurgeIntentsFile))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.com", Tags: []string{"tag"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A new tag snapshot of the same size must reject before CommitFence.
	c.genMu.Lock()
	c.genSeq.Store(10)
	c.genMu.Unlock()
	// Count capacity exercises the same admission path without 4,096 live keys.
	c.disk.purgeIntentMu.Lock()
	for len(c.disk.purgeIntents) < MaxActiveFences {
		c.disk.purgeIntents = append(c.disk.purgeIntents, PurgeRecoveryIntent{FenceGen: uint64(100 + len(c.disk.purgeIntents)), Kind: SelectorAll})
	}
	c.disk.purgeIntentMu.Unlock()
	_, err = c.InvalidateScope(context.Background(), sel, InvalidateOptions{CommitFence: func(uint64, []string, PurgeRecoveryIntent) error { called = true; return nil }})
	if !errors.Is(err, ErrPurgeIntentCapacity) || called {
		t.Fatalf("capacity crossed cutoff: called=%t err=%v", called, err)
	}
	after, err := os.ReadFile(filepath.Join(c.disk.dir, diskPurgeIntentsFile))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejection rewrote durable intents")
	}
}

func TestPurgeRecoveryVariantKeyAndRotatedSecret(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(fmt.Sprint(rotate), func(t *testing.T) {
			c, cfg := recoveryTestCache(t)
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=600")
				w.Header().Set("Vary", "Accept-Language")
				_, _ = w.Write([]byte(r.Header.Get("Accept-Language")))
			}))
			base := "GET\nexample.com\n/article?secret=value"
			keys := make([]string, 0, 2)
			for _, lang := range []string{"en", "es"} {
				req := httptest.NewRequest("GET", "http://example.com/article?secret=value", nil)
				req.Header.Set("Accept-Language", lang)
				h.ServeHTTP(httptest.NewRecorder(), req)
				keys = append(keys, variantKey(base, []string{"Accept-Language"}, req))
			}
			sel := NormalizedSelector{Kind: SelectorKey, Key: keys[0], Host: "example.com", URI: "/article?secret=value"}
			snap := t.TempDir()
			c.onAfterFenceInstall = func(NormalizedSelector, uint64) { copyCacheDirSnapshot(t, cfg.DiskPath, snap) }
			if _, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{}); err != nil {
				t.Fatal(err)
			}
			cfg.DiskPath = snap
			vctx := c.versionContext()
			if rotate {
				vctx, _ = versionid.New(bytes.Repeat([]byte{0x58}, 32))
			}
			restarted, err := NewWithVersionContext(cfg, nil, vctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, hit := restarted.get(keys[0]); hit {
				t.Fatal("target variant survived")
			}
			if _, hit := restarted.get(keys[1]); !hit {
				t.Fatal("sibling variant lost")
			}
			if _, hit := restarted.get(base); !hit {
				t.Fatal("Vary stub lost")
			}
		})
	}
}

func TestPurgeLegacyLedgerMissingIdentitySweepsActualCutoff(t *testing.T) {
	for _, kind := range []string{"exact_url", "key", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			c, cfg := recoveryTestCache(t)
			old := "GET\nexample.com\n/article?secret=value"
			fresh := "GET\nexample.com\n/fresh"
			seedRecoveryEntry(c, old)
			c.genSeq.Store(199)
			seedRecoveryEntry(c, fresh)
			ledger := fmt.Sprintf(`[{"response":{"fence_sequence":100,"terminal":false,"selector":{"kind":%q,"host":"example.com","uri":"/article?<redacted>"}}}]`, kind)
			if err := os.WriteFile(filepath.Join(cfg.DiskPath, diskPurgeLedgerFile), []byte(ledger), 0600); err != nil {
				t.Fatal(err)
			}
			restarted, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, hit := restarted.get(old); hit {
				t.Fatal("unreconstructible record reconciled without retiring old body")
			}
			if _, hit := restarted.get(fresh); !hit {
				t.Fatal("legacy fallback removed post-cutoff fill")
			}
			if !restarted.WasPurgeFenceReconciledOnStartup(100) {
				t.Fatal("actual cutoff sweep not recorded")
			}
		})
	}
}

func TestPurgeRetryRetainsOriginalRecoveryHashes(t *testing.T) {
	c, _ := recoveryTestCache(t)
	original := PurgeRecoveryIntent{FenceGen: 10, Kind: SelectorKey, TargetHashes: []string{hashKey("variant")}}
	if err := c.disk.recordPurgeIntent(original); err != nil {
		t.Fatal(err)
	}
	got, err := c.disk.commitPurgeIntent(PurgeRecoveryIntent{FenceGen: 10, Kind: SelectorKey, TargetHashes: []string{hashKey("base")}})
	if err != nil || len(got.TargetHashes) != 1 || got.TargetHashes[0] != original.TargetHashes[0] {
		t.Fatalf("retry replaced original identity: %+v %v", got, err)
	}
}
