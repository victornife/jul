package waf

import (
	"testing"

	"jul/internal/config"
)

func cfgWithGlobalWAF(enabled bool) *config.Config {
	return &config.Config{
		WAF: config.WAFConfig{Enabled: enabled},
		Servers: []config.ServerConfig{{
			Listen:    ":80",
			Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}},
		}},
	}
}

func cfgWithLocationWAF(enabled bool) *config.Config {
	return &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":80",
			Locations: []config.LocationConfig{{
				Match: config.MatchConfig{Type: "prefix", Path: "/"},
				Root:  "/srv",
				WAF:   &config.WAFConfig{Enabled: enabled},
			}},
		}},
	}
}

func TestEnabledDetectsGlobalAndLocation(t *testing.T) {
	if Enabled(cfgWithGlobalWAF(false)) {
		t.Error("disabled global WAF should report not enabled")
	}
	if !Enabled(cfgWithGlobalWAF(true)) {
		t.Error("enabled global WAF should report enabled")
	}
	if Enabled(cfgWithLocationWAF(false)) {
		t.Error("disabled location WAF should report not enabled")
	}
	if !Enabled(cfgWithLocationWAF(true)) {
		t.Error("enabled location WAF should report enabled")
	}
}

func TestCheckMatchesCompiled(t *testing.T) {
	// In a lean build (Compiled == false) Check must reject an enabled WAF; in a
	// waf build it must accept it. A disabled WAF is always accepted.
	err := Check(cfgWithGlobalWAF(true))
	if Compiled && err != nil {
		t.Errorf("waf build should accept an enabled WAF, got: %v", err)
	}
	if !Compiled && err == nil {
		t.Error("lean build should reject an enabled WAF")
	}
	if err := Check(cfgWithGlobalWAF(false)); err != nil {
		t.Errorf("a disabled WAF must always be accepted, got: %v", err)
	}
}
