// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Covers both independently reachable journal limits with real disk recovery.
// The declared local startup budget is 10s for 10k disk entries, including
// rehydration, selector evaluation, tombstones, unlink, and manifest cleanup.
func TestMeasure667RecoveryJournalBudgets(t *testing.T) {
	if os.Getenv("RUN_667_DURABLE_PROFILE") != "1" {
		t.Skip("set RUN_667_DURABLE_PROFILE=1 for journal-limit startup profiles")
	}
	for _, mode := range []string{"256_operations", "near_1MiB"} {
		for sample := 1; sample <= 3; sample++ {
			t.Run(fmt.Sprintf("%s_sample_%d", mode, sample), func(t *testing.T) {
				c, cfg := recoveryTestCache(t)
				cfg.DiskMaxSize = 128 << 20
				// Expand the fixture before inserting real entries.
				c.disk.maxBytes = cfg.DiskMaxSize.Bytes()
				const entries = 10000
				ids := make([][]string, 4)
				for i := 0; i < entries; i++ {
					uri := fmt.Sprintf("/group/%d/%d", i%256, i)
					seedRecoveryEntry(c, "GET\nexample.com\n"+uri)
					ids[i%4] = append(ids[i%4], hashKey(resourceKeyOf("example.com", uri)))
				}
				fence := c.genSeq.Load() + 1
				if mode == "256_operations" {
					for i := range MaxActiveFences {
						if err := c.disk.recordPurgeIntent(PurgeRecoveryIntent{FenceGen: fence + uint64(i), Kind: SelectorPrefix, Host: "example.com", Prefix: fmt.Sprintf("/group/%d/", i)}); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					for i := range 4 {
						for len(ids[i]) < 3900 {
							ids[i] = append(ids[i], hashKey(fmt.Sprintf("evicted-resource-%d-%d", i, len(ids[i]))))
						}
						if err := c.disk.recordPurgeIntent(PurgeRecoveryIntent{FenceGen: fence + uint64(i), Kind: SelectorTag, Host: "example.com", Tags: []string{"old-tag"}, MatchedResourceIDs: ids[i]}); err != nil {
							t.Fatal(err)
						}
					}
				}
				raw, err := os.ReadFile(filepath.Join(cfg.DiskPath, diskPurgeIntentsFile))
				if err != nil {
					t.Fatal(err)
				}
				if mode == "near_1MiB" && (len(raw) < maxDiskMetaBytes*99/100 || len(raw) > maxDiskMetaBytes) {
					t.Fatalf("journal not at byte limit: %d", len(raw))
				}
				start := time.Now()
				restarted, err := New(cfg, nil)
				elapsed := time.Since(start)
				if err != nil {
					t.Fatal(err)
				}
				if _, hit := restarted.get("GET\nexample.com\n/group/0/0"); hit {
					t.Fatal("target survived recovery")
				}
				if _, err := os.Stat(filepath.Join(cfg.DiskPath, diskPurgeIntentsFile)); !os.IsNotExist(err) {
					t.Fatalf("journal not reconciled: %v", err)
				}
				t.Logf("mode=%s sample=%d disk_entries=%d journal_bytes=%d startup=%s", mode, sample, entries, len(raw), elapsed)
				if elapsed > 10*time.Second {
					t.Fatal("declared local startup budget exceeded")
				}
			})
		}
	}
}
