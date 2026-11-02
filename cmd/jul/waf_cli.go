// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"jul/internal/atomicfile"
	"jul/internal/config"
	"jul/internal/versionid"
	"jul/internal/waf"
)

func cmdWAF(args []string) int {
	if len(args) == 0 {
		printWAFUsage()
		return 2
	}
	switch args[0] {
	case "evaluate":
		return cmdWAFEvaluate(args[1:])
	case "manifest":
		return cmdWAFManifest(args[1:])
	case "verify-assets":
		return cmdWAFVerifyAssets(args[1:])
	default:
		printWAFUsage()
		return 2
	}
}

func printWAFUsage() {
	fmt.Fprint(stderr, `usage:
  jul waf evaluate [-config f] [-baseline f] [-fixtures f] [-scope s] [-version-key-file f] [-json]
  jul waf manifest [-config f] [-scope s] [-out f] [-version-key-file f] [-json]
  jul waf verify-assets [-config f] -manifest f [-version-key-file f] [-json]
`)
}

func loadResolvedCLICandidate(ctx context.Context, path string) (*config.Candidate, error) {
	raw, err := config.NewTOMLSource(path).Load()
	if err != nil {
		return nil, fmt.Errorf("load config %s: %w", path, err)
	}
	cand, err := config.NewCandidateContext(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("resolve config %s: %w", path, err)
	}
	if vErr := config.Validate(cand.Effective); vErr != nil {
		return nil, fmt.Errorf("validate config %s: %s", path, cand.Redaction.Apply(vErr.Error()))
	}
	return cand, nil
}

func resolveCLIVersionContext(versionKeyPath string) (*versionid.Context, error) {
	p := strings.TrimSpace(versionKeyPath)
	if p != "" {
		return versionid.Load(p)
	}
	defPath, err := versionid.DefaultPath()
	if err == nil && defPath != "" {
		if _, statErr := os.Lstat(defPath); statErr == nil || !os.IsNotExist(statErr) {
			return versionid.Load(defPath)
		}
	}
	return versionid.Process(), nil
}

func resolveWAFScopeConfig(cfg *config.Config, scope string) (config.WAFConfig, string, error) {
	if cfg == nil {
		return config.WAFConfig{}, "", fmt.Errorf("configuration is nil")
	}
	scope = strings.TrimSpace(scope)
	if scope == "" || scope == "global" {
		if !cfg.WAF.Enabled {
			return config.WAFConfig{}, "global", fmt.Errorf("global [waf] is not enabled in configuration")
		}
		return cfg.WAF, "global", nil
	}
	// 1. Durable route_id has explicit precedence over path selectors.
	for i := range cfg.Servers {
		for j := range cfg.Servers[i].Locations {
			loc := cfg.Servers[i].Locations[j]
			if loc.RouteID != nil && strings.TrimSpace(*loc.RouteID) == scope {
				wcfg, ok := appEffectiveWAF(cfg, loc)
				if !ok {
					return config.WAFConfig{}, scope, fmt.Errorf("WAF is not enabled on route %q", scope)
				}
				return wcfg, scope, nil
			}
		}
	}
	// 2. Otherwise match by path or qualified <server_name><path> / <listen><path>,
	// rejecting ambiguous path selectors across multiple virtual hosts or routes.
	type matchedRoute struct {
		loc   config.LocationConfig
		label string
	}
	var matches []matchedRoute
	for i := range cfg.Servers {
		srv := cfg.Servers[i]
		for j := range srv.Locations {
			loc := srv.Locations[j]
			matched := loc.Match.Path == scope || (srv.Listen != "" && srv.Listen+loc.Match.Path == scope)
			if !matched {
				for _, sn := range srv.ServerNames {
					if sn != "" && sn+loc.Match.Path == scope {
						matched = true
						break
					}
				}
			}
			if matched {
				label := scope
				if loc.RouteID != nil && strings.TrimSpace(*loc.RouteID) != "" {
					label = strings.TrimSpace(*loc.RouteID)
				}
				matches = append(matches, matchedRoute{loc: loc, label: label})
			}
		}
	}
	if len(matches) > 1 {
		return config.WAFConfig{}, scope, fmt.Errorf("waf scope %q is ambiguous across %d routes; specify a unique route_id or <server_name><path>", scope, len(matches))
	}
	if len(matches) == 1 {
		wcfg, ok := appEffectiveWAF(cfg, matches[0].loc)
		if !ok {
			return config.WAFConfig{}, scope, fmt.Errorf("WAF is not enabled on route %q", scope)
		}
		return wcfg, matches[0].label, nil
	}
	return config.WAFConfig{}, scope, fmt.Errorf("route scope %q not found in configuration", scope)
}

func appEffectiveWAF(c *config.Config, loc config.LocationConfig) (config.WAFConfig, bool) {
	if loc.WAF != nil {
		return *loc.WAF, loc.WAF.Enabled
	}
	return c.WAF, c.WAF.Enabled
}

func cmdWAFEvaluate(args []string) int {
	fs := flag.NewFlagSet("waf evaluate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "server.toml", "path to the candidate TOML configuration file")
	baselinePath := fs.String("baseline", "", "optional path to the baseline TOML configuration file")
	fixturesPath := fs.String("fixtures", "", "optional path to a custom JSON fixture pack (default: built-in pack)")
	scope := fs.String("scope", "global", "policy scope: 'global', route_id, or location path")
	versionKeyFile := fs.String("version-key-file", "", "optional path to the server's protected effective-version key file")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON evaluation report")
	timeout := fs.Duration("timeout", 30*time.Second, "evaluation timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		printWAFUsage()
		return 2
	}

	if !waf.Compiled {
		return writeWAFCLIError(*jsonOut, 1, "this binary was built without -tags waf; rebuild with -tags waf to evaluate WAF policies")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	vctx, err := resolveCLIVersionContext(*versionKeyFile)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, err.Error())
	}

	cand, err := loadResolvedCLICandidate(ctx, *configPath)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, err.Error())
	}
	var baseCand *config.Candidate
	redactErr := func(msg string) string {
		msg = cand.Redaction.Apply(msg)
		if baseCand != nil {
			msg = baseCand.Redaction.Apply(msg)
		}
		return msg
	}
	candWAF, resolvedScope, err := resolveWAFScopeConfig(cand.Effective, *scope)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, redactErr(err.Error()))
	}

	var baseWAF *config.WAFConfig
	if strings.TrimSpace(*baselinePath) != "" {
		var bErr error
		baseCand, bErr = loadResolvedCLICandidate(ctx, *baselinePath)
		if bErr != nil {
			return writeWAFCLIError(*jsonOut, 1, redactErr(bErr.Error()))
		}
		bw, _, bErr := resolveWAFScopeConfig(baseCand.Effective, *scope)
		if bErr != nil {
			return writeWAFCLIError(*jsonOut, 1, redactErr(fmt.Sprintf("baseline scope: %v", bErr)))
		}
		baseWAF = &bw
	}

	var pack *waf.WAFFixturePack
	if strings.TrimSpace(*fixturesPath) != "" {
		rawPack, fErr := os.ReadFile(*fixturesPath)
		if fErr != nil {
			return writeWAFCLIError(*jsonOut, 1, redactErr(fmt.Sprintf("read fixtures file: %v", fErr)))
		}
		pack, fErr = waf.ParseFixturePack(rawPack)
		if fErr != nil {
			return writeWAFCLIError(*jsonOut, 1, redactErr(fErr.Error()))
		}
	}

	rep, err := waf.EvaluatePolicy(ctx, resolvedScope, candWAF, baseWAF, pack, vctx)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, redactErr(err.Error()))
	}

	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		fmt.Fprintf(stdout, "WAF Evaluation (%s) — pack %s\n", rep.Summary.Scope, rep.Summary.PackID)
		fmt.Fprintf(stdout, "  Engine: Coraza %s · CRS %s · Completeness: %s\n", rep.Summary.EngineVersion, rep.Summary.EmbeddedCRSVersion, rep.Summary.AnalysisCompleteness)
		fmt.Fprintf(stdout, "  Cases: %d/%d passed (%d legitimate blocked, %d malicious allowed, %d changed from baseline)\n",
			rep.Summary.PassedCases, rep.Summary.TotalCases, rep.Summary.LegitimateBlocked, rep.Summary.MaliciousAllowed, rep.Summary.ChangedFromBaseline)
		for _, c := range rep.Cases {
			verdict := "PASS"
			if !c.PassedExpectation {
				verdict = "FAIL"
			}
			fmt.Fprintf(stdout, "  [%s] %s (%s): outcome=%s status=%d origin_reached=%v\n",
				verdict, c.ID, c.Category, c.CandidateOutcome, c.CandidateStatus, c.CandidateOriginReached)
		}
	}
	if rep.Summary.FailedCases > 0 {
		return 1
	}
	return 0
}

func cmdWAFManifest(args []string) int {
	fs := flag.NewFlagSet("waf manifest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "server.toml", "path to the TOML configuration file")
	scope := fs.String("scope", "global", "policy scope: 'global', route_id, or location path")
	outPath := fs.String("out", "", "optional file path to write the JSON manifest atomically")
	versionKeyFile := fs.String("version-key-file", "", "optional path to the server's protected effective-version key file")
	jsonOut := fs.Bool("json", false, "emit manifest as JSON to stdout")
	timeout := fs.Duration("timeout", 30*time.Second, "manifest build timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		printWAFUsage()
		return 2
	}
	if !waf.Compiled {
		return writeWAFCLIError(*jsonOut, 1, "this binary was built without -tags waf; rebuild with -tags waf to generate WAF policy manifests")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	vctx, err := resolveCLIVersionContext(*versionKeyFile)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, err.Error())
	}

	cand, err := loadResolvedCLICandidate(ctx, *configPath)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, err.Error())
	}
	wcfg, resolvedScope, err := resolveWAFScopeConfig(cand.Effective, *scope)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, cand.Redaction.Apply(err.Error()))
	}

	m, fw, err := waf.BuildPolicyManifest(ctx, resolvedScope, wcfg, vctx)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, cand.Redaction.Apply(err.Error()))
	}
	_ = fw.Close()

	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, cand.Redaction.Apply(err.Error()))
	}
	encoded = append(encoded, '\n')
	if strings.TrimSpace(*outPath) != "" {
		if err := atomicfile.Write(*outPath, encoded, 0o600); err != nil {
			return writeWAFCLIError(*jsonOut, 1, cand.Redaction.Apply(fmt.Sprintf("write manifest %s: %v", *outPath, err)))
		}
	}
	if *jsonOut || strings.TrimSpace(*outPath) == "" {
		_, _ = stdout.Write(encoded)
	} else {
		fmt.Fprintf(stdout, "Wrote WAF policy manifest (%s, %d external assets) to %s\n", m.Scope, len(m.ExternalAssets), *outPath)
	}
	return 0
}

func cmdWAFVerifyAssets(args []string) int {
	fs := flag.NewFlagSet("waf verify-assets", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "server.toml", "path to the TOML configuration file")
	manifestPath := fs.String("manifest", "", "path to the saved WAF policy manifest JSON file")
	versionKeyFile := fs.String("version-key-file", "", "optional path to the server's protected effective-version key file")
	jsonOut := fs.Bool("json", false, "emit verification result as JSON")
	timeout := fs.Duration("timeout", 30*time.Second, "asset verification timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || strings.TrimSpace(*manifestPath) == "" {
		printWAFUsage()
		return 2
	}
	if !waf.Compiled {
		return writeWAFCLIError(*jsonOut, 1, "this binary was built without -tags waf; rebuild with -tags waf to verify WAF policy assets")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	vctx, err := resolveCLIVersionContext(*versionKeyFile)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, err.Error())
	}

	rawManifest, err := os.ReadFile(*manifestPath)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, fmt.Sprintf("read manifest: %v", err))
	}
	manifest, err := waf.ParsePolicyManifest(rawManifest)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, err.Error())
	}

	cand, err := loadResolvedCLICandidate(ctx, *configPath)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, err.Error())
	}
	wcfg, _, err := resolveWAFScopeConfig(cand.Effective, manifest.Scope)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, cand.Redaction.Apply(err.Error()))
	}

	res, err := waf.VerifyPolicyAssets(ctx, wcfg, manifest, vctx)
	if err != nil {
		return writeWAFCLIError(*jsonOut, 1, cand.Redaction.Apply(err.Error()))
	}

	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	} else {
		fmt.Fprintf(stdout, "WAF Asset Verification (%s): status=%s verified=%v\n", res.Scope, res.Status, res.Verified)
		for _, mm := range res.Mismatches {
			fmt.Fprintf(stdout, "  - %s\n", mm)
		}
	}
	if !res.Verified {
		return 1
	}
	return 0
}

func writeWAFCLIError(jsonOut bool, code int, msg string) int {
	if jsonOut {
		_ = json.NewEncoder(stdout).Encode(map[string]any{
			"status": "error",
			"error":  msg,
		})
	} else {
		fmt.Fprintf(stderr, "error: %s\n", msg)
	}
	return code
}
