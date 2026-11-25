// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"
)

func validateACMEDNS(a *ACMEConfig, serverNames []string, where string) []error {
	var errs []error
	if !acmeBuildEnabled {
		errs = append(errs, fmt.Errorf("%s: dns-01 requires a binary built with the acme tag", where))
	}
	if a.DNSProvider != "rfc2136" {
		errs = append(errs, fmt.Errorf("%s: dns-01 requires dns_provider = \"rfc2136\"", where))
	}
	dns := a.RFC2136
	if dns == nil {
		return append(errs, fmt.Errorf("%s: dns-01 requires an rfc2136 block", where))
	}
	dnsWhere := where + ".rfc2136"
	if !ContainsSecretRef(dns.Server) {
		if !validDNSHostPort(dns.Server) {
			errs = append(errs, fmt.Errorf("%s: server must be a DNS host or IP with a numeric port", dnsWhere))
		}
	}
	if len(dns.Observers) == 0 || len(dns.Observers) > MaxRFC2136Observers {
		errs = append(errs, fmt.Errorf("%s: observers must contain 1 to %d CA-facing authoritative DNS endpoints", dnsWhere, MaxRFC2136Observers))
	}
	seenObservers := make(map[string]bool, len(dns.Observers))
	for _, observer := range dns.Observers {
		if ContainsSecretRef(observer) {
			continue
		}
		if !validDNSHostPort(observer) {
			errs = append(errs, fmt.Errorf("%s: each observer must be a DNS host or IP with a numeric port", dnsWhere))
		}
		key := strings.ToLower(observer)
		if seenObservers[key] {
			errs = append(errs, fmt.Errorf("%s: observers must not contain duplicates", dnsWhere))
		}
		seenObservers[key] = true
	}
	if !ContainsSecretRef(dns.Zone) && !validDNSName(dns.Zone, true) {
		errs = append(errs, fmt.Errorf("%s: zone must be a DNS zone name", dnsWhere))
	}
	delegations := make(map[string]string, len(dns.CNAMEDelegations))
	if len(dns.CNAMEDelegations) > 100 {
		errs = append(errs, fmt.Errorf("%s: cname_delegations supports at most 100 challenge owners", dnsWhere))
	}
	for rawOwner, delegation := range dns.CNAMEDelegations {
		owner, target := normalizeDNSName(rawOwner), normalizeDNSName(delegation.Target)
		if len(delegation.Observers) == 0 || len(delegation.Observers) > MaxRFC2136Observers {
			errs = append(errs, fmt.Errorf("%s: CNAME observers must contain 1 to %d authoritative endpoints", dnsWhere, MaxRFC2136Observers))
		}
		seen := make(map[string]bool, len(delegation.Observers))
		for _, observer := range delegation.Observers {
			if !ContainsSecretRef(observer) && !validDNSHostPort(observer) {
				errs = append(errs, fmt.Errorf("%s: each CNAME observer must be a DNS host or IP with a numeric port", dnsWhere))
			}
			key := strings.ToLower(observer)
			if seen[key] {
				errs = append(errs, fmt.Errorf("%s: CNAME observers must not contain duplicates", dnsWhere))
			}
			seen[key] = true
		}
		if !strings.HasPrefix(owner, "_acme-challenge.") || !validDNSName(owner, true) ||
			!validDNSName(target, true) || owner == target {
			errs = append(errs, fmt.Errorf("%s: each cname_delegations entry needs a challenge owner and a distinct DNS target", dnsWhere))
			continue
		}
		if _, exists := delegations[owner]; exists {
			errs = append(errs, fmt.Errorf("%s: cname_delegations contains a duplicate challenge owner", dnsWhere))
		}
		delegations[owner] = target
		if !ContainsSecretRef(dns.Zone) {
			zone := normalizeDNSName(dns.Zone)
			if target != zone && !strings.HasSuffix(target, "."+zone) {
				errs = append(errs, fmt.Errorf("%s: CNAME target must be inside the configured update zone", dnsWhere))
			}
		}
	}
	if !ContainsSecretRef(dns.TSIGKeyName) && !validDNSName(dns.TSIGKeyName, true) {
		errs = append(errs, fmt.Errorf("%s: tsig_key_name must be a DNS key name", dnsWhere))
	}
	switch {
	case dns.TSIGSecret == "":
		errs = append(errs, fmt.Errorf("%s: tsig_secret requires a secret reference", dnsWhere))
	case !dns.secretResolved && !isSingleSecretRef(dns.TSIGSecret):
		errs = append(errs, fmt.Errorf("%s: tsig_secret must be a single ${env:NAME} or ${file:/path} reference", dnsWhere))
	case dns.secretResolved:
		key, err := base64.StdEncoding.DecodeString(dns.TSIGSecret)
		if err != nil || len(key) < 16 {
			errs = append(errs, fmt.Errorf("%s: resolved TSIG secret must be base64-encoded and at least 16 bytes", dnsWhere))
		}
	}
	timeout, interval := dns.PropagationTimeout.Std(), dns.PollInterval.Std()
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	if interval == 0 {
		interval = 2 * time.Second
	}
	if timeout < time.Second || timeout > 15*time.Minute {
		errs = append(errs, fmt.Errorf("%s: propagation_timeout must be between 1s and 15m", dnsWhere))
	}
	if interval < 250*time.Millisecond || interval > 30*time.Second || interval > timeout {
		errs = append(errs, fmt.Errorf("%s: poll_interval must be between 250ms and 30s and not exceed propagation_timeout", dnsWhere))
	}
	domains := a.Domains
	serverNamesWhere := strings.TrimSuffix(where, ".tls.acme") + ".server_names"
	domainsWhere := where + ".domains"
	if len(domains) == 0 {
		domains = serverNames
		domainsWhere = serverNamesWhere
	}
	if len(serverNames) == 0 {
		errs = append(errs, fmt.Errorf("%s: dns-01 requires server_names to scope SNI on the listener", where))
	}
	seen := make(map[string]bool)
	for i, domain := range domains {
		if ContainsSecretRef(domain) {
			continue
		}
		name := normalizeDNSName(domain)
		base := strings.TrimPrefix(name, "*.")
		if !validDNSName(base, false) || strings.Contains(base, "*") {
			errs = append(errs, fmt.Errorf("%s[%d]: invalid DNS-01 certificate name", domainsWhere, i))
			continue
		}
		seen[name] = true
		if !ContainsSecretRef(dns.Zone) {
			zone := normalizeDNSName(dns.Zone)
			owner := "_acme-challenge." + base
			_, delegated := delegations[owner]
			if !delegated && owner != zone && !strings.HasSuffix(owner, "."+zone) {
				errs = append(errs, fmt.Errorf("%s[%d]: DNS challenge name is outside the configured zone", domainsWhere, i))
			}
		}
	}
	if len(seen) > 100 {
		errs = append(errs, fmt.Errorf("%s: dns-01 supports at most 100 certificate names per listener", where))
	}
	for i, name := range serverNames {
		if ContainsSecretRef(name) {
			continue
		}
		covered := false
		for _, domain := range domains {
			if ContainsSecretRef(domain) || dnsNameCovers(domain, name) {
				covered = true
				break
			}
		}
		if !covered {
			errs = append(errs, fmt.Errorf("%s[%d]: server name is not covered by its configured ACME domains", serverNamesWhere, i))
		}
	}
	return errs
}

func validDNSHostPort(endpoint string) bool {
	host, port, err := net.SplitHostPort(endpoint)
	n, portErr := strconv.Atoi(port)
	return err == nil && host != "" && portErr == nil && n >= 1 && n <= 65535 &&
		(net.ParseIP(host) != nil || validDNSName(host, false))
}

func normalizeDNSName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

func validDNSName(name string, allowUnderscore bool) bool {
	name = normalizeDNSName(name)
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' && (!allowUnderscore || ch != '_') {
				return false
			}
		}
	}
	return true
}

func dnsNameCovers(pattern, name string) bool {
	pattern, name = normalizeDNSName(pattern), normalizeDNSName(name)
	if pattern == name {
		return true
	}
	if !strings.HasPrefix(pattern, "*.") || strings.HasPrefix(name, "*.") {
		return false
	}
	i := strings.IndexByte(name, '.')
	return i > 0 && name[i+1:] == pattern[2:]
}

func equalRFC2136(a, b *RFC2136Config) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.CNAMEDelegations) != len(b.CNAMEDelegations) {
		return false
	}
	for owner, delegation := range a.CNAMEDelegations {
		other, ok := b.CNAMEDelegations[owner]
		if !ok || delegation.Target != other.Target || !slices.Equal(delegation.Observers, other.Observers) {
			return false
		}
	}
	return a.Server == b.Server && slices.Equal(a.Observers, b.Observers) &&
		a.Zone == b.Zone && a.TSIGKeyName == b.TSIGKeyName &&
		a.TSIGSecret == b.TSIGSecret && a.PropagationTimeout == b.PropagationTimeout &&
		a.PollInterval == b.PollInterval
}
