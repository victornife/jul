// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"net/http"
	"strings"

	"jul/internal/config"
)

// streamDef is the stream_add / stream_set payload: the guided editor's view of
// one [[stream]] L4 (TCP/UDP) listener. It mirrors config.StreamServer but
// carries durations as strings parsed on apply, so a malformed value is rejected
// before mutating. Everything else (target references resolve to a known
// upstream or host:port, no duplicate listener, the udp/tcp feature
// constraints) is re-checked by the validated SaveConfig re-parse, so a
// structured edit never bypasses validation.
type streamDef struct {
	Listen         string            `json:"listen"`
	Protocol       string            `json:"protocol,omitempty"` // "tcp" (default) | "udp"
	ProxyPass      string            `json:"proxy_pass,omitempty"`
	SNIRoutes      map[string]string `json:"sni_routes,omitempty"`
	TLSPassthrough bool              `json:"tls_passthrough,omitempty"`
	ProxyProtocol  string            `json:"proxy_protocol,omitempty"`  // "" | "in" | "out" | "both"
	ConnectTimeout string            `json:"connect_timeout,omitempty"` // duration string, e.g. "10s"
	IdleTimeout    string            `json:"idle_timeout,omitempty"`    // duration string, e.g. "5m"
}

// buildStream constructs a config.StreamServer from the guided editor payload.
// The near-side checks (listen present, protocol/proxy_protocol keywords, a
// default target or at least one SNI route, the TCP-only constraints) mirror
// the validator so the operator gets a clear message before the diff is
// generated; the validated re-parse still enforces target references and
// duplicate listeners. It returns a short label for the audit summary.
func buildStream(in streamDef) (config.StreamServer, string, error) {
	listen := strings.TrimSpace(in.Listen)
	if listen == "" {
		return config.StreamServer{}, "", fmt.Errorf("a listen address is required")
	}
	proto := streamProtoOrDefault(in.Protocol)
	switch proto {
	case "tcp", "udp":
	default:
		return config.StreamServer{}, "", fmt.Errorf("protocol must be %q or %q", "tcp", "udp")
	}
	proxyPass := strings.TrimSpace(in.ProxyPass)
	sni := trimSNIRoutes(in.SNIRoutes)
	if proxyPass == "" && len(sni) == 0 {
		return config.StreamServer{}, "", fmt.Errorf("a default proxy_pass or at least one SNI route is required")
	}
	pp := strings.ToLower(strings.TrimSpace(in.ProxyProtocol))
	switch pp {
	case "", "in", "out", "both":
	default:
		return config.StreamServer{}, "", fmt.Errorf("proxy_protocol must be %q, %q, or %q", "in", "out", "both")
	}
	// SNI routing, TLS passthrough, and the PROXY protocol are TCP-only in v1
	// (the validator enforces the same; surface it before the diff).
	if proto == "udp" {
		if len(sni) > 0 {
			return config.StreamServer{}, "", fmt.Errorf("sni_routes is only supported for tcp streams")
		}
		if in.TLSPassthrough {
			return config.StreamServer{}, "", fmt.Errorf("tls_passthrough is only supported for tcp streams")
		}
		if pp != "" {
			return config.StreamServer{}, "", fmt.Errorf("proxy_protocol is only supported for tcp streams")
		}
	}
	st := config.StreamServer{
		Listen:         listen,
		Protocol:       proto,
		ProxyPass:      proxyPass,
		SNIRoutes:      sni,
		TLSPassthrough: in.TLSPassthrough,
		ProxyProtocol:  pp,
	}
	if raw := strings.TrimSpace(in.ConnectTimeout); raw != "" {
		var d config.Duration
		if err := d.UnmarshalText([]byte(raw)); err != nil {
			return config.StreamServer{}, "", fmt.Errorf("connect_timeout: %w", err)
		}
		st.ConnectTimeout = d
	}
	if raw := strings.TrimSpace(in.IdleTimeout); raw != "" {
		var d config.Duration
		if err := d.UnmarshalText([]byte(raw)); err != nil {
			return config.StreamServer{}, "", fmt.Errorf("idle_timeout: %w", err)
		}
		st.IdleTimeout = d
	}
	return st, streamSummary(st), nil
}

// streamProtoOrDefault returns the stream's protocol, mapping the empty default
// to "tcp" (the same default the validator and runtime apply), lower-cased.
func streamProtoOrDefault(p string) string {
	if v := strings.ToLower(strings.TrimSpace(p)); v != "" {
		return v
	}
	return "tcp"
}

// trimSNIRoutes returns a copy of m with blank keys dropped and values trimmed,
// or nil when nothing remains, so a serialized stream omits an empty
// [stream.sni_routes] table.
func trimSNIRoutes(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for host, target := range m {
		host = strings.TrimSpace(host)
		target = strings.TrimSpace(target)
		if host != "" && target != "" {
			out[host] = target
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// streamSummary renders a stream listener for an audit summary or diff entry:
// its protocol and listen plus the default target and/or SNI-route count.
func streamSummary(st config.StreamServer) string {
	out := fmt.Sprintf("%s %s", streamProtoOrDefault(st.Protocol), st.Listen)
	if t := strings.TrimSpace(st.ProxyPass); t != "" {
		out += " → " + t
	}
	if n := len(st.SNIRoutes); n > 0 {
		out += fmt.Sprintf(" + %d SNI route%s", n, plural(n))
	}
	return out
}

// plural returns "s" for any count other than one, for human-readable summaries.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// findStreamIndex resolves the [[stream]] block addressed by listen + protocol
// to its index in c.Streams. The protocol defaults to tcp. It returns an error
// when no stream matches, or (defensively) when more than one does — the
// validated config rejects duplicate proto/listen, but a raw file could carry
// them, and a structured edit must never silently target the wrong one.
func findStreamIndex(c *config.Config, listen, protocol string) (int, error) {
	target := strings.TrimSpace(listen)
	if target == "" {
		return -1, fmt.Errorf("a stream listen address is required")
	}
	proto := streamProtoOrDefault(protocol)
	matches := make([]int, 0, 1)
	for i := range c.Streams {
		if strings.TrimSpace(c.Streams[i].Listen) == target && streamProtoOrDefault(c.Streams[i].Protocol) == proto {
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 0:
		return -1, fmt.Errorf("no %s stream listening on %s", proto, target)
	case 1:
		return matches[0], nil
	default:
		return -1, fmt.Errorf("multiple %s streams listening on %s; cannot target unambiguously", proto, target)
	}
}

// streamTaken reports whether a different stream block already claims the
// proto/listen identity, so stream_add and an identity-changing stream_set can
// refuse a duplicate before the validated re-parse does (with a clearer
// message). except is the index to ignore (the stream being edited), or -1.
func streamTaken(c *config.Config, listen, protocol string, except int) bool {
	target := strings.TrimSpace(listen)
	proto := streamProtoOrDefault(protocol)
	for i := range c.Streams {
		if i == except {
			continue
		}
		if strings.TrimSpace(c.Streams[i].Listen) == target && streamProtoOrDefault(c.Streams[i].Protocol) == proto {
			return true
		}
	}
	return false
}

// ── Streams projection (v2 API contract) ─────────────────────────────────────

// StreamsProjection is the Console v2 Streams panel payload: the declared L4
// listeners plus whether this binary can actually serve them.
type StreamsProjection struct {
	// Compiled reports whether this build includes the L4 stream proxy (the
	// "stream" build tag). When false, declaring a stream still validates but a
	// lean binary refuses to start with it, so the panel can warn up front.
	Compiled bool               `json:"compiled"`
	Streams  []StreamProjection `json:"streams"`
}

// StreamProjection is one declared [[stream]] for the Streams panel and its
// guided editor. It carries the declaration verbatim; the protocol is
// normalized to its effective value (tcp default) so the editor seeds a faithful
// round-trip.
type StreamProjection struct {
	Listen         string            `json:"listen"`
	Protocol       string            `json:"protocol"`
	ProxyPass      string            `json:"proxy_pass,omitempty"`
	SNIRoutes      map[string]string `json:"sni_routes,omitempty"`
	TLSPassthrough bool              `json:"tls_passthrough"`
	ProxyProtocol  string            `json:"proxy_protocol,omitempty"`
	ConnectTimeout string            `json:"connect_timeout,omitempty"`
	IdleTimeout    string            `json:"idle_timeout,omitempty"`
}

// projectStreams builds the Streams panel projection from the parsed config.
// compiled reports whether the running binary includes the L4 stream proxy.
func projectStreams(c *config.Config, compiled bool) StreamsProjection {
	out := StreamsProjection{Compiled: compiled, Streams: make([]StreamProjection, 0, len(c.Streams))}
	for i := range c.Streams {
		st := &c.Streams[i]
		sp := StreamProjection{
			Listen:         st.Listen,
			Protocol:       streamProtoOrDefault(st.Protocol),
			ProxyPass:      st.ProxyPass,
			SNIRoutes:      st.SNIRoutes,
			TLSPassthrough: st.TLSPassthrough,
			ProxyProtocol:  strings.ToLower(strings.TrimSpace(st.ProxyProtocol)),
		}
		if st.ConnectTimeout.Std() > 0 {
			sp.ConnectTimeout = durStr(st.ConnectTimeout)
		}
		if st.IdleTimeout.Std() > 0 {
			sp.IdleTimeout = durStr(st.IdleTimeout)
		}
		out.Streams = append(out.Streams, sp)
	}
	return out
}

// handleStreams serves the Streams panel projection. GET /api/streams
func (s *Server) handleStreams(w http.ResponseWriter, r *http.Request) {
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		writeJSON(w, http.StatusOK, projectStreams(c, s.deps.StreamCompiled))
	})(w, r)
}
