// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"bytes"
	"jul/internal/config"
	"testing"
)

func TestCompactCandidateRetainsEffectivePolicies(t *testing.T) {
	cfg, _ := translate(t, `http {
  gzip on;
  upstream backend {server 127.0.0.1:9000;}
  server {listen 8080;client_max_body_size 4m;
   location / {proxy_pass http://backend;expires 0;proxy_buffering off;types {};}
  }
 }`)
	full, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := MarshalCandidate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, err := config.Parse(full)
	if err != nil {
		t.Fatal(err)
	}
	b, err := config.Parse(compact)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, _ := config.Marshal(a)
	bBytes, _ := config.Marshal(b)
	if !bytes.Equal(aBytes, bBytes) {
		t.Fatal("behavior changed")
	}
	for _, want := range []string{"expires = '0s'", "proxy_buffering = false", "client_max_body_size = '4m'", "types"} {
		if !bytes.Contains(compact, []byte(want)) {
			t.Fatalf("missing %q: %s", want, compact)
		}
	}
	for _, unwanted := range []string{"worker_threads", "shutdown_timeout", "proxy_connect_timeout", "write_timeout", "[observability]"} {
		if bytes.Contains(compact, []byte(unwanted)) {
			t.Fatalf("default %q: %s", unwanted, compact)
		}
	}
	if len(compact) >= len(full) {
		t.Fatal("not compact")
	}
	if err = config.Validate(b); err != nil {
		t.Fatal(err)
	}
}

func TestPruneDefaultValuesPreservesOptionalPresence(t *testing.T) {
	values := map[string]any{"x": int64(0), "flag": false, "optional": map[string]any{}, "list": []any{}, "tables": []any{map[string]any{"x": int64(0), "kept": "v"}}}
	defaults := map[string]any{"x": int64(0), "flag": true, "list": []any{}, "tables": []any{map[string]any{"x": int64(0)}}}
	pruneCandidateDefaults(values, defaults)
	if _, ok := values["x"]; ok {
		t.Fatal("default retained")
	}
	if _, ok := values["list"]; ok {
		t.Fatal("empty default list retained")
	}
	if _, ok := values["optional"]; !ok {
		t.Fatal("optional presence lost")
	}
	if values["flag"] != false {
		t.Fatal("explicit disable lost")
	}
	if _, ok := values["tables"].([]any)[0].(map[string]any)["x"]; ok {
		t.Fatal("table default retained")
	}
}
