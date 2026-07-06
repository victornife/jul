// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// writeHtpasswd writes an htpasswd file with bcrypt hashes for the given
// user->password pairs and returns its path.
func writeHtpasswd(t *testing.T, users map[string]string) string {
	t.Helper()
	var b []byte
	for user, pass := range users {
		hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.MinCost)
		if err != nil {
			t.Fatalf("hash: %v", err)
		}
		b = append(b, []byte(user+":"+string(hash)+"\n")...)
	}
	path := filepath.Join(t.TempDir(), "htpasswd")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write htpasswd: %v", err)
	}
	return path
}

func TestBasicAuthCheck(t *testing.T) {
	path := writeHtpasswd(t, map[string]string{"alice": "s3cret", "bob": "hunter2"})
	b, err := newBasicAuth(path, "Restricted")
	if err != nil {
		t.Fatalf("newBasicAuth: %v", err)
	}

	tests := []struct {
		name     string
		user     string
		pass     string
		setCreds bool
		want     bool
	}{
		{"valid alice", "alice", "s3cret", true, true},
		{"valid bob", "bob", "hunter2", true, true},
		{"wrong password", "alice", "nope", true, false},
		{"unknown user", "carol", "whatever", true, false},
		{"no credentials", "", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.setCreds {
				r.SetBasicAuth(tt.user, tt.pass)
			}
			if got := b.check(r); got != tt.want {
				t.Errorf("check() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBasicAuthChallenge(t *testing.T) {
	path := writeHtpasswd(t, map[string]string{"alice": "s3cret"})
	b, err := newBasicAuth(path, "My Realm")
	if err != nil {
		t.Fatalf("newBasicAuth: %v", err)
	}
	rec := httptest.NewRecorder()
	b.challenge(rec)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="My Realm", charset="UTF-8"` {
		t.Errorf("WWW-Authenticate = %q", got)
	}
}

func TestNewBasicAuthErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if _, err := newBasicAuth(filepath.Join(t.TempDir(), "nope"), "r"); err == nil {
			t.Error("expected error for missing file")
		}
	})
	t.Run("non-bcrypt hash rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "htpasswd")
		if err := os.WriteFile(path, []byte("alice:{PLAIN}password\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newBasicAuth(path, "r"); err == nil {
			t.Error("expected error for non-bcrypt hash")
		}
	})
	t.Run("malformed entry rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "htpasswd")
		if err := os.WriteFile(path, []byte("no-colon-here\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newBasicAuth(path, "r"); err == nil {
			t.Error("expected error for malformed entry")
		}
	})
	t.Run("empty file rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "htpasswd")
		if err := os.WriteFile(path, []byte("# only a comment\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newBasicAuth(path, "r"); err == nil {
			t.Error("expected error for file with no entries")
		}
	})
}
