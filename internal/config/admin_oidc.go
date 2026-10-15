// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
)

// DefaultAdminOIDC materializes documented defaults without enabling OIDC.
func DefaultAdminOIDC(o *AdminOIDCConfig) {
	if !o.Enabled {
		return
	}
	if o.ProviderName == "" {
		o.ProviderName = "OpenID Connect"
	}
	if len(o.Scopes) == 0 {
		o.Scopes = []string{"openid"}
	}
	if o.SessionIdle == 0 {
		o.SessionIdle = Duration(15 * time.Minute)
	}
	if o.SessionAbsolute == 0 {
		o.SessionAbsolute = Duration(8 * time.Hour)
	}
	if o.MaxSessions == 0 {
		o.MaxSessions = 1024
	}
	if o.MaxTransactions == 0 {
		o.MaxTransactions = 128
	}
	if o.ClockSkew == 0 {
		o.ClockSkew = Duration(time.Minute)
	}
}

// OIDCURL validates protocol URLs. HTTP is restricted to literal loopback or
// exactly localhost; remote providers and callbacks always require HTTPS.
func OIDCURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("OIDC URL must be absolute without credentials, query or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, fmt.Errorf("OIDC URL requires HTTPS (HTTP is limited to loopback)")
	}
	return u, nil
}

func validateAdminOIDC(a AdminConfig) []error {
	o := a.OIDC
	if !o.Enabled {
		return nil
	}
	DefaultAdminOIDC(&o)
	var errs []error
	bad := func(what string) { errs = append(errs, fmt.Errorf("[admin.oidc] %s", what)) }
	if !a.RBAC.Enabled {
		bad("requires admin.rbac.enabled and a local admin recovery credential")
	}
	_, err := OIDCURL(o.Issuer)
	if err != nil {
		bad("issuer must be an exact HTTPS or loopback URL")
	}
	callback, err := OIDCURL(o.CallbackURL)
	if err != nil || (callback != nil && (callback.Path != "/auth/oidc/callback" || callback.RawPath != "")) {
		bad("callback_url must use the exact /auth/oidc/callback path")
	}
	if callback != nil && callback.Scheme == "http" && !IsLoopbackListen(a.Listen) {
		bad("HTTP callbacks require a loopback admin listener")
	}
	if len(o.ProviderName) < 1 || len(o.ProviderName) > 80 || strings.ContainsAny(o.ProviderName, "\r\n\x00") {
		bad("provider_name must contain 1..80 printable characters")
	}
	if strings.TrimSpace(o.ClientID) != o.ClientID || o.ClientID == "" || len(o.ClientID) > 256 {
		bad("client_id must contain 1..256 non-padding characters")
	}
	if len(o.ClientSecret) > 4096 {
		bad("client_secret exceeds 4096 bytes")
	}
	if o.SessionIdle.Std() < time.Minute || o.SessionAbsolute.Std() < o.SessionIdle.Std() || o.SessionAbsolute.Std() > 24*time.Hour {
		bad("session lifetimes require 1m <= idle <= absolute <= 24h")
	}
	if o.MaxSessions < 1 || o.MaxSessions > 65536 || o.MaxTransactions < 1 || o.MaxTransactions > 1024 {
		bad("session/transaction counts exceed supported bounds")
	}
	if o.ClockSkew.Std() < 0 || o.ClockSkew.Std() > 2*time.Minute {
		bad("clock_skew must be between 0s and 120s")
	}
	if !slices.Contains(o.Scopes, "openid") || len(o.Scopes) > 16 {
		bad("scopes must include openid and contain at most 16 entries")
	}
	seen := map[string]bool{}
	for _, scope := range o.Scopes {
		if scope == "" || len(scope) > 128 || strings.ContainsAny(scope, " \t\r\n\x00") || scope == "offline_access" || seen[scope] {
			bad("scopes must be unique bounded values without offline_access")
		}
		seen[scope] = true
	}
	if len(o.Subjects)+len(o.Groups) == 0 || len(o.Subjects) > 1024 || len(o.Groups) > 256 {
		bad("configure bounded subject and/or group mappings")
	}
	seen = map[string]bool{}
	names := map[string]bool{}
	for _, m := range o.Subjects {
		if m.Subject == "" || len(m.Subject) > 255 || seen[m.Subject] || !principalNameRe.MatchString(m.Principal) || m.Principal == "shared" || strings.HasPrefix(m.Principal, "oidc-") || names[m.Principal] || !isKnownRole(m.Role, a.RBAC.Roles) {
			bad("subject mappings require unique exact subjects, unique safe principal names and known roles")
		}
		seen[m.Subject], names[m.Principal] = true, true
	}
	if len(o.GroupClaim) > 256 || (len(o.Groups) > 0 && o.GroupClaim == "") {
		bad("groups require one bounded flat group_claim name")
	}
	seen = map[string]bool{}
	for _, m := range o.Groups {
		if m.Group == "" || len(m.Group) > 256 || seen[m.Group] || !isKnownRole(m.Role, a.RBAC.Roles) {
			bad("group mappings require unique bounded values and known roles")
		}
		seen[m.Group] = true
	}
	return errs
}
