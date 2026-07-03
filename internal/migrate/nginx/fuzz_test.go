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
