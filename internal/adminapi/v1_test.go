// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestTimestampIsRFC3339Z pins ADR 0019 §24a's wire rule. A local offset would
// make two servers in different zones report the same instant differently, and
// a zero time rendered as year 1 would be published as a real timestamp.
func TestTimestampIsRFC3339Z(t *testing.T) {
	if got := Timestamp(time.Time{}); got != "" {
		t.Fatalf("the zero time rendered as %q; an absent timestamp is omitted, not published as year 1", got)
	}

	utc := time.Date(2026, 8, 31, 12, 34, 56, 0, time.UTC)
	if got := Timestamp(utc); got != "2026-08-31T12:34:56Z" {
		t.Fatalf("Timestamp(utc) = %q", got)
	}

	// A non-UTC instant must render as the same instant with a Z offset, not
	// with the original zone's offset.
	zone := time.FixedZone("UTC+5", 5*60*60)
	if got := Timestamp(utc.In(zone)); got != "2026-08-31T12:34:56Z" {
		t.Fatalf("a non-UTC instant rendered as %q; §24a requires a Z offset", got)
	}
	if strings.Contains(Timestamp(utc.In(zone)), "+") {
		t.Fatal("the rendering carried a numeric offset")
	}
}

// TestStatusResponseFlattensTheAuthorityState proves the embedded struct
// reaches the wire flat, which is what the generated schema describes. A nested
// object here would make every published client wrong about four fields.
func TestStatusResponseFlattensTheAuthorityState(t *testing.T) {
	b, err := json.Marshal(StatusResponse{
		APIVersion: APIVersion,
		AuthorityState: AuthorityState{
			ConfigAuthority:       "managed",
			ConfigAuthoritySource: "explicit",
			ConfigState:           "managed_clean",
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"config_authority", "config_authority_source", "config_state"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("%q is not a top-level key: %s", key, b)
		}
	}
	if _, nested := raw["AuthorityState"]; nested {
		t.Fatal("the embedded struct was published as a nested object")
	}
}

// TestApplySummaryAlwaysCarriesADegradedArray is §33.2's "present and empty"
// rule: a script must be able to test the array unconditionally.
func TestApplySummaryAlwaysCarriesADegradedArray(t *testing.T) {
	b, err := json.Marshal(ApplySummary{ApplyID: "rl_aaaaaaaaaaaa_1", Degraded: []Degradation{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"degraded":[]`) {
		t.Fatalf("degraded must serialize as an empty array, got %s", b)
	}
}
