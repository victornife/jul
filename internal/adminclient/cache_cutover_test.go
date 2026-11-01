// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminclient

import (
	"encoding/json"
	"testing"
)

func TestPreparedCacheCutoverOption(t *testing.T) {
	c := &Client{}
	for _, mode := range []string{"retain", "invalidate"} {
		p, err := c.PrepareApply([]byte("#same bytes"), "base", "hot", "cutover-key", false, mode)
		if err != nil {
			t.Fatal(err)
		}
		if p.Query.Get("cache_cutover") != mode || string(p.Body) != "#same bytes" {
			t.Fatalf("lost override/body: %+v", p)
		}
		rb, err := c.PrepareRollback("snap", "base", "cutover-key", false, mode)
		if err != nil {
			t.Fatal(err)
		}
		var request map[string]string
		if err := json.Unmarshal(rb.Body, &request); err != nil {
			t.Fatal(err)
		}
		if request["cache_cutover"] != mode {
			t.Fatalf("rollback=%s", rb.Body)
		}
	}
	if _, err := c.PrepareApply(nil, "base", "stage_restart", "cutover-key", false, "retain"); err == nil {
		t.Fatal("staged ephemeral override accepted")
	}
	if _, err := c.PrepareApply(nil, "base", "hot", "cutover-key", false, "bad"); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if _, err := c.PrepareRollback("snap", "base", "cutover-key", false, "bad"); err == nil {
		t.Fatal("invalid rollback mode accepted")
	}
}
