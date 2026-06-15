package config

import (
	"strings"
	"testing"
)

func lintMessages(diags []Diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Severity.String())
		b.WriteString(": ")
		b.WriteString(d.Field)
		b.WriteString(": ")
		b.WriteString(d.Message)
		b.WriteByte('\n')
	}
	return b.String()
}

func hasWarning(diags []Diagnostic, substr string) bool {
	for _, d := range diags {
		if strings.Contains(d.Message, substr) {
			return true
		}
	}
	return false
}

func TestLintEmptyServerWarns(t *testing.T) {
	c := &Config{
		Servers:     []ServerConfig{{Listen: ":80"}},
		Compression: CompressionConfig{Enabled: true},
	}
	if !hasWarning(Lint(c), "no locations") {
		t.Errorf("expected a no-locations warning, got:\n%s", lintMessages(Lint(c)))
	}
}

func TestLintEmptyServerWithRedirectIsClean(t *testing.T) {
	c := &Config{
		Servers:     []ServerConfig{{Listen: ":80", RedirectHTTPS: 308}},
		Compression: CompressionConfig{Enabled: true},
	}
	if hasWarning(Lint(c), "no locations") {
		t.Errorf("HTTPS redirector should not warn about missing locations:\n%s", lintMessages(Lint(c)))
	}
}

func TestLintDuplicateLocation(t *testing.T) {
	c := &Config{
		Servers: []ServerConfig{{
			Listen: ":80",
			Locations: []LocationConfig{
				{Match: MatchConfig{Type: "prefix", Path: "/api"}, Root: "/a"},
				{Match: MatchConfig{Type: "prefix", Path: "/api"}, Root: "/b"},
			},
		}},
		Compression: CompressionConfig{Enabled: true},
	}
	if !hasWarning(Lint(c), "unreachable") {
		t.Errorf("expected an unreachable-location warning, got:\n%s", lintMessages(Lint(c)))
	}
}

func TestLintDirectoryListing(t *testing.T) {
	c := &Config{
		Servers: []ServerConfig{{
			Listen:    ":80",
			Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv", DirectoryListing: true}},
		}},
		Compression: CompressionConfig{Enabled: true},
	}
	if !hasWarning(Lint(c), "directory_listing") {
		t.Errorf("expected a directory_listing warning, got:\n%s", lintMessages(Lint(c)))
	}
}

func TestLintTLSMinVersion(t *testing.T) {
	withTLS := func(min string) *Config {
		return &Config{
			Servers: []ServerConfig{{
				Listen:    ":443",
				TLS:       &TLSConfig{Enabled: true, Cert: "c", Key: "k", MinVersion: min},
				Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}},
			}},
			Compression: CompressionConfig{Enabled: true},
		}
	}
	if !hasWarning(Lint(withTLS("")), "min_version") {
		t.Error("expected a min_version warning when unset")
	}
	if hasWarning(Lint(withTLS("1.3")), "min_version") {
		t.Error("did not expect a min_version warning when explicitly 1.3")
	}
}

func TestLintAdminExposed(t *testing.T) {
	base := func(listen, token string) *Config {
		return &Config{
			Servers:     []ServerConfig{{Listen: ":80", Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}}}},
			Compression: CompressionConfig{Enabled: true},
			Admin:       AdminConfig{Enabled: true, Listen: listen, Token: token},
		}
	}
	if !hasWarning(Lint(base("0.0.0.0:9090", "")), "unauthenticated") {
		t.Error("expected a warning for exposed admin without token")
	}
	if hasWarning(Lint(base("127.0.0.1:9090", "")), "unauthenticated") {
		t.Error("loopback admin should not warn")
	}
	if hasWarning(Lint(base("0.0.0.0:9090", "secret")), "unauthenticated") {
		t.Error("tokened admin should not warn")
	}
}

func TestLintCompressionDisabled(t *testing.T) {
	c := &Config{Servers: []ServerConfig{{Listen: ":80", Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}}}}}
	if !hasWarning(Lint(c), "compression is disabled") {
		t.Errorf("expected a compression-disabled warning, got:\n%s", lintMessages(Lint(c)))
	}
}

func TestLintCleanConfigHasNoWarnings(t *testing.T) {
	c := &Config{
		Servers: []ServerConfig{{
			Listen:    ":443",
			TLS:       &TLSConfig{Enabled: true, Cert: "c", Key: "k", MinVersion: "1.3"},
			Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}},
		}},
		Compression: CompressionConfig{Enabled: true},
		Admin:       AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: "secret"},
	}
	if diags := Lint(c); len(diags) != 0 {
		t.Errorf("expected no warnings, got:\n%s", lintMessages(diags))
	}
}

func TestIsLoopbackListen(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:9090": true,
		"localhost:9090": true,
		"[::1]:9090":     true,
		"0.0.0.0:9090":   false,
		":9090":          false,
		"10.0.0.5:9090":  false,
		"127.0.0.1":      true,
	}
	for addr, want := range cases {
		if got := isLoopbackListen(addr); got != want {
			t.Errorf("isLoopbackListen(%q) = %v, want %v", addr, got, want)
		}
	}
}
