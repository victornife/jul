// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Historical ledgers already persisted an immutable resource snapshot for tag
// purges. [] proves there were no targets; absent/null cannot prove that.
func TestLegacyTagLedgerRecoveryUsesOriginalSnapshot(t *testing.T) {
	const host = "example.com"
	const uri = "/article?private=original"
	const cutoff = 100
	resourceID := hashKey(resourceKeyOf(host, uri))
	for _, terminal := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			snapshot json.RawMessage
			targeted bool
			broad    bool
		}{
			{name: "populated", snapshot: json.RawMessage(fmt.Sprintf(`["%s"]`, resourceID)), targeted: true},
			{name: "empty", snapshot: json.RawMessage(`[]`)},
			{name: "missing", broad: true},
			{name: "null", snapshot: json.RawMessage(`null`), broad: true},
			{name: "invalid", snapshot: json.RawMessage(`["not-a-resource-hash"]`), broad: true},
		} {
			t.Run(fmt.Sprintf("%s/terminal=%t", tc.name, terminal), func(t *testing.T) {
				t.Parallel()
				c, cfg := recoveryTestCache(t)
				targets := []string{"GET\n" + host + "\n" + uri, "HEAD\n" + host + "\n" + uri}
				unrelated := []string{"GET\nexample.com\n/other", "GET\nother.example.com\n/keep"}
				for _, key := range append(append([]string{}, targets...), unrelated...) {
					seedRecoveryEntry(c, key)
					if _, err := os.Stat(filepath.Join(cfg.DiskPath, hashKey(key))); err != nil {
						t.Fatalf("fixture not on disk: %v", err)
					}
				}
				// Even the broad fallback must preserve post-cutoff publications.
				c.genSeq.Store(cutoff + 1)
				fresh := "GET\nexample.com\n/fresh"
				seedRecoveryEntry(c, fresh)
				record := map[string]any{
					"response": map[string]any{
						"fence_sequence": cutoff, "terminal": terminal, "invalidation_effective": "uncertain",
						"selector": map[string]any{"kind": "tag", "host": host, "tags": []string{"historical-tag"}, "tag_match": "any"},
					},
				}
				if tc.snapshot != nil {
					record["matched_resource_ids"] = tc.snapshot
				}
				ledger, err := json.Marshal([]any{record})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfg.DiskPath, diskPurgeLedgerFile), ledger, 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(cfg.DiskPath, diskPurgeIntentsFile)); !os.IsNotExist(err) {
					t.Fatalf("fixture must recover solely from historical ledger: %v", err)
				}
				// Tag ingestion is disabled on this boot: recovery must use the
				// original IDs, not reconstruct ownership from current tags.
				for boot := 0; boot < 2; boot++ {
					restarted, err := New(cfg, nil)
					if err != nil {
						t.Fatal(err)
					}
					for _, key := range targets {
						_, hit := restarted.get(key)
						if want := !(tc.targeted || tc.broad); hit != want {
							t.Errorf("boot %d target %q hit=%t want=%t", boot, key, hit, want)
						}
					}
					for _, key := range unrelated {
						_, hit := restarted.get(key)
						if want := !tc.broad; hit != want {
							t.Errorf("boot %d unrelated %q hit=%t want=%t", boot, key, hit, want)
						}
					}
					if _, hit := restarted.get(fresh); !hit {
						t.Errorf("boot %d removed post-cutoff publication", boot)
					}
					if !restarted.WasPurgeFenceReconciledOnStartup(cutoff) {
						t.Errorf("boot %d did not reconcile original cutoff", boot)
					}
				}
			})
		}
	}
}
