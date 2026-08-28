// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"testing"

	"jul/internal/config"
)

// ── add_header ────────────────────────────────────────────────────────────────

func TestTranslateAddHeaderAlwaysMapsToResponseHeaders(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      add_header X-Frame-Options DENY always;
      return 200;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if len(loc.ResponseHeaders) != 1 {
		t.Fatalf("response_headers = %+v, want 1 entry", loc.ResponseHeaders)
	}
	op := loc.ResponseHeaders[0]
	if op.Op != "add" || op.Name != "X-Frame-Options" || op.Value == nil || *op.Value != "DENY" {
		t.Errorf("unexpected op: %+v", op)
	}
	if hasSkip(rep, "X-Frame-Options") {
		t.Errorf("an always-flagged header should not be reported, got %+v", rep.Skipped)
	}
}

func TestTranslateAddHeaderWithoutAlwaysIsReported(t *testing.T) {
	_, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      add_header X-Frame-Options DENY;
      return 200;
    }
  }
}`)
	if !hasSkip(rep, "always") {
		t.Errorf(`expected a skip mentioning "always", got %+v`, rep.Skipped)
	}
}

func TestTranslateAddHeaderVariableValueIsReported(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      add_header X-Request-Id $request_id always;
      return 200;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if len(loc.ResponseHeaders) != 0 {
		t.Errorf("expected no response_headers for a variable value, got %+v", loc.ResponseHeaders)
	}
	if !hasSkip(rep, "variable") {
		t.Errorf("expected a skip mentioning a variable, got %+v", rep.Skipped)
	}
}

// ── CORS synthesized from Access-Control-* headers ─────────────────────────────

func TestTranslateCORSFromStaticAccessControlHeaders(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location /api/ {
      add_header Access-Control-Allow-Origin "https://app.example.test" always;
      add_header Access-Control-Allow-Methods "GET, POST, OPTIONS" always;
      add_header Access-Control-Allow-Headers "Content-Type, Authorization" always;
      add_header Access-Control-Expose-Headers "X-Request-Id" always;
      add_header Access-Control-Allow-Credentials "true" always;
      add_header Access-Control-Max-Age "600" always;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if loc.CORS == nil || !loc.CORS.Enabled {
		t.Fatalf("expected an enabled CORS policy, got %+v", loc.CORS)
	}
	if len(loc.CORS.AllowedOrigins) != 1 || loc.CORS.AllowedOrigins[0] != "https://app.example.test" {
		t.Errorf("allowed_origins = %v", loc.CORS.AllowedOrigins)
	}
	if len(loc.CORS.AllowedMethods) != 3 {
		t.Errorf("allowed_methods = %v", loc.CORS.AllowedMethods)
	}
	if len(loc.CORS.AllowedHeaders) != 2 {
		t.Errorf("allowed_headers = %v", loc.CORS.AllowedHeaders)
	}
	if len(loc.CORS.ExposedHeaders) != 1 || loc.CORS.ExposedHeaders[0] != "X-Request-Id" {
		t.Errorf("exposed_headers = %v", loc.CORS.ExposedHeaders)
	}
	if !loc.CORS.AllowCredentials {
		t.Error("expected allow_credentials = true")
	}
	if loc.CORS.MaxAge == nil || loc.CORS.MaxAge.Std().Seconds() != 600 {
		t.Errorf("max_age = %v, want 600s", loc.CORS.MaxAge)
	}
	// None of the six recognized headers should also appear as a plain
	// response_headers operation.
	if len(loc.ResponseHeaders) != 0 {
		t.Errorf("Access-Control-* headers leaked into response_headers: %+v", loc.ResponseHeaders)
	}
	assertValidNginxOutput(t, cfg, rep)
}

func TestTranslateCORSWildcardOriginAlone(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location /api/ {
      add_header Access-Control-Allow-Origin "*" always;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if loc.CORS == nil || len(loc.CORS.AllowedOrigins) != 1 || loc.CORS.AllowedOrigins[0] != "*" {
		t.Fatalf("expected a wildcard CORS policy, got %+v", loc.CORS)
	}
}

func TestTranslateCORSWildcardWithCredentialsIsNotEmitted(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location /api/ {
      add_header Access-Control-Allow-Origin "*" always;
      add_header Access-Control-Allow-Credentials "true" always;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if loc.CORS != nil {
		t.Errorf("expected the unsafe wildcard+credentials CORS policy to be dropped, got %+v", loc.CORS)
	}
	if !hasSkip(rep, "Credentials") {
		t.Errorf("expected a skip explaining the wildcard+credentials conflict, got %+v", rep.Skipped)
	}
}

func TestTranslateCORSOriginVariableIsNotTranslated(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location /api/ {
      add_header Access-Control-Allow-Origin $http_origin always;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if loc.CORS != nil {
		t.Errorf("a reflected $http_origin must never become a permissive CORS policy, got %+v", loc.CORS)
	}
	if !hasSkip(rep, "variable") {
		t.Errorf("expected a skip mentioning a variable, got %+v", rep.Skipped)
	}
}

// ── limit_except ─────────────────────────────────────────────────────────────

func TestTranslateLimitExceptDenyAllMapsToMethods(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location /api/ {
      limit_except GET HEAD {
        deny all;
      }
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if len(loc.Match.Methods) != 2 || loc.Match.Methods[0] != "GET" || loc.Match.Methods[1] != "HEAD" {
		t.Fatalf("match.methods = %v, want [GET HEAD]", loc.Match.Methods)
	}
	if !hasNote(rep, "limit_except") {
		t.Errorf("expected a note explaining the 403-vs-404 difference, got %v", rep.Notes)
	}
	assertValidNginxOutput(t, cfg, rep)
}

func TestTranslateLimitExceptReturn403MapsToMethods(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location / {
      limit_except POST {
        return 403;
      }
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if len(loc.Match.Methods) != 1 || loc.Match.Methods[0] != "POST" {
		t.Fatalf("match.methods = %v, want [POST]", loc.Match.Methods)
	}
}

func TestTranslateLimitExceptWithOtherDirectivesIsReported(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      limit_except GET {
        proxy_pass http://127.0.0.1:9001;
      }
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if loc.Match.Methods != nil {
		t.Errorf("expected no method predicate, got %v", loc.Match.Methods)
	}
	if !hasSkip(rep, "limit_except") {
		t.Errorf("expected a skip for the non-bare-denial body, got %+v", rep.Skipped)
	}
}

func TestTranslateLimitExceptNoMethodsIsReported(t *testing.T) {
	_, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      limit_except {
        deny all;
      }
      return 200;
    }
  }
}`)
	if !hasSkip(rep, "limit_except") {
		t.Errorf("expected a skip for limit_except with no methods, got %+v", rep.Skipped)
	}
}

func TestTranslateLimitExceptSecondOneIsReportedNotMerged(t *testing.T) {
	// Two limit_except directives on one location: the second finds
	// Match.Methods already set by the first, and is reported rather than
	// silently overwriting or merging it.
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      limit_except GET {
        deny all;
      }
      limit_except POST {
        deny all;
      }
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if len(loc.Match.Methods) != 1 || loc.Match.Methods[0] != "GET" {
		t.Fatalf("match.methods = %v, want the first limit_except's [GET] preserved", loc.Match.Methods)
	}
	if !hasSkip(rep, "already has a method constraint") {
		t.Errorf("expected a skip for the second limit_except, got %+v", rep.Skipped)
	}
}

// ── location-level directives not otherwise covered ────────────────────────────

func TestTranslateLocationLevelIfIsReported(t *testing.T) {
	_, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      if ($request_method = POST) {
        return 405;
      }
      return 200;
    }
  }
}`)
	if !hasSkip(rep, "location-level if is not translated") {
		t.Errorf("expected a skip for location-level if, got %+v", rep.Skipped)
	}
}

func TestTranslateUnsupportedLocationDirectiveIsReported(t *testing.T) {
	_, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      internal;
      return 200;
    }
  }
}`)
	if !hasSkip(rep, "unsupported location-level directive") {
		t.Errorf("expected a skip for an unsupported location-level directive, got %+v", rep.Skipped)
	}
}

// ── add_header edge cases ───────────────────────────────────────────────────────

func TestTranslateAddHeaderMalformedIsReported(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      add_header X-Only-Name;
      return 200;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if len(loc.ResponseHeaders) != 0 {
		t.Errorf("expected no response_headers from a malformed add_header, got %+v", loc.ResponseHeaders)
	}
	if !hasSkip(rep, "malformed add_header") {
		t.Errorf("expected a skip for malformed add_header, got %+v", rep.Skipped)
	}
}

func TestTranslateCORSMaxAgeInvalidValueIsReported(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location /api/ {
      add_header Access-Control-Allow-Origin "https://app.example.test" always;
      add_header Access-Control-Max-Age "not-a-number" always;
      proxy_pass http://127.0.0.1:9000;
    }
  }
}`)
	loc := onlyServer(t, cfg).Locations[0]
	if loc.CORS == nil || loc.CORS.MaxAge != nil {
		t.Fatalf("expected max_age left unset after an invalid value, got %+v", loc.CORS)
	}
	if !hasSkip(rep, "non-negative whole number of seconds") {
		t.Errorf("expected a skip explaining the invalid max_age, got %+v", rep.Skipped)
	}
}

// assertValidNginxOutput proves the translated config marshals, re-parses, and
// validates cleanly — the same gate cmd/jul's `import nginx` runs before ever
// writing output, and the #147 §7 requirement that generated config passes
// strict validation.
func assertValidNginxOutput(t *testing.T, cfg *config.Config, _ *Report) {
	t.Helper()
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, raw)
	}
	if err := config.Validate(parsed); err != nil {
		t.Fatalf("validate: %v\n%s", err, raw)
	}
}
