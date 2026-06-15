package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwtClaimLeeway absorbs small clock differences between this server and the
// token issuer when validating time-based claims (exp/nbf/iat).
const jwtClaimLeeway = 60 * time.Second

// jwtAuth validates RFC 7519 bearer tokens against keys published at a JWKS
// endpoint, enforcing an algorithm allow-list plus issuer/audience/expiry.
type jwtAuth struct {
	issuer     string
	audience   string
	algorithms []string
	parser     *jwt.Parser
	jwks       *jwksCache
}

// newJWTAuth builds a JWT validator. The algorithm list is the allow-list used
// for signature verification; the symmetric "none" method is never accepted.
func newJWTAuth(jwksURL, issuer, audience string, algorithms []string, client *http.Client) *jwtAuth {
	opts := []jwt.ParserOption{
		jwt.WithValidMethods(algorithms),
		jwt.WithLeeway(jwtClaimLeeway),
		jwt.WithExpirationRequired(),
	}
	if issuer != "" {
		opts = append(opts, jwt.WithIssuer(issuer))
	}
	if audience != "" {
		opts = append(opts, jwt.WithAudience(audience))
	}
	return &jwtAuth{
		issuer:     issuer,
		audience:   audience,
		algorithms: algorithms,
		parser:     jwt.NewParser(opts...),
		jwks:       newJWKSCache(jwksURL, client),
	}
}

// validate parses and verifies the request's bearer token, returning its claims
// on success.
func (j *jwtAuth) validate(r *http.Request) (map[string]any, error) {
	raw, ok := bearerToken(r)
	if !ok {
		return nil, errors.New("missing bearer token")
	}
	claims := jwt.MapClaims{}
	_, err := j.parser.ParseWithClaims(raw, claims, j.keyFunc)
	if err != nil {
		return nil, err
	}
	return map[string]any(claims), nil
}

// keyFunc selects the verification key for a token by its "kid" header,
// confirming the token's algorithm family matches the key type.
func (j *jwtAuth) keyFunc(t *jwt.Token) (any, error) {
	kid, _ := t.Header["kid"].(string)
	key, err := j.jwks.keyByID(kid)
	if err != nil {
		return nil, err
	}
	switch t.Method.(type) {
	case *jwt.SigningMethodRSA, *jwt.SigningMethodRSAPSS:
		if _, ok := key.(*rsa.PublicKey); !ok {
			return nil, fmt.Errorf("jwt: key %q is not RSA", kid)
		}
	case *jwt.SigningMethodECDSA:
		if _, ok := key.(*ecdsa.PublicKey); !ok {
			return nil, fmt.Errorf("jwt: key %q is not ECDSA", kid)
		}
	default:
		return nil, fmt.Errorf("jwt: unsupported signing method %v", t.Header["alg"])
	}
	return key, nil
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// jwksCache fetches and caches the JWKS document, refreshing it periodically and
// serving stale keys within a grace window when the endpoint is unreachable.
type jwksCache struct {
	url    string
	client *http.Client

	refreshAfter time.Duration
	staleGrace   time.Duration
	minRefresh   time.Duration

	mu        sync.RWMutex
	keys      map[string]crypto.PublicKey
	fetchedAt time.Time

	// refreshMu serializes network fetches and guards lastAttempt so a flood of
	// tokens bearing unknown key ids cannot amplify into unbounded outbound JWKS
	// requests.
	refreshMu   sync.Mutex
	lastAttempt time.Time
}

func newJWKSCache(url string, client *http.Client) *jwksCache {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &jwksCache{
		url:          url,
		client:       client,
		refreshAfter: 15 * time.Minute,
		staleGrace:   1 * time.Hour,
		minRefresh:   30 * time.Second,
	}
}

// keyByID returns the public key for the given key id, refreshing the cache when
// the requested key is unknown or the cache is stale. Network refreshes are
// throttled to at most one per minRefresh so unknown-kid floods cannot amplify
// into a storm of JWKS fetches; within the throttle window a stale key is served
// while it remains inside the grace window.
func (c *jwksCache) keyByID(kid string) (crypto.PublicKey, error) {
	c.mu.RLock()
	key, known := c.keys[kid]
	fresh := time.Since(c.fetchedAt) < c.refreshAfter
	c.mu.RUnlock()
	if known && fresh {
		return key, nil
	}

	// A miss or an aged cache warrants a refresh, subject to throttling.
	if c.tryRefresh() {
		c.mu.RLock()
		key, known = c.keys[kid]
		c.mu.RUnlock()
		if known {
			return key, nil
		}
		return nil, fmt.Errorf("jwt: no key for kid %q", kid)
	}

	// Refresh was throttled or failed: serve a stale key within the grace window.
	c.mu.RLock()
	key, known = c.keys[kid]
	withinGrace := time.Since(c.fetchedAt) < c.staleGrace
	c.mu.RUnlock()
	if known && withinGrace {
		return key, nil
	}
	return nil, fmt.Errorf("jwt: no key for kid %q", kid)
}

// tryRefresh fetches the JWKS at most once per minRefresh interval. It reports
// whether a fetch succeeded and updated the cache.
func (c *jwksCache) tryRefresh() bool {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if !c.lastAttempt.IsZero() && time.Since(c.lastAttempt) < c.minRefresh {
		return false
	}
	c.lastAttempt = time.Now()
	return c.refresh() == nil
}

func (c *jwksCache) refresh() error {
	req, err := http.NewRequest(http.MethodGet, c.url, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	keys, err := parseJWKS(body)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.keys = keys
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	return nil
}

// jwkSet / jwk model the subset of RFC 7517 fields needed to reconstruct RSA and
// EC public keys.
type jwkSet struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func parseJWKS(body []byte) (map[string]crypto.PublicKey, error) {
	var set jwkSet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("decode JWKS: %w", err)
	}
	keys := make(map[string]crypto.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		pub, err := k.publicKey()
		if err != nil {
			// Skip keys we cannot model rather than rejecting the whole set.
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("JWKS contains no usable RSA or EC keys")
	}
	return keys, nil
}

// publicKey reconstructs the crypto public key described by a JWK.
func (k jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		return k.rsaKey()
	case "EC":
		return k.ecKey()
	default:
		return nil, fmt.Errorf("unsupported kty %q", k.Kty)
	}
}

func (k jwk) rsaKey() (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("RSA n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("RSA e: %w", err)
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e == 0 {
		// Some encoders pad the exponent; fall back to a fixed-width decode.
		var buf [8]byte
		copy(buf[len(buf)-len(eBytes):], eBytes)
		e = int(binary.BigEndian.Uint64(buf[:]))
	}
	if e <= 0 {
		return nil, errors.New("RSA exponent out of range")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

func (k jwk) ecKey() (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("unsupported EC curve %q", k.Crv)
	}
	xBytes, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("EC x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, fmt.Errorf("EC y: %w", err)
	}
	x := new(big.Int).SetBytes(xBytes)
	y := new(big.Int).SetBytes(yBytes)
	if !curve.IsOnCurve(x, y) {
		return nil, errors.New("EC point is not on curve")
	}
	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
}
