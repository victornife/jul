// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package backendtls

import (
	"errors"
	"fmt"
	"strings"
)

// Validate checks a backend_tls block's structure and cross-field rules. It
// performs no file I/O, so configuration validation stays deterministic and
// offline; readability and PEM contents are checked by Resolve, during reload
// preparation, where a failure aborts before publish.
//
// Errors are field-relative ("ca_mode: ..."); the caller prefixes them with the
// canonical configuration path.
//
// insecure_skip_verify is deliberately **accepted** here. A field whose entire
// purpose is opting into an insecure mode cannot be a validation rejection, or
// the field is unusable and the emergency path it exists to provide disappears.
// It is a lint *error* instead, so `jul lint` exits 1 even without -strict. Only
// self-contradictory combinations are hard errors.
func Validate(o Options) []error {
	var errs []error

	mode := strings.TrimSpace(o.CAMode)
	switch mode {
	case "", CAModeSystem, CAModeSystemAndFile, CAModeFileOnly:
	default:
		errs = append(errs, fmt.Errorf("ca_mode: invalid value %q; expected %s", o.CAMode, strings.Join(CAModes(), ", ")))
	}
	caFile := strings.TrimSpace(o.CAFile)
	if (mode == CAModeSystemAndFile || mode == CAModeFileOnly) && caFile == "" {
		errs = append(errs, fmt.Errorf("ca_file: required when ca_mode is %q", mode))
	}
	if caFile != "" && (mode == "" || mode == CAModeSystem) {
		// Never inferred: a ca_file with the default mode would otherwise be
		// silently ignored, which is the worst of both readings.
		errs = append(errs, fmt.Errorf("ca_file: set ca_mode to %q or %q to use it; it is ignored under %q", CAModeSystemAndFile, CAModeFileOnly, CAModeSystem))
	}

	cert := strings.TrimSpace(o.ClientCert)
	key := strings.TrimSpace(o.ClientKey)
	if (cert == "") != (key == "") {
		errs = append(errs, errors.New("client_cert and client_key: both are required, or neither"))
	}

	if err := validateServerName(o.ServerName); err != nil {
		errs = append(errs, err)
	}

	if v := strings.TrimSpace(o.MinVersion); v != "" && v != MinVersion12 && v != MinVersion13 {
		errs = append(errs, fmt.Errorf("min_version: invalid value %q; expected %s", o.MinVersion, strings.Join(MinVersions(), " or ")))
	}

	seen := make(map[string]struct{}, len(o.PeerIdentities))
	for i, raw := range o.PeerIdentities {
		id, err := ParseIdentity(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("peer_identities[%d]: %v", i, err))
			continue
		}
		if _, dup := seen[id.String()]; dup {
			errs = append(errs, fmt.Errorf("peer_identities[%d]: duplicate identity %q", i, id.String()))
			continue
		}
		seen[id.String()] = struct{}{}
	}

	if o.InsecureSkipVerify {
		// The contradictions, and only the contradictions, are hard errors:
		// asking for an identity check while disabling the verification it
		// depends on, or supplying trust roots that would never be consulted.
		if len(o.PeerIdentities) > 0 {
			errs = append(errs, errors.New("insecure_skip_verify: cannot be combined with peer_identities; verification is disabled, so no identity can be proven"))
		}
		if mode != "" && mode != CAModeSystem {
			errs = append(errs, fmt.Errorf("insecure_skip_verify: cannot be combined with ca_mode %q; the trust roots would never be consulted", mode))
		}
	}
	return errs
}

// validateServerName rejects forms that cannot be an SNI value or a verified
// DNS identity.
func validateServerName(raw string) error {
	name := strings.TrimSpace(raw)
	if name == "" {
		return nil
	}
	if name != raw {
		return fmt.Errorf("server_name: %q has surrounding whitespace", raw)
	}
	if strings.Contains(name, "*") {
		return fmt.Errorf("server_name: %q must be a concrete name; a wildcard is matched from the certificate, not requested", raw)
	}
	if strings.ContainsAny(name, "/ \t") || strings.Contains(name, "://") {
		return fmt.Errorf("server_name: %q must be a host name, not a URL or path", raw)
	}
	if strings.Contains(name, ":") {
		return fmt.Errorf("server_name: %q must not carry a port", raw)
	}
	return nil
}
