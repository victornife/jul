// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"bytes"
	"fmt"
	"text/template"
)

const (
	adminToken  = "assessment-695-token-not-a-secret" // local, synthetic, loopback-only
	wafReqMark  = "ASSESS-WAF-REQ-BLOCK"
	wafRespMark = "ASSESS-WAF-RESP-BLOCK"
	wasmReqMark = "ASSESS-WASM-REQ-BLOCK"
)

// Adapter describes how a target binary's configuration surface differs. Only
// known capabilities are generated; unknown adapters are rejected.
type Adapter struct {
	Name string `json:"name"`
	// GlobalInspection reports whether the binary accepts [global.inspection].
	GlobalInspection bool `json:"global_inspection"`
	// WAFBudget is the explicit max_buffered_bytes written for variants with
	// WAF. It is required because the enforce-mode minimum-budget check of
	// post-I1 binaries derives a floor from the 1 MiB WAF request limit that
	// exceeds the 256 MiB default; it is not evidence that the process fits
	// the envelope.
	WAFBudget   string `json:"waf_max_buffered_bytes,omitempty"`
	Description string `json:"description"`
}

// Adapters are the known configuration surfaces.
var Adapters = map[string]Adapter{
	"baseline-858590dc": {Name: "baseline-858590dc", Description: "ADR 0032 comparison baseline (#702, #703); no [global.inspection] table"},
	"current": {Name: "current", GlobalInspection: true, WAFBudget: "512m",
		Description: "post-I1 main; [global.inspection] defaults (enforce, 256m, body_timeout 60s, 64 KiB header ceiling) except an explicit 512m budget for WAF variants"},
}

// ConfigParams are the per-run template inputs.
type ConfigParams struct {
	Adapter           Adapter
	WAF, WASM, Cache  bool
	Port, TLSPort     int
	AdminPort         int
	Origin            string
	WWW, Plugin, Cert string
	Key               string
	NameA             string // inspect-a label; changing it is a real plugin policy change
	WAFRuleID         int    // request canary rule id; changing it is a real WAF policy change
	BrokenWAF         bool   // an invalid directive: preparation must fail
	ReadTimeout       string
	MaxPending        int
}

// VariantFlags maps a variant to its inspection layers.
func VariantFlags(v string) (waf, wasm bool) {
	return v == "waf" || v == "both", v == "wasm" || v == "both"
}

const configTemplate = `[global]
log_level = "warn"
{{- if and .Adapter.GlobalInspection .WAF .Adapter.WAFBudget}}

[global.inspection]
max_buffered_bytes = "{{.Adapter.WAFBudget}}"
{{- end}}

[admin]
enabled = true
listen = "127.0.0.1:{{.AdminPort}}"
token = "` + adminToken + `"
pprof = true
console = false
{{if .Cache}}
[cache]
enabled = true
memory_max_size = "64MB"
max_entry_size = "4MB"
default_ttl = "5m"
{{end}}
[[upstreams]]
name = "origin"
strategy = "round_robin"
servers = [{ address = "{{.Origin}}" }]

[[upstreams]]
name = "limited"
strategy = "round_robin"
servers = [{ address = "{{.Origin}}" }]
  [upstreams.resilience]
  max_active_requests = 8
  max_pending_requests = {{.MaxPending}}
  pending_timeout = "2s"
{{if .WASM}}
[plugins.inspect-a]
path = "{{.Plugin}}"
abi = "jul-abi/v2"
config = { name = "{{.NameA}}", read_body = "true", subscribe = "true" }

[plugins.inspect-b]
path = "{{.Plugin}}"
abi = "jul-abi/v2"
config = { name = "b", read_body = "true", subscribe = "true", replace = "true" }
{{end}}
{{define "locs"}}
  [[servers.locations]]
  match = { type = "exact", path = "/healthz" }
  return = 204
{{range $l := .Locs}}
  [[servers.locations]]
  match = { type = "prefix", path = "{{$l.Path}}" }
  {{$l.Action}}
{{- if $.P.Cache}}{{if $l.Cache}}
  cache = true{{end}}{{end}}
{{- if $.P.WASM}}
  plugins = ["inspect-a", "inspect-b"]{{end}}
{{- if $.P.WAF}}
  [servers.locations.waf]
  enabled = true
  mode = "block"
  crs_enabled = true
  request_body_limit = "1mb"
  response_body_check = true
  inline_rules = '''
SecRule REQUEST_BODY|ARGS_POST "@contains ` + wafReqMark + `" "id:{{$.P.WAFRuleID}},phase:2,deny,status:403,log,msg:'assessment request canary'"
SecRule RESPONSE_BODY "@contains ` + wafRespMark + `" "id:1695102,phase:4,deny,status:403,log,msg:'assessment response canary'"
{{- if $.P.BrokenWAF}}
SecRule THIS_IS_NOT_A_VARIABLE "@rx (" "id:1695199,phase:1,deny"
{{- end}}
'''
{{- end}}
{{end}}{{end}}
[[servers]]
listen = "127.0.0.1:{{.Port}}"
{{- if .ReadTimeout}}
read_timeout = "{{.ReadTimeout}}"{{end}}
{{template "locs" (locs .)}}
[[servers]]
listen = "127.0.0.1:{{.TLSPort}}"
server_names = ["localhost"]
{{- if .ReadTimeout}}
read_timeout = "{{.ReadTimeout}}"{{end}}
  [servers.tls]
  enabled = true
  cert = "{{.Cert}}"
  key = "{{.Key}}"
  [servers.http3]
  enabled = true
{{template "locs" (locs .)}}`

type locSpec struct {
	Path, Action string
	Cache        bool
}

type locsData struct {
	P    ConfigParams
	Locs []locSpec
}

var configTmpl = template.Must(template.New("cfg").Funcs(template.FuncMap{
	"locs": func(p ConfigParams) locsData {
		return locsData{P: p, Locs: []locSpec{
			{Path: "/proxy/", Action: `proxy_pass = "http://origin"`},
			{Path: "/admitted/", Action: `proxy_pass = "http://limited"`},
			{Path: "/cached/", Action: `proxy_pass = "http://origin"`, Cache: true},
			{Path: "/static/", Action: fmt.Sprintf(`root = %q`, p.WWW)},
		}}
	},
}).Parse(configTemplate))

// RenderConfig renders a Jul configuration. Defaults match the archive.
func RenderConfig(p ConfigParams) ([]byte, error) {
	if p.Adapter.Name == "" {
		return nil, fmt.Errorf("configuration adapter is required")
	}
	if p.NameA == "" {
		p.NameA = "a"
	}
	if p.WAFRuleID == 0 {
		p.WAFRuleID = 1695101
	}
	var b bytes.Buffer
	if err := configTmpl.Execute(&b, p); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
