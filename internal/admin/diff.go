package admin

import (
	"fmt"
	"strings"
	"time"

	"jul/internal/config"
)

// (helpers: serverIndex, sortedKeys, durStr, sizeStr, orNone and the
// diffLocations / diffUpstreams / diffGlobal* comparators live in
// diff_helpers.go.)

// ConfigDiff is a structured before/after report for the Console v2 diff view.
type ConfigDiff struct {
	Summary       string      `json:"summary"`
	Affected      []string    `json:"affected,omitempty"`
	Warnings      []string    `json:"warnings,omitempty"`
	Additions     []DiffEntry `json:"additions,omitempty"`
	Removals      []DiffEntry `json:"removals,omitempty"`
	Modifications []DiffEntry `json:"modifications,omitempty"`
}

// DiffEntry is one change entry in a structured diff.
type DiffEntry struct {
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func (d *ConfigDiff) add(e DiffEntry, a string) {
	d.Additions = append(d.Additions, e)
	d.Affected = append(d.Affected, a)
}
func (d *ConfigDiff) del(e DiffEntry, a string) {
	d.Removals = append(d.Removals, e)
	d.Affected = append(d.Affected, a)
}
func (d *ConfigDiff) mod(e DiffEntry, a string) {
	d.Modifications = append(d.Modifications, e)
	d.Affected = append(d.Affected, a)
}
func (d *ConfigDiff) warn(f string, a ...any) { d.Warnings = append(d.Warnings, fmt.Sprintf(f, a...)) }

// diffConfigs produces a human-auditable diff between the current running
// configuration and a draft candidate, explaining operational consequences
// across servers, locations/routes (action, target, auth, cache, compression,
// rate limit, timeouts), TLS/ACME/mTLS, upstream pools (targets, retries), and
// global cache/compression/rate-limit settings.
func diffConfigs(before, after *config.Config) ConfigDiff {
	var d ConfigDiff
	if before == nil || after == nil {
		d.Summary = "Unable to diff — one of the configurations is unavailable."
		return d
	}
	diffServers(before, after, &d)
	diffUpstreams(before, after, &d)
	diffGlobalCache(before, after, &d)
	diffGlobalCompression(before, after, &d)
	diffGlobalRateLimit(before, after, &d)
	diffGlobalWAF(before, after, &d)
	diffGlobalTracing(before, after, &d)
	diffSecretRefs(before, after, &d)
	if len(d.Affected) == 0 {
		d.Summary = "No structural changes detected between the current configuration and the draft."
	} else {
		d.Summary = fmt.Sprintf("This change affects %d items: %d additions, %d removals, %d modifications.",
			len(d.Affected), len(d.Additions), len(d.Removals), len(d.Modifications))
	}
	return d
}

func diffServers(before, after *config.Config, d *ConfigDiff) {
	bs, as := serverIndex(before.Servers), serverIndex(after.Servers)
	for _, name := range sortedKeys(as) {
		srv := as[name]
		b, ok := bs[name]
		if !ok {
			d.add(DiffEntry{Kind: "server", Name: name, After: fmt.Sprintf("listen=%s, %d locations", srv.Listen, len(srv.Locations)), Detail: "Add server block for " + name}, "server "+name)
			continue
		}
		if srv.Listen != b.Listen {
			d.mod(DiffEntry{Kind: "server", Name: name, Before: b.Listen, After: srv.Listen, Detail: "Change " + name + " listen address"}, "server "+name+" listen")
			d.warn("Changing the listen address on %s may break clients bound to the old address.", name)
		}
		diffServerTimeouts(name, b, srv, d)
		diffServerBodyLimit(name, b, srv, d)
		diffServerTLS(name, b.TLS, srv.TLS, d)
		diffLocations(name, b.Locations, srv.Locations, before.WAF, after.WAF, d)
	}
	for _, name := range sortedKeys(bs) {
		if _, ok := as[name]; !ok {
			srv := bs[name]
			d.del(DiffEntry{Kind: "server", Name: name, Before: fmt.Sprintf("listen=%s, %d locations", srv.Listen, len(srv.Locations)), Detail: "Remove server block " + name}, "server "+name)
			d.warn("Removing server block %s will stop serving traffic on %s.", name, srv.Listen)
		}
	}
}

func diffServerTimeouts(name string, b, a serverWrapper, d *ConfigDiff) {
	type pair struct {
		label string
		b, a  config.Duration
	}
	for _, p := range []pair{
		{"read timeout", b.ReadTimeout, a.ReadTimeout},
		{"read header timeout", b.ReadHeaderTimeout, a.ReadHeaderTimeout},
		{"write timeout", b.WriteTimeout, a.WriteTimeout},
		{"idle timeout", b.IdleTimeout, a.IdleTimeout},
	} {
		if p.b != p.a {
			d.mod(DiffEntry{Kind: "timeouts", Name: name, Before: durStr(p.b), After: durStr(p.a), Detail: fmt.Sprintf("Change %s %s", name, p.label)}, fmt.Sprintf("server %s %s", name, p.label))
			if time.Duration(p.a) == 0 && time.Duration(p.b) != 0 {
				d.warn("Clearing the %s on %s removes a safety bound and may let slow clients or backends hold connections open.", p.label, name)
			}
		}
	}
}

func diffServerBodyLimit(name string, b, a serverWrapper, d *ConfigDiff) {
	if b.ClientMaxBodySize != a.ClientMaxBodySize {
		d.mod(DiffEntry{Kind: "timeouts", Name: name, Before: sizeStr(b.ClientMaxBodySize), After: sizeStr(a.ClientMaxBodySize), Detail: "Change " + name + " request body size limit"}, "server "+name+" body limit")
		if a.ClientMaxBodySize.Bytes() == 0 && b.ClientMaxBodySize.Bytes() != 0 {
			d.warn("Removing the request body size limit on %s allows arbitrarily large uploads.", name)
		}
	}
}

func diffServerTLS(name string, b, a *config.TLSConfig, d *ConfigDiff) {
	bOn, aOn := b != nil && b.Enabled, a != nil && a.Enabled
	if bOn != aOn {
		action := "Enable"
		if !aOn {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "tls", Name: name, Detail: fmt.Sprintf("%s TLS for %s", action, name)}, "server "+name+" TLS")
		if action == "Disable" {
			d.warn("Disabling TLS on %s will expose traffic in plaintext.", name)
		}
		return
	}
	if !aOn {
		return
	}
	if b.Cert != a.Cert || b.Key != a.Key {
		d.mod(DiffEntry{Kind: "tls", Name: name, Detail: "Change TLS certificate/key for " + name}, "server "+name+" TLS cert")
	}
	if !strings.EqualFold(b.MinVersion, a.MinVersion) {
		d.mod(DiffEntry{Kind: "tls", Name: name, Before: orNone(b.MinVersion), After: orNone(a.MinVersion), Detail: "Change TLS minimum version for " + name}, "server "+name+" TLS min version")
		if a.MinVersion == "1.2" && b.MinVersion == "1.3" {
			d.warn("Lowering the TLS minimum version on %s to 1.2 weakens transport security.", name)
		}
	}
	diffACME(name, b.ACME, a.ACME, d)
	diffMTLS(name, b.ClientAuth, a.ClientAuth, d)
}

func diffACME(name string, b, a *config.ACMEConfig, d *ConfigDiff) {
	bOn, aOn := b != nil && b.Enabled, a != nil && a.Enabled
	if bOn != aOn {
		action := "Enable"
		if !aOn {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "acme", Name: name, Detail: fmt.Sprintf("%s automatic HTTPS (ACME) for %s", action, name)}, "server "+name+" ACME")
		if action == "Disable" {
			d.warn("Disabling ACME on %s stops automatic certificate renewal; certificates may expire.", name)
		}
		return
	}
	if !aOn {
		return
	}
	if !strings.EqualFold(b.CA, a.CA) {
		d.mod(DiffEntry{Kind: "acme", Name: name, Before: orNone(b.CA), After: orNone(a.CA), Detail: "Change ACME directory (CA) for " + name}, "server "+name+" ACME CA")
	}
	if !strings.EqualFold(b.Challenge, a.Challenge) {
		d.mod(DiffEntry{Kind: "acme", Name: name, Before: orNone(b.Challenge), After: orNone(a.Challenge), Detail: "Change ACME challenge type for " + name}, "server "+name+" ACME challenge")
	}
}

func diffMTLS(name string, b, a *config.ClientAuthConfig, d *ConfigDiff) {
	bOn, aOn := b.Active(), a.Active()
	if bOn != aOn {
		action := "Enable"
		if !aOn {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "mtls", Name: name, Detail: fmt.Sprintf("%s mutual TLS (client certificates) for %s", action, name)}, "server "+name+" mTLS")
		if action == "Disable" {
			d.warn("Disabling mutual TLS on %s removes client-certificate authentication.", name)
		}
		return
	}
	if !aOn {
		return
	}
	if !strings.EqualFold(b.Mode, a.Mode) {
		d.mod(DiffEntry{Kind: "mtls", Name: name, Before: orNone(b.Mode), After: orNone(a.Mode), Detail: "Change mutual TLS mode for " + name}, "server "+name+" mTLS mode")
		if strings.EqualFold(a.Mode, "request") && strings.EqualFold(b.Mode, "require") {
			d.warn("Relaxing mTLS on %s from require to request admits connections without a client certificate.", name)
		}
	}
	if b.CAFile != a.CAFile {
		d.mod(DiffEntry{Kind: "mtls", Name: name, Detail: "Change mutual TLS CA bundle for " + name}, "server "+name+" mTLS ca")
	}
	if b.CRLFile != a.CRLFile {
		d.mod(DiffEntry{Kind: "mtls", Name: name, Detail: "Change mutual TLS revocation list (CRL) for " + name}, "server "+name+" mTLS crl")
	}
}
