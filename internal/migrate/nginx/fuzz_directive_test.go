// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"testing"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
)

// FuzzParseDirective exercises the NGINX directive parser with arbitrary
// input. It focuses on directive nesting, modifier edge cases, and malformed
// blocks that might panic the third-party parser. The target verifies that
// parseString's recover() catches all parser panics.
func FuzzParseDirective(f *testing.F) {
	seeds := []string{
		`server { listen 80; }`,
		`location ~ \.php$ { fastcgi_pass 127.0.0.1:9000; }`,
		`upstream u { server 1:80 weight=5; server 2:80 backup; }`,
		`http { server { listen 80; location / { proxy_pass http://b; } } }`,
		`events { worker_connections 1024; } http { server { listen 80; } }`,
		`server { listen 80; location = /exact { return 200; } location ~* \.(gif|jpg|png)$ { root /var/www; } }`,
		`map $http_host $name { hostnames; .example.com 1; }`,
		`geo $country { default US; 127.0.0.1 UK; }`,
		`server { listen 80; # comment
location / { } }`,
		`server { listen 80; location / { proxy_pass http://backend; proxy_set_header Host $host; } }`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		cfg, err := parseString(raw)
		if err != nil {
			// Parse errors are expected for random input; skip them.
			return
		}
		if cfg == nil {
			t.Fatal("parseString returned nil config without error")
		}
		// Walk the parsed tree to ensure it is well-formed.
		walkDirectives(cfg.GetDirectives())
	})
}

func walkDirectives(dirs []ngx.IDirective) {
	for _, d := range dirs {
		_ = d.GetName()
		_ = d.GetLine()
		_ = d.GetParameters()
		if block, ok := d.(interface{ GetDirectives() []ngx.IDirective }); ok {
			walkDirectives(block.GetDirectives())
		}
	}
}
