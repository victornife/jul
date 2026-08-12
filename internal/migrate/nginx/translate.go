// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strconv"
	"strings"

	"jul/internal/config"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
)

// translator accumulates a Report while walking an nginx directive tree.
type translator struct {
	report Report
	// httpRealIP is the http-level realip scope, inherited by every server
	// block exactly as nginx inherits the directives.
	httpRealIP realIPPolicy
}

// Translate converts a parsed nginx configuration into a Jul.IA configuration,
// returning the result alongside a Report of what was and was not translated.
// source labels the input (a file path) for the report header.
func Translate(src *ngx.Config, source string) (out *config.Config, rep *Report) {
	t := &translator{report: Report{Source: source}}
	out = &config.Config{}
	rep = &t.report
	defer func() {
		// The tree is already parsed and well-formed, but a best-effort
		// migration tool must never crash on an unusual shape: degrade to a
		// note and return whatever was translated so far.
		if r := recover(); r != nil {
			t.report.note("internal error during translation: %v (output may be incomplete)", r)
		}
	}()

	for _, d := range topLevelDirectives(src) {
		switch d.GetName() {
		case "http":
			t.translateHTTP(d, out)
			// The trusted-proxy policy is listener scoped, so it can only be
			// resolved once every server block on an address is known.
			t.hoistClientAddress(out)
		case "stream", "mail":
			t.report.skip(d, "the "+d.GetName()+" module is not supported")
		case "include":
			t.report.skip(d, "include not followed; import each included file separately")
		default:
			if !isIgnorableProcessDirective(d.GetName()) {
				t.report.skip(d, "unsupported top-level directive")
			}
		}
	}
	return out, rep
}

// translateHTTP walks the directives inside an http block.
func (t *translator) translateHTTP(d ngx.IDirective, out *config.Config) {
	kids := httpChildren(d)
	// realip directives are collected first: httpChildren yields the server
	// blocks before their sibling directives, so a server would otherwise be
	// translated before the http-level policy it inherits has been seen.
	for _, c := range kids {
		if isRealIPDirective(c.GetName()) {
			t.realIPDirective(c.GetName(), paramValues(c), c.GetLine(), &t.httpRealIP)
		}
	}
	for _, c := range kids {
		switch c.GetName() {
		case "server":
			t.translateServer(c, out)
		case "upstream":
			t.translateUpstream(c, out)
		case "gzip":
			if isOn(paramValues(c)) {
				out.Compression.Enabled = config.Bool(true)
			}
		case "include":
			t.report.skip(c, "include not followed; import each included file separately")
		case "map", "geo", "split_clients":
			t.report.skip(c, c.GetName()+" blocks are not supported")
		default:
			if isRealIPDirective(c.GetName()) {
				continue // consumed in the pre-pass above
			}
			t.report.skip(c, "unsupported http-level directive")
		}
	}
}

// translateServer converts one server block into a config.ServerConfig.
func (t *translator) translateServer(d ngx.IDirective, out *config.Config) {
	var s config.ServerConfig
	var realIP realIPPolicy
	var tls config.TLSConfig
	hasTLS := false
	var serverRoot string
	var serverIndex []string
	var serverReturn []string
	serverReturnLine := 0

	for _, c := range children(d) {
		cp := paramValues(c)
		switch c.GetName() {
		case "listen":
			listen, ssl := parseListen(cp)
			if listen == "" {
				t.report.skip(c, "unsupported listen address (e.g. a unix socket)")
			} else if s.Listen == "" {
				s.Listen = listen
			} else if listen != s.Listen {
				t.report.note("server line %d: extra listen %q dropped; one Jul.IA server block binds a single address (kept %q)", c.GetLine(), listen, s.Listen)
			}
			if ssl {
				hasTLS = true
				tls.Enabled = true
			}
		case "server_name":
			for _, n := range cp {
				if n != "_" && n != "" {
					s.ServerNames = append(s.ServerNames, n)
				}
			}
		case "root":
			if len(cp) > 0 {
				serverRoot = cp[0]
			}
		case "index":
			if len(cp) > 0 {
				serverIndex = cp
			}
		case "location":
			if loc, ok := t.translateLocation(c, serverRoot, serverIndex); ok {
				s.Locations = append(s.Locations, loc)
			}
		case "ssl_certificate":
			if len(cp) > 0 {
				tls.Cert = cp[0]
				hasTLS = true
				tls.Enabled = true
			}
		case "ssl_certificate_key":
			if len(cp) > 0 {
				tls.Key = cp[0]
				hasTLS = true
				tls.Enabled = true
			}
		case "ssl_protocols":
			if mv := minTLSFrom(cp, &t.report, c.GetLine()); mv != "" {
				tls.MinVersion = mv
				hasTLS = true
			}
		case "return":
			serverReturn = cp
			serverReturnLine = c.GetLine()
		case "if", "rewrite":
			t.report.skip(c, "server-level "+c.GetName()+" is not translated; port it manually")
		default:
			if t.realIPDirective(c.GetName(), cp, c.GetLine(), &realIP) {
				continue
			}
			t.report.skip(c, "unsupported server-level directive")
		}
	}

	// A server-level `return` applies to every request: synthesize a catch-all.
	if len(serverReturn) > 0 {
		if len(s.Locations) > 0 {
			t.report.note("server return at line %d: synthesized a catch-all '/' but nginx evaluates a server-level return before locations; Jul.IA gives matching locations precedence, so verify the intended order", serverReturnLine)
		}
		loc := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/"}}
		applyReturn(&loc, serverReturn, &t.report, serverReturnLine)
		s.Locations = append(s.Locations, loc)
		t.report.Locations++
	}

	// A server-level root with no location serving "/" is exposed at "/".
	if serverRoot != "" && !hasRootPathLocation(s.Locations) {
		loc := config.LocationConfig{
			Match: config.MatchConfig{Type: "prefix", Path: "/"},
			Root:  serverRoot,
		}
		if len(serverIndex) > 0 {
			loc.Index = serverIndex
		}
		s.Locations = append(s.Locations, loc)
		t.report.Locations++
	}

	if s.Listen == "" {
		if hasTLS {
			s.Listen = ":443"
		} else {
			s.Listen = ":80"
		}
	}
	if hasTLS {
		s.TLS = &tls
	}
	s.ClientAddress = t.clientAddressFrom(t.httpRealIP.merge(realIP))
	out.Servers = append(out.Servers, s)
	t.report.Servers++
}

// translateLocation converts one location block. serverRoot/serverIndex are the
// inherited static-file defaults from the enclosing server. ok is false when the
// location could not be represented (and was reported instead).
func (t *translator) translateLocation(d ngx.IDirective, serverRoot string, serverIndex []string) (config.LocationConfig, bool) {
	mod, path, ok := locationModifierAndPath(d)
	if !ok {
		t.report.skipNamed("location", d.GetLine(), "could not parse the location match")
		return config.LocationConfig{}, false
	}
	if strings.HasPrefix(path, "@") {
		t.report.skipNamed("location "+path, d.GetLine(), "named locations are not supported")
		return config.LocationConfig{}, false
	}
	match, mok := matchConfig(mod, path, &t.report, d.GetLine())
	if !mok {
		t.report.skipNamed("location "+path, d.GetLine(), "unsupported location match")
		return config.LocationConfig{}, false
	}

	loc := config.LocationConfig{Match: match}
	root := serverRoot
	index := serverIndex

	for _, c := range children(d) {
		cp := paramValues(c)
		switch c.GetName() {
		case "proxy_pass":
			if len(cp) > 0 {
				loc.ProxyPass = translateProxyPass(cp[0], &t.report, c.GetLine())
			}
		case "fastcgi_pass":
			if len(cp) > 0 {
				loc.FastCGIPass = cp[0]
			}
		case "root":
			if len(cp) > 0 {
				root = cp[0]
			}
		case "alias":
			if len(cp) > 0 {
				root = cp[0]
				t.report.note("location %s at line %d: alias mapped to root (path semantics differ slightly)", path, c.GetLine())
			}
		case "index":
			if len(cp) > 0 {
				index = cp
			}
		case "try_files":
			loc.TryFiles = cp
		case "return":
			applyReturn(&loc, cp, &t.report, c.GetLine())
		case "rewrite":
			applyRewrite(&loc, cp, &t.report, c.GetLine())
		case "if", "limit_except":
			t.report.skip(c, "location-level "+c.GetName()+" is not translated")
		default:
			t.report.skip(c, "unsupported location-level directive")
		}
	}

	// Inherited root/index apply only to static locations (no other action).
	if loc.ProxyPass == "" && loc.FastCGIPass == "" && loc.Return == 0 && loc.Redirect == "" {
		if root != "" {
			loc.Root = root
		}
		if len(index) > 0 {
			loc.Index = index
		}
	}

	t.report.Locations++
	return loc, true
}

// translateUpstream converts an upstream block into a config.UpstreamConfig,
// preserving per-server weights and mapping the balancing method.
func (t *translator) translateUpstream(d ngx.IDirective, out *config.Config) {
	name, servers, others := upstreamFrom(d)
	if name == "" {
		t.report.skip(d, "upstream block without a name")
		return
	}
	u := config.UpstreamConfig{Name: name}
	strategy := "round_robin"
	weighted := false
	for _, s := range servers {
		if s.addr == "" {
			continue
		}
		if s.down {
			t.report.note("upstream %s: server %s (line %d) is marked down and was omitted", name, s.addr, s.line)
			continue
		}
		u.Servers = append(u.Servers, config.UpstreamServer{Address: s.addr, Weight: s.weight})
		if s.weight > 1 {
			weighted = true
		}
	}
	for _, o := range others {
		switch o.GetName() {
		case "least_conn":
			strategy = "least_conn"
		case "ip_hash", "hash", "random":
			t.report.note("upstream %s: %s balancing is not supported; using round_robin", name, o.GetName())
		case "keepalive", "keepalive_timeout", "keepalive_requests", "zone":
			// connection-pool tuning; safe to ignore
		default:
			t.report.skip(o, "unsupported upstream directive")
		}
	}
	if weighted && strategy == "round_robin" {
		strategy = "weighted_round_robin"
	}
	u.Strategy = strategy
	out.Upstreams = append(out.Upstreams, u)
	t.report.Upstreams++
}

// ----- directive-level helpers -------------------------------------------------

// parseListen extracts the listen address and whether the listener is TLS. It
// normalizes a bare port ("80") to ":80" and wildcard/IPv6-any forms to ":port",
// and returns an empty address for forms it cannot map (e.g. unix sockets).
func parseListen(params []string) (listen string, ssl bool) {
	for _, p := range params {
		if p == "ssl" {
			ssl = true
		}
	}
	if len(params) == 0 {
		return "", ssl
	}
	addr := params[0]
	switch {
	case addr == "ssl":
		return "", ssl
	case isAllDigits(addr):
		return ":" + addr, ssl
	case strings.HasPrefix(addr, "*:"):
		return ":" + addr[2:], ssl
	case strings.HasPrefix(addr, "[::]:"):
		return ":" + addr[len("[::]:"):], ssl
	case strings.HasPrefix(addr, "unix:"):
		return "", ssl
	default:
		return addr, ssl
	}
}

// matchConfig maps an nginx location modifier and path to a Jul.IA match. ok is
// false for forms that cannot be represented.
func matchConfig(mod, path string, rep *Report, line int) (config.MatchConfig, bool) {
	switch mod {
	case "=":
		return config.MatchConfig{Type: "exact", Path: path}, true
	case "~":
		return config.MatchConfig{Type: "regex", Path: path}, true
	case "~*":
		rep.note("location ~* %q at line %d: case-insensitive regex mapped to regex (case sensitivity not preserved)", path, line)
		return config.MatchConfig{Type: "regex", Path: path}, true
	case "^~":
		return config.MatchConfig{Type: "prefix", Path: path}, true
	case "":
		return config.MatchConfig{Type: "prefix", Path: path}, true
	default:
		rep.note("location modifier %q at line %d treated as a prefix match", mod, line)
		return config.MatchConfig{Type: "prefix", Path: path}, true
	}
}

// translateProxyPass normalizes an nginx proxy_pass value into a Jul.IA
// proxy_pass URL. A bare host gets an http:// scheme; an upstream name is kept
// verbatim so it resolves to the imported [[upstreams]] pool.
func translateProxyPass(v string, rep *Report, line int) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	trimmed := strings.TrimRight(v, "/")
	if trimmed != v {
		rep.note("proxy_pass %q at line %d: trailing slash dropped; nginx rewrites the matched location prefix on a trailing-slash target, which Jul.IA does not — adjust the location/upstream path if needed", v, line)
	}
	return trimmed
}

// applyReturn maps an nginx `return` directive onto a location.
func applyReturn(loc *config.LocationConfig, params []string, rep *Report, line int) {
	if len(params) == 0 {
		return
	}
	code, err := strconv.Atoi(params[0])
	if err != nil {
		// `return <url>` is an implicit 302 redirect.
		loc.Return = 302
		loc.Redirect = params[0]
		return
	}
	loc.Return = code
	if len(params) >= 2 {
		arg := params[1]
		if code >= 300 && code < 400 {
			loc.Redirect = arg
		} else {
			rep.note("return %d at line %d: response text %q was dropped (no body field)", code, line, arg)
		}
	}
}

// applyRewrite maps an nginx `rewrite` directive onto a location.
func applyRewrite(loc *config.LocationConfig, params []string, rep *Report, line int) {
	if len(params) < 2 {
		rep.skipNamed("rewrite", line, "rewrite needs a pattern and a replacement")
		return
	}
	rw := config.RewriteConfig{Pattern: params[0], Replacement: params[1]}
	if len(params) >= 3 {
		switch params[2] {
		case "last", "break", "redirect", "permanent":
			rw.Flag = params[2]
		default:
			rep.note("rewrite flag %q at line %d was ignored", params[2], line)
		}
	}
	loc.Rewrites = append(loc.Rewrites, rw)
}

// minTLSFrom maps an nginx ssl_protocols list to a Jul.IA min_version. Legacy
// protocols raise the floor to 1.2 (with a note) since Jul.IA supports only 1.2
// and 1.3.
func minTLSFrom(params []string, rep *Report, line int) string {
	var has12, has13, hasLegacy bool
	for _, p := range params {
		switch p {
		case "TLSv1.2":
			has12 = true
		case "TLSv1.3":
			has13 = true
		case "TLSv1", "TLSv1.1", "SSLv3", "SSLv2":
			hasLegacy = true
		}
	}
	switch {
	case hasLegacy:
		rep.note("ssl_protocols at line %d lists legacy protocols; min_version set to 1.2", line)
		return "1.2"
	case has12:
		return "1.2"
	case has13:
		return "1.3"
	default:
		return ""
	}
}

// ----- tree helpers ------------------------------------------------------------

// serverSpec is an upstream backend extracted from a `server` directive.
type serverSpec struct {
	addr   string
	weight int
	down   bool
	line   int
}

// topLevelDirectives returns the directives at the root of the configuration.
func topLevelDirectives(src *ngx.Config) []ngx.IDirective {
	if src == nil {
		return nil
	}
	return src.GetDirectives()
}

// paramValues returns a directive's parameters as plain strings.
func paramValues(d ngx.IDirective) []string {
	ps := d.GetParameters()
	out := make([]string, 0, len(ps))
	for i := range ps {
		out = append(out, ps[i].Value)
	}
	return out
}

// children returns the directives inside a directive's block, or nil when it has
// no block.
func children(d ngx.IDirective) []ngx.IDirective {
	b := d.GetBlock()
	if b == nil {
		return nil
	}
	return b.GetDirectives()
}

// httpChildren returns the directives inside an http block, handling both the
// typed *HTTP wrapper (which holds servers separately) and a raw directive.
func httpChildren(d ngx.IDirective) []ngx.IDirective {
	if h, ok := d.(*ngx.HTTP); ok {
		out := make([]ngx.IDirective, 0, len(h.Servers)+len(h.Directives))
		for _, s := range h.Servers {
			out = append(out, s)
		}
		out = append(out, h.Directives...)
		return out
	}
	return children(d)
}

// locationModifierAndPath returns the modifier and match path of a location,
// handling both the typed *Location wrapper and a raw directive.
func locationModifierAndPath(d ngx.IDirective) (mod, path string, ok bool) {
	if loc, is := d.(*ngx.Location); is && loc.Match != "" {
		return loc.Modifier, loc.Match, true
	}
	ps := paramValues(d)
	switch len(ps) {
	case 0:
		return "", "", false
	case 1:
		return "", ps[0], true
	default:
		return ps[0], ps[1], true
	}
}

// upstreamFrom extracts the name, backend servers, and remaining directives of
// an upstream block, handling both the typed *Upstream wrapper and a raw
// directive.
func upstreamFrom(d ngx.IDirective) (name string, servers []serverSpec, others []ngx.IDirective) {
	if up, ok := d.(*ngx.Upstream); ok {
		name = up.UpstreamName
		for _, us := range up.UpstreamServers {
			servers = append(servers, serverSpecFromTyped(us))
		}
		return name, servers, up.Directives
	}
	ps := paramValues(d)
	if len(ps) > 0 {
		name = ps[0]
	}
	for _, c := range children(d) {
		if c.GetName() == "server" {
			servers = append(servers, serverSpecFromParams(paramValues(c), c.GetLine()))
		} else {
			others = append(others, c)
		}
	}
	return name, servers, others
}

func serverSpecFromTyped(us *ngx.UpstreamServer) serverSpec {
	s := serverSpec{addr: us.Address, line: us.GetLine()}
	if w, ok := us.Parameters["weight"]; ok {
		s.weight = atoiSafe(w)
	}
	for _, f := range us.Flags {
		if f == "down" {
			s.down = true
		}
	}
	return s
}

func serverSpecFromParams(ps []string, line int) serverSpec {
	s := serverSpec{line: line}
	if len(ps) > 0 {
		s.addr = ps[0]
	}
	for _, p := range ps[1:] {
		switch {
		case strings.HasPrefix(p, "weight="):
			s.weight = atoiSafe(strings.TrimPrefix(p, "weight="))
		case p == "down":
			s.down = true
		}
	}
	return s
}

// hasRootPathLocation reports whether any location already matches "/".
func hasRootPathLocation(locs []config.LocationConfig) bool {
	for _, l := range locs {
		if l.Match.Path == "/" {
			return true
		}
	}
	return false
}

// processLevelDirectives are top-level nginx directives that configure the
// nginx process itself and have no per-server Jul.IA equivalent; they are
// ignored without a report entry to keep the output focused on real gaps.
var processLevelDirectives = map[string]bool{
	"events": true, "worker_processes": true, "worker_rlimit_nofile": true,
	"pid": true, "user": true, "daemon": true, "master_process": true,
	"load_module": true, "pcre_jit": true, "error_log": true,
}

func isIgnorableProcessDirective(name string) bool { return processLevelDirectives[name] }

func isOn(params []string) bool {
	return len(params) > 0 && strings.EqualFold(params[0], "on")
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
