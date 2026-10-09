// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strings"
	"testing"
)

// The original audit probe now has only its deferred retry directive blocking.
// Existing send_timeout remains explicitly approximate, including on H2/H3.
func TestOriginalCommonIdiomProbe(t *testing.T) {
	a, rep := assessString(t, `http {
 gzip on;gzip_types text/plain application/json;
 upstream media {server 127.0.0.1:18096;}
 server {listen 18080;client_max_body_size 50m;
 location /socket {proxy_pass http://media;proxy_http_version 1.1;proxy_set_header Upgrade $http_upgrade;proxy_set_header Connection upgrade;}
 location /events {proxy_pass http://media;proxy_buffering off;proxy_read_timeout 1h;}
 location /videos/ {proxy_pass http://media;proxy_next_upstream error timeout http_503;send_timeout 30s;expires 1h;}
 }}`)
	for _, r := range a.Results {
		if r.Class == AssessmentBlocking && r.Directive != "proxy_next_upstream" {
			t.Fatalf("unexpected blocker: %+v", r)
		}
		if r.Directive == "send_timeout" && r.Class != AssessmentApproximated {
			t.Fatal("timeout approximation lost")
		}
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Name, "proxy_next_upstream") {
		t.Fatalf("skipped: %+v", rep.Skipped)
	}
}
