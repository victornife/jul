// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"net/netip"
	"strings"

	"golang.org/x/net/http/httpguts"
)

const (
	maxQuotaPolicies    = 8
	maxQuotaClasses     = 8
	maxQuotaPools       = 64
	maxQuotaOperations  = 8
	maxQuotaMappings    = 256
	maxQuotaPrefixes    = 256
	maxQuotaCallerState = 32_768
	maxQuotaRate        = 1_000_000_000
	maxQuotaCallers     = 4_096
	maxQuotaNameBytes   = 64
	maxQuotaValueBytes  = 1024
)

func validateQuotaPolicies(c *Config) []error {
	var errs []error
	if len(c.QuotaPolicies) > maxQuotaPolicies {
		errs = append(errs, fmt.Errorf("quota_policies: at most %d policies are supported", maxQuotaPolicies))
	}
	for name, policy := range c.QuotaPolicies {
		where := "quota_policies." + name
		if !validQuotaName(name) {
			errs = append(errs, fmt.Errorf("%s: name must be 1..%d ASCII letters, digits, '_' or '-'", where, maxQuotaNameBytes))
		}
		if policy.Mode != "enforce" && policy.Mode != "observe" {
			errs = append(errs, fmt.Errorf("%s.mode: must be enforce or observe", where))
		}
		if err := validateQuotaLimit(QuotaLimitConfig{Rate: policy.Rate, Burst: policy.Burst}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
		}
		if policy.MaxCallers < 1 || policy.MaxCallers > maxQuotaCallers {
			errs = append(errs, fmt.Errorf("%s.max_callers: must be between 1 and %d", where, maxQuotaCallers))
		}
		if len(policy.Classes) > maxQuotaClasses {
			errs = append(errs, fmt.Errorf("%s.classes: at most %d classes are supported", where, maxQuotaClasses))
		}
		if len(policy.Operations) > maxQuotaOperations {
			errs = append(errs, fmt.Errorf("%s.operations: at most %d operations are supported", where, maxQuotaOperations))
		}
		callerStores := 2 * (len(policy.Classes) + len(policy.Operations))
		if callerStores > 0 && policy.MaxCallers > maxQuotaCallerState/callerStores {
			errs = append(errs, fmt.Errorf("%s.max_callers: caller stores across enforcement and observation are limited to %d entries", where, maxQuotaCallerState))
		}
		if len(policy.Pools) > maxQuotaPools {
			errs = append(errs, fmt.Errorf("%s.pools: at most %d CIDR pools are supported", where, maxQuotaPools))
		}
		_, ok := policy.Classes["public"]
		if !ok {
			errs = append(errs, fmt.Errorf("%s.classes: required public fallback class is missing", where))
		}
		var allocatedRate, allocatedBurst int
		principalClass := make(map[string]string)
		principalCount := 0
		for className, class := range policy.Classes {
			classWhere := where + ".classes." + className
			if !validQuotaName(className) {
				errs = append(errs, fmt.Errorf("%s: invalid class name", classWhere))
			}
			if err := validateQuotaLimit(class.Shared); err != nil {
				errs = append(errs, fmt.Errorf("%s.shared: %w", classWhere, err))
			}
			if err := validateQuotaLimit(class.PerCaller); err != nil {
				errs = append(errs, fmt.Errorf("%s.per_caller: %w", classWhere, err))
			}
			if class.Shared.Rate > policy.Rate-allocatedRate || class.Shared.Burst > policy.Burst-allocatedBurst {
				errs = append(errs, fmt.Errorf("%s.shared: class allocations exceed the application rate or burst", classWhere))
			} else {
				allocatedRate += class.Shared.Rate
				allocatedBurst += class.Shared.Burst
			}
			if className == "public" && len(class.Principals) != 0 {
				errs = append(errs, fmt.Errorf("%s.principals: public is a fallback class and cannot be selected by identity", classWhere))
			}
			for i, principal := range class.Principals {
				principalCount++
				if err := validateQuotaPrincipalMatch(principal); err != nil {
					errs = append(errs, fmt.Errorf("%s.principals[%d]: %w", classWhere, i, err))
					continue
				}
				key := quotaPrincipalSourceKey(principal)
				if previous, ok := principalClass[key]; ok {
					errs = append(errs, fmt.Errorf("%s.principals[%d]: principal is already assigned to class %q", classWhere, i, previous))
				} else {
					principalClass[key] = className
				}
			}
		}
		if principalCount > maxQuotaMappings {
			errs = append(errs, fmt.Errorf("%s.classes: at most %d principal mappings are supported", where, maxQuotaMappings))
		}
		prefixClass := make(map[netip.Prefix]string)
		prefixCount := 0
		for poolName, pool := range policy.Pools {
			poolWhere := where + ".pools." + poolName
			if !validQuotaName(poolName) {
				errs = append(errs, fmt.Errorf("%s: invalid pool name", poolWhere))
			}
			if pool.Class == "" || pool.Class == "public" {
				errs = append(errs, fmt.Errorf("%s.class: must name a non-public class", poolWhere))
			} else if _, ok := policy.Classes[pool.Class]; !ok {
				errs = append(errs, fmt.Errorf("%s.class: unknown class %q", poolWhere, pool.Class))
			} else if len(policy.Classes[pool.Class].Principals) != 0 {
				errs = append(errs, fmt.Errorf("%s.class: class %q is reserved for verified principals and cannot be selected by CIDR", poolWhere, pool.Class))
			}
			if len(pool.CIDRs) == 0 {
				errs = append(errs, fmt.Errorf("%s.cidrs: at least one prefix is required", poolWhere))
			}
			for i, raw := range pool.CIDRs {
				prefixCount++
				prefix, err := quotaPrefix(raw)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s.cidrs[%d]: invalid CIDR %q", poolWhere, i, raw))
					continue
				}
				prefix = prefix.Masked()
				if previous, ok := prefixClass[prefix]; ok {
					errs = append(errs, fmt.Errorf("%s.cidrs[%d]: prefix %s is already assigned to pool for class %q", poolWhere, i, prefix, previous))
					continue
				}
				prefixClass[prefix] = pool.Class
			}
		}
		if prefixCount > maxQuotaPrefixes {
			errs = append(errs, fmt.Errorf("%s.pools: at most %d CIDR prefixes are supported", where, maxQuotaPrefixes))
		}
		for operationName, operation := range policy.Operations {
			opWhere := where + ".operations." + operationName
			if !validQuotaName(operationName) {
				errs = append(errs, fmt.Errorf("%s: invalid operation name", opWhere))
			}
			if err := validateQuotaLimit(operation.Shared); err != nil {
				errs = append(errs, fmt.Errorf("%s.shared: %w", opWhere, err))
			}
			if err := validateQuotaLimit(operation.PerCaller); err != nil {
				errs = append(errs, fmt.Errorf("%s.per_caller: %w", opWhere, err))
			}
			if operation.Shared.Rate > policy.Rate || operation.Shared.Burst > policy.Burst ||
				operation.PerCaller.Rate > policy.Rate || operation.PerCaller.Burst > policy.Burst {
				errs = append(errs, fmt.Errorf("%s: operation limits cannot exceed the application ceiling", opWhere))
			}
		}
	}

	usedPrincipals := make(map[string]bool)
	for i, server := range c.Servers {
		for j, location := range server.Locations {
			where := fmt.Sprintf("servers[%d].locations[%d]", i, j)
			if location.QuotaPolicy == "" {
				if location.QuotaOperation != "" {
					errs = append(errs, fmt.Errorf("%s.quota_operation: requires quota_policy", where))
				}
				continue
			}
			policy, ok := c.QuotaPolicies[location.QuotaPolicy]
			if !ok {
				errs = append(errs, fmt.Errorf("%s.quota_policy: unknown policy %q", where, location.QuotaPolicy))
				continue
			}
			if location.QuotaOperation != "" {
				if _, ok := policy.Operations[location.QuotaOperation]; !ok {
					errs = append(errs, fmt.Errorf("%s.quota_operation: unknown operation %q in policy %q", where, location.QuotaOperation, location.QuotaPolicy))
				}
			}
			for _, class := range policy.Classes {
				for _, principal := range class.Principals {
					if isQuotaPrincipalSource(location.Auth, principal) {
						usedPrincipals[location.QuotaPolicy+"\x00"+quotaPrincipalSourceKey(principal)] = true
					}
				}
			}
		}
	}
	for policyName, policy := range c.QuotaPolicies {
		for className, class := range policy.Classes {
			for _, principal := range class.Principals {
				key := policyName + "\x00" + quotaPrincipalSourceKey(principal)
				if !usedPrincipals[key] {
					errs = append(errs, fmt.Errorf("quota_policies.%s.classes.%s.principals: no attached route authenticates this principal authority", policyName, className))
				}
			}
		}
	}
	return errs
}

func quotaPrefix(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, err
	}
	if prefix.Addr().Is4In6() {
		if prefix.Bits() < 96 {
			return netip.Prefix{}, fmt.Errorf("IPv4-mapped IPv6 prefix must be at least /96")
		}
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
	}
	return prefix.Masked(), nil
}

func validateQuotaLimit(limit QuotaLimitConfig) error {
	if limit.Rate < 1 || limit.Burst < 1 || limit.Rate > maxQuotaRate || limit.Burst > maxQuotaRate {
		return fmt.Errorf("rate and burst must be between 1 and %d", maxQuotaRate)
	}
	return nil
}

func validateQuotaPrincipalMatch(principal QuotaPrincipalMatch) error {
	if principal.Method != "basic" && principal.Method != "jwt" && principal.Method != "forward" {
		return fmt.Errorf("method must be basic, jwt, or forward")
	}
	if !validQuotaValue(principal.Domain) || strings.TrimSpace(principal.Domain) != principal.Domain {
		return fmt.Errorf("domain must be nonempty and at most %d bytes", maxQuotaValueBytes)
	}
	if !validQuotaValue(principal.Subject) {
		return fmt.Errorf("subject must be nonempty and at most %d bytes", maxQuotaValueBytes)
	}
	if principal.Method == "jwt" {
		if !validQuotaValue(principal.Issuer) {
			return fmt.Errorf("JWT principal issuer must be nonempty and at most %d bytes", maxQuotaValueBytes)
		}
	} else if principal.Issuer != "" {
		return fmt.Errorf("issuer is supported only for JWT principals")
	}
	return nil
}

func validQuotaName(name string) bool {
	if len(name) == 0 || len(name) > maxQuotaNameBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

func validQuotaValue(value string) bool {
	if len(value) == 0 || len(value) > maxQuotaValueBytes {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validateAuthQuotaPrincipal(a *AuthConfig, where string) []error {
	qp := a.QuotaPrincipal
	if qp == nil {
		return nil
	}
	var errs []error
	if !validQuotaValue(qp.Domain) || strings.TrimSpace(qp.Domain) != qp.Domain {
		errs = append(errs, fmt.Errorf("%s.quota_principal.domain: must be nonempty and at most %d bytes", where, maxQuotaValueBytes))
	}
	methods := 0
	if a.Basic != nil {
		methods++
	}
	if a.JWT != nil {
		methods++
	}
	if a.ForwardAuth != nil {
		methods++
	}
	if methods != 1 {
		errs = append(errs, fmt.Errorf("%s.quota_principal: requires exactly one credential method", where))
		return errs
	}
	switch {
	case a.Basic != nil:
		if qp.ResponseHeader != "" {
			errs = append(errs, fmt.Errorf("%s.quota_principal.response_header: supported only with forward_auth", where))
		}
	case a.JWT != nil:
		if a.JWT.Issuer == "" || a.JWT.Audience == "" {
			errs = append(errs, fmt.Errorf("%s.quota_principal: JWT quota identity requires explicit issuer and audience", where))
		}
		if qp.ResponseHeader != "" {
			errs = append(errs, fmt.Errorf("%s.quota_principal.response_header: supported only with forward_auth", where))
		}
	case a.ForwardAuth != nil:
		if len(qp.ResponseHeader) > 128 || !httpguts.ValidHeaderFieldName(qp.ResponseHeader) {
			errs = append(errs, fmt.Errorf("%s.quota_principal.response_header: must be a valid HTTP header field name", where))
		} else if strings.EqualFold(qp.ResponseHeader, "Connection") ||
			strings.EqualFold(qp.ResponseHeader, "Transfer-Encoding") ||
			strings.EqualFold(qp.ResponseHeader, "Upgrade") ||
			strings.EqualFold(qp.ResponseHeader, "Content-Length") {
			errs = append(errs, fmt.Errorf("%s.quota_principal.response_header: must not be hop-by-hop", where))
		}
	}
	return errs
}

func isQuotaPrincipalSource(auth *AuthConfig, principal QuotaPrincipalMatch) bool {
	if auth == nil || auth.QuotaPrincipal == nil || auth.QuotaPrincipal.Domain != principal.Domain {
		return false
	}
	switch principal.Method {
	case "basic":
		return auth.Basic != nil && principal.Issuer == ""
	case "jwt":
		return auth.JWT != nil && auth.JWT.Issuer == principal.Issuer && auth.JWT.Audience != ""
	case "forward":
		return auth.ForwardAuth != nil && principal.Issuer == ""
	default:
		return false
	}
}

func quotaPrincipalSourceKey(p QuotaPrincipalMatch) string {
	return strings.Join([]string{p.Method, p.Domain, p.Issuer, p.Subject}, "\x00")
}
