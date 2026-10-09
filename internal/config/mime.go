// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"mime"
	"strings"
)

// ResolveMIME copies and resolves policies in global, server, location order.
// Compiled handlers never retain a caller-owned mutable extension map.
func ResolveMIME(scopes ...*MIMEConfig) *MIMEConfig {
	var out *MIMEConfig
	for _, scope := range scopes {
		if scope == nil {
			continue
		}
		if out == nil {
			out = &MIMEConfig{}
		}
		if scope.Types != nil {
			types := make(map[string]string, len(*scope.Types))
			for ext, typ := range *scope.Types {
				types[ext] = typ
			}
			out.Types = &types
		}
		if scope.DefaultType != "" {
			out.DefaultType = scope.DefaultType
		}
	}
	return out
}

func validateMIME(policy *MIMEConfig, where string) []error {
	if policy == nil {
		return nil
	}
	var errs []error

	if policy.DefaultType != "" && !ValidMIMEType(policy.DefaultType) {
		errs = append(errs, fmt.Errorf("%s.mime.default_type: invalid media type %q", where, policy.DefaultType))
	}
	if policy.Types != nil {
		if len(*policy.Types) > 4096 {
			errs = append(errs, fmt.Errorf("%s.mime.types: at most 4096 extensions are allowed", where))
		}
		for ext, typ := range *policy.Types {
			validExt := len(ext) > 1 && len(ext) <= 64 && ext[0] == '.' && ext == strings.ToLower(ext)
			suffix := strings.TrimPrefix(ext, ".")
			for _, c := range suffix {
				if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' && c != '+' {
					validExt = false
				}
			}
			if !validExt {
				errs = append(errs, fmt.Errorf("%s.mime.types: invalid lowercase dotted extension %q", where, ext))
			}
			if !ValidMIMEType(typ) {
				errs = append(errs, fmt.Errorf("%s.mime.types[%q]: invalid media type %q", where, ext, typ))
			}
		}
	}
	return errs
}

// ValidMIMEType checks the bounded media-type grammar shared by configuration
// validation and migration; header injection is rejected before MIME parsing.
func ValidMIMEType(value string) bool {
	if len(value) > 256 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	_, _, err := mime.ParseMediaType(value)
	return err == nil && strings.Contains(value, "/")
}
