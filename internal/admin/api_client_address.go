// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// Listener-granularity trusted-proxy policy endpoint.
//
// The schema keeps the policy on [[servers]], but the *unit of change* is the
// listen address: identity is derived per listen address before the Host header
// selects a block, and configuration validation rejects blocks sharing a listen
// whose policies disagree. Exposing a per-block PATCH would therefore fail
// whenever sibling blocks exist. This route writes every block on the address
// in one atomic operation instead.
//
// The URL is deliberately listener-shaped. If a future [[listeners]] block is
// ever promoted, this endpoint does not change — only its backing storage does.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"jul/internal/clientaddr"
	"jul/internal/config"
)

// listenerClientAddressRequest is the wire body of
// PATCH /api/listeners/{addr}/client_address.
//
// Policy is a pointer so that clearing the policy (null) is distinguishable
// from an empty policy object. Inside it, forwarded_headers is likewise a
// pointer: omitted keeps the default preference, while an explicit [] disables
// every forwarding header.
type listenerClientAddressRequest struct {
	BaseVersion string              `json:"base_version,omitempty"`
	Policy      *clientAddressPatch `json:"client_address"`
}

// handleListenerClientAddress applies one trusted-proxy policy to every server
// block on the addressed listener, through the same validated apply path as
// every other structured change.
func (s *Server) handleListenerClientAddress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		methodNotAllowed(w, http.MethodPatch)
		return
	}
	addr, err := listenerAddrFromPath(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	var req listenerClientAddressRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot read request body"})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}

	mode := r.URL.Query().Get("mode")
	switch mode {
	case "", "hot":
		// The policy is hot-reloadable; the lifecycle projection in the
		// response proves it rather than this handler asserting it.
		mode = "hot"
	case "stage_restart":
		// valid
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "unknown apply mode " + mode + "; valid values: hot, stage_restart",
		})
		return
	}

	s.applyPatchOps(w, r, patchApplyParams{
		ops: []patchRequest{{
			Op:            "listener_set_client_address",
			Listen:        addr,
			ClientAddress: req.Policy,
		}},
		baseVersion: req.BaseVersion,
		mode:        mode,
		auditAction: auditActionClientAddress,
	})
}

// handleListenerClientAddressRead returns the effective policy of one listener
// plus the blocks it covers, so the Console can show what actually applies
// without reconstructing it from the per-server projection.
func (s *Server) handleListenerClientAddressRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	addr, err := listenerAddrFromPath(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		view, ok := projectListenerClientAddress(c, addr)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no server block listens on " + addr})
			return
		}
		writeJSON(w, http.StatusOK, view)
	})(w, r)
}

// handleListenersList returns the trusted-proxy projection for every listen
// address, so the Console can render the whole picture in one request rather
// than probing addresses it has to discover elsewhere.
func (s *Server) handleListenersList(w http.ResponseWriter, r *http.Request) {
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		seen := map[string]bool{}
		out := []ListenerClientAddress{}
		for i := range c.Servers {
			addr := strings.TrimSpace(c.Servers[i].Listen)
			if addr == "" || seen[addr] {
				continue
			}
			seen[addr] = true
			if view, ok := projectListenerClientAddress(c, addr); ok {
				out = append(out, view)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Listen < out[j].Listen })
		writeJSON(w, http.StatusOK, out)
	})(w, r)
}

// ListenerClientAddress is the secret-free projection of one listener's
// trusted-proxy policy. Every field is configuration the operator wrote; no
// request-derived address ever appears here.
type ListenerClientAddress struct {
	Listen           string   `json:"listen"`
	ServerBlocks     int      `json:"server_blocks"`
	Configured       bool     `json:"configured"`
	TrustedProxies   []string `json:"trusted_proxies"`
	ForwardedHeaders []string `json:"forwarded_headers"`
	MaxHops          int      `json:"max_hops"`
	// HeadersDisabled reports the explicitly-empty forwarded_headers state,
	// which is not the same as the default preference.
	HeadersDisabled bool `json:"headers_disabled"`
	// TrustsEveryClient flags a range covering the whole address space, which
	// lets any client assert any address. It mirrors the `jul lint` finding.
	TrustsEveryClient bool `json:"trusts_every_client"`
}

// projectListenerClientAddress builds the effective policy view for addr.
func projectListenerClientAddress(c *config.Config, addr string) (ListenerClientAddress, bool) {
	view := ListenerClientAddress{
		Listen:           addr,
		TrustedProxies:   []string{},
		ForwardedHeaders: clientaddrDefaultHeaders(),
		MaxHops:          clientaddrDefaultMaxHops(),
	}
	var found bool
	for i := range c.Servers {
		if strings.TrimSpace(c.Servers[i].Listen) != addr {
			continue
		}
		found = true
		view.ServerBlocks++
		policy := c.Servers[i].ClientAddress
		if policy == nil || view.Configured {
			continue
		}
		// Validation guarantees every block on the address declares the same
		// effective policy, so the first one is authoritative.
		view.Configured = true
		view.TrustedProxies = append([]string{}, policy.TrustedProxies...)
		if policy.ForwardedHeaders != nil {
			view.ForwardedHeaders = append([]string{}, policy.ForwardedHeaders...)
			view.HeadersDisabled = len(policy.ForwardedHeaders) == 0
		}
		if policy.MaxHops > 0 {
			view.MaxHops = policy.MaxHops
		}
		view.TrustsEveryClient = trustsEveryClient(policy.TrustedProxies)
	}
	return view, found
}

// listenerAddrFromPath extracts and bounds the {addr} path segment.
func listenerAddrFromPath(r *http.Request) (string, error) {
	addr := strings.TrimSpace(r.PathValue("addr"))
	if addr == "" {
		return "", errors.New("listener address is required")
	}
	if len(addr) > 255 || strings.ContainsAny(addr, "/\\ \t\r\n") {
		return "", errors.New("invalid listener address")
	}
	return addr, nil
}

// clientaddrDefaultHeaders and clientaddrDefaultMaxHops mirror the derivation
// package's defaults so a projection shows what actually applies when the
// operator omitted the field.
func clientaddrDefaultHeaders() []string { return clientaddr.DefaultForwardedHeaders() }

func clientaddrDefaultMaxHops() int { return clientaddr.DefaultMaxHops }

// trustsEveryClient reports whether any entry covers the whole address space.
// It reuses the compiled-policy parser, so the projection and `jul lint` cannot
// disagree about what an entry means.
func trustsEveryClient(entries []string) bool {
	for _, raw := range entries {
		prefix, err := clientaddr.ParsePrefix(raw)
		if err == nil && prefix.Bits() == 0 {
			return true
		}
	}
	return false
}

// clientAddressKey renders a policy as a comparable string. It normalises the
// prefix set (parsed, sorted, deduplicated) and applies defaults, so two
// configurations that differ only in spelling compare equal — the same
// semantics configuration validation uses when it requires blocks sharing a
// listen to agree.
func clientAddressKey(p *config.ClientAddressConfig) string {
	if p == nil {
		return ""
	}
	trusted := make([]string, 0, len(p.TrustedProxies))
	seen := make(map[string]bool, len(p.TrustedProxies))
	for _, raw := range p.TrustedProxies {
		entry := strings.TrimSpace(raw)
		if prefix, err := clientaddr.ParsePrefix(raw); err == nil {
			entry = prefix.String()
		}
		if entry == "" || seen[entry] {
			continue
		}
		seen[entry] = true
		trusted = append(trusted, entry)
	}
	sort.Strings(trusted)
	headers := clientaddr.DefaultForwardedHeaders()
	if p.ForwardedHeaders != nil {
		headers = p.ForwardedHeaders
	}
	maxHops := clientaddr.DefaultMaxHops
	if p.MaxHops > 0 {
		maxHops = p.MaxHops
	}
	return strings.Join(trusted, ",") + "|" + strings.Join(headers, ",") + "|" + strconv.Itoa(maxHops)
}
