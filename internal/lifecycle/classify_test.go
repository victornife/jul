// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"errors"
	"reflect"
	"testing"

	"jul/internal/config"
)

func TestClassifyRequiresBothConfigs(t *testing.T) {
	if _, err := Classify(nil, fullConfig(), Live{}); !errors.Is(err, ErrNilConfig) {
		t.Fatalf("err = %v, want ErrNilConfig", err)
	}
	if _, err := Classify(fullConfig(), nil, Live{}); !errors.Is(err, ErrNilConfig) {
		t.Fatalf("err = %v, want ErrNilConfig", err)
	}
}

func TestClassifyNoChangesCanApplyHot(t *testing.T) {
	res, err := Classify(fullConfig(), fullConfig(), Live{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 0 {
		t.Fatalf("changes = %+v, want none", res.Changes)
	}
	if !res.CanApplyHot || !res.CanStageRestart {
		t.Fatalf("an empty transition must be applicable: %+v", res)
	}
}

func TestClassifyHotChange(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Servers[0].Locations[0].ProxyPass = "http://other"

	res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CanApplyHot {
		t.Fatalf("a proxy_pass edit must apply hot: %+v", res)
	}
	if len(res.Changes) != 1 || res.Changes[0].Effective != HotReloadClass {
		t.Fatalf("changes = %+v", res.Changes)
	}
}

// TestClassifyRetainedListenerIsRestartRequired proves the conditional
// resolution: the same edit is restart-required when the address survives.
func TestClassifyRetainedListenerIsRestartRequired(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Servers[0].TLS.MinVersion = "1.2"

	res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.CanApplyHot {
		t.Fatal("editing TLS on a retained listener cannot apply hot")
	}
	if !contains(res.RestartRequired, "servers.*.tls.min_version") {
		t.Fatalf("restart-required = %v", res.RestartRequired)
	}
	if res.Changes[0].Detail != detailRetainedListener {
		t.Fatalf("detail = %q", res.Changes[0].Detail)
	}
	if !res.CanStageRestart {
		t.Fatal("a restart-required change must still be stageable")
	}
}

// TestClassifyNewListenerAdoptsBindTimeValues proves the same leaf resolves to
// new_listener_only when only a newly added address is affected.
func TestClassifyNewListenerAdoptsBindTimeValues(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Servers = append(after.Servers, config.ServerConfig{
		Listen:      ":9443",
		ServerNames: []string{"new.example.com"},
		TLS:         &config.TLSConfig{Enabled: true, Cert: "inline-cert", Key: "inline-key", MinVersion: "1.2"},
	})

	res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CanApplyHot {
		t.Fatalf("adding a listener must apply hot: restart-required = %v", res.RestartRequired)
	}
	for _, ch := range res.Changes {
		if ch.Declared != RestartRequiredClass {
			continue
		}
		// A bind-time value on a brand-new address is adopted when the socket
		// binds. A backend-trust value is not a listener property at all: the
		// new route's outbound client is built with the generation that owns
		// it, so it applies on this reload. Both are "not restart-required";
		// they differ in why, and the class says which.
		want := NewListenerOnlyClass
		if isBackendTLSPath(ch.Path) {
			want = HotReloadClass
		}
		if ch.Effective != want {
			t.Errorf("%s resolved to %s, want %s", ch.Path, ch.Effective, want)
		}
	}
}

// TestClassifyRemovedListenerDoesNotStrand proves removing an address is not
// reported as restart-required.
func TestClassifyRemovedListenerDoesNotStrand(t *testing.T) {
	before := fullConfig()
	before.Servers = append(before.Servers, config.ServerConfig{
		Listen: ":9443", ServerNames: []string{"old.example.com"},
		TLS: &config.TLSConfig{Enabled: true, Cert: "inline", Key: "inline"},
	})
	after := fullConfig()

	res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443", ":9443"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CanApplyHot {
		t.Fatalf("removing a listener must apply hot: %v", res.RestartRequired)
	}
}

// TestClassifyListenerTimeoutOnRetainedAddress covers the new_listener_only
// group that is not part of the startup fingerprint.
func TestClassifyListenerTimeoutOnRetainedAddress(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Servers[0].ReadHeaderTimeout = config.Duration(9e9)

	res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.RestartRequired, "servers.*.read_header_timeout") {
		t.Fatalf("a bind-time timeout on a kept address must be restart-required: %+v", res)
	}
}

// TestClassifyConnectionCapIsListenerGlobal proves the global cap strands every
// kept listener.
func TestClassifyConnectionCapIsListenerGlobal(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.RateLimit.MaxConns = 5

	res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.RestartRequired, "rate_limit.max_conns") {
		t.Fatalf("restart-required = %v", res.RestartRequired)
	}

	// With no live listener there is nothing to strand.
	res, err = Classify(&config.Config{}, &config.Config{RateLimit: config.RateLimitConfig{MaxConns: 5}}, Live{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CanApplyHot {
		t.Fatalf("without a bound listener the cap applies on the next bind: %+v", res)
	}
}

// TestClassifyListenAddressEditIsHot proves that moving a listener is performed
// by the reload's listener diff rather than deferred to a restart.
func TestClassifyListenAddressEditIsHot(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Servers[0].Listen = ":8444"

	res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range res.Changes {
		if ch.Path == "servers.*.listen" && ch.Effective != HotReloadClass {
			t.Fatalf("servers.*.listen resolved to %s, want hot_reload", ch.Effective)
		}
	}
}

// TestClassifyMixedCandidate proves a candidate carrying both hot and
// restart-required edits is reported completely and cannot apply hot.
func TestClassifyMixedCandidate(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Servers[0].Locations[0].Root = "/srv2" // hot
	after.Cache.Enabled = false                  // restart (#92 left cache.enabled restart-required)
	after.Global.AccessLog = "/ignored.log"      // ignored

	res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.CanApplyHot {
		t.Fatal("a candidate carrying a restart-required edit cannot apply hot")
	}
	if !reflect.DeepEqual(res.RestartRequired, []string{"cache.enabled"}) {
		t.Fatalf("restart-required = %v", res.RestartRequired)
	}
	if !reflect.DeepEqual(res.IgnoredDeprecated, []string{"global.access_log"}) {
		t.Fatalf("ignored = %v", res.IgnoredDeprecated)
	}
	if len(res.Changes) != 3 {
		t.Fatalf("changes = %+v", res.Changes)
	}
}

// TestClassifyIgnoredOnlyCandidateAppliesHot proves an ignored-only edit is not
// turned into a restart.
func TestClassifyIgnoredOnlyCandidateAppliesHot(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Global.ErrorLog = "/var/log/other"

	res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CanApplyHot || len(res.RestartRequired) != 0 {
		t.Fatalf("ignored-only candidate must apply hot: %+v", res)
	}
}

// TestClassifyReservedFieldBlocksApply proves a validation-rejected field is
// neither hot nor stageable.
func TestClassifyReservedFieldBlocksApply(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Servers[0].TLS.ACME.DNSProvider = "cloudflare"

	res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.ValidationRejected, "servers.*.tls.acme.dns_provider") {
		t.Fatalf("validation-rejected = %v", res.ValidationRejected)
	}
	if res.CanApplyHot || res.CanStageRestart {
		t.Fatal("a validation-rejected change can be neither applied nor staged")
	}
}

// TestClassifyIsDeterministicAndSideEffectFree proves preview and apply reach
// identical verdicts from the same inputs, which is the property #77 relies on.
func TestClassifyIsDeterministicAndSideEffectFree(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Servers[0].TLS.MinVersion = "1.2"
	after.Servers[0].Locations[0].Root = "/srv2"
	live := Live{BoundHTTPAddrs: []string{":8443"}}

	preview, err := Classify(before, after, live)
	if err != nil {
		t.Fatal(err)
	}
	apply, err := Classify(before, after, live)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preview, apply) {
		t.Fatalf("preview and apply disagree:\npreview=%+v\napply=%+v", preview, apply)
	}
}

// TestClassifyAgreesWithRestartRequired proves the two entry points into the
// restart gate cannot drift: whatever Classify calls restart-required, the
// fingerprint comparison also rejects.
func TestClassifyAgreesWithRestartRequired(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*config.Config)
	}{
		{"cache", func(c *config.Config) { c.Cache.Enabled = false }},
		{"admin token", func(c *config.Config) { c.Admin.Token = "rotated" }},
		{"tracing", func(c *config.Config) { c.Observability.Tracing.Endpoint = "otel:4317" }},
		{"access log", func(c *config.Config) { c.Observability.AccessLog.Format = "json" }},
		{"egress", func(c *config.Config) { c.Egress.Allow = []string{"192.0.2.0/24"} }},
		{"log format", func(c *config.Config) { c.Global.LogFormat = "json" }},
		{"metrics", func(c *config.Config) { c.Observability.Metrics.HostLabel = false }},
		{"acme domains", func(c *config.Config) { c.Servers[0].TLS.ACME.Domains = []string{"other.example.com"} }},
		{"h2c", func(c *config.Config) { c.Servers[0].H2C = false }},
		{"http3", func(c *config.Config) { c.Servers[0].HTTP3.Enabled = false }},
	}
	live := Live{BoundHTTPAddrs: []string{":8443"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := fullConfig()
			after := fullConfig()
			tc.apply(after)

			res, err := Classify(before, after, live)
			if err != nil {
				t.Fatal(err)
			}
			_, gate := RestartRequired(ComputeFingerprint(before), ComputeFingerprint(after))
			if gate != !res.CanApplyHot {
				t.Fatalf("fingerprint gate = %v but Classify.CanApplyHot = %v", gate, res.CanApplyHot)
			}
		})
	}
}

// TestClassifyStreamProtocolIsHot pins the reclassification proven by the
// stream characterization matrix.
func TestClassifyStreamProtocolIsHot(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Streams[0].Protocol = "udp"

	res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CanApplyHot {
		t.Fatalf("switching the stream protocol must apply hot: %+v", res)
	}
}

// TestClassifyBackendTLSAppliesOnReload pins the classification earned by the
// consumer wiring: every outbound client — the HTTP transport, the native gRPC
// transport, the transcoder's connections and the health probe client — is
// rebuilt from the candidate policy, so a change applies on the next successful
// reload rather than needing a restart.
func TestClassifyBackendTLSAppliesOnReload(t *testing.T) {
	base := func() *config.Config {
		cfg := fullConfig()
		cfg.Upstreams[0].BackendTLS = &config.BackendTLSConfig{
			CAMode: "file_only", CAFile: "/etc/jul/ca.pem", ServerName: "app.internal",
		}
		return cfg
	}

	cases := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{
			name: "editing a retained pool's verified name",
			mutate: func(c *config.Config) {
				c.Upstreams[0].BackendTLS.ServerName = "moved.internal"
			},
		},
		{
			name: "changing the trust-root mode",
			mutate: func(c *config.Config) {
				c.Upstreams[0].BackendTLS.CAMode = "system_and_file"
			},
		},
		{
			name: "adding a peer identity",
			mutate: func(c *config.Config) {
				c.Upstreams[0].BackendTLS.PeerIdentities = []string{"dns:app.internal"}
			},
		},
		{
			name: "adding a pool that carries a policy",
			mutate: func(c *config.Config) {
				c.Upstreams = append(c.Upstreams, config.UpstreamConfig{
					Name:    "second",
					Servers: []config.UpstreamServer{{Address: "127.0.0.1:9443", Weight: 1}},
					BackendTLS: &config.BackendTLSConfig{
						CAMode: "file_only", CAFile: "/etc/jul/other-ca.pem", ServerName: "second.internal",
					},
				})
			},
		},
		{
			name: "editing a route's policy",
			mutate: func(c *config.Config) {
				c.Servers[0].Locations[0].BackendTLS.ServerName = "route-moved.internal"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := base()
			after := base()
			tc.mutate(after)
			res, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
			if err != nil {
				t.Fatal(err)
			}
			if !res.CanApplyHot {
				t.Fatalf("backend trust must apply on reload now that every consumer rebuilds: restart-required = %v", res.RestartRequired)
			}
			for _, path := range res.RestartRequired {
				if isBackendTLSPath(path) {
					t.Fatalf("%s was reported restart-required", path)
				}
			}
		})
	}
}
