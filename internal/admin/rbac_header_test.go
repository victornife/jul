// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

func TestLegacyAdminRejectsNonBearerAndDuplicateAuthorization(t *testing.T) {
	token := "legacy-token-32-chars-padded-xxx"
	for _, tc := range []struct {
		name   string
		header []string
		allow  bool
	}{
		{"valid bearer", []string{"Bearer " + token}, true},
		{"wrong scheme", []string{"Invalid " + token}, false},
		{"duplicate credential", []string{"Bearer " + token, "Bearer other"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
			for _, h := range tc.header {
				r.Header.Add("Authorization", h)
			}
			if got := checkLegacyToken(r, token); got != tc.allow {
				t.Fatalf("accepted = %v, want %v", got, tc.allow)
			}
		})
	}
}

func TestRBACAdminRejectsDuplicateAuthorization(t *testing.T) {
	token := "rbac-admin-32-chars-padded-xxxxx"
	s := &Server{cfg: config.AdminConfig{Listen: "127.0.0.1:0"}}
	s.installAuth(config.AdminConfig{}, snapAdminPolicy(t, token, time.Now()))
	h := s.requirePermission(rbac.ConfigRead, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name   string
		header []string
		want   int
	}{
		{"single credential", []string{"Bearer " + token}, http.StatusNoContent},
		{"duplicate credential", []string{"Bearer " + token, "Bearer other"}, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
			for _, v := range tc.header {
				r.Header.Add("Authorization", v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}
