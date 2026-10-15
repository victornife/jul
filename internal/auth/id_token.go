// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// IDTokenVerifier adds OIDC time/subject/authorized-party requirements to the
// existing JWT verifier and JWKS cache. It is generation-owned by its caller.
// API bearer authentication continues to use its existing validation contract.
type IDTokenVerifier struct {
	validator *jwtAuth
	clientID  string
}

// NewIDTokenVerifier shares algorithm, key parsing, refresh throttling, stale
// grace, dependency and egress behavior with request JWT authentication.
func NewIDTokenVerifier(jwksURL, issuer, clientID string, client *http.Client, skew time.Duration) *IDTokenVerifier {
	j := newJWTAuth(jwksURL, issuer, clientID, []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}, client, nil)
	j.parser = jwt.NewParser(jwt.WithValidMethods(j.algorithms), jwt.WithIssuer(issuer), jwt.WithAudience(clientID), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(skew))
	return &IDTokenVerifier{validator: j, clientID: clientID}
}

// Verify verifies a bounded signed ID token. Nonce and admission mapping remain
// transaction-specific duties of the Console relying party.
func (v *IDTokenVerifier) Verify(ctx context.Context, raw string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > 16384 {
		return nil, errors.New("invalid ID token")
	}
	claims := jwt.MapClaims{}
	if _, err := v.validator.parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		key, err := v.validator.keyFuncContext(ctx, t)
		if err != nil {
			return nil, err
		}
		if rsaKey, ok := key.(*rsa.PublicKey); ok && rsaKey.N.BitLen() < 2048 {
			return nil, errors.New("ID token RSA key is below 2048 bits")
		}
		return key, nil
	}); err != nil {
		return nil, errors.New("invalid ID token")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sub, ok := claims["sub"].(string)
	if !ok || sub == "" || len(sub) > 255 {
		return nil, errors.New("invalid ID token subject")
	}
	iat, err := claims.GetIssuedAt()
	if err != nil || iat == nil {
		return nil, errors.New("missing ID token issued-at")
	}
	aud, err := claims.GetAudience()
	azp, present := claims["azp"]
	if err != nil || (len(aud) > 1 && !present) || (present && azp != v.clientID) {
		return nil, errors.New("invalid authorized party")
	}
	return map[string]any(claims), nil
}
