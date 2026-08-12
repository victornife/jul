// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

// Backend TLS wiring for the HTTP reverse proxy.
//
// The transport receives a resolved *backendtls.Policy and never the public
// configuration. One policy is resolved per handler generation, so a policy
// change produces a new transport with its own connection pool: requests that
// start after the publish cannot reuse a keep-alive or HTTP/2 connection
// established under the previous trust, and the retiring generation closes its
// idle connections when it is retired.

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"jul/internal/backendtls"
	"jul/internal/config"
)

// resolveBackendTLS returns the policy that governs a location's backend.
//
// Precedence is fixed and documented: a location's own block overrides the
// pool's for that route. They are never merged — a merged security policy is one
// nobody can read off the configuration.
//
// logicalHost is the *configured* target (the upstream name, or the literal
// host from proxy_pass), never a selected backend address. That is what keeps a
// discovery-returned address a dial destination rather than an identity.
func resolveBackendTLS(loc config.LocationConfig, upstreams map[string]config.UpstreamConfig) (*backendtls.Policy, error) {
	logicalHost := ""
	if u, err := url.Parse(loc.ProxyPass); err == nil {
		logicalHost = u.Host
	}
	block := loc.BackendTLS
	if block == nil {
		if up, ok := upstreams[logicalHost]; ok {
			block = up.BackendTLS
		}
	}
	if block == nil {
		return nil, nil
	}
	policy, err := backendtls.Resolve(block.Options(), logicalHost)
	if err != nil {
		return nil, fmt.Errorf("backend_tls for %q: %w", logicalHost, err)
	}
	return policy, nil
}

// transportCloser retires a generation's HTTP transport. Closing idle
// connections is what prevents a connection established under a previous trust
// policy from serving a request admitted after the new policy is live; requests
// still in flight keep their connection until they finish, which is the
// generation's drain contract rather than an abrupt cut.
type transportCloser struct{ t *http.Transport }

func (c transportCloser) Close() error {
	if c.t != nil {
		c.t.CloseIdleConnections()
	}
	return nil
}

// tlsFailureCategory maps a transport error to a bounded category.
//
// The categories are a closed set so they are safe as a log field and, later, as
// a metric label. No host, address, certificate subject, SAN, file path or raw
// error text is ever derived from here — the category is the whole payload.
func tlsFailureCategory(err error) string {
	if err == nil {
		return ""
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	var recordHeader tls.RecordHeaderError
	var certVerification *tls.CertificateVerificationError
	switch {
	case errors.As(err, &certVerification):
		// Unwrap once: the verification error names the underlying reason.
		if inner := certVerification.Err; inner != nil {
			if category := tlsFailureCategory(inner); category != "" && category != "tls_other" {
				return category
			}
		}
		return "unknown_authority"
	case errors.As(err, &unknownAuthority):
		return "unknown_authority"
	case errors.As(err, &hostnameErr):
		return "hostname_mismatch"
	case errors.As(err, &invalidCert):
		if invalidCert.Reason == x509.Expired {
			return "certificate_expired"
		}
		return "certificate_invalid"
	case errors.As(err, &recordHeader):
		return "tls_handshake"
	}

	// The remaining cases have no typed error in the standard library. Match on
	// the fixed sentences the TLS stack produces, not on user-controlled text.
	msg := err.Error()
	switch {
	case strings.Contains(msg, backendtls.PeerIdentityErrorFragment):
		return "peer_identity_mismatch"
	case strings.Contains(msg, "tls: protocol version not supported"),
		strings.Contains(msg, "protocol version not supported"):
		return "tls_version"
	case strings.Contains(msg, "certificate required"),
		strings.Contains(msg, "bad certificate"):
		return "client_certificate"
	case strings.Contains(msg, "tls: handshake failure"),
		strings.Contains(msg, "handshake timeout"):
		return "tls_handshake"
	case strings.Contains(msg, "tls:"), strings.Contains(msg, "x509:"):
		return "tls_other"
	}
	return ""
}
