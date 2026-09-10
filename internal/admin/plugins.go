// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"net/http"
	"strings"

	"jul/internal/config"
)

// pluginDef is the plugin_set payload: the guided editor's view of a single
// [plugins.NAME] declaration. It mirrors config.PluginConfig but carries the
// module source as a discriminator (path vs. inline) rather than the raw bytes,
// because the console never ships base64 WASM blobs over the wire. When Source
// is "inline" the editor is only editing metadata of an existing inline plugin;
// buildPlugin preserves the stored bytes. Durations/sizes are strings parsed on
// apply, and the validated SaveConfig re-parse enforces the rest (the path
// exists, the type is valid, fetch needs allowed_hosts).
type pluginDef struct {
	Source       string            `json:"source,omitempty"`
	Path         string            `json:"path,omitempty"`
	Type         string            `json:"type,omitempty"`
	Config       map[string]string `json:"config,omitempty"`
	MemoryLimit  string            `json:"memory_limit,omitempty"`
	Timeout      string            `json:"timeout,omitempty"`
	KV           bool              `json:"kv,omitempty"`
	Fetch        bool              `json:"fetch,omitempty"`
	AllowedHosts []string          `json:"allowed_hosts,omitempty"`
}

func buildPlugin(in pluginDef, existing config.PluginConfig) (config.PluginConfig, string, error) {
	typ := strings.TrimSpace(in.Type)
	switch typ {
	case "", "middleware", "handler":
	default:
		return config.PluginConfig{}, "", fmt.Errorf("plugin_set: type must be %q or %q", "middleware", "handler")
	}
	pc := config.PluginConfig{Type: typ, Config: trimConfigMap(in.Config), KV: in.KV, Fetch: in.Fetch, AllowedHosts: normalizeStringSlice(in.AllowedHosts)}
	switch strings.TrimSpace(in.Source) {
	case "", "path":
		p := strings.TrimSpace(in.Path)
		if p == "" {
			return config.PluginConfig{}, "", fmt.Errorf("plugin_set: a module path is required")
		}
		pc.Path = p
	case "inline":
		if strings.TrimSpace(existing.Inline) == "" {
			return config.PluginConfig{}, "", fmt.Errorf("plugin_set: inline source can only be kept on an existing inline plugin; set a path instead")
		}
		pc.Inline = existing.Inline
	default:
		return config.PluginConfig{}, "", fmt.Errorf("plugin_set: source must be %q or %q", "path", "inline")
	}
	if pc.Fetch && len(pc.AllowedHosts) == 0 {
		return config.PluginConfig{}, "", fmt.Errorf("plugin_set: fetch is enabled but allowed_hosts is empty (an allowlist is required)")
	}
	if raw := strings.TrimSpace(in.MemoryLimit); raw != "" {
		var size config.Size
		if err := size.UnmarshalText([]byte(raw)); err != nil {
			return config.PluginConfig{}, "", fmt.Errorf("plugin_set: memory_limit: %w", err)
		}
		pc.MemoryLimit = size
	}
	if raw := strings.TrimSpace(in.Timeout); raw != "" {
		var d config.Duration
		if err := d.UnmarshalText([]byte(raw)); err != nil {
			return config.PluginConfig{}, "", fmt.Errorf("plugin_set: timeout: %w", err)
		}
		pc.Timeout = d
	}
	return pc, pluginSummary(pc), nil
}

func pluginTypeOrDefault(p config.PluginConfig) string {
	if typ := strings.TrimSpace(p.Type); typ != "" {
		return typ
	}
	return "middleware"
}

func pluginSourceKind(p config.PluginConfig) string {
	if strings.TrimSpace(p.Inline) != "" {
		return "inline"
	}
	return "path"
}

func pluginSummary(p config.PluginConfig) string {
	src := pluginSourceKind(p)
	if src == "path" && strings.TrimSpace(p.Path) != "" {
		src = "path " + p.Path
	}
	out := fmt.Sprintf("%s, %s", pluginTypeOrDefault(p), src)
	if caps := pluginCaps(p); caps != "" {
		out += ", " + caps
	}
	return out
}

func pluginCaps(p config.PluginConfig) string {
	var caps []string
	if p.KV {
		caps = append(caps, "kv")
	}
	if p.Fetch {
		caps = append(caps, "fetch")
	}
	return strings.Join(caps, "+")
}

func trimConfigMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func pluginReferences(c *config.Config, name string) []string {
	var refs []string
	for i := range c.Servers {
		srv := &c.Servers[i]
		for _, p := range srv.Plugins {
			if p == name {
				refs = append(refs, fmt.Sprintf("server %s", srv.Listen))
			}
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			for _, p := range loc.Plugins {
				if p == name {
					refs = append(refs, fmt.Sprintf("route %s%s", srv.Listen, loc.Match.Path))
				}
			}
			if loc.Plugin == name {
				refs = append(refs, fmt.Sprintf("route %s%s (handler)", srv.Listen, loc.Match.Path))
			}
		}
	}
	return refs
}

type PluginsProjection struct {
	Compiled        bool               `json:"compiled"`
	UploadEnabled   bool               `json:"upload_enabled"`
	UploadMaxSizeMB int                `json:"upload_max_size_mb"`
	Plugins         []PluginProjection `json:"plugins"`
}

type PluginProjection struct {
	Name         string             `json:"name"`
	Source       string             `json:"source"`
	Path         string             `json:"path,omitempty"`
	Type         string             `json:"type"`
	Config       map[string]string  `json:"config,omitempty"`
	MemoryLimit  string             `json:"memory_limit,omitempty"`
	Timeout      string             `json:"timeout,omitempty"`
	KV           bool               `json:"kv"`
	Fetch        bool               `json:"fetch"`
	AllowedHosts []string           `json:"allowed_hosts,omitempty"`
	Attachments  []PluginAttachment `json:"attachments,omitempty"`
}

type PluginAttachment struct {
	Scope       string   `json:"scope"`
	Role        string   `json:"role"`
	Listen      string   `json:"listen"`
	ServerNames []string `json:"server_names,omitempty"`
	MatchType   string   `json:"match_type,omitempty"`
	Path        string   `json:"path,omitempty"`
}

func projectPlugins(c *config.Config, compiled bool) PluginsProjection {
	uploadEnabled := c.Admin.PluginUploadEnabled != nil && *c.Admin.PluginUploadEnabled
	out := PluginsProjection{Compiled: compiled, UploadEnabled: uploadEnabled, Plugins: make([]PluginProjection, 0, len(c.Plugins))}
	if uploadEnabled {
		out.UploadMaxSizeMB = c.Admin.PluginUploadMaxSize
	}
	for _, name := range sortedKeys(c.Plugins) {
		p := c.Plugins[name]
		pp := PluginProjection{
			Name: name, Source: pluginSourceKind(p), Path: p.Path, Type: pluginTypeOrDefault(p), Config: p.Config,
			KV: p.KV, Fetch: p.Fetch, AllowedHosts: p.AllowedHosts, Attachments: pluginAttachments(c, name),
		}
		if p.MemoryLimit.Bytes() > 0 {
			pp.MemoryLimit = sizeStr(p.MemoryLimit)
		}
		if p.Timeout.Std() > 0 {
			pp.Timeout = durStr(p.Timeout)
		}
		out.Plugins = append(out.Plugins, pp)
	}
	return out
}

func pluginAttachments(c *config.Config, name string) []PluginAttachment {
	var out []PluginAttachment
	for i := range c.Servers {
		srv := &c.Servers[i]
		for _, p := range srv.Plugins {
			if p == name {
				out = append(out, PluginAttachment{Scope: "server", Role: "middleware", Listen: srv.Listen, ServerNames: srv.ServerNames})
			}
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			for _, p := range loc.Plugins {
				if p == name {
					out = append(out, PluginAttachment{Scope: "location", Role: "middleware", Listen: srv.Listen, ServerNames: srv.ServerNames, MatchType: loc.Match.Type, Path: loc.Match.Path})
				}
			}
			if loc.Plugin == name {
				out = append(out, PluginAttachment{Scope: "location", Role: "handler", Listen: srv.Listen, ServerNames: srv.ServerNames, MatchType: loc.Match.Type, Path: loc.Match.Path})
			}
		}
	}
	return out
}

// handlePlugins serves plugin declarations from the parsed config but reports
// the upload policy from the same immutable runtime snapshot pinned at request
// entry. This prevents an in-flight request from authenticating under generation
// A and projecting generation B's upload state after a concurrent Publish.
func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	snap := s.requestAdminSnapshot(r)
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		out := projectPlugins(c, s.deps.PluginsCompiled)
		out.UploadEnabled = pluginUploadEnabled(snap.cfg) && snap.cfg.PluginUploadMaxSize > 0
		out.UploadMaxSizeMB = 0
		if out.UploadEnabled {
			out.UploadMaxSizeMB = snap.cfg.PluginUploadMaxSize
		}
		writeJSON(w, http.StatusOK, out)
	})(w, r)
}
