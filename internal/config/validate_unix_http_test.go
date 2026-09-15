// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

func TestValidateUnixBackendsRejectsEmptyPath(t *testing.T) {
	errs := validateUnixBackends(UpstreamConfig{Servers: []UpstreamServer{{Address: "unix:"}}}, "upstreams[0]")
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "path is empty") {
		t.Fatalf("errors = %v", errs)
	}
}

func TestValidateUnixBackendsRejectsBackendTLS(t *testing.T) {
	errs := validateUnixBackends(UpstreamConfig{Servers: []UpstreamServer{{Address: "unix:/run/app.sock"}}, BackendTLS: &BackendTLSConfig{}}, "upstreams[0]")
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "plaintext only") {
		t.Fatalf("errors = %v", errs)
	}
}

func TestValidateUnixBackendsRejectsHTTPHealth(t *testing.T) {
	errs := validateUnixBackends(UpstreamConfig{Servers: []UpstreamServer{{Address: "unix:/run/app.sock"}}, HealthCheck: &HealthCheckConfig{Enabled: true, Type: "http"}}, "upstreams[0]")
	if len(errs) == 0 {
		t.Fatalf("errors = %v", errs)
	}
}

func TestValidateUnixBackendsAllowsConnectHealth(t *testing.T) {
	errs := validateUnixBackends(UpstreamConfig{Servers: []UpstreamServer{{Address: "unix:/run/app.sock"}}, HealthCheck: &HealthCheckConfig{Enabled: true, Type: "tcp"}}, "upstreams[0]")
	if len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
}
