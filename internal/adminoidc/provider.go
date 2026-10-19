// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminoidc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"jul/internal/auth"
	"jul/internal/config"
	"jul/internal/rbac"
)

const maxProviderBytes = 64 << 10

func joinScopes(scopes []string) string { return strings.Join(scopes, " ") }

func (r *Runtime) readJSON(req *http.Request, out any) error {
	resp, err := r.client.Do(req)
	if err != nil {
		return ErrUnauthenticated
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrUnauthenticated
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBytes+1))
	if err != nil || len(b) > maxProviderBytes {
		return ErrUnauthenticated
	}
	if err := json.Unmarshal(b, out); err != nil {
		return ErrUnauthenticated
	}
	return nil
}

type metadata struct {
	Issuer        string   `json:"issuer"`
	Authorization string   `json:"authorization_endpoint"`
	Token         string   `json:"token_endpoint"`
	JWKS          string   `json:"jwks_uri"`
	Responses     []string `json:"response_types_supported"`
	Algorithms    []string `json:"id_token_signing_alg_values_supported"`
	PKCE          []string `json:"code_challenge_methods_supported"`
	AuthMethods   []string `json:"token_endpoint_auth_methods_supported"`
}

func (r *Runtime) discover(ctx context.Context) (*provider, error) {
	if !r.providerMu.TryLock() {
		return nil, ErrUnauthenticated
	}
	defer r.providerMu.Unlock()
	now := r.now()
	if r.provider != nil && now.Before(r.providerUntil) {
		return r.provider, nil
	}
	if now.Before(r.providerRetry) {
		return nil, ErrUnauthenticated
	}
	r.providerRetry = now.Add(5 * time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(r.cfg.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	var m metadata
	if err := r.readJSON(req, &m); err != nil {
		return nil, err
	}
	method, err := validateMetadata(r.cfg, m)
	if err != nil {
		return nil, err
	}
	p := &provider{authorization: m.Authorization, token: m.Token, jwks: m.JWKS, authMethod: method, verifier: auth.NewIDTokenVerifier(m.JWKS, r.cfg.Issuer, r.cfg.ClientID, r.client, r.cfg.ClockSkew.Std())}
	r.provider = p
	r.providerUntil = now.Add(15 * time.Minute)
	return p, nil
}

func (r *Runtime) exchange(ctx context.Context, t transaction, code string) (map[string]any, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {r.cfg.CallbackURL}, "client_id": {r.cfg.ClientID}, "code_verifier": {t.verifier}}
	if t.provider.authMethod == "client_secret_post" {
		form.Set("client_secret", r.cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.provider.token, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, ErrUnauthenticated
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if t.provider.authMethod == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(r.cfg.ClientID), url.QueryEscape(r.cfg.ClientSecret))
	}
	var response struct {
		IDToken   string `json:"id_token"`
		TokenType string `json:"token_type"`
	}
	if err := r.readJSON(req, &response); err != nil {
		return nil, err
	}
	claims, err := t.provider.verifier.Verify(ctx, response.IDToken)
	if err != nil || claims["nonce"] != t.nonce {
		return nil, ErrUnauthenticated
	}
	return claims, nil
}

func (r *Runtime) mapIdentity(claims map[string]any) (rbac.Identity, error) {
	sub, ok := claims["sub"].(string)
	if !ok || sub == "" || len(sub) > 255 {
		return rbac.Identity{}, ErrUnauthenticated
	}
	fp := subjectFingerprint(r.cfg.Issuer, sub)
	// Exact subject mapping has precedence; no email or other display claim is used.
	for _, m := range r.cfg.Subjects {
		if sub == m.Subject {
			return r.policy.ExternalIdentity(m.Principal, m.Role, fp)
		}
	}
	if len(r.cfg.Groups) == 0 {
		return rbac.Identity{}, ErrUnauthenticated
	}
	// Entra overage indicators explicitly deny group-derived admission; no Graph fetch.
	if _, ok := claims["hasgroups"]; ok {
		return rbac.Identity{}, ErrUnauthenticated
	}
	if names, ok := claims["_claim_names"].(map[string]any); ok {
		if _, over := names[r.cfg.GroupClaim]; over {
			return rbac.Identity{}, ErrUnauthenticated
		}
	}
	raw, ok := claims[r.cfg.GroupClaim].([]any)
	if !ok || len(raw) == 0 || len(raw) > 256 {
		return rbac.Identity{}, ErrUnauthenticated
	}
	role := ""
	for _, value := range raw {
		group, ok := value.(string)
		if !ok || len(group) > 256 {
			return rbac.Identity{}, ErrUnauthenticated
		}
		for _, m := range r.cfg.Groups {
			if group == m.Group {
				if role != "" && role != m.Role {
					return rbac.Identity{}, errors.New("conflicting role mapping")
				}
				role = m.Role
			}
		}
	}
	if role == "" {
		return rbac.Identity{}, ErrUnauthenticated
	}
	// The derived public principal must not expose the unkeyed subject fingerprint.
	public := r.versions.Digest("oidc-public-principal", []byte(fp))
	return r.policy.ExternalIdentity("oidc-"+public[len(public)-32:], role, fp)
}

func validateMetadata(cfg config.AdminOIDCConfig, m metadata) (string, error) {
	if m.Issuer != cfg.Issuer || !slices.Contains(m.Responses, "code") || (len(m.PKCE) > 0 && !slices.Contains(m.PKCE, "S256")) {
		return "", ErrUnauthenticated
	}
	approved := false
	for _, alg := range m.Algorithms {
		if slices.Contains([]string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}, alg) {
			approved = true
		}
	}
	if !approved {
		return "", ErrUnauthenticated
	}
	for _, raw := range []string{m.Authorization, m.Token, m.JWKS} {
		endpoint, err := config.OIDCURL(raw)
		if err != nil {
			return "", ErrUnauthenticated
		}
		issuer, _ := url.Parse(cfg.Issuer)
		// A remote HTTPS issuer may never downgrade to even a loopback HTTP endpoint.
		if issuer.Scheme == "https" && endpoint.Scheme != "https" {
			return "", ErrUnauthenticated
		}
	}
	method := "none"
	if cfg.ClientSecret != "" {
		method = "client_secret_basic"
		if len(m.AuthMethods) > 0 && !slices.Contains(m.AuthMethods, method) {
			if !slices.Contains(m.AuthMethods, "client_secret_post") {
				return "", ErrUnauthenticated
			}
			method = "client_secret_post"
		}
	}
	// Public clients are selected explicitly by omitting the configured secret.
	// Keycloak omits "none" from discovery even when public PKCE clients are
	// enabled. Never use this compatibility behavior to downgrade a client
	// configured with a secret or to retry a failed confidential exchange.

	return method, nil
}
