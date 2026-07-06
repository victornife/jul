// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package server owns the network listeners and HTTP serving lifecycle,
// including hot configuration reload without dropping in-flight connections.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/netutil"

	"jul/internal/config"
)

// HandlerFactory builds the request handler for each unique listen address from
// a configuration. It is supplied by the composition root (main) so routing and
// content-handler construction live outside this package. Returning fresh
// handlers lets reload swap behavior atomically. The second result is a retire
// callback that closes the resources owned by the generation this build
// replaces (gRPC backend connections, WASM plugin runtimes, static-file
// directory handles); the server invokes it only after that previous generation
// has drained, so a reload never closes resources an in-flight request is still
// using. It is nil when there is nothing to retire (for example the first build
// at startup).
type HandlerFactory func(cfg *config.Config) (handlers map[string]http.Handler, retirePrev func(), err error)

// Server runs one http.Server per unique listen address and coordinates
// graceful shutdown and configuration reload.
type Server struct {
	log      *slog.Logger
	source   config.Source              // reload source; nil disables reload
	validate func(*config.Config) error // validation applied before swapping
	factory  HandlerFactory

	// ConnStateHook, when set, is installed as http.Server.ConnState on every
	// listener so the composition root can observe connection lifecycle (for
	// example a metrics gauge) without this package importing observability.
	ConnStateHook func(net.Conn, http.ConnState)

	// ACME, when set, supplies certificates for listeners whose server blocks
	// enable ACME and answers HTTP-01 challenges. It is built and set once by
	// the composition root before Run; nil means static-file TLS only.
	ACME ACMEManager

	// HTTP3ConnHook, when set, is invoked with +1 when an HTTP/3 (QUIC)
	// connection opens and -1 when it closes, so the composition root can track
	// a gauge without this package importing observability. It is only consulted
	// in builds compiled with the http3 tag.
	HTTP3ConnHook func(int64)

	// MTLSResultHook, when set, is invoked once per mutual-TLS handshake that
	// presents a client certificate which passed CA-chain verification, with
	// "verified" or "rejected" (rejected covers a revoked serial or a
	// disallowed SAN). It lets the composition root count handshakes without
	// this package importing observability. Handshakes that fail CA-chain
	// verification are rejected by the stdlib before this hook and are not
	// reported here.
	MTLSResultHook func(result string)

	// OnReloaded, when set, is invoked after the new configuration has validated
	// and handlers have been built, but before the HTTP handler swap and listener
	// diff are finalized. It must be idempotent and must not assume the HTTP
	// generation is already live.
	OnReloaded func(*config.Config)

	lastReload atomic.Pointer[lastReloadInfo]

	mu        sync.Mutex
	cfg       *config.Config
	listeners map[string]*listenerEntry // keyed by listen address
	handlers  atomic.Pointer[handlerGen]

	wg       sync.WaitGroup
	serveErr chan error
}

// lastReloadInfo captures the outcome and timing of the most recent reload.
type lastReloadInfo struct {
	OK       bool
	TimedOut bool
	Duration time.Duration
	At       time.Time
	Error    string
}

// LastReload returns a copy of the most recent reload outcome. It returns nil
// if no reload has been attempted.
func (s *Server) LastReload() *lastReloadInfo {
	if p := s.lastReload.Load(); p != nil {
		cp := *p
		return &cp
	}
	return nil
}

// handlerGen is one generation of the per-listen-address handler map plus the
// bookkeeping that lets the server close the generation's resources only after
// the requests that may be using them have drained. A reload installs a new
// generation atomically; the generation it replaces is retired once its
// in-flight requests finish (or a grace period elapses), so handler-owned
// resources (gRPC backend connections, WASM plugin runtimes, static-file
// directory handles) are never closed while an old request is still executing.
type handlerGen struct {
	handlers map[string]http.Handler

	inflight   atomic.Int64
	retiring   atomic.Bool
	drained    chan struct{}
	drainOnce  sync.Once
	retire     func()
	retireOnce sync.Once
}

func newHandlerGen(handlers map[string]http.Handler) *handlerGen {
	return &handlerGen{handlers: handlers, drained: make(chan struct{})}
}

// release marks an in-flight request against this generation done. The request
// generation has drained so its resources can be closed.
func (g *handlerGen) release() {
	if g.inflight.Add(-1) == 0 && g.retiring.Load() {
		g.drainOnce.Do(func() { close(g.drained) })
	}
}

// doRetire closes the generation's resources exactly once.
func (g *handlerGen) doRetire() {
	g.retireOnce.Do(func() {
		if g.retire != nil {
			g.retire()
		}
	})
}

// listenerEntry tracks a bound listener and its hot-reloadable TLS provider.
type listenerEntry struct {
	addr     string
	httpd    *http.Server
	ln       net.Listener
	provider *dynamicCertProvider // nil for plain HTTP
	h3       h3Listener           // nil unless HTTP/3 is enabled and compiled in
}

// New creates a Server. source and validate may be nil to disable reload.
func New(cfg *config.Config, log *slog.Logger, factory HandlerFactory, source config.Source, validate func(*config.Config) error) *Server {
	return &Server{
		log:       log,
		source:    source,
		validate:  validate,
		factory:   factory,
		cfg:       cfg,
		listeners: map[string]*listenerEntry{},
		serveErr:  make(chan error, 8),
	}
}

// Run binds all listeners, serves until ctx is cancelled, reloading on each
// receive from the reload channel, then drains in-flight requests within the
// configured shutdown timeout.
func (s *Server) Run(ctx context.Context, reload <-chan struct{}) error {
	handlers, _, err := s.factory(s.cfg)
	if err != nil {
		return fmt.Errorf("build handlers: %w", err)
	}
	s.handlers.Store(newHandlerGen(handlers))

	addrs := uniqueListenAddrs(s.cfg.Servers)
	if len(addrs) == 0 {
		return errors.New("no listen addresses configured")
	}
	for _, addr := range addrs {
		if err := s.bind(addr); err != nil {
			s.shutdownAll(context.Background())
			s.wg.Wait()
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			s.log.Info("shutdown signal received, draining connections")
			return s.drain()
		case err := <-s.serveErr:
			s.log.Error("listener failed", "error", err)
			s.shutdownAll(context.Background())
			s.wg.Wait()
			return err
		case <-reload:
			s.doReload()
		}
	}
}

// bind starts serving on addr using the current config (s.cfg) for timeouts and
// TLS.
func (s *Server) bind(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	// Cap concurrent connections per listener before the optional TLS wrap so
	// the limit counts raw accepts and TLS handshakes happen only for admitted
	// connections. Gated by the [rate_limit] master switch; the cap is fixed at
	// bind time, so changing max_conns applies to newly bound listeners.
	if rl := s.cfg.RateLimit; rl.Enabled && rl.MaxConns > 0 {
		ln = netutil.LimitListener(ln, rl.MaxConns)
	}

	entry := &listenerEntry{addr: addr}

	// altSvc is the Alt-Svc header value advertising HTTP/3; empty unless an
	// HTTP/3 listener is started below, so HTTP/1.1 + HTTP/2 responses tell
	// clients to upgrade to h3 on a subsequent request.
	var altSvc string

	bindings, minVer, tlsOK := tlsBindingsForAddr(s.cfg.Servers, addr)
	if tlsOK {
		provider, err := s.certProviderFor(addr, bindings)
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("tls config for %s: %w", addr, err)
		}
		dyn := &dynamicCertProvider{}
		dyn.set(provider)
		entry.provider = dyn
		tlsConf := &tls.Config{
			GetCertificate: dyn.GetCertificate,
			MinVersion:     minVer,
			NextProtos:     s.listenerNextProtos(addr),
		}

		// Mutual TLS is bound at listener creation, like MinVersion: the CA
		// bundle, mode, and verifier are read from the config once and apply to
		// connections accepted on this listener. Changing tls.client_auth takes
		// effect on restart, not on hot reload.
		ca, err := clientAuthForAddr(s.cfg.Servers, addr, s.MTLSResultHook)
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("client auth for %s: %w", addr, err)
		}
		if ca != nil {
			tlsConf.ClientAuth = ca.mode
			tlsConf.ClientCAs = ca.pool
			tlsConf.VerifyPeerCertificate = ca.verify
		}
		ln = tls.NewListener(ln, tlsConf)

		// Start the parallel HTTP/3 (QUIC) listener on the same UDP address when
		// enabled. It shares dyn.GetCertificate, so ACME/static cert reloads via
		// reloadCertificates apply to h3 automatically (the provider is swapped
		// atomically inside dyn, which h3 holds by method value).
		if s.http3EnabledForAddr(addr) {
			h3, err := startHTTP3(addr, dyn.GetCertificate, s.dynamicHandler(addr), s.HTTP3ConnHook, s.log)
			if err != nil {
				_ = ln.Close()
				return fmt.Errorf("http3 %s: %w", addr, err)
			}
			entry.h3 = h3
			altSvc = altSvcValue(addr, s.http3MaxAgeForAddr(addr))
		}
	}

	httpd := &http.Server{
		Addr:              addr,
		Handler:           s.handlerForAddr(addr, altSvc),
		ReadHeaderTimeout: s.readHeaderTimeout(addr),
		ReadTimeout:       s.readTimeout(addr),
		WriteTimeout:      s.writeTimeout(addr),
		IdleTimeout:       s.idleTimeout(addr),
		MaxHeaderBytes:    s.maxHeaderBytes(addr),
	}
	// On a plaintext listener, optionally accept cleartext HTTP/2 (h2c) so
	// native gRPC clients can connect without TLS. TLS listeners already
	// negotiate HTTP/2 via ALPN, so this only applies when !tlsOK.
	if !tlsOK && s.h2cEnabledForAddr(addr) {
		enableH2C(httpd)
	}
	if s.ConnStateHook != nil {
		httpd.ConnState = s.ConnStateHook
	}
	entry.httpd = httpd
	entry.ln = ln

	s.mu.Lock()
	s.listeners[addr] = entry
	s.mu.Unlock()

	s.log.Info("listening", "addr", addr, "tls", tlsOK, "http3", entry.h3 != nil)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := httpd.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case s.serveErr <- fmt.Errorf("serve %s: %w", addr, err):
			default:
			}
		}
	}()
	return nil
}

// acquireGen registers an in-flight request against the current generation and
// returns it. If the generation it loaded is already being retired (its
// resources may be closing), it releases the registration and retries against
// the generation now installed, which is never the retiring one because the
// replacement is stored before a generation is marked retiring. It returns nil
// only when no generation is installed yet.
//
// Correctness of the swap relies on Go's sequentially consistent atomics: a
// request increments inflight before reading retiring, while retireGen stores
// retiring before reading inflight. So if retireGen observes inflight == 0 and
// closes resources, any request whose increment it missed is guaranteed to
// observe retiring == true here and retry on the live generation instead of
// touching the closed resources.
func (s *Server) acquireGen() *handlerGen {
	for {
		g := s.handlers.Load()
		if g == nil {
			return nil
		}
		g.inflight.Add(1)
		if g.retiring.Load() {
			g.release()
			continue
		}
		return g
	}
}

// retireGen closes the resources of a swapped-out generation after its in-flight
// requests drain, or after the shutdown grace period if they do not. retire is
// the factory-supplied closer for that generation and may be nil (the initial
// generation owns nothing a previous one did not).
func (s *Server) retireGen(g *handlerGen, retire func()) {
	if g == nil || retire == nil {
		return
	}
	g.retire = retire
	g.retiring.Store(true)
	if g.inflight.Load() == 0 {
		g.doRetire()
		return
	}
	grace := s.shutdownTimeout()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTimer(grace)
		defer t.Stop()
		select {
		case <-g.drained:
		case <-t.C:
			s.log.Warn("reload: previous handler generation did not drain within grace; closing its resources", "grace", grace)
		}
		g.doRetire()
	}()
}

// dynamicHandler returns a handler that dispatches to the currently-installed
// handler for addr, so reload can swap behavior atomically while keeping the
// previous generation's resources alive until its in-flight requests drain.
func (s *Server) dynamicHandler(addr string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g := s.acquireGen()
		if g == nil {
			http.Error(w, "503 Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		defer g.release()
		h, ok := g.handlers[addr]
		if !ok || h == nil {
			http.Error(w, "503 Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// doReload loads, validates, and applies a new configuration. On any failure it
// logs and keeps the running configuration so a bad edit never causes downtime.
func (s *Server) doReload() {
	if s.source == nil {
		return
	}
	s.log.Info("reloading configuration", "source", s.source.Name())

	newCfg, err := s.source.Load()
	if err != nil {
		s.log.Error("reload aborted: load failed", "error", err)
		s.lastReload.Store(&lastReloadInfo{OK: false, At: time.Now(), Error: err.Error()})
		return
	}

	// Time-bound the reload using the configured timeout. Zero means unbounded.
	start := time.Now()
	info := &lastReloadInfo{At: start}

	// Run validation, factory, and OnReloaded synchronously. A timeout is
	// advisory (duration reporting and warning) rather than a hard cancellation,
	// because the factory is side-effectful and cannot be safely interrupted
	// mid-flight. The preflight already eliminates build and bind failures.
	if s.validate != nil {
		if err := s.validate(newCfg); err != nil {
			info.Duration = time.Since(start)
			info.OK = false
			info.Error = "validate: " + err.Error()
			s.log.Error("reload aborted", "stage", "validate", "error", err)
			s.lastReload.Store(info)
			return
		}
	}
	newHandlers, retirePrev, err := s.factory(newCfg)
	if err != nil {
		info.Duration = time.Since(start)
		info.OK = false
		info.Error = "build: " + err.Error()
		s.log.Error("reload aborted", "stage", "build", "error", err)
		s.lastReload.Store(info)
		return
	}
	// OnReloaded may do network I/O (stream rebind); it is the only potentially
	// long-running step after the build.
	if s.OnReloaded != nil {
		s.OnReloaded(newCfg)
	}
	info.Duration = time.Since(start)

	// Advisory timeout check: if the reload took longer than the configured
	// threshold, record TimedOut=true and warn, but the swap still completes.
	threshold := newCfg.Global.ReloadTimeout.Std()
	if threshold > 0 && info.Duration > threshold {
		info.TimedOut = true
		s.log.Warn("reload exceeded timeout threshold", "duration", info.Duration, "threshold", threshold)
	}

	// Swap request handlers atomically, then retire the previous generation once
	// its in-flight requests drain.
	prevGen := s.handlers.Load()
	s.handlers.Store(newHandlerGen(newHandlers))
	s.retireGen(prevGen, retirePrev)

	oldCfg := s.cfg
	s.cfg = newCfg

	// Tracing / access-log change warnings.
	if oldCfg.Observability.Tracing != newCfg.Observability.Tracing {
		s.log.Warn("tracing settings changed; restart required to apply (running tracer kept)")
	}
	if !accessLogEqual(oldCfg.Observability.AccessLog, newCfg.Observability.AccessLog) {
		s.log.Warn("access-log settings changed; restart required to apply (running sinks kept)")
	}

	// Refresh TLS certificates.
	s.reloadCertificates()

	// Diff listen addresses.
	oldAddrs := setOf(uniqueListenAddrs(oldCfg.Servers))
	newAddrs := setOf(uniqueListenAddrs(newCfg.Servers))

	for addr := range newAddrs {
		if _, existed := oldAddrs[addr]; !existed {
			if err := s.bind(addr); err != nil {
				s.log.Error("reload: failed to bind new listener", "addr", addr, "error", err)
			} else {
				s.log.Info("reload: added listener", "addr", addr)
			}
		}
	}
	for addr := range oldAddrs {
		if _, kept := newAddrs[addr]; !kept {
			s.removeListener(addr)
			s.log.Info("reload: removed listener", "addr", addr)
		}
	}

	info.OK = true
	s.lastReload.Store(info)
	s.log.Info("configuration reloaded", "duration", info.Duration)
}

// reloadCertificates rebuilds and swaps the cert provider for each currently
// TLS-enabled listener that is still TLS-enabled in the new config.
func (s *Server) reloadCertificates() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for addr, entry := range s.listeners {
		if entry.provider == nil {
			continue
		}
		bindings, _, ok := tlsBindingsForAddr(s.cfg.Servers, addr)
		if !ok {
			continue // becomes plain HTTP; handled by listener diff (rebind)
		}
		provider, err := s.certProviderFor(addr, bindings)
		if err != nil {
			s.log.Error("reload: certificate reload failed", "addr", addr, "error", err)
			continue
		}
		entry.provider.set(provider)
		s.log.Info("reload: certificates refreshed", "addr", addr)
	}
}

// removeListener gracefully shuts down and forgets a listener.
func (s *Server) removeListener(addr string) {
	s.mu.Lock()
	entry := s.listeners[addr]
	delete(s.listeners, addr)
	s.mu.Unlock()
	if entry == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout())
	defer cancel()
	if err := entry.httpd.Shutdown(ctx); err != nil {
		_ = entry.httpd.Close()
	}
	if entry.h3 != nil {
		_ = entry.h3.Close(ctx)
	}
}

// drain gracefully shuts down all listeners within the shutdown timeout.
func (s *Server) drain() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout())
	defer cancel()
	s.shutdownAll(ctx)
	s.wg.Wait()
	s.log.Info("shutdown complete")
	return nil
}

func (s *Server) shutdownAll(ctx context.Context) {
	s.mu.Lock()
	entries := make([]*listenerEntry, 0, len(s.listeners))
	for _, e := range s.listeners {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	for _, e := range entries {
		if err := e.httpd.Shutdown(ctx); err != nil {
			s.log.Warn("graceful shutdown timed out; forcing close", "addr", e.addr, "error", err)
			_ = e.httpd.Close()
		}
		if e.h3 != nil {
			_ = e.h3.Close(ctx)
		}
	}
}

func (s *Server) shutdownTimeout() time.Duration {
	if t := s.cfg.Global.ShutdownTimeout.Std(); t > 0 {
		return t
	}
	return 30 * time.Second
}

// uniqueListenAddrs returns the distinct listen addresses across server blocks,
// preserving first-seen order.
func uniqueListenAddrs(servers []config.ServerConfig) []string {
	seen := map[string]struct{}{}
	var addrs []string
	for _, srv := range servers {
		if srv.Listen == "" {
			continue
		}
		if _, ok := seen[srv.Listen]; ok {
			continue
		}
		seen[srv.Listen] = struct{}{}
		addrs = append(addrs, srv.Listen)
	}
	return addrs
}

func setOf(items []string) map[string]struct{} {
	m := make(map[string]struct{}, len(items))
	for _, it := range items {
		m[it] = struct{}{}
	}
	return m
}

// PreflightListeners validates that every listen address introduced by next
// (present in next but not in old) can actually be bound, so an apply that adds
// an unbindable address — a port already in use by another process, an invalid
// host, or a privileged port without permission — fails fast before the config
// is persisted. Without this, doReload binds new listeners best-effort and only
// logs a bind failure, so the apply would already be recorded as successful
// while the new listener silently never serves.
//
// Only NEW addresses are probed. The running server still holds every address it
// already serves, so probing an unchanged address would always fail with
// "address already in use" (a false positive). Each probe binds and immediately
// closes the listener; a narrow TOCTOU window remains before the reload re-binds
// the same address, which is acceptable for a fail-fast check that strictly
// improves on the silent-failure status quo.
func PreflightListeners(old, next []config.ServerConfig) error {
	oldAddrs := setOf(uniqueListenAddrs(old))
	for _, addr := range uniqueListenAddrs(next) {
		if _, existed := oldAddrs[addr]; existed {
			continue
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen address %s: %w", addr, err)
		}
		_ = ln.Close()
	}
	return nil
}

func (s *Server) readHeaderTimeout(addr string) time.Duration {
	if srv := s.serverFor(addr); srv != nil && srv.ReadHeaderTimeout.Std() > 0 {
		return srv.ReadHeaderTimeout.Std()
	}
	return 10 * time.Second
}

func (s *Server) idleTimeout(addr string) time.Duration {
	if srv := s.serverFor(addr); srv != nil && srv.IdleTimeout.Std() > 0 {
		return srv.IdleTimeout.Std()
	}
	return 60 * time.Second
}

// readTimeout and writeTimeout default to 0 (no limit) so that long-lived
// streams (SSE, WebSocket, large transfers) are not severed; slowloris is
// mitigated by ReadHeaderTimeout. They are honored only when configured.
func (s *Server) readTimeout(addr string) time.Duration {
	if srv := s.serverFor(addr); srv != nil {
		return srv.ReadTimeout.Std()
	}
	return 0
}

func (s *Server) writeTimeout(addr string) time.Duration {
	if srv := s.serverFor(addr); srv != nil {
		return srv.WriteTimeout.Std()
	}
	return 0
}

// maxHeaderBytes caps request header size, defaulting to 1 MiB.
func (s *Server) maxHeaderBytes(addr string) int {
	if srv := s.serverFor(addr); srv != nil && srv.MaxHeaderBytes.Bytes() > 0 {
		return int(srv.MaxHeaderBytes.Bytes())
	}
	return 1 << 20
}

// serverFor returns the first server block bound to addr, or nil.
func (s *Server) serverFor(addr string) *config.ServerConfig {
	for i := range s.cfg.Servers {
		if s.cfg.Servers[i].Listen == addr {
			return &s.cfg.Servers[i]
		}
	}
	return nil
}
