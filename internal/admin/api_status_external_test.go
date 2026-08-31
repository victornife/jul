// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

// statusRows fetches the Console Status overview for a configuration.
func statusRows(t *testing.T, c *config.Config) map[string]FeatureStatus {
	t.Helper()
	s := newTestServer(t, config.AdminConfig{}, Deps{
		Metrics:    http.NewServeMux(),
		LoadConfig: func() (*config.Config, error) { return c, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var rows []FeatureStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := make(map[string]FeatureStatus, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

// TestStatusReportsAdminTransportPosture is the Console surface for
// ADR 0019 §28.1. The reason it earns a row of its own is that the gate is the
// only breaking change in this contract, and the deployment it breaks — a
// non-loopback listener speaking cleartext — is exactly the one whose operator
// needs to be told before a scrape or a pipeline starts failing.
func TestStatusReportsAdminTransportPosture(t *testing.T) {
	loopback := statusRows(t, &config.Config{Admin: config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090"}})
	row, ok := loopback["Admin transport security"]
	if !ok {
		t.Fatal("the Status overview has no admin transport row")
	}
	if !row.Active {
		t.Fatal("the row is inactive for an enabled admin listener")
	}
	if !strings.Contains(row.Detail, "loopback") {
		t.Fatalf("a loopback listener reports %q", row.Detail)
	}

	exposed := statusRows(t, &config.Config{Admin: config.AdminConfig{Enabled: true, Listen: "0.0.0.0:9090"}})
	row = exposed["Admin transport security"]
	if !strings.Contains(row.Detail, "requires TLS") {
		t.Fatalf("a non-loopback listener must say TLS is required, got %q", row.Detail)
	}
	if !strings.Contains(row.Detail, "/metrics") {
		t.Fatalf("a non-loopback listener must name /metrics, the consequence most likely to be found in production: %q", row.Detail)
	}

	// Bounded metadata only, matching the rule the trusted-proxy and
	// backend-TLS rows already follow: the panel reports the posture, never
	// the address.
	for _, rows := range []map[string]FeatureStatus{loopback, exposed} {
		for _, addr := range []string{"127.0.0.1:9090", "0.0.0.0:9090"} {
			if strings.Contains(rows["Admin transport security"].Detail, addr) {
				t.Fatalf("the status detail projects the listen address: %q", rows["Admin transport security"].Detail)
			}
		}
	}
}

// TestStatusReportsExternalAPISurface gives "is this endpoint stable?" an
// answer an operator can reach from the Console rather than only from the
// generated document.
func TestStatusReportsExternalAPISurface(t *testing.T) {
	rows := statusRows(t, &config.Config{Admin: config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090"}})
	row, ok := rows["Supported external admin API"]
	if !ok {
		t.Fatal("the Status overview has no external API row")
	}
	if !row.Active {
		t.Fatal("the row is inactive for an enabled admin listener")
	}
	if !strings.Contains(row.Detail, apiVersionNamespace) {
		t.Fatalf("the detail does not name the supported namespace: %q", row.Detail)
	}
	if !strings.Contains(row.Detail, "internal") {
		t.Fatalf("the detail does not say the rest of the surface is internal: %q", row.Detail)
	}

	// The counts are derived from the catalog, so they cannot drift from the
	// classification the guard tests enforce.
	if externalOperationCount != len(ExternalRoutes()) {
		t.Fatalf("external operation count %d does not match ExternalRoutes() %d", externalOperationCount, len(ExternalRoutes()))
	}
	if externalOperationCount == 0 || internalRouteCount == 0 {
		t.Fatalf("counts were not populated: external=%d internal=%d", externalOperationCount, internalRouteCount)
	}
	if got := internalRouteCount; got != len(internalRouteReasons) {
		t.Fatalf("internal route count %d does not match the classification inventory %d", got, len(internalRouteReasons))
	}
}

// TestStatusMetricsRowNamesTheTransportRequirement: an operator whose
// Prometheus scrape stopped working should be able to see why from the panel
// rather than from a 403 buried in a Prometheus log.
func TestStatusMetricsRowNamesTheTransportRequirement(t *testing.T) {
	rows := statusRows(t, &config.Config{Admin: config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090"}})
	detail := rows["Prometheus metrics"].Detail
	if !strings.Contains(detail, "metrics:read") {
		t.Errorf("the metrics row does not say the endpoint is authenticated: %q", detail)
	}
	if !strings.Contains(detail, "loopback or TLS") {
		t.Errorf("the metrics row does not state the transport requirement: %q", detail)
	}
}

// TestStatusAdminRowsAreInactiveWhenAdminIsDisabled: a capability that is not
// running must not claim to be.
func TestStatusAdminRowsAreInactiveWhenAdminIsDisabled(t *testing.T) {
	rows := statusRows(t, &config.Config{})
	for _, name := range []string{"Admin transport security", "Supported external admin API"} {
		row, ok := rows[name]
		if !ok {
			t.Fatalf("the Status overview has no %q row", name)
		}
		if row.Active {
			t.Errorf("%q is active with the admin listener disabled", name)
		}
		if row.Detail != "" {
			t.Errorf("%q reports a detail with the admin listener disabled: %q", name, row.Detail)
		}
	}
}
