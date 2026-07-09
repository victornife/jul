// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"testing"
	"time"

	"jul/internal/config"
)

// These characterization tests pin the behaviour of the pure patch builders
// extracted into patch_builders.go (AUX-05 / #49). They exercise the DTO →
// config translation and audit-summary rendering directly, independent of the
// applyPatch dispatch and the HTTP surface, so the extraction is provably
// behaviour-preserving and the seam stays honest for future refactors.

func TestBuildLocationAuth(t *testing.T) {
	t.Run("cidr", func(t *testing.T) {
		cfg, note, err := buildLocationAuth(locationAuth{Method: "cidr", Allow: []string{" 10.0.0.0/8 ", ""}, Deny: []string{"10.9.0.0/16"}})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if note != "IP allow/deny" {
			t.Errorf("note = %q", note)
		}
		if len(cfg.Allow) != 1 || cfg.Allow[0] != "10.0.0.0/8" || len(cfg.Deny) != 1 {
			t.Errorf("allow/deny = %v / %v (blank entries should be trimmed)", cfg.Allow, cfg.Deny)
		}
	})
	t.Run("cidr empty rejected", func(t *testing.T) {
		if _, _, err := buildLocationAuth(locationAuth{Method: "cidr"}); err == nil {
			t.Error("expected error for cidr with no allow/deny")
		}
	})
	t.Run("basic", func(t *testing.T) {
		cfg, note, err := buildLocationAuth(locationAuth{Method: "basic", BasicFile: " /etc/htpasswd ", BasicRealm: "R"})
		if err != nil || cfg.Basic == nil || cfg.Basic.File != "/etc/htpasswd" || cfg.Basic.Realm != "R" || note != "HTTP Basic" {
			t.Errorf("basic = %+v note=%q err=%v", cfg.Basic, note, err)
		}
	})
	t.Run("basic needs file", func(t *testing.T) {
		if _, _, err := buildLocationAuth(locationAuth{Method: "basic"}); err == nil {
			t.Error("expected error for basic with no file")
		}
	})
	t.Run("jwt", func(t *testing.T) {
		cfg, note, err := buildLocationAuth(locationAuth{Method: "jwt", JWTJWKSURL: "https://i/.well-known/jwks", JWTIssuer: "iss", JWTAudience: "aud"})
		if err != nil || cfg.JWT == nil || cfg.JWT.JWKSURL != "https://i/.well-known/jwks" || note != "JWT" {
			t.Errorf("jwt = %+v note=%q err=%v", cfg.JWT, note, err)
		}
	})
	t.Run("jwt needs url", func(t *testing.T) {
		if _, _, err := buildLocationAuth(locationAuth{Method: "jwt"}); err == nil {
			t.Error("expected error for jwt with no jwks_url")
		}
	})
	t.Run("forward", func(t *testing.T) {
		cfg, note, err := buildLocationAuth(locationAuth{Method: "forward", ForwardURL: "http://auth"})
		if err != nil || cfg.ForwardAuth == nil || cfg.ForwardAuth.URL != "http://auth" || note != "forward-auth" {
			t.Errorf("forward = %+v note=%q err=%v", cfg.ForwardAuth, note, err)
		}
	})
	t.Run("unknown method", func(t *testing.T) {
		if _, _, err := buildLocationAuth(locationAuth{Method: "oauth"}); err == nil {
			t.Error("expected error for unknown method")
		}
	})
}

func TestBuildHealthCheck(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		hc, note, err := buildHealthCheck(upstreamHealthCheck{Enabled: false})
		if err != nil || hc != nil || note != "disabled" {
			t.Errorf("hc=%v note=%q err=%v", hc, note, err)
		}
	})
	t.Run("http requires path", func(t *testing.T) {
		if _, _, err := buildHealthCheck(upstreamHealthCheck{Enabled: true, Type: "http"}); err == nil {
			t.Error("expected error for http probe without a path")
		}
	})
	t.Run("http defaults + durations", func(t *testing.T) {
		hc, note, err := buildHealthCheck(upstreamHealthCheck{Enabled: true, Path: "/healthz", Interval: "5s", Timeout: "2s", ExpectStatus: []int{200, 204}})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if hc.Type != "http" { // empty type defaults to http
			t.Errorf("type = %q, want http", hc.Type)
		}
		if hc.Interval != config.Duration(5*time.Second) || hc.Timeout != config.Duration(2*time.Second) {
			t.Errorf("durations = %v / %v", hc.Interval, hc.Timeout)
		}
		if len(hc.ExpectStatus) != 2 || hc.ExpectStatus[0] != 200 {
			t.Errorf("expect_status = %v", hc.ExpectStatus)
		}
		if note != "enabled (http /healthz)" {
			t.Errorf("note = %q", note)
		}
	})
	t.Run("tcp", func(t *testing.T) {
		hc, note, err := buildHealthCheck(upstreamHealthCheck{Enabled: true, Type: "tcp"})
		if err != nil || hc.Type != "tcp" || note != "enabled (tcp)" {
			t.Errorf("hc=%+v note=%q err=%v", hc, note, err)
		}
	})
	t.Run("invalid type", func(t *testing.T) {
		if _, _, err := buildHealthCheck(upstreamHealthCheck{Enabled: true, Type: "icmp"}); err == nil {
			t.Error("expected error for invalid probe type")
		}
	})
}

func TestBuildDiscovery(t *testing.T) {
	t.Run("static clears", func(t *testing.T) {
		d, note, err := buildDiscovery(upstreamDiscovery{Type: "static"}, nil)
		if err != nil || d != nil || note != "disabled (static backends)" {
			t.Errorf("d=%v note=%q err=%v", d, note, err)
		}
	})
	t.Run("invalid type", func(t *testing.T) {
		if _, _, err := buildDiscovery(upstreamDiscovery{Type: "etcd"}, nil); err == nil {
			t.Error("expected error for invalid discovery type")
		}
	})
	t.Run("consul preserves token on same type", func(t *testing.T) {
		prev := &config.DiscoveryConfig{Type: "consul", Consul: &config.ConsulDiscovery{Token: "secret-acl"}}
		d, _, err := buildDiscovery(upstreamDiscovery{Type: "consul", Consul: &consulDiscoveryFields{Service: "web"}}, prev)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if d.Consul == nil || d.Consul.Service != "web" || d.Consul.Token != "secret-acl" {
			t.Errorf("consul = %+v (token must be preserved from prev)", d.Consul)
		}
	})
	t.Run("kubernetes preserves token on same type", func(t *testing.T) {
		prev := &config.DiscoveryConfig{Type: "kubernetes", Kubernetes: &config.KubernetesDiscovery{Token: "secret-bearer"}}
		d, _, err := buildDiscovery(upstreamDiscovery{Type: "kubernetes", Kubernetes: &k8sDiscoveryFields{Service: "api"}}, prev)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if d.Kubernetes == nil || d.Kubernetes.Service != "api" || d.Kubernetes.Token != "secret-bearer" {
			t.Errorf("kubernetes = %+v (token must be preserved from prev)", d.Kubernetes)
		}
	})
	t.Run("type change discards previous token", func(t *testing.T) {
		prev := &config.DiscoveryConfig{Type: "consul", Consul: &config.ConsulDiscovery{Token: "old"}}
		d, _, err := buildDiscovery(upstreamDiscovery{Type: "kubernetes", Kubernetes: &k8sDiscoveryFields{Service: "api"}}, prev)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if d.Kubernetes == nil || d.Kubernetes.Token != "" {
			t.Errorf("switching provider must not carry the old token: %+v", d.Kubernetes)
		}
	})
}

func TestParseDurInto(t *testing.T) {
	t.Run("empty leaves zero", func(t *testing.T) {
		var d config.Duration
		if err := parseDurInto("", &d, "interval"); err != nil || d != 0 {
			t.Errorf("d=%v err=%v, want 0/nil", d, err)
		}
	})
	t.Run("valid", func(t *testing.T) {
		var d config.Duration
		if err := parseDurInto("1500ms", &d, "timeout"); err != nil || d != config.Duration(1500*time.Millisecond) {
			t.Errorf("d=%v err=%v", d, err)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		var d config.Duration
		if err := parseDurInto("nope", &d, "interval"); err == nil {
			t.Error("expected a parse error")
		}
	})
}

func TestPatchSummaryFormatters(t *testing.T) {
	if got := wafModeNote(false, "block", true); got != "" {
		t.Errorf("disabled waf note = %q, want empty", got)
	}
	if got := wafModeNote(true, "block", true); got != " — block, CRS" {
		t.Errorf("waf note = %q", got)
	}
	if got := wafModeNote(true, "detect", false); got != " — detect" {
		t.Errorf("waf note = %q", got)
	}
	if onOff(true) != "enabled" || onOff(false) != "disabled" {
		t.Error("onOff mapping wrong")
	}
	if orDefault("  ", "def") != "def" || orDefault("x", "def") != "x" {
		t.Error("orDefault mapping wrong")
	}
	if got := trimNonEmpty([]string{" a ", "", "  ", "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("trimNonEmpty = %v", got)
	}
}
