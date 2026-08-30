// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

// Package nginx translates an NGINX configuration into a Jul.IA configuration.
// It is a best-effort migration aid: common directives (http/server/location,
// listen, server_name, root/index/try_files, proxy_pass with upstreams,
// return/rewrite, basic TLS) are translated, and everything it cannot map is
// reported with a line reference rather than silently dropped.
//
// The package is compiled only into builds with the "importer" tag so the
// nginx-parser dependency stays out of the lean default binary.
package nginx

import (
	"fmt"

	"jul/internal/config"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
	ngxparser "github.com/tufanbarisyildirim/gonginx/parser"
)

// ImportFile parses an nginx configuration file and translates it into a Jul.IA
// configuration, returning the result along with a Report of everything that
// could not be translated. It is the single entry point used by the CLI.
func ImportFile(path string) (*config.Config, *Report, error) {
	src, err := parseFile(path)
	if err != nil {
		return nil, nil, err
	}
	cfg, rep := Translate(src, path)
	if rep != nil {
		rep.Assessment = BuildAssessment(src, path, rep)
	}
	return cfg, rep, nil
}

// parseFile reads and parses an nginx configuration file into its directive
// tree. Unknown directives are tolerated (reported later by the translator)
// rather than failing the parse, and any panic from the third-party parser is
// converted into an error so a malformed file never crashes the tool.
func parseFile(path string) (cfg *ngx.Config, err error) {
	defer func() {
		if r := recover(); r != nil {
			cfg, err = nil, fmt.Errorf("nginx parse failed: %v", r)
		}
	}()
	p, perr := ngxparser.NewParser(path, ngxparser.WithSkipValidDirectivesErr())
	if perr != nil {
		return nil, perr
	}
	return p.Parse()
}

// parseString parses nginx configuration text. It mirrors parseFile and is used
// by the tests.
func parseString(s string) (cfg *ngx.Config, err error) {
	defer func() {
		if r := recover(); r != nil {
			cfg, err = nil, fmt.Errorf("nginx parse failed: %v", r)
		}
	}()
	return ngxparser.NewStringParser(s, ngxparser.WithSkipValidDirectivesErr()).Parse()
}
