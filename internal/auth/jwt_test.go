package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwksServer is an httptest.Server that serves a swappable JWKS document and
// counts the number of times it was fetched.
type jwksServer struct {
	*httptest.Server
	mu   sync.Mutex
	body []byte
	hits int
}

func newJWKSServer(t *testing.T, keys ...map[string]any) *jwksServer {
	t.Helper()
	s := &jwksServer{}
	s.setKeys(t, keys...)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits++
		body := s.body
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) setKeys(t *testing.T, keys ...map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	s.mu.Lock()
	s.body = body
	s.mu.Unlock()
}

func (s *jwksServer) fetchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func rsaJWK(kid string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func ecJWK(kid string, pub *ecdsa.PublicKey) map[string]any {
	size := (pub.Curve.Params().BitSize + 7) / 8
	return map[string]any{
		"kty": "EC",
		"kid": kid,
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(leftPad(pub.X.Bytes(), size)),
		"y":   base64.RawURLEncoding.EncodeToString(leftPad(pub.Y.Bytes(), size)),
	}
}

func leftPad(b []byte, size int) []byte {
	if len(b) >= size {
		return b
	}
	out := make([]byte, size)
	copy(out[size-len(b):], b)
	return out
}

// signRS256 signs claims with the RSA key and a "kid" header.
func signRS256(tb testing.TB, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	tb.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		tb.Fatalf("sign RS256: %v", err)
	}
	return s
}

func bearerReq(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss": "https://issuer.example",
		"aud": "my-api",
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Add(-time.Minute).Unix(),
	}
}

func TestJWTValidate(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	srv := newJWKSServer(t, rsaJWK("rsa-1", &rsaKey.PublicKey))
	j := newJWTAuth(srv.URL, "https://issuer.example", "my-api", defaultAlgs(), srv.Client())

	t.Run("valid token", func(t *testing.T) {
		token := signRS256(t, rsaKey, "rsa-1", validClaims())
		claims, err := j.validate(bearerReq(token))
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if claims["sub"] != "user-123" {
			t.Errorf("sub = %v, want user-123", claims["sub"])
		}
	})

	t.Run("missing token", func(t *testing.T) {
		if _, err := j.validate(bearerReq("")); err == nil {
			t.Error("expected error for missing token")
		}
	})

	t.Run("expired token", func(t *testing.T) {
		c := validClaims()
		c["exp"] = time.Now().Add(-time.Hour).Unix()
		token := signRS256(t, rsaKey, "rsa-1", c)
		if _, err := j.validate(bearerReq(token)); err == nil {
			t.Error("expected error for expired token")
		}
	})

	t.Run("wrong audience", func(t *testing.T) {
		c := validClaims()
		c["aud"] = "other-api"
		token := signRS256(t, rsaKey, "rsa-1", c)
		if _, err := j.validate(bearerReq(token)); err == nil {
			t.Error("expected error for wrong audience")
		}
	})

	t.Run("wrong issuer", func(t *testing.T) {
		c := validClaims()
		c["iss"] = "https://evil.example"
		token := signRS256(t, rsaKey, "rsa-1", c)
		if _, err := j.validate(bearerReq(token)); err == nil {
			t.Error("expected error for wrong issuer")
		}
	})

	t.Run("unknown kid", func(t *testing.T) {
		token := signRS256(t, rsaKey, "rsa-unknown", validClaims())
		if _, err := j.validate(bearerReq(token)); err == nil {
			t.Error("expected error for unknown kid")
		}
	})

	t.Run("none algorithm rejected", func(t *testing.T) {
		tok := jwt.NewWithClaims(jwt.SigningMethodNone, validClaims())
		tok.Header["kid"] = "rsa-1"
		token, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("sign none: %v", err)
		}
		if _, err := j.validate(bearerReq(token)); err == nil {
			t.Error("expected 'none' algorithm to be rejected")
		}
	})

	t.Run("wrong signing key rejected", func(t *testing.T) {
		other, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("rsa key: %v", err)
		}
		token := signRS256(t, other, "rsa-1", validClaims())
		if _, err := j.validate(bearerReq(token)); err == nil {
			t.Error("expected error for token signed by a different key")
		}
	})
}

func TestJWTValidateEC(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ec key: %v", err)
	}
	srv := newJWKSServer(t, ecJWK("ec-1", &ecKey.PublicKey))
	j := newJWTAuth(srv.URL, "https://issuer.example", "my-api", defaultAlgs(), srv.Client())

	tok := jwt.NewWithClaims(jwt.SigningMethodES256, validClaims())
	tok.Header["kid"] = "ec-1"
	token, err := tok.SignedString(ecKey)
	if err != nil {
		t.Fatalf("sign ES256: %v", err)
	}
	if _, err := j.validate(bearerReq(token)); err != nil {
		t.Fatalf("validate EC token: %v", err)
	}
}

func TestJWTAlgorithmNotAllowed(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ec key: %v", err)
	}
	srv := newJWKSServer(t, ecJWK("ec-1", &ecKey.PublicKey))
	// Allow only RSA algorithms; an ES256 token must be rejected by the method
	// allow-list even though the key resolves.
	j := newJWTAuth(srv.URL, "", "", []string{"RS256"}, srv.Client())

	tok := jwt.NewWithClaims(jwt.SigningMethodES256, validClaims())
	tok.Header["kid"] = "ec-1"
	token, err := tok.SignedString(ecKey)
	if err != nil {
		t.Fatalf("sign ES256: %v", err)
	}
	if _, err := j.validate(bearerReq(token)); err == nil {
		t.Error("expected ES256 token to be rejected when only RS256 is allowed")
	}
}

func TestJWKSRotation(t *testing.T) {
	key1, _ := rsa.GenerateKey(rand.Reader, 2048)
	key2, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := newJWKSServer(t, rsaJWK("rsa-1", &key1.PublicKey))
	j := newJWTAuth(srv.URL, "https://issuer.example", "my-api", defaultAlgs(), srv.Client())
	// Disable refresh throttling so the immediate rotation below is picked up
	// without waiting out the miss-fetch interval.
	j.jwks.minRefresh = 0

	// First token validates against the initial key.
	if _, err := j.validate(bearerReq(signRS256(t, key1, "rsa-1", validClaims()))); err != nil {
		t.Fatalf("validate key1 token: %v", err)
	}

	// Rotate: the server now publishes a new key id. A token with the new kid is
	// a cache miss and triggers a refresh that picks it up.
	srv.setKeys(t, rsaJWK("rsa-2", &key2.PublicKey))
	if _, err := j.validate(bearerReq(signRS256(t, key2, "rsa-2", validClaims()))); err != nil {
		t.Fatalf("validate key2 token after rotation: %v", err)
	}
}

func TestJWKSRefreshThrottle(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := newJWKSServer(t, rsaJWK("rsa-1", &key.PublicKey))
	j := newJWTAuth(srv.URL, "https://issuer.example", "my-api", defaultAlgs(), srv.Client())

	// Prime the cache with one fetch.
	if _, err := j.validate(bearerReq(signRS256(t, key, "rsa-1", validClaims()))); err != nil {
		t.Fatalf("prime cache: %v", err)
	}
	if got := srv.fetchCount(); got != 1 {
		t.Fatalf("after priming, fetchCount = %d, want 1", got)
	}

	// A flood of tokens with unknown kids must not amplify into more fetches
	// within the throttle window.
	for i := 0; i < 10; i++ {
		_, _ = j.validate(bearerReq(signRS256(t, key, "rsa-unknown", validClaims())))
	}
	if got := srv.fetchCount(); got != 1 {
		t.Errorf("unknown-kid flood triggered %d fetches, want it throttled to 1", got)
	}
}

func TestJWKSStaleGrace(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := newJWKSServer(t, rsaJWK("rsa-1", &key.PublicKey))
	j := newJWTAuth(srv.URL, "https://issuer.example", "my-api", defaultAlgs(), srv.Client())
	// Force a refresh on every lookup so the outage path is exercised, but keep a
	// long stale-grace window and disable throttling so the failing fetch is
	// actually attempted.
	j.jwks.refreshAfter = 0
	j.jwks.staleGrace = time.Hour
	j.jwks.minRefresh = 0

	// Prime the cache while the endpoint is up.
	if _, err := j.validate(bearerReq(signRS256(t, key, "rsa-1", validClaims()))); err != nil {
		t.Fatalf("prime cache: %v", err)
	}

	// Take the endpoint down: the cached key must still validate within grace.
	srv.Close()
	if _, err := j.validate(bearerReq(signRS256(t, key, "rsa-1", validClaims()))); err != nil {
		t.Fatalf("expected stale key to validate during outage: %v", err)
	}
}

func defaultAlgs() []string {
	return []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "PS256", "PS384", "PS512"}
}
