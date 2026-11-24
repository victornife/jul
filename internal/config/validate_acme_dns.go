// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"encoding/base64"
	"fmt"
	"net"
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
		host, port, err := net.SplitHostPort(dns.Server)
		n, portErr := strconv.Atoi(port)
		if err != nil || host == "" || portErr != nil || n < 1 || n > 65535 ||
			(net.ParseIP(host) == nil && !validDNSName(host, false)) {
			errs = append(errs, fmt.Errorf("%s: server must be a DNS host or IP with a numeric port", dnsWhere))
		}
	}
	if !ContainsSecretRef(dns.Zone) && !validDNSName(dns.Zone, true) {
		errs = append(errs, fmt.Errorf("%s: zone must be a DNS zone name", dnsWhere))
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
	if len(domains) == 0 {
		domains = serverNames
	}
	if len(serverNames) == 0 {
		errs = append(errs, fmt.Errorf("%s: dns-01 requires server_names to scope SNI on the listener", where))
	}
	seen := make(map[string]bool)
	for _, domain := range domains {
		if ContainsSecretRef(domain) {
			continue
		}
		name := normalizeDNSName(domain)
		base := strings.TrimPrefix(name, "*.")
		if !validDNSName(base, false) || strings.Contains(base, "*") {
			errs = append(errs, fmt.Errorf("%s: invalid DNS-01 certificate name %q", where, domain))
			continue
		}
		seen[name] = true
		if !ContainsSecretRef(dns.Zone) {
			zone := normalizeDNSName(dns.Zone)
			owner := "_acme-challenge." + base
			if owner != zone && !strings.HasSuffix(owner, "."+zone) {
				errs = append(errs, fmt.Errorf("%s: DNS challenge name for %q is outside the configured zone", where, domain))
			}
		}
	}
	if len(seen) > 100 {
		errs = append(errs, fmt.Errorf("%s: dns-01 supports at most 100 certificate names per listener", where))
	}
	for _, name := range serverNames {
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
			errs = append(errs, fmt.Errorf("%s: server name %q is not covered by its configured ACME domains", where, name))
		}
	}
	return errs
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
	return a.Server == b.Server && a.Zone == b.Zone && a.TSIGKeyName == b.TSIGKeyName &&
		a.TSIGSecret == b.TSIGSecret && a.PropagationTimeout == b.PropagationTimeout &&
		a.PollInterval == b.PollInterval
}
