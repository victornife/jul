// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
)

func TestApplyPatchConsistentHashStrategy(t *testing.T) {
	c := patchTestConfig()
	summary, err := applyPatch(c, patchRequest{
		Op: "upstream_set_strategy", Upstream: "pool", Strategy: "consistent_hash",
		Hash: &upstreamHash{Key: "header", Name: " X-Tenant ", Fallback: "least_conn"},
	})
	if err != nil {
		t.Fatalf("set consistent_hash: %v", err)
	}
	up := c.Upstreams[0]
	if up.Strategy != "consistent_hash" || up.Hash == nil || up.Hash.Key != "header" || up.Hash.Name != "X-Tenant" || up.Hash.Fallback != "least_conn" {
		t.Fatalf("upstream = %+v hash %+v", up, up.Hash)
	}
	if !strings.Contains(summary, "consistent_hash (key=header:X-Tenant fallback=least_conn)") {
		t.Fatalf("summary = %q", summary)
	}
	if err := config.Validate(c); err != nil && strings.Contains(err.Error(), "hash") {
		t.Fatalf("patched hash block invalid: %v", err)
	}

	// Switching away clears the block, so no stale hash lingers.
	if _, err := applyPatch(c, patchRequest{Op: "upstream_set_strategy", Upstream: "pool", Strategy: "round_robin"}); err != nil {
		t.Fatal(err)
	}
	if c.Upstreams[0].Hash != nil {
		t.Fatal("hash block survived a switch to round_robin")
	}

	for _, bad := range []patchRequest{
		{Op: "upstream_set_strategy", Upstream: "pool", Strategy: "consistent_hash"},
		{Op: "upstream_set_strategy", Upstream: "pool", Strategy: "consistent_hash", Hash: &upstreamHash{}},
		{Op: "upstream_set_strategy", Upstream: "pool", Strategy: "least_conn", Hash: &upstreamHash{Key: "client_ip"}},
		{Op: "upstream_set_strategy", Upstream: "pool", Strategy: "ip_hash"},
	} {
		if _, err := applyPatch(c, bad); err == nil {
			t.Errorf("patch %+v accepted", bad)
		}
	}
}

func TestApplyPatchUpstreamAddConsistentHash(t *testing.T) {
	c := patchTestConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "upstream_add", Upstream: "sticky", Address: "10.0.0.9:80", Strategy: "consistent_hash",
		Hash: &upstreamHash{Key: "client_ip"},
	}); err != nil {
		t.Fatalf("upstream_add: %v", err)
	}
	added := c.Upstreams[len(c.Upstreams)-1]
	if added.Strategy != "consistent_hash" || added.Hash == nil || added.Hash.Key != "client_ip" {
		t.Fatalf("added = %+v", added)
	}
	if _, err := applyPatch(c, patchRequest{Op: "upstream_add", Upstream: "other", Address: "10.0.0.8:80", Strategy: "consistent_hash"}); err == nil {
		t.Fatal("upstream_add consistent_hash without a key was accepted")
	}
}

func TestUpstreamHashProjectionAndDiff(t *testing.T) {
	before := patchTestConfig()
	after := patchTestConfig()
	after.Upstreams[0].Strategy = "consistent_hash"
	after.Upstreams[0].Hash = &config.HashConfig{Key: "cookie", Name: "sid"}

	apps := projectApps(after, nil)
	h := apps[0].Hash
	if h == nil || h.Key != "cookie" || h.Name != "sid" || h.Fallback != "round_robin" || h.Algorithm != "rendezvous_v1" || h.AppliesTo != "http" {
		t.Fatalf("projected hash = %+v", h)
	}
	if projectApps(before, nil)[0].Hash != nil {
		t.Fatal("round_robin upstream projected a hash")
	}
	ip := &config.UpstreamConfig{Strategy: "consistent_hash", Hash: &config.HashConfig{Key: "client_ip", Fallback: "least_conn"}}
	if v := upstreamHashView(ip); v.AppliesTo != "http_and_stream" || v.Fallback != "least_conn" {
		t.Fatalf("client_ip view = %+v", v)
	}

	d := diffConfigs(before, after)
	var found bool
	for _, e := range d.Modifications {
		if strings.Contains(e.Detail, "Change affinity key of pool") {
			found = true
			if e.After != "key=cookie:sid fallback=round_robin" {
				t.Fatalf("diff entry after = %q", e.After)
			}
		}
	}
	if !found {
		t.Fatalf("diff has no affinity-key entry: %+v", d.Modifications)
	}
	if strategySummary("", nil) != "round_robin" || hashSummary(nil) != "" {
		t.Fatal("summaries of non-hash strategies changed")
	}
}

func TestV1UpstreamsCarryHashPolicy(t *testing.T) {
	cfg, err := config.Parse([]byte(`
[[upstreams]]
name = "sticky"
strategy = "consistent_hash"
servers = ["127.0.0.1:9001", "127.0.0.1:9002"]
[upstreams.hash]
key = "header"
name = "X-Tenant"

[[upstreams]]
name = "plain"
servers = ["127.0.0.1:9003"]

[[servers]]
listen = "127.0.0.1:8080"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://sticky"
`))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{LoadConfig: func() (*config.Config, error) { return cfg, nil }})
	got := decodeInto[adminapi.UpstreamsResponse](t, getV1(t, s, "/api/v1/upstreams", ""))
	h := got.Upstreams[0].Hash
	if h == nil || h.Key != "header" || h.Name != "X-Tenant" || h.Fallback != "round_robin" || h.Algorithm != "rendezvous_v1" || h.AppliesTo != "http" {
		t.Fatalf("hash = %+v", h)
	}
	if got.Upstreams[1].Hash != nil {
		t.Fatal("a non-hashing upstream carries a hash block")
	}
}
