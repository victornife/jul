// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/plugins"
	"jul/internal/router"
	"jul/internal/waf"
)

// inspectionRequirementRecord is a generation's route-aware minimum budget
// (ADR 0032 §7), published so the server can attach it to the limits it
// installs for that generation.
type inspectionRequirementRecord struct {
	genID uint64
	set   bool
	req   inspection.Requirement
}

// listenerHeaderBytes mirrors the server's effective max_header_bytes for a
// listener: the first server block on the address decides.
func listenerHeaderBytes(c *config.Config, listen string) uint64 {
	for i := range c.Servers {
		if c.Servers[i].Listen == listen {
			if n := c.Servers[i].MaxHeaderBytes.Bytes(); n > 0 {
				return uint64(n)
			}
			break
		}
	}
	return inspection.DefaultRequestHeaderBytes
}

// inspectionRouteFacts describes every configured route's inspection
// composition: the middleware plugins (server then location), the terminal
// handler plugin when it is the route's action, and the effective WAF.
func inspectionRouteFacts(c *config.Config, pluginFacts func(string) (inspection.PluginFacts, bool), wafFacts func(srv config.ServerConfig, loc config.LocationConfig) *inspection.WAFFacts) []inspection.RouteFacts {
	var routes []inspection.RouteFacts
	for i := range c.Servers {
		srv := c.Servers[i]
		for j := range srv.Locations {
			loc := srv.Locations[j]
			rf := inspection.RouteFacts{
				Route:              fmt.Sprintf("servers[%d].locations[%d] (%s %s)", i, j, srv.Listen, loc.Match.Path),
				WAF:                wafFacts(srv, loc),
				RequestHeaderBytes: listenerHeaderBytes(c, srv.Listen),
			}
			for _, name := range append(append([]string(nil), srv.Plugins...), loc.Plugins...) {
				if pf, ok := pluginFacts(name); ok {
					rf.Plugins = append(rf.Plugins, pf)
				}
			}
			if action, err := router.ActionOf(loc); err == nil && action == router.ActionPlugin {
				if pf, ok := pluginFacts(loc.Plugin); ok {
					pf.SubscribesResponse = false
					pf.ReplacesResponseBody = false
					rf.Plugins = append(rf.Plugins, pf)
				}
			}
			routes = append(routes, rf)
		}
	}
	return routes
}

// inspectionRequirement computes the configuration's route-aware minimum
// budget and checks it against the configured limits. A shortfall is
// returned as err in enforce mode and as warning in observe mode.
func inspectionRequirement(c *config.Config, routes []inspection.RouteFacts) (req inspection.Requirement, warning, err error) {
	limits, err := inspection.LimitsFromConfig(c, 0)
	if err != nil {
		return req, nil, err
	}
	req, err = inspection.MinimumBudget(routes, limits.BaseChargeBytes, limits.MaxResponseHeaderBytes)
	if err != nil {
		return req, nil, fmt.Errorf("inspection: %w", err)
	}
	if shortfall := inspection.CheckBudget(limits, req); shortfall != nil {
		if limits.Mode == inspection.Enforce {
			return req, nil, fmt.Errorf("inspection: %w", shortfall)
		}
		return req, fmt.Errorf("inspection (observe): %w", shortfall), nil
	}
	return req, nil, nil
}

func (f *HandlerFactory) publishInspectionRequirement(genID uint64, req inspection.Requirement) {
	f.moduleMu.Lock()
	defer f.moduleMu.Unlock()
	f.inspectionReq = inspectionRequirementRecord{genID: genID, set: true, req: req}
}

// InspectionRequirement returns generation genID's route-aware minimum
// budget: the largest single-request worst-case charge and the route that
// needs it. Prepare records it before commit so reload can attach it to the
// candidate limits. ok is false for an unknown generation.
func (f *HandlerFactory) InspectionRequirement(genID uint64) (minimum uint64, route string, ok bool) {
	f.moduleMu.Lock()
	defer f.moduleMu.Unlock()
	if !f.inspectionReq.set || f.inspectionReq.genID != genID {
		return 0, "", false
	}
	return f.inspectionReq.req.Bytes, f.inspectionReq.req.Route, true
}

// CheckInspectionBudget is the offline (`jul check`) form of the route-aware
// minimum-budget check that startup, reload and admin apply perform while
// building handlers. It compiles the effective WAF policies and plugins of a
// secret-resolved clone, reads their static limits and closes them. Plugins
// are compiled and ABI-negotiated but never instantiated, so no guest code
// (start function or _initialize) runs during an offline check. A shortfall
// is an error in enforce mode and a returned warning in observe mode.
func CheckInspectionBudget(ctx context.Context, c *config.Config) (warning error, err error) {
	err = config.PreflightClone(c, func(clone *config.Config) error {
		pluginFacts := func(string) (inspection.PluginFacts, bool) { return inspection.PluginFacts{}, false }
		if len(clone.Plugins) > 0 && plugins.Compiled {
			mgr, err := plugins.NewManager(plugins.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			if err != nil {
				return fmt.Errorf("plugins: %w", err)
			}
			defer mgr.Close()
			facts, err := mgr.InspectionFacts(ctx, clone.Plugins)
			if err != nil {
				return fmt.Errorf("plugins: %w", err)
			}
			pluginFacts = func(name string) (inspection.PluginFacts, bool) {
				pf, ok := facts[name]
				return pf, ok
			}
		}
		firewalls := map[string]*inspection.WAFFacts{}
		var compiled []*waf.Firewall
		defer func() {
			for _, fw := range compiled {
				fw.Close()
			}
		}()
		var compileErr error
		wafFacts := func(srv config.ServerConfig, loc config.LocationConfig) *inspection.WAFFacts {
			if !waf.Compiled || compileErr != nil {
				return nil
			}
			wcfg, ok := EffectiveWAF(clone, loc)
			if !ok {
				return nil
			}
			key := "global"
			if loc.WAF != nil {
				key = WAFScope(srv, loc)
			}
			if facts, ok := firewalls[key]; ok {
				return facts
			}
			fw, err := waf.New(ctx, wcfg, waf.Options{})
			if err != nil {
				compileErr = fmt.Errorf("waf: %w", err)
				return nil
			}
			compiled = append(compiled, fw)
			facts := fw.InspectionFacts()
			firewalls[key] = &facts
			return &facts
		}
		routes := inspectionRouteFacts(clone, pluginFacts, wafFacts)
		if compileErr != nil {
			return compileErr
		}
		var reqErr error
		_, warning, reqErr = inspectionRequirement(clone, routes)
		return reqErr
	})
	if err != nil {
		return nil, err
	}
	return warning, nil
}
