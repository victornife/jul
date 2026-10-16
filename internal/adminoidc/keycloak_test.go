// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminoidc

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"jul/internal/config"
)

// TestKeycloakCodePKCE is required by the dedicated pinned-provider CI lane.
// Normal unit tests remain offline; REQUIRE prevents a missing fixture from
// silently passing that lane as a skipped test.
func TestKeycloakCodePKCE(t *testing.T) {
	issuer := os.Getenv("JUL_KEYCLOAK_ISSUER")
	if issuer == "" {
		if os.Getenv("JUL_REQUIRE_KEYCLOAK") == "1" {
			t.Fatal("Keycloak fixture is required")
		}
		t.Skip("run scripts/admin-oidc-keycloak.sh")
	}
	for _, tt := range []struct {
		name, user string
		subject    bool
		accepted   bool
	}{{"subject", "alice", true, true}, {"group", "alice", false, true}, {"unknown", "bob", false, false}} {
		t.Run(tt.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:9298")
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.AdminOIDCConfig{Enabled: true, Issuer: issuer, ClientID: "jul-console", CallbackURL: "http://127.0.0.1:9298/auth/oidc/callback", GroupClaim: "groups", Groups: []config.AdminOIDCGroup{{Group: "operators", Role: "operator"}}}
			if tt.subject {
				cfg.Subjects = []config.AdminOIDCSubject{{Subject: "11111111-1111-4111-8111-111111111111", Principal: "alice", Role: "viewer"}}
			}
			tr := http.DefaultTransport.(*http.Transport).Clone()
			runtime, err := New(cfg, policy(t), &http.Client{Transport: tr})
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth/oidc/login":
					runtime.Login(w, r)
				case "/auth/oidc/callback":
					runtime.Callback(w, r)
				case "/auth/oidc/logout":
					runtime.Logout(w, r)
				default:
					id, _, err := runtime.Authenticate(r)
					if err != nil {
						w.WriteHeader(401)
						return
					}
					_ = json.NewEncoder(w).Encode(id)
				}
			})}
			go srv.Serve(listener)
			defer srv.Close()
			defer tr.CloseIdleConnections()
			jar, _ := cookiejar.New(nil)
			client := &http.Client{Transport: tr, Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			base := "http://127.0.0.1:9298"
			start := request(t, client, "POST", base+"/auth/oidc/login", base, false)
			page := request(t, client, "GET", start.Header.Get("Location"), "", false)
			// Keycloak's real browser credential form, not a password-grant shortcut.
			doc, err := html.Parse(page.Body)
			if err != nil {
				t.Fatal(err)
			}
			action := ""
			var visit func(*html.Node)
			visit = func(n *html.Node) {
				if n.Type == html.ElementNode && n.Data == "form" {
					for _, a := range n.Attr {
						if a.Key == "action" {
							action = a.Val
						}
					}
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					visit(c)
				}
			}
			visit(doc)
			if action == "" {
				t.Fatalf("Keycloak login form missing; status=%d", page.StatusCode)
			}
			form := url.Values{"username": {tt.user}, "password": {"synthetic-keycloak-test-password"}, "credentialId": {""}}
			resp, err := client.PostForm(action, form)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			callback := resp.Header.Get("Location")
			if !strings.HasPrefix(callback, cfg.CallbackURL) {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 1000))
				t.Fatalf("Keycloak did not issue callback: status=%d body=%s", resp.StatusCode, b)
			}
			result := request(t, client, "GET", callback, "", false)
			if (result.Header.Get("Location") == "/") != tt.accepted {
				t.Fatalf("callback admission mismatch: %s", result.Header.Get("Location"))
			}
			if !tt.accepted {
				return
			}
			me := request(t, client, "GET", base, "", false)
			if me.StatusCode != 200 {
				t.Fatal("Keycloak session missing")
			}
			if container := os.Getenv("JUL_KEYCLOAK_CONTAINER"); container != "" && tt.name == "group" {
				if err := exec.Command("docker", "pause", container).Run(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = exec.Command("docker", "unpause", container).Run() })
				if request(t, client, "GET", base, "", false).StatusCode != 200 {
					t.Fatal("local session failed during IdP outage")
				}
				fresh, err := New(cfg, policy(t), &http.Client{})
				if err != nil {
					t.Fatal(err)
				}
				rr := httptest.NewRecorder()
				req := httptest.NewRequest("POST", base+"/auth/oidc/login", nil)
				req.Header.Set("Origin", base)
				fresh.Login(rr, req)
				if rr.Header().Get("Location") != "/?oidc_error=unavailable" {
					t.Fatal("new login admitted during Keycloak outage")
				}
				if err := exec.Command("docker", "unpause", container).Run(); err != nil {
					t.Fatal(err)
				}
				if _, err := fresh.discover(context.Background()); err != nil {
					t.Fatal("Keycloak discovery did not recover")
				}
			}
			if request(t, client, "POST", base+"/auth/oidc/logout", base, true).StatusCode != 204 {
				t.Fatal("local logout failed")
			}
			if request(t, client, "GET", base, "", false).StatusCode != 401 {
				t.Fatal("revoked session accepted")
			}
		})
	}
}
