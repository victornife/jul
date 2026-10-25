// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestParseSelectorAndNormalizationTable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		in        Selector
		wantKind  SelectorKind
		wantHost  string
		wantURI   string
		wantPfx   string
		wantBroad bool
		wantErr   bool
	}{
		{
			name:      "exact_url with https default port preserved literally per ADR 0027 §1.1 and host lowercased",
			in:        Selector{Kind: SelectorExactURL, URL: "https://PORTAL.Example.COM:443/articles/42?lang=en"},
			wantKind:  SelectorExactURL,
			wantHost:  "portal.example.com:443",
			wantURI:   "/articles/42?lang=en",
			wantBroad: false,
		},
		{
			name:      "exact_url with non-default port preserved literally",
			in:        Selector{Kind: SelectorExactURL, URL: "https://portal.example.com:8443/articles/42"},
			wantKind:  SelectorExactURL,
			wantHost:  "portal.example.com:8443",
			wantURI:   "/articles/42",
			wantBroad: false,
		},
		{
			name:      "exact_url with host and uri fields and %2F preserved",
			in:        Selector{Kind: SelectorExactURL, Host: "API.Example.com", URI: "/v1/files/a%2Fb~test?token=secret"},
			wantKind:  SelectorExactURL,
			wantHost:  "api.example.com",
			wantURI:   "/v1/files/a%2Fb~test?token=secret",
			wantBroad: false,
		},
		{
			name:      "prefix preserves trailing slash",
			in:        Selector{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/articles/"},
			wantKind:  SelectorPrefix,
			wantHost:  "portal.example.com",
			wantPfx:   "/articles/",
			wantBroad: false,
		},
		{
			name:    "root prefix is rejected in favor of kind=host",
			in:      Selector{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/"},
			wantErr: true,
		},
		{
			name:      "glob anchored with single and double star",
			in:        Selector{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/articles/*/comments/**"},
			wantKind:  SelectorGlob,
			wantHost:  "portal.example.com",
			wantBroad: false,
		},
		{
			name:    "glob /** is rejected in favor of kind=host",
			in:      Selector{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/**"},
			wantErr: true,
		},
		{
			name:      "tag selector deduplicates and defaults to any",
			in:        Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"article:42", "article:42", "category:news"}},
			wantKind:  SelectorTag,
			wantHost:  "portal.example.com",
			wantBroad: false,
		},
		{
			name:      "host selector is broad",
			in:        Selector{Kind: SelectorHost, Host: "[::1]:8080"},
			wantKind:  SelectorHost,
			wantHost:  "[::1]:8080",
			wantBroad: true,
		},
		{
			name:      "all selector requires all=true and is broad",
			in:        Selector{Kind: SelectorAll, All: true},
			wantKind:  SelectorAll,
			wantBroad: true,
		},
		{
			name:      "legacy key selector",
			in:        Selector{Kind: SelectorKey, Key: "GET\nportal.example.com\n/articles/42"},
			wantKind:  SelectorKey,
			wantHost:  "portal.example.com",
			wantURI:   "/articles/42",
			wantBroad: false,
		},
		// Rejection cases: malformed, ambiguous, or cross-scope inputs must never fall back to broader purge.
		{
			name:    "reject empty selector kind",
			in:      Selector{},
			wantErr: true,
		},
		{
			name:    "reject all selector without All=true",
			in:      Selector{Kind: SelectorAll, All: false},
			wantErr: true,
		},
		{
			name:    "reject exact_url with fragment",
			in:      Selector{Kind: SelectorExactURL, URL: "https://portal.example.com/articles/42#section"},
			wantErr: true,
		},
		{
			name:    "reject exact_url with userinfo",
			in:      Selector{Kind: SelectorExactURL, URL: "https://user:pass@portal.example.com/articles/42"},
			wantErr: true,
		},
		{
			name:    "reject exact_url with mismatched host and url host",
			in:      Selector{Kind: SelectorExactURL, Host: "other.example.com", URL: "https://portal.example.com/articles/42"},
			wantErr: true,
		},
		{
			name:    "reject exact_url with dot segment",
			in:      Selector{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/articles/../admin"},
			wantErr: true,
		},
		{
			name:    "reject exact_url with encoded dot segment %2e%2e",
			in:      Selector{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/articles/%2e%2e/admin"},
			wantErr: true,
		},
		{
			name:    "reject prefix with query string",
			in:      Selector{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/articles?page=1"},
			wantErr: true,
		},
		{
			name:    "reject prefix with wildcard",
			in:      Selector{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/articles/*"},
			wantErr: true,
		},
		{
			name:    "reject prefix with double slash",
			in:      Selector{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "//articles"},
			wantErr: true,
		},
		{
			name:    "reject glob with mid-segment double star",
			in:      Selector{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/articles/**foo"},
			wantErr: true,
		},
		{
			name:    "reject glob with ? or [] character classes",
			in:      Selector{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/articles/[0-9]"},
			wantErr: true,
		},
		{
			name:    "reject glob with more than 8 wildcards",
			in:      Selector{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/*/*/*/*/*/*/*/*/*"},
			wantErr: true,
		},
		{
			name:    "reject wildcard host",
			in:      Selector{Kind: SelectorHost, Host: "*.example.com"},
			wantErr: true,
		},
		{
			name:    "reject tag selector with credential-like tag",
			in:      Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"token=secret123"}},
			wantErr: true,
		},
		{
			name:    "reject tag selector with invalid tagmatch mode",
			in:      Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"article:42"}, TagMatch: "xor"},
			wantErr: true,
		},
		{
			name:    "reject mixed fields from another selector kind",
			in:      Selector{Kind: SelectorHost, Host: "portal.example.com", Prefix: "/articles"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			norm, err := ParseSelector(tc.in, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %+v, got normalized %+v", tc.in, norm)
				}
				var selErr *SelectorError
				if !errors.As(err, &selErr) {
					t.Fatalf("expected *SelectorError, got %T: %v", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if norm.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", norm.Kind, tc.wantKind)
			}
			if norm.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", norm.Host, tc.wantHost)
			}
			if tc.wantURI != "" && norm.URI != tc.wantURI {
				t.Errorf("URI = %q, want %q", norm.URI, tc.wantURI)
			}
			if tc.wantPfx != "" && norm.Prefix != tc.wantPfx {
				t.Errorf("Prefix = %q, want %q", norm.Prefix, tc.wantPfx)
			}
			if norm.BroadScope != tc.wantBroad {
				t.Errorf("BroadScope = %v, want %v", norm.BroadScope, tc.wantBroad)
			}
			if norm.DigestHex() == "" {
				t.Errorf("DigestHex() was empty")
			}
		})
	}
}

func TestMatchesResourceBoundariesAndEscapedSeparators(t *testing.T) {
	t.Parallel()

	// 1. Escaped %2F vs literal / must never be equated.
	exactEscaped, err := ParseSelector(Selector{
		Kind: SelectorExactURL,
		Host: "portal.example.com",
		URI:  "/files/a%2Fb?ver=1",
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector exactEscaped: %v", err)
	}
	if !exactEscaped.MatchesResource("portal.example.com", "/files/a%2Fb?ver=1", nil) {
		t.Errorf("expected exactEscaped to match identical %%2F path")
	}
	if exactEscaped.MatchesResource("portal.example.com", "/files/a/b?ver=1", nil) {
		t.Errorf("exactEscaped must NOT equate %%2F with literal /")
	}
	if exactEscaped.MatchesResource("portal.example.com", "/files/a%2Fb?ver=2", nil) {
		t.Errorf("exactEscaped must NOT match different query string")
	}

	// 2. Prefix segment boundary: /articles matches /articles and /articles/42?x=1, NEVER /articles-archive.
	pfx, err := ParseSelector(Selector{
		Kind:   SelectorPrefix,
		Host:   "portal.example.com",
		Prefix: "/articles",
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector prefix: %v", err)
	}
	if !pfx.MatchesResource("portal.example.com", "/articles", nil) {
		t.Errorf("expected prefix /articles to match /articles")
	}
	if !pfx.MatchesResource("portal.example.com", "/articles/42?page=2", nil) {
		t.Errorf("expected prefix /articles to match /articles/42?page=2")
	}
	if pfx.MatchesResource("portal.example.com", "/articles-archive/42", nil) {
		t.Errorf("prefix /articles must NOT match sibling /articles-archive/42")
	}
	if pfx.MatchesResource("portal.example.com:8443", "/articles/42", nil) {
		t.Errorf("prefix on portal.example.com must NOT match portal.example.com:8443")
	}

	// 3. Anchored glob: * matches single segment, ** matches 0+ segments, and partial-segment * (e.g. bundle-*.min.js).
	globSel, err := ParseSelector(Selector{
		Kind:    SelectorGlob,
		Host:    "portal.example.com",
		Pattern: "/articles/*/comments/**",
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector glob: %v", err)
	}
	if !globSel.MatchesResource("portal.example.com", "/articles/42/comments", nil) {
		t.Errorf("expected /** to match 0 trailing segments")
	}
	if !globSel.MatchesResource("portal.example.com", "/articles/42/comments/p/1?sort=asc", nil) {
		t.Errorf("expected /** to match multiple trailing segments and query")
	}
	if globSel.MatchesResource("portal.example.com", "/articles/42/43/comments/1", nil) {
		t.Errorf("single * must not match across multiple segments")
	}

	partialGlob, err := ParseSelector(Selector{
		Kind:    SelectorGlob,
		Host:    "portal.example.com",
		Pattern: "/assets/bundle-*-*.min.js",
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector partialGlob: %v", err)
	}
	if !partialGlob.MatchesResource("portal.example.com", "/assets/bundle-v1-prod.min.js", nil) {
		t.Errorf("expected partialGlob to match /assets/bundle-v1-prod.min.js")
	}
	if partialGlob.MatchesResource("portal.example.com", "/assets/bundle-v1.css", nil) {
		t.Errorf("expected partialGlob not to match /assets/bundle-v1.css")
	}
	if partialGlob.MatchesResource("other.example.com", "/assets/bundle-v1-prod.min.js", nil) {
		t.Errorf("expected partialGlob not to match different host")
	}

	// 4. Tag matching: ANY vs ALL and strict host isolation.
	tagAll, err := ParseSelector(Selector{
		Kind:     SelectorTag,
		Host:     "portal.example.com",
		Tags:     []string{"article:42", "locale:en"},
		TagMatch: TagMatchAll,
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector tagAll: %v", err)
	}
	if tagAll.MatchesResource("portal.example.com", "/a", []string{"article:42"}) {
		t.Errorf("TagMatchAll must not match when only 1 of 2 tags is present")
	}
	if !tagAll.MatchesResource("portal.example.com", "/a", []string{"locale:en", "article:42", "extra"}) {
		t.Errorf("TagMatchAll should match when all required tags are present")
	}
	if tagAll.MatchesResource("other.example.com", "/a", []string{"locale:en", "article:42"}) {
		t.Errorf("TagMatchAll must NOT match another host with identical tags")
	}
}

func TestRedactedSummaryAndDeriveRouteCutoverSelectors(t *testing.T) {
	t.Parallel()

	sel, err := ParseSelector(Selector{
		Kind: SelectorExactURL,
		URL:  "https://portal.example.com/reset?token=super-secret-value&user=alice",
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector: %v", err)
	}
	redacted := sel.RedactedSummary()
	if strings.Contains(redacted.URL, "super-secret-value") || strings.Contains(redacted.URI, "super-secret-value") {
		t.Fatalf("RedactedSummary leaked query secret: %+v", redacted)
	}
	if !strings.Contains(redacted.URI, "<redacted>") {
		t.Fatalf("expected <redacted> in RedactedSummary URI, got %q", redacted.URI)
	}

	// #624 successor contract: DeriveRouteCutoverSelectors.
	routeID := "route-articles"
	beforeCfg := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen:      ":443",
				ServerNames: []string{"portal.example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID:   &routeID,
						Match:     config.MatchConfig{Type: "prefix", Path: "/articles/"},
						ProxyPass: "http://upstream-v1",
					},
				},
			},
		},
	}
	afterCfg := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen:      ":443",
				ServerNames: []string{"portal.example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID:   &routeID,
						Match:     config.MatchConfig{Type: "prefix", Path: "/articles/"},
						ProxyPass: "http://upstream-v2",
					},
				},
			},
		},
	}
	cutoverSels := DeriveRouteCutoverSelectors(beforeCfg, afterCfg)
	if len(cutoverSels) != 1 || cutoverSels[0].Kind != SelectorPrefix || cutoverSels[0].Host != "portal.example.com" || cutoverSels[0].Prefix != "/articles/" {
		t.Fatalf("unexpected cutoverSels: %+v", cutoverSels)
	}

	// Also verify ParseSelector with RouteID against active configuration.
	withRouteID, err := ParseSelector(Selector{
		Kind:    SelectorPrefix,
		RouteID: routeID,
	}, beforeCfg)
	if err != nil {
		t.Fatalf("ParseSelector with RouteID: %v", err)
	}
	if withRouteID.Host != "portal.example.com" || withRouteID.Prefix != "/articles/" {
		t.Fatalf("unexpected RouteID resolution: %+v", withRouteID)
	}

	// Additional DeriveRouteCutoverSelectors branches: upstream changes, exact/root/regex routes, wildcard/empty server_names.
	upBefore := &config.Config{
		Upstreams: []config.UpstreamConfig{
			{Name: "app", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: "10.0.0.1:8080"}}},
		},
		Servers: []config.ServerConfig{
			{
				Listen:      ":443",
				ServerNames: []string{"portal.example.com"},
				Locations: []config.LocationConfig{
					{Match: config.MatchConfig{Type: "exact", Path: "/health"}, ProxyPass: "http://app"},
					{Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: "http://app"},
					{Match: config.MatchConfig{Type: "regex", Path: "^/v1/.*"}, ProxyPass: "http://app"},
				},
			},
			{
				Listen:      ":8080",
				ServerNames: []string{"*.example.com"},
				Locations: []config.LocationConfig{
					{Match: config.MatchConfig{Type: "prefix", Path: "/wild/"}, ProxyPass: "http://app"},
				},
			},
			{
				Listen: ":8081",
				Locations: []config.LocationConfig{
					{Match: config.MatchConfig{Type: "prefix", Path: "/anon/"}, ProxyPass: "http://app"},
				},
			},
		},
	}
	upAfter := &config.Config{
		Upstreams: []config.UpstreamConfig{
			{Name: "app", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: "10.0.0.2:8080"}}},
		},
		Servers: upBefore.Servers,
	}
	upSels := DeriveRouteCutoverSelectors(upBefore, upAfter)
	if len(upSels) < 2 {
		t.Fatalf("expected selectors from upstream change across exact/root/regex/wildcard routes, got %+v", upSels)
	}
	if nilSels := DeriveRouteCutoverSelectors(nil, nil); nilSels != nil {
		t.Fatalf("expected nil for DeriveRouteCutoverSelectors(nil, nil)")
	}

	// Exhaustive validation error branches in selector.go (host, URI, glob, key, route_id).
	invalidSelectors := []Selector{
		{Kind: SelectorHost, Host: ""},
		{Kind: SelectorHost, Host: "portal .example.com"},
		{Kind: SelectorHost, Host: strings.Repeat("a", MaxSelectorHostBytes+1)},
		{Kind: SelectorHost, Host: "bad/host"},
		{Kind: SelectorHost, Host: "[]"},
		{Kind: SelectorHost, Host: "[not-an-ip]"},
		{Kind: SelectorHost, Host: "[::1]extra"},
		{Kind: SelectorHost, Host: "[::1]:"},
		{Kind: SelectorHost, Host: "a:b:c"},
		{Kind: SelectorHost, Host: "portal.example.com:"},
		{Kind: SelectorHost, Host: "portal.example.com:0"},
		{Kind: SelectorHost, Host: "portal.example.com:99999"},
		{Kind: SelectorHost, Host: "portal$example.com"},
		{Kind: SelectorExactURL, URL: "ftp://portal.example.com/a"},
		{Kind: SelectorExactURL, URL: "http://"},
		{Kind: SelectorExactURL, Host: "portal.example.com", URI: "relative/path"},
		{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/bad%ZZhex"},
		{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/bad%00null"},
		{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/bad space"},
		{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/" + strings.Repeat("a", MaxSelectorURIBytes)},
		{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: ""},
		{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/" + strings.Repeat("a", MaxSelectorPatternBytes)},
		{Kind: SelectorGlob, Host: "portal.example.com", Pattern: ""},
		{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "relative/*"},
		{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/bad%ZZ/*"},
		{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/bad%00/*"},
		{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/bad space/*"},
		{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/a/../b/*"},
		{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/a/**/**/c/**"},
		{Kind: SelectorGlob, Host: "portal.example.com", Pattern: strings.Repeat("/seg", MaxGlobSegments+2)},
		{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/" + strings.Repeat("a", MaxSelectorPatternBytes)},
		{Kind: SelectorKey, Key: ""},
		{Kind: SelectorKey, Key: "POST\nportal.example.com\n/a"},
		{Kind: SelectorKey, Key: "GET\nportal.example.com"},
		{Kind: SelectorKey, Key: "GET\nportal.example.com\nrelative"},
		{Kind: SelectorKey, Key: "GET\nportal.example.com\n/a", All: true},
		{Kind: SelectorExactURL, URL: "https://portal.example.com/a", Prefix: "/a"},
		{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/a", All: true},
		{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/a/*", All: true},
		{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"t1"}, All: true},
		{Kind: SelectorAll, All: true, Host: "portal.example.com"},
		{Kind: SelectorPrefix, RouteID: routeID, Host: "other.example.com", Prefix: "/articles/"},
		{Kind: SelectorTag, Host: "portal.example.com", Tags: make([]string, MaxSelectorTags+1)},
		{Kind: SelectorPrefix, RouteID: "missing-route"},
	}
	for _, inv := range invalidSelectors {
		if _, err := ParseSelector(inv, beforeCfg); err == nil {
			t.Errorf("expected ParseSelector(%+v) to fail", inv)
		} else {
			_ = err.Error()
		}
	}
	_ = (&SelectorError{Message: "no-field"}).Error()
	if _, err := ParseSelector(Selector{Kind: SelectorPrefix, RouteID: routeID}, nil); err == nil {
		t.Errorf("expected error when RouteID is used with nil config")
	}

	// RouteID resolution across exact_url, glob, tag, and host selector kinds.
	for _, s := range []Selector{
		{Kind: SelectorExactURL, RouteID: routeID, URI: "/articles/42"},
		{Kind: SelectorGlob, RouteID: routeID, Pattern: "/articles/*"},
		{Kind: SelectorTag, RouteID: routeID, Tags: []string{"article:42"}},
		{Kind: SelectorHost, RouteID: routeID},
		{Kind: SelectorExactURL, URL: "https://portal.example.com", Host: "portal.example.com"},
		{Kind: SelectorKey, Key: "GET\nportal.example.com\n/articles/42?secret=1"},
	} {
		norm, err := ParseSelector(s, beforeCfg)
		if err != nil {
			t.Errorf("ParseSelector(%+v) failed: %v", s, err)
		} else {
			_ = norm.RedactedSummary()
		}
	}
	for _, badURL := range []Selector{
		{Kind: SelectorExactURL, URL: "https://portal.example.com?q=1"},
		{Kind: SelectorExactURL, URL: "https://portal.example.com/a", Host: "other.example.com"},
		{Kind: SelectorExactURL, URL: "https://portal.example.com/a", Host: "bad host"},
		{Kind: SelectorExactURL, URL: "https://" + strings.Repeat("a", MaxSelectorURIBytes+MaxSelectorHostBytes+20)},
	} {
		if _, err := ParseSelector(badURL, beforeCfg); err == nil {
			t.Errorf("expected ParseSelector(%+v) to fail", badURL)
		}
	}
	_ = DeriveRouteCutoverSelectors(nil, upAfter)
}

func TestTagValidationAndHeaderExtraction(t *testing.T) {
	t.Parallel()

	if err := ValidateTag("article:42_v1.0-rc+1/section=main"); err != nil {
		t.Fatalf("expected valid tag, got %v", err)
	}
	for _, bad := range []string{
		"",
		strings.Repeat("a", MaxTagLength+1),
		"tag with spaces",
		"Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		"token=abcd123456",
		"api_key=secret123",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig",
	} {
		if err := ValidateTag(bad); err == nil {
			t.Errorf("expected ValidateTag(%q) to fail", bad)
		}
	}

	h := make(http.Header)
	h.Add("Cache-Tag", "article:42, category:news , article:42")
	h.Add("Surrogate-Key", "tenant:acme")
	tags, present, skipReason := extractOriginTags(h, []string{"Cache-Tag", "Surrogate-Key"})
	if !present || skipReason != "" || len(tags) != 3 || tags[0] != "article:42" || tags[1] != "category:news" || tags[2] != "tenant:acme" {
		t.Fatalf("unexpected extracted tags: present=%v skip=%q tags=%v", present, skipReason, tags)
	}

	// Verify credential-like tag triggers TagSkipInvalid conservative skip!
	hBad := make(http.Header)
	hBad.Set("Cache-Tag", "article:42, token=secret123")
	if _, _, reason := extractOriginTags(hBad, []string{"Cache-Tag"}); reason != TagSkipInvalid {
		t.Fatalf("expected TagSkipInvalid for credential tag, got %q", reason)
	}

	stripConfiguredTagHeaders(h, []string{"Cache-Tag", "Surrogate-Key"})
	if h.Get("Cache-Tag") != "" || h.Get("Surrogate-Key") != "" {
		t.Fatalf("expected tag headers to be stripped, got %v", h)
	}

	// Cover oversize raw header, empty line, consecutive commas, >32 tags, >1024 tag bytes, and stripOriginTagHeadersFromResponse.
	hEmpty := http.Header{"Cache-Tag": []string{"   "}}
	if tags, present, reason := extractOriginTags(hEmpty, []string{"Cache-Tag"}); !present || reason != "" || len(tags) != 0 {
		t.Fatalf("expected empty line to be valid with 0 tags, got %v %v %q", tags, present, reason)
	}
	hDoubleComma := http.Header{"Cache-Tag": []string{"a,,b"}}
	if _, _, reason := extractOriginTags(hDoubleComma, []string{"Cache-Tag"}); reason != TagSkipInvalid {
		t.Fatalf("expected TagSkipInvalid for 'a,,b', got %q", reason)
	}
	hRawOversize := http.Header{"Cache-Tag": []string{strings.Repeat("a", MaxRawTagHeaderBytes+1)}}
	if _, _, reason := extractOriginTags(hRawOversize, []string{"Cache-Tag"}); reason != TagSkipOversize {
		t.Fatalf("expected TagSkipOversize for >4096 raw header bytes, got %q", reason)
	}
	var manyTags []string
	for i := 0; i <= MaxTagsPerEntry; i++ {
		manyTags = append(manyTags, "t"+strings.Repeat("x", 4)+string(rune('a'+(i%26)))+string(rune('0'+(i/26))))
	}
	hTooMany := http.Header{"Cache-Tag": []string{strings.Join(manyTags, ",")}}
	if _, _, reason := extractOriginTags(hTooMany, []string{"Cache-Tag"}); reason != TagSkipOversize {
		t.Fatalf("expected TagSkipOversize for >32 tags, got %q", reason)
	}
	var bigTags []string
	for i := 0; i < 12; i++ {
		bigTags = append(bigTags, strings.Repeat("b", 100)+string(rune('a'+i)))
	}
	hTooManyBytes := http.Header{"Cache-Tag": []string{strings.Join(bigTags, ",")}}
	if _, _, reason := extractOriginTags(hTooManyBytes, []string{"Cache-Tag"}); reason != TagSkipOversize {
		t.Fatalf("expected TagSkipOversize for >1024 tag bytes, got %q", reason)
	}

	liveHdr := http.Header{"Cache-Tag": []string{"origin-added"}, "Surrogate-Key": []string{"s1"}}
	snapHdr := http.Header{"Cache-Tag": []string{"pre-existing"}}
	stripOriginTagHeadersFromResponse(liveHdr, snapHdr, []string{"Cache-Tag", "Surrogate-Key"})
	if liveHdr.Get("Cache-Tag") != "" || liveHdr.Get("Surrogate-Key") != "" {
		t.Fatalf("unexpected stripOriginTagHeadersFromResponse result: %v", liveHdr)
	}
	_, _, _ = extractOriginTags(nil, nil)
	stripConfiguredTagHeaders(nil, nil)
	stripOriginTagHeadersFromResponse(nil, nil, nil)
}

func FuzzParseSelector(f *testing.F) {
	f.Add("exact_url", "portal.example.com", "https://portal.example.com/articles/42?a=1", "/articles/42", "", "", "article:42")
	f.Add("prefix", "portal.example.com:8443", "", "", "/articles/2026/", "", "")
	f.Add("glob", "portal.example.com", "", "", "", "/articles/*/comments/**", "")
	f.Add("tag", "portal.example.com", "", "", "", "", "article:42,tenant:acme")
	f.Add("host", "portal.example.com", "", "", "", "", "")
	f.Add("all", "", "", "", "", "", "")

	f.Fuzz(func(t *testing.T, kind, host, rawURL, uri, prefix, pattern, tag string) {
		var tags []string
		if tag != "" {
			tags = strings.Split(tag, ",")
		}
		sel := Selector{
			Kind:    SelectorKind(kind),
			Host:    host,
			URL:     rawURL,
			URI:     uri,
			Prefix:  prefix,
			Pattern: pattern,
			Tags:    tags,
		}
		norm, err := ParseSelector(sel, nil)
		if err != nil {
			return
		}
		// Invariant: ParseSelector must never return SelectorAll unless Kind=="all" and All==true.
		if norm.Kind == SelectorAll && (sel.Kind != SelectorAll || !sel.All) {
			t.Fatalf("ParseSelector produced SelectorAll without explicit All=true: %+v", sel)
		}
		_ = norm.MatchesResource("portal.example.com", "/articles/42?a=1", []string{"article:42"})
		_ = norm.RedactedSummary()
	})
}
