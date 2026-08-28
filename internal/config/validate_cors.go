// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"strings"
)

// This file validates [servers.locations.cors] (ADR 0018 §9, §16). Validation
// checks legality only; normalization (lowercasing, canonical header-name
// form) happens once at compile time in internal/middleware, the same split
// already used for match.headers (config validates, router/middleware compiles
// the immutable runtime form). A populated-but-disabled block validates every
// field exactly as an enabled one would (ADR 0018 §9): flipping enabled later
// must not surface a value that was never valid.

// Bounds on a location's CORS policy (ADR 0018 §16). allowed_methods/
// allowed_headers/exposed_headers count and per-token bounds are not given
// exact numbers by the record beyond flagging that an unbounded-length entry
// is not a bound; these are conservative, documented ceilings chosen
// symmetric with the request-side Access-Control-Request-Headers bound of 64,
// additive to raise.
const (
	// MaxCORSOrigins bounds allowed_origins entries.
	MaxCORSOrigins = 64
	// MaxCORSOriginBytes bounds one origin's length.
	MaxCORSOriginBytes = 256
	// MaxCORSAllowedMethods bounds allowed_methods entries.
	MaxCORSAllowedMethods = 32
	// MaxCORSAllowedHeaders bounds allowed_headers entries.
	MaxCORSAllowedHeaders = 64
	// MaxCORSExposedHeaders bounds exposed_headers entries.
	MaxCORSExposedHeaders = 32
	// MaxCORSTokenBytes bounds one method/header token's length.
	MaxCORSTokenBytes = 256
	// MaxCORSMaxAge is the cap on max_age: 24 hours.
	MaxCORSMaxAge = 24 * 60 * 60 // seconds
	// MaxCORSGeneratedHeaderBytes bounds the worst-case total size of the
	// Access-Control-* + Vary header block Jul itself generates for one
	// response (a conservative static estimate, not a measured wire size).
	MaxCORSGeneratedHeaderBytes = 4096
)

// validateCORS checks a location's CORS policy. cors may be nil (no policy).
func validateCORS(cors *CORSConfig, where string) []error {
	if cors == nil {
		return nil
	}
	field := where + ".cors"
	var errs []error

	wildcard := false
	for _, o := range cors.AllowedOrigins {
		if o == "*" {
			wildcard = true
			break
		}
	}

	switch {
	case len(cors.AllowedOrigins) == 0 && cors.Enabled:
		errs = append(errs, fmt.Errorf("%s: allowed_origins is required when enabled = true", field))
	case wildcard && len(cors.AllowedOrigins) > 1:
		errs = append(errs, fmt.Errorf("%s: allowed_origins = [\"*\"] may not be combined with any other entry", field))
	case wildcard && cors.AllowCredentials:
		errs = append(errs, fmt.Errorf("%s: allowed_origins = [\"*\"] forbids allow_credentials = true; Fetch rejects a wildcard grant on a credentialed request, and the combination is a validation error rather than a silent downgrade", field))
	}
	if len(cors.AllowedOrigins) > MaxCORSOrigins {
		errs = append(errs, fmt.Errorf("%s.allowed_origins: %d entries, over the limit of %d", field, len(cors.AllowedOrigins), MaxCORSOrigins))
	}
	for i, o := range cors.AllowedOrigins {
		if o == "*" {
			continue
		}
		originField := fmt.Sprintf("%s.allowed_origins[%d]", field, i)
		if len(o) > MaxCORSOriginBytes {
			errs = append(errs, fmt.Errorf("%s: %d bytes, over the limit of %d", originField, len(o), MaxCORSOriginBytes))
			continue
		}
		if o == "null" {
			continue // meaningful only in a non-wildcard policy; lint-warns.
		}
		if err := validateOriginGrammar(o); err != nil {
			errs = append(errs, fmt.Errorf("%s: %q is not a valid origin: %w", originField, o, err))
		}
	}

	if cors.AllowedMethods != nil && len(cors.AllowedMethods) == 0 {
		errs = append(errs, fmt.Errorf("%s: allowed_methods is explicitly empty; omit the field to get the default safelisted set, since an empty list denies every preflight", field))
	}
	if len(cors.AllowedMethods) > MaxCORSAllowedMethods {
		errs = append(errs, fmt.Errorf("%s.allowed_methods: %d entries, over the limit of %d", field, len(cors.AllowedMethods), MaxCORSAllowedMethods))
	}
	for i, m := range cors.AllowedMethods {
		errs = append(errs, validateCORSToken(m, fmt.Sprintf("%s.allowed_methods[%d]", field, i))...)
	}

	if len(cors.AllowedHeaders) > MaxCORSAllowedHeaders {
		errs = append(errs, fmt.Errorf("%s.allowed_headers: %d entries, over the limit of %d", field, len(cors.AllowedHeaders), MaxCORSAllowedHeaders))
	}
	for i, h := range cors.AllowedHeaders {
		errs = append(errs, validateCORSToken(h, fmt.Sprintf("%s.allowed_headers[%d]", field, i))...)
	}

	if len(cors.ExposedHeaders) > MaxCORSExposedHeaders {
		errs = append(errs, fmt.Errorf("%s.exposed_headers: %d entries, over the limit of %d", field, len(cors.ExposedHeaders), MaxCORSExposedHeaders))
	}
	for i, h := range cors.ExposedHeaders {
		errs = append(errs, validateCORSToken(h, fmt.Sprintf("%s.exposed_headers[%d]", field, i))...)
	}

	if cors.MaxAge != nil {
		d := cors.MaxAge.Std()
		switch {
		case d < 0:
			errs = append(errs, fmt.Errorf("%s.max_age: must not be negative", field))
		case d%1e9 != 0: // not a whole number of seconds
			errs = append(errs, fmt.Errorf("%s.max_age: %s is not a whole number of seconds", field, d))
		case d.Seconds() > MaxCORSMaxAge:
			errs = append(errs, fmt.Errorf("%s.max_age: %s is over the limit of 24h", field, d))
		}
	}

	if estimate := estimateCORSHeaderBytes(cors); estimate > MaxCORSGeneratedHeaderBytes {
		errs = append(errs, fmt.Errorf("%s: the generated Access-Control-*/Vary header set is an estimated %d bytes in the worst case, over the limit of %d", field, estimate, MaxCORSGeneratedHeaderBytes))
	}
	return errs
}

// validateCORSToken checks one allowed_methods/allowed_headers/exposed_headers
// entry: a valid RFC 9110 token, no wildcard. Unlike match.methods these are
// compared case-insensitively, so no case rule applies here.
func validateCORSToken(tok, field string) []error {
	if tok == "*" {
		return []error{fmt.Errorf("%s: \"*\" is not permitted here; under Fetch a wildcard in this position does not cover Authorization, which is usually the header an operator writing \"*\" wants", field)}
	}
	if tok == "" || !isFieldToken(tok) {
		return []error{fmt.Errorf("%s: %q is not a valid token", field, tok)}
	}
	if len(tok) > MaxCORSTokenBytes {
		return []error{fmt.Errorf("%s: %d bytes, over the limit of %d", field, len(tok), MaxCORSTokenBytes)}
	}
	return nil
}

// validateOriginGrammar checks that s is exactly "scheme://host[:port]" with
// no path, query, fragment, userinfo or explicitly-written default port.
// Casing is not enforced here; the compiled runtime form lowercases it.
func validateOriginGrammar(s string) error {
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok || scheme == "" {
		return fmt.Errorf("missing scheme")
	}
	lscheme := strings.ToLower(scheme)
	if lscheme != "http" && lscheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	if rest == "" {
		return fmt.Errorf("missing host")
	}
	if strings.ContainsAny(rest, "/?#") {
		return fmt.Errorf("must not carry a path, query or fragment")
	}
	if strings.Contains(rest, "@") {
		return fmt.Errorf("must not carry userinfo")
	}
	host, port, hasPort := strings.Cut(rest, ":")
	if host == "" {
		return fmt.Errorf("missing host")
	}
	if hasPort {
		if port == "" {
			return fmt.Errorf("empty port")
		}
		for _, c := range port {
			if c < '0' || c > '9' {
				return fmt.Errorf("invalid port %q", port)
			}
		}
		if (lscheme == "http" && port == "80") || (lscheme == "https" && port == "443") {
			return fmt.Errorf("must not write the scheme's default port explicitly")
		}
	}
	return nil
}

// estimateCORSHeaderBytes computes a conservative worst-case byte count for
// the Access-Control-*/Vary header block a granted response can carry: the
// widest configured origin, every allowed method/header/exposed header joined
// as they are emitted, plus the credentials and max-age lines. It is a static
// approximation for a configuration-time bound, not a measured wire size.
func estimateCORSHeaderBytes(cors *CORSConfig) int {
	total := len("Access-Control-Allow-Origin: ") + MaxCORSOriginBytes
	if len(cors.AllowedMethods) > 0 {
		total += len("Access-Control-Allow-Methods: ") + joinedLen(cors.AllowedMethods)
	} else {
		total += len("Access-Control-Allow-Methods: GET, HEAD, POST")
	}
	if len(cors.AllowedHeaders) > 0 {
		total += len("Access-Control-Allow-Headers: ") + joinedLen(cors.AllowedHeaders)
	}
	if len(cors.ExposedHeaders) > 0 {
		total += len("Access-Control-Expose-Headers: ") + joinedLen(cors.ExposedHeaders)
	}
	if cors.AllowCredentials {
		total += len("Access-Control-Allow-Credentials: true")
	}
	if cors.MaxAge != nil {
		total += len("Access-Control-Max-Age: 86400")
	}
	total += len("Vary: Origin, Access-Control-Request-Method, Access-Control-Request-Headers")
	return total
}

// joinedLen is the byte length of strs joined with ", ", the wire form of a
// multi-token header value.
func joinedLen(strs []string) int {
	if len(strs) == 0 {
		return 0
	}
	n := 2 * (len(strs) - 1)
	for _, s := range strs {
		n += len(s)
	}
	return n
}
