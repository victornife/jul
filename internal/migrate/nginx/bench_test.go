// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"testing"

	ngxparser "github.com/tufanbarisyildirim/gonginx/parser"
)

// Example nginx configuration used by benchmarks. It covers the most common
// directives translated today: two servers (static + TLS proxy), an upstream
// pool with weighted backends, location modifiers, return/rewrite, gzip, and
// a stream block that is skipped.
const benchSrc = `
worker_processes auto;
events { worker_connections 1024; }

http {
  include /etc/nginx/mime.types;
  gzip on;

  upstream app {
    least_conn;
    server 10.0.0.1:8080 weight=3;
    server 10.0.0.2:8080;
    server 10.0.0.3:8080 down;
  }

  server {
    listen 80;
    server_name www.example.com example.com;
    return 301 https://example.com$request_uri;
  }

  server {
    listen 443 ssl;
    server_name example.com;
    ssl_certificate /etc/ssl/example.crt;
    ssl_certificate_key /etc/ssl/example.key;
    ssl_protocols TLSv1.2 TLSv1.3;

    root /var/www/html;
    index index.html;

    location / {
      try_files $uri $uri/ =404;
    }

    location /api {
      proxy_pass http://app;
    }

    location = /health {
      return 200 "healthy";
    }

    location ~* \.(jpg|png|css|js)$ {
      root /var/www/assets;
    }
  }
}

stream {
  server { listen 5353; }
}
`

func BenchmarkTranslate(b *testing.B) {
	parsed, err := parseString(benchSrc)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Translate(parsed, "bench.conf")
	}
}

func BenchmarkParse(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ngxparser.NewStringParser(benchSrc, ngxparser.WithSkipValidDirectivesErr()).Parse()
		if err != nil {
			b.Fatal(err)
		}
	}
}
