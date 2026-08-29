// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package app holds the composition-root wiring helpers for the jul command.
//
// These functions are the pure, dependency-light pieces of the server's
// composition root (see cmd/jul/main.go): scope keys, listener-set derivation,
// upstream indexing, reload fan-in, and the runtime preflight. Extracting them
// into an importable package lets the wiring be unit-tested directly instead of
// only through a full process boot, following through on ADR-0007's plan to make
// the composition root testable (Finding CQ-2).
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"jul/internal/auth"
	"jul/internal/clientaddr"
	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/server"
	"jul/internal/waf"
)

// RateKeyKind maps a rate-limit key spec to its kind label for metrics, keeping
// cardinality bounded (the raw client value is never used as a label).
func RateKeyKind(spec string) string {
	switch {
	case strings.HasPrefix(spec, "header:"):
		return "header"
	case strings.HasPrefix(spec, "jwt:"):
		return "jwt"
	default:
		return "ip"
	}
}

// LocationScope is the canonical identity of a location's matching behaviour:
// a deterministic digest over its listen address, its normalized server_names
// set, its match type, its path, its normalized predicate set, and the
// preflight-widening bit that makes CORS a matcher input (ADR 0018 §14).
//
// It replaces the old `listen | names | match.path` key, which already collided
// between an exact and a prefix location on the same path — a pre-existing
// defect that predicates turn from unlikely into ordinary, since the whole point
// of method and header predicates is that two locations may legitimately share
// a path.
//
// It is a fingerprint rather than an ordinal on purpose. A rate-limit bucket
// carries live state, and an ordinal-keyed bucket would transfer to a different
// predicate set the moment an operator inserted or reordered a same-path route,
// silently handing one route's accumulated limiter state to another. A
// fingerprint is stable across insertion and reordering and changes exactly when
// the route's matching behaviour changes — which is exactly when resetting the
// state is the correct answer.
//
// The digest algorithm itself is private: nothing persists it, exports it as a
// resource name, or correlates it across revisions. A durable external route_id
// is ADR 0019's to define.
func LocationScope(srv config.ServerConfig, loc config.LocationConfig) string {
	names := append([]string(nil), srv.ServerNames...)
	sort.Strings(names)

	h := sha256.New()
	writeScopeField := func(parts ...string) {
		for _, p := range parts {
			// Length-prefixed so no operator-controlled value can impersonate a
			// different tuple by containing the separator.
			fmt.Fprintf(h, "%d:%s\n", len(p), p)
		}
	}
	writeScopeField(srv.Listen)
	writeScopeField(names...)
	writeScopeField(matchTypeOrDefault(loc.Match.Type), loc.Match.Path, loc.Match.CanonicalPredicates())
	if config.LocationPreflightWidening(loc) {
		writeScopeField("preflight_widening")
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// matchTypeOrDefault renders an omitted match type as the documented default, so
// two spellings of the same route produce one scope.
func matchTypeOrDefault(t string) string {
	if t == "" {
		return "prefix"
	}
	return t
}

// AuthScope builds a stable identity for a location's auth policy, used to map a
// pre-built Authenticator back to the location during router construction.
func AuthScope(srv config.ServerConfig, loc config.LocationConfig) string {
	return LocationScope(srv, loc)
}

// WAFScope builds a stable identity for a location's WAF policy, used to map a
// pre-built Firewall back to the location during router construction.
func WAFScope(srv config.ServerConfig, loc config.LocationConfig) string {
	return LocationScope(srv, loc)
}

// EffectiveWAF resolves the WAF policy that applies to a location: its own [waf]
// override when present, otherwise the global [waf] policy. The bool reports
// whether an enabled policy applies (so the caller builds a firewall).
func EffectiveWAF(c *config.Config, loc config.LocationConfig) (config.WAFConfig, bool) {
	if loc.WAF != nil {
		return *loc.WAF, loc.WAF.Enabled
	}
	return c.WAF, c.WAF.Enabled
}

// UniqueListenAddrs returns the distinct listen addresses across server blocks.
func UniqueListenAddrs(servers []config.ServerConfig) []string {
	seen := map[string]struct{}{}
	var addrs []string
	for _, srv := range servers {
		addr := config.CanonicalListenAddr(srv.Listen)
		if addr == "" {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		addrs = append(addrs, addr)
	}
	return addrs
}

// ClientAddressPolicy compiles the trusted-proxy policy that applies to addr.
//
// The policy is listener scoped: configuration validation requires every server
// block sharing a listen address to declare the same effective policy, so the
// first matching block is authoritative. A block without a [client_address]
// policy yields a policy that trusts no proxy, meaning the canonical client is
// always the direct transport peer.
func ClientAddressPolicy(servers []config.ServerConfig, addr string) (*clientaddr.Policy, error) {
	for _, srv := range servers {
		if srv.Listen != addr || srv.ClientAddress == nil {
			continue
		}
		return clientaddr.NewPolicy(srv.ClientAddress.TrustedProxies, srv.ClientAddress.ForwardedHeaders, srv.ClientAddress.MaxHops)
	}
	return nil, nil
}

// AddrServesTLS reports whether any server block on addr enables TLS. It marks
// plain HTTP listeners, where ACME HTTP-01 challenge responses are mounted.
func AddrServesTLS(servers []config.ServerConfig, addr string) bool {
	for _, srv := range servers {
		if srv.Listen == addr && srv.TLS != nil && srv.TLS.Enabled {
			return true
		}
	}
	return false
}

// IndexUpstreams builds a name -> upstream lookup table.
func IndexUpstreams(ups []config.UpstreamConfig) map[string]config.UpstreamConfig {
	m := make(map[string]config.UpstreamConfig, len(ups))
	for _, u := range ups {
		m[u.Name] = u
	}
	return m
}

// MergeReload fans multiple reload sources into one typed channel. Untyped
// notifications are coalesced before enqueue; candidate-bearing admin requests
// are forwarded losslessly and are never removed from the shared output.
//
// fileWatch, when non-nil, carries the SHA-256 digest of the file contents at
// the moment the watcher fired. When a digest matches lastAdminDigest, the
// event is treated as the echo of a recent admin write and is suppressed
// (R10-01).
func MergeReload(ctx context.Context, sigReload <-chan struct{}, fileWatch <-chan [32]byte, adminReload <-chan server.ReloadRequest, lastAdminDigest *atomic.Pointer[[32]byte]) <-chan server.ReloadRequest {
	// Unbuffered handoff gives ownership a precise boundary: a send succeeds only
	// when server.Run has accepted the request. Before that point this fan-in is
	// responsible for completing or rejecting managed requests on cancellation.
	out := make(chan server.ReloadRequest)
	var wg sync.WaitGroup

	forward := func(in <-chan struct{}, source server.ReloadSource) {
		if in == nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			pending := false
			for {
				var send chan server.ReloadRequest
				if pending {
					send = out
				}
				select {
				case <-ctx.Done():
					return
				case send <- server.ReloadRequest{Source: source}:
					pending = false
				case _, ok := <-in:
					if !ok {
						return
					}
					pending = true
				}
			}
		}()
	}

	forward(sigReload, server.ReloadSourceSIGHUP)

	if fileWatch != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pending := false
			for {
				var send chan server.ReloadRequest
				if pending {
					send = out
				}
				select {
				case <-ctx.Done():
					return
				case send <- server.ReloadRequest{Source: server.ReloadSourceFileWatch}:
					pending = false
				case digest, ok := <-fileWatch:
					if !ok {
						return
					}
					// Suppress the one-shot echo of a recent admin write: if
					// the file digest matches the last digest produced by an
					// admin apply, the watcher is just reporting our own save
					// (R10-01, R11-01).
					if lastAdminDigest != nil {
						// Atomically consume the digest so a later legitimate
						// external write with the same bytes is not permanently
						// suppressed (R11-01).
						if last := lastAdminDigest.Swap(nil); last != nil && *last == digest {
							continue
						}
					}
					pending = true
				}
			}
		}()
	}

	if adminReload != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			completeCanceled := func(req server.ReloadRequest) {
				req.PreparedAdmin.Abort()
				if req.Result == nil {
					return
				}
				result := server.ReloadResult{
					ID:          req.ID,
					Source:      req.Source,
					Outcome:     server.ReloadNotApplied,
					Persisted:   req.Candidate != nil,
					FailedPhase: "enqueue",
					Error:       "reload dispatch canceled before server acceptance",
				}
				select {
				case req.Result <- result:
				default:
				}
			}
			drainCanceled := func() {
				for {
					select {
					case pending, ok := <-adminReload:
						if !ok {
							return
						}
						completeCanceled(pending)
					default:
						return
					}
				}
			}
			for {
				select {
				case <-ctx.Done():
					drainCanceled()
					return
				case req, ok := <-adminReload:
					if !ok {
						return
					}
					select {
					case out <- req:
					case <-ctx.Done():
						completeCanceled(req)
						drainCanceled()
						return
					}
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}

// driftOnlySignalConsumer drains sigReload without ever forwarding it into a
// reload fan-in: in managed authority, SIGHUP becomes a drift detector rather
// than a reload trigger (ADR 0019 §11 point 5). Each signal schedules one
// drift re-assessment, coalesced the same way MergeReload coalesces bursts.
// It returns when in is nil, closed, or ctx is done.
func driftOnlySignalConsumer(ctx context.Context, in <-chan struct{}, assessRequests chan<- struct{}) {
	if in == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-in:
			if !ok {
				return
			}
			select {
			case assessRequests <- struct{}{}:
			default:
			}
		}
	}
}

// driftOnlyFileConsumer is driftOnlySignalConsumer's counterpart for the file
// watcher: in managed authority the watcher becomes a drift detector and
// never enqueues a reload (ADR 0019 §11 point 4). It intentionally ignores
// the reported digest — echo suppression is meaningless once nothing is
// forwarded, and this consumer's drift assessment re-reads the file itself.
func driftOnlyFileConsumer(ctx context.Context, in <-chan [32]byte, assessRequests chan<- struct{}) {
	if in == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-in:
			if !ok {
				return
			}
			select {
			case assessRequests <- struct{}{}:
			default:
			}
		}
	}
}

// ValidateRuntimeConfig performs the full runtime preflight for a configuration:
// it clones the config, runs the structural validation, and dry-runs the
// build-tag-gated subsystems (WAF, auth, compression) so an edit that a lean
// build cannot serve — or that a compiled build would reject — fails here,
// before anything is written, keeping admin "apply" truthful.
//
// config.PreflightClone resolves secrets into a deep-copied clone without
// mutating the live redaction registry, so no save/restore dance is needed
// (R6-16).
func ValidateRuntimeConfig(ctx context.Context, c *config.Config) error {
	wafExtra := func(clone *config.Config) error {
		if err := waf.Check(clone); err != nil {
			return err
		}
		if waf.Compiled {
			for i := range clone.Servers {
				for j := range clone.Servers[i].Locations {
					loc := clone.Servers[i].Locations[j]
					wcfg, ok := EffectiveWAF(clone, loc)
					if !ok {
						continue
					}
					if _, err := waf.New(ctx, wcfg, waf.Options{}); err != nil {
						return fmt.Errorf("waf: %w", err)
					}
				}
			}
		}
		authExtra := func(c2 *config.Config) error {
			for i := range c2.Servers {
				for j := range c2.Servers[i].Locations {
					loc := c2.Servers[i].Locations[j]
					if loc.Auth == nil {
						continue
					}
					if _, err := auth.New(ctx, *loc.Auth, auth.Options{}); err != nil {
						return fmt.Errorf("auth: %w", err)
					}
				}
			}
			return nil
		}
		if err := authExtra(clone); err != nil {
			return err
		}
		// Dry-run the compression middleware so a configured encoder that is not
		// compiled into this build (br/zstd behind their tags) fails the
		// preflight here, before the config file is written, instead of only at
		// the asynchronous reload — keeping admin "apply" truthful: a rejected
		// build never reports success. Mirrors the WAF/auth dry-runs above.
		if clone.Compression.IsEnabled() {
			if _, err := middleware.NewCompression(middleware.CompressionOptions{
				Encoders: clone.Compression.Encoders,
				Level:    clone.Compression.Level,
				MinSize:  clone.Compression.MinSize.Bytes(),
				Types:    clone.Compression.Types,
			}); err != nil {
				return fmt.Errorf("compression: %w", err)
			}
		}
		return nil
	}
	if err := config.PreflightClone(c, wafExtra); err != nil {
		return err
	}
	return nil
}
