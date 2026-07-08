// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// hostPortOf strips the scheme from an httptest URL, yielding the host:port that
// the [admin] listen field or the -addr flag would carry.
func hostPortOf(rawURL string) string {
	return strings.TrimPrefix(rawURL, "http://")
}

// pathProbe is an httptest handler that records the last requested path (under a
// mutex, so the test goroutine can read it after the probe returns) and replies
// with a fixed status.
type pathProbe struct {
	mu     sync.Mutex
	last   string
	status int
}

func (p *pathProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.last = r.URL.Path
	p.mu.Unlock()
	w.WriteHeader(p.status)
}

func (p *pathProbe) path() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

func TestCmdHealthcheckHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	code, out, _ := capture(t, func() int { return cmdHealthcheck([]string{"-url", srv.URL + "/healthz"}) })
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "healthy:") {
		t.Errorf("stdout = %q, want a healthy line", out)
	}
}

func TestCmdHealthcheckUnhealthyStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	code, _, errOut := capture(t, func() int { return cmdHealthcheck([]string{"-url", srv.URL + "/readyz"}) })
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, "503") {
		t.Errorf("stderr = %q, want the 503 status", errOut)
	}
}

func TestCmdHealthcheckUnreachable(t *testing.T) {
	// Start then immediately stop a server so its port is free: the probe should
	// fail to connect and report unhealthy (exit 1), not a usage error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	code, _, errOut := capture(t, func() int { return cmdHealthcheck([]string{"-url", url + "/healthz", "-timeout", "500ms"}) })
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, "unreachable") {
		t.Errorf("stderr = %q, want an unreachable error", errOut)
	}
}

func TestCmdHealthcheckTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // block until the test releases it, forcing the client timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(release); srv.Close() }()

	code, _, errOut := capture(t, func() int { return cmdHealthcheck([]string{"-url", srv.URL, "-timeout", "20ms"}) })
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, "unreachable") {
		t.Errorf("stderr = %q, want a timeout/unreachable error", errOut)
	}
}

func TestCmdHealthcheckConfigDiscovery(t *testing.T) {
	probe := &pathProbe{status: http.StatusOK}
	srv := httptest.NewServer(probe)
	defer srv.Close()

	cfg := "[admin]\nenabled = true\nlisten = \"" + hostPortOf(srv.URL) + "\"\n"
	path := writeTemp(t, cfg)

	// Default probes liveness (/healthz).
	if code, _, errOut := capture(t, func() int { return cmdHealthcheck([]string{"-config", path}) }); code != 0 {
		t.Fatalf("liveness exit = %d, want 0 (%s)", code, errOut)
	}
	if got := probe.path(); got != "/healthz" {
		t.Errorf("probed path = %q, want /healthz", got)
	}

	// -ready switches to readiness (/readyz).
	if code, _, errOut := capture(t, func() int { return cmdHealthcheck([]string{"-config", path, "-ready"}) }); code != 0 {
		t.Fatalf("readiness exit = %d, want 0 (%s)", code, errOut)
	}
	if got := probe.path(); got != "/readyz" {
		t.Errorf("probed path = %q, want /readyz", got)
	}
}

func TestCmdHealthcheckAddrOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	code, _, _ := capture(t, func() int { return cmdHealthcheck([]string{"-addr", hostPortOf(srv.URL)}) })
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestCmdHealthcheckConfigMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.toml")
	code, _, _ := capture(t, func() int { return cmdHealthcheck([]string{"-config", missing}) })
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (config error)", code)
	}
}

func TestCmdHealthcheckAdminDisabled(t *testing.T) {
	path := writeTemp(t, validConfig) // no [admin] block: admin is disabled
	code, _, errOut := capture(t, func() int { return cmdHealthcheck([]string{"-config", path}) })
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (admin disabled)", code)
	}
	if !strings.Contains(errOut, "admin listener is not enabled") {
		t.Errorf("stderr = %q, want an admin-disabled message", errOut)
	}
}

func TestCmdHealthcheckBadFlag(t *testing.T) {
	code, _, _ := capture(t, func() int { return cmdHealthcheck([]string{"-nope"}) })
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage error)", code)
	}
}

func TestCmdHealthcheckJSON(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		code, out, _ := capture(t, func() int { return cmdHealthcheck([]string{"-url", srv.URL, "-json"}) })
		if code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		var got healthcheckOutput
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, out)
		}
		if !got.OK || got.Status != http.StatusOK {
			t.Errorf("json = %+v, want ok/200", got)
		}
	})

	t.Run("unhealthy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		code, out, _ := capture(t, func() int { return cmdHealthcheck([]string{"-url", srv.URL, "-json"}) })
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		var got healthcheckOutput
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, out)
		}
		if got.OK || got.Status != http.StatusServiceUnavailable || got.Error == "" {
			t.Errorf("json = %+v, want not-ok/503 with an error", got)
		}
	})
}

func TestCmdHealthcheckQuiet(t *testing.T) {
	t.Run("healthy is silent", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		code, out, errOut := capture(t, func() int { return cmdHealthcheck([]string{"-url", srv.URL, "-quiet"}) })
		if code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		if out != "" || errOut != "" {
			t.Errorf("quiet produced output: stdout=%q stderr=%q", out, errOut)
		}
	})

	t.Run("unhealthy is silent", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		code, out, errOut := capture(t, func() int { return cmdHealthcheck([]string{"-url", srv.URL, "-quiet"}) })
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if out != "" || errOut != "" {
			t.Errorf("quiet produced output: stdout=%q stderr=%q", out, errOut)
		}
	})
}

func TestProbeHostPort(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:9090": "127.0.0.1:9090",
		"0.0.0.0:9090":   "127.0.0.1:9090",
		":9090":          "127.0.0.1:9090",
		"[::]:9090":      "[::1]:9090",
		"localhost:9090": "localhost:9090",
		"not-host-port":  "not-host-port",
	}
	for in, want := range cases {
		if got := probeHostPort(in); got != want {
			t.Errorf("probeHostPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCmdHealthcheckDispatch(t *testing.T) {
	// The verb must route through dispatchSubcommand (a bad flag returns quickly
	// with exit 2 without hanging on a network probe).
	handled, code := dispatchSubcommand([]string{"healthcheck", "-nope"})
	if !handled {
		t.Fatalf("dispatchSubcommand(healthcheck) handled = false, want true")
	}
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}
