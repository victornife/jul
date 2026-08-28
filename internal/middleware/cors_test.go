// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func corsStrPtr(s string) *string { return &s }

func compileCORS(t *testing.T, c *config.CORSConfig) *CORSPolicy {
	t.Helper()
	p := CompileCORS(c)
	if p == nil {
		t.Fatal("expected a compiled policy")
	}
	return p
}

func TestCompileCORSNilWhenAbsentOrDisabled(t *testing.T) {
	if CompileCORS(nil) != nil {
		t.Error("nil config should compile to nil")
	}
	if CompileCORS(&config.CORSConfig{Enabled: false, AllowedOrigins: []string{"https://a.example.test"}}) != nil {
		t.Error("disabled config should compile to nil, regardless of content")
	}
}

func TestCompileCORSDefaultsAllowedMethods(t *testing.T) {
	p := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	for _, m := range []string{"GET", "HEAD", "POST"} {
		if _, ok := p.allowedMethodSet[m]; !ok {
			t.Errorf("default allowed_methods should include %s", m)
		}
	}
}

func preflightRequest(origin, method string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodOptions, "/", nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("Access-Control-Request-Method", method)
	for _, h := range headers {
		r.Header.Add("Access-Control-Request-Headers", h)
	}
	return r
}

func TestIsPreflight(t *testing.T) {
	t.Run("well-formed preflight", func(t *testing.T) {
		if !IsPreflight(preflightRequest("https://a.example.test", "POST")) {
			t.Error("expected a preflight")
		}
	})
	t.Run("not OPTIONS", func(t *testing.T) {
		r := preflightRequest("https://a.example.test", "POST")
		r.Method = http.MethodGet
		if IsPreflight(r) {
			t.Error("a GET is never a preflight")
		}
	})
	t.Run("plain OPTIONS with no Origin", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodOptions, "/", nil)
		if IsPreflight(r) {
			t.Error("OPTIONS without Origin is not a preflight")
		}
	})
	t.Run("two Origin field lines", func(t *testing.T) {
		r := preflightRequest("https://a.example.test", "POST")
		r.Header.Add("Origin", "https://b.example.test")
		if IsPreflight(r) {
			t.Error("two Origin field lines is not a preflight")
		}
	})
	t.Run("repeated Access-Control-Request-Method field lines", func(t *testing.T) {
		r := preflightRequest("https://a.example.test", "POST")
		r.Header.Add("Access-Control-Request-Method", "GET")
		if IsPreflight(r) {
			t.Error("repeated ACRM field lines is not well-formed")
		}
	})
	t.Run("comma-separated Access-Control-Request-Method is not a single token", func(t *testing.T) {
		r := preflightRequest("https://a.example.test", "POST, GET")
		if IsPreflight(r) {
			t.Error("a CSV ACRM is not well-formed")
		}
	})
}

func TestEvaluatePreflightOriginAndMethod(t *testing.T) {
	p := compileCORS(t, &config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"https://a.example.test"},
		AllowedMethods: []string{"GET", "POST"},
	})

	if _, ok := p.EvaluatePreflight(preflightRequest("https://a.example.test", "POST")); !ok {
		t.Error("allowed origin+method should approve")
	}
	if _, ok := p.EvaluatePreflight(preflightRequest("https://evil.example.test", "POST")); ok {
		t.Error("disallowed origin should not approve")
	}
	if _, ok := p.EvaluatePreflight(preflightRequest("https://a.example.test", "DELETE")); ok {
		t.Error("disallowed method should not approve")
	}
	// Case-insensitive method comparison.
	if _, ok := p.EvaluatePreflight(preflightRequest("https://a.example.test", "post")); !ok {
		t.Error("CORS method comparison is case-insensitive, unlike match.methods")
	}
}

func TestEvaluatePreflightHeaders(t *testing.T) {
	p := compileCORS(t, &config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"https://a.example.test"},
		AllowedHeaders: []string{"Content-Type", "Authorization"},
	})

	if _, ok := p.EvaluatePreflight(preflightRequest("https://a.example.test", "POST", "Content-Type")); !ok {
		t.Error("an allowed header should approve")
	}
	if _, ok := p.EvaluatePreflight(preflightRequest("https://a.example.test", "POST", "X-Not-Allowed")); ok {
		t.Error("a header not in allowed_headers should not approve")
	}
	if _, ok := p.EvaluatePreflight(preflightRequest("https://a.example.test", "POST", "content-type")); !ok {
		t.Error("header comparison should be case-insensitive")
	}
}

func TestEvaluatePreflightHeaderCountBound(t *testing.T) {
	p := compileCORS(t, &config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"X-A"},
	})
	r := preflightRequest("https://a.example.test", "GET")
	tokens := ""
	for i := 0; i < maxPreflightRequestHeaders+1; i++ {
		if i > 0 {
			tokens += ","
		}
		tokens += "X-A"
	}
	r.Header.Set("Access-Control-Request-Headers", tokens)
	if _, ok := p.EvaluatePreflight(r); ok {
		t.Error("over the 64-token bound should not approve")
	}
}

func TestEvaluatePreflightWildcard(t *testing.T) {
	p := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"*"}})
	grant, ok := p.EvaluatePreflight(preflightRequest("https://anything.example.test", "GET"))
	if !ok || grant != "*" {
		t.Errorf("wildcard should approve every origin and grant \"*\", got %q, %v", grant, ok)
	}
}

func TestWritePreflightResponse(t *testing.T) {
	t.Run("non-wildcard includes Origin in Vary", func(t *testing.T) {
		p := compileCORS(t, &config.CORSConfig{
			Enabled:          true,
			AllowedOrigins:   []string{"https://a.example.test"},
			AllowedMethods:   []string{"GET", "POST"},
			AllowedHeaders:   []string{"Content-Type"},
			AllowCredentials: true,
			MaxAge:           corsMaxAge(600),
		})
		rec := httptest.NewRecorder()
		p.WritePreflightResponse(rec, "https://a.example.test")

		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204", rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://a.example.test" {
			t.Errorf("Allow-Origin = %q", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST" {
			t.Errorf("Allow-Methods = %q", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
			t.Errorf("Allow-Headers = %q", got)
		}
		if got := rec.Header().Get("Access-Control-Max-Age"); got != "600" {
			t.Errorf("Max-Age = %q", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("Allow-Credentials = %q", got)
		}
		if got := rec.Header().Get("Vary"); got != "Origin, Access-Control-Request-Method, Access-Control-Request-Headers" {
			t.Errorf("Vary = %q", got)
		}
	})

	t.Run("wildcard omits Origin from Vary", func(t *testing.T) {
		p := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"*"}})
		rec := httptest.NewRecorder()
		p.WritePreflightResponse(rec, "*")
		if got := rec.Header().Get("Vary"); got != "Access-Control-Request-Method, Access-Control-Request-Headers" {
			t.Errorf("Vary = %q, want Origin omitted", got)
		}
	})

	t.Run("allowed_headers empty omits Allow-Headers", func(t *testing.T) {
		p := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
		rec := httptest.NewRecorder()
		p.WritePreflightResponse(rec, "https://a.example.test")
		if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "" {
			t.Errorf("Allow-Headers = %q, want omitted", got)
		}
	})
}

func TestApplyToResponseWildcardIsUnconditional(t *testing.T) {
	p := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"*"}, ExposedHeaders: []string{"X-Id"}})

	for _, tc := range []struct {
		name   string
		origin string
	}{
		{"no origin", ""},
		{"origin null", "null"},
		{"ordinary origin", "https://anyone.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			p.ApplyToResponse(h, r)
			if got := h.Get("Access-Control-Allow-Origin"); got != "*" {
				t.Errorf("Allow-Origin = %q, want *", got)
			}
			if got := h.Get("Vary"); got != "" {
				t.Errorf("Vary = %q, want omitted under the unconditional wildcard", got)
			}
			if got := h.Get("Access-Control-Expose-Headers"); got != "X-Id" {
				t.Errorf("Expose-Headers = %q", got)
			}
		})
	}
}

func TestApplyToResponseNonWildcardAlwaysVaries(t *testing.T) {
	p := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})

	t.Run("allowed origin grants and varies", func(t *testing.T) {
		h := http.Header{}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Origin", "https://a.example.test")
		p.ApplyToResponse(h, r)
		if h.Get("Access-Control-Allow-Origin") != "https://a.example.test" {
			t.Error("expected a grant")
		}
		if h.Get("Vary") != "Origin" {
			t.Errorf("Vary = %q", h.Get("Vary"))
		}
	})

	t.Run("disallowed origin varies but does not grant", func(t *testing.T) {
		h := http.Header{}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Origin", "https://evil.example.test")
		p.ApplyToResponse(h, r)
		if h.Get("Access-Control-Allow-Origin") != "" {
			t.Error("a disallowed origin must not get a permissive grant")
		}
		if h.Get("Vary") != "Origin" {
			t.Errorf("Vary = %q, want Origin appended even when disallowed", h.Get("Vary"))
		}
	})

	t.Run("no origin at all still varies but does not grant", func(t *testing.T) {
		h := http.Header{}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		p.ApplyToResponse(h, r)
		if h.Get("Access-Control-Allow-Origin") != "" {
			t.Error("no origin must not get a grant")
		}
		if h.Get("Vary") != "Origin" {
			t.Errorf("Vary = %q, want Origin appended even with no Origin header", h.Get("Vary"))
		}
	})
}

func TestApplyToResponseStripsUpstreamAccessControlHeaders(t *testing.T) {
	p := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	h := http.Header{}
	h.Set("Access-Control-Allow-Origin", "https://impersonator.example.test")
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Set("Vary", "Accept-Encoding")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://a.example.test")

	p.ApplyToResponse(h, r)

	if got := h.Get("Access-Control-Allow-Origin"); got != "https://a.example.test" {
		t.Errorf("Allow-Origin = %q, want Jul's own grant, not the upstream's", got)
	}
	if got := h.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Allow-Credentials = %q, want stripped (not configured)", got)
	}
	if got := h.Values("Vary"); len(got) != 2 || got[0] != "Accept-Encoding" || got[1] != "Origin" {
		t.Errorf("Vary = %v, want the upstream's Accept-Encoding preserved and Origin appended", got)
	}
}

func TestApplyToResponsePreservesUpstreamVaryOrigin(t *testing.T) {
	// An upstream Vary: Origin is a fact about the stored representation, not
	// an optimization opportunity to strip (ADR 0018 §8b, §11).
	p := compileCORS(t, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example.test"}})
	h := http.Header{}
	h.Set("Vary", "Origin")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://a.example.test")

	p.ApplyToResponse(h, r)

	if got := h.Values("Vary"); len(got) != 1 || got[0] != "Origin" {
		t.Errorf("Vary = %v, want the single upstream Origin entry preserved (not duplicated)", got)
	}
}

func corsMaxAge(seconds int) *config.Duration {
	d := config.Duration(int64(seconds) * 1e9)
	return &d
}
