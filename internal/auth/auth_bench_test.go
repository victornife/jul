package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// BenchmarkBasicVerify measures a successful HTTP Basic check, dominated by the
// deliberate cost of bcrypt password comparison.
func BenchmarkBasicVerify(b *testing.B) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.DefaultCost)
	if err != nil {
		b.Fatalf("bcrypt: %v", err)
	}
	ba := &basicAuth{realm: "bench", users: map[string]string{"alice": string(hash)}}
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("alice", "correct horse")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !ba.check(req) {
			b.Fatal("check failed")
		}
	}
}

// BenchmarkJWTValidate measures token parsing plus RS256 signature verification
// against a pre-warmed JWKS cache (no network on the hot path).
func BenchmarkJWTValidate(b *testing.B) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		b.Fatalf("rsa key: %v", err)
	}
	j := newJWTAuth("https://issuer.example/jwks.json",
		"https://issuer.example", "my-api", defaultAlgs(), nil)
	j.jwks.keys = map[string]crypto.PublicKey{"rsa-1": &key.PublicKey}
	j.jwks.fetchedAt = time.Now()
	req := bearerReq(signRS256(b, key, "rsa-1", validClaims()))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := j.validate(req); err != nil {
			b.Fatalf("validate: %v", err)
		}
	}
}
