// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"

	"jul/internal/config"
)

const maxPrincipalBytes = 1024

// PrincipalOptions selects the explicit authority that may supply quota
// principals. ForwardResponseHeader is read from the auth service response,
// independently of the headers copied to the upstream request.
type PrincipalOptions struct {
	Domain                string
	ForwardResponseHeader string
}

// Principal is an authenticated identity, not an authorization decision.
// Issuer is populated for JWT so equal subjects from different issuers remain
// distinct even if a domain is accidentally reused.
type Principal struct {
	Method  string
	Domain  string
	Issuer  string
	Subject string
}

type principalKey struct{}

// PrincipalFromContext returns only identities published after successful
// authentication by a configured authenticator.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok && p.Method != ""
}

func validatePrincipalOptions(cfg config.AuthConfig, opts *PrincipalOptions) error {
	if opts == nil {
		return nil
	}
	if !validPrincipalValue(opts.Domain) || strings.TrimSpace(opts.Domain) != opts.Domain {
		return fmt.Errorf("quota principal domain must be nonempty, at most %d bytes, and have no surrounding whitespace", maxPrincipalBytes)
	}
	switch {
	case cfg.Basic != nil:
		if opts.ForwardResponseHeader != "" {
			return fmt.Errorf("quota principal response header requires forward authentication")
		}
	case cfg.JWT != nil:
		if cfg.JWT.Issuer == "" || cfg.JWT.Audience == "" {
			return fmt.Errorf("quota JWT principal requires a configured issuer and audience")
		}
		if opts.ForwardResponseHeader != "" {
			return fmt.Errorf("quota principal response header requires forward authentication")
		}
	case cfg.ForwardAuth != nil:
		if len(opts.ForwardResponseHeader) > 128 || !httpguts.ValidHeaderFieldName(opts.ForwardResponseHeader) {
			return fmt.Errorf("quota forward principal requires a valid response header")
		}
		if hopByHopHeaders[http.CanonicalHeaderKey(opts.ForwardResponseHeader)] {
			return fmt.Errorf("quota forward principal response header must not be hop-by-hop")
		}
	default:
		return fmt.Errorf("quota principal requires a credential authentication method")
	}
	return nil
}

func validPrincipalValue(value string) bool {
	return len(value) > 0 && len(value) <= maxPrincipalBytes
}

func (a *Authenticator) withPrincipal(r *http.Request, method, issuer, subject string) *http.Request {
	if a.principal == nil || !validPrincipalValue(subject) {
		return r
	}
	if method == "forward" && strings.Contains(subject, ",") {
		return r
	}
	p := Principal{Method: method, Domain: a.principal.Domain, Issuer: issuer, Subject: subject}
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
}
