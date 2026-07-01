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
	Source       string            `json:"source,omitempty"` // "path" (default) | "inline"
	Path         string            `json:"path,omitempty"`
	Type         string            `json:"type,omitempty"` // "middleware" (default) | "handler"
	Config       map[string]string `json:"config,omitempty"`
	MemoryLimit  string            `json:"memory_limit,omitempty"` // size string, e.g. "16m"
	Timeout      string            `json:"timeout,omitempty"`      // duration string, e.g. "100ms"
	KV           bool              `json:"kv,omitempty"`
	Fetch        bool              `json:"fetch,omitempty"`
	AllowedHosts []string          `json:"allowed_hosts,omitempty"`
}

// buildPlugin constructs a config.PluginConfig from the guided editor payload.
// existing is the current declaration of the same name (zero value when adding),
// used only to preserve inline module bytes the console does not transmit. The
// parse of durations/sizes happens here so a malformed value is rejected before
// mutating; everything else (path exists, type sanity, fetch needs an
// allowlist) is enforced by the validated SaveConfig re-parse, so the structured
// edit never bypasses validation. It returns a short label for the audit
// summary.
func buildPlugin(in pluginDef, existing config.PluginConfig) (config.PluginConfig, string, error) {
	typ := strings.TrimSpace(in.Type)
	switch typ {
	case "", "middleware", "handler":
	default:
		return config.PluginConfig{}, "", fmt.Errorf("plugin_set: type must be %q or %q", "middleware", "handler")
	}
	pc := config.PluginConfig{
		Type:         typ,
		Config:       trimConfigMap(in.Config),
		KV:           in.KV,
		Fetch:        in.Fetch,
		AllowedHosts: trimNonEmpty(in.AllowedHosts),
	}
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
	// Mirror the validator's near-side check so the operator gets a clear message
	// before the diff is generated (the re-parse also enforces it).
	if pc.Fetch && len(pc.AllowedHosts) == 0 {
		return config.PluginConfig{}, "", fmt.Errorf("plugin_set: fetch is enabled but allowed_hosts is empty (an allowlist is required)")
	}
	if raw := strings.TrimSpace(in.MemoryLimit); raw != "" {
		var s config.Size
		if err := s.UnmarshalText([]byte(raw)); err != nil {
			return config.PluginConfig{}, "", fmt.Errorf("plugin_set: memory_limit: %w", err)
		}
		pc.MemoryLimit = s
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

// pluginTypeOrDefault returns the plugin's declared type, mapping the empty
// default to "middleware" (the same default the validator and runtime apply).
func pluginTypeOrDefault(p config.PluginConfig) string {
	if t := strings.TrimSpace(p.Type); t != "" {
		return t
	}
	return "middleware"
}

// pluginSourceKind reports whether the plugin's module comes from a file path or
// inline bytes, for the projection's source discriminator.
func pluginSourceKind(p config.PluginConfig) string {
	if strings.TrimSpace(p.Inline) != "" {
		return "inline"
	}
	return "path"
}

// pluginSummary renders a plugin declaration for an audit summary or diff entry:
// its type, module source, and any granted capabilities.
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

// pluginCaps renders the granted host-capability set ("kv", "fetch") for a
// summary, or "" when none are granted.
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

// trimConfigMap returns a copy of m with blank keys dropped and kept values
// intact, or nil when nothing remains, so a serialized plugin omits an empty
// [plugins.NAME.config] table.
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

// pluginReferences lists the human-readable places a plugin name is still
// attached — a server-level middleware chain, a location middleware chain, or a
// location's handler action — so plugin_remove can refuse to leave a dangling
// reference (which the validated re-parse would reject anyway, but with a less
// targeted message).
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

// ── Plugins projection (v2 API contract) ─────────────────────────────────────

// PluginsProjection is the Console v2 Plugins panel payload: the declared WASM
// plugin set plus whether this binary can actually run it.
type PluginsProjection struct {
	// Compiled reports whether this build includes the WASM plugin runtime (the
	// "wasmplugins" build tag). When false, declaring a plugin still validates
	// but the apply preflight rejects it, so the panel can warn up front.
	Compiled        bool               `json:"compiled"`
	UploadEnabled   bool               `json:"upload_enabled"`
	UploadMaxSizeMB int                `json:"upload_max_size_mb"` // 0 if upload disabled
	Plugins         []PluginProjection `json:"plugins"`
}

// PluginProjection is one declared [plugins.NAME] for the Plugins panel and its
// guided editor. It carries the non-secret declaration verbatim (the inline
// module bytes are never projected — only the source kind) plus the list of
// places the plugin is attached so the panel can show usage and guard removal.
type PluginProjection struct {
	Name         string             `json:"name"`
	Source       string             `json:"source"` // "path" | "inline"
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

// PluginAttachment is one place a plugin is referenced. The identity fields
// mirror the structured-patch location selector (listen + server_names + match
// type/path) so the guided attach/detach editor can target the exact location.
type PluginAttachment struct {
	Scope       string   `json:"scope"` // "location" | "server"
	Role        string   `json:"role"`  // "middleware" | "handler"
	Listen      string   `json:"listen"`
	ServerNames []string `json:"server_names,omitempty"`
	MatchType   string   `json:"match_type,omitempty"`
	Path        string   `json:"path,omitempty"`
}

// projectPlugins builds the Plugins panel projection from the parsed config.
// compiled reports whether the running binary includes the plugin runtime.
func projectPlugins(c *config.Config, compiled bool) PluginsProjection {
	uploadEnabled := c.Admin.PluginUploadEnabled == nil || *c.Admin.PluginUploadEnabled
	out := PluginsProjection{
		Compiled:        compiled,
		UploadEnabled:   uploadEnabled,
		UploadMaxSizeMB: 0,
		Plugins:         make([]PluginProjection, 0, len(c.Plugins)),
	}
	if uploadEnabled {
		out.UploadMaxSizeMB = c.Admin.PluginUploadMaxSize
	}
	for _, name := range sortedKeys(c.Plugins) {
		p := c.Plugins[name]
		pp := PluginProjection{
			Name:         name,
			Source:       pluginSourceKind(p),
			Path:         p.Path,
			Type:         pluginTypeOrDefault(p),
			Config:       p.Config,
			KV:           p.KV,
			Fetch:        p.Fetch,
			AllowedHosts: p.AllowedHosts,
			Attachments:  pluginAttachments(c, name),
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

// pluginAttachments scans the server/location tree for every reference to name,
// returning a stable list (servers in order, then each location's middleware
// chain, then a handler action) so the panel can show usage and the editor can
// target a detach.
func pluginAttachments(c *config.Config, name string) []PluginAttachment {
	var out []PluginAttachment
	for i := range c.Servers {
		srv := &c.Servers[i]
		for _, p := range srv.Plugins {
			if p == name {
				out = append(out, PluginAttachment{
					Scope:       "server",
					Role:        "middleware",
					Listen:      srv.Listen,
					ServerNames: srv.ServerNames,
				})
			}
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			for _, p := range loc.Plugins {
				if p == name {
					out = append(out, PluginAttachment{
						Scope:       "location",
						Role:        "middleware",
						Listen:      srv.Listen,
						ServerNames: srv.ServerNames,
						MatchType:   loc.Match.Type,
						Path:        loc.Match.Path,
					})
				}
			}
			if loc.Plugin == name {
				out = append(out, PluginAttachment{
					Scope:       "location",
					Role:        "handler",
					Listen:      srv.Listen,
					ServerNames: srv.ServerNames,
					MatchType:   loc.Match.Type,
					Path:        loc.Match.Path,
				})
			}
		}
	}
	return out
}

// handlePlugins serves the Plugins panel projection. GET /api/plugins
func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		writeJSON(w, http.StatusOK, projectPlugins(c, s.deps.PluginsCompiled))
	})(w, r)
}
