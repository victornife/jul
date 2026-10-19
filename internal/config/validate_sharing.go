// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SharingMode values (ADR 0026 Decision 2, spec §4.5).
const (
	SharingModeNotDeclared    = "not_declared"
	SharingModePreview        = "preview"
	SharingModePrivateNetwork = "private_network"
	SharingModeRestricted     = "restricted"
	SharingModePublic         = "public"
	SharingModeUnknown        = "unknown"
)

// BindScope values derived from a literal listen host (spec §4.4).
const (
	BindScopeLoopback           = "loopback"
	BindScopePrivateAddress     = "private_address"
	BindScopeSharedAddressSpace = "shared_address_space"
	BindScopeAllInterfaces      = "all_interfaces"
	BindScopePublicAddress      = "public_address"
	BindScopeUnknown            = "unknown"
)

// Closed finding codes for sharing intent and cross-entry cache sharing (spec §4.4, §4.5).
const (
	FindingIntentRestrictedUnenforced   = "intent_restricted_unenforced"
	FindingIntentRestrictedExternalOnly = "intent_restricted_external_only"
	FindingIntentPrivateBindTooWide     = "intent_private_bind_too_wide"
	FindingIntentPreviewNotLoopback     = "intent_preview_not_loopback"
	FindingIntentPublicHasChecks        = "intent_public_has_checks"
	FindingIntentChecksBypassed         = "intent_checks_bypassed"
	FindingIntentRestrictedCached       = "intent_restricted_cached"
	FindingCacheSharedAcrossEntries     = "cache_shared_across_entries"
)

const sharingLabelMaxLen = 64

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// ValidSharingMode reports whether mode is a valid declared [servers.locations.sharing].mode.
func ValidSharingMode(mode string) bool {
	switch mode {
	case SharingModePreview, SharingModePrivateNetwork, SharingModeRestricted, SharingModePublic:
		return true
	default:
		return false
	}
}

// SharingModeRank returns the audience breadth rank of a declared sharing mode
// (preview < private_network < restricted < public). Returns 0 when not declared
// or unknown.
func SharingModeRank(mode string) int {
	switch mode {
	case SharingModePreview:
		return 1
	case SharingModePrivateNetwork:
		return 2
	case SharingModeRestricted:
		return 3
	case SharingModePublic:
		return 4
	default:
		return 0
	}
}

// IsSharingModeWider reports whether afterMode declares a wider intended
// audience than beforeMode.
func IsSharingModeWider(beforeMode, afterMode string) bool {
	rBefore := SharingModeRank(beforeMode)
	rAfter := SharingModeRank(afterMode)
	if rAfter == 0 {
		return false
	}
	if rBefore == 0 {
		// Declaring public on a previously undeclared route is treated as a wider
		// declared mode when transitioning into public; declaring restricted or
		// narrower from not_declared is narrowing/bounding intent.
		return afterMode == SharingModePublic
	}
	return rAfter > rBefore
}

// validateSharing validates an optional [servers.locations.sharing] block (spec §4.5).
func validateSharing(s *SharingConfig, routeID *string, where string) []error {
	if s == nil {
		return nil
	}
	var errs []error
	if routeID == nil || strings.TrimSpace(*routeID) == "" {
		errs = append(errs, fmt.Errorf("%s.sharing: [servers.locations.sharing] requires a non-empty route_id on the same location", where))
	}
	if s.Mode == "" {
		errs = append(errs, fmt.Errorf("%s.sharing.mode: mode is required when [servers.locations.sharing] is present (want preview | private_network | restricted | public)", where))
	} else if !ValidSharingMode(s.Mode) {
		errs = append(errs, fmt.Errorf("%s.sharing.mode: invalid mode %q (want preview | private_network | restricted | public)", where, s.Mode))
	}
	if s.AudienceLabel != nil {
		if err := validateSharingLabel(*s.AudienceLabel, where+".sharing.audience_label"); err != nil {
			errs = append(errs, err)
		}
	}
	if s.ExternalEnforcer != nil {
		if err := validateSharingLabel(*s.ExternalEnforcer, where+".sharing.external_enforcer"); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func validateSharingLabel(v, field string) error {
	if v == "" {
		return fmt.Errorf("%s: value is present and empty; omit the key or set 1 to %d bytes of printable UTF-8", field, sharingLabelMaxLen)
	}
	if len(v) > sharingLabelMaxLen {
		return fmt.Errorf("%s: value %q is %d bytes, want at most %d", field, v, len(v), sharingLabelMaxLen)
	}
	if !utf8.ValidString(v) {
		return fmt.Errorf("%s: value must be valid UTF-8", field)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f || unicode.IsControl(r) || !unicode.IsPrint(r) {
			return fmt.Errorf("%s: value contains control or non-printable character %U", field, r)
		}
	}
	return nil
}

// BindScope classifies a server listen address into the closed-set bind_scope
// vocabulary of spec §4.4:
//   - 127.0.0.0/8, ::1, localhost => "loopback"
//   - RFC 1918, fc00::/7, link-local (169.254.0.0/16, fe80::/10) => "private_address"
//   - 100.64.0.0/10 => "shared_address_space"
//   - empty host, 0.0.0.0, :: => "all_interfaces"
//   - any other literal IP => "public_address"
//   - otherwise (unexpanded reference, non-literal hostname, malformed) => "unknown"
func BindScope(listen string) string {
	trimmed := strings.TrimSpace(listen)
	if trimmed == "" || ContainsSecretRef(trimmed) {
		return BindScopeUnknown
	}
	host, _, err := net.SplitHostPort(trimmed)
	if err != nil {
		return BindScopeUnknown
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return BindScopeAllInterfaces
	}
	if strings.EqualFold(host, "localhost") {
		return BindScopeLoopback
	}
	// Strip optional zone suffix for link-local IPv6 addresses (e.g. fe80::1%eth0).
	hostNoZone := host
	if idx := strings.IndexByte(hostNoZone, '%'); idx >= 0 {
		hostNoZone = hostNoZone[:idx]
	}
	addr, err := netip.ParseAddr(hostNoZone)
	if err != nil {
		return BindScopeUnknown
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() {
		return BindScopeAllInterfaces
	}
	if addr.IsLoopback() {
		return BindScopeLoopback
	}
	if addr.Is4() && cgnatPrefix.Contains(addr) {
		return BindScopeSharedAddressSpace
	}
	if addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() {
		return BindScopePrivateAddress
	}
	return BindScopePublicAddress
}

// NormalizeHostPattern normalizes a server_names entry with the exact rule used
// by internal/router/router.go (`strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")`).
func NormalizeHostPattern(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

// IsCatchAllServerNames reports whether serverNames matches any host on the
// listener (empty list or containing "*").
func IsCatchAllServerNames(serverNames []string) bool {
	if len(serverNames) == 0 {
		return true
	}
	for _, raw := range serverNames {
		if NormalizeHostPattern(raw) == "*" {
			return true
		}
	}
	return false
}

// HostPatternsOverlap reports whether two server_names patterns can match the
// same normalized request Host header under router.matchHost semantics (spec §9.5).
func HostPatternsOverlap(a, b string) bool {
	na := NormalizeHostPattern(a)
	nb := NormalizeHostPattern(b)
	if na == "" || nb == "" {
		return false
	}
	if na == "*" || nb == "*" {
		return true
	}
	aWild := strings.HasPrefix(na, "*.")
	bWild := strings.HasPrefix(nb, "*.")
	switch {
	case !aWild && !bWild:
		return na == nb
	case aWild && !bWild:
		suffix := na[2:]
		return suffix != "" && strings.HasSuffix(nb, "."+suffix) && len(nb) > len(suffix)+1
	case !aWild && bWild:
		suffix := nb[2:]
		return suffix != "" && strings.HasSuffix(na, "."+suffix) && len(na) > len(suffix)+1
	default:
		sa := na[2:]
		sb := nb[2:]
		if sa == "" || sb == "" {
			return false
		}
		return sa == sb || strings.HasSuffix(sa, "."+sb) || strings.HasSuffix(sb, "."+sa)
	}
}

// ServerHostSetsOverlap reports whether two server blocks' server_names sets
// overlap (including catch-all blocks).
func ServerHostSetsOverlap(a, b []string) bool {
	if IsCatchAllServerNames(a) || IsCatchAllServerNames(b) {
		return true
	}
	for _, ha := range a {
		for _, hb := range b {
			if HostPatternsOverlap(ha, hb) {
				return true
			}
		}
	}
	return false
}

// ServerHostSetsOverlapLiteralOnly reports whether two server blocks'
// server_names sets overlap using only literal patterns or identical reference
// tokens, preserving RR11 disclosure rules so a caller-chosen host pattern
// cannot act as an oracle for a hidden reference value.
func ServerHostSetsOverlapLiteralOnly(a, b []string) bool {
	if IsCatchAllServerNames(a) || IsCatchAllServerNames(b) {
		return true
	}
	for _, ha := range a {
		for _, hb := range b {
			if ContainsSecretRef(ha) || ContainsSecretRef(hb) {
				if strings.TrimSpace(ha) != "" && strings.TrimSpace(ha) == strings.TrimSpace(hb) {
					return true
				}
				continue
			}
			if HostPatternsOverlap(ha, hb) {
				return true
			}
		}
	}
	return false
}

// MatchPathsOverlapLiteralOnly reports whether two location match configurations
// overlap without comparing a secret reference against a different literal (RR11).
func MatchPathsOverlapLiteralOnly(a, b MatchConfig) bool {
	if ContainsSecretRef(a.Path) || ContainsSecretRef(b.Path) {
		ta := a.Type
		if ta == "" {
			ta = "prefix"
		}
		tb := b.Type
		if tb == "" {
			tb = "prefix"
		}
		return ta == tb && strings.TrimSpace(a.Path) != "" && strings.TrimSpace(a.Path) == strings.TrimSpace(b.Path)
	}
	return MatchPathsOverlap(a, b)
}

// MatchPathsOverlap reports whether two location match configurations can match
// an overlapping request path.
func MatchPathsOverlap(a, b MatchConfig) bool {
	ta := a.Type
	if ta == "" {
		ta = "prefix"
	}
	tb := b.Type
	if tb == "" {
		tb = "prefix"
	}
	pa := a.Path
	pb := b.Path
	if ta == "regex" || tb == "regex" {
		// Regex overlap is treated conservatively when paths are identical or when
		// either regex is combined with a root prefix.
		return pa == pb || (ta == "prefix" && pa == "/") || (tb == "prefix" && pb == "/")
	}
	switch {
	case ta == "exact" && tb == "exact":
		return pa == pb
	case ta == "prefix" && tb == "prefix":
		return strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa)
	case ta == "prefix" && tb == "exact":
		return strings.HasPrefix(pb, pa)
	case ta == "exact" && tb == "prefix":
		return strings.HasPrefix(pa, pb)
	default:
		return pa == pb
	}
}

// LocationHasNetworkCheck reports whether a location configures a CIDR allow or
// deny list.
func LocationHasNetworkCheck(loc LocationConfig) bool {
	return loc.Auth != nil && (len(loc.Auth.Allow) > 0 || len(loc.Auth.Deny) > 0)
}

// LocationHasCredentialCheck reports whether a location configures HTTP Basic,
// JWT, or forward_auth.
func LocationHasCredentialCheck(loc LocationConfig) bool {
	if loc.Auth == nil {
		return false
	}
	return loc.Auth.Basic != nil || loc.Auth.JWT != nil || loc.Auth.ForwardAuth != nil
}

// LocationHasClientCertCheck reports whether a location requires a verified
// client certificate (either via listener client_auth.mode = "require" or
// location require_client_cert = true with active listener client_auth).
func LocationHasClientCertCheck(srv ServerConfig, loc LocationConfig) bool {
	if srv.TLS == nil || !srv.TLS.Enabled || srv.TLS.ClientAuth == nil {
		return false
	}
	switch srv.TLS.ClientAuth.Mode {
	case "require":
		return true
	case "request":
		return loc.RequireClientCert
	default:
		return false
	}
}

// LocationHasClientCertCheckInConfig reports whether a location requires a
// verified client certificate when taking into account listener-scoped
// client_auth across all server blocks sharing srv.Listen.
func LocationHasClientCertCheckInConfig(c *Config, srv ServerConfig, loc LocationConfig) bool {
	if LocationHasClientCertCheck(srv, loc) {
		return true
	}
	if c == nil {
		return false
	}
	for _, other := range c.Servers {
		if other.Listen != srv.Listen {
			continue
		}
		if LocationHasClientCertCheck(other, loc) {
			return true
		}
	}
	return false
}

// LocationHasClientCertCheckForPlan evaluates listener-scoped client-certificate
// enforcement for servers[srvIdx] while applying §9.5 / RR12 file-reference
// suppression across every block sharing the listener. It returns
// (hasCertCheck, hasFileRefOnListener).
func LocationHasClientCertCheckForPlan(rawCfg, effCfg *Config, srvIdx int, effLoc LocationConfig) (bool, bool) {
	if rawCfg == nil {
		rawCfg = effCfg
	}
	if effCfg == nil {
		effCfg = rawCfg
	}
	if rawCfg == nil || srvIdx < 0 || srvIdx >= len(rawCfg.Servers) {
		return false, false
	}
	rawSrv := rawCfg.Servers[srvIdx]
	effSrv := rawSrv
	if srvIdx < len(effCfg.Servers) {
		effSrv = effCfg.Servers[srvIdx]
	}
	_, rawSrvListenHasFile := SecretRefSchemes(rawSrv.Listen)

	hasCert := false
	for i := range rawCfg.Servers {
		rOther := rawCfg.Servers[i]
		eOther := rOther
		if i < len(effCfg.Servers) {
			eOther = effCfg.Servers[i]
		}
		sameRawListen := strings.TrimSpace(rOther.Listen) != "" && rOther.Listen == rawSrv.Listen
		sameEffListen := strings.TrimSpace(eOther.Listen) != "" && eOther.Listen == effSrv.Listen
		if i != srvIdx && !sameRawListen {
			if !sameEffListen {
				continue
			}
			_, otherListenHasFile := SecretRefSchemes(rOther.Listen)
			if rawSrvListenHasFile || otherListenHasFile {
				if (rOther.TLS != nil && rOther.TLS.ClientAuth != nil) || LocationHasClientCertCheck(eOther, effLoc) {
					return false, true
				}
				continue
			}
		}
		if rOther.TLS != nil && rOther.TLS.ClientAuth != nil {
			if _, hasFile := SecretRefSchemes(rOther.TLS.ClientAuth.Mode); hasFile {
				return false, true
			}
		}
		if LocationHasClientCertCheck(eOther, effLoc) {
			hasCert = true
		}
	}
	return hasCert, false
}

// LocationHasAnyJulCheck reports whether a location has at least one Jul-enforced
// network, credential, or client-certificate check.
func LocationHasAnyJulCheck(srv ServerConfig, loc LocationConfig) bool {
	return LocationHasNetworkCheck(loc) || LocationHasCredentialCheck(loc) || LocationHasClientCertCheck(srv, loc)
}

// LocationCheckBypassKinds returns the ordered list of outer-stage mechanisms
// ("redirect_rewrite", "request_plugin", "cors_preflight") that execute before
// per-location access checks on loc.
func LocationCheckBypassKinds(srv ServerConfig, loc LocationConfig) []string {
	var kinds []string
	for _, rw := range loc.Rewrites {
		if rw.Flag == "redirect" || rw.Flag == "permanent" {
			kinds = append(kinds, "redirect_rewrite")
			break
		}
	}
	if len(srv.Plugins) > 0 || len(loc.Plugins) > 0 || strings.TrimSpace(loc.Plugin) != "" {
		kinds = append(kinds, "request_plugin")
	}
	if loc.CORS != nil && loc.CORS.Enabled {
		kinds = append(kinds, "cors_preflight")
	}
	return kinds
}

// LintLocationSharingFindings evaluates the closed intent-finding rules for one
// location (spec §4.5). It returns findings and a boolean reporting whether all
// inputs required to evaluate the location's intent findings were known.
func LintLocationSharingFindings(c *Config, srvIdx, locIdx int) ([]Diagnostic, bool) {
	return LintLocationSharingFindingsWithRaw(c, c, srvIdx, locIdx, false)
}

// LintLocationSharingFindingsWithRaw evaluates the closed intent-finding rules
// for one location using rawCfg for all display text (never exposing resolved
// secret references). When forPlan is true, all checks are evaluated strictly
// against rawCfg so plan findings never depend on or disclose resolved
// ${env:...} or ${file:...} reference values (spec §9.5, RR11). When forPlan is
// false (the serving layer of GET /api/sharing, spec §4.3), closed-set facts
// are derived from effCfg while display strings still come from rawCfg.
func LintLocationSharingFindingsWithRaw(rawCfg, effCfg *Config, srvIdx, locIdx int, forPlan bool) ([]Diagnostic, bool) {
	if rawCfg == nil {
		rawCfg = effCfg
	}
	if effCfg == nil {
		effCfg = rawCfg
	}
	if rawCfg == nil || srvIdx < 0 || srvIdx >= len(rawCfg.Servers) {
		return nil, true
	}
	rawSrv := rawCfg.Servers[srvIdx]
	if locIdx < 0 || locIdx >= len(rawSrv.Locations) {
		return nil, true
	}
	rawLoc := rawSrv.Locations[locIdx]
	if rawLoc.Sharing == nil {
		return nil, true
	}

	effSrv := rawSrv
	effLoc := rawLoc
	if srvIdx < len(effCfg.Servers) {
		effSrv = effCfg.Servers[srvIdx]
		if locIdx < len(effSrv.Locations) {
			effLoc = effSrv.Locations[locIdx]
		}
	}

	field := fmt.Sprintf("servers[%d].locations[%d].sharing", srvIdx, locIdx)
	rawMode := strings.TrimSpace(rawLoc.Sharing.Mode)
	if forPlan {
		if _, hasFile := SecretRefSchemes(rawMode); hasFile {
			return nil, false
		}
	}
	mode := rawMode
	if ContainsSecretRef(rawMode) {
		if effLoc.Sharing != nil && !ContainsSecretRef(effLoc.Sharing.Mode) {
			mode = strings.TrimSpace(effLoc.Sharing.Mode)
		} else {
			return nil, false
		}
	}

	complete := true
	bindScope := BindScope(effSrv.Listen)
	if forPlan {
		if _, hasFile := SecretRefSchemes(rawSrv.Listen); hasFile {
			bindScope = BindScopeUnknown
		}
	}
	if bindScope == BindScopeUnknown {
		complete = false
	}

	displayListen := rawSrv.Listen
	enforcer := strings.TrimSpace(rawLoc.Sharing.ExternalEnforcerValue())

	var diags []Diagnostic
	hasNetCheck := LocationHasNetworkCheck(effLoc)
	if forPlan && rawLoc.Auth != nil {
		for _, cidr := range append(append([]string(nil), rawLoc.Auth.Allow...), rawLoc.Auth.Deny...) {
			if _, hasFile := SecretRefSchemes(cidr); hasFile {
				complete = false
				break
			}
		}
	}
	hasCredCheck := LocationHasCredentialCheck(effLoc)
	var hasCertCheck bool
	if forPlan {
		var certHasFile bool
		hasCertCheck, certHasFile = LocationHasClientCertCheckForPlan(rawCfg, effCfg, srvIdx, effLoc)
		if certHasFile {
			complete = false
		}
	} else {
		hasCertCheck = LocationHasClientCertCheckInConfig(effCfg, effSrv, effLoc)
	}
	hasJulCheck := hasNetCheck || hasCredCheck || hasCertCheck
	hasCredOrCert := hasCredCheck || hasCertCheck

	switch mode {
	case SharingModeRestricted:
		if !effLoc.Deny && !hasJulCheck {
			msg := "location declares sharing.mode = \"restricted\" but configures no Jul access check (auth.allow, auth.basic, auth.jwt, auth.forward_auth, or client certificate requirement)"
			hint := "configure a Jul access check on this location, or verify origin protection if an external gate enforces access"
			if enforcer != "" {
				msg = fmt.Sprintf("location declares sharing.mode = \"restricted\" with no Jul access check; external_enforcer %q is declared by the operator, not verified by Jul, and origin-protection evidence is required", enforcer)
			}
			diags = append(diags, Diagnostic{
				Code:     FindingIntentRestrictedUnenforced,
				Severity: SeverityWarning,
				Field:    field,
				Message:  msg,
				Hint:     hint,
			})
		}
	case SharingModePrivateNetwork:
		if bindScope == BindScopeAllInterfaces || bindScope == BindScopePublicAddress {
			diags = append(diags, Diagnostic{
				Code:     FindingIntentPrivateBindTooWide,
				Severity: SeverityWarning,
				Field:    field,
				Message:  fmt.Sprintf("location declares sharing.mode = \"private_network\" on listener %q with bind scope %s", displayListen, bindScope),
				Hint:     "bind the listener to a loopback, RFC 1918 private, link-local, or 100.64.0.0/10 address, or add an explicit auth.allow CIDR check",
			})
		}
	case SharingModePreview:
		if bindScope != BindScopeLoopback && bindScope != BindScopeUnknown {
			diags = append(diags, Diagnostic{
				Code:     FindingIntentPreviewNotLoopback,
				Severity: SeverityWarning,
				Field:    field,
				Message:  fmt.Sprintf("location declares sharing.mode = \"preview\" on non-loopback listener %q (bind scope %s)", displayListen, bindScope),
				Hint:     "bind the listener to 127.0.0.1, ::1, or localhost for local preview, or update sharing.mode",
			})
		}
	case SharingModePublic:
		if hasCredOrCert {
			diags = append(diags, Diagnostic{
				Code:     FindingIntentPublicHasChecks,
				Severity: SeverityInfo,
				Field:    field,
				Message:  "location declares sharing.mode = \"public\" but configures a Jul credential or client-certificate requirement",
				Hint:     "set sharing.mode = \"restricted\" if access is intentionally gated, or remove the check if the entry should be anonymous",
			})
		}
	}

	if (mode == SharingModeRestricted || mode == SharingModePrivateNetwork) && !effLoc.Deny {
		if bypass := LocationCheckBypassKinds(effSrv, effLoc); len(bypass) > 0 {
			diags = append(diags, Diagnostic{
				Code:     FindingIntentChecksBypassed,
				Severity: SeverityWarning,
				Field:    field,
				Message:  fmt.Sprintf("location declares sharing.mode = %q while %s runs before per-location access checks", mode, strings.Join(bypass, ", ")),
				Hint:     "review redirect rewrites, request plugins, and CORS preflight on this location because they execute before per-location auth and certificate checks",
			})
		}
		if effCfg.Cache.Enabled && effLoc.Cache {
			diags = append(diags, Diagnostic{
				Code:     FindingIntentRestrictedCached,
				Severity: SeverityWarning,
				Field:    field,
				Message:  fmt.Sprintf("location declares sharing.mode = %q with response caching enabled", mode),
				Hint:     "disable cache on this location unless cached responses are intentionally shared across permitted callers",
			})
		}
	}

	return diags, complete
}

// LintSharingFindings returns all location sharing-intent diagnostics and
// cross-entry cache-sharing diagnostics for c.
func LintSharingFindings(c *Config) []Diagnostic {
	return LintSharingFindingsWithRaw(c, c, false)
}

// LintSharingFindingsWithRaw returns all location sharing-intent diagnostics
// and cross-entry cache-sharing diagnostics using rawCfg for display strings
// and literal host/path comparisons and effCfg for closed-set facts.
func LintSharingFindingsWithRaw(rawCfg, effCfg *Config, forPlan bool) []Diagnostic {
	if rawCfg == nil {
		rawCfg = effCfg
	}
	if effCfg == nil {
		effCfg = rawCfg
	}
	if rawCfg == nil {
		return nil
	}
	var diags []Diagnostic
	for i := range rawCfg.Servers {
		for j := range rawCfg.Servers[i].Locations {
			locDiags, _ := LintLocationSharingFindingsWithRaw(rawCfg, effCfg, i, j, forPlan)
			diags = append(diags, locDiags...)
		}
	}
	diags = append(diags, lintSharedCacheAcrossEntriesWithRaw(rawCfg, effCfg)...)
	return diags
}

type cacheEnabledLocRef struct {
	srvIdx int
	locIdx int
	rawSrv ServerConfig
	rawLoc LocationConfig
}

func lintSharedCacheAcrossEntriesWithRaw(rawCfg, effCfg *Config) []Diagnostic {
	if rawCfg == nil {
		rawCfg = effCfg
	}
	if effCfg == nil {
		effCfg = rawCfg
	}
	if rawCfg == nil || !effCfg.Cache.Enabled {
		return nil
	}
	var refs []cacheEnabledLocRef
	for i, rawSrv := range rawCfg.Servers {
		for j, rawLoc := range rawSrv.Locations {
			effLoc := rawLoc
			if i < len(effCfg.Servers) && j < len(effCfg.Servers[i].Locations) {
				effLoc = effCfg.Servers[i].Locations[j]
			}
			if effLoc.Cache && !effLoc.Deny {
				refs = append(refs, cacheEnabledLocRef{
					srvIdx: i,
					locIdx: j,
					rawSrv: rawSrv,
					rawLoc: rawLoc,
				})
			}
		}
	}
	if len(refs) < 2 {
		return nil
	}
	var diags []Diagnostic
	reported := make(map[[2]int]bool)
	for a := 0; a < len(refs); a++ {
		for b := a + 1; b < len(refs); b++ {
			ra, rb := refs[a], refs[b]
			// Flag cache-enabled entries on different listeners (or different server
			// blocks) whose host sets and match paths overlap.
			if ra.srvIdx == rb.srvIdx {
				continue
			}
			if !ServerHostSetsOverlapLiteralOnly(ra.rawSrv.ServerNames, rb.rawSrv.ServerNames) {
				continue
			}
			if !MatchPathsOverlapLiteralOnly(ra.rawLoc.Match, rb.rawLoc.Match) {
				continue
			}
			for _, ref := range []cacheEnabledLocRef{ra, rb} {
				key := [2]int{ref.srvIdx, ref.locIdx}
				if reported[key] {
					continue
				}
				reported[key] = true
				other := rb
				if ref.srvIdx == rb.srvIdx && ref.locIdx == rb.locIdx {
					other = ra
				}
				diags = append(diags, Diagnostic{
					Code:     FindingCacheSharedAcrossEntries,
					Severity: SeverityWarning,
					Field:    fmt.Sprintf("servers[%d].locations[%d].cache", ref.srvIdx, ref.locIdx),
					Message:  fmt.Sprintf("cache-enabled location shares the process cache namespace with servers[%d].locations[%d] (listen %q) for overlapping host and path", other.srvIdx, other.locIdx, other.rawSrv.Listen),
					Hint:     "the HTTP cache keys by method, scheme, Host, URI, and Vary (not listener); disable cache on one entry or separate their server_names",
				})
			}
		}
	}
	return diags
}

type cidrInterval struct {
	start *big.Int
	end   *big.Int
}

// ClassifyCIDRChange compares two CIDR allow/deny sets (allow minus deny across
// IPv4 and IPv6) and returns ("unchanged"|"narrowed"|"widened"|"uncertain", reason).
// When rejectSecretRefs is true (plan preview), any secret reference in a
// changed allow/deny list returns "uncertain" without inspecting resolved values.
func ClassifyCIDRChange(bAllow, bDeny, aAllow, aDeny []string, rejectSecretRefs bool) (string, string) {
	if slices.Equal(bAllow, aAllow) && slices.Equal(bDeny, aDeny) {
		return "unchanged", ""
	}
	if rejectSecretRefs {
		for _, list := range [][]string{bAllow, bDeny, aAllow, aDeny} {
			for _, entry := range list {
				if ContainsSecretRef(entry) {
					return "uncertain", "CIDR allow/deny comparison involves a secret reference"
				}
			}
		}
	}
	bV4, bV6, ok1 := computePermittedCIDRSet(bAllow, bDeny)
	aV4, aV6, ok2 := computePermittedCIDRSet(aAllow, aDeny)
	if !ok1 || !ok2 {
		return "uncertain", "CIDR allow/deny entries could not be compared"
	}
	v4Eq := cidrIntervalsEqual(bV4, aV4)
	v6Eq := cidrIntervalsEqual(bV6, aV6)
	if v4Eq && v6Eq {
		return "unchanged", ""
	}
	aSubB := cidrIntervalsSubset(aV4, bV4) && cidrIntervalsSubset(aV6, bV6)
	bSubA := cidrIntervalsSubset(bV4, aV4) && cidrIntervalsSubset(bV6, aV6)
	switch {
	case aSubB && !bSubA:
		return "narrowed", "CIDR permitted address set strictly shrank"
	case bSubA && !aSubB:
		return "widened", "CIDR permitted address set strictly grew"
	default:
		return "uncertain", "CIDR permitted address set changed in both directions"
	}
}

// CIDRSetPermitsNone reports whether allow minus deny leaves both the IPv4 and
// IPv6 permitted sets empty.
func CIDRSetPermitsNone(allow, deny []string) bool {
	if len(deny) == 0 {
		return false
	}
	v4, v6, ok := computePermittedCIDRSet(allow, deny)
	return ok && len(v4) == 0 && len(v6) == 0
}

func computePermittedCIDRSet(allow, deny []string) (v4, v6 []cidrInterval, ok bool) {
	maxV4 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 32), big.NewInt(1))
	maxV6 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))

	if len(allow) == 0 {
		v4 = []cidrInterval{{big.NewInt(0), maxV4}}
		v6 = []cidrInterval{{big.NewInt(0), maxV6}}
	} else {
		for _, raw := range allow {
			iv, isV6, valid := parseCIDRInterval(raw)
			if !valid {
				return nil, nil, false
			}
			if isV6 {
				v6 = append(v6, iv)
			} else {
				v4 = append(v4, iv)
			}
		}
		v4 = normalizeCIDRIntervals(v4)
		v6 = normalizeCIDRIntervals(v6)
	}

	for _, raw := range deny {
		iv, isV6, valid := parseCIDRInterval(raw)
		if !valid {
			return nil, nil, false
		}
		if isV6 {
			v6 = subtractCIDRInterval(v6, iv)
		} else {
			v4 = subtractCIDRInterval(v4, iv)
		}
	}
	return v4, v6, true
}

func parseCIDRInterval(raw string) (cidrInterval, bool, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return cidrInterval{}, false, false
	}
	var pfx netip.Prefix
	if strings.Contains(s, "/") {
		var err error
		pfx, err = netip.ParsePrefix(s)
		if err != nil {
			return cidrInterval{}, false, false
		}
		if pfx.Addr().Is4In6() && pfx.Bits() >= 96 {
			pfx = netip.PrefixFrom(pfx.Addr().Unmap(), pfx.Bits()-96)
		}
		pfx = pfx.Masked()
	} else {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return cidrInterval{}, false, false
		}
		addr = addr.Unmap()
		pfx = netip.PrefixFrom(addr, addr.BitLen())
	}
	addr := pfx.Addr()
	bits := pfx.Bits()
	if addr.Is4() {
		if bits < 0 || bits > 32 {
			return cidrInterval{}, false, false
		}
		b4 := addr.As4()
		start := new(big.Int).SetBytes(b4[:])
		hostBits := uint(32 - bits)
		mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), hostBits), big.NewInt(1))
		end := new(big.Int).Or(start, mask)
		return cidrInterval{start: start, end: end}, false, true
	}
	if bits < 0 || bits > 128 {
		return cidrInterval{}, false, false
	}
	b16 := addr.As16()
	start := new(big.Int).SetBytes(b16[:])
	hostBits := uint(128 - bits)
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), hostBits), big.NewInt(1))
	end := new(big.Int).Or(start, mask)
	return cidrInterval{start: start, end: end}, true, true
}

func normalizeCIDRIntervals(in []cidrInterval) []cidrInterval {
	if len(in) == 0 {
		return nil
	}
	sorted := make([]cidrInterval, len(in))
	for i, iv := range in {
		sorted[i] = cidrInterval{
			start: new(big.Int).Set(iv.start),
			end:   new(big.Int).Set(iv.end),
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		if cmp := sorted[i].start.Cmp(sorted[j].start); cmp != 0 {
			return cmp < 0
		}
		return sorted[i].end.Cmp(sorted[j].end) < 0
	})
	out := []cidrInterval{sorted[0]}
	for _, cur := range sorted[1:] {
		last := &out[len(out)-1]
		lastPlusOne := new(big.Int).Add(last.end, big.NewInt(1))
		if cur.start.Cmp(lastPlusOne) <= 0 {
			if cur.end.Cmp(last.end) > 0 {
				last.end = new(big.Int).Set(cur.end)
			}
		} else {
			out = append(out, cur)
		}
	}
	return out
}

func subtractCIDRInterval(set []cidrInterval, sub cidrInterval) []cidrInterval {
	var out []cidrInterval
	for _, iv := range set {
		if sub.end.Cmp(iv.start) < 0 || sub.start.Cmp(iv.end) > 0 {
			out = append(out, iv)
			continue
		}
		if sub.start.Cmp(iv.start) > 0 {
			out = append(out, cidrInterval{
				start: new(big.Int).Set(iv.start),
				end:   new(big.Int).Sub(sub.start, big.NewInt(1)),
			})
		}
		if sub.end.Cmp(iv.end) < 0 {
			out = append(out, cidrInterval{
				start: new(big.Int).Add(sub.end, big.NewInt(1)),
				end:   new(big.Int).Set(iv.end),
			})
		}
	}
	return out
}

func cidrIntervalsEqual(a, b []cidrInterval) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].start.Cmp(b[i].start) != 0 || a[i].end.Cmp(b[i].end) != 0 {
			return false
		}
	}
	return true
}

func cidrIntervalsSubset(a, b []cidrInterval) bool {
	for _, iv := range a {
		rem := []cidrInterval{iv}
		for _, biv := range b {
			rem = subtractCIDRInterval(rem, biv)
			if len(rem) == 0 {
				break
			}
		}
		if len(rem) > 0 {
			return false
		}
	}
	return true
}
