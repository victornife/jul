// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestIDTokenVerifierClaimsAndCancellation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := newJWKSServer(t, rsaJWK("id", &key.PublicKey))
	v := NewIDTokenVerifier(srv.URL, "https://issuer.example", "my-api", srv.Client(), time.Minute)
	for _, tt := range []struct {
		name, claim string
		value       any
		valid       bool
	}{
		{"valid", "nonce", "nonce", true}, {"missing-sub", "sub", "", false}, {"typed-sub", "sub", 42, false},
		{"missing-iat", "iat", nil, false}, {"multi-aud", "aud", []string{"my-api", "other"}, false},
		{"wrong-azp", "azp", "other", false}, {"wrong-issuer", "iss", "other", false},
		{"future-nbf", "nbf", time.Now().Add(time.Hour).Unix(), false}, {"future-iat", "iat", time.Now().Add(time.Hour).Unix(), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claims := validClaims()
			if tt.value == nil {
				delete(claims, tt.claim)
			} else {
				claims[tt.claim] = tt.value
			}
			_, err := v.Verify(context.Background(), signRS256(t, key, "id", claims))
			if (err == nil) != tt.valid {
				t.Fatal(err)
			}
		})
	}
	for _, raw := range []string{"", strings.Repeat("x", 16385), "not.a.token"} {
		if _, err := v.Verify(context.Background(), raw); err == nil {
			t.Fatal("bad token accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Verify(ctx, "token"); err == nil {
		t.Fatal("cancelled")
	}
}
func TestJWKSSecurityBoundsAndUsage(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	good := rsaJWK("id", &key.PublicKey)
	for _, tt := range []struct {
		name  string
		keys  []map[string]any
		valid bool
	}{
		{"duplicate", []map[string]any{good, good}, false},
		{"too-many", make([]map[string]any, 129), false},
		{"valid", []map[string]any{good}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := json.Marshal(map[string]any{"keys": tt.keys})
			_, err := parseJWKS(b)
			if (err == nil) != tt.valid {
				t.Fatal(err)
			}
		})
	}
	for _, field := range []string{"use", "key_ops"} {
		bad := rsaJWK("id", &key.PublicKey)
		if field == "use" {
			bad[field] = "enc"
		} else {
			bad[field] = []string{"encrypt"}
		}
		b, _ := json.Marshal(map[string]any{"keys": []any{bad}})
		if _, err := parseJWKS(b); err == nil {
			t.Fatal("non-signing key accepted")
		}
	}
	if _, err := (&jwk{Kty: "RSA", N: "AA", E: "AAAAAAAAAAAA"}).rsaKey(); err == nil {
		t.Fatal("oversized exponent accepted")
	}
}

func TestIDTokenRejectsWeakRSAKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	srv := newJWKSServer(t, rsaJWK("weak", &key.PublicKey))
	v := NewIDTokenVerifier(srv.URL, "https://issuer.example", "my-api", srv.Client(), time.Minute)
	if _, err := v.Verify(context.Background(), signRS256(t, key, "weak", validClaims())); err == nil {
		t.Fatal("OIDC admitted a key below its 2048-bit RSA minimum")
	}
}
