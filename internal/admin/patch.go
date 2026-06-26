package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"jul/internal/config"
)

// patchRequest is a structured, comment-free edit to the running configuration
// (Wave B). Rather than rewriting raw TOML (which risks mangling comments and
// formatting), each operation mutates the PARSED config model; the result is
// re-serialized and routed through the same validated SaveConfig path as the
// settings form, and the operator reviews the full generated diff before it is
// applied. This trades raw-comment preservation for safe, structured edits of
// the most common fields, exactly the P1-4 recommendation.
//
// Targets address an existing object:
//   - a route location by its server Listen + ServerNames set and the location's
//     Match type + Path (all four, so a patch is never silently applied to the
//     wrong vhost when listens repeat or the wrong location when paths repeat)
//   - an upstream pool by Name
//
// Exactly one operation is performed per request.
type patchRequest struct {
	Op string `json:"op"`

	// Route-location target (route_* ops). A location is addressed by its
	// server's Listen + ServerNames set and the location's Match type + Path.
	// ServerNames disambiguates name-based virtual hosts that share a listen;
	// MatchType disambiguates locations that share a path under different match
	// types (prefix/exact/regex). The console sends all of them from the route
	// projection so the target resolves to exactly one location.
	Listen      string   `json:"listen,omitempty"`
	ServerNames []string `json:"server_names,omitempty"`
	MatchType   string   `json:"match_type,omitempty"`
	Path        string   `json:"path,omitempty"`

	// Upstream target (upstream_* ops).
	Upstream string `json:"upstream,omitempty"`

	// Operation payloads (only the field relevant to Op is read).
	Target  string `json:"target,omitempty"`  // route_set_target: new proxy_pass
	Enabled *bool  `json:"enabled,omitempty"` // route_toggle_cache / route_toggle_rate_limit
	Address string `json:"address,omitempty"` // upstream_add_backend / upstream_remove_backend
	Weight  int    `json:"weight,omitempty"`  // upstream_add_backend (defaults to 1)

	// server_set_limits payload. Each field is an optional string-typed size or
	// duration (e.g. "10m", "30s"); only non-empty fields are applied, so the
	// edit is sparse. An empty string leaves the existing value untouched.
	Limits *serverLimits `json:"limits,omitempty"`
}

// serverLimits carries the per-server limit/timeout fields the editor can set.
type serverLimits struct {
	ClientMaxBodySize string `json:"client_max_body_size,omitempty"`
	ReadHeaderTimeout string `json:"read_header_timeout,omitempty"`
	ReadTimeout       string `json:"read_timeout,omitempty"`
	WriteTimeout      string `json:"write_timeout,omitempty"`
	IdleTimeout       string `json:"idle_timeout,omitempty"`
	MaxHeaderBytes    string `json:"max_header_bytes,omitempty"`
}

// applyPatch mutates c in place according to req, returning a human-readable
// description of the change for the audit log, or an error when the target is
// not found or the operation is unknown.
func applyPatch(c *config.Config, req patchRequest) (string, error) {
	switch req.Op {
	case "route_set_target":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(req.Target) == "" {
			return "", fmt.Errorf("route_set_target: target is required")
		}
		loc.ProxyPass = req.Target
		return fmt.Sprintf("route %s%s proxy_pass set to %s", req.Listen, req.Path, req.Target), nil

	case "route_toggle_cache":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.Enabled == nil {
			return "", fmt.Errorf("route_toggle_cache: enabled is required")
		}
		loc.Cache = *req.Enabled
		return fmt.Sprintf("route %s%s cache %s", req.Listen, req.Path, onOff(*req.Enabled)), nil

	case "route_toggle_rate_limit":
		loc, err := findLocation(c, req.Listen, req.ServerNames, req.MatchType, req.Path)
		if err != nil {
			return "", err
		}
		if req.Enabled == nil {
			return "", fmt.Errorf("route_toggle_rate_limit: enabled is required")
		}
		if *req.Enabled {
			if loc.RateLimit == nil {
				loc.RateLimit = &config.RateLimitConfig{}
			}
			loc.RateLimit.Enabled = true
		} else if loc.RateLimit != nil {
			loc.RateLimit.Enabled = false
		}
		return fmt.Sprintf("route %s%s rate limit %s", req.Listen, req.Path, onOff(*req.Enabled)), nil

	case "upstream_add_backend":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		addr := strings.TrimSpace(req.Address)
		if addr == "" {
			return "", fmt.Errorf("upstream_add_backend: address is required")
		}
		for _, s := range up.Servers {
			if s.Address == addr {
				return "", fmt.Errorf("upstream %q already has backend %q", req.Upstream, addr)
			}
		}
		weight := req.Weight
		if weight < 1 {
			weight = 1
		}
		up.Servers = append(up.Servers, config.UpstreamServer{Address: addr, Weight: weight})
		return fmt.Sprintf("upstream %s added backend %s (weight %d)", req.Upstream, addr, weight), nil

	case "upstream_remove_backend":
		up, err := findUpstream(c, req.Upstream)
		if err != nil {
			return "", err
		}
		addr := strings.TrimSpace(req.Address)
		idx := -1
		for i, s := range up.Servers {
			if s.Address == addr {
				idx = i
				break
			}
		}
		if idx < 0 {
			return "", fmt.Errorf("upstream %q has no backend %q", req.Upstream, addr)
		}
		if len(up.Servers) == 1 {
			return "", fmt.Errorf("cannot remove the last backend of upstream %q", req.Upstream)
		}
		up.Servers = append(up.Servers[:idx], up.Servers[idx+1:]...)
		return fmt.Sprintf("upstream %s removed backend %s", req.Upstream, addr), nil

	case "server_set_limits":
		return applyServerLimits(c, req)

	default:
		return "", fmt.Errorf("unknown patch op %q", req.Op)
	}
}

// applyServerLimits sets the per-server limit/timeout fields on the server block
// addressed by Listen. Only non-empty fields in req.Limits are applied, so the
// edit is sparse; each value is parsed through its config type so a malformed
// size/duration is rejected (without mutating) rather than silently dropped.
func applyServerLimits(c *config.Config, req patchRequest) (string, error) {
	if req.Limits == nil {
		return "", fmt.Errorf("server_set_limits: limits payload is required")
	}
	srv, err := findServer(c, req.Listen)
	if err != nil {
		return "", err
	}
	applied := make([]string, 0, 6)
	setSize := func(name, val string, dst *config.Size) error {
		if strings.TrimSpace(val) == "" {
			return nil
		}
		var s config.Size
		if err := s.UnmarshalText([]byte(val)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = s
		applied = append(applied, fmt.Sprintf("%s=%s", name, val))
		return nil
	}
	setDur := func(name, val string, dst *config.Duration) error {
		if strings.TrimSpace(val) == "" {
			return nil
		}
		var d config.Duration
		if err := d.UnmarshalText([]byte(val)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = d
		applied = append(applied, fmt.Sprintf("%s=%s", name, val))
		return nil
	}
	if err := setSize("client_max_body_size", req.Limits.ClientMaxBodySize, &srv.ClientMaxBodySize); err != nil {
		return "", err
	}
	if err := setDur("read_header_timeout", req.Limits.ReadHeaderTimeout, &srv.ReadHeaderTimeout); err != nil {
		return "", err
	}
	if err := setDur("read_timeout", req.Limits.ReadTimeout, &srv.ReadTimeout); err != nil {
		return "", err
	}
	if err := setDur("write_timeout", req.Limits.WriteTimeout, &srv.WriteTimeout); err != nil {
		return "", err
	}
	if err := setDur("idle_timeout", req.Limits.IdleTimeout, &srv.IdleTimeout); err != nil {
		return "", err
	}
	if err := setSize("max_header_bytes", req.Limits.MaxHeaderBytes, &srv.MaxHeaderBytes); err != nil {
		return "", err
	}
	if len(applied) == 0 {
		return "", fmt.Errorf("server_set_limits: no limit fields provided")
	}
	return fmt.Sprintf("server %s limits updated (%s)", req.Listen, strings.Join(applied, ", ")), nil
}

// findServer returns a pointer to the first server block bound to listen.
func findServer(c *config.Config, listen string) (*config.ServerConfig, error) {
	if strings.TrimSpace(listen) == "" {
		return nil, fmt.Errorf("server target requires a listen address")
	}
	for i := range c.Servers {
		if c.Servers[i].Listen == listen {
			return &c.Servers[i], nil
		}
	}
	return nil, fmt.Errorf("no server found for listen %q", listen)
}

func onOff(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

// findLocation returns a pointer to the single location uniquely identified by
// its server's listen address and ServerNames set plus the location's match
// type and path, so a mutation updates exactly the intended route in place.
// Matching on listen + path alone (the earlier behavior) could silently target
// the wrong virtual host when several server blocks share a listen, or the
// wrong location when a path repeats under different match types. The console
// always sends the full coordinates from the route projection; a target that
// resolves to more than one location is rejected rather than guessed.
func findLocation(c *config.Config, listen string, serverNames []string, matchType, path string) (*config.LocationConfig, error) {
	if strings.TrimSpace(listen) == "" || strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("route target requires both listen and path")
	}
	var found *config.LocationConfig
	matches := 0
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.Listen != listen || !stringSetsEqual(srv.ServerNames, serverNames) {
			continue
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			if loc.Match.Path == path && loc.Match.Type == matchType {
				found = loc
				matches++
			}
		}
	}
	switch {
	case matches == 0:
		return nil, fmt.Errorf("no route found for listen %q names %v match %q path %q", listen, serverNames, matchType, path)
	case matches > 1:
		return nil, fmt.Errorf("route target is ambiguous: %d locations match listen %q names %v match %q path %q", matches, listen, serverNames, matchType, path)
	default:
		return found, nil
	}
}

// stringSetsEqual reports whether a and b contain the same elements regardless
// of order (multiset equality, so repeated server names are compared exactly).
func stringSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

// findUpstream returns a pointer to the upstream pool with the given name.
func findUpstream(c *config.Config, name string) (*config.UpstreamConfig, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("upstream name is required")
	}
	for i := range c.Upstreams {
		if c.Upstreams[i].Name == name {
			return &c.Upstreams[i], nil
		}
	}
	return nil, fmt.Errorf("no upstream named %q", name)
}

// handleConfigPatch applies a single structured edit to the running config and
// returns the generated full diff for review BEFORE the change is applied — it
// does not persist. The UI shows the diff and the operator confirms via the
// existing /api/config/apply path with the returned candidate TOML.
// POST /api/config/patch
func (s *Server) handleConfigPatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.LoadConfig == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req patchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Snapshot the pre-patch config so the diff reflects exactly this edit.
	before, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	summary, err := applyPatch(cfg, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "The edit could not be applied.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	candidate, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Re-parse the before/after so the diff is computed over parsed models,
	// mirroring handleConfigDiff.
	beforeCfg, err := config.Parse(before)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	resp := map[string]any{
		"ok":        true,
		"summary":   summary,
		"candidate": string(candidate),
		"diff":      diffConfigs(beforeCfg, cfg),
	}
	// Cheap preview validation: parse the candidate and run the same structural,
	// secret-expansion, and WAF/auth dry-run checks as /api/config/validate, so
	// the operator sees problems BEFORE confirming the apply. This is advisory —
	// the authoritative, heavier full-factory preflight still runs at apply time
	// (WriteConfigRaw -> applyPreflight). A failing check does not block the
	// preview: the diff is still returned so the operator can see what the edit
	// would do, with the errors surfaced alongside it.
	if verr := validateRaw(candidate); verr != nil {
		resp["validation_errors"] = humanizeErr(verr.Error())
	}
	writeJSON(w, http.StatusOK, resp)
}
