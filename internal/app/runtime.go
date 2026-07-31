// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"jul/internal/config"
	"jul/internal/observability"
	"jul/internal/server"
	"jul/internal/stream"
	"jul/internal/waf"
)

// RuntimeBuilder constructs the process-lifetime, build-tag-gated subsystems the
// composition root creates once at startup and that outlive every reload: the
// tracing pipeline, the ACME manager, and the L4 stream server. It also runs the
// startup feature-flag checks (HTTP/3, stream, WAF) that fail fast when the
// configuration needs a capability this binary was not built with.
//
// Build opens long-lived resources but performs no teardown; the returned
// *Runtime exposes those resources and a single Close, so the composition root
// keeps ownership of shutdown ordering. Extracting this out of serve() lets the
// startup sequence be unit-tested without a full process boot (ADR-0007, SEQ-04).
type RuntimeBuilder struct {
	Config  *config.Config
	Logger  *slog.Logger
	Metrics *observability.Metrics
	// ACMEClient guards the ACME directory/order/challenge HTTP calls when the
	// egress allow-list is enabled; nil keeps the acme package's default client.
	ACMEClient *http.Client
	// OCSPClient guards the OCSP responder fetch when the egress allow-list is
	// enabled; nil keeps the default client.
	OCSPClient *http.Client
}

// Runtime holds the process-lifetime subsystems built by RuntimeBuilder. It is
// created once per process and must not be copied (LastStreamReload embeds an
// atomic pointer); callers pass it around as *Runtime.
type Runtime struct {
	// Tracer is the process TracerProvider / OTLP pipeline. Its Middleware wraps
	// every listener; Close flushes buffered spans on shutdown. It is a no-op in
	// a build without the "otel" tag.
	Tracer *observability.Tracer

	// ACME is the certificate manager covering the union of acme-enabled
	// domains, or nil when no server block enables ACME.
	ACME server.ACMEManager

	// Stream is the L4 stream proxy server. It persists across reloads and is
	// re-driven from each applied config via the server's OnReloaded hook. In a
	// build without the "stream" tag it is a no-op stub.
	Stream *stream.Server

	// LastStreamReload publishes the outcome of the most recent stream reload for
	// the console Overview: "" means none configured, "ok" means the running set
	// matches the applied config, and "failed: ..." means the last reload was
	// rejected and the previous listeners are still bound.
	LastStreamReload atomic.Pointer[string]

	log *slog.Logger
}

// Build validates the build-tag feature flags and constructs the runtime
// subsystems, applying the initial stream configuration. It fails fast with a
// descriptive error when the configuration enables a capability this binary
// lacks, or when a subsystem fails to initialise, without leaking a
// partially-built runtime: the tracer is shut down (and any started stream
// server closed) before the error is returned.
func (b RuntimeBuilder) Build() (*Runtime, error) {
	cfg := b.Config
	rt := &Runtime{log: b.Logger}

	// Tracing is initialised once at startup (like ACME): the OTLP pipeline and
	// global TracerProvider are fixed for the process, so changing
	// [observability.tracing] takes effect only after a restart. It is a no-op
	// unless enabled and built with the "otel" tag; an enabled block in a binary
	// without that tag is a startup error.
	tracer, err := observability.NewTracer(cfg.Observability.Tracing)
	if err != nil {
		return nil, fmt.Errorf("initialize tracing: %w", err)
	}
	rt.Tracer = tracer

	// From here on a failure must shut the tracer down so a rejected startup
	// leaks no OTLP exporter goroutine (serve() previously relied on a deferred
	// tracer.Shutdown for this).
	fail := func(err error) (*Runtime, error) {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tracer.Shutdown(shutCtx)
		return nil, err
	}

	// Build the ACME manager once from the startup configuration. It covers the
	// union of acme-enabled domains, caches certificates on disk, auto-renews,
	// and answers HTTP-01 challenges. nil means no block enables ACME; an error
	// means ACME is enabled but this binary lacks the "acme" build tag. Enabling
	// ACME after startup requires a restart (the domain set is fixed here).
	acmeMgr, err := server.NewACMEManager(cfg.Servers, b.Metrics.ObserveCertExpiry, b.ACMEClient, b.OCSPClient)
	if err != nil {
		return fail(fmt.Errorf("initialize ACME: %w", err))
	}
	rt.ACME = acmeMgr

	// HTTP/3 support is a build-time choice (the "http3" tag). Fail fast at
	// startup when the configuration enables HTTP/3 but this binary cannot serve
	// it, mirroring the ACME/tracing build-tag checks, instead of silently
	// serving only TCP.
	if err := server.CheckHTTP3(cfg.Servers); err != nil {
		return fail(fmt.Errorf("initialize HTTP/3: %w", err))
	}

	// L4 stream proxying ([[stream]]) is a build-time choice (the "stream" tag).
	// Fail fast when the configuration declares a stream but this binary cannot
	// serve it, mirroring the HTTP/3 check.
	if err := stream.Check(cfg.Streams); err != nil {
		return fail(fmt.Errorf("initialize stream proxy: %w", err))
	}

	// The web application firewall ([waf]) is a build-time choice (the "waf"
	// tag). Fail fast when the configuration enables it but this binary cannot
	// enforce rules, mirroring the stream/HTTP3 checks.
	if err := waf.Check(cfg); err != nil {
		return fail(fmt.Errorf("initialize WAF: %w", err))
	}

	// The L4 stream proxy persists across reloads so its listeners (and any
	// in-flight relayed connections) survive config edits: a reload diffs the
	// desired stream set against the running one. The initial set is applied here
	// before serving; subsequent reloads are driven by the server's OnReloaded
	// hook.
	streamSrv := stream.NewServer(stream.Options{
		Logger: b.Logger,
		Hooks: stream.Hooks{
			OnConnDelta:          b.Metrics.StreamConnDelta,
			OnBytes:              b.Metrics.ObserveStreamBytes,
			OnUDPSessionEvicted:  b.Metrics.StreamUDPEvicted,
			OnUDPSessionRejected: b.Metrics.StreamUDPRejected,
		},
	})
	rt.Stream = streamSrv
	if err := streamSrv.Reload(cfg.Streams, IndexUpstreams(cfg.Upstreams)); err != nil {
		_ = streamSrv.Close()
		return fail(fmt.Errorf("start stream proxy: %w", err))
	}

	initial := ""
	if len(cfg.Streams) > 0 {
		initial = "ok"
	}
	rt.LastStreamReload.Store(&initial)

	return rt, nil
}

// StreamStatus returns the outcome of the most recent stream reload for the
// console Overview, or "" when no stream is configured.
func (r *Runtime) StreamStatus() string {
	if p := r.LastStreamReload.Load(); p != nil {
		return *p
	}
	return ""
}

// Close tears down the runtime's process-lifetime resources: it closes the L4
// stream server (releasing its listeners) and then shuts the tracer down within
// a bounded window, flushing buffered spans. It is intended to be deferred once
// on shutdown.
func (r *Runtime) Close() {
	if r.Stream != nil {
		_ = r.Stream.Close()
	}
	if r.Tracer != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.Tracer.Shutdown(shutCtx); err != nil {
			r.log.Warn("tracing shutdown", "error", err)
		}
	}
}
