// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

// clientAddressServer builds an RBAC-enabled server whose editable config has
// two virtual hosts sharing one listener, plus a second listener.
func clientAddressServer(t *testing.T) (*Server, string, string, string) {
	t.Helper()
	cfg, err := config.Parse([]byte(`
[global]
log_level = "info"

[admin]
enabled = true
listen = "127.0.0.1:8080"
token = "admin-token-32-chars-padded--"

[[servers]]
listen = "127.0.0.1:8081"
server_names = ["public.example.com"]

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204

[[servers]]
listen = "127.0.0.1:8081"
server_names = ["internal.example.com"]

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204

[[servers]]
listen = "127.0.0.1:8082"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204
`))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return wave1Server(t, cfg)
}

func patchClientAddress(t *testing.T, s *Server, token, addr, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/listeners/"+addr+"/client_address", bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	return rr
}

// TestListenerClientAddressWritesEverySiblingBlock proves the listener
// granularity: one PATCH updates every server block on the address, which is
// the only shape configuration validation accepts.
func TestListenerClientAddressWritesEverySiblingBlock(t *testing.T) {
	s, adminTok, _, _ := clientAddressServer(t)
	var saved *config.Config
	s.deps.SaveConfig = func(c *config.Config) error { saved = c; return nil }
	s.deps.WriteConfigRaw = func(data []byte) error {
		c, err := config.Parse(data)
		if err != nil {
			return err
		}
		saved = c
		return nil
	}

	rr := patchClientAddress(t, s, adminTok, "127.0.0.1:8081",
		`{"client_address":{"trusted_proxies":["10.0.0.0/8"],"forwarded_headers":["x-forwarded-for"],"max_hops":4}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if saved == nil {
		t.Fatal("no configuration was written")
	}

	var written int
	for _, srv := range saved.Servers {
		if srv.Listen != "127.0.0.1:8081" {
			if srv.ClientAddress != nil {
				t.Errorf("listener %s gained a policy it never asked for", srv.Listen)
			}
			continue
		}
		written++
		if srv.ClientAddress == nil {
			t.Fatalf("server %s was not written", strings.Join(srv.ServerNames, ","))
		}
		if got := srv.ClientAddress.TrustedProxies; len(got) != 1 || got[0] != "10.0.0.0/8" {
			t.Errorf("trusted_proxies = %v", got)
		}
		if got := srv.ClientAddress.ForwardedHeaders; len(got) != 1 || got[0] != "x-forwarded-for" {
			t.Errorf("forwarded_headers = %v", got)
		}
		if srv.ClientAddress.MaxHops != 4 {
			t.Errorf("max_hops = %d, want 4", srv.ClientAddress.MaxHops)
		}
	}
	if written != 2 {
		t.Fatalf("wrote %d blocks on the address, want 2", written)
	}
	if err := config.Validate(saved); err != nil {
		t.Fatalf("written configuration does not validate: %v", err)
	}
}

// TestListenerClientAddressRequiresTrustPermission pins the dedicated grant:
// an operator may apply ordinary configuration but not a trust change, on the
// dedicated route and through the generic patch surface alike.
func TestListenerClientAddressRequiresTrustPermission(t *testing.T) {
	t.Run("dedicated route", func(t *testing.T) {
		s, _, opTok, viewTok := clientAddressServer(t)
		for _, tc := range []struct {
			name  string
			token string
		}{
			{name: "operator", token: opTok},
			{name: "viewer", token: viewTok},
		} {
			rr := patchClientAddress(t, s, tc.token, "127.0.0.1:8081", `{"client_address":{"trusted_proxies":["10.0.0.0/8"]}}`)
			if rr.Code != http.StatusForbidden {
				t.Errorf("%s got %d, want 403: %s", tc.name, rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "config:trust") {
				t.Errorf("%s response does not name the required permission: %s", tc.name, rr.Body.String())
			}
		}
	})

	// Gating only the dedicated route would be theatre: the same change is
	// expressible as a generic patch op, so the check is on the effective diff.
	t.Run("generic patch surface", func(t *testing.T) {
		s, _, opTok, _ := clientAddressServer(t)
		body := `{"ops":[{"op":"listener_set_client_address","listen":"127.0.0.1:8081","client_address":{"trusted_proxies":["10.0.0.0/8"]}}]}`
		req := httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+opTok)
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("operator patched a trust policy: status %d, body %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "config:trust") {
			t.Fatalf("response does not name the required permission: %s", rr.Body.String())
		}
	})
}

// TestListenerClientAddressRejectsInvalidPolicy proves the whole patch is
// rejected rather than partially written.
func TestListenerClientAddressRejectsInvalidPolicy(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "host bits set", body: `{"client_address":{"trusted_proxies":["10.1.2.3/8"]}}`, want: "host bits"},
		{name: "hostname", body: `{"client_address":{"trusted_proxies":["proxy.example.com"]}}`, want: "trusted_proxies"},
		{name: "unknown header", body: `{"client_address":{"trusted_proxies":["10.0.0.0/8"],"forwarded_headers":["x-real-ip"]}}`, want: "forwarded_headers"},
		{name: "max hops out of range", body: `{"client_address":{"trusted_proxies":["10.0.0.0/8"],"max_hops":9000}}`, want: "max_hops"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, adminTok, _, _ := clientAddressServer(t)
			var wrote bool
			s.deps.WriteConfigRaw = func([]byte) error { wrote = true; return nil }
			s.deps.SaveConfig = func(*config.Config) error { wrote = true; return nil }

			rr := patchClientAddress(t, s, adminTok, "127.0.0.1:8081", tt.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tt.want) {
				t.Errorf("error does not mention %q: %s", tt.want, rr.Body.String())
			}
			if wrote {
				t.Error("an invalid policy reached persistence")
			}
		})
	}
}

func TestListenerClientAddressUnknownListener(t *testing.T) {
	s, adminTok, _, _ := clientAddressServer(t)
	rr := patchClientAddress(t, s, adminTok, "127.0.0.1:9999", `{"client_address":{"trusted_proxies":["10.0.0.0/8"]}}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
}

// TestListenerClientAddressClearsPolicy proves a null payload returns the
// listener to peer-only identity on every block.
func TestListenerClientAddressClearsPolicy(t *testing.T) {
	s, adminTok, _, _ := clientAddressServer(t)
	var saved *config.Config
	s.deps.WriteConfigRaw = func(data []byte) error {
		c, err := config.Parse(data)
		if err != nil {
			return err
		}
		saved = c
		return nil
	}

	if rr := patchClientAddress(t, s, adminTok, "127.0.0.1:8081", `{"client_address":{"trusted_proxies":["10.0.0.0/8"]}}`); rr.Code != http.StatusOK {
		t.Fatalf("seed status = %d: %s", rr.Code, rr.Body.String())
	}
	if rr := patchClientAddress(t, s, adminTok, "127.0.0.1:8081", `{"client_address":null}`); rr.Code != http.StatusOK {
		t.Fatalf("clear status = %d: %s", rr.Code, rr.Body.String())
	}
	for _, srv := range saved.Servers {
		if srv.ClientAddress != nil {
			t.Fatalf("listener %s kept a policy after clearing: %+v", srv.Listen, srv.ClientAddress)
		}
	}
}

// TestListenerClientAddressAuditCategory pins the distinct audit action, so a
// trust-boundary change is greppable without reading every config.patch entry.
func TestListenerClientAddressAuditCategory(t *testing.T) {
	s, adminTok, _, _ := clientAddressServer(t)
	s.audit = newAuditLog(64)
	if rr := patchClientAddress(t, s, adminTok, "127.0.0.1:8081", `{"client_address":{"trusted_proxies":["10.0.0.0/8"]}}`); rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var found bool
	for _, e := range s.audit.snapshot("", "", 100) {
		if e.Operation == auditActionClientAddress {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s audit entry was recorded", auditActionClientAddress)
	}
}

// TestListenerClientAddressProjection covers the read side, including the
// omitted-versus-empty distinction that a naive projection would lose.
func TestListenerClientAddressProjection(t *testing.T) {
	tests := []struct {
		name            string
		policy          *config.ClientAddressConfig
		wantConfigured  bool
		wantHeaders     []string
		wantDisabled    bool
		wantMaxHops     int
		wantOpenTrust   bool
		wantTrustedList []string
	}{
		{
			name:            "no policy shows the defaults",
			wantHeaders:     []string{"x-forwarded-for"},
			wantMaxHops:     16,
			wantTrustedList: []string{},
		},
		{
			name:            "omitted headers keep the default preference",
			policy:          &config.ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}},
			wantConfigured:  true,
			wantHeaders:     []string{"x-forwarded-for"},
			wantMaxHops:     16,
			wantTrustedList: []string{"10.0.0.0/8"},
		},
		{
			name:            "explicitly empty headers are reported as disabled",
			policy:          &config.ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{}},
			wantConfigured:  true,
			wantHeaders:     []string{},
			wantDisabled:    true,
			wantMaxHops:     16,
			wantTrustedList: []string{"10.0.0.0/8"},
		},
		{
			name:            "a range covering everything is flagged",
			policy:          &config.ClientAddressConfig{TrustedProxies: []string{"0.0.0.0/0"}, MaxHops: 3},
			wantConfigured:  true,
			wantHeaders:     []string{"x-forwarded-for"},
			wantMaxHops:     3,
			wantOpenTrust:   true,
			wantTrustedList: []string{"0.0.0.0/0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Servers: []config.ServerConfig{
				{Listen: ":8443", ServerNames: []string{"a"}, ClientAddress: tt.policy},
				{Listen: ":8443", ServerNames: []string{"b"}, ClientAddress: tt.policy},
			}}
			view, ok := projectListenerClientAddress(cfg, ":8443")
			if !ok {
				t.Fatal("listener not found")
			}
			if view.ServerBlocks != 2 {
				t.Errorf("server_blocks = %d, want 2", view.ServerBlocks)
			}
			if view.Configured != tt.wantConfigured {
				t.Errorf("configured = %v, want %v", view.Configured, tt.wantConfigured)
			}
			if strings.Join(view.ForwardedHeaders, ",") != strings.Join(tt.wantHeaders, ",") {
				t.Errorf("forwarded_headers = %v, want %v", view.ForwardedHeaders, tt.wantHeaders)
			}
			if view.HeadersDisabled != tt.wantDisabled {
				t.Errorf("headers_disabled = %v, want %v", view.HeadersDisabled, tt.wantDisabled)
			}
			if view.MaxHops != tt.wantMaxHops {
				t.Errorf("max_hops = %d, want %d", view.MaxHops, tt.wantMaxHops)
			}
			if view.TrustsEveryClient != tt.wantOpenTrust {
				t.Errorf("trusts_every_client = %v, want %v", view.TrustsEveryClient, tt.wantOpenTrust)
			}
			if strings.Join(view.TrustedProxies, ",") != strings.Join(tt.wantTrustedList, ",") {
				t.Errorf("trusted_proxies = %v, want %v", view.TrustedProxies, tt.wantTrustedList)
			}
		})
	}
}

func TestListenersListEndpoint(t *testing.T) {
	s, adminTok, _, _ := clientAddressServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/listeners", nil)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var got []ListenerClientAddress
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listeners, want 2 distinct addresses: %+v", len(got), got)
	}
	if got[0].Listen != "127.0.0.1:8081" || got[0].ServerBlocks != 2 {
		t.Errorf("first listener = %+v", got[0])
	}
	if got[1].Listen != "127.0.0.1:8082" || got[1].ServerBlocks != 1 {
		t.Errorf("second listener = %+v", got[1])
	}
}

// TestListenerClientAddressRoundTrip proves the Console's read-modify-read
// cycle: the projection reflects what was written, including the
// explicitly-empty header list that a naive round trip would lose.
func TestListenerClientAddressRoundTrip(t *testing.T) {
	s, adminTok, _, _ := clientAddressServer(t)
	var current *config.Config
	s.deps.WriteConfigRaw = func(data []byte) error {
		c, err := config.Parse(data)
		if err != nil {
			return err
		}
		current = c
		s.deps.LoadConfig = func() (*config.Config, error) { return current, nil }
		s.deps.ReadConfigRaw = func() ([]byte, error) { return config.Marshal(current) }
		return nil
	}

	read := func() ListenerClientAddress {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/listeners/127.0.0.1:8081/client_address", nil)
		req.Header.Set("Authorization", "Bearer "+adminTok)
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("read status = %d: %s", rr.Code, rr.Body.String())
		}
		var view ListenerClientAddress
		if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return view
	}

	if view := read(); view.Configured || view.ServerBlocks != 2 {
		t.Fatalf("initial view = %+v, want two blocks and no policy", view)
	}

	if rr := patchClientAddress(t, s, adminTok, "127.0.0.1:8081",
		`{"client_address":{"trusted_proxies":["10.0.0.0/8"],"forwarded_headers":[],"max_hops":4}}`); rr.Code != http.StatusOK {
		t.Fatalf("write status = %d: %s", rr.Code, rr.Body.String())
	}

	view := read()
	if !view.Configured {
		t.Fatal("policy was not read back")
	}
	if len(view.TrustedProxies) != 1 || view.TrustedProxies[0] != "10.0.0.0/8" {
		t.Errorf("trusted_proxies = %v", view.TrustedProxies)
	}
	if !view.HeadersDisabled || len(view.ForwardedHeaders) != 0 {
		t.Errorf("an explicitly empty header list did not survive the round trip: %+v", view)
	}
	if view.MaxHops != 4 {
		t.Errorf("max_hops = %d, want 4", view.MaxHops)
	}
	if view.ServerBlocks != 2 {
		t.Errorf("server_blocks = %d, want 2", view.ServerBlocks)
	}
}
