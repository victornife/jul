// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"testing"

	"jul/internal/config"
)

// FuzzTranslate drives random (but structurally plausible) nginx configuration
// fragments through parse + translate. The fuzzer mutates the input string and
// any panic or translation-time crash is a failure.  Because parseString already
// recovers from parser panics, the target checks that we never leak a panic
// through Translate itself and that the resulting config can always marshal.
func FuzzTranslate(f *testing.F) {
	// Seed corpus — representative directive patterns.
	seeds := []string{
		`http { server { listen 80; location / { return 200; } } }`,
		`http { server { listen 443 ssl; ssl_certificate c; ssl_certificate_key k; location / { proxy_pass http://b; } } }`,
		`http { server { listen 80; root /var/www; index i.html; location / { try_files $uri $uri/ =404; } } }`,
		`http { upstream u { server 1:80; } server { listen 80; location / { proxy_pass http://u; } } }`,
		`http { server { listen 80; location / { rewrite ^/old/(.*)$ /new/$1 permanent; } } }`,
		`http { gzip on; server { listen 80; location / { return 204; } } }`,
		`events { worker_connections 1024; } http { server { listen 80; location / { return 200; } } }`,
		`http { server { listen 80; location = /exact { return 200; } location ~ \.php$ { return 200; } } }`,
		`stream { server { listen 5353; proxy_pass 1.2.3.4:80; proxy_timeout 30s; proxy_connect_timeout 5s; } }`,
		`stream { server { listen 5353 udp; proxy_pass 1.2.3.4:80; } }`,
		`stream { server { listen 5353 proxy_protocol; proxy_pass 1.2.3.4:80; proxy_protocol on; } }`,
		`stream { server { listen 5353; server_name a.example.com; ssl_preread on; proxy_pass 1.2.3.4:80; } server { listen 5353; ssl_preread on; proxy_pass 5.6.7.8:80; } }`,
		`stream { upstream u { server 1.2.3.4:80 weight=2; } server { listen 5353; proxy_pass u; } }`,
		`http { server { listen 80 proxy_protocol; set_real_ip_from 10.0.0.0/8; real_ip_header proxy_protocol; location / { return 200; } } }`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := parseString(raw)
		if err != nil {
			// Parse errors are expected for random input; skip them.
			return
		}
		cfg, rep := Translate(parsed, "fuzz.conf")
		if cfg == nil {
			t.Fatal("Translate returned nil config")
		}
		if rep == nil {
			t.Fatal("Translate returned nil report")
		}
		// The translated config must always marshal (representable).
		_, err = config.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
	})
}
