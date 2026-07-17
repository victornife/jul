// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package server owns the network listeners and HTTP serving lifecycle,
// including hot configuration reload without dropping in-flight connections.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/netutil"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

// ReloadSource identifies which mechanism requested a configuration reload.
type ReloadSource int

const (
	// ReloadSourceSIGHUP is a manual Unix signal reload.
	ReloadSourceSIGHUP ReloadSource = iota
	// ReloadSourceFileWatch is an automatic reload triggered by a change to
	// the on-disk configuration file.
	ReloadSourceFileWatch
	// ReloadSourceAdmin is an explicit reload triggered through the admin API.
	ReloadSourceAdmin
)

// ReloadRequest is a typed reload event. It carries the candidate for admin
// apply paths so the exact preflight-resolved candidate is tied to the event
// that publishes it (R9-02). File-watch and SIGHUP events carry a nil
// Candidate and the live reload falls back to loading from the source.
type ReloadRequest struct {
	Source    ReloadSource
	Candidate *config.Candidate
	// RawDigest, when non-empty, is the SHA-256 of the raw configuration bytes
	// that Candidate represents. It lets the server reject a stale candidate
	// if the on-disk file has changed since the candidate was injected.
	RawDigest [32]byte
}

// HandlerFactory prepares a new handler generation for cfg without committing
// it. The returned genID uniquely identifies this generation for redaction
// retirement. The returned commitFn promotes staged resources (upstream pools,
// closers) to live, captures the generation-scoped pool snapshots from the now-
// live registry, and returns a retire callback for the previous generation. The
// returned abortFn discards staged resources, leaving the live generation
// untouched. The factory does not return a redact.State: the caller (the
// composition root for startup, ReloadPlan for reloads) owns the single
// candidate redaction state and installs it only at the publish boundary.
// Exactly one of commitFn or abortFn must be called; both release any lock held
// during the build so concurrent builds are serialised without holding a lock
// across the bind-attempt window. When commit or abort is nil (e.g. in tests),
// the caller may skip the call safely.
type HandlerFactory func(cfg *config.Config) (handlers map[string]http.Handler, genID uint64, commit func() (snapshots upstream.SnapshotMap, retirePrev func()), abort func(), err error)

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

	// OnReloaded, when set, is invoked after the new HTTP handlers are live
	// (handler swap and listener changes committed). It applies side-effects that
	// must not run on an aborted reload: log-level changes, GOMAXPROCS, and the
	// stream-proxy config update. Errors are reported as a degraded reload result
	// but do not roll back the handler swap.
	OnReloaded func(*config.Config) error

	lastReload atomic.Pointer[lastReloadInfo]

	mu  sync.Mutex
	cfg *config.Config
	// rawCfg is the pre-expansion startup config used for restart-required
	// comparisons in doReload. It is never nil after construction when set
	// from serve.go; nil is accepted for tests that use literal (non-secret)
	// configs and don't need the raw-vs-raw comparison guarantee.
	rawCfg *config.Config
	// startupFP is the effective startup fingerprint captured from the
	// expanded config. Candidate fingerprints are compared against it to decide
	// whether a reload requires a process restart.
	startupFP lifecycle.Fingerprint
	listeners map[string]*listenerEntry // keyed by listen address
	handlers  atomic.Pointer[handlerGen]

	// redactMu guards redactGens and retiredRedaction. A generation is
	// registered when its handlers are published and retired after its
	// in-flight requests drain, so secrets are masked as long as any request
	// of that generation may still emit them (R7-02). Generations that do not
	// drain before the resource grace timeout move to retiredRedaction so the
	// secret union remains masked without blocking shutdown (R9-03).
	// A generation is registered when its handlers are published and retired
	// after its in-flight requests drain, so secrets are masked as long as any
	// request of that generation may still emit them (R7-02).
	redactMu         sync.Mutex
	redactGens       map[uint64]redact.State
	retiredRedaction redact.State // union of grace-expired generations

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

// InjectCandidate is retained for compatibility but is no longer used by the
// typed reload request path (R9-02). Callers should send a ReloadRequest with
// Source == ReloadSourceAdmin and Candidate set.
func (s *Server) InjectCandidate(c *config.Candidate) {
	// No-op: typed ReloadRequest carries the candidate directly.
	_ = c
}

// candidateStillValid reports whether an injected admin candidate still
// matches the configuration currently persisted by the source. When the
// source has diverged from the candidate since preflight, the candidate is
// stale and must not be published (R9-02).
func (s *Server) candidateStillValid(req ReloadRequest) bool {
	if req.Candidate == nil || req.Candidate.Raw == nil || s.source == nil {
		return false
	}
	// If no digest was supplied, accept the candidate (legacy path).
	if req.RawDigest == [32]byte{} {
		return true
	}
	current, err := s.source.Load()
	if err != nil {
		return false
	}
	// Compare canonical TOML bytes so normalization differences (defaults,
	// ordering) do not produce false mismatches.
	currentBytes, err := config.Marshal(current)
	if err != nil {
		return false
	}
	return sha256Digest(currentBytes) == req.RawDigest
}

// handlerGen is one generation of the per-listen-address handler map plus the
// bookkeeping that lets the server close the generation's resources only after
// the requests that may be using them have drained. A reload installs a new
// generation atomically; the generation it replaces is retired once its
// in-flight requests finish (or a grace period elapses), so handler-owned
// resources (gRPC backend connections, WASM plugin runtimes, static-file
// directory handles) are never closed while an old request is still executing.
type handlerGen struct {
	handlers  map[string]http.Handler
	snapshots upstream.SnapshotMap
	genID     uint64

	inflight   atomic.Int64
	retiring   atomic.Bool
	drained    chan struct{}
	drainOnce  sync.Once
	retire     func()
	retireOnce sync.Once
}

func newHandlerGen(handlers map[string]http.Handler, snapshots upstream.SnapshotMap, genID uint64) *handlerGen {
	return &handlerGen{handlers: handlers, snapshots: snapshots, genID: genID, drained: make(chan struct{})}
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
	addr             string
	httpd            *http.Server
	ln               net.Listener
	provider         *dynamicCertProvider // nil for plain HTTP
	h3               h3Listener           // nil unless HTTP/3 is enabled and compiled in
	boundFingerprint string               // listenerBindFingerprint at bind time, for rotation detection
}

// New creates a Server. source and validate may be nil to disable reload.
// rawStartupCfg is the pre-expansion configuration at process startup; when
// non-nil it is used by doReload for restart-required comparison so that
// secret references (${env:...}) do not produce false restart signals. Pass
// nil from tests that use literal configuration values. startupFP is the
// authoritative effective startup fingerprint; when empty it is computed from
// cfg.
func New(cfg *config.Config, rawStartupCfg *config.Config, startupFP lifecycle.Fingerprint, log *slog.Logger, factory HandlerFactory, source config.Source, validate func(*config.Config) error) *Server {
	if len(startupFP.Values) == 0 {
		if expanded, _, _, err := config.Resolve(cfg); err == nil {
			startupFP = lifecycle.ComputeFingerprint(expanded)
		}
	}
	return &Server{
		log:        log,
		source:     source,
		validate:   validate,
		factory:    factory,
		cfg:        cfg,
		rawCfg:     rawStartupCfg,
		startupFP:  startupFP,
		listeners:  map[string]*listenerEntry{},
		redactGens: make(map[uint64]redact.State),
		serveErr:   make(chan error, 8),
	}
}

// Run binds all listeners, serves until ctx is cancelled, reloading on each
// receive from the reload channel, then drains in-flight requests within the
// configured shutdown timeout.
// Run binds all listeners, serves until ctx is cancelled, reloading on each
// receive from the reload channel, then drains in-flight requests within the
// configured shutdown timeout.
//
// initialRedaction is the redaction state for the startup candidate. The
// factory intentionally does not return redaction state (R9-01); it is passed
// separately so the initial generation's secrets remain masked for the
// process lifetime.
func (s *Server) Run(ctx context.Context, reload <-chan ReloadRequest, initialRedaction redact.State) error {
	// The startup effective config is already resolved by the composition root
	// and passed as s.cfg. The factory receives the same candidate that will be
	// served, so there is no second secret resolution at startup (R7-05).
	handlers, genID, commit, _, err := s.factory(s.cfg)
	if err != nil {
		return fmt.Errorf("build handlers: %w", err)
	}
	snapshots, _ := commit() // promote the initial generation; retirePrev is nil at startup
	s.registerRedactionGen(genID, initialRedaction)
	s.handlers.Store(newHandlerGen(handlers, snapshots, genID))

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
		case req := <-reload:
			s.doReload(req)
		}
	}
}

// bind starts serving on addr using the current config (s.cfg) for timeouts and TLS.
func (s *Server) bind(addr string) error { return s.bindFrom(addr, s.cfg) }

// bindFrom builds and immediately starts serving on addr. Used at startup and
// for existing-address operations; for a staged reload, use buildListenerEntry
// followed by startServing after the generation commit.
func (s *Server) bindFrom(addr string, cfg *config.Config) error {
	entry, err := s.buildListenerEntry(addr, cfg)
	if err != nil {
		return err
	}
	s.startServing(entry)
	return nil
}

// buildListenerEntry creates a listenerEntry for addr using cfg for all
// bind-time settings: TLS, mTLS, h2c, HTTP/3, timeouts, header limits, and
// connection cap. The entry is NOT yet registered in s.listeners and httpd.Serve
// is NOT yet started — connections queue in the kernel backlog until startServing
// is called. This separation lets doReload stage binds before committing the
// generation, so no 503 responses are served on a new address during an abort.
func (s *Server) buildListenerEntry(addr string, cfg *config.Config) (*listenerEntry, error) {
	// cv is a configuration view for this bind: a lightweight Server value whose
	// only purpose is to let the config-reading helper methods (readHeaderTimeout,
	// h2cEnabledForAddr, http3EnabledForAddr, etc.) resolve from cfg rather than
	// s.cfg. Hooks and wg/listeners/serveErr are not needed on cv.
	cv := &Server{cfg: cfg, ACME: s.ACME, MTLSResultHook: s.MTLSResultHook, HTTP3ConnHook: s.HTTP3ConnHook}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}

	// Cap concurrent connections per listener before the optional TLS wrap so
	// the limit counts raw accepts and TLS handshakes happen only for admitted
	// connections. Gated by the [rate_limit] master switch; the cap is fixed at
	// bind time, so changing max_conns applies to newly bound listeners.
	if rl := cfg.RateLimit; rl.Enabled && rl.MaxConns > 0 {
		ln = netutil.LimitListener(ln, rl.MaxConns)
	}

	entry := &listenerEntry{addr: addr}

	// altSvc is the Alt-Svc header value advertising HTTP/3; empty unless an
	// HTTP/3 listener is staged below, so HTTP/1.1 + HTTP/2 responses tell
	// clients to upgrade to h3 on a subsequent request.
	var altSvc string

	bindings, minVer, tlsOK := tlsBindingsForAddr(cfg.Servers, addr)
	if tlsOK {
		provider, err := cv.certProviderFor(addr, bindings)
		if err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("tls config for %s: %w", addr, err)
		}
		dyn := &dynamicCertProvider{}
		dyn.set(provider)
		entry.provider = dyn
		tlsConf := &tls.Config{
			GetCertificate: dyn.GetCertificate,
			MinVersion:     minVer,
			NextProtos:     cv.listenerNextProtos(addr),
		}

		// Mutual TLS is bound at listener creation, like MinVersion: the CA
		// bundle, mode, and verifier are read from the config once and apply to
		// connections accepted on this listener. Changing tls.client_auth takes
		// effect on restart, not on hot reload.
		ca, err := clientAuthForAddr(cfg.Servers, addr, s.MTLSResultHook)
		if err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("client auth for %s: %w", addr, err)
		}
		if ca != nil {
			tlsConf.ClientAuth = ca.mode
			tlsConf.ClientCAs = ca.pool
			tlsConf.VerifyPeerCertificate = ca.verify
		}
		ln = tls.NewListener(ln, tlsConf)

		// Stage the parallel HTTP/3 (QUIC) listener on the same UDP address when
		// enabled. Its accept loop is not started here, so QUIC connections do
		// not reach the previous handler generation before Publish either.
		if cv.http3EnabledForAddr(addr) {
			h3, err := newStagedHTTP3(addr, dyn.GetCertificate, s.dynamicHandler(addr), s.HTTP3ConnHook, s.log)
			if err != nil {
				_ = ln.Close()
				return nil, fmt.Errorf("http3 %s: %w", addr, err)
			}
			entry.h3 = h3
			altSvc = altSvcValue(addr, cv.http3MaxAgeForAddr(addr))
		}
	}

	httpd := &http.Server{
		Addr:              addr,
		Handler:           s.handlerForAddr(addr, altSvc),
		ReadHeaderTimeout: cv.readHeaderTimeout(addr),
		ReadTimeout:       cv.readTimeout(addr),
		WriteTimeout:      cv.writeTimeout(addr),
		IdleTimeout:       cv.idleTimeout(addr),
		MaxHeaderBytes:    cv.maxHeaderBytes(addr),
	}
	// On a plaintext listener, optionally accept cleartext HTTP/2 (h2c) so
	// native gRPC clients can connect without TLS. TLS listeners already
	// negotiate HTTP/2 via ALPN, so this only applies when !tlsOK.
	if !tlsOK && cv.h2cEnabledForAddr(addr) {
		enableH2C(httpd)
	}
	if s.ConnStateHook != nil {
		httpd.ConnState = s.ConnStateHook
	}
	entry.httpd = httpd
	entry.ln = ln
	// Capture the bind fingerprint at entry-creation time so that future
	// listenerBoundRebindRequired calls can detect in-place CA/CRL rotation
	// (same path, changed file contents) without re-reading the current file.
	entry.boundFingerprint = listenerBindFingerprint(cfg, addr)
	return entry, nil
}

// startServing registers entry in s.listeners and starts httpd.Serve in a
// goroutine. Call only after the handler generation that covers entry.addr has
// been committed so in-flight connections see the correct handlers immediately.
func (s *Server) startServing(entry *listenerEntry) {
	s.mu.Lock()
	s.listeners[entry.addr] = entry
	s.mu.Unlock()

	s.log.Info("listening", "addr", entry.addr, "tls", entry.provider != nil, "http3", entry.h3 != nil)

	if entry.h3 != nil {
		if err := entry.h3.Activate(); err != nil {
			s.log.Error("http3 activation failed", "addr", entry.addr, "error", err)
			select {
			case s.serveErr <- fmt.Errorf("http3 %s: %w", entry.addr, err):
			default:
			}
			// http3 is optional; continue serving TCP.
		}
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := entry.httpd.Serve(entry.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case s.serveErr <- fmt.Errorf("serve %s: %w", entry.addr, err):
			default:
			}
		}
	}()
}

// listenerBoundRebindRequired reports whether any listener that is kept across
// the reload has bind-time properties that differ between the bound state and
// the candidate config. Unlike ListenerRebindRequired (which compares two config
// objects and may miss in-place file rotation because both sides read the same
// current file), this method uses the fingerprint captured at bind time for the
// old side, so a CA or CRL file rotated in place without changing its configured
// path is correctly detected.
func (s *Server) listenerBoundRebindRequired(next *config.Config) (string, bool) {
	for _, addr := range uniqueListenAddrs(next.Servers) {
		s.mu.Lock()
		entry := s.listeners[addr]
		s.mu.Unlock()
		if entry == nil {
			continue // newly added address: will be bound fresh
		}
		if entry.boundFingerprint != listenerBindFingerprint(next, addr) {
			return fmt.Sprintf(
				"listener %s has bind-time settings (timeouts, header limits, h2c, HTTP/3, TLS, mutual TLS, or connection cap) that changed; these are fixed when the listener binds and take effect on restart",
				addr,
			), true
		}
	}
	return "", false
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
// requests drain, or after the shutdown grace period if they do not.
// retireResources is the factory-supplied closer for that generation and may be
// nil (the initial generation owns nothing a previous one did not).
// retireRedaction removes the generation's secrets from the active redaction
// set. If the generation drains within the grace timeout it is removed from
// the active generation set. If the grace timeout fires first, the generation's
// secrets are moved to retiredRedaction instead, so secret masking outlives
// forced resource closure without blocking process shutdown (R8-14, R9-03).
func (s *Server) retireGen(g *handlerGen, retireResources, retireRedaction, retireToTombstone func()) {
	if g == nil || retireResources == nil {
		return
	}
	g.retire = retireResources
	g.retiring.Store(true)
	if g.inflight.Load() == 0 {
		g.doRetire()
		if retireRedaction != nil {
			retireRedaction()
		}
		return
	}
	if retireToTombstone == nil {
		retireToTombstone = retireRedaction
	}
	grace := s.shutdownTimeout()

	// Close resources when the generation drains or the grace timeout fires,
	// whichever comes first.
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

	// Wait for genuine drain, but do not block shutdown. If the drain completes
	// within grace, retire the redaction normally. If it does not, move the
	// generation's secrets to retiredRedaction so masking continues until the
	// process exits while the shutdown wait group remains bounded (R9-03).
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTimer(grace)
		defer t.Stop()
		select {
		case <-g.drained:
			if retireRedaction != nil {
				retireRedaction()
			}
		case <-t.C:
			s.moveRedactionToRetired(retireToTombstone)
		}
	}()
}

// moveRedactionToRetired transfers a generation's redaction state into the
// permanent retired union. It is used when a generation fails to drain before
// the resource grace timeout, so secrets stay masked without an unbounded
// shutdown waiter.
func (s *Server) moveRedactionToRetired(retireRedaction func()) {
	if retireRedaction == nil {
		return
	}
	// The callback (e.g. retireRedactionForGen) acquires redactMu itself, so
	// do not hold it across the call: that would deadlock.
	retireRedaction()
	s.redactMu.Lock()
	defer s.redactMu.Unlock()
	// Recompute the live union after retiring the generation; any secrets that
	// were unique to it are now captured in retiredRedaction.
	redact.Install(s.redactUnionLocked())
}

// retireRedactionForGen extracts the generation's redaction state and adds it
// to retiredRedaction. It is used as the retireRedaction callback when a
// generation's resource grace timeout fires before the generation drains.
func (s *Server) retireRedactionForGen(genID uint64) {
	s.redactMu.Lock()
	defer s.redactMu.Unlock()
	state, ok := s.redactGens[genID]
	if !ok {
		return
	}
	delete(s.redactGens, genID)
	if s.retiredRedaction.Count() == 0 {
		s.retiredRedaction = state
	} else {
		s.retiredRedaction = s.retiredRedaction.Union(state)
	}
	redact.Install(s.redactUnionLocked())
}

// dynamicHandler returns a handler that dispatches to the currently-installed
// handler for addr, so reload can swap behavior atomically while keeping the
// previous generation's resources alive until its in-flight requests drain.
// It also installs the generation-scoped upstream pool snapshots into the
// request context so every request observes the backend set that was live when
// its generation began (R7-03).
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
		if len(g.snapshots) > 0 {
			r = r.WithContext(upstream.WithSnapshot(r.Context(), g.snapshots))
		}
		h.ServeHTTP(w, r)
	})
}

// registerRedactionGen adds genID's secrets to the active set and atomically
// installs the union of all active generations' redaction states. Mutation,
// union computation, and global installation happen under redactMu so a stale
// asynchronous retirement can never overwrite a newer generation's union.
func (s *Server) registerRedactionGen(genID uint64, state redact.State) {
	s.redactMu.Lock()
	defer s.redactMu.Unlock()
	s.redactGens[genID] = state
	redact.Install(s.redactUnionLocked())
}

// retireRedactionGen removes genID's secrets and atomically installs the union
// of the remaining active generations. It is called once a generation has
// actually drained, not when its resource teardown timeout fires, so secret
// masking outlives forced resource closure.
func (s *Server) retireRedactionGen(genID uint64) {
	s.redactMu.Lock()
	defer s.redactMu.Unlock()
	delete(s.redactGens, genID)
	redact.Install(s.redactUnionLocked())
}

// redactUnionLocked returns the union of all registered generation redaction
// states. Callers must hold s.redactMu.
func sha256Digest(data []byte) [32]byte {
	return sha256.Sum256(data)
}

func (s *Server) redactUnionLocked() redact.State {
	var merged redact.State
	first := true
	for _, state := range s.redactGens {
		if first {
			merged = state
			first = false
		} else {
			merged = merged.Union(state)
		}
	}
	if !first && s.retiredRedaction.Count() > 0 {
		merged = merged.Union(s.retiredRedaction)
	} else if first && s.retiredRedaction.Count() > 0 {
		merged = s.retiredRedaction
	} else if first {
		merged = redact.EmptyState()
	}
	return merged
}

// doReload loads, validates, and applies a new configuration. On any failure it
// logs and keeps the running configuration so a bad edit never causes downtime.
//
// The reload is now driven by a single ReloadPlan value (ADR 0011). Phases:
//  1. Resolve   — expand secrets, compute effective config + redaction + fingerprint.
//  2. Validate  — structural/runtime validation on the raw source config.
//  3. Lifecycle — compare candidate effective fingerprint to startup fingerprint.
//  4. Prepare   — build handlers, stage upstream pools and closers.
//  5. StageListeners — bind new TCP listeners (and HTTP/3) without serving.
//  6. Publish   — commit generation, install redaction, swap configs + handlers.
//  7. Activate  — start serving on staged listeners.
//  8. Retire    — remove listeners no longer in the config.
//  9. Refresh   — reload TLS certificates.
//
// 10. PostCommit — log level, GOMAXPROCS, stream reload.
// On any failure before Publish, plan.Abort() releases candidate resources.
func (s *Server) doReload(req ReloadRequest) {
	if s.source == nil {
		return
	}
	s.log.Info("reloading configuration", "source", s.source.Name())

	var plan *ReloadPlan
	if req.Candidate != nil {
		// Admin apply path: use the exact candidate that passed preflight.
		// If the on-disk file has diverged from the candidate's raw digest,
		// fall back to source load so a stale candidate cannot publish.
		if s.candidateStillValid(req) {
			plan = s.newReloadPlan(req.Candidate.Raw, req.Candidate)
		} else {
			s.log.Warn("reload: injected candidate no longer matches persisted file; falling back to source load", "source", s.source.Name())
		}
	}
	if plan == nil {
		newCfg, err := s.source.Load()
		if err != nil {
			s.log.Error("reload aborted: load failed", "error", err)
			s.lastReload.Store(&lastReloadInfo{OK: false, At: time.Now(), Error: err.Error()})
			return
		}
		plan = s.newReloadPlan(newCfg, nil)
	}
	info := &lastReloadInfo{At: plan.start}

	// Phase 1–5: side-effect-free preparation.
	for _, phase := range []struct {
		name string
		fn   func() error
	}{
		{"resolve", plan.Resolve},
		{"validate", plan.Validate},
		{"lifecycle", plan.Lifecycle},
		{"prepare", plan.Prepare},
		{"stage_listeners", plan.StageListeners},
	} {
		if err := phase.fn(); err != nil {
			plan.Abort()
			info.Duration = time.Since(plan.start)
			info.OK = false
			info.Error = phase.name + ": " + err.Error() + "; reload aborted"
			s.log.Error("reload aborted", "stage", phase.name, "error", err)
			s.lastReload.Store(info)
			return
		}
	}

	// Phase 6: publish — this is the point of no return.
	if _, err := plan.Publish(); err != nil {
		plan.Abort()
		info.Duration = time.Since(plan.start)
		info.OK = false
		info.Error = "publish: " + err.Error()
		s.log.Error("reload aborted", "stage", "publish", "error", err)
		s.lastReload.Store(info)
		return
	}

	// Phase 7–10: activation and post-commit side effects.
	plan.Activate()
	plan.RetireRemovedListeners()
	certErrs := plan.RefreshCerts()
	onReloadErr := plan.PostCommit()

	info.Duration = time.Since(plan.start)
	// Advisory timeout check: warn but do not fail the reload.
	threshold := plan.Candidate.Effective.Global.ReloadTimeout.Std()
	if threshold > 0 && info.Duration > threshold {
		info.TimedOut = true
		s.log.Warn("reload exceeded timeout threshold", "duration", info.Duration, "threshold", threshold)
	}

	switch {
	case len(certErrs) > 0 && onReloadErr != nil:
		info.OK = false
		info.Error = "degraded: certificate refresh failed: " + strings.Join(certErrs, "; ") +
			"; stream reload: " + onReloadErr.Error()
		s.log.Warn("reload completed with errors", "cert_errors", strings.Join(certErrs, "; "),
			"stream_error", onReloadErr)
	case len(certErrs) > 0:
		info.OK = false
		info.Error = "degraded: certificate refresh failed: " + strings.Join(certErrs, "; ") + "; old certificate(s) remain active"
		s.log.Warn("reload completed with certificate errors", "errors", strings.Join(certErrs, "; "))
	case onReloadErr != nil:
		info.OK = false
		info.Error = "degraded: stream reload: " + onReloadErr.Error()
		s.log.Warn("reload completed with stream error", "error", onReloadErr)
	default:
		info.OK = true
		s.log.Info("configuration reloaded", "duration", info.Duration)
	}
	s.lastReload.Store(info)
}

// reloadCertificates is now a no-op: TLS certificate rotation is restart-only
// (R7-07). The dynamicCertProvider is still used by new listeners at bind time,
// but once a listener is bound its provider is frozen for the listener's
// lifetime. Operators must restart the process to pick up new cert/key files.
func (s *Server) reloadCertificates() []string {
	return nil
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
