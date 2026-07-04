package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseError(t *testing.T) {
	_, err := Parse([]byte("not valid toml = ["))
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
	if !strings.Contains(err.Error(), "parse config:") {
		t.Errorf("expected error starting with 'parse config:', got: %v", err)
	}
}

func TestTOMLSourceLoad(t *testing.T) {
	t.Run("file not found", func(t *testing.T) {
		src := NewTOMLSource(filepath.Join(t.TempDir(), "missing.toml"))
		_, err := src.Load()
		if err == nil {
			t.Fatal("expected error for missing file")
		}
		if err.Error() != `read config "`+src.Name()+`": The system cannot find the file specified.` {
			t.Logf("expected error contains Windows message, got: %v", err)
		}
	})

	t.Run("load and parse round-trip", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.toml")
		data := []byte("[global]\nlog_level = \"debug\"\n\n[[servers]]\nlisten = \"127.0.0.1:8080\"\n")
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		src := NewTOMLSource(path)
		if src.Name() != path {
			t.Errorf("Name() = %q, want %q", src.Name(), path)
		}
		cfg, err := src.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Global.LogLevel != "debug" {
			t.Errorf("log_level = %q, want debug", cfg.Global.LogLevel)
		}
		if len(cfg.Servers) != 1 {
			t.Fatalf("expected 1 server, got %d", len(cfg.Servers))
		}
		if cfg.Servers[0].Listen != "127.0.0.1:8080" {
			t.Errorf("listen = %q", cfg.Servers[0].Listen)
		}
	})
}

func TestMarshalRoundTripErrors(t *testing.T) {
	// This baseline test ensures Marshal encodes effectively.
	cfg := &Config{
		Global: GlobalConfig{
			LogLevel:        "info",
			LogFormat:       "text",
			ShutdownTimeout: Duration(45 * time.Second),
		},
		Servers: []ServerConfig{{
			Listen:            "127.0.0.1:8080",
			ClientMaxBodySize: Size(1 << 20),
			MaxHeaderBytes:    Size(1 << 10),
			ReadHeaderTimeout: Duration(10 * time.Second),
			IdleTimeout:       Duration(120 * time.Second),
		}},
	}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Marshal produced empty output")
	}
	// Should re-parse successfully.
	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if parsed.Global.ShutdownTimeout.Std() != 45*time.Second {
		t.Errorf("round-trip shutdown_timeout: got %v", parsed.Global.ShutdownTimeout.Std())
	}
}

func TestApplyDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
[[servers]]
listen = "127.0.0.1:8080"

[[upstreams]]
name = "app"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if cfg.Global.LogLevel != "info" {
		t.Errorf("default log_level = %q, want info", cfg.Global.LogLevel)
	}
	if cfg.Global.LogFormat != "text" {
		t.Errorf("default log_format = %q, want text", cfg.Global.LogFormat)
	}
	if cfg.Global.ShutdownTimeout.Std() != 30*time.Second {
		t.Errorf("default shutdown_timeout = %v, want 30s", cfg.Global.ShutdownTimeout.Std())
	}
	if cfg.Global.ReloadTimeout.Std() != 10*time.Second {
		t.Errorf("default reload_timeout = %v, want 10s", cfg.Global.ReloadTimeout.Std())
	}

	srv := cfg.Servers[0]
	if srv.ReadHeaderTimeout.Std() != 10*time.Second {
		t.Errorf("default read_header_timeout = %v", srv.ReadHeaderTimeout.Std())
	}
	if srv.IdleTimeout.Std() != 60*time.Second {
		t.Errorf("default idle_timeout = %v", srv.IdleTimeout.Std())
	}
	if srv.ClientMaxBodySize.Bytes() != 1<<20 {
		t.Errorf("default client_max_body_size = %d", srv.ClientMaxBodySize.Bytes())
	}
	if srv.MaxHeaderBytes.Bytes() != 1<<20 {
		t.Errorf("default max_header_bytes = %d", srv.MaxHeaderBytes.Bytes())
	}

	// Upstream defaults
	up := cfg.Upstreams[0]
	if up.Strategy != "round_robin" {
		t.Errorf("default strategy = %q, want round_robin", up.Strategy)
	}
	if up.MaxFails != 3 {
		t.Errorf("default max_fails = %d, want 3", up.MaxFails)
	}
	if up.FailTimeout.Std() != 10*time.Second {
		t.Errorf("default fail_timeout = %v", up.FailTimeout.Std())
	}
}

func TestConfigClone(t *testing.T) {
	cfg, _ := Parse([]byte("[global]\nlog_level = \"debug\"\n\n[[servers]]\nlisten=\":80\"\n"))
	clone, err := cfg.Clone()
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if clone.Global.LogLevel != "debug" {
		t.Errorf("clone log_level = %q", clone.Global.LogLevel)
	}
	// Mutate clone; original should be unaffected.
	clone.Global.LogLevel = "error"
	if cfg.Global.LogLevel != "debug" {
		t.Error("original was mutated")
	}
}


func TestParseReloadTimeoutZeroDefaultsToTenSeconds(t *testing.T) {
	// Explicit zero in TOML must still default to 10s (unbounded reload is
	// intentionally not supported to prevent accidental production stalls).
	cfg, err := Parse([]byte(`[global]
reload_timeout = "0s"

[[servers]]
listen = "127.0.0.1:8080"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := Duration(10 * time.Second)
	if cfg.Global.ReloadTimeout != want {
		t.Fatalf("reload_timeout = %v, want %v (zero/omitted must default to 10s)", cfg.Global.ReloadTimeout, want)
	}
}
func TestPreflightClone(t *testing.T) {
	cfg, _ := Parse([]byte("[global]\nlog_level = \"info\"\n\n[[servers]]\nlisten=\":80\"\n"))
	if err := PreflightClone(cfg); err != nil {
		t.Fatalf("PreflightClone: %v", err)
	}
}
