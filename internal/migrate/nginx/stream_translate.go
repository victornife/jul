// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

// Translation of a bounded subset of nginx's stream module (#426) into Jul's
// [[stream]] L4 listeners. This is deliberately not stream-module parity: only
// source behavior Jul's existing TCP/UDP proxy, upstream pool, PROXY protocol
// and SNI-preread runtime can represent honestly is translated. Everything
// else - mail, Lua, third-party stream modules, arbitrary map/variable
// programs, stream TLS termination, and inbound stream PROXY protocol (nginx's
// stream module has no trusted-source directive to supply Jul's required
// trusted_proxies) - stays an explicit blocking finding.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"jul/internal/config"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
)

// streamServerSpec is one nginx stream `server {}` block, translated as far as
// this tranche can go. Fields are zero-valued when the corresponding
// directive was absent or unsupported (and reported via t.report.skip).
type streamServerSpec struct {
	listen            string
	protocol          string // "tcp" or "udp"
	serverName        string
	sslPreread        bool
	proxyPass         string
	hasConnectTimeout bool
	connectTimeout    config.Duration
	hasIdleTimeout    bool
	idleTimeout       config.Duration
	proxyProtocolOut  bool
	// sniRoutes is populated only by mergeStreamSNIGroup, never by a single
	// server block's own translation.
	sniRoutes map[string]string
	line      int
}

// translateStream converts a `stream {}` block into Jul stream listeners.
func (t *translator) translateStream(d ngx.IDirective, out *config.Config) {
	var specs []streamServerSpec
	for _, c := range children(d) {
		switch c.GetName() {
		case "upstream":
			t.translateUpstream(c, out)
		case "server":
			specs = append(specs, t.translateStreamServer(c))
		case "map":
			t.report.skip(c, "variable maps (including ssl_preread-driven routing) are not representable in the bounded Jul stream model")
		case "include":
			t.report.skip(c, "include not followed; import each included file separately")
		default:
			t.report.skip(c, "unsupported stream-level directive")
		}
	}
	t.emitStreamServers(specs, out)
}

// translateStreamServer converts one stream server block's directives into a
// best-effort spec. Every directive this tranche does not recognize is
// reported and simply omitted from the spec; emission/validation downstream
// decides whether the result is usable, exactly as the HTTP server
// translation already does for unsupported server-level directives.
func (t *translator) translateStreamServer(d ngx.IDirective) streamServerSpec {
	spec := streamServerSpec{protocol: "tcp", line: d.GetLine()}
	for _, c := range children(d) {
		cp := paramValues(c)
		switch c.GetName() {
		case "listen":
			listen, protocol, code, message := parseStreamListen(cp)
			if code != "" {
				t.report.skip(c, message)
				continue
			}
			spec.listen = listen
			spec.protocol = protocol
		case "server_name":
			if len(cp) > 0 && strings.TrimSpace(cp[0]) != "" {
				spec.serverName = strings.TrimSpace(cp[0])
			}
		case "ssl_preread":
			spec.sslPreread = isOn(cp)
		case "proxy_pass":
			if len(cp) == 0 || strings.TrimSpace(cp[0]) == "" {
				t.report.skip(c, "proxy_pass target is missing")
				continue
			}
			target := strings.TrimSpace(cp[0])
			if strings.Contains(target, "$") {
				t.report.skip(c, "variable-derived proxy targets are not translated")
				continue
			}
			spec.proxyPass = target
		case "proxy_timeout":
			du, ok := parseNginxDuration(firstParam(cp))
			if !ok {
				t.report.skip(c, "duration value is not representable (supported units: ms, s, m, h)")
				continue
			}
			spec.idleTimeout, spec.hasIdleTimeout = du, true
		case "proxy_connect_timeout":
			du, ok := parseNginxDuration(firstParam(cp))
			if !ok {
				t.report.skip(c, "duration value is not representable (supported units: ms, s, m, h)")
				continue
			}
			spec.connectTimeout, spec.hasConnectTimeout = du, true
		case "proxy_protocol":
			on, ok := onOff(firstParam(cp))
			if !ok {
				t.report.skip(c, "proxy_protocol requires an on or off value")
				continue
			}
			spec.proxyProtocolOut = on
		default:
			t.report.skip(c, "unsupported stream server directive")
		}
	}
	if spec.proxyProtocolOut && spec.protocol == "udp" {
		t.report.skipNamed("proxy_protocol", spec.line, "outbound PROXY protocol is only supported for tcp stream listeners")
		spec.proxyProtocolOut = false
	}
	return spec
}

// emitStreamServers groups specs by listen address. nginx's stream module
// dispatches server_name-distinguished siblings on one address by inspecting
// one ClientHello on one socket; Jul represents that as a single [[stream]]
// entry's sni_routes map, not multiple entries sharing an address (which
// config.Validate rejects as a duplicate listener). A group that cannot be
// merged safely (mixed protocols, missing ssl_preread, or an ambiguous
// fallback) is not translated at all, rather than guessing which member wins.
func (t *translator) emitStreamServers(specs []streamServerSpec, out *config.Config) {
	type group struct {
		key     string
		members []streamServerSpec
	}
	var order []string
	byAddr := map[string]*group{}
	for _, spec := range specs {
		if spec.listen == "" {
			continue
		}
		key := spec.protocol + "/" + spec.listen
		g, ok := byAddr[key]
		if !ok {
			g = &group{key: key}
			byAddr[key] = g
			order = append(order, key)
		}
		g.members = append(g.members, spec)
	}
	for _, key := range order {
		g := byAddr[key]
		if len(g.members) == 1 {
			t.appendStreamServer(g.members[0], out)
			continue
		}
		merged, reason := mergeStreamSNIGroup(g.members)
		if reason != "" {
			t.report.skipNamed("listen", g.members[0].line, fmt.Sprintf("stream listeners on %s declare incompatible or ambiguous routing (%s); none were translated", g.members[0].listen, reason))
			continue
		}
		t.appendStreamServer(*merged, out)
	}
}

// mergeStreamSNIGroup merges server_name-distinguished stream servers sharing
// one listen address into a single spec with sni_routes populated. reason is
// non-empty (and merged nil) when the group cannot be merged safely. Members
// are already guaranteed to share one protocol: emitStreamServers groups by
// protocol+address, because a tcp and a udp listener on the same port number
// are independent resources, not a conflict, for any group with more than one
// member here.
func mergeStreamSNIGroup(members []streamServerSpec) (merged *streamServerSpec, reason string) {
	base := members[0]
	base.serverName = ""
	sniRoutes := map[string]string{}
	fallbackCount := 0
	for _, m := range members {
		if !m.sslPreread {
			return nil, "duplicate listener without ssl_preread to distinguish them by SNI"
		}
		if m.serverName == "" {
			fallbackCount++
			if m.proxyPass != "" {
				base.proxyPass = m.proxyPass
				base.hasConnectTimeout = base.hasConnectTimeout || m.hasConnectTimeout
				base.hasIdleTimeout = base.hasIdleTimeout || m.hasIdleTimeout
				if m.hasConnectTimeout {
					base.connectTimeout = m.connectTimeout
				}
				if m.hasIdleTimeout {
					base.idleTimeout = m.idleTimeout
				}
			}
			continue
		}
		if m.proxyPass == "" {
			return nil, fmt.Sprintf("server_name %q has no proxy_pass target", m.serverName)
		}
		if _, dup := sniRoutes[m.serverName]; dup {
			return nil, fmt.Sprintf("duplicate server_name %q", m.serverName)
		}
		sniRoutes[m.serverName] = m.proxyPass
	}
	if fallbackCount > 1 {
		return nil, "more than one server block has no server_name to act as the fallback"
	}
	if len(sniRoutes) == 0 {
		return nil, "no server_name distinguishes these listeners"
	}
	base.sslPreread = true
	base.sniRoutes = sniRoutes
	return &base, ""
}

// appendStreamServer converts one merged spec into a config.StreamServer.
func (t *translator) appendStreamServer(spec streamServerSpec, out *config.Config) {
	st := config.StreamServer{Listen: spec.listen}
	if spec.protocol == "udp" {
		st.Protocol = "udp" // Jul defaults an empty protocol to tcp; keep generated TOML minimal otherwise.
	}
	if spec.proxyPass != "" {
		st.ProxyPass = spec.proxyPass
	}
	if len(spec.sniRoutes) > 0 {
		st.SNIRoutes = spec.sniRoutes
		st.TLSPassthrough = true
	}
	if spec.hasConnectTimeout {
		st.ConnectTimeout = spec.connectTimeout
	}
	if spec.hasIdleTimeout {
		st.IdleTimeout = spec.idleTimeout
	}
	if spec.proxyProtocolOut {
		st.ProxyProtocol = "out"
	}
	out.Streams = append(out.Streams, st)
	t.report.Streams++
	if spec.protocol == "udp" {
		t.report.note("stream listener %s at line %d is udp: Jul tracks one bounded, client-address-keyed session per source with a configurable idle timeout and a session-count cap; this is not QUIC Connection-ID-aware load balancing", spec.listen, spec.line)
	}
}

// streamListenOperationalTokenPrefixes are stream `listen` parameters that map
// to OS socket tuning nginx and Jul both apply implicitly; they carry no
// distinct Jul knob and are silently accepted rather than blocking.
func streamListenOperationalToken(p string) bool {
	if streamListenOperationalTokens[p] {
		return true
	}
	return strings.HasPrefix(p, "backlog=") || strings.HasPrefix(p, "ipv6only=") || strings.HasPrefix(p, "so_keepalive=")
}

// parseStreamListen extracts the address and protocol ("tcp" or "udp") from a
// stream `listen` directive's parameters. It is the single source of truth
// for which listen forms are representable: both the assessment classifier
// and the translator call it, so they can never disagree about which forms
// block. code is "" when the directive is fully representable; otherwise it
// is a stable assessment code and message explains why, ready to use directly
// as either a report.skip reason or a blocking capability message.
func parseStreamListen(params []string) (address, protocol, code, message string) {
	if len(params) == 0 || strings.TrimSpace(params[0]) == "" {
		return "", "", "NGX_STREAM_LISTEN_UNSUPPORTED", "listen address is missing or not representable"
	}
	addr := strings.TrimSpace(params[0])
	if strings.HasPrefix(addr, "unix:") {
		return "", "", "NGX_STREAM_LISTEN_UNSUPPORTED", "unix-socket stream listeners are not representable"
	}
	protocol = "tcp"
	for _, raw := range params[1:] {
		p := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case p == "udp":
			protocol = "udp"
		case p == "ssl":
			return "", "", "NGX_STREAM_TLS_TERMINATION", "stream TLS termination is not representable; Jul's stream listener only supports SNI-preread passthrough"
		case p == "proxy_protocol":
			return "", "", "NGX_STREAM_LISTEN_PROXY_PROTOCOL_IN", "Jul requires an explicit trusted_proxies allow-list for inbound stream PROXY protocol; nginx's stream module has no equivalent source, so this must be added to [[stream]] manually"
		case streamListenOperationalToken(p):
			continue
		default:
			return "", "", "NGX_STREAM_LISTEN_OPTION", "listen option is not translated"
		}
	}
	listen, _ := parseListen(params[:1])
	if listen == "" {
		return "", "", "NGX_STREAM_LISTEN_UNSUPPORTED", "listen address is missing or not representable"
	}
	return listen, protocol, "", ""
}

// firstParam returns the first element of params, or "" when empty.
func firstParam(params []string) string {
	if len(params) == 0 {
		return ""
	}
	return params[0]
}

// onOff parses a bare "on"/"off" token.
func onOff(value string) (on bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on":
		return true, true
	case "off":
		return false, true
	default:
		return false, false
	}
}

// parseNginxDuration converts an nginx duration string into a config.Duration.
// nginx accepts a bare number of seconds or a Go-compatible ms/s/m/h suffix;
// nginx's additional d/w/M/y units are not supported and report as blocking
// rather than being approximated.
func parseNginxDuration(value string) (config.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		value += "s"
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		return 0, false
	}
	return config.Duration(d), true
}
