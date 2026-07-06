// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	coreruleset "github.com/corazawaf/coraza-coreruleset/v4"
	"github.com/corazawaf/coraza/v3"
	corazahttp "github.com/corazawaf/coraza/v3/http"
	"github.com/corazawaf/coraza/v3/types"
	"github.com/jcchavezs/mergefs"
	mergefsio "github.com/jcchavezs/mergefs/io"

	"jul/internal/config"
	"jul/internal/middleware"
)

// Compiled reports whether this binary includes WAF support. It is true in
// builds with the "waf" tag, which link the Coraza engine and the embedded
// OWASP Core Rule Set.
const Compiled = true

// Firewall wraps a configured Coraza engine and exposes it as a middleware.
type Firewall struct {
	waf coraza.WAF
}

// New builds a Firewall from a WAF policy. It assembles the SecLang directive
// program (optional embedded CRS, user directive files, inline rules, and the
// enforcement-mode override), compiles the engine, and wires the per-rule error
// callback to the supplied metrics/log hooks. It returns an error if the rules
// fail to compile so a reload surfaces the problem instead of silently serving
// without protection.
func New(cfg config.WAFConfig, opts Options) (*Firewall, error) {
	directives, err := buildDirectives(cfg)
	if err != nil {
		return nil, err
	}

	wcfg := coraza.NewWAFConfig().
		WithRootFS(rootFS(cfg)).
		WithDirectives(directives).
		WithRequestBodyAccess().
		WithErrorCallback(errorCallback(cfg, opts))
	if limit := cfg.RequestBodyLimit.Bytes(); limit > 0 {
		wcfg = wcfg.WithRequestBodyLimit(int(limit))
	}
	if cfg.ResponseBodyCheck {
		wcfg = wcfg.WithResponseBodyAccess()
	}

	w, err := coraza.NewWAF(wcfg)
	if err != nil {
		return nil, fmt.Errorf("waf: compiling rules: %w", err)
	}
	return &Firewall{waf: w}, nil
}

// Middleware returns the per-location middleware that runs each request (and,
// when response_body_check is set, each response) through the engine. A blocked
// request is short-circuited by Coraza with the status configured in the
// SecLang program.
func (f *Firewall) Middleware() middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return corazahttp.WrapHandler(f.waf, next)
	}
}

// Close releases engine resources. The Coraza engine holds no resources that
// require explicit release, so this is a no-op kept for interface symmetry with
// the stub.
func (f *Firewall) Close() error { return nil }

// rootFS selects the filesystem the SecLang parser resolves Include directives
// against. With the embedded CRS it merges the rule-set assets with the OS
// filesystem so both "@owasp_crs/..." includes and user files on disk resolve;
// otherwise it is the OS filesystem alone.
func rootFS(cfg config.WAFConfig) fs.FS {
	var root fs.FS
	if cfg.CRSEnabled {
		root = mergefs.Merge(coreruleset.FS, mergefsio.OSFS)
	} else {
		root = mergefsio.OSFS
	}
	return &normalizeFS{inner: root}
}

// buildDirectives assembles the SecLang program in a deterministic order:
//
//  1. SecDefaultAction (all phases) with the configured block_status — emitted
//     ONLY when crs_enabled is false — so that any rule using the generic
//     "block" action or no explicit disruptive action inherits
//     "deny,status:<block_status>" instead of Coraza's hardcoded 403 fallback.
//     When CRS is enabled this step is skipped: @crs-setup.conf.example already
//     defines its own SecDefaultAction for phases 1–4 and Coraza rejects a
//     duplicate, so CRS-scored anomaly blocks use the CRS setup's default
//     status (403) rather than block_status. See docs/waf.md for the overrides
//     (inline_rule / directives_files) that recover block_status under CRS.
//  2. the embedded CRS when crs_enabled;
//  3. each user directive file;
//  4. the inline rules snippet;
//  5. the enforcement-mode override last, so "block"/"detect" always wins.
func buildDirectives(cfg config.WAFConfig) (string, error) {
	var b strings.Builder

	bs := cfg.BlockStatus
	if bs == 0 {
		bs = 403
	}
	// SecDefaultAction sets the default deny status for rules without an
	// explicit status action.  We emit it before user rules so inline rules
	// inherit the configured block_status.
	//
	// When CRS is enabled we skip our SecDefaultAction because the embedded
	// @crs-setup.conf.example already defines its own for phases 1–4 and
	// Coraza rejects duplicates.  Consequently CRS-scored anomaly blocks use
	// the CRS setup's default status (403) rather than block_status.
	// See docs/waf.md for work-arounds (inline_rule overrides, directives_files).
	if !cfg.CRSEnabled {
		for phase := 1; phase <= 4; phase++ {
			fmt.Fprintf(&b, "SecDefaultAction \"phase:%d,deny,status:%d,log\"\n", phase, bs)
		}
	}

	if cfg.CRSEnabled {
		b.WriteString("Include @coraza.conf-recommended\n")
		b.WriteString("Include @crs-setup.conf.example\n")
		if cfg.Paranoia > 0 {
			fmt.Fprintf(&b,
				"SecAction \"id:900000,phase:1,nolog,pass,t:none,setvar:tx.blocking_paranoia_level=%d,setvar:tx.detection_paranoia_level=%d\"\n",
				cfg.Paranoia, cfg.Paranoia)
		}
		b.WriteString("Include @owasp_crs/*.conf\n")
	}

	for _, path := range cfg.DirectivesFiles {
		p := strings.TrimSpace(path)
		if p == "" {
			continue
		}
		fmt.Fprintf(&b, "Include %s\n", p)
	}

	if r := strings.TrimSpace(cfg.InlineRules); r != "" {
		b.WriteString(r)
		b.WriteByte('\n')
	}

	// Enforce the configured mode last so it overrides any engine state set by
	// the included rule files.
	switch cfg.Mode {
	case "detect":
		b.WriteString("SecRuleEngine DetectionOnly\n")
	default:
		b.WriteString("SecRuleEngine On\n")
	}

	return b.String(), nil
}

// errorCallback reports each matched rule to the metrics and log hooks. Coraza
// invokes it per relevant rule match (in both block and detect modes); the
// configured mode labels the event so dashboards can tell enforced blocks from
// detection-only signals.
func errorCallback(cfg config.WAFConfig, opts Options) func(types.MatchedRule) {
	return func(mr types.MatchedRule) {
		ruleID := strconv.Itoa(mr.Rule().ID())
		if opts.Hooks.OnEvent != nil {
			opts.Hooks.OnEvent(cfg.Mode, ruleID)
		}
		if opts.Logger != nil {
			opts.Logger.Warn("waf rule matched",
				"rule_id", ruleID,
				"uri", mr.URI(),
				"mode", cfg.Mode,
				"message", mr.Message())
		}
	}
}
