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

// A history snapshot is still a configuration mutation: an operator with
// history:rollback cannot restore an older, wider trusted-proxy policy without
// config:trust. Both console rollback routes share this guard.
func TestRollbackClientAddressRequiresTrustPermission(t *testing.T) {
	cfg := wave1Config(t)
	s, adminTok, opTok, _ := wave1Server(t, cfg)
	writes := 0
	s.deps.WriteConfigRaw = func([]byte) error { writes++; return nil }
	previous := *cfg
	previous.Servers = append([]config.ServerConfig(nil), cfg.Servers...)
	previous.Servers[0].ClientAddress = &config.ClientAddressConfig{TrustedProxies: []string{"0.0.0.0/0"}}
	raw, err := config.Marshal(&previous)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.hist.snapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/history/rollback", "/api/config/rollback"} {
		for _, tc := range []struct {
			name  string
			token string
			want  int
		}{
			{name: "operator", token: opTok, want: http.StatusForbidden},
			{name: "admin", token: adminTok, want: http.StatusOK},
		} {
			t.Run(tc.name+path, func(t *testing.T) {
				before := writes
				body := []byte(`{"id":"` + id + `"}`)
				req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+tc.token)
				rr := httptest.NewRecorder()
				s.routes().ServeHTTP(rr, req)
				if rr.Code != tc.want {
					t.Fatalf("status %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
				}
				if tc.want == http.StatusForbidden && !strings.Contains(rr.Body.String(), "config:trust") {
					t.Errorf("denial omitted config:trust: %s", rr.Body.String())
				}
				if tc.want == http.StatusForbidden && writes != before {
					t.Errorf("unauthorized rollback wrote config")
				}
			})
		}
	}
}

// Enabling PROXY protocol lets an allowed transport peer assert the client
// address even when HTTP forwarding headers are explicitly disabled. This is
// an identity trust transition despite an unchanged client_address block.
func TestInboundProxyProtocolRequiresTrustPermission(t *testing.T) {
	cfg := wave1Config(t)
	cfg.Servers[0].ClientAddress = &config.ClientAddressConfig{
		TrustedProxies:   []string{"127.0.0.1/32"},
		ForwardedHeaders: []string{},
	}
	candidate := *cfg
	candidate.Servers = append([]config.ServerConfig(nil), cfg.Servers...)
	candidate.Servers[0].ProxyProtocol = "in"
	raw, err := config.Marshal(&candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(&candidate); err != nil {
		t.Fatalf("candidate validation: %v", err)
	}
	for _, tc := range []struct {
		name  string
		admin bool
		want  int
	}{
		{name: "operator", want: http.StatusForbidden},
		{name: "admin", admin: true, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, adminTok, opTok, _ := wave1Server(t, cfg)
			var writes int
			s.deps.WriteConfigRaw = func([]byte) error { writes++; return nil }
			token := opTok
			if tc.admin {
				token = adminTok
			}
			req := httptest.NewRequest(http.MethodPost, "/api/config/apply?mode=stage_restart", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			s.routes().ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
			if !tc.admin && (!strings.Contains(rr.Body.String(), "config:trust") || writes != 0) {
				t.Fatalf("unauthorized identity transition: writes %d, body %s", writes, rr.Body.String())
			}
			if tc.admin && writes != 1 {
				t.Fatalf("authorized identity transition wrote %d times, want 1", writes)
			}
		})
	}
}

func TestRollbackInboundProxyProtocolRequiresTrustPermission(t *testing.T) {
	cfg := wave1Config(t)
	cfg.Servers[0].ClientAddress = &config.ClientAddressConfig{
		TrustedProxies: []string{"127.0.0.1/32"}, ForwardedHeaders: []string{},
	}
	s, adminTok, opTok, _ := wave1Server(t, cfg)
	var writes int
	s.deps.WriteConfigRaw = func([]byte) error { writes++; return nil }
	candidate := *cfg
	candidate.Servers = append([]config.ServerConfig(nil), cfg.Servers...)
	candidate.Servers[0].ProxyProtocol = "in"
	raw, err := config.Marshal(&candidate)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.hist.snapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/history/rollback", "/api/config/rollback"} {
		for _, tc := range []struct {
			name  string
			token string
			want  int
		}{
			{name: "operator", token: opTok, want: http.StatusForbidden},
			{name: "admin", token: adminTok, want: http.StatusOK},
		} {
			t.Run(tc.name+path, func(t *testing.T) {
				before := writes
				req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{"id":"`+id+`"}`)))
				req.Header.Set("Authorization", "Bearer "+tc.token)
				rr := httptest.NewRecorder()
				s.routes().ServeHTTP(rr, req)
				if rr.Code != tc.want {
					t.Fatalf("status %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
				}
				if tc.want == http.StatusForbidden && (writes != before || !strings.Contains(rr.Body.String(), "config:trust")) {
					t.Fatalf("unauthorized rollback: writes %d, body %s", writes-before, rr.Body.String())
				}
			})
		}
	}
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
