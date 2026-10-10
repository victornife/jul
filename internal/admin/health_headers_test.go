// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl
package admin

import (
	"encoding/json"
	"jul/internal/config"
	"strings"
	"testing"
)

func TestHealthHeadersWriteOnlyPreserveReplaceClear(t *testing.T) {
	c := &config.Config{Upstreams: []config.UpstreamConfig{{Name: "api", HealthCheck: &config.HealthCheckConfig{Enabled: true, Type: "http", Path: "/", Host: "health.internal", Headers: map[string]string{"Authorization": "secret-token"}}}}}
	projections := projectApps(c, nil)
	raw, _ := json.Marshal(projections)
	if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "Authorization") {
		t.Fatal("header secret leaked in projection")
	}
	if projections[0].HealthCheckHeaderCount != 1 || projections[0].HealthCheckHost != "health.internal" {
		t.Fatal("missing safe metadata")
	}
	patch := patchRequest{Op: "upstream_set_health_check", Upstream: "api", HealthCheck: &upstreamHealthCheck{Enabled: true, Type: "http", Path: "/ready", Host: "other.internal"}}
	if _, err := applyPatch(c, patch); err != nil {
		t.Fatal(err)
	}
	if c.Upstreams[0].HealthCheck.Headers["Authorization"] != "secret-token" {
		t.Fatal("omitted headers clobbered credential")
	}
	replacement := map[string]string{"X-Token": "${env:HEALTH_TOKEN}"}
	patch.HealthCheck.Headers = &replacement
	if _, err := applyPatch(c, patch); err != nil {
		t.Fatal(err)
	}
	replacement["X-Token"] = "mutated"
	if c.Upstreams[0].HealthCheck.Headers["X-Token"] != "${env:HEALTH_TOKEN}" {
		t.Fatal("replacement map aliases wire")
	}
	empty := map[string]string{}
	patch.HealthCheck.Headers = &empty
	if _, err := applyPatch(c, patch); err != nil {
		t.Fatal(err)
	}
	if len(c.Upstreams[0].HealthCheck.Headers) != 0 {
		t.Fatal("explicit empty did not clear")
	}
	patch.HealthCheck.Headers = nil
	patch.HealthCheck.Type = "grpc"
	patch.HealthCheck.Path = ""
	if _, err := applyPatch(c, patch); err != nil {
		t.Fatal(err)
	}
	if len(c.Upstreams[0].HealthCheck.Headers) != 0 {
		t.Fatal("protocol switch preserved ignored fields")
	}
}
func TestHealthHeaderDiffNeverCarriesValues(t *testing.T) {
	before := &config.Config{Upstreams: []config.UpstreamConfig{{Name: "api", HealthCheck: &config.HealthCheckConfig{Enabled: true, Type: "http", Host: "one", Headers: map[string]string{"Authorization": "before-secret"}}}}}
	after, _ := before.Clone()
	after.Upstreams[0].HealthCheck.Host = "two"
	after.Upstreams[0].HealthCheck.Headers["Authorization"] = "after-secret"
	d := diffConfigs(before, after)
	if !diffHas(d, "health-check Host/authority") || !diffHas(d, "health-check headers") {
		t.Fatalf("missing review diff: %+v", d)
	}
	raw, _ := json.Marshal(d)
	for _, secret := range []string{"before-secret", "after-secret", "Authorization"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("diff leaked credential")
		}
	}
	if d := diffConfigs(after, after); diffHas(d, "health-check headers") {
		t.Fatal("no-op header diff")
	}
}

func TestStatusAPIHealthRequestPolicies(t *testing.T) {
	row := statusRows(t, &config.Config{})["Health probe headers / Host"]
	if row.Active || row.Detail != "" {
		t.Fatal("inactive policy marked active")
	}
	c := &config.Config{Upstreams: []config.UpstreamConfig{{Name: "api", HealthCheck: &config.HealthCheckConfig{Enabled: true, Type: "http", Host: "hidden.internal", Headers: map[string]string{"Authorization": "secret-token"}}}}}
	row = statusRows(t, c)["Health probe headers / Host"]
	if !row.Active || row.Detail != "1 pool" {
		t.Fatal(row)
	}
	c.Upstreams[0].HealthCheck.Enabled = false
	if statusRows(t, c)["Health probe headers / Host"].Active {
		t.Fatal("disabled health counted")
	}
	c.Upstreams[0].HealthCheck.Enabled = true
	c.Upstreams[0].HealthCheck.Type = "grpc"
	if !statusRows(t, c)["Health probe headers / Host"].Active {
		t.Fatal("grpc authority missing")
	}
	c.Upstreams[0].HealthCheck.Type = "tcp"
	if statusRows(t, c)["Health probe headers / Host"].Active {
		t.Fatal("ignored TCP fields counted")
	}
}
