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
// handlers lets reload swap behavior atomically.
type HandlerFactory func(cfg *config.Config) (map[string]http.Handler, error)

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

	// OnReloaded, when set, is invoked with the newly applied configuration at
	// the end of every successful reload. The composition root uses it to drive
	// reloads of subsystems that run alongside the HTTP listeners (presently the
	// L4 stream proxy) without this package importing them. It is not called at
	// startup; the composition root applies the initial configuration directly.
	OnReloaded func(*config.Config)

	mu        sync.Mutex
	cfg       *config.Config
	listeners map[string]*listenerEntry // keyed by listen address
	handlers  atomic.Pointer[map[string]http.Handler]

	wg       sync.WaitGroup
	serveErr chan error
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
	handlers, err := s.factory(s.cfg)
	if err != nil {
		return fmt.Errorf("build handlers: %w", err)
	}
	s.handlers.Store(&handlers)

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
		ln = tls.NewListener(ln, &tls.Config{
			GetCertificate: dyn.GetCertificate,
			MinVersion:     minVer,
			NextProtos:     s.listenerNextProtos(addr),
		})

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

// dynamicHandler returns a handler that dispatches to the currently-installed
// handler for addr, so reload can swap behavior atomically.
func (s *Server) dynamicHandler(addr string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := s.handlers.Load()
		if m == nil {
			http.Error(w, "503 Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		h, ok := (*m)[addr]
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
		return
	}
	if s.validate != nil {
		if err := s.validate(newCfg); err != nil {
			s.log.Error("reload aborted: invalid configuration", "error", err)
			return
		}
	}
	newHandlers, err := s.factory(newCfg)
	if err != nil {
		s.log.Error("reload aborted: build handlers failed", "error", err)
		return
	}

	// Swap request handlers atomically; in-flight requests keep the old one.
	s.handlers.Store(&newHandlers)

	oldCfg := s.cfg
	s.cfg = newCfg

	// Tracing is wired once at startup (the OTLP exporter pipeline and the
	// global tracing seam), so a reload keeps the running tracer. Warn when the
	// block changes so the operator knows a restart is needed to apply it.
	if oldCfg.Observability.Tracing != newCfg.Observability.Tracing {
		s.log.Warn("tracing settings changed; restart required to apply (running tracer kept)")
	}

	// Refresh TLS certificates for listeners that remain TLS-enabled.
	s.reloadCertificates()

	// Diff listen addresses: bind added, drain removed, keep unchanged.
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

	// Drive subsystems that run alongside the HTTP listeners (the L4 stream
	// proxy) from the same validated configuration. They manage their own
	// listeners and report binding errors internally, so a stream failure does
	// not roll back the HTTP swap already applied above.
	if s.OnReloaded != nil {
		s.OnReloaded(newCfg)
	}

	s.log.Info("configuration reloaded")
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
		_ = entry.h3.Close()
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
			_ = e.h3.Close()
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
