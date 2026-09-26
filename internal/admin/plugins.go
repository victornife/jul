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
	// SHA256 sets the module pin; omitted keeps the existing pin so an editor
	// unaware of pins can never silently drop one, and "" clears it.
	SHA256 *string `json:"sha256,omitempty"`
	// The resource limits below follow the same rule as SHA256: omitted keeps
	// the existing value, an explicit value replaces it, and "" (or 0 for the
	// counts) clears it back to the runtime default (#462).
	MaxRequestBody   *string `json:"max_request_body,omitempty"`
	MaxResponseBody  *string `json:"max_response_body,omitempty"`
	FetchTimeout     *string `json:"fetch_timeout,omitempty"`
	MaxFetchResponse *string `json:"max_fetch_response,omitempty"`
	KVMaxEntries     *int    `json:"kv_max_entries,omitempty"`
	KVMaxBytes       *string `json:"kv_max_bytes,omitempty"`
	MaxInvocations   *int    `json:"max_invocations,omitempty"`
	// ABI follows the same rule: an editor that does not send it can never
	// change a plugin's ABI; "" restores the jul-abi/v1 default.
	ABI *string `json:"abi,omitempty"`
}

// keepSize resolves an omitted-means-keep size field.
func keepSize(in *string, existing config.Size, field string) (config.Size, error) {
	if in == nil {
		return existing, nil
	}
	var size config.Size
	if err := size.UnmarshalText([]byte(*in)); err != nil {
		return 0, fmt.Errorf("plugin_set: %s: %w", field, err)
	}
	return size, nil
}

// keepDuration resolves an omitted-means-keep duration field.
func keepDuration(in *string, existing config.Duration, field string) (config.Duration, error) {
	if in == nil {
		return existing, nil
	}
	var d config.Duration
	if err := d.UnmarshalText([]byte(*in)); err != nil {
		return 0, fmt.Errorf("plugin_set: %s: %w", field, err)
	}
	if d.Std() < 0 {
		return 0, fmt.Errorf("plugin_set: %s must not be negative", field)
	}
	return d, nil
}

// keepCount resolves an omitted-means-keep count field.
func keepCount(in *int, existing int, field string) (int, error) {
	if in == nil {
		return existing, nil
	}
	if *in < 0 {
		return 0, fmt.Errorf("plugin_set: %s must not be negative", field)
	}
	return *in, nil
}

// applyKeptLimits copies every omitted-means-keep resource limit into pc.
func applyKeptLimits(pc *config.PluginConfig, in pluginDef, existing config.PluginConfig) error {
	var err error
	if pc.MaxRequestBody, err = keepSize(in.MaxRequestBody, existing.MaxRequestBody, "max_request_body"); err != nil {
		return err
	}
	if pc.MaxResponseBody, err = keepSize(in.MaxResponseBody, existing.MaxResponseBody, "max_response_body"); err != nil {
		return err
	}
	if pc.FetchTimeout, err = keepDuration(in.FetchTimeout, existing.FetchTimeout, "fetch_timeout"); err != nil {
		return err
	}
	if pc.MaxFetchResponse, err = keepSize(in.MaxFetchResponse, existing.MaxFetchResponse, "max_fetch_response"); err != nil {
		return err
	}
	if pc.KVMaxEntries, err = keepCount(in.KVMaxEntries, existing.KVMaxEntries, "kv_max_entries"); err != nil {
		return err
	}
	if pc.KVMaxBytes, err = keepSize(in.KVMaxBytes, existing.KVMaxBytes, "kv_max_bytes"); err != nil {
		return err
	}
	pc.MaxInvocations, err = keepCount(in.MaxInvocations, existing.MaxInvocations, "max_invocations")
	return err
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
	pc.SHA256 = existing.SHA256
	if in.SHA256 != nil {
		pin, err := config.ParseSHA256Pin(*in.SHA256)
		if err != nil {
			return config.PluginConfig{}, "", fmt.Errorf("plugin_set: %w", err)
		}
		pc.SHA256 = pin
	}
	if err := applyKeptLimits(&pc, in, existing); err != nil {
		return config.PluginConfig{}, "", err
	}
	pc.ABI = existing.ABI
	if in.ABI != nil {
		switch abi := strings.TrimSpace(*in.ABI); abi {
		case "":
			pc.ABI = ""
		case config.PluginABIV1, config.PluginABIV2:
			pc.ABI = abi
		default:
			return config.PluginConfig{}, "", fmt.Errorf("plugin_set: abi must be %q or %q", config.PluginABIV1, config.PluginABIV2)
		}
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
	// Pinned reports whether the declaration sets a sha256 pin.
	Pinned bool `json:"pinned"`
	// Digest is the sha256 of the module bytes the serving generation
	// compiled for this plugin name; absent when it is not serving. It is
	// computed by the runtime, never by the Console.
	Digest      string `json:"digest,omitempty"`
	DigestShort string `json:"digest_short,omitempty"`
	// Limits holds the resource limits the declaration sets explicitly; an
	// absent key means the runtime default applies.
	Limits *PluginLimits `json:"limits,omitempty"`
	// ABI is the declaration's effective ABI (jul-abi/v1 when unset).
	ABI string `json:"abi"`
	// ResponsePhase reports that the serving module can subscribe to the
	// jul-abi/v2 response phase; it is computed by the runtime.
	ResponsePhase bool `json:"response_phase"`
	// ResponseBodyMax is the effective max_response_body bounding a v2
	// response-phase body; absent for v1 plugins.
	ResponseBodyMax string `json:"response_body_max,omitempty"`
}

// PluginLimits is the configured (non-default) resource limits of a plugin.
type PluginLimits struct {
	MaxRequestBody   string `json:"max_request_body,omitempty"`
	MaxResponseBody  string `json:"max_response_body,omitempty"`
	FetchTimeout     string `json:"fetch_timeout,omitempty"`
	MaxFetchResponse string `json:"max_fetch_response,omitempty"`
	KVMaxEntries     int    `json:"kv_max_entries,omitempty"`
	KVMaxBytes       string `json:"kv_max_bytes,omitempty"`
	MaxInvocations   int    `json:"max_invocations,omitempty"`
}

func projectLimits(p config.PluginConfig) *PluginLimits {
	l := PluginLimits{KVMaxEntries: p.KVMaxEntries, MaxInvocations: p.MaxInvocations}
	if p.MaxRequestBody.Bytes() > 0 {
		l.MaxRequestBody = sizeStr(p.MaxRequestBody)
	}
	if p.MaxResponseBody.Bytes() > 0 {
		l.MaxResponseBody = sizeStr(p.MaxResponseBody)
	}
	if p.FetchTimeout.Std() > 0 {
		l.FetchTimeout = durStr(p.FetchTimeout)
	}
	if p.MaxFetchResponse.Bytes() > 0 {
		l.MaxFetchResponse = sizeStr(p.MaxFetchResponse)
	}
	if p.KVMaxBytes.Bytes() > 0 {
		l.KVMaxBytes = sizeStr(p.KVMaxBytes)
	}
	if l == (PluginLimits{}) {
		return nil
	}
	return &l
}

// PluginModule is the serving content identity of one plugin module.
type PluginModule struct {
	Digest        string
	ResponsePhase bool
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
			Pinned: strings.TrimSpace(p.SHA256) != "",
			Limits: projectLimits(p),
			ABI:    config.EffectivePluginABI(p),
		}
		if pp.ABI == config.PluginABIV2 {
			pp.ResponseBodyMax = "8m"
			if p.MaxResponseBody.Bytes() > 0 {
				pp.ResponseBodyMax = sizeStr(p.MaxResponseBody)
			}
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

// attachPluginModules adds the serving module digests to a projection.
func attachPluginModules(out *PluginsProjection, modules map[string]PluginModule) {
	for i := range out.Plugins {
		if m, ok := modules[out.Plugins[i].Name]; ok && m.Digest != "" {
			out.Plugins[i].Digest = m.Digest
			out.Plugins[i].DigestShort = shortDigest(m.Digest)
			out.Plugins[i].ResponsePhase = m.ResponsePhase
		}
	}
}

// shortDigest is the first 12 hex digits of a "sha256:<hex>" digest.
func shortDigest(digest string) string {
	h := strings.TrimPrefix(digest, "sha256:")
	if len(h) > 12 {
		h = h[:12]
	}
	return h
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
		if s.deps.PluginModules != nil {
			attachPluginModules(&out, s.deps.PluginModules())
		}
		out.UploadEnabled = pluginUploadEnabled(snap.cfg) && snap.cfg.PluginUploadMaxSize > 0
		out.UploadMaxSizeMB = 0
		if out.UploadEnabled {
			out.UploadMaxSizeMB = snap.cfg.PluginUploadMaxSize
		}
		writeJSON(w, http.StatusOK, out)
	})(w, r)
}
