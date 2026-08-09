// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"jul/internal/lifecycle"
)

func TestGlobalRateLimitSetSparseAndExplicitValues(t *testing.T) {
	c := issue80BaseConfig()
	summary, err := applyPatch(c, patchRequest{
		Op: "rate_limit_global_set",
		RateLimit: &rateLimitPatch{
			Rate:     ptr(150),
			Burst:    ptr(0),
			MaxConns: ptr(0),
		},
	})
	if err != nil {
		t.Fatalf("apply sparse global rate limit: %v", err)
	}
	if summary != "rate limit fields changed: rate, burst, max_conns" {
		t.Fatalf("summary = %q", summary)
	}
	if !c.RateLimit.Enabled || c.RateLimit.Key != "ip" {
		t.Fatalf("omitted fields changed: %+v", c.RateLimit)
	}
	if c.RateLimit.Rate != 150 || c.RateLimit.Burst != 0 || c.RateLimit.MaxConns != 0 {
		t.Fatalf("explicit values not retained before canonical reparse: %+v", c.RateLimit)
	}

	result, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: issue80BaseConfig(),
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{{
		Op: "rate_limit_global_set",
		RateLimit: &rateLimitPatch{
			Rate:     ptr(150),
			Burst:    ptr(0),
			MaxConns: ptr(0),
		},
	}})
	if err != nil {
		t.Fatalf("execute sparse global rate limit: %v", err)
	}
	if !result.Valid {
		t.Fatalf("candidate invalid: %+v", result.ValidationErrors)
	}
	if result.CandidateConfig.RateLimit.Burst != 150 {
		t.Fatalf("burst = %d, want canonical reset to rate 150", result.CandidateConfig.RateLimit.Burst)
	}
	if result.CandidateConfig.RateLimit.MaxConns != 0 {
		t.Fatalf("max_conns = %d, want unlimited 0", result.CandidateConfig.RateLimit.MaxConns)
	}
}

func TestGlobalRateLimitSetDisableRetainsDormantSettings(t *testing.T) {
	c := issue80BaseConfig()
	before := c.RateLimit
	if _, err := applyPatch(c, patchRequest{
		Op:        "rate_limit_global_set",
		RateLimit: &rateLimitPatch{Enabled: ptr(false)},
	}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if c.RateLimit.Enabled {
		t.Fatal("rate limit remains enabled")
	}
	if c.RateLimit.Key != before.Key || c.RateLimit.Rate != before.Rate ||
		c.RateLimit.Burst != before.Burst || c.RateLimit.MaxConns != before.MaxConns {
		t.Fatalf("disable reset dormant settings: got %+v want %+v", c.RateLimit, before)
	}

	// A disabled policy may retain an explicit zero rate and later reject an
	// enable until a positive rate is supplied.
	if _, err := applyPatch(c, patchRequest{
		Op:        "rate_limit_global_set",
		RateLimit: &rateLimitPatch{Rate: ptr(0)},
	}); err != nil {
		t.Fatalf("set dormant zero rate: %v", err)
	}
	before = c.RateLimit
	if _, err := applyPatch(c, patchRequest{
		Op:        "rate_limit_global_set",
		RateLimit: &rateLimitPatch{Enabled: ptr(true)},
	}); err == nil {
		t.Fatal("expected enable with zero effective rate to fail")
	}
	if !reflect.DeepEqual(c.RateLimit, before) {
		t.Fatalf("failed enable mutated dormant policy: got %+v want %+v", c.RateLimit, before)
	}
}

func TestGlobalRateLimitSetRejectsInvalidFieldWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch rateLimitPatch
	}{
		{name: "empty", patch: rateLimitPatch{}},
		{name: "blank key", patch: rateLimitPatch{Key: ptr("")}},
		{name: "invalid key", patch: rateLimitPatch{Key: ptr("cookie:tenant")}},
		{name: "negative rate", patch: rateLimitPatch{Rate: ptr(-1)}},
		{name: "negative burst", patch: rateLimitPatch{Burst: ptr(-1)}},
		{name: "negative max", patch: rateLimitPatch{MaxConns: ptr(-1)}},
		{name: "rate exceeds retained burst", patch: rateLimitPatch{Rate: ptr(300)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := issue80BaseConfig()
			before := mustConfigBytes(t, c)
			_, err := applyPatch(c, patchRequest{Op: "rate_limit_global_set", RateLimit: &tc.patch})
			if err == nil {
				t.Fatal("expected rejection")
			}
			if after := mustConfigBytes(t, c); !bytes.Equal(after, before) {
				t.Fatalf("rejected patch mutated config\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
	if _, err := applyPatch(issue80BaseConfig(), patchRequest{Op: "rate_limit_global_set"}); err == nil {
		t.Fatal("expected nil payload rejection")
	}
}

func TestGlobalRateLimitSetValueFreeDeterministicSummary(t *testing.T) {
	c := issue80BaseConfig()
	summary, err := applyPatch(c, patchRequest{
		Op: "rate_limit_global_set",
		RateLimit: &rateLimitPatch{
			Enabled:  ptr(false),
			Key:      ptr("header:X-Tenant"),
			Rate:     ptr(50),
			Burst:    ptr(60),
			MaxConns: ptr(25),
		},
	})
	if err != nil {
		t.Fatalf("apply all fields: %v", err)
	}
	want := "rate limit fields changed: enabled, key, rate, burst, max_conns"
	if summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
	for _, forbidden := range []string{"false", "header:X-Tenant", "50", "60", "25"} {
		if strings.Contains(summary, forbidden) {
			t.Fatalf("summary leaked %q: %q", forbidden, summary)
		}
	}
}

func TestGlobalRateLimitLifecycleHotAndRetainedListenerStage(t *testing.T) {
	hot, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: issue80BaseConfig(),
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{{
		Op: "rate_limit_global_set",
		RateLimit: &rateLimitPatch{
			Key:   ptr("header:X-Tenant"),
			Rate:  ptr(120),
			Burst: ptr(240),
		},
	}})
	if err != nil {
		t.Fatalf("execute hot rate fields: %v", err)
	}
	if !hot.Valid || !hot.Lifecycle.CanApplyHot {
		t.Fatalf("hot lifecycle = %+v valid=%v errors=%+v", hot.Lifecycle, hot.Valid, hot.ValidationErrors)
	}
	for _, path := range []string{"rate_limit.key", "rate_limit.rate", "rate_limit.burst"} {
		if !hasLifecyclePath(hot.Lifecycle, path) {
			t.Errorf("missing hot lifecycle path %s: %+v", path, hot.Lifecycle.Changes)
		}
	}

	stage, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: issue80BaseConfig(),
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{{
		Op: "rate_limit_global_set",
		RateLimit: &rateLimitPatch{
			Rate:     ptr(120),
			Burst:    ptr(240),
			MaxConns: ptr(20),
		},
	}})
	if err != nil {
		t.Fatalf("execute retained-listener max_conns: %v", err)
	}
	if stage.Lifecycle.CanApplyHot || !stage.Lifecycle.CanStageRestart {
		t.Fatalf("retained-listener lifecycle = %+v, want staged complete candidate", stage.Lifecycle)
	}
	if !hasPath(stage.Lifecycle.RestartRequired, "rate_limit.max_conns") {
		t.Fatalf("restart paths = %v, want rate_limit.max_conns", stage.Lifecycle.RestartRequired)
	}
	if stage.CandidateConfig.RateLimit.Rate != 120 || stage.CandidateConfig.RateLimit.MaxConns != 20 {
		t.Fatalf("mixed candidate was partially built: %+v", stage.CandidateConfig.RateLimit)
	}
}

func TestGlobalRateLimitMaxConnsListenerAwareLifecycle(t *testing.T) {
	// Adding a new address while retaining an existing address still strands the
	// old listener with its previous cap.
	retained, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: issue80BaseConfig(),
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{
		{Op: "server_add", Listen: ":9090"},
		{Op: "rate_limit_global_set", RateLimit: &rateLimitPatch{MaxConns: ptr(20)}},
	})
	if err != nil {
		t.Fatalf("execute retained plus new listener: %v", err)
	}
	if retained.Lifecycle.CanApplyHot || !hasPath(retained.Lifecycle.RestartRequired, "rate_limit.max_conns") {
		t.Fatalf("retained plus new lifecycle = %+v, want restart", retained.Lifecycle)
	}

	// When every previously bound address is removed and every desired listener
	// is newly bound in the same candidate, the candidate cap is installed live.
	allNew, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: issue80BaseConfig(),
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{
		{Op: "server_add", Listen: ":9090"},
		{Op: "server_remove", Listen: ":8080"},
		{Op: "rate_limit_global_set", RateLimit: &rateLimitPatch{MaxConns: ptr(20)}},
	})
	if err != nil {
		t.Fatalf("execute all-new listeners: %v", err)
	}
	if !allNew.Valid || !allNew.Lifecycle.CanApplyHot {
		t.Fatalf("all-new lifecycle = %+v valid=%v errors=%+v", allNew.Lifecycle, allNew.Valid, allNew.ValidationErrors)
	}
	if !hasPath(allNew.Lifecycle.NewListenerOnly, "rate_limit.max_conns") {
		t.Fatalf("new-listener paths = %v, want rate_limit.max_conns", allNew.Lifecycle.NewListenerOnly)
	}

	unchanged, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: issue80BaseConfig(),
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{{
		Op:        "rate_limit_global_set",
		RateLimit: &rateLimitPatch{MaxConns: ptr(10)},
	}})
	if err != nil {
		t.Fatalf("execute unchanged cap: %v", err)
	}
	if hasLifecyclePath(unchanged.Lifecycle, "rate_limit.max_conns") {
		t.Fatalf("unchanged cap produced lifecycle change: %+v", unchanged.Lifecycle.Changes)
	}
}

func TestRouteRateLimitCompatibilityAndGlobalDiscrimination(t *testing.T) {
	c := patchTestConfig()
	loc := &c.Servers[0].Locations[0]

	// Existing clients may omit burst and key; route_set_rate_limit remains a
	// complete replacement and keeps its historical defaults.
	if _, err := applyPatch(c, patchRequest{
		Op: "route_set_rate_limit", Listen: ":8080", MatchType: "prefix", Path: "/api",
		RateLimit: &rateLimitPatch{Enabled: ptr(true), Rate: ptr(40)},
	}); err != nil {
		t.Fatalf("legacy route payload: %v", err)
	}
	if loc.RateLimit == nil || !loc.RateLimit.Enabled || loc.RateLimit.Rate != 40 ||
		loc.RateLimit.Burst != 40 || loc.RateLimit.Key != "ip" {
		t.Fatalf("legacy route defaults changed: %+v", loc.RateLimit)
	}

	// Missing enabled retains the old zero-value meaning: disable the existing
	// route policy rather than converting route_set_rate_limit into a sparse merge.
	if _, err := applyPatch(c, patchRequest{
		Op: "route_set_rate_limit", Listen: ":8080", MatchType: "prefix", Path: "/api",
		RateLimit: &rateLimitPatch{Rate: ptr(90)},
	}); err != nil {
		t.Fatalf("legacy omitted enabled payload: %v", err)
	}
	if loc.RateLimit == nil || loc.RateLimit.Enabled {
		t.Fatalf("omitted enabled no longer disables route policy: %+v", loc.RateLimit)
	}

	before := mustConfigBytes(t, c)
	if _, err := applyPatch(c, patchRequest{
		Op: "route_set_rate_limit", Listen: ":8080", MatchType: "prefix", Path: "/api",
		RateLimit: &rateLimitPatch{Enabled: ptr(true), Rate: ptr(10), MaxConns: ptr(5)},
	}); err == nil || !strings.Contains(err.Error(), "valid only for rate_limit_global_set") {
		t.Fatalf("route max_conns error = %v", err)
	}
	if after := mustConfigBytes(t, c); !bytes.Equal(after, before) {
		t.Fatal("route max_conns rejection mutated route config")
	}
}

func TestRateLimitWireDecodeIsStrictAndPresenceAware(t *testing.T) {
	body := `{"op":"rate_limit_global_set","rate_limit":{"enabled":false,"key":"ip","rate":0,"burst":0,"max_conns":0}}`
	var req patchRequest
	if err := decodePatchJSON(strings.NewReader(body), &req); err != nil {
		t.Fatalf("decode global rate limit: %v", err)
	}
	if req.RateLimit == nil || req.RateLimit.Enabled == nil || req.RateLimit.Rate == nil ||
		req.RateLimit.Burst == nil || req.RateLimit.MaxConns == nil {
		t.Fatalf("explicit false/zero presence lost: %+v", req.RateLimit)
	}
	if *req.RateLimit.Enabled || *req.RateLimit.Rate != 0 || *req.RateLimit.MaxConns != 0 {
		t.Fatalf("decoded values changed: %+v", req.RateLimit)
	}

	for _, invalid := range []string{
		`{"op":"rate_limit_global_set","rate_limit":{"unknown":1}}`,
		body + ` {}`,
	} {
		var out patchRequest
		if err := decodePatchJSON(strings.NewReader(invalid), &out); err == nil {
			t.Fatalf("strict decoder accepted %s", invalid)
		}
	}

	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if strings.Count(string(encoded), `"rate_limit"`) != 1 {
		t.Fatalf("encoded request has ambiguous rate_limit key: %s", encoded)
	}
}
