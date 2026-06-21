package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"
)

// FuzzParseJWKS exercises the untrusted JWKS document parser (JSON decode,
// base64url decode, and RSA/EC public-key reconstruction including the
// on-curve check). It must never panic for any input.
func FuzzParseJWKS(f *testing.F) {
	seeds := []string{
		`{"keys":[]}`,
		`{"keys":[{"kty":"RSA","kid":"a","n":"AQAB","e":"AQAB"}]}`,
		`{"keys":[{"kty":"EC","kid":"b","crv":"P-256","x":"AAAA","y":"AAAA"}]}`,
		`{"keys":[{"kty":"RSA","kid":"c","n":"%%%","e":"@@"}]}`,
		`{"keys":[{"kty":"EC","kid":"d","crv":"P-999","x":"AA","y":"AA"}]}`,
		`{"keys":[{"kty":"oct","kid":"e"}]}`,
		`not json`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		// Errors are expected for malformed input; a panic is a bug.
		_, _ = parseJWKS(body)
	})
}

// FuzzValidateToken feeds arbitrary bearer tokens through the full JWT
// validation path against a pre-warmed, network-blocked JWKS cache. Any token
// must be rejected cleanly (no panic).
func FuzzValidateToken(f *testing.F) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		f.Fatalf("rsa key: %v", err)
	}
	j := newJWTAuth("https://issuer.example/jwks.json",
		"https://issuer.example", "my-api", defaultAlgs(), nil)
	j.jwks.keys = map[string]crypto.PublicKey{"rsa-1": &key.PublicKey}
	j.jwks.fetchedAt = time.Now()
	// Block any network refresh on an unknown kid so the fuzz stays hermetic.
	j.jwks.lastAttempt = time.Now()
	j.jwks.minRefresh = time.Hour

	f.Add(signRS256(f, key, "rsa-1", validClaims()))
	f.Add("not.a.token")
	f.Add("a.b.c")
	f.Add("")
	f.Fuzz(func(t *testing.T, token string) {
		_, _ = j.validate(bearerReq(token))
	})
}
