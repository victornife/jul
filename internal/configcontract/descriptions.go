// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"

	"jul/internal/config"
)

// LoadFieldComments extracts the leading sentence of every exported,
// toml-tagged struct field's Go doc comment from schemaGoPath (Jul's sole
// source of public configuration struct definitions,
// internal/config/schema.go), keyed by "TypeName.FieldName".
//
// This is a pure text-extraction utility, not a second schema walker: it does
// not decide kind, optionality, nesting or dynamism (config.SchemaPaths()
// remains the only authority for that). It lets generated descriptions reuse
// Jul's existing, reviewed Go documentation instead of duplicating it into a
// second hand-written registry — the same discipline ADR 0019 §19 requires of
// the lifecycle and value-contract sources.
func LoadFieldComments(schemaGoPath string) (map[string]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, schemaGoPath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("configcontract: parse %s: %w", schemaGoPath, err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			for _, field := range st.Fields.List {
				if field.Tag == nil || !strings.Contains(field.Tag.Value, "toml:") {
					continue
				}
				if field.Doc == nil {
					continue
				}
				summary := firstSentence(field.Doc.Text())
				if summary == "" {
					continue
				}
				for _, name := range field.Names {
					out[ts.Name.Name+"."+name.Name] = summary
				}
			}
		}
	}
	return out, nil
}

var reWhitespace = regexp.MustCompile(`\s+`)

// abbreviations that end in a period but do not end a sentence.
var sentenceAbbreviations = []string{"e.g.", "i.e.", "etc.", "vs."}

// firstSentence collapses doc to one line and returns its leading sentence,
// so a generated description stays concise (ADR 0019 §9: "descriptions
// should explain what the field means, not become mini tutorials"). Go's RE2
// engine has no lookahead, so the abbreviation guard is a manual scan rather
// than a single regexp.
func firstSentence(doc string) string {
	doc = strings.TrimSpace(reWhitespace.ReplaceAllString(doc, " "))
	for i := 0; i < len(doc)-1; i++ {
		if doc[i] != '.' || doc[i+1] != ' ' {
			continue
		}
		if hasAbbreviationSuffix(doc[:i+1]) {
			continue
		}
		return doc[:i]
	}
	return strings.TrimSuffix(doc, ".")
}

func hasAbbreviationSuffix(s string) bool {
	for _, ab := range sentenceAbbreviations {
		if strings.HasSuffix(s, ab) {
			return true
		}
	}
	return false
}

// DescriptionOverrides supplies a concise factual description for a canonical
// path when no adjacent Go doc comment covers it directly — most commonly
// when two fields share one comment block (e.g. "ClientCert and ClientKey ...
// Both are required together", which go/ast attaches only to ClientCert), or
// a field simply has no comment of its own (e.g. Name/Listen on ServerConfig).
// This is the small, explicit, reviewed table ADR 0019 §21/§9 asks for; it
// supplements the AST-derived descriptions, it does not replace them.
var DescriptionOverrides = map[string]string{
	"admin.enabled":                                 "Enabled turns on the separate admin/observability listener",
	"admin.listen":                                  "Listen is the bind address for the admin/observability listener, defaulting to loopback",
	"cache.enabled":                                 "Enabled turns on the two-tier response cache",
	"compression.enabled":                           "Enabled turns on negotiated response compression",
	"global.access_log":                             "AccessLog is the destination for access records (e.g. a file path or \"stdout\")",
	"global.error_log":                              "ErrorLog is the legacy destination for error records, kept for v1 compatibility",
	"observability.tracing.enabled":                 "Enabled turns on OpenTelemetry distributed tracing",
	"rate_limit.enabled":                            "Enabled turns on request rate limiting",
	"servers.*.access_log":                          "AccessLog overrides the global access-log destination for this server block",
	"servers.*.error_log":                           "ErrorLog overrides the global legacy error-log destination for this server block",
	"servers.*.http3.enabled":                       "Enabled starts a parallel HTTP/3 (QUIC) listener for this server block",
	"servers.*.idle_timeout":                        "IdleTimeout closes an idle keep-alive connection after this period",
	"servers.*.listen":                              "Listen is the bind address (host:port) this server block accepts connections on",
	"servers.*.name":                                "Name is a descriptive label for this server block, used only in projections",
	"servers.*.read_header_timeout":                 "ReadHeaderTimeout bounds reading request headers for this server block",
	"servers.*.read_timeout":                        "ReadTimeout bounds reading the full request for this server block",
	"servers.*.server_names":                        "ServerNames lists the virtual-host names this server block matches, used for routing and TLS certificate selection",
	"servers.*.write_timeout":                       "WriteTimeout bounds writing the response for this server block",
	"servers.*.tls.enabled":                         "Enabled turns on TLS termination for this server block",
	"servers.*.tls.cert":                            "Cert is the path to the PEM certificate file for static (non-ACME) TLS",
	"servers.*.tls.key":                             "Key is the path to the PEM private key file paired with cert",
	"servers.*.tls.acme.enabled":                    "Enabled turns on automatic certificate management (ACME) for this listener",
	"servers.*.locations.*.allow_hidden":            "AllowHidden permits serving dotfiles and other hidden paths from the document root",
	"servers.*.locations.*.auth.deny":               "Deny is a CIDR list evaluated before any credential check; a matching client is rejected outright",
	"servers.*.locations.*.backend_tls.client_key":  "ClientKey is the private key paired with client_cert for backend mutual TLS; both are required together",
	"servers.*.locations.*.cache_control":           "CacheControl sets a fixed Cache-Control response header value for this location",
	"servers.*.locations.*.cors.enabled":            "Enabled turns on this location's CORS policy",
	"servers.*.locations.*.directory_listing":       "DirectoryListing serves an auto-generated index when a directory has no index file",
	"servers.*.locations.*.fastcgi_params.*":        "FastCGIParams are additional FastCGI protocol parameters passed to the backend",
	"servers.*.locations.*.index":                   "Index lists filenames tried, in order, when a request resolves to a directory",
	"servers.*.locations.*.match.path":              "Path is the request path pattern compared according to match.type",
	"servers.*.locations.*.proxy_connect_timeout":   "ProxyConnectTimeout bounds dialing the backend for this location",
	"servers.*.locations.*.proxy_read_timeout":      "ProxyReadTimeout bounds reading the backend's response for this location",
	"servers.*.locations.*.proxy_send_timeout":      "ProxySendTimeout bounds writing the request to the backend for this location",
	"servers.*.locations.*.rate_limit.enabled":      "Enabled turns on this location's rate-limit override",
	"servers.*.locations.*.response_headers.*.name": "Name is the response header field name the operation applies to",
	"servers.*.locations.*.return":                  "Return is the bare HTTP status code this location responds with",
	"servers.*.locations.*.rewrites.*.pattern":      "Pattern is the regular expression matched against the request path",
	"servers.*.locations.*.rewrites.*.replacement":  "Replacement is the substitution text applied to a matched path",
	"servers.*.locations.*.try_files":               "TryFiles lists candidate paths tried in order before falling back to the location's action",
	"servers.*.locations.*.uwsgi_pass":              "UWSGIPass is the uWSGI backend address this location dispatches to",
	"upstreams.*.name":                              "Name is the pool's identifier, referenced by proxy_pass and the admin API",
	"upstreams.*.fail_timeout":                      "FailTimeout is the deprecated spelling of [upstreams.resilience] fail_timeout: how long a backend stays out of rotation before being probed again",
	"upstreams.*.health_check.enabled":              "Enabled turns on active health checking for this upstream pool",
	"upstreams.*.backend_tls.client_key":            "ClientKey is the private key paired with client_cert for backend mutual TLS; both are required together",
	"upstreams.*.discovery.consul.tls.client_key":   "ClientKey is the private key paired with client_cert used to authenticate to the Consul agent; both are required together",
	"upstreams.*.servers.*.address":                 "Address is the backend's host:port (or unix socket path)",
	"upstreams.*.servers.*.weight":                  "Weight biases backend selection under weighted-round-robin; higher values receive proportionally more requests",
}

// DescribeLeaf returns the concise factual description for a schema path,
// preferring an explicit override and falling back to the AST-derived
// doc-comment summary for (DeclaringType, FieldName).
func DescribeLeaf(comments map[string]string, p config.SchemaPath) (string, bool) {
	if d, ok := DescriptionOverrides[p.Path]; ok {
		return d, true
	}
	d, ok := comments[p.DeclaringType+"."+p.FieldName]
	return d, ok
}
