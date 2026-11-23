// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

func TestConfigExplanationCatalog(t *testing.T) {
	tests := []struct {
		name      string
		code      string
		kind      string
		scope     string
		source    string
		state     string
		configure func(before, after *config.Config)
	}{
		{
			name:  "cache origin freshness",
			code:  "cache.origin_freshness",
			kind:  "information",
			scope: "global cache",
			configure: func(before, after *config.Config) {
				before.Cache.Enabled = true
				before.Cache.DefaultTTL = config.Duration(time.Minute)
				after.Cache = before.Cache
				after.Cache.DefaultTTL = config.Duration(2 * time.Minute)
			},
		},
		{
			name:  "cache stale serving",
			code:  "cache.stale_serving",
			kind:  "tradeoff",
			scope: "global cache",
			configure: func(before, after *config.Config) {
				before.Cache.Enabled = true
				before.Cache.StaleIfError = config.Duration(time.Minute)
				after.Cache = before.Cache
				after.Cache.StaleIfError = config.Duration(2 * time.Minute)
			},
		},
		{
			name:  "cache cookie session boundary",
			code:  "cache.cookie_session_boundary",
			kind:  "warning",
			scope: "/",
			configure: func(before, after *config.Config) {
				before.Servers[0].Locations[0].Cache = true
				after.Servers[0].Locations[0].Cache = true
				before.Servers[0].Locations[0].CachePolicy = &config.LocationCachePolicy{
					BypassAnyCookie: true,
				}
				after.Servers[0].Locations[0].CachePolicy = &config.LocationCachePolicy{}
			},
		},
		{
			name:  "cache range can fill full object",
			code:  "cache.range_full_fill",
			kind:  "tradeoff",
			scope: "global cache",
			configure: func(before, after *config.Config) {
				before.Cache.Enabled = true
				after.Cache = before.Cache
				after.Cache.RangeEnabled = true
			},
		},
		{
			name:  "removed route authentication",
			code:  "access.route_auth_removed",
			kind:  "critical",
			scope: "/",
			configure: func(before, after *config.Config) {
				before.Servers[0].Locations[0].Auth = &config.AuthConfig{}
			},
		},
		{
			name:  "trusted forwarding identity",
			code:  "access.trusted_forwarding_identity",
			kind:  "warning",
			scope: ":8080",
			configure: func(before, after *config.Config) {
				before.Servers[0].ClientAddress = &config.ClientAddressConfig{
					TrustedProxies: []string{"10.0.0.0/8"},
				}
				after.Servers[0].ClientAddress = &config.ClientAddressConfig{
					TrustedProxies: []string{"10.0.0.0/8", "192.168.0.0/16"},
				}
			},
		},
		{
			name:  "new route rate limit",
			code:  "rate.request_not_bandwidth",
			kind:  "information",
			scope: "/",
			configure: func(before, after *config.Config) {
				after.Servers[0].Locations[0].RateLimit = &config.RateLimitConfig{
					Enabled: true,
					Key:     "ip",
					Rate:    10,
					Burst:   20,
				}
			},
		},
		{
			name:  "WAF response body inspection cost",
			code:  "inspection.body_resource_cost",
			kind:  "tradeoff",
			scope: "global WAF body inspection",
			configure: func(before, after *config.Config) {
				before.Servers = nil
				after.Servers = nil
				before.WAF = config.WAFConfig{Enabled: true, Mode: "detect"}
				after.WAF = before.WAF
				after.WAF.ResponseBodyCheck = true
			},
		},
		{
			name:  "WAF detect mode",
			code:  "waf.detect_not_block",
			kind:  "warning",
			scope: "global WAF",
			configure: func(before, after *config.Config) {
				before.Servers = nil
				after.Servers = nil
				before.WAF = config.WAFConfig{Enabled: true, Mode: "block"}
				after.WAF = config.WAFConfig{Enabled: true, Mode: "detect"}
			},
		},
		{
			name:  "WAF block mode",
			code:  "waf.block_policy",
			kind:  "warning",
			scope: "global WAF",
			configure: func(before, after *config.Config) {
				before.Servers = nil
				after.Servers = nil
				before.WAF = config.WAFConfig{Enabled: true, Mode: "detect"}
				after.WAF = config.WAFConfig{Enabled: true, Mode: "block"}
			},
		},
		{
			name:  "WAF body limit resource cost",
			code:  "inspection.body_resource_cost",
			kind:  "tradeoff",
			scope: "global WAF body inspection",
			configure: func(before, after *config.Config) {
				before.Servers = nil
				after.Servers = nil
				before.WAF = config.WAFConfig{Enabled: true, Mode: "detect", RequestBodyLimit: config.Size(128 << 10)}
				after.WAF = config.WAFConfig{Enabled: true, Mode: "detect", RequestBodyLimit: config.Size(1 << 20)}
			},
		},
		{
			name:  "global WAF mode inherited by route",
			code:  "waf.detect_not_block",
			kind:  "warning",
			scope: "127.0.0.1:8080 prefix /",
			configure: func(before, after *config.Config) {
				before.WAF = config.WAFConfig{Enabled: true, Mode: "block"}
				after.WAF = config.WAFConfig{Enabled: true, Mode: "detect"}
			},
		},
		{
			name:  "global WAF enabled",
			code:  "waf.build_capability",
			kind:  "warning",
			scope: "global WAF",
			configure: func(before, after *config.Config) {
				before.Servers = nil
				after.Servers = nil
				before.WAF = config.WAFConfig{Mode: "block"}
				after.WAF = config.WAFConfig{Enabled: true, Mode: "block"}
			},
		},
		{
			name:   "global WAF outcome policy changed",
			code:   "waf.request_outcome_unobserved",
			kind:   "information",
			scope:  "global WAF",
			source: "request_event_producer",
			state:  "not_observed",
			configure: func(before, after *config.Config) {
				before.Servers = nil
				after.Servers = nil
				before.WAF = config.WAFConfig{Enabled: true, Mode: "block"}
				after.WAF = config.WAFConfig{
					Enabled: true, Mode: "block", BlockStatus: 403,
					DirectivesFiles: []string{"/etc/jul/waf.conf"}, InlineRules: "SecRuleEngine On",
				}
			},
		},
		{
			name:  "route WAF enabled",
			code:  "waf.effective_policy",
			kind:  "warning",
			scope: "127.0.0.1:8080 prefix /",
			configure: func(before, after *config.Config) {
				after.Servers[0].Locations[0].WAF = &config.WAFConfig{Enabled: true, Mode: "block"}
			},
		},
		{
			name:  "route WAF disabled",
			code:  "waf.effective_policy",
			kind:  "warning",
			scope: "127.0.0.1:8080 prefix /",
			configure: func(before, after *config.Config) {
				before.Servers[0].Locations[0].WAF = &config.WAFConfig{Enabled: true, Mode: "block"}
			},
		},
		{
			name:  "backend verification disabled",
			code:  "tls.backend_identity_unverified",
			kind:  "critical",
			scope: "api",
			configure: func(before, after *config.Config) {
				before.Upstreams = []config.UpstreamConfig{{
					Name:    "api",
					Servers: []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					BackendTLS: &config.BackendTLSConfig{
						ServerName: "api.internal",
					},
				}}
				after.Upstreams = []config.UpstreamConfig{{
					Name:    "api",
					Servers: []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					BackendTLS: &config.BackendTLSConfig{
						ServerName:         "api.internal",
						InsecureSkipVerify: true,
					},
				}}
			},
		},
		{
			name:  "backend verification restored",
			code:  "tls.backend_verification_restored",
			kind:  "information",
			scope: "api",
			configure: func(before, after *config.Config) {
				before.Upstreams = []config.UpstreamConfig{{
					Name:       "api",
					Servers:    []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					BackendTLS: &config.BackendTLSConfig{InsecureSkipVerify: true},
				}}
				after.Upstreams = []config.UpstreamConfig{{
					Name:       "api",
					Servers:    []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					BackendTLS: &config.BackendTLSConfig{ServerName: "api.internal"},
				}}
			},
		},
		{
			name:  "backend trust changed",
			code:  "tls.backend_trust_changed",
			kind:  "warning",
			scope: "api",
			configure: func(before, after *config.Config) {
				before.Upstreams = []config.UpstreamConfig{{
					Name:       "api",
					Servers:    []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					BackendTLS: &config.BackendTLSConfig{CAFile: "/etc/jul/old-ca.pem"},
				}}
				after.Upstreams = []config.UpstreamConfig{{
					Name:       "api",
					Servers:    []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					BackendTLS: &config.BackendTLSConfig{CAFile: "/etc/jul/new-ca.pem"},
				}}
			},
		},
		{
			name:  "inbound TLS disabled",
			code:  "tls.inbound_weakened",
			kind:  "critical",
			scope: ":8080",
			configure: func(before, after *config.Config) {
				before.Servers[0].TLS = &config.TLSConfig{Enabled: true}
				after.Servers[0].TLS = &config.TLSConfig{}
			},
		},
		{
			name:  "inbound TLS enabled",
			code:  "tls.certificate_prerequisite",
			kind:  "tradeoff",
			scope: ":8080",
			configure: func(before, after *config.Config) {
				after.Servers[0].TLS = &config.TLSConfig{Enabled: true}
			},
		},
		{
			name:  "inbound TLS minimum weakened",
			code:  "tls.inbound_weakened",
			kind:  "warning",
			scope: ":8080",
			configure: func(before, after *config.Config) {
				before.Servers[0].TLS = &config.TLSConfig{Enabled: true, MinVersion: "1.3"}
				after.Servers[0].TLS = &config.TLSConfig{Enabled: true, MinVersion: "1.2"}
			},
		},
		{
			name:  "mTLS requires client reconnect",
			code:  "tls.mtls_handshake_boundary",
			kind:  "information",
			scope: ":8080",
			configure: func(before, after *config.Config) {
				before.Servers[0].TLS = &config.TLSConfig{
					Enabled:    true,
					ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: "/etc/jul/clients.pem"},
				}
				after.Servers[0].TLS = &config.TLSConfig{
					Enabled:    true,
					ClientAuth: &config.ClientAuthConfig{Mode: "request", CAFile: "/etc/jul/clients.pem"},
				}
			},
		},
		{
			name:  "mTLS disabled",
			code:  "tls.mtls_weakened",
			kind:  "critical",
			scope: ":8080",
			configure: func(before, after *config.Config) {
				before.Servers[0].TLS = &config.TLSConfig{
					Enabled:    true,
					ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: "/etc/jul/clients.pem"},
				}
				after.Servers[0].TLS = &config.TLSConfig{Enabled: true}
			},
		},
		{
			name:  "route client certificate requirement changed",
			code:  "tls.mtls_route_prerequisite",
			kind:  "warning",
			scope: "127.0.0.1:8080 prefix /",
			configure: func(before, after *config.Config) {
				before.Servers[0].Locations[0].RequireClientCert = true
			},
		},
		{
			name:  "untrusted rate key",
			code:  "rate.header_key_untrusted",
			kind:  "warning",
			scope: "global rate limit",
			configure: func(before, after *config.Config) {
				before.RateLimit = config.RateLimitConfig{Enabled: true, Key: "ip", Rate: 10, Burst: 20}
				after.RateLimit = config.RateLimitConfig{Enabled: true, Key: "header:X-Client", Rate: 10, Burst: 20}
			},
		},
		{
			name:  "JWT rate key depends on auth",
			code:  "rate.jwt_key_requires_auth",
			kind:  "tradeoff",
			scope: "global rate limit",
			configure: func(before, after *config.Config) {
				before.RateLimit = config.RateLimitConfig{Enabled: true, Key: "ip", Rate: 10, Burst: 20}
				after.RateLimit = config.RateLimitConfig{Enabled: true, Key: "jwt:sub", Rate: 10, Burst: 20}
			},
		},
		{
			name:  "listener connection cap",
			code:  "limits.connection_admission",
			kind:  "tradeoff",
			scope: "global listener connection cap",
			configure: func(before, after *config.Config) {
				before.RateLimit = config.RateLimitConfig{Enabled: true, Key: "ip", Rate: 10, Burst: 20, MaxConns: 100}
				after.RateLimit = config.RateLimitConfig{Enabled: true, Key: "ip", Rate: 10, Burst: 20, MaxConns: 50}
			},
		},
		{
			name:  "retry may repeat upstream work",
			code:  "resilience.retry_repeats_work",
			kind:  "warning",
			scope: "api",
			configure: func(before, after *config.Config) {
				before.Upstreams = []config.UpstreamConfig{{
					Name:    "api",
					Servers: []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					Resilience: &config.ResilienceConfig{
						RetryAttempts: 1,
					},
				}}
				after.Upstreams = []config.UpstreamConfig{{
					Name:    "api",
					Servers: []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					Resilience: &config.ResilienceConfig{
						RetryAttempts: 2,
					},
				}}
			},
		},
		{
			name:  "resilience queue is bounded",
			code:  "resilience.bounded_queue",
			kind:  "tradeoff",
			scope: "api",
			configure: func(before, after *config.Config) {
				before.Upstreams = []config.UpstreamConfig{{
					Name: "api", Servers: []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					Resilience: &config.ResilienceConfig{MaxPendingRequests: 10, PendingTimeout: config.Duration(time.Second)},
				}}
				after.Upstreams = []config.UpstreamConfig{{
					Name: "api", Servers: []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					Resilience: &config.ResilienceConfig{MaxPendingRequests: 20, PendingTimeout: config.Duration(2 * time.Second)},
				}}
			},
		},
		{
			name:  "resilience admission is bounded",
			code:  "resilience.admission_bounds",
			kind:  "tradeoff",
			scope: "api",
			configure: func(before, after *config.Config) {
				before.Upstreams = []config.UpstreamConfig{{
					Name: "api", Servers: []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					Resilience: &config.ResilienceConfig{MaxActiveRequests: 10},
				}}
				after.Upstreams = []config.UpstreamConfig{{
					Name: "api", Servers: []config.UpstreamServer{{Address: "127.0.0.1:9000"}},
					Resilience: &config.ResilienceConfig{MaxActiveRequests: 20},
				}}
			},
		},
		{
			name:  "route resilience override",
			code:  "resilience.location_override",
			kind:  "tradeoff",
			scope: "/",
			configure: func(before, after *config.Config) {
				after.Servers[0].Locations[0].Resilience = &config.LocationResilienceConfig{RetryAttempts: 2}
			},
		},
		{
			name:  "proxy read timeout is inactivity bound",
			code:  "resilience.timeout_semantics",
			kind:  "tradeoff",
			scope: "/",
			configure: func(before, after *config.Config) {
				before.Servers[0].Locations[0].ProxyReadTimeout = config.Duration(time.Second)
				after.Servers[0].Locations[0].ProxyReadTimeout = config.Duration(2 * time.Second)
			},
		},
		{
			name:  "route retarget",
			code:  "routing.target_retargeted",
			kind:  "warning",
			scope: "/",
			configure: func(before, after *config.Config) {
				before.Servers[0].Locations[0].ProxyPass = "http://127.0.0.1:9001"
				after.Servers[0].Locations[0].ProxyPass = "http://127.0.0.1:9002"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := config.ProxyTarget("127.0.0.1:9001", "127.0.0.1:8080")
			after, err := before.Clone()
			if err != nil {
				t.Fatal(err)
			}
			tt.configure(before, after)
			diff := DiffConfigs(before, after)
			entry := findExplanation(t, diff.Explanations, tt.code)
			if entry.Kind != tt.kind || !strings.Contains(entry.Scope, tt.scope) {
				t.Fatalf("explanation = %+v, want kind=%q and scope containing %q", entry, tt.kind, tt.scope)
			}
			source, state := tt.source, tt.state
			if source == "" {
				source = "candidate_configuration"
			}
			if state == "" {
				state = "candidate_only"
			}
			if entry.EvidenceSource != source || entry.EvidenceState != state {
				t.Fatalf("explanation evidence = %+v, want source=%q state=%q", entry, source, state)
			}
			if entry.Message == "" || entry.NextAction == "" {
				t.Fatalf("explanation is not actionable: %+v", entry)
			}
		})
	}
}

func TestProxyTimeoutExplanationsDistinguishConnectFromInactivity(t *testing.T) {
	before := config.ProxyTarget("127.0.0.1:9001", "127.0.0.1:8080")
	after, err := before.Clone()
	if err != nil {
		t.Fatal(err)
	}
	before.Servers[0].Locations[0].ProxyConnectTimeout = config.Duration(time.Second)
	after.Servers[0].Locations[0].ProxyConnectTimeout = config.Duration(2 * time.Second)
	connect := findExplanation(t, DiffConfigs(before, after).Explanations, "resilience.timeout_semantics")
	if !strings.Contains(connect.Message, "establishing the upstream connection") || strings.Contains(connect.Message, "inactivity") {
		t.Fatalf("connect timeout explanation incorrectly describes inactivity: %+v", connect)
	}

	before.Servers[0].Locations[0].ProxyConnectTimeout = 0
	after.Servers[0].Locations[0].ProxyConnectTimeout = 0
	before.Servers[0].Locations[0].ProxyReadTimeout = config.Duration(time.Second)
	after.Servers[0].Locations[0].ProxyReadTimeout = config.Duration(2 * time.Second)
	read := findExplanation(t, DiffConfigs(before, after).Explanations, "resilience.timeout_semantics")
	if !strings.Contains(read.Message, "inactivity") || !strings.Contains(read.Message, "stream") {
		t.Fatalf("read timeout explanation omits inactivity/streaming semantics: %+v", read)
	}
}

func TestTypedAndRawAssessmentShareCandidateExplanations(t *testing.T) {
	before := config.ProxyTarget("127.0.0.1:9001", "127.0.0.1:8080")
	ops := []patchRequest{{
		Op:        "route_set_target",
		Listen:    "127.0.0.1:8080",
		MatchType: "prefix",
		Path:      "/",
		Target:    "http://127.0.0.1:9002",
	}}
	live := lifecycle.Live{BoundHTTPAddrs: []string{"127.0.0.1:8080"}}
	typed, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: before,
		Live:   live,
	}, "", ops)
	if err != nil {
		t.Fatalf("typed assessment: %v", err)
	}
	rawCandidate, err := config.Marshal(typed.CandidateConfig)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := previewRawCandidate(context.Background(), before, nil, live, "base", rawCandidate)
	if err != nil {
		t.Fatalf("raw assessment: %v", err)
	}
	if !reflect.DeepEqual(typed.Diff.Explanations, raw.Diff.Explanations) {
		t.Fatalf("typed explanations differ from raw assessment:\ntyped: %#v\nraw: %#v", typed.Diff.Explanations, raw.Diff.Explanations)
	}
}

func TestConfigExplanationsAreQuietDeterministicAndBounded(t *testing.T) {
	before := config.ProxyTarget("127.0.0.1:9001", "127.0.0.1:8080")
	unchanged, err := before.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if got := DiffConfigs(before, unchanged).Explanations; len(got) != 0 {
		t.Fatalf("semantic no-op explanations = %+v, want none", got)
	}

	unrelated, err := before.Clone()
	if err != nil {
		t.Fatal(err)
	}
	unrelated.Global.LogLevel = "debug"
	if got := DiffConfigs(before, unrelated).Explanations; len(got) != 0 {
		t.Fatalf("unrelated log-level edit explanations = %+v, want none", got)
	}

	var diff ConfigDiff
	diff.explain(candidateExplanation("z", "warning", "b", "message", "next", ""))
	diff.explain(candidateExplanation("z", "warning", "b", "duplicate", "next", ""))
	diff.explain(candidateExplanation("z", "warning", "a", "message", "next", ""))
	if len(diff.Explanations) != 2 {
		t.Fatalf("deduplicated explanations = %d, want same code/scope collapsed but distinct scope retained", len(diff.Explanations))
	}
	left := []ConfigExplanation{
		candidateExplanation("z", "warning", "b", "warning", "next", ""),
		candidateExplanation("a", "information", "a", "information", "next", ""),
		candidateExplanation("a", "warning", "z", "same kind and code", "next", ""),
		candidateExplanation("a", "warning", "a", "same kind and code", "next", ""),
		candidateExplanation("unknown", "unclassified", "scope", "unknown kind", "next", ""),
	}
	right := []ConfigExplanation{left[1], left[0]}
	right = append(right, left[2:]...)
	if !reflect.DeepEqual(finalizeExplanations(left), finalizeExplanations(right)) {
		t.Fatal("explanation order depends on insertion order")
	}
	ordered := finalizeExplanations(left)
	if ordered[0].Kind != "warning" || ordered[0].Code != "a" || ordered[0].Scope != "a" ||
		ordered[len(ordered)-1].Kind != "unclassified" {
		t.Fatalf("explanation tie-break or unknown-kind ordering = %+v", ordered)
	}

	entries := make([]ConfigExplanation, maxConfigExplanations+5)
	for i := range entries {
		entries[i] = ConfigExplanation{
			Code:           "catalogue.test",
			Kind:           "warning",
			Scope:          strings.Repeat("x", i+1),
			EvidenceSource: "candidate_configuration",
			EvidenceState:  "candidate_only",
			Message:        "bounded",
		}
	}
	bounded := finalizeExplanations(entries)
	if len(bounded) != maxConfigExplanations || bounded[len(bounded)-1].Code != "catalogue.scope_summarized" {
		t.Fatalf("bounded explanations = %d entries, last=%+v", len(bounded), bounded[len(bounded)-1])
	}
}

func findExplanation(t *testing.T, entries []ConfigExplanation, code string) ConfigExplanation {
	t.Helper()
	for _, entry := range entries {
		if entry.Code == code {
			return entry
		}
	}
	t.Fatalf("explanation code %q not found in %+v", code, entries)
	return ConfigExplanation{}
}
