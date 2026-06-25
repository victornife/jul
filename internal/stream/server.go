//go:build stream

package stream

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

// Compiled reports whether this binary includes L4 stream-proxy support. It is
// true in builds with the "stream" tag.
const Compiled = true

// Server runs the L4 (TCP/UDP) reverse-proxy listeners declared by [[stream]]
// blocks. It is created once by the composition root and persists across
// reloads; Reload diffs the desired listener set against the running one,
// binding added listeners, stopping removed ones, and atomically swapping the
// route of listeners whose address is unchanged so in-flight connections are
// never dropped.
type Server struct {
	log   *slog.Logger
	hooks Hooks

	mu        sync.Mutex
	listeners map[string]*listener // keyed by "proto|addr"
}

// NewServer builds a stream Server. The returned server holds no listeners
// until Reload is called.
func NewServer(opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		log:       log,
		hooks:     opts.Hooks,
		listeners: map[string]*listener{},
	}
}

func (s *Server) connDelta(proto string, d int64) {
	if s.hooks.OnConnDelta != nil {
		s.hooks.OnConnDelta(proto, d)
	}
}

func (s *Server) addBytes(proto, dir string, n int64) {
	if s.hooks.OnBytes != nil && n > 0 {
		s.hooks.OnBytes(proto, dir, n)
	}
}

// route is the immutable forwarding decision for a listener: the default and
// SNI-keyed backend pools plus the per-connection options. A reload publishes a
// new route via the listener's atomic pointer; existing connections keep the
// route they started with.
type route struct {
	proto          string
	defaultPool    *upstream.Pool            // proxy_pass target; may be nil
	sniPools       map[string]*upstream.Pool // SNI host -> pool, incl "*" catch-all
	proxyIn        bool
	proxyOut       bool
	connectTimeout time.Duration
	idleTimeout    time.Duration

	// pools owns every pool referenced above so a replaced route can be torn
	// down. Pool.Close is idempotent.
	pools []*upstream.Pool
}

// listener owns one bound socket and serves it until closed. Its route is
// swapped atomically on reload.
type listener struct {
	server *Server
	key    string
	proto  string
	addr   string
	tcpLn  net.Listener
	udpLn  *net.UDPConn
	route  atomic.Pointer[route]
	wg     sync.WaitGroup

	udpMu       sync.Mutex
	udpSessions map[string]*udpSession
}

// Reload applies the desired stream configuration transactionally: all routes
// and pools are built and all new listeners are bound before any running state
// is mutated, so a failure (bad target, address in use) leaves the currently
// serving configuration untouched.
func (s *Server) Reload(streams []config.StreamServer, upstreams map[string]config.UpstreamConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Phase 1: build every route. On any error, close all pools built so far.
	want := make(map[string]*route, len(streams))
	var built []*route
	for i := range streams {
		st := streams[i]
		proto := normProto(st.Protocol)
		key := proto + "|" + st.Listen
		if _, dup := want[key]; dup {
			closeRoutes(built)
			return fmt.Errorf("stream: duplicate %s listener %q", proto, st.Listen)
		}
		r, err := s.buildRoute(st, upstreams)
		if err != nil {
			closeRoutes(built)
			return err
		}
		built = append(built, r)
		want[key] = r
	}

	// Phase 1b: bind listeners that are newly added. Roll back on error.
	var newlyBound []*listener
	for key, r := range want {
		if _, exists := s.listeners[key]; exists {
			continue
		}
		l, err := s.bindListener(key, r)
		if err != nil {
			for _, nl := range newlyBound {
				nl.shutdown()
			}
			closeRoutes(built)
			return err
		}
		newlyBound = append(newlyBound, l)
	}

	// Phase 2: commit. Swap routes on surviving listeners, start newly bound
	// ones, and stop listeners no longer desired.
	for key, r := range want {
		if l, exists := s.listeners[key]; exists {
			if old := l.route.Swap(r); old != nil {
				closeRoutes([]*route{old})
			}
		}
	}
	for _, l := range newlyBound {
		s.listeners[l.key] = l
		l.start()
		s.log.Info("stream: listening", "proto", l.proto, "addr", l.addr)
	}
	for key, l := range s.listeners {
		if _, kept := want[key]; !kept {
			delete(s.listeners, key)
			l.shutdown()
			s.log.Info("stream: stopped listener", "proto", l.proto, "addr", l.addr)
		}
	}
	return nil
}

// PreflightBuild validates that the desired stream configuration can be built
// without binding any socket or mutating the running listeners. It runs the
// same Phase 1 as Reload — building every route and rejecting duplicate
// listener keys — then closes the throwaway routes. A nil return means a
// subsequent Reload cannot be rejected for configuration reasons; only a socket
// bind can still fail, and Reload performs that under the lock without touching
// the live set until it succeeds. PreflightBuild does not take s.mu because it
// neither reads nor writes the live listener set.
func (s *Server) PreflightBuild(streams []config.StreamServer, upstreams map[string]config.UpstreamConfig) error {
	seen := make(map[string]struct{}, len(streams))
	var built []*route
	for i := range streams {
		st := streams[i]
		proto := normProto(st.Protocol)
		key := proto + "|" + st.Listen
		if _, dup := seen[key]; dup {
			closeRoutes(built)
			return fmt.Errorf("stream: duplicate %s listener %q", proto, st.Listen)
		}
		seen[key] = struct{}{}
		r, err := s.buildRoute(st, upstreams)
		if err != nil {
			closeRoutes(built)
			return err
		}
		built = append(built, r)
	}
	closeRoutes(built)
	return nil
}

// Close stops every listener and releases all sockets.
func (s *Server) Close() error {
	s.mu.Lock()
	ls := s.listeners
	s.listeners = map[string]*listener{}
	s.mu.Unlock()
	for _, l := range ls {
		l.shutdown()
	}
	return nil
}

// buildRoute resolves a stream block into a route, building its backend pools.
func (s *Server) buildRoute(st config.StreamServer, upstreams map[string]config.UpstreamConfig) (*route, error) {
	r := &route{
		proto:          normProto(st.Protocol),
		connectTimeout: st.ConnectTimeout.Std(),
		idleTimeout:    st.IdleTimeout.Std(),
	}
	if r.connectTimeout <= 0 {
		r.connectTimeout = 10 * time.Second
	}
	if r.idleTimeout <= 0 {
		r.idleTimeout = 5 * time.Minute
	}
	switch strings.ToLower(strings.TrimSpace(st.ProxyProtocol)) {
	case "in":
		r.proxyIn = true
	case "out":
		r.proxyOut = true
	case "both":
		r.proxyIn, r.proxyOut = true, true
	}

	if strings.TrimSpace(st.ProxyPass) != "" {
		p, err := buildPool(st.ProxyPass, upstreams)
		if err != nil {
			return nil, err
		}
		r.defaultPool = p
		r.pools = append(r.pools, p)
	}
	if len(st.SNIRoutes) > 0 {
		r.sniPools = make(map[string]*upstream.Pool, len(st.SNIRoutes))
		for host, target := range st.SNIRoutes {
			p, err := buildPool(target, upstreams)
			if err != nil {
				closeRoutes([]*route{r})
				return nil, err
			}
			r.sniPools[strings.ToLower(host)] = p
			r.pools = append(r.pools, p)
		}
	}
	return r, nil
}

// bindListener opens the socket for a route without starting its accept loop.
func (s *Server) bindListener(key string, r *route) (*listener, error) {
	proto, addr, _ := strings.Cut(key, "|")
	l := &listener{
		server:      s,
		key:         key,
		proto:       proto,
		addr:        addr,
		udpSessions: map[string]*udpSession{},
	}
	l.route.Store(r)
	if proto == "udp" {
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			return nil, fmt.Errorf("stream: listen udp %s: %w", addr, err)
		}
		l.udpLn = pc.(*net.UDPConn)
	} else {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("stream: listen tcp %s: %w", addr, err)
		}
		l.tcpLn = ln
	}
	return l, nil
}

// start launches the listener's accept/serve goroutine.
func (l *listener) start() {
	l.wg.Add(1)
	if l.proto == "udp" {
		go func() { defer l.wg.Done(); l.serveUDP() }()
	} else {
		go func() { defer l.wg.Done(); l.serveTCP() }()
	}
}

// shutdown closes the socket, tears down sessions, and waits for goroutines.
func (l *listener) shutdown() {
	if l.tcpLn != nil {
		_ = l.tcpLn.Close()
	}
	if l.udpLn != nil {
		_ = l.udpLn.Close()
	}
	l.udpMu.Lock()
	for _, sess := range l.udpSessions {
		_ = sess.backend.Close()
	}
	l.udpMu.Unlock()
	l.wg.Wait()
	if r := l.route.Load(); r != nil {
		closeRoutes([]*route{r})
	}
}

// dialBackend picks an available backend and dials it, retrying other backends
// on dial failure. The returned backend must be released to the pool when the
// connection/session ends.
func (l *listener) dialBackend(pool *upstream.Pool, network string, timeout time.Duration) (net.Conn, *upstream.Backend, error) {
	attempts := len(pool.Backends())
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		b, err := pool.Pick()
		if err != nil {
			return nil, nil, err
		}
		conn, derr := net.DialTimeout(network, b.Address, timeout)
		if derr != nil {
			pool.MarkFailure(b)
			pool.Release(b)
			lastErr = derr
			continue
		}
		pool.MarkSuccess(b)
		return conn, b, nil
	}
	if lastErr == nil {
		lastErr = errors.New("stream: no backend available")
	}
	return nil, nil, lastErr
}

// buildPool resolves a target (named upstream or literal host:port) to a pool.
func buildPool(target string, upstreams map[string]config.UpstreamConfig) (*upstream.Pool, error) {
	if up, ok := upstreams[target]; ok {
		return upstream.NewPool(up, "tcp")
	}
	return upstream.NewPool(config.UpstreamConfig{
		Name:        "_stream:" + target,
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: target, Weight: 1}},
		MaxFails:    1,
		FailTimeout: config.Duration(10 * time.Second),
	}, "tcp")
}

func closeRoutes(routes []*route) {
	for _, r := range routes {
		if r == nil {
			continue
		}
		for _, p := range r.pools {
			if p != nil {
				p.Close()
			}
		}
	}
}

func normProto(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return "tcp"
	}
	return p
}

func isClosedConn(err error) bool {
	return errors.Is(err, net.ErrClosed)
}
