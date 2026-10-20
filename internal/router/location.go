// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"fmt"
	"net"
	"net/http"

	"jul/internal/config"
)

// Action names identify the kind of handler a location dispatches to. They
// double as keys in the Builder registry, which is the seam future plugins use
// to register custom handlers without modifying the router.
const (
	ActionStatic        = "static"
	ActionProxy         = "proxy"
	ActionFastCGI       = "fastcgi"
	ActionRedirect      = "redirect"
	ActionReturn        = "return"
	ActionDeny          = "deny"
	ActionGRPCTranscode = "grpc_transcode"
	ActionPlugin        = "plugin"
)

// actionOf derives the action for a location from which fields are set. Deeper
// conflict checks (e.g. root + proxy_pass together) live in config validation.
func actionOf(loc config.LocationConfig) (string, error) {
	switch {
	case loc.Deny:
		return ActionDeny, nil
	case loc.Redirect != "":
		return ActionRedirect, nil
	case loc.Return != 0:
		return ActionReturn, nil
	case loc.ProxyPass != "":
		return ActionProxy, nil
	case loc.FastCGIPass != "" || loc.UWSGIPass != "":
		return ActionFastCGI, nil
	case loc.GRPCTranscode != nil:
		return ActionGRPCTranscode, nil
	case loc.Plugin != "":
		return ActionPlugin, nil
	case loc.Root != "":
		return ActionStatic, nil
	default:
		return "", fmt.Errorf("location %q has no action (set one of root, proxy_pass, fastcgi_pass, redirect, return, deny, grpc_transcode, or plugin)", loc.Match.Path)
	}
}

// builtinBuilders returns the Builders for the actions the router implements
// itself because they derive entirely from config and need no external handler
// package: deny, redirect, and return. New seeds these into the registry before
// the caller's content builders, so every action — built-in, content, or future
// plugin — flows through the one uniform Builder lookup in buildServerRoute.
func builtinBuilders() map[string]Builder {
	return map[string]Builder{
		ActionDeny: func(config.ServerConfig, config.LocationConfig) (http.Handler, error) {
			return denyHandler(), nil
		},
		ActionRedirect: func(_ config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
			return redirectHandler(loc), nil
		},
		ActionReturn: func(_ config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
			return redirectHandler(loc), nil
		},
	}
}

// denyHandler rejects every request with 403.
func denyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "403 Forbidden", http.StatusForbidden)
	})
}

// buildWithdrawnHandler constructs the stage-0 outermost withdrawal gate handler
// (ADR 0026 #642 amendment, spec §9.3). It applies any configured location
// response_headers operations, then unconditionally sets Cache-Control: no-store
// and removes Expires so conflicting response_headers or expires policies can
// never make a withdrawn 403 response cacheable.
func buildWithdrawnHandler(headerOps []config.ResponseHeaderOp) http.Handler {
	type compiledOp struct {
		op    string
		name  string
		value string
	}
	ops := make([]compiledOp, 0, len(headerOps))
	for _, o := range headerOps {
		val := ""
		if o.Value != nil {
			val = *o.Value
		}
		ops = append(ops, compiledOp{
			op:    o.Op,
			name:  http.CanonicalHeaderKey(o.Name),
			value: val,
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		for _, op := range ops {
			switch op.op {
			case "add":
				h.Add(op.name, op.value)
			case "set":
				h.Set(op.name, op.value)
			case "remove":
				h.Del(op.name)
			}
		}
		h.Set("Cache-Control", "no-store")
		h.Del("Expires")
		http.Error(w, "403 Forbidden", http.StatusForbidden)
	})
}

// redirectHandler implements both the "redirect" and "return" actions.
func redirectHandler(loc config.LocationConfig) http.Handler {
	code := loc.Return
	target := loc.Redirect
	if target != "" && code == 0 {
		code = http.StatusFound
	}
	if code == 0 {
		code = http.StatusOK
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if target != "" {
			http.Redirect(w, r, target, code)
			return
		}
		w.WriteHeader(code)
	})
}

// redirectToHTTPS issues a redirect to the HTTPS equivalent of the request URL,
// preserving host (port stripped) and request URI.
func redirectToHTTPS(w http.ResponseWriter, r *http.Request, code int) {
	// Config validation already restricts redirect_https to 301 or 308 (see
	// config.Validate), so this coercion is belt-and-suspenders for an
	// unvalidated zero value rather than a silent override of an operator choice.
	if code != http.StatusMovedPermanently && code != http.StatusPermanentRedirect {
		code = http.StatusMovedPermanently
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	target := "https://" + host + r.URL.RequestURI()
	http.Redirect(w, r, target, code)
}
