// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
	"jul/internal/server"
)

func strPtrTest(s string) *string { return &s }

func TestLocationSharingTypedPatchOperations(t *testing.T) {
	baseCfg := func() *config.Config {
		return &config.Config{
			Global: config.GlobalConfig{ConfigAuthority: "managed"},
			Servers: []config.ServerConfig{{
				Listen:      "127.0.0.1:8080",
				ServerNames: []string{"app.example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID: strPtrTest("dash"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/dash"},
						Return:  200,
					},
					{
						Match:  config.MatchConfig{Type: "prefix", Path: "/idless"},
						Return: 200,
					},
					{
						RouteID: strPtrTest("${env:SECRET_ROUTE_ID}"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/refid"},
						Return:  200,
					},
				},
			}},
		}
	}

	t.Run("refuses route without literal route_id before any side effect", func(t *testing.T) {
		cfg := baseCfg()
		_, err := applyPatch(cfg, patchRequest{
			Op:          "location_set_sharing",
			Listen:      "127.0.0.1:8080",
			ServerNames: []string{"app.example.com"},
			MatchType:   "prefix",
			Path:        "/idless",
			Sharing: &locationSharingPatch{
				Mode: "restricted",
			},
		})
		if err == nil || !strings.Contains(err.Error(), "no literal route_id") || !strings.Contains(err.Error(), "raw TOML") {
			t.Fatalf("expected refusal directing operator to raw TOML, got: %v", err)
		}
		if cfg.Servers[0].Locations[1].Sharing != nil || cfg.Servers[0].Locations[1].RouteID != nil {
			t.Fatal("failed patch must not mutate location or mint route_id")
		}

		// Also refuse when route_id is a secret reference.
		_, err = applyPatch(cfg, patchRequest{
			Op:          "location_set_sharing",
			Listen:      "127.0.0.1:8080",
			ServerNames: []string{"app.example.com"},
			MatchType:   "prefix",
			Path:        "/refid",
			Sharing: &locationSharingPatch{
				Mode: "restricted",
			},
		})
		if err == nil || !strings.Contains(err.Error(), "no literal route_id") {
			t.Fatalf("expected refusal for reference route_id, got: %v", err)
		}
	})

	t.Run("sets and clears sharing by route_id or coordinates and is repeatable", func(t *testing.T) {
		cfg := baseCfg()
		setOp := patchRequest{
			Op:      "location_set_sharing",
			RouteID: strPtrTest("dash"),
			Sharing: &locationSharingPatch{
				Mode:             "restricted",
				AudienceLabel:    strPtrTest("engineering"),
				ExternalEnforcer: strPtrTest("cloudflare-access:eng"),
			},
		}
		if !IsPatchOpRepeatable(setOp) || !IsPatchBatchRepeatable([]patchRequest{setOp}) {
			t.Fatal("location_set_sharing must be classified as repeatable")
		}
		if IsPatchBatchRepeatable(nil) || IsPatchBatchRepeatable([]patchRequest{{Op: "location_add"}}) {
			t.Fatal("location_add and empty batch must not be classified as repeatable")
		}

		if _, err := applyPatch(cfg, setOp); err != nil {
			t.Fatalf("applyPatch set: %v", err)
		}
		got := cfg.Servers[0].Locations[0].Sharing
		if got == nil || got.Mode != "restricted" || got.AudienceLabelValue() != "engineering" || got.ExternalEnforcerValue() != "cloudflare-access:eng" {
			t.Fatalf("unexpected Sharing after set: %+v", got)
		}

		// Repeating the exact same operation produces an identical configuration.
		raw1, _ := config.Marshal(cfg)
		if _, err := applyPatch(cfg, setOp); err != nil {
			t.Fatalf("repeat applyPatch set: %v", err)
		}
		raw2, _ := config.Marshal(cfg)
		if !bytes.Equal(raw1, raw2) {
			t.Fatal("repeating location_set_sharing must be a no-op")
		}

		// Clear via location_clear_sharing and repeat clear.
		clearOp := patchRequest{
			Op:      "location_clear_sharing",
			RouteID: strPtrTest("dash"),
		}
		if !IsPatchOpRepeatable(clearOp) {
			t.Fatal("location_clear_sharing must be repeatable")
		}
		if _, err := applyPatch(cfg, clearOp); err != nil {
			t.Fatalf("applyPatch clear: %v", err)
		}
		if cfg.Servers[0].Locations[0].Sharing != nil {
			t.Fatal("expected Sharing to be nil after clear")
		}
		if _, err := applyPatch(cfg, clearOp); err != nil {
			t.Fatalf("repeat applyPatch clear: %v", err)
		}

		// Set via coordinates + matching route_id, then clear via location_set_sharing with nil payload.
		if _, err := applyPatch(cfg, patchRequest{
			Op:          "location_set_sharing",
			Listen:      "127.0.0.1:8080",
			ServerNames: []string{"app.example.com"},
			MatchType:   "prefix",
			Path:        "/dash",
			RouteID:     strPtrTest("dash"),
			Sharing:     &locationSharingPatch{Mode: "preview"},
		}); err != nil {
			t.Fatalf("applyPatch by coordinates: %v", err)
		}
		if _, err := applyPatch(cfg, patchRequest{
			Op:      "location_set_sharing",
			RouteID: strPtrTest("dash"),
			Sharing: nil,
		}); err != nil {
			t.Fatalf("applyPatch nil sharing clear: %v", err)
		}
		if cfg.Servers[0].Locations[0].Sharing != nil {
			t.Fatal("expected nil sharing payload to clear block")
		}

		// Error cases: mismatched route_id, missing target, empty/invalid mode.
		for _, bad := range []patchRequest{
			{
				Op:          "location_set_sharing",
				Listen:      "127.0.0.1:8080",
				ServerNames: []string{"app.example.com"},
				MatchType:   "prefix",
				Path:        "/dash",
				RouteID:     strPtrTest("wrong-id"),
				Sharing:     &locationSharingPatch{Mode: "public"},
			},
			{
				Op:      "location_set_sharing",
				RouteID: strPtrTest("nonexistent"),
				Sharing: &locationSharingPatch{Mode: "public"},
			},
			{
				Op:      "location_set_sharing",
				Sharing: &locationSharingPatch{Mode: "public"},
			},
			{
				Op:      "location_set_sharing",
				RouteID: strPtrTest("dash"),
				Sharing: &locationSharingPatch{Mode: ""},
			},
			{
				Op:      "location_set_sharing",
				RouteID: strPtrTest("dash"),
				Sharing: &locationSharingPatch{Mode: "bogus"},
			},
		} {
			if _, err := applyPatch(cfg, bad); err == nil {
				t.Fatalf("expected error for invalid request %+v", bad)
			}
		}
	})

	t.Run("file_owned authority refuses patch apply with 409 config_authority_read_only", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Global.ConfigAuthority = "file_owned"
		raw, _ := config.Marshal(cfg)
		ver := configVersion(raw)
		s := newTestServer(t, config.AdminConfig{}, Deps{
			LoadConfig:    func() (*config.Config, error) { return cfg.Clone() },
			ReadConfigRaw: func() ([]byte, error) { return raw, nil },
			Authority: func() ConfigAuthorityStatus {
				return ConfigAuthorityStatus{Mode: "file_owned", Source: "explicit", ConfigState: "file_owned_clean"}
			},
		})
		body, _ := json.Marshal(patchApplyRequest{
			BaseVersion: ver,
			Ops: []patchRequest{{
				Op:      "location_set_sharing",
				RouteID: strPtrTest("dash"),
				Sharing: &locationSharingPatch{Mode: "restricted"},
			}},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/patch/apply", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "config_authority_read_only") {
			t.Fatalf("status = %d, body = %s; want 409 config_authority_read_only", rr.Code, rr.Body.String())
		}
	})
}

func TestAudienceDeltaClassificationMatrix(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})

	makeSingleLocConfig := func(srv config.ServerConfig, loc config.LocationConfig) *config.Config {
		if srv.Listen == "" {
			srv.Listen = "127.0.0.1:8080"
		}
		if loc.RouteID == nil {
			loc.RouteID = strPtrTest("app")
		}
		if loc.Match.Path == "" {
			loc.Match = config.MatchConfig{Type: "prefix", Path: "/"}
		}
		if loc.Return == 0 && loc.ProxyPass == "" && !loc.Deny {
			loc.Return = 200
		}
		srv.Locations = []config.LocationConfig{loc}
		return &config.Config{Servers: []config.ServerConfig{srv}}
	}

	classifySingle := func(t *testing.T, before, after *config.Config) SharingEntryDelta {
		t.Helper()
		rep := s.BuildSharingPlanReport(before, before, after, after, true, nil, true)
		if len(rep.Entries) == 0 {
			return SharingEntryDelta{Class: AudienceDeltaUnchanged}
		}
		return rep.Entries[0]
	}

	t.Run("metadata_only intent change vs mode widening acknowledgement", func(t *testing.T) {
		b := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Sharing: &config.SharingConfig{Mode: "restricted", AudienceLabel: strPtrTest("team-a")},
		})
		aNarrow := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Sharing: &config.SharingConfig{Mode: "private_network", AudienceLabel: strPtrTest("team-b")},
		})
		d1 := classifySingle(t, b, aNarrow)
		if d1.Class != AudienceDeltaMetadataOnly || d1.TriggersRetirement || d1.RequiresAcknowledgement {
			t.Fatalf("narrowing declared mode alone must be metadata_only without retirement or ack: %+v", d1)
		}

		aWide := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Sharing: &config.SharingConfig{Mode: "public"},
		})
		d2 := classifySingle(t, b, aWide)
		if d2.Class != AudienceDeltaMetadataOnly || d2.TriggersRetirement || !d2.RequiresAcknowledgement || !d2.DeclaredModeWidened {
			t.Fatalf("widening declared mode must be metadata_only with acknowledgement and no retirement: %+v", d2)
		}
	})

	t.Run("CIDR permitted set shrinkage, growth, removing last allow, and RR11 secret reference", func(t *testing.T) {
		bAllow := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Auth: &config.AuthConfig{Allow: []string{"10.0.0.0/8"}},
		})
		aNarrow := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Auth: &config.AuthConfig{Allow: []string{"10.1.0.0/16"}},
		})
		if got := classifySingle(t, bAllow, aNarrow); got.Class != AudienceDeltaNarrowed || !got.TriggersRetirement {
			t.Fatalf("subnet narrowing: got %+v, want narrowed", got)
		}
		if got := classifySingle(t, aNarrow, bAllow); got.Class != AudienceDeltaWidened || !got.RequiresAcknowledgement {
			t.Fatalf("subnet widening: got %+v, want widened", got)
		}

		// Removing the last allow entry leaves only deny, so the permitted set grows to all other addresses (widened).
		bAllowAndDeny := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Auth: &config.AuthConfig{Allow: []string{"10.0.0.0/8"}, Deny: []string{"10.0.0.1/32"}},
		})
		aOnlyDeny := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Auth: &config.AuthConfig{Deny: []string{"10.0.0.1/32"}},
		})
		if got := classifySingle(t, bAllowAndDeny, aOnlyDeny); got.Class != AudienceDeltaWidened {
			t.Fatalf("removing last allow entry must be widened, got %+v", got)
		}

		// Disjoint CIDR change -> uncertain.
		aDisjoint := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Auth: &config.AuthConfig{Allow: []string{"192.168.0.0/16"}},
		})
		if got := classifySingle(t, bAllow, aDisjoint); got.Class != AudienceDeltaUncertain {
			t.Fatalf("disjoint CIDR change must be uncertain, got %+v", got)
		}

		// RR11 secret-reference oracle protection: unchanged ${env:...} is unchanged;
		// comparing ${env:...} against any literal candidate is always uncertain regardless of resolved value.
		bRefRaw := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Auth: &config.AuthConfig{Allow: []string{"${env:OFFICE_CIDR}"}},
		})
		bRefEff1 := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Auth: &config.AuthConfig{Allow: []string{"10.1.2.3/32"}},
		})
		bRefEff2 := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Auth: &config.AuthConfig{Allow: []string{"203.0.113.9/32"}},
		})
		// Unchanged written reference -> unchanged.
		repUnchanged := s.BuildSharingPlanReport(bRefRaw, bRefEff1, bRefRaw, bRefEff1, true, nil, true)
		if len(repUnchanged.Entries) != 0 {
			t.Fatalf("unchanged ${env:...} reference must classify as unchanged, got %+v", repUnchanged.Entries)
		}
		// Candidate probing 0.0.0.0/1 against ${env:OFFICE_CIDR} must produce identical uncertain output for both resolved values!
		probeCand := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Auth: &config.AuthConfig{Allow: []string{"0.0.0.0/1"}},
		})
		repProbe1 := s.BuildSharingPlanReport(bRefRaw, bRefEff1, probeCand, probeCand, true, nil, true)
		repProbe2 := s.BuildSharingPlanReport(bRefRaw, bRefEff2, probeCand, probeCand, true, nil, true)
		if repProbe1.Entries[0].Class != AudienceDeltaUncertain || repProbe2.Entries[0].Class != AudienceDeltaUncertain {
			t.Fatalf("RR11 violation: secret reference comparison must be uncertain (%s vs %s)", repProbe1.Entries[0].Class, repProbe2.Entries[0].Class)
		}
	})

	t.Run("bind changes, credentials, client_auth, CORS, and WAF SD-14 rules", func(t *testing.T) {
		// Bind specific IPv4 -> 0.0.0.0 (same port) is widened; reverse is narrowed; loopback -> LAN is uncertain.
		bLoop := makeSingleLocConfig(config.ServerConfig{Listen: "127.0.0.1:8080"}, config.LocationConfig{})
		aWild := makeSingleLocConfig(config.ServerConfig{Listen: "0.0.0.0:8080"}, config.LocationConfig{})
		aLan := makeSingleLocConfig(config.ServerConfig{Listen: "192.168.1.10:8080"}, config.LocationConfig{})
		if got := classifySingle(t, bLoop, aWild); got.Class != AudienceDeltaWidened {
			t.Fatalf("127.0.0.1 -> 0.0.0.0 want widened, got %+v", got)
		}
		if got := classifySingle(t, aWild, bLoop); got.Class != AudienceDeltaNarrowed {
			t.Fatalf("0.0.0.0 -> 127.0.0.1 want narrowed, got %+v", got)
		}
		if got := classifySingle(t, bLoop, aLan); got.Class != AudienceDeltaUncertain {
			t.Fatalf("127.0.0.1 -> 192.168.1.10 want uncertain, got %+v", got)
		}

		// Credential added / removed / replaced.
		bOpen := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{})
		aBasic := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Auth: &config.AuthConfig{Basic: &config.BasicAuthConfig{File: "/etc/htpasswd"}},
		})
		aJWT := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			Auth: &config.AuthConfig{JWT: &config.JWTAuthConfig{JWKSURL: "https://idp.example/jwks"}},
		})
		if got := classifySingle(t, bOpen, aBasic); got.Class != AudienceDeltaNarrowed {
			t.Fatalf("adding Basic auth want narrowed, got %+v", got)
		}
		if got := classifySingle(t, aBasic, bOpen); got.Class != AudienceDeltaWidened {
			t.Fatalf("removing Basic auth want widened, got %+v", got)
		}
		if got := classifySingle(t, aBasic, aJWT); got.Class != AudienceDeltaUncertain {
			t.Fatalf("replacing Basic with JWT want uncertain, got %+v", got)
		}

		// client_auth: none <-> request is uncertain; request -> require is narrowed; require -> request (without require_client_cert) is widened.
		tlsSrv := func(mode string) config.ServerConfig {
			return config.ServerConfig{
				Listen: "127.0.0.1:8443",
				TLS: &config.TLSConfig{
					Enabled: true,
					Cert:    "c.pem",
					Key:     "k.pem",
					ClientAuth: &config.ClientAuthConfig{
						Mode:   mode,
						CAFile: "/etc/ca.pem",
					},
				},
			}
		}
		bNone := makeSingleLocConfig(tlsSrv("none"), config.LocationConfig{})
		bReq := makeSingleLocConfig(tlsSrv("request"), config.LocationConfig{})
		bMust := makeSingleLocConfig(tlsSrv("require"), config.LocationConfig{})
		if got := classifySingle(t, bNone, bReq); got.Class != AudienceDeltaUncertain {
			t.Fatalf("client_auth none -> request want uncertain, got %+v", got)
		}
		if got := classifySingle(t, bReq, bMust); got.Class != AudienceDeltaNarrowed {
			t.Fatalf("client_auth request -> require want narrowed, got %+v", got)
		}
		if got := classifySingle(t, bMust, bReq); got.Class != AudienceDeltaWidened {
			t.Fatalf("client_auth require -> request want widened, got %+v", got)
		}

		// CORS: adding origin or enabling credentials -> widened; removing origin -> narrowed.
		bCors := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			CORS: &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example"}},
		})
		aCorsWide := makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
			CORS: &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example", "https://b.example"}, AllowCredentials: true},
		})
		if got := classifySingle(t, bCors, aCorsWide); got.Class != AudienceDeltaWidened {
			t.Fatalf("CORS origin+credentials added want widened, got %+v", got)
		}
		if got := classifySingle(t, aCorsWide, bCors); got.Class != AudienceDeltaNarrowed {
			t.Fatalf("CORS origin+credentials removed want narrowed, got %+v", got)
		}

		// WAF SD-14: provably without ctl:ruleEngine vs with ctl:ruleEngine / directives_files.
		wafLoc := func(enabled bool, mode, inline string, files []string) *config.Config {
			return makeSingleLocConfig(config.ServerConfig{}, config.LocationConfig{
				WAF: &config.WAFConfig{
					Enabled:         enabled,
					Mode:            mode,
					InlineRules:     inline,
					DirectivesFiles: files,
				},
			})
		}
		wOff := wafLoc(false, "", `SecRule ARGS:x "@streq 1" "id:1,phase:1,deny"`, nil)
		wDetectNoCtl := wafLoc(true, "detect", `SecRule ARGS:x "@streq 1" "id:1,phase:1,deny"`, nil)
		wBlockNoCtl := wafLoc(true, "block", `SecRule ARGS:x "@streq 1" "id:1,phase:1,deny"`, nil)
		if got := classifySingle(t, wOff, wDetectNoCtl); got.Class != AudienceDeltaUnchanged {
			t.Fatalf("WAF without ctl: disabled -> detect want unchanged, got %+v", got)
		}
		if got := classifySingle(t, wDetectNoCtl, wBlockNoCtl); got.Class != AudienceDeltaNarrowed {
			t.Fatalf("WAF without ctl: detect -> block want narrowed, got %+v", got)
		}
		if got := classifySingle(t, wBlockNoCtl, wDetectNoCtl); got.Class != AudienceDeltaWidened {
			t.Fatalf("WAF without ctl: block -> detect want widened, got %+v", got)
		}

		// With ctl:ruleEngine or directives_files: disabled -> detect is narrowed; detect <-> block is uncertain.
		wOffCtl := wafLoc(false, "", "", []string{"/etc/waf/rules.conf"})
		wDetectCtl := wafLoc(true, "detect", "", []string{"/etc/waf/rules.conf"})
		wBlockCtl := wafLoc(true, "block", "", []string{"/etc/waf/rules.conf"})
		if got := classifySingle(t, wOffCtl, wDetectCtl); got.Class != AudienceDeltaNarrowed {
			t.Fatalf("WAF with rule files: disabled -> detect want narrowed, got %+v", got)
		}
		if got := classifySingle(t, wDetectCtl, wBlockCtl); got.Class != AudienceDeltaUncertain {
			t.Fatalf("WAF with rule files: detect -> block want uncertain, got %+v", got)
		}
	})

	t.Run("normative host-dispatch expansion marks overlapping and first-declared blocks only", func(t *testing.T) {
		before := &config.Config{
			Servers: []config.ServerConfig{
				{
					Name:        "first-block",
					Listen:      ":8080",
					ServerNames: []string{" *.example.com. "},
					Locations: []config.LocationConfig{{
						RouteID: strPtrTest("first-route"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/"},
						Return:  200,
					}},
				},
				{
					Name:        "disjoint-block",
					Listen:      ":8080",
					ServerNames: []string{"unrelated.org"},
					Locations: []config.LocationConfig{{
						RouteID: strPtrTest("unrelated-route"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/"},
						Return:  200,
					}},
				},
			},
		}
		after, _ := before.Clone()
		after.Servers = append(after.Servers, config.ServerConfig{
			Name:        "corp-block",
			Listen:      ":8080",
			ServerNames: []string{"*.corp.example.com"},
			Locations: []config.LocationConfig{{
				RouteID: strPtrTest("corp-route"),
				Match:   config.MatchConfig{Type: "prefix", Path: "/"},
				Return:  200,
			}},
		})

		rep := s.BuildSharingPlanReport(before, before, after, after, true, nil, true)
		byID := make(map[string]SharingEntryDelta)
		for _, e := range rep.Entries {
			byID[e.Ref.RouteID] = e
		}
		if d, ok := byID["first-route"]; !ok || d.Class != AudienceDeltaUncertain {
			t.Fatalf("first-route (overlapping *.example.com) must be marked uncertain, got %+v", byID)
		}
		if d, ok := byID["corp-route"]; !ok || d.Class != AudienceDeltaUncertain {
			t.Fatalf("added corp-route must be marked uncertain, got %+v", byID)
		}
		if _, ok := byID["unrelated-route"]; ok {
			t.Fatalf("disjoint unrelated.org block must not be marked affected, got %+v", byID["unrelated-route"])
		}
	})
}

func TestPostconditionProducerAndEvaluator(t *testing.T) {
	baseCfg := &config.Config{
		Global: config.GlobalConfig{ConfigAuthority: "managed"},
		Servers: []config.ServerConfig{{
			Listen:      "127.0.0.1:8080",
			ServerNames: []string{"app.example.com"},
			Locations: []config.LocationConfig{
				{
					RouteID: strPtrTest("dash"),
					Match:   config.MatchConfig{Type: "prefix", Path: "/dash"},
					Return:  200,
					Sharing: &config.SharingConfig{Mode: "preview"},
				},
				{
					Match:  config.MatchConfig{Type: "prefix", Path: "/pos-only"},
					Return: 200,
				},
			},
		}},
	}
	candCfg, _ := baseCfg.Clone()
	candCfg.Servers[0].Locations[0].Auth = &config.AuthConfig{
		Allow: []string{"127.0.0.1/32"},
		JWT:   &config.JWTAuthConfig{JWKSURL: "https://idp.example/jwks", Algorithms: []string{"RS256"}},
	}
	candCfg.Servers[0].Locations[0].Sharing = &config.SharingConfig{
		Mode:             "restricted",
		ExternalEnforcer: strPtrTest("cloudflare-access:dash"),
	}
	// Mutate the position-only route too so both route_id and position refs are tested.
	candCfg.Servers[0].Locations[1].Deny = true

	var currentServing *config.Config
	currentServing, _ = baseCfg.Clone()

	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return currentServing.Clone() },
		LiveSnapshot: func() server.LiveSnapshot {
			c, _ := currentServing.Clone()
			return server.LiveSnapshot{
				Generation:      1,
				RawConfig:       c,
				EffectiveConfig: c,
			}
		},
	})

	t.Run("S17-1 disclosure gate withholds postcondition without config:raw on raw plans and plugin_set", func(t *testing.T) {
		repNoRaw := s.BuildSharingPlanReport(baseCfg, baseCfg, candCfg, candCfg, true, nil, false)
		if repNoRaw.Postcondition == nil || repNoRaw.Postcondition.Status != "unavailable" || repNoRaw.Postcondition.UnavailableReason != "config_raw_required" {
			t.Fatalf("expected unavailable postcondition for raw plan without config:raw, got %+v", repNoRaw.Postcondition)
		}

		repPluginNoRaw := s.BuildSharingPlanReport(baseCfg, baseCfg, candCfg, candCfg, false, []patchRequest{{Op: "plugin_set", PluginName: "authz"}}, false)
		if repPluginNoRaw.Postcondition == nil || repPluginNoRaw.Postcondition.Status != "unavailable" {
			t.Fatalf("expected unavailable postcondition for plugin_set without config:raw, got %+v", repPluginNoRaw.Postcondition)
		}

		repTypedReadable := s.BuildSharingPlanReport(baseCfg, baseCfg, candCfg, candCfg, false, []patchRequest{{Op: "location_set_sharing", RouteID: strPtrTest("dash")}}, false)
		if repTypedReadable.Postcondition == nil || repTypedReadable.Postcondition.Status != "available" {
			t.Fatalf("expected available postcondition for readable typed op, got %+v", repTypedReadable.Postcondition)
		}
	})

	t.Run("evaluates not_serving_yet, candidate_serving, access_matches, diverged, and unverifiable", func(t *testing.T) {
		rep := s.BuildSharingPlanReport(baseCfg, baseCfg, candCfg, candCfg, true, nil, true)
		post := rep.Postcondition
		if post == nil || post.Status != "available" || len(post.Entries) != 2 {
			t.Fatalf("unexpected postcondition: %+v", post)
		}

		evalReq := SharingPostconditionEvaluateRequest{
			PostconditionVersion:  post.PostconditionVersion,
			KeyID:                 post.KeyID,
			BaseConfigDigest:      post.BaseConfigDigest,
			CandidateConfigDigest: post.CandidateConfigDigest,
			Repeatable:            post.Repeatable,
			Entries:               post.Entries,
		}

		// 1. While baseCfg is still serving -> not_serving_yet.
		resBase := s.EvaluatePostcondition(evalReq, true)
		if resBase.Result != PostconditionResultNotServingYet || resBase.Candidate != "base_serving" {
			t.Fatalf("expected not_serving_yet while base is serving, got %+v", resBase)
		}

		// 2. When candCfg is serving -> candidate_serving.
		currentServing, _ = candCfg.Clone()
		resServing := s.EvaluatePostcondition(evalReq, true)
		if resServing.Result != PostconditionResultCandidateServing || resServing.Candidate != "serving" || resServing.Access != "matches" {
			t.Fatalf("expected candidate_serving when candidate serves, got %+v", resServing)
		}

		// 3. When a third revision is serving that keeps "dash" access facts identical -> access_matches for route_id-only postcondition,
		// and unverifiable when position-bound entry is included!
		thirdCfg, _ := candCfg.Clone()
		thirdCfg.Global.LogLevel = "debug"
		currentServing = thirdCfg
		resThirdWithPos := s.EvaluatePostcondition(evalReq, true)
		if resThirdWithPos.Result != PostconditionResultUnverifiable || resThirdWithPos.Candidate != "other_revision" {
			t.Fatalf("expected unverifiable for position ref under other_revision, got %+v", resThirdWithPos)
		}

		routeIDOnlyReq := evalReq
		routeIDOnlyReq.Entries = []SharingPostconditionItem{post.Entries[0]}
		resThirdRouteID := s.EvaluatePostcondition(routeIDOnlyReq, true)
		if resThirdRouteID.Result != PostconditionResultAccessMatches || resThirdRouteID.Candidate != "other_revision" || resThirdRouteID.Access != "matches" {
			t.Fatalf("expected access_matches for route_id ref under other_revision, got %+v", resThirdRouteID)
		}

		// 4. When access facts on "dash" diverge -> diverged.
		divCfg, _ := candCfg.Clone()
		divCfg.Servers[0].Locations[0].Auth = nil
		currentServing = divCfg
		resDiverged := s.EvaluatePostcondition(routeIDOnlyReq, true)
		if resDiverged.Result != PostconditionResultDiverged || resDiverged.Access != "differs" {
			t.Fatalf("expected diverged when auth is removed, got %+v", resDiverged)
		}

		// 5. After a simulated restart (different key_id) -> unverifiable.
		wrongKeyReq := routeIDOnlyReq
		wrongKeyReq.KeyID = "pk1-0000000000000000"
		resRestart := s.EvaluatePostcondition(wrongKeyReq, true)
		if resRestart.Result != PostconditionResultUnverifiable || resRestart.Candidate != "unverifiable" {
			t.Fatalf("expected unverifiable on key_id mismatch, got %+v", resRestart)
		}
	})

	t.Run("RR12 omits file-dependent expected facts and never treats them as differing", func(t *testing.T) {
		rawBefore := &config.Config{
			Servers: []config.ServerConfig{{
				Listen: "${file:/etc/jul/listen}",
				Locations: []config.LocationConfig{{
					RouteID: strPtrTest("file-bound"),
					Match:   config.MatchConfig{Type: "prefix", Path: "/"},
					Return:  200,
					Sharing: &config.SharingConfig{Mode: "private_network"},
				}},
			}},
		}
		effBefore := &config.Config{
			Servers: []config.ServerConfig{{
				Listen: "10.0.0.1:8080",
				Locations: []config.LocationConfig{{
					RouteID: strPtrTest("file-bound"),
					Match:   config.MatchConfig{Type: "prefix", Path: "/"},
					Return:  200,
					Sharing: &config.SharingConfig{Mode: "private_network"},
				}},
			}},
		}
		rawAfter, _ := rawBefore.Clone()
		rawAfter.Servers[0].Locations[0].Sharing = &config.SharingConfig{Mode: "restricted"}
		effAfter, _ := effBefore.Clone()
		effAfter.Servers[0].Locations[0].Sharing = &config.SharingConfig{Mode: "restricted"}

		rep := s.BuildSharingPlanReport(rawBefore, effBefore, rawAfter, effAfter, true, nil, true)
		item := rep.Postcondition.Entries[0]
		if item.Expected.BindScope != "" || !strings.Contains(strings.Join(item.Expected.OmittedFileFacts, ","), "bind_scope") {
			t.Fatalf("RR12: expected bind_scope to be omitted when listen uses ${file:...}, got %+v", item.Expected)
		}
		if !strings.Contains(strings.Join(item.ExternalInputs, ","), ExternalInputFileContents) {
			t.Fatalf("RR12: expected file_contents in external_inputs, got %+v", item.ExternalInputs)
		}
	})
}

func TestSharingViewProjectionAndFullValueRead(t *testing.T) {
	longLabel := strings.Repeat(" audience-成员 ", 30)  // > 256 bytes with multibyte UTF-8
	longPath := "/" + strings.Repeat("segment/", 150) // > 1024 bytes

	rawCfg := &config.Config{
		Global: config.GlobalConfig{ConfigAuthority: "managed"},
		Cache:  config.CacheConfig{Enabled: true},
		Servers: []config.ServerConfig{
			{
				Name:        "primary",
				Listen:      "0.0.0.0:8080",
				ServerNames: []string{"app.example.com", "*.wild.example.com", "${env:SECRET_HOST}"},
				Locations: []config.LocationConfig{
					{
						RouteID:   strPtrTest("dash"),
						Match:     config.MatchConfig{Type: "prefix", Path: longPath},
						ProxyPass: "http://backend",
						Cache:     true,
						Sharing: &config.SharingConfig{
							Mode:             "restricted",
							AudienceLabel:    strPtrTest(longLabel),
							ExternalEnforcer: strPtrTest("cloudflare-access:dash"),
						},
					},
					{
						RouteID:   strPtrTest("${env:SECRET_ROUTE_ID}"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/ref"},
						ProxyPass: "http://backend",
					},
				},
			},
		},
		Upstreams: []config.UpstreamConfig{{
			Name:    "backend",
			Servers: []config.UpstreamServer{{Address: "127.0.0.1:9000", Weight: 1}},
		}},
	}
	effCfg, _ := rawCfg.Clone()
	effCfg.Servers[0].ServerNames = []string{"app.example.com", "*.wild.example.com", "resolved-secret-host.internal"}
	effCfg.Servers[0].Locations[1].RouteID = strPtrTest("resolved-secret-route-id")

	savedCfg, _ := rawCfg.Clone()
	savedCfg.Servers[0].Listen = "${env:SAVED_LISTEN}"
	savedCfg.Servers[0].Locations[0].Sharing = &config.SharingConfig{Mode: "${env:SAVED_MODE}"}
	savedRawBytes, _ := config.Marshal(savedCfg)

	var gen uint64 = 1
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig:    func() (*config.Config, error) { return effCfg.Clone() },
		ReadConfigRaw: func() ([]byte, error) { return savedRawBytes, nil },
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{
				Generation:      gen,
				RawConfig:       rawCfg,
				EffectiveConfig: effCfg,
			}
		},
		Authority: func() ConfigAuthorityStatus {
			return ConfigAuthorityStatus{
				Mode:        "managed",
				Source:      "explicit",
				ConfigState: "managed_desired_ahead",
				DiskVersion: "saved-v2",
			}
		},
	})

	t.Run("sharing view bounds display strings, withholds resolved secrets, and separates serving vs saved layers", func(t *testing.T) {
		page1, err := s.BuildSharingViewPage("", 1)
		if err != nil {
			t.Fatalf("BuildSharingViewPage(1): %v", err)
		}
		if page1.SharingViewVersion != 1 || page1.TotalEntries != 2 || len(page1.Entries) != 1 || page1.NextCursor == "" {
			t.Fatalf("unexpected page1 metadata: %+v", page1)
		}
		rawJSON, _ := json.Marshal(page1)
		if strings.Contains(string(rawJSON), "resolved-secret-host.internal") || strings.Contains(string(rawJSON), "resolved-secret-route-id") {
			t.Fatalf("sharing view leaked resolved secret reference value: %s", string(rawJSON))
		}

		e0 := page1.Entries[0]
		if e0.Serving == nil || e0.Saved == nil {
			t.Fatalf("expected both serving and saved layers on entry 0, got %+v", e0)
		}
		if !e0.Serving.Path.Truncated || e0.Serving.Path.FullBytes != len(longPath) || len(e0.Serving.Path.Text) > BoundedPathMaxBytes {
			t.Fatalf("expected truncated path BoundedText, got %+v", e0.Serving.Path)
		}
		if e0.Serving.DeclaredIntent.AudienceLabel == nil || !e0.Serving.DeclaredIntent.AudienceLabel.Truncated {
			t.Fatalf("expected truncated audience_label BoundedText, got %+v", e0.Serving.DeclaredIntent)
		}
		if e0.Serving.Selector.TooLarge {
			t.Fatalf("expected entry 0 selector to fit within 4 KiB cap, got %+v", e0.Serving.Selector)
		}
		// In the saved layer, listen and sharing.mode are written as ${env:...} references, so they report unknown!
		if e0.Saved.Listener.BindScope != config.BindScopeUnknown || e0.Saved.DeclaredIntent.State != config.SharingModeUnknown || e0.Saved.IntentFindingsComplete {
			t.Fatalf("expected saved layer to report unknown for reference-backed facts and intent_findings_complete=false: %+v", e0.Saved)
		}

		// Fetch page 2 using NextCursor.
		page2, err := s.BuildSharingViewPage(page1.NextCursor, 1)
		if err != nil {
			t.Fatalf("BuildSharingViewPage(2): %v", err)
		}
		if len(page2.Entries) != 1 || page2.NextCursor != "" {
			t.Fatalf("unexpected page2: %+v", page2)
		}
		e1 := page2.Entries[0]
		if e1.Serving.Identity != "revision_bound" || e1.Serving.RouteID == nil || e1.Serving.RouteID.FromReference == nil {
			t.Fatalf("expected reference-backed route_id to be revision_bound with from_reference: %+v", e1.Serving)
		}

		// Bumping the snapshot generation invalidates the cursor (SH-13 -> 409).
		gen = 2
		if _, err := s.BuildSharingViewPage(page1.NextCursor, 1); err == nil || !strings.Contains(err.Error(), "the configuration changed") {
			t.Fatalf("expected stale cursor error after generation bump, got: %v", err)
		}
	})

	t.Run("full-value chunked read returns raw strings across chunks and refuses stale snapshot", func(t *testing.T) {
		page, err := s.BuildSharingViewPage("", 10)
		if err != nil {
			t.Fatalf("BuildSharingViewPage: %v", err)
		}
		var reassembled strings.Builder
		offset := 0
		for {
			chunk, err := s.ReadSharingFullValue(page.Snapshot, "serving", "dash", "path", offset, 300)
			if err != nil {
				t.Fatalf("ReadSharingFullValue at %d: %v", offset, err)
			}
			reassembled.WriteString(chunk.Chunk)
			if chunk.NextOffset == nil {
				break
			}
			offset = *chunk.NextOffset
		}
		if reassembled.String() != longPath {
			t.Fatalf("reassembled path length = %d, want %d", reassembled.Len(), len(longPath))
		}

		// Reading route_id_ref on entry 0:1 returns the raw reference text "${env:SECRET_ROUTE_ID}", never the resolved value!
		refVal, err := s.ReadSharingFullValue(page.Snapshot, "serving", "0:1", "route_id_ref", 0, 1024)
		if err != nil {
			t.Fatalf("ReadSharingFullValue route_id_ref: %v", err)
		}
		if refVal.Chunk != "${env:SECRET_ROUTE_ID}" {
			t.Fatalf("route_id_ref = %q, want raw reference text", refVal.Chunk)
		}

		// Stale snapshot token is refused.
		if _, err := s.ReadSharingFullValue("stale-token", "serving", "dash", "path", 0, 1024); err == nil {
			t.Fatal("expected stale snapshot token to be refused")
		}
	})

	t.Run("HTTP endpoints enforce status:read AND config:read", func(t *testing.T) {
		policy, err := rbac.Build(true, "admin", map[string][]string{
			"status_only": {string(rbac.StatusRead)},
			"inspector":   {string(rbac.StatusRead), string(rbac.ConfigRead)},
		}, []rbac.PrincipalDef{
			{Name: "admin-user", Role: rbac.RoleAdmin, Token: "tok-admin-12345"},
			{Name: "status-user", Role: "status_only", Token: "tok-status-only-12345"},
			{Name: "inspector-user", Role: "inspector", Token: "tok-inspector-12345"},
		}, "", time.Now())
		if err != nil {
			t.Fatalf("rbac.Build: %v", err)
		}
		s.UpdatePolicy(policy)
		h := s.routes()

		// status:read without config:read -> 403 Forbidden.
		for _, ep := range []struct {
			method string
			path   string
		}{
			{http.MethodGet, "/api/sharing"},
			{http.MethodGet, "/api/sharing/value?snapshot=x&ref=dash&field=path"},
			{http.MethodPost, "/api/sharing/postcondition/evaluate"},
		} {
			req := httptest.NewRequest(ep.method, ep.path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer tok-status-only-12345")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("%s %s with status:read only returned %d, want 403", ep.method, ep.path, rr.Code)
			}
		}

		// status:read + config:read -> 200 OK on /api/sharing.
		reqOK := httptest.NewRequest(http.MethodGet, "/api/sharing?limit=1", nil)
		reqOK.Header.Set("Authorization", "Bearer tok-inspector-12345")
		rrOK := httptest.NewRecorder()
		h.ServeHTTP(rrOK, reqOK)
		if rrOK.Code != http.StatusOK {
			t.Fatalf("GET /api/sharing with inspector returned %d: %s", rrOK.Code, rrOK.Body.String())
		}
		var page SharingViewResponse
		if err := json.Unmarshal(rrOK.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode SharingViewResponse: %v", err)
		}

		// Full-value HTTP endpoint returns 200 with valid snapshot and 409 with stale snapshot.
		reqVal := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sharing/value?snapshot=%s&ref=dash&field=audience_label&offset=0&limit=64", page.Snapshot), nil)
		reqVal.Header.Set("Authorization", "Bearer tok-inspector-12345")
		rrVal := httptest.NewRecorder()
		h.ServeHTTP(rrVal, reqVal)
		if rrVal.Code != http.StatusOK {
			t.Fatalf("GET /api/sharing/value returned %d: %s", rrVal.Code, rrVal.Body.String())
		}

		// HTTP POST /api/sharing/postcondition/evaluate endpoint.
		postBody, _ := json.Marshal(SharingPostconditionEvaluateRequest{
			PostconditionVersion: 1,
			KeyID:                "pk1-test",
			Entries:              nil,
		})
		reqPost := httptest.NewRequest(http.MethodPost, "/api/sharing/postcondition/evaluate", bytes.NewReader(postBody))
		reqPost.Header.Set("Authorization", "Bearer tok-inspector-12345")
		rrPost := httptest.NewRecorder()
		h.ServeHTTP(rrPost, reqPost)
		if rrPost.Code != http.StatusOK {
			t.Fatalf("POST /api/sharing/postcondition/evaluate returned %d: %s", rrPost.Code, rrPost.Body.String())
		}

		// Method not allowed and bad JSON on /api/sharing/postcondition/evaluate and /api/sharing.
		for _, badReq := range []*http.Request{
			httptest.NewRequest(http.MethodPost, "/api/sharing", nil),
			httptest.NewRequest(http.MethodPost, "/api/sharing/value", nil),
			httptest.NewRequest(http.MethodGet, "/api/sharing/postcondition/evaluate", nil),
		} {
			badReq.Header.Set("Authorization", "Bearer tok-inspector-12345")
			rrBad := httptest.NewRecorder()
			h.ServeHTTP(rrBad, badReq)
			if rrBad.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s returned %d, want 405", badReq.Method, badReq.URL.Path, rrBad.Code)
			}
		}
		reqBadJSON := httptest.NewRequest(http.MethodPost, "/api/sharing/postcondition/evaluate", strings.NewReader("not-json"))
		reqBadJSON.Header.Set("Authorization", "Bearer tok-inspector-12345")
		rrBadJSON := httptest.NewRecorder()
		h.ServeHTTP(rrBadJSON, reqBadJSON)
		if rrBadJSON.Code != http.StatusBadRequest {
			t.Fatalf("bad JSON returned %d, want 400", rrBadJSON.Code)
		}
	})
}

func TestSharingAdditionalCoverageBranches(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})

	t.Run("CIDR IPv4/IPv6 single hosts, merging intervals, and invalid CIDRs", func(t *testing.T) {
		// Single IPv4 and IPv6 addresses, adjacent/overlapping ranges, and IPv6 bind changes.
		cls, _ := classifyCIDRChange(
			[]string{"10.0.0.1", "10.0.0.2", "10.0.0.0/30", "2001:db8::/32"},
			[]string{"10.0.0.3", "2001:db8:1::1"},
			[]string{"10.0.0.1"},
			nil,
		)
		if cls != AudienceDeltaNarrowed {
			t.Fatalf("expected narrowed for IPv4+IPv6 interval subset, got %s", cls)
		}
		if got, _ := classifyCIDRChange([]string{"not-an-ip"}, nil, []string{"10.0.0.0/8"}, nil); got != AudienceDeltaUncertain {
			t.Fatalf("expected uncertain on invalid CIDR, got %s", got)
		}

		// IPv6 bind widening/narrowing and secret-ref bind.
		if got, _ := classifyBindChange("[::1]:8443", "[::]:8443"); got != AudienceDeltaWidened {
			t.Fatalf("expected IPv6 bind widened, got %s", got)
		}
		if got, _ := classifyBindChange("[::]:8443", "[::1]:8443"); got != AudienceDeltaNarrowed {
			t.Fatalf("expected IPv6 bind narrowed, got %s", got)
		}
		if got, _ := classifyBindChange("${env:B1}", ":8080"); got != AudienceDeltaUncertain {
			t.Fatalf("expected secret-ref bind uncertain, got %s", got)
		}
		if got, _ := classifyBindChange(":8080", ":8081"); got != AudienceDeltaUncertain {
			t.Fatalf("expected port change uncertain, got %s", got)
		}
	})

	t.Run("plugins, upstreams, backend_tls, rewrites, forward_auth, CORS, and file-backed inputs", func(t *testing.T) {
		before := &config.Config{
			Plugins: map[string]config.PluginConfig{
				"authz": {Path: "/etc/plugins/authz.wasm", Type: "middleware", Config: map[string]string{"k": "v1"}},
			},
			Upstreams: []config.UpstreamConfig{{
				Name:       "up1",
				Strategy:   "round_robin",
				Servers:    []config.UpstreamServer{{Address: "127.0.0.1:9001", Weight: 1}},
				BackendTLS: &config.BackendTLSConfig{ServerName: "a.internal"},
				Discovery:  &config.DiscoveryConfig{Type: "dns", Target: "a.internal"},
			}},
			Servers: []config.ServerConfig{{
				Name:          "srv-a",
				Listen:        "127.0.0.1:8443",
				ServerNames:   []string{"a.example.com", "b.example.com"},
				RedirectHTTPS: 301,
				ProxyProtocol: "in",
				ClientAddress: &config.ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}},
				Plugins:       []string{"authz"},
				TLS: &config.TLSConfig{
					Enabled:    true,
					Cert:       "c.pem",
					Key:        "k.pem",
					MinVersion: "1.2",
					ClientAuth: &config.ClientAuthConfig{
						Mode:      "request",
						CAFile:    "/etc/ca.pem",
						CRLFile:   "/etc/crl.pem",
						VerifySAN: []string{"client.example"},
					},
				},
				Locations: []config.LocationConfig{
					{
						RouteID:           strPtrTest("r-main"),
						Match:             config.MatchConfig{Type: "prefix", Path: "/api"},
						ProxyPass:         "http://up1",
						RequireClientCert: true,
						Rewrites:          []config.RewriteConfig{{Pattern: "^/api/(.*)$", Replacement: "/v1/$1", Flag: "last"}},
						CORS:              &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example"}, AllowCredentials: true},
						Auth: &config.AuthConfig{
							ForwardAuth: &config.ForwardAuthConfig{URL: "http://auth.internal/check", AuthResponseHeaders: []string{"X-User"}},
						},
						BackendTLS: &config.BackendTLSConfig{ServerName: "loc.internal"},
					},
					{
						Match:  config.MatchConfig{Type: "prefix", Path: "/removed"},
						Return: 200,
					},
				},
			}},
		}

		after, _ := before.Clone()
		// Mutate plugin config, upstream server, redirect_https, client_auth CA, require_client_cert, rewrites, CORS wildcard, forward_auth URL, and replace /removed with /added.
		after.Plugins["authz"] = config.PluginConfig{Path: "/etc/plugins/authz.wasm", Type: "middleware", Config: map[string]string{"k": "v2"}}
		after.Upstreams[0].Servers[0].Address = "127.0.0.1:9002"
		after.Servers[0].RedirectHTTPS = 308
		after.Servers[0].TLS.MinVersion = "1.3"
		after.Servers[0].TLS.ClientAuth.CAFile = "/etc/ca2.pem"
		after.Servers[0].Locations[0].RequireClientCert = false
		after.Servers[0].Locations[0].Rewrites[0].Replacement = "/v2/$1"
		after.Servers[0].Locations[0].CORS.AllowedOrigins = []string{"*"}
		after.Servers[0].Locations[0].Auth.ForwardAuth.URL = "http://auth.internal/v2"
		after.Servers[0].Locations[1] = config.LocationConfig{
			Match:  config.MatchConfig{Type: "prefix", Path: "/added"},
			Return: 201,
		}

		rep := s.BuildSharingPlanReport(before, before, after, after, false, []patchRequest{{Op: "plugin_set", PluginName: "authz"}}, false)
		if !rep.RequiresAcknowledgement || rep.FileBackedEntryCount == 0 {
			t.Fatalf("expected acknowledgement and file-backed entries, got %+v", rep)
		}

		// Evaluate postcondition when no entries are affected (none_expected):
		// 1) when candidate is serving -> candidate_serving
		// 2) when another revision is serving -> other_revision
		sEval := newTestServer(t, config.AdminConfig{}, Deps{
			LiveSnapshot: func() server.LiveSnapshot {
				return server.LiveSnapshot{Generation: 1, RawConfig: after, EffectiveConfig: after}
			},
		})
		keyID, _ := sEval.ensurePostconditionKey()
		bDig, _ := sEval.keyedConfigDigest(before)
		aDig, _ := sEval.keyedConfigDigest(after)

		resNoneServing := sEval.EvaluatePostcondition(SharingPostconditionEvaluateRequest{
			PostconditionVersion:  1,
			KeyID:                 keyID,
			BaseConfigDigest:      bDig,
			CandidateConfigDigest: aDig,
			Repeatable:            true,
			Entries:               nil,
		}, true)
		if resNoneServing.Result != PostconditionResultCandidateServing || resNoneServing.Access != "none_expected" {
			t.Fatalf("expected candidate_serving with none_expected, got %+v", resNoneServing)
		}

		resNoneOther := sEval.EvaluatePostcondition(SharingPostconditionEvaluateRequest{
			PostconditionVersion:  1,
			KeyID:                 keyID,
			BaseConfigDigest:      bDig,
			CandidateConfigDigest: "hmac-sha256:0000",
			Repeatable:            false,
			Entries:               nil,
		}, true)
		if resNoneOther.Result != PostconditionResultOtherRevision {
			t.Fatalf("expected other_revision with none_expected, got %+v", resNoneOther)
		}

		// Absent entry expectation when still present -> differs ("still_present") vs when absent -> matches.
		resStillPresent := sEval.EvaluatePostcondition(SharingPostconditionEvaluateRequest{
			PostconditionVersion:  1,
			KeyID:                 keyID,
			BaseConfigDigest:      bDig,
			CandidateConfigDigest: aDig,
			Entries: []SharingPostconditionItem{{
				Ref:    SharingEntryRef{Kind: "route_id", RouteID: "r-main"},
				Expect: "absent",
			}},
		}, true)
		if resStillPresent.Result != PostconditionResultDiverged || resStillPresent.Entries[0].DifferingFields[0] != "still_present" {
			t.Fatalf("expected still_present divergence, got %+v", resStillPresent)
		}

		resAbsentMatches := sEval.EvaluatePostcondition(SharingPostconditionEvaluateRequest{
			PostconditionVersion:  1,
			KeyID:                 keyID,
			BaseConfigDigest:      bDig,
			CandidateConfigDigest: aDig,
			Entries: []SharingPostconditionItem{{
				Ref:    SharingEntryRef{Kind: "route_id", RouteID: "gone-route"},
				Expect: "absent",
			}},
		}, true)
		if resAbsentMatches.Result != PostconditionResultCandidateServing || resAbsentMatches.Access != "matches" {
			t.Fatalf("expected absent route_id to match when absent, got %+v", resAbsentMatches)
		}
	})

	t.Run("sharing view selector too_large, backend groups, warnings, and full-value block/entry fields", func(t *testing.T) {
		manyNames := make([]string, 105)
		for i := range manyNames {
			manyNames[i] = fmt.Sprintf("host-%d.example.com", i)
		}
		cfg := &config.Config{
			Cache:     config.CacheConfig{Enabled: true},
			RateLimit: config.RateLimitConfig{Enabled: true, Rate: 10, Burst: 10, Key: "ip"},
			WAF:       config.WAFConfig{Enabled: true, Mode: "detect"},
			Servers: []config.ServerConfig{{
				Name:          "big-block",
				Listen:        "0.0.0.0:443",
				ServerNames:   manyNames,
				RedirectHTTPS: 301,
				ClientAddress: &config.ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}},
				TLS: &config.TLSConfig{
					Enabled:    true,
					Cert:       "c.pem",
					Key:        "k.pem",
					ClientAuth: &config.ClientAuthConfig{Mode: "request", CAFile: "/etc/ca.pem"},
				},
				HTTP3: &config.HTTP3Config{Enabled: true},
				Locations: []config.LocationConfig{
					{
						RouteID: strPtrTest("jwt-route"),
						Match:   config.MatchConfig{Type: "regex", Path: "^/v1/.*"},
						GRPCTranscode: &config.GRPCTranscodeConfig{
							Target:        "127.0.0.1:9090",
							UseReflection: true,
						},
						Cache: true,
						CORS:  &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"*"}},
						Auth: &config.AuthConfig{
							Allow: []string{"10.0.0.0/8"},
							Deny:  []string{"10.0.0.1/32"},
							JWT:   &config.JWTAuthConfig{JWKSURL: "https://idp.example/jwks"},
						},
						Sharing: &config.SharingConfig{
							Mode:             "restricted",
							AudienceLabel:    strPtrTest("partner"),
							ExternalEnforcer: strPtrTest("cf:partner"),
						},
					},
					{
						RouteID: strPtrTest("open-peer"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/open"},
						GRPCTranscode: &config.GRPCTranscodeConfig{
							Target:        "127.0.0.1:9090",
							UseReflection: true,
						},
					},
				},
			}},
		}

		// Saved config removes "open-peer" and adds "saved-only" so both PendingRemoval and saved-only entries are exercised.
		savedCfg, _ := cfg.Clone()
		savedCfg.Servers[0].Locations = []config.LocationConfig{
			cfg.Servers[0].Locations[0],
			{
				RouteID: strPtrTest("saved-only"),
				Match:   config.MatchConfig{Type: "prefix", Path: "/new"},
				Root:    "/var/www",
			},
		}
		savedBytes, _ := config.Marshal(savedCfg)

		sView := newTestServer(t, config.AdminConfig{}, Deps{
			ReadConfigRaw: func() ([]byte, error) { return savedBytes, nil },
			LiveSnapshot: func() server.LiveSnapshot {
				return server.LiveSnapshot{Generation: 1, RawConfig: cfg, EffectiveConfig: cfg}
			},
		})

		page, err := sView.BuildSharingViewPage("", 50)
		if err != nil {
			t.Fatalf("BuildSharingViewPage: %v", err)
		}
		if len(page.Blocks) == 0 || !page.Blocks[0].Truncated {
			t.Fatalf("expected truncated block names (>100), got %+v", page.Blocks)
		}
		if len(page.BackendGroups) == 0 {
			t.Fatalf("expected shared backend group for 127.0.0.1:9090, got %+v", page.BackendGroups)
		}
		if !page.Entries[0].Serving.Selector.TooLarge {
			t.Fatalf("expected selector TooLarge when block names are truncated, got %+v", page.Entries[0].Serving.Selector)
		}

		for _, tc := range []struct {
			layer string
			ref   string
			field string
		}{
			{"serving", "0", "listen"},
			{"serving", "serving:block:0", "display_label"},
			{"serving", "0", "server_names"},
			{"serving", "jwt-route", "path"},
			{"serving", "jwt-route", "audience_label"},
			{"serving", "jwt-route", "external_enforcer"},
			{"serving", "jwt-route", "selector"},
			{"saved", "saved-only", "path"},
		} {
			val, err := sView.ReadSharingFullValue(page.Snapshot, tc.layer, tc.ref, tc.field, 0, 0)
			if err != nil || val.Chunk == "" {
				t.Fatalf("ReadSharingFullValue(%q, %q, %q) = %+v, err=%v", tc.layer, tc.ref, tc.field, val, err)
			}
		}

		// Error cases for ReadSharingFullValue and handleSharingValue.
		for _, bad := range []struct {
			layer string
			ref   string
			field string
		}{
			{"invalid-layer", "jwt-route", "path"},
			{"serving", "bad-block", "listen"},
			{"serving", "missing-entry", "path"},
			{"serving", "jwt-route", "unknown_field"},
		} {
			if _, err := sView.ReadSharingFullValue(page.Snapshot, bad.layer, bad.ref, bad.field, 0, 0); err == nil {
				t.Fatalf("expected error for %+v", bad)
			}
		}
		rrBadVal := httptest.NewRecorder()
		sView.handleSharingValue(rrBadVal, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sharing/value?snapshot=%s&ref=missing&field=path", page.Snapshot), nil))
		if rrBadVal.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 on missing ref, got %d", rrBadVal.Code)
		}

		// Test SharingRouteIDView JSON round-trip.
		var ridLit, ridRef SharingRouteIDView
		if err := json.Unmarshal([]byte(`"my-route"`), &ridLit); err != nil || ridLit.Literal != "my-route" {
			t.Fatalf("Unmarshal literal route_id: %v (%+v)", err, ridLit)
		}
		if err := json.Unmarshal([]byte(`{"from_reference":{"text":"${env:X}","truncated":false,"full_bytes":8}}`), &ridRef); err != nil || ridRef.FromReference == nil {
			t.Fatalf("Unmarshal reference route_id: %v (%+v)", err, ridRef)
		}
	})

	t.Run("remaining classifier, postcondition, and sharing view edge branches", func(t *testing.T) {
		// 1. symmetricNormalizedHostDiff catch-all vs named, block reorder, and correlation by route_id.
		if diff := symmetricNormalizedHostDiff(nil, []string{"a.example"}); len(diff) == 0 {
			t.Fatal("expected non-empty diff between catch-all and named block")
		}
		reorderBefore := &config.Config{
			Servers: []config.ServerConfig{
				{
					Name:        "b1",
					Listen:      ":8080",
					ServerNames: []string{"a.example"},
					Locations:   []config.LocationConfig{{RouteID: strPtrTest("r1"), Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
				},
				{
					Name:        "b2",
					Listen:      ":8080",
					ServerNames: []string{"b.example"},
					Locations:   []config.LocationConfig{{RouteID: strPtrTest("r2"), Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
				},
			},
		}
		reorderAfter := &config.Config{
			Servers: []config.ServerConfig{
				reorderBefore.Servers[1],
				reorderBefore.Servers[0],
			},
		}
		ab, aa := computeHostDispatchAffectedBlocks(reorderBefore, reorderAfter)
		if !ab[0] || !ab[1] || !aa[0] || !aa[1] {
			t.Fatalf("expected reordered blocks on same listener to be marked affected: ab=%v aa=%v", ab, aa)
		}

		// Correlate server block by shared literal route_id when listen and server_names both change.
		corrBefore := &config.Config{
			Servers: []config.ServerConfig{{
				Listen:      "127.0.0.1:8080",
				ServerNames: []string{"old.example"},
				Locations:   []config.LocationConfig{{RouteID: strPtrTest("shared-rid"), Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
			}},
		}
		corrAfter := &config.Config{
			Servers: []config.ServerConfig{{
				Listen:      "127.0.0.1:8081",
				ServerNames: []string{"new.example"},
				Locations:   []config.LocationConfig{{RouteID: strPtrTest("shared-rid"), Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
			}},
		}
		if m := correlateServerBlocks(corrBefore.Servers, corrAfter.Servers); m[0] != 0 {
			t.Fatalf("expected block correlation by route_id, got %v", m)
		}

		// 2. classifyCredentialChange, classifyCORSChange, classifyWAFChange, classifyListenerAndCertChanges.
		if cls, _ := classifyCredentialChange(
			&config.AuthConfig{Basic: &config.BasicAuthConfig{File: "/a"}},
			&config.AuthConfig{Basic: &config.BasicAuthConfig{File: "/b"}},
		); cls != AudienceDeltaUncertain {
			t.Fatalf("expected basic file change uncertain, got %s", cls)
		}
		if cls, _ := classifyCredentialChange(
			&config.AuthConfig{JWT: &config.JWTAuthConfig{JWKSURL: "https://a", Issuer: "i1"}},
			&config.AuthConfig{JWT: &config.JWTAuthConfig{JWKSURL: "https://a", Issuer: "i2"}},
		); cls != AudienceDeltaUncertain {
			t.Fatalf("expected jwt issuer change uncertain, got %s", cls)
		}
		if cls, _ := classifyCredentialChange(
			&config.AuthConfig{Basic: &config.BasicAuthConfig{File: "/a"}},
			&config.AuthConfig{Basic: &config.BasicAuthConfig{File: "/a"}},
		); cls != AudienceDeltaUnchanged {
			t.Fatalf("expected identical basic auth unchanged, got %s", cls)
		}

		for _, tc := range []struct {
			b, a *config.CORSConfig
			want string
		}{
			{nil, nil, AudienceDeltaUnchanged},
			{&config.CORSConfig{Enabled: true, AllowedOrigins: []string{"${env:ORIG}"}}, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a"}}, AudienceDeltaUncertain},
			{&config.CORSConfig{Enabled: true, AllowedMethods: []string{"GET"}}, &config.CORSConfig{Enabled: true, AllowedMethods: []string{"GET", "POST"}}, AudienceDeltaUncertain},
			{&config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a"}}, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://b"}}, AudienceDeltaUncertain},
			{&config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a"}}, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a", "https://b"}}, AudienceDeltaWidened},
			{&config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a"}, AllowCredentials: true}, &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a"}, AllowCredentials: false}, AudienceDeltaNarrowed},
		} {
			if got, _ := classifyCORSChange(tc.b, tc.a); got != tc.want {
				t.Fatalf("classifyCORSChange(%+v, %+v) = %s, want %s", tc.b, tc.a, got, tc.want)
			}
		}

		rawCtlCfg := &config.Config{WAF: config.WAFConfig{Enabled: true, Mode: "detect", DirectivesFiles: []string{"/etc/waf.conf"}}}
		rawOffCfg := &config.Config{WAF: config.WAFConfig{Enabled: false, DirectivesFiles: []string{"/etc/waf.conf"}}}
		rawBlkCfg := &config.Config{WAF: config.WAFConfig{Enabled: true, Mode: "block", DirectivesFiles: []string{"/etc/waf.conf"}}}
		if got, _ := classifyWAFChange(rawCtlCfg, rawOffCfg, nil, nil); got != AudienceDeltaWidened {
			t.Fatalf("WAF ctl detect -> disabled = %s, want widened", got)
		}
		if got, _ := classifyWAFChange(rawOffCfg, rawBlkCfg, nil, nil); got != AudienceDeltaNarrowed {
			t.Fatalf("WAF ctl disabled -> block = %s, want narrowed", got)
		}
		if got, _ := classifyWAFChange(rawBlkCfg, rawOffCfg, nil, nil); got != AudienceDeltaWidened {
			t.Fatalf("WAF ctl block -> disabled = %s, want widened", got)
		}
		rawRefWAF := &config.Config{WAF: config.WAFConfig{Enabled: true, Mode: "${env:WAF_MODE}"}}
		if got, _ := classifyWAFChange(rawRefWAF, rawBlkCfg, nil, nil); got != AudienceDeltaUncertain {
			t.Fatalf("WAF secret ref = %s, want uncertain", got)
		}

		// 3. upstreamsAccessEqual and hasFileSecretRefInEntry branches.
		uBase := config.UpstreamConfig{
			Name:      "u",
			Strategy:  "round_robin",
			Servers:   []config.UpstreamServer{{Address: "127.0.0.1:8080", Weight: 1}},
			Discovery: &config.DiscoveryConfig{Type: "dns", Target: "a.internal"},
		}
		if !upstreamsAccessEqual(uBase, uBase) {
			t.Fatal("expected identical upstreams to be equal")
		}
		uDiffDisc := uBase
		uDiffDisc.Discovery = &config.DiscoveryConfig{Type: "dns", Target: "b.internal"}
		if upstreamsAccessEqual(uBase, uDiffDisc) {
			t.Fatal("expected different discovery target to be unequal")
		}
		uNilDisc := uBase
		uNilDisc.Discovery = nil
		if upstreamsAccessEqual(uBase, uNilDisc) {
			t.Fatal("expected nil vs non-nil discovery to be unequal")
		}

		for _, loc := range []config.LocationConfig{
			{Match: config.MatchConfig{Path: "${file:/p}"}},
			{Sharing: &config.SharingConfig{Mode: "${file:/m}"}},
			{Auth: &config.AuthConfig{Allow: []string{"${file:/c}"}}},
			{Auth: &config.AuthConfig{Basic: &config.BasicAuthConfig{File: "${file:/b}"}}},
			{Auth: &config.AuthConfig{JWT: &config.JWTAuthConfig{JWKSURL: "${file:/j}"}}},
			{Auth: &config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{URL: "${file:/f}"}}},
			{WAF: &config.WAFConfig{Enabled: true, Mode: "${file:/w}"}},
			{WAF: &config.WAFConfig{Enabled: true, DirectivesFiles: []string{"${file:/d}"}}},
		} {
			if !hasFileSecretRefInEntry(&config.Config{}, config.ServerConfig{}, loc) {
				t.Fatalf("expected hasFileSecretRefInEntry=true for %+v", loc)
			}
		}

		// 4. Postcondition ref resolution, external notes, and unknown snapshot.
		sEmpty := newTestServer(t, config.AdminConfig{}, Deps{})
		if res := sEmpty.EvaluatePostcondition(SharingPostconditionEvaluateRequest{}, true); res.Result != PostconditionResultUnknown {
			t.Fatalf("expected unknown when no snapshot exists, got %+v", res)
		}
		notes := buildExternalInputNotes(
			[]string{"bind_scope", "network_check", "client_certificate", "declared_intent"},
			[]string{ExternalInputJWTKeySet, ExternalInputForwardAuth, ExternalInputProviderPolicy, ExternalInputFileContents},
			true,
		)
		if len(notes) != 8 {
			t.Fatalf("expected 8 external notes, got %d: %v", len(notes), notes)
		}

		sampleCfg := &config.Config{
			Plugins: map[string]config.PluginConfig{
				"mw":  {Path: "mw.wasm", Type: "middleware"},
				"hdl": {Path: "hdl.wasm", Type: "handler"},
			},
			Servers: []config.ServerConfig{{
				Listen: "${file:/listen}",
				TLS: &config.TLSConfig{
					Enabled:    true,
					ClientAuth: &config.ClientAuthConfig{Mode: "${file:/camode}", CAFile: "ca.pem"},
				},
				Locations: []config.LocationConfig{{
					RouteID: strPtrTest("r1"),
					Plugins: []string{"mw"},
					Plugin:  "hdl",
					Auth: &config.AuthConfig{
						Allow:       []string{"${file:/allow}"},
						ForwardAuth: &config.ForwardAuthConfig{URL: "http://auth.internal"},
					},
					Sharing: &config.SharingConfig{Mode: "${file:/smode}"},
				}},
			}},
		}
		expFacts, extIn := s.buildExpectedEntryFacts(sampleCfg, sampleCfg, 0, 0)
		if len(expFacts.OmittedFileFacts) != 4 || len(extIn) == 0 {
			t.Fatalf("expected all 4 file-dependent facts omitted, got %+v ext=%v", expFacts, extIn)
		}

		// Exercise resolvePostconditionRef edge cases.
		for _, tc := range []struct {
			state      string
			ref        SharingEntryRef
			expect     string
			wantStatus string
		}{
			{"serving", SharingEntryRef{Kind: "route_id", RouteID: ""}, "present", "unverifiable"},
			{"serving", SharingEntryRef{Kind: "position", Position: nil}, "present", "unverifiable"},
			{"serving", SharingEntryRef{Kind: "position", Position: &SharingEntryPosition{BlockIndex: 0, LocationIndex: 0}}, "absent", "not_found"},
			{"base_serving", SharingEntryRef{Kind: "position", Position: &SharingEntryPosition{BlockIndex: 0, LocationIndex: 0}}, "absent", "found"},
			{"other_revision", SharingEntryRef{Kind: "position", Position: &SharingEntryPosition{BlockIndex: 0, LocationIndex: 0}}, "absent", "unverifiable"},
			{"serving", SharingEntryRef{Kind: "position", Position: &SharingEntryPosition{Revision: "base", BlockIndex: 0, LocationIndex: 0}}, "present", "unverifiable"},
			{"serving", SharingEntryRef{Kind: "position", Position: &SharingEntryPosition{Revision: "candidate", BlockIndex: 99, LocationIndex: 0}}, "present", "unverifiable"},
			{"serving", SharingEntryRef{Kind: "bogus"}, "present", "unverifiable"},
		} {
			_, _, st, _ := resolvePostconditionRef(sampleCfg, tc.state, tc.ref, tc.expect)
			if st != tc.wantStatus {
				t.Fatalf("resolvePostconditionRef(%s, %+v, %s) = %s, want %s", tc.state, tc.ref, tc.expect, st, tc.wantStatus)
			}
		}

		// 5. SharingView handleSharingView stale cursor 409 & 503 when unavailable.
		rr503 := httptest.NewRecorder()
		sEmpty.handleSharingView(rr503, httptest.NewRequest(http.MethodGet, "/api/sharing", nil))
		if rr503.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 when snapshot unavailable, got %d", rr503.Code)
		}
		sWithSnap := newTestServer(t, config.AdminConfig{}, Deps{
			LiveSnapshot: func() server.LiveSnapshot {
				return server.LiveSnapshot{Generation: 1, RawConfig: sampleCfg, EffectiveConfig: sampleCfg}
			},
		})
		rr409 := httptest.NewRecorder()
		sWithSnap.handleSharingView(rr409, httptest.NewRequest(http.MethodGet, "/api/sharing?cursor=badcursor", nil))
		if rr409.Code != http.StatusConflict {
			t.Fatalf("expected 409 on bad cursor, got %d", rr409.Code)
		}

		// Additional postcondition & delta branches.
		if (SharingEntryRef{}).String() != "unknown" {
			t.Fatal("expected empty SharingEntryRef.String() == unknown")
		}
		if (SharingEntryRef{Kind: "position", Position: &SharingEntryPosition{BlockIndex: 1, LocationIndex: 2}}).String() != "servers[1].locations[2]" {
			t.Fatal("unexpected SharingEntryRef.String() without revision")
		}
		if repNil := s.BuildSharingPlanReport(nil, nil, nil, nil, true, nil, true); len(repNil.Entries) != 0 {
			t.Fatal("expected empty report on nil config")
		}
		_ = s.BuildSharingPlanReport(sampleCfg, nil, sampleCfg, nil, true, nil, true)

		sLoadOnly := newTestServer(t, config.AdminConfig{}, Deps{
			LoadConfig: func() (*config.Config, error) { return sampleCfg.Clone() },
		})
		lKeyID, _ := sLoadOnly.ensurePostconditionKey()
		lDig, _ := sLoadOnly.keyedConfigDigest(sampleCfg)
		fTrue := true
		resDiffs := sLoadOnly.EvaluatePostcondition(SharingPostconditionEvaluateRequest{
			PostconditionVersion:  1,
			KeyID:                 lKeyID,
			BaseConfigDigest:      lDig,
			CandidateConfigDigest: lDig,
			Repeatable:            false,
			Entries: []SharingPostconditionItem{
				{
					Ref:    SharingEntryRef{Kind: "route_id", RouteID: "r1"},
					Expect: "present",
					Expected: &SharingPostconditionExpected{
						BindScope:         "loopback",
						ClientCertificate: "required_by_listener",
						Withdrawn:         &fTrue,
						AccessFingerprint: "hmac-sha256:mismatch",
					},
				},
				{
					Ref:    SharingEntryRef{Kind: "route_id", RouteID: "missing-route"},
					Expect: "present",
				},
				{
					Ref:      SharingEntryRef{Kind: "route_id", RouteID: "r1"},
					Expect:   "present",
					Expected: nil,
				},
			},
		}, true)
		if resDiffs.Result != PostconditionResultDiverged {
			t.Fatalf("expected diverged result, got %+v", resDiffs)
		}
		resWrongKeyNonRep := sLoadOnly.EvaluatePostcondition(SharingPostconditionEvaluateRequest{
			PostconditionVersion: 1,
			KeyID:                "pk1-wrong",
			Repeatable:           false,
			Entries:              []SharingPostconditionItem{{Ref: SharingEntryRef{Kind: "route_id", RouteID: "r1"}, Expect: "present"}},
		}, true)
		if resWrongKeyNonRep.Result != PostconditionResultUnverifiable || !strings.Contains(resWrongKeyNonRep.RecoveryGuidance, "non-repeatable") {
			t.Fatalf("expected non-repeatable guidance on wrong key_id, got %+v", resWrongKeyNonRep)
		}
		if _, err := sLoadOnly.BuildSharingViewPage("", 10); err != nil {
			t.Fatalf("BuildSharingViewPage with LoadConfig fallback: %v", err)
		}
	})
}
