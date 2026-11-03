// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package waf

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"jul/internal/redact"
)

func TestSanitizeMatchedTarget(t *testing.T) {
	prev := redact.Global()
	redact.Install(redact.NewState([]string{"supersecretfield"}, 4))
	t.Cleanup(func() { redact.Install(prev) })

	cases := []struct {
		collection string
		key        string
		want       string
	}{
		{"", "comment", ""},
		{"ARGS", "comment", "ARGS:comment"},
		{"args_post", "Rich_Text-Body", "ARGS_POST:rich_text-body"},
		{"REQUEST_HEADERS", "User-Agent", "REQUEST_HEADERS:user-agent"},
		{"REQUEST_COOKIES", "theme", "REQUEST_COOKIES:theme"},
		{"ARGS", "", "ARGS"},
		{"XML", "/root/node", "XML:/*"},
		// Whole-URI/body and internal collections never retain keys.
		{"TX", "942100-secret-key", "TX"},
		{"REQUEST_URI", "/login?token=s3cr3t", "REQUEST_URI"},
		{"REQUEST_BODY", "raw-body", "REQUEST_BODY"},
		{"MATCHED_VAR", "secret", "MATCHED_VAR"},
		{"CUSTOM_UNLISTED", "anything", "CUSTOM_UNLISTED"},
		// Sensitive or token-like keys are redacted.
		{"ARGS", "token", "ARGS:[redacted]"},
		{"ARGS", "api_key", "ARGS:[redacted]"},
		{"ARGS", "user_password", "ARGS:[redacted]"},
		{"ARGS", "canary_param", "ARGS:[redacted]"},
		{"ARGS", "oauth_code", "ARGS:[redacted]"},
		{"ARGS", "s3cr3t", "ARGS:[redacted]"},
		{"ARGS", "field123", "ARGS:[redacted]"},
		{"ARGS", "1startsWithDigit", "ARGS:[redacted]"},
		{"ARGS", "bad.dot", "ARGS:[redacted]"},
		{"ARGS", "supersecretfield", "ARGS:[redacted]"},
		{"ARGS", strings.Repeat("a", 33), "ARGS:[redacted]"},
		{"REQUEST_HEADERS", "Authorization", "REQUEST_HEADERS:[redacted]"},
		{"REQUEST_COOKIES", "session_id", "REQUEST_COOKIES:[redacted]"},
	}

	for _, tc := range cases {
		if got := SanitizeMatchedTarget(tc.collection, tc.key); got != tc.want {
			t.Errorf("SanitizeMatchedTarget(%q, %q) = %q, want %q", tc.collection, tc.key, got, tc.want)
		}
	}
}

func TestInvestigationStoreBoundsFiltersAndTTL(t *testing.T) {
	// Nil store safety.
	var nilStore *InvestigationStore
	nilStore.SetEnabled(false)
	nilStore.Clear()
	if nilStore.Enabled() {
		t.Fatal("nil store must report Enabled() == false")
	}
	if res, ok := nilStore.Record(TransactionEvent{}); ok || res != "" {
		t.Fatalf("nil store Record = (%q, %v), want empty/false", res, ok)
	}
	if id := nilStore.NewEventID(); !strings.HasPrefix(id, "wev_") || len(id) != 36 {
		t.Fatalf("nil store NewEventID = %q", id)
	}
	nilSnap := nilStore.Snapshot(time.Time{}, InvestigationFilter{})
	if nilSnap.Enabled || len(nilSnap.Events) != 0 {
		t.Fatalf("nil store Snapshot = %+v", nilSnap)
	}

	defaultStore := NewInvestigationStore(0, 0)
	if !defaultStore.Enabled() || defaultStore.Snapshot(time.Time{}, InvestigationFilter{}).Capacity != MaxInvestigationEvents {
		t.Fatal("expected default store capacity and enabled state")
	}

	store := NewInvestigationStore(4, 10*time.Minute)
	if !store.Enabled() {
		t.Fatal("expected store enabled by default")
	}

	id1 := store.NewEventID()
	id2 := store.NewEventID()
	if id1 == id2 || !strings.HasPrefix(id1, "wev_") {
		t.Fatalf("expected unique wev_ IDs, got %q and %q", id1, id2)
	}

	baseTime := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	// 1. Record an event with > MaxMatchedRulesPerEvent (16) rules to trigger truncation.
	manyRules := make([]MatchedRuleSummary, 20)
	for i := range manyRules {
		manyRules[i] = MatchedRuleSummary{
			RuleID:         942100 + i,
			Phase:          2,
			Severity:       "CRITICAL",
			SourceClass:    "embedded",
			Disruptive:     i == 19,
			Logged:         true,
			Tags:           []string{"attack-sqli", "OWASP_CRS", "bad\"quote", strings.Repeat("x", 80)},
			MatchedTargets: []string{"ARGS:comment", "ARGS:token", "TX:internal"},
		}
	}
	res1, ok := store.Record(TransactionEvent{
		EventID:           id1,
		Time:              baseTime,
		Outcome:           OutcomeInterruptedRequest,
		Mode:              "block",
		WouldInterrupt:    true,
		InterruptedRuleID: 942119,
		ClientStatus:      403,
		Method:            "post",
		Host:              "APP.EXAMPLE.TEST",
		Path:              "/api/upload?secret=canary123",
		QueryOmitted:      true,
		Route:             RouteAttribution{RouteID: "upload-route", Scope: "override"},
		Generation:        3,
		MatchedRules:      manyRules,
		RequestIDHint:     "req-1",
		TraceIDHint:       "trace-1",
	})
	if !ok || res1 != InvestigationTruncated {
		t.Fatalf("Record manyRules = (%q, %v), want (%q, true)", res1, ok, InvestigationTruncated)
	}

	// 2. Fill remaining 3 slots and overflow the 4-entry ring while entries are still live.
	for i := 0; i < 3; i++ {
		res, ok := store.Record(TransactionEvent{
			Time:              baseTime.Add(time.Duration(i+1) * time.Minute),
			Outcome:           OutcomeMatchedObserved,
			Mode:              "detect",
			ClientStatus:      200,
			OriginReached:     true,
			OriginStatus:      200,
			Method:            "GET",
			Host:              "portal.example.test",
			Path:              fmt.Sprintf("/item/%d", i),
			Route:             RouteAttribution{RouteID: "portal-route", Scope: "global"},
			MatchedRules:      []MatchedRuleSummary{{RuleID: 920350, Phase: 1, SourceClass: "embedded", Logged: true}},
			RequestIDHint:     "bad\"hint",
			TraceIDHint:       strings.Repeat("t", 200),
			TotalMatchedRules: 1,
		})
		if !ok || res != InvestigationRecorded {
			t.Fatalf("slot %d Record = (%q, %v), want recorded", i, res, ok)
		}
	}

	// 5th live entry overwrites the 1st live entry -> InvestigationDropped.
	resOverflow, ok := store.Record(TransactionEvent{
		EventID:           id2,
		Time:              baseTime.Add(4 * time.Minute),
		Outcome:           OutcomeInterruptedResponse,
		Mode:              "block",
		WouldInterrupt:    true,
		InterruptedRuleID: 959100,
		ClientStatus:      403,
		OriginReached:     true,
		OriginStatus:      200,
		Method:            "123INVALID",
		Host:              "",
		Path:              "/leak",
		Route:             RouteAttribution{RouteID: "leak-route"},
	})
	if !ok || resOverflow != InvestigationDropped {
		t.Fatalf("overflow Record = (%q, %v), want (%q, true)", resOverflow, ok, InvestigationDropped)
	}

	snap := store.Snapshot(baseTime.Add(5*time.Minute), InvestigationFilter{})
	if snap.Capacity != 4 || snap.Retained != 4 || snap.RecordedTotal != 5 || snap.TruncatedTotal != 1 || snap.DroppedTotal != 1 {
		t.Fatalf("unexpected snapshot counts: %+v", snap)
	}
	if snap.Events[0].EventID != id2 || snap.Events[0].Method != "UNKNOWN" || snap.Events[0].Host != "_unmatched" {
		t.Fatalf("newest event = %+v", snap.Events[0])
	}

	// Filter checks.
	if got := store.Snapshot(baseTime.Add(5*time.Minute), InvestigationFilter{EventID: id2}); len(got.Events) != 1 {
		t.Fatalf("filter EventID = %d, want 1", len(got.Events))
	}
	if got := store.Snapshot(baseTime.Add(5*time.Minute), InvestigationFilter{Outcome: OutcomeMatchedObserved}); len(got.Events) != 3 {
		t.Fatalf("filter Outcome = %d, want 3", len(got.Events))
	}
	if got := store.Snapshot(baseTime.Add(5*time.Minute), InvestigationFilter{Mode: "detect", Limit: 2}); len(got.Events) != 2 {
		t.Fatalf("filter Mode+Limit = %d, want 2", len(got.Events))
	}
	if got := store.Snapshot(baseTime.Add(5*time.Minute), InvestigationFilter{RuleID: 920350}); len(got.Events) != 3 {
		t.Fatalf("filter RuleID = %d, want 3", len(got.Events))
	}
	if got := store.Snapshot(baseTime.Add(5*time.Minute), InvestigationFilter{RuleID: 959100}); len(got.Events) != 1 {
		t.Fatalf("filter InterruptedRuleID = %d, want 1", len(got.Events))
	}
	if got := store.Snapshot(baseTime.Add(5*time.Minute), InvestigationFilter{RouteID: "portal-route"}); len(got.Events) != 3 {
		t.Fatalf("filter RouteID = %d, want 3", len(got.Events))
	}
	if got := store.Snapshot(baseTime.Add(5*time.Minute), InvestigationFilter{PathPrefix: "/leak"}); len(got.Events) != 1 {
		t.Fatalf("filter PathPrefix = %d, want 1", len(got.Events))
	}
	if got := store.Snapshot(baseTime.Add(5*time.Minute), InvestigationFilter{Since: baseTime.Add(3 * time.Minute)}); len(got.Events) != 2 {
		t.Fatalf("filter Since = %d, want 2", len(got.Events))
	}

	// TTL expiry check: after 20 minutes, all 4 entries are expired.
	expiredSnap := store.Snapshot(baseTime.Add(20*time.Minute), InvestigationFilter{})
	if expiredSnap.Retained != 0 || len(expiredSnap.Events) != 0 {
		t.Fatalf("expired snapshot = %+v, want 0 retained", expiredSnap)
	}

	// Enforce MaxEventBytes (4096) when an event has huge metadata (both Step 1 tag drop and Step 2 rule trimming).
	hugeServerNames := make([]string, 12)
	for i := range hugeServerNames {
		hugeServerNames[i] = strings.Repeat(fmt.Sprintf("sub%d.", i), 30) + "example.test"
	}
	hugeRules := make([]MatchedRuleSummary, 16)
	for i := range hugeRules {
		hugeRules[i] = MatchedRuleSummary{
			RuleID:         900001 + i,
			Phase:          2,
			Severity:       "CRITICAL",
			SourceClass:    "invalid_class",
			Tags:           []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9"},
			MatchedTargets: []string{"ARGS", "ARGS:[redacted]", "XML:/*", "ARGS:alphabetagamma", "ARGS:deltaepsilonzeta", "ARGS:etathotaiota", "ARGS:kappalambdamu", "ARGS:nunxomicron", "ARGS:pirhosigma"},
		}
	}
	_, _ = store.Record(TransactionEvent{
		Outcome:       OutcomeMatchedObserved,
		Mode:          "detect",
		Host:          strings.Repeat("h", 200),
		Path:          "/" + strings.Repeat("segment/", 40),
		Route:         RouteAttribution{RouteID: strings.Repeat("r", 200), Listen: strings.Repeat("l", 100), PathPattern: strings.Repeat("/p", 150), ServerNames: hugeServerNames},
		MatchedRules:  hugeRules,
		RequestIDHint: "supersecretfield",
	})
	afterHuge := store.Snapshot(time.Time{}, InvestigationFilter{Limit: 1})
	if len(afterHuge.Events) != 1 {
		t.Fatalf("expected 1 event after huge record, got %d", len(afterHuge.Events))
	}
	encoded, err := json.Marshal(afterHuge.Events[0])
	if err != nil || len(encoded) > MaxEventBytes || !afterHuge.Events[0].EventTruncated {
		t.Fatalf("encoded event size = %d (err=%v, truncated=%v), want <= %d", len(encoded), err, afterHuge.Events[0].EventTruncated, MaxEventBytes)
	}

	// Disabling clears retained events and rejects new records.
	store.SetEnabled(false)
	if store.Enabled() {
		t.Fatal("expected store disabled")
	}
	if _, ok := store.Record(TransactionEvent{Outcome: OutcomeInterruptedRequest}); ok {
		t.Fatal("disabled store must not record events")
	}
	disabledSnap := store.Snapshot(baseTime.Add(25*time.Minute), InvestigationFilter{})
	if disabledSnap.Enabled || disabledSnap.Retained != 0 {
		t.Fatalf("disabled snapshot = %+v", disabledSnap)
	}
}
