// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

// ─── Header formatting (#161) ───────────────────────────────────────────────

func TestAltSvcHeaderValue(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:443":         `h3=":443"; ma=86400`,
		":8443":               `h3=":8443"; ma=86400`,
		"example.com:443":     `h3=":443"; ma=86400`,
		"127.0.0.1:443":       `h3=":443"; ma=86400`,
		"[::]:443":            `h3=":443"; ma=86400`,
		"[::1]:8443":          `h3=":8443"; ma=86400`,
		"addr-without-a-port": `h3=":443"; ma=86400`, // SplitHostPort fails -> default 443
	}
	for addr, want := range cases {
		if got := altSvcHeaderValue(addr, 86400); got != want {
			t.Errorf("altSvcHeaderValue(%q) = %q, want %q", addr, got, want)
		}
	}
	if got := altSvcHeaderValue(":443", 3600); got != `h3=":443"; ma=3600` {
		t.Errorf("altSvcHeaderValue ma not honored: %q", got)
	}
	if got := altSvcHeaderValue(":443", -1); got != `h3=":443"; ma=0` {
		t.Errorf("altSvcHeaderValue negative maxAge = %q, want ma=0 (clamped, never negative)", got)
	}
}

// TestAltSvcMaxAgeZeroCoercedAtParseTime characterizes #161 scope item 3's
// zero-semantics question: the schema has no way to distinguish an explicit
// alt_svc_max_age = 0 from an omitted one (AltSvcMaxAge is a plain int, not
// *int), so applyHTTP3Defaults coerces both to the 86400 default before any
// hot-reload code ever observes the value. Literal zero-max-age support would
// require a schema change (a pointer field), out of scope here; AltSvcClear
// remains the only way to signal HTTP/3 unavailability.
func TestAltSvcMaxAgeZeroCoercedAtParseTime(t *testing.T) {
	raw := []byte(`
[[servers]]
listen = "127.0.0.1:0"
[servers.tls]
enabled = true
cert = "unused-cert.pem"
key = "unused-key.pem"
[servers.http3]
enabled = true
alt_svc_max_age = 0
[[servers.locations]]
root = "."
[servers.locations.match]
type = "prefix"
path = "/"
`)
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cfg.Servers[0].HTTP3.AltSvcMaxAge; got != 86400 {
		t.Fatalf("alt_svc_max_age = 0 parsed as %d, want the coerced default 86400", got)
	}
}

// ─── DynamicAltSvc state (#161) ─────────────────────────────────────────────

func TestDynamicAltSvcZeroValueIsNone(t *testing.T) {
	var d DynamicAltSvc
	if mode, header := d.Load(); mode != AltSvcNone || header != "" {
		t.Fatalf("zero-value DynamicAltSvc = (%v, %q), want (AltSvcNone, \"\")", mode, header)
	}
	var nilD *DynamicAltSvc
	if mode, _ := nilD.Load(); mode != AltSvcNone {
		t.Fatalf("nil *DynamicAltSvc = %v, want AltSvcNone", mode)
	}
}

func TestDynamicAltSvcSetIsAtomic(t *testing.T) {
	var d DynamicAltSvc
	d.Set(AltSvcAdvertise, `h3=":443"; ma=3600`)
	if mode, header := d.Load(); mode != AltSvcAdvertise || header != `h3=":443"; ma=3600` {
		t.Fatalf("after Set = (%v, %q)", mode, header)
	}
	d.Set(AltSvcClear, "")
	if mode, _ := d.Load(); mode != AltSvcClear {
		t.Fatalf("after second Set = %v, want AltSvcClear", mode)
	}
}

// ─── altSvcMiddleware (#161) ─────────────────────────────────────────────────

func TestAltSvcMiddlewareModes(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	t.Run("none emits nothing", func(t *testing.T) {
		var d DynamicAltSvc // zero value: AltSvcNone
		rec := httptest.NewRecorder()
		altSvcMiddleware(next, &d).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if got := rec.Header().Get("Alt-Svc"); got != "" {
			t.Errorf("Alt-Svc = %q, want empty in AltSvcNone", got)
		}
	})

	t.Run("advertise emits the header", func(t *testing.T) {
		var d DynamicAltSvc
		d.Set(AltSvcAdvertise, `h3=":443"; ma=86400`)
		rec := httptest.NewRecorder()
		altSvcMiddleware(next, &d).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if got := rec.Header().Get("Alt-Svc"); got != `h3=":443"; ma=86400` {
			t.Errorf("Alt-Svc = %q, want the advertised value", got)
		}
	})

	t.Run("clear emits the clear token", func(t *testing.T) {
		var d DynamicAltSvc
		d.Set(AltSvcClear, "")
		rec := httptest.NewRecorder()
		altSvcMiddleware(next, &d).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if got := rec.Header().Get("Alt-Svc"); got != "clear" {
			t.Errorf("Alt-Svc = %q, want \"clear\"", got)
		}
	})

	t.Run("nil state is a passthrough", func(t *testing.T) {
		rec := httptest.NewRecorder()
		altSvcMiddleware(next, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if got := rec.Header().Get("Alt-Svc"); got != "" {
			t.Errorf("Alt-Svc = %q, want empty for a nil state", got)
		}
		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204 (passthrough must not alter the response)", rec.Code)
		}
	})
}

// TestAltSvcMiddlewareOneCoherentStatePerResponse proves each response
// resolves to exactly one mode even while Set is called concurrently: no
// response ever mixes headers from two different states, because the
// middleware loads state exactly once per request. Run with -race.
func TestAltSvcMiddlewareOneCoherentStatePerResponse(t *testing.T) {
	var d DynamicAltSvc
	d.Set(AltSvcAdvertise, `h3=":443"; ma=1`)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := altSvcMiddleware(next, &d)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 1
		for {
			select {
			case <-stop:
				return
			default:
			}
			i++
			if i%2 == 0 {
				d.Set(AltSvcAdvertise, altSvcHeaderValue(":443", i))
			} else {
				d.Set(AltSvcClear, "")
			}
		}
	}()

	for i := 0; i < 200; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		got := rec.Header().Values("Alt-Svc")
		if len(got) > 1 {
			t.Fatalf("response carried %d Alt-Svc headers, want at most 1: %v", len(got), got)
		}
	}
	close(stop)
	wg.Wait()
}

// TestBuildListenerEntryColdRestartDisabledClearsAltSvc characterizes #161's
// cold-restart-disabled decision: a TLS listener whose current config does not
// enable HTTP/3 has no persisted memory of whether a *previous* process
// generation advertised it, so a client could still be holding a cached
// Alt-Svc header. In a binary built with HTTP/3 support, Jul always emits an
// explicit clear for such an address rather than silently omitting the
// header. In a binary without HTTP/3 support, no client of this server could
// ever have received an Alt-Svc header, so no clear is needed and altSvc
// stays nil (matching every other plain TLS listener).
func TestBuildListenerEntryColdRestartDisabledClearsAltSvc(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeSelfSigned(t, dir, "cert", "a.example.com")
	addr := freePort(t)
	cfg := tlsCfgFor(addr, cert, key, "a.example.com") // TLS enabled, HTTP3 nil (disabled)

	s := &Server{cfg: cfg, log: quietLogger(), listeners: map[string]*listenerEntry{}}
	entry, err := s.buildListenerEntry(addr, cfg)
	if err != nil {
		t.Fatalf("buildListenerEntry: %v", err)
	}
	t.Cleanup(func() { _ = entry.ln.Close() })

	if entry.h3 != nil {
		t.Fatal("HTTP3 disabled in config: entry.h3 must stay nil")
	}
	if http3Compiled {
		if entry.altSvc == nil {
			t.Fatal("http3-compiled binary: expected entry.altSvc to be set so a stale client advertisement can be cleared")
		}
		if mode, _ := entry.altSvc.Load(); mode != AltSvcClear {
			t.Fatalf("http3-compiled binary: Alt-Svc mode = %v, want AltSvcClear", mode)
		}
	} else if entry.altSvc != nil {
		t.Fatal("non-http3 binary: entry.altSvc should stay nil, no client could have a cached advertisement")
	}
}

// ─── Live H3 activation/failure integration (#161) ──────────────────────────

// fakeH3Listener is a minimal h3Listener for tests that don't need a real
// QUIC socket: only activation success/failure and the SetOnExit contract.
type fakeH3Listener struct {
	activateErr error
	onExit      func(error)
}

func (f *fakeH3Listener) Activate() error             { return f.activateErr }
func (f *fakeH3Listener) Close(context.Context) error { return nil }
func (f *fakeH3Listener) SetOnExit(fn func(error))    { f.onExit = fn }

func newLoopbackListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestStartServingActivationFailureClearsAltSvc(t *testing.T) {
	s := &Server{listeners: make(map[string]*listenerEntry), serveErr: make(chan error, 1), log: discardLogger()}
	entry := &listenerEntry{
		addr:         ":8443",
		altSvc:       &DynamicAltSvc{},
		altSvcMaxAge: 86400,
		h3:           &fakeH3Listener{activateErr: errors.New("bind failed")},
		httpd:        &http.Server{Addr: ":8443", Handler: http.NotFoundHandler()},
		ln:           newLoopbackListener(t),
	}
	s.startServing(entry)
	t.Cleanup(func() { _ = entry.httpd.Close() })

	if !entry.h3Degraded.Load() {
		t.Fatal("expected h3Degraded after a failed Activate")
	}
	if mode, _ := entry.altSvc.Load(); mode != AltSvcClear {
		t.Fatalf("Alt-Svc mode after failed activation = %v, want AltSvcClear", mode)
	}
}

func TestStartServingActivationSuccessAdvertises(t *testing.T) {
	s := &Server{listeners: make(map[string]*listenerEntry), serveErr: make(chan error, 1), log: discardLogger()}
	entry := &listenerEntry{
		addr:         ":8443",
		altSvc:       &DynamicAltSvc{},
		altSvcMaxAge: 3600,
		h3:           &fakeH3Listener{},
		httpd:        &http.Server{Addr: ":8443", Handler: http.NotFoundHandler()},
		ln:           newLoopbackListener(t),
	}
	s.startServing(entry)
	t.Cleanup(func() { _ = entry.httpd.Close() })

	if entry.h3Degraded.Load() {
		t.Fatal("h3Degraded should not be set after a successful Activate")
	}
	mode, header := entry.altSvc.Load()
	if mode != AltSvcAdvertise {
		t.Fatalf("Alt-Svc mode after successful activation = %v, want AltSvcAdvertise", mode)
	}
	if header != `h3=":8443"; ma=3600` {
		t.Fatalf("Alt-Svc header = %q", header)
	}
}

// TestUpdateAltSvcStateHotReloadsMaxAge proves a retained HTTP/3 listener's
// advertised max-age changes without touching entry.h3/ln (#161's core
// acceptance criterion).
func TestUpdateAltSvcStateHotReloadsMaxAge(t *testing.T) {
	s := &Server{listeners: make(map[string]*listenerEntry)}
	entry := &listenerEntry{addr: ":8443", altSvc: &DynamicAltSvc{}, h3: &fakeH3Listener{}}
	entry.altSvc.Set(AltSvcAdvertise, `h3=":8443"; ma=86400`)
	s.listeners[":8443"] = entry

	next := &config.Config{Servers: []config.ServerConfig{
		{Listen: ":8443", HTTP3: &config.HTTP3Config{Enabled: true, AltSvcMaxAge: 120}},
	}}
	s.updateAltSvcState(next)

	mode, header := entry.altSvc.Load()
	if mode != AltSvcAdvertise || header != `h3=":8443"; ma=120` {
		t.Fatalf("after updateAltSvcState = (%v, %q), want (AltSvcAdvertise, ma=120)", mode, header)
	}
}

// TestUpdateAltSvcStateSkipsDegradedListener proves a listener already marked
// h3Degraded stays cleared regardless of the candidate max-age — #161 does
// not attempt automatic recovery.
func TestUpdateAltSvcStateSkipsDegradedListener(t *testing.T) {
	s := &Server{listeners: make(map[string]*listenerEntry)}
	entry := &listenerEntry{addr: ":8443", altSvc: &DynamicAltSvc{}, h3: &fakeH3Listener{}}
	entry.h3Degraded.Store(true)
	entry.altSvc.Set(AltSvcClear, "")
	s.listeners[":8443"] = entry

	next := &config.Config{Servers: []config.ServerConfig{
		{Listen: ":8443", HTTP3: &config.HTTP3Config{Enabled: true, AltSvcMaxAge: 120}},
	}}
	s.updateAltSvcState(next)

	if mode, _ := entry.altSvc.Load(); mode != AltSvcClear {
		t.Fatalf("degraded listener's Alt-Svc mode = %v, want AltSvcClear (unchanged)", mode)
	}
}

// TestUpdateAltSvcStateSkipsNewOrNonHTTP3Address proves a nil altSvc/h3 entry
// (a plain address, or one newly added this reload) is left alone —
// buildListenerEntry computes its initial state fresh.
func TestUpdateAltSvcStateSkipsNewOrNonHTTP3Address(t *testing.T) {
	s := &Server{listeners: make(map[string]*listenerEntry)}
	plain := &listenerEntry{addr: ":8080"}
	s.listeners[":8080"] = plain

	next := &config.Config{Servers: []config.ServerConfig{{Listen: ":8080"}}}
	s.updateAltSvcState(next) // must not panic on a nil altSvc/h3
}

// TestAltSvcTransitionHookFiresOnEverySetAltSvc proves every production call
// site that changes a listener's Alt-Svc advertisement — activation success,
// activation failure, and an ordinary hot-reload max-age refresh — reports the
// bounded destination state through AltSvcTransitionHook, so the composition
// root can count these events (#161 scope item 8) without this package
// importing observability.
func TestAltSvcTransitionHookFiresOnEverySetAltSvc(t *testing.T) {
	var mu sync.Mutex
	var got []string
	hook := func(to string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, to)
	}
	reset := func() {
		mu.Lock()
		defer mu.Unlock()
		got = nil
	}
	snapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}

	t.Run("activation success", func(t *testing.T) {
		reset()
		s := &Server{listeners: make(map[string]*listenerEntry), serveErr: make(chan error, 1), log: discardLogger(), AltSvcTransitionHook: hook}
		entry := &listenerEntry{
			addr: ":8443", altSvc: &DynamicAltSvc{}, altSvcMaxAge: 3600,
			h3: &fakeH3Listener{}, httpd: &http.Server{Addr: ":8443", Handler: http.NotFoundHandler()}, ln: newLoopbackListener(t),
		}
		s.startServing(entry)
		t.Cleanup(func() { _ = entry.httpd.Close() })
		if want := []string{"advertise"}; !slicesEqual(snapshot(), want) {
			t.Fatalf("hook calls = %v, want %v", snapshot(), want)
		}
	})

	t.Run("activation failure", func(t *testing.T) {
		reset()
		s := &Server{listeners: make(map[string]*listenerEntry), serveErr: make(chan error, 1), log: discardLogger(), AltSvcTransitionHook: hook}
		entry := &listenerEntry{
			addr: ":8443", altSvc: &DynamicAltSvc{}, altSvcMaxAge: 3600,
			h3: &fakeH3Listener{activateErr: errors.New("bind failed")}, httpd: &http.Server{Addr: ":8443", Handler: http.NotFoundHandler()}, ln: newLoopbackListener(t),
		}
		s.startServing(entry)
		t.Cleanup(func() { _ = entry.httpd.Close() })
		if want := []string{"clear"}; !slicesEqual(snapshot(), want) {
			t.Fatalf("hook calls = %v, want %v", snapshot(), want)
		}
	})

	t.Run("hot-reload max-age refresh", func(t *testing.T) {
		reset()
		s := &Server{listeners: make(map[string]*listenerEntry), AltSvcTransitionHook: hook}
		entry := &listenerEntry{addr: ":8443", altSvc: &DynamicAltSvc{}, h3: &fakeH3Listener{}}
		entry.altSvc.Set(AltSvcAdvertise, `h3=":8443"; ma=86400`)
		s.listeners[":8443"] = entry
		next := &config.Config{Servers: []config.ServerConfig{{Listen: ":8443", HTTP3: &config.HTTP3Config{Enabled: true, AltSvcMaxAge: 120}}}}
		s.updateAltSvcState(next)
		if want := []string{"advertise"}; !slicesEqual(snapshot(), want) {
			t.Fatalf("hook calls = %v, want %v", snapshot(), want)
		}
	})
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestBoundListenerInfoReportsAltSvcStatus proves LiveSnapshot's per-listener
// projection carries the bounded H3Degraded/AltSvcMode status fields (#161
// scope item 7), so a future admin/Console consumer can show truthful H3
// health without reaching into server internals.
func TestBoundListenerInfoReportsAltSvcStatus(t *testing.T) {
	entry := &listenerEntry{addr: ":8443", h3: &fakeH3Listener{}, altSvc: &DynamicAltSvc{}}
	entry.altSvc.Set(AltSvcAdvertise, `h3=":8443"; ma=86400`)
	infos := copyListenerMapFromEntries(map[string]*listenerEntry{":8443": entry})
	info := infos[":8443"]
	if !info.H3 || info.H3Degraded || info.AltSvcMode != "advertise" {
		t.Fatalf("info = %+v, want H3=true H3Degraded=false AltSvcMode=advertise", info)
	}

	entry.h3Degraded.Store(true)
	entry.altSvc.Set(AltSvcClear, "")
	infos = copyListenerMapFromEntries(map[string]*listenerEntry{":8443": entry})
	info = infos[":8443"]
	if !info.H3Degraded || info.AltSvcMode != "clear" {
		t.Fatalf("info after degradation = %+v, want H3Degraded=true AltSvcMode=clear", info)
	}
}

// atomicFlag is a tiny race-free bool for tests that only need to observe
// "did this callback fire".
type atomicFlag struct{ v atomic.Bool }

func (f *atomicFlag) set()      { f.v.Store(true) }
func (f *atomicFlag) get() bool { return f.v.Load() }
