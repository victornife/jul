package admin

import (
	"fmt"

	"jul/internal/config"
)

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
	Kind   string `json:"kind"` // server | location | upstream | tls | cache | rate_limit | compression
	Name   string `json:"name,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// diffConfigs produces a human-auditable diff between the current running
// configuration and a draft candidate. It is intentionally high-level: the
// raw TOML diff is the source of truth for exact changes; this projection
// answers "what is affected and what are the risks?".
func diffConfigs(before, after *config.Config) ConfigDiff {
	var d ConfigDiff
	if before == nil || after == nil {
		d.Summary = "Unable to diff — one of the configurations is unavailable."
		return d
	}

	// Server-level changes.
	beforeServers := serverIndex(before.Servers)
	afterServers := serverIndex(after.Servers)

	for name, srv := range afterServers {
		if _, ok := beforeServers[name]; !ok {
			d.Additions = append(d.Additions, DiffEntry{
				Kind:   "server",
				Name:   name,
				After:  fmt.Sprintf("listen=%s, %d locations", srv.Listen, len(srv.Locations)),
				Detail: fmt.Sprintf("Add server block for %s", name),
			})
			d.Affected = append(d.Affected, fmt.Sprintf("server %s", name))
			continue
		}
		bSrv := beforeServers[name]
		if srv.Listen != bSrv.Listen {
			d.Modifications = append(d.Modifications, DiffEntry{
				Kind:   "server",
				Name:   name,
				Before: bSrv.Listen,
				After:  srv.Listen,
				Detail: fmt.Sprintf("Change %s listen address", name),
			})
			d.Affected = append(d.Affected, fmt.Sprintf("server %s listen", name))
		}
		if (srv.TLS != nil) != (bSrv.TLS != nil) {
			action := "Enable"
			if srv.TLS == nil {
				action = "Disable"
			}
			d.Modifications = append(d.Modifications, DiffEntry{
				Kind:   "server",
				Name:   name,
				Detail: fmt.Sprintf("%s TLS for %s", action, name),
			})
			d.Affected = append(d.Affected, fmt.Sprintf("server %s TLS", name))
			if action == "Disable" {
				d.Warnings = append(d.Warnings, fmt.Sprintf("Disabling TLS on %s will expose traffic in plaintext.", name))
			}
		}
	}

	for name, srv := range beforeServers {
		if _, ok := afterServers[name]; !ok {
			d.Removals = append(d.Removals, DiffEntry{
				Kind:   "server",
				Name:   name,
				Before: fmt.Sprintf("listen=%s, %d locations", srv.Listen, len(srv.Locations)),
				Detail: fmt.Sprintf("Remove server block %s", name),
			})
			d.Affected = append(d.Affected, fmt.Sprintf("server %s", name))
			d.Warnings = append(d.Warnings, fmt.Sprintf("Removing server block %s will stop serving traffic on %s.", name, srv.Listen))
		}
	}

	// Upstream-level changes.
	beforeUps := upstreamIndex(before.Upstreams)
	afterUps := upstreamIndex(after.Upstreams)
	for name := range afterUps {
		if _, ok := beforeUps[name]; !ok {
			d.Additions = append(d.Additions, DiffEntry{
				Kind:   "upstream",
				Name:   name,
				Detail: fmt.Sprintf("Add upstream pool %s", name),
			})
			d.Affected = append(d.Affected, fmt.Sprintf("upstream %s", name))
		}
	}
	for name := range beforeUps {
		if _, ok := afterUps[name]; !ok {
			d.Removals = append(d.Removals, DiffEntry{
				Kind:   "upstream",
				Name:   name,
				Detail: fmt.Sprintf("Remove upstream pool %s", name),
			})
			d.Affected = append(d.Affected, fmt.Sprintf("upstream %s", name))
			d.Warnings = append(d.Warnings, fmt.Sprintf("Removing upstream %s may break routes that proxy to it.", name))
		}
	}

	if len(d.Affected) == 0 {
		d.Summary = "No structural changes detected between the current configuration and the draft."
	} else {
		d.Summary = fmt.Sprintf("This change affects %d items: %d additions, %d removals, %d modifications.",
			len(d.Affected), len(d.Additions), len(d.Removals), len(d.Modifications))
	}
	return d
}

type serverWrapper struct {
	Name string
	*config.ServerConfig
}

func serverIndex(servers []config.ServerConfig) map[string]serverWrapper {
	m := make(map[string]serverWrapper, len(servers))
	for i := range servers {
		srv := &servers[i]
		key := srv.Listen
		if len(srv.ServerNames) > 0 {
			key = srv.ServerNames[0] + ":" + srv.Listen
		}
		m[key] = serverWrapper{Name: key, ServerConfig: srv}
	}
	return m
}

func upstreamIndex(upstreams []config.UpstreamConfig) map[string]*config.UpstreamConfig {
	m := make(map[string]*config.UpstreamConfig, len(upstreams))
	for i := range upstreams {
		up := &upstreams[i]
		m[up.Name] = up
	}
	return m
}
