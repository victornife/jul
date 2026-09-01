// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"fmt"
	"net"
	"net/http"
	"sync/atomic"

	"jul/internal/config"
)

// AltSvcMode is the closed set of Alt-Svc advertisement states for one TCP/TLS
// listener (#161). Exactly one governs any given response.
type AltSvcMode uint8

const (
	// AltSvcNone emits no Alt-Svc header: this address has never had HTTP/3
	// configured, or is not TLS.
	AltSvcNone AltSvcMode = iota
	// AltSvcAdvertise emits h3=":port"; ma=<max-age>: HTTP/3 is configured on
	// this address and its listener has been successfully activated.
	AltSvcAdvertise
	// AltSvcClear emits "Alt-Svc: clear": HTTP/3 was previously advertised (or
	// failed to activate) and clients must retire any cached alternative.
	AltSvcClear
)

// altSvcState is the immutable value one listener's DynamicAltSvc publishes.
// header is precomputed so the request path never assembles a string.
type altSvcState struct {
	mode   AltSvcMode
	header string
}

// DynamicAltSvc is a per-listener atomic holder for the listener's current
// Alt-Svc advertisement state (#161). One instance is created when the
// listener binds and lives for the listener's lifetime; Set installs a new
// state atomically so a hot alt_svc_max_age change, or a live HTTP/3
// activation/failure transition, never rebinds the TCP or UDP listener. The
// zero value (including a nil *DynamicAltSvc) reports AltSvcNone, matching
// "no advertisement until HTTP/3 has been successfully activated".
type DynamicAltSvc struct {
	current atomic.Pointer[altSvcState]
}

// Set installs mode/header as the state the next response observes.
func (d *DynamicAltSvc) Set(mode AltSvcMode, header string) {
	d.current.Store(&altSvcState{mode: mode, header: header})
}

// Load returns the currently active mode and header value.
func (d *DynamicAltSvc) Load() (AltSvcMode, string) {
	if d == nil {
		return AltSvcNone, ""
	}
	s := d.current.Load()
	if s == nil {
		return AltSvcNone, ""
	}
	return s.mode, s.header
}

// altSvcHeaderValue builds the Alt-Svc header value advertising HTTP/3 on the
// same port as addr, e.g. h3=":443"; ma=86400. The host part of addr is
// dropped: Alt-Svc advertises a port on the same authority, never a wildcard
// host. Both bracketed IPv6 (e.g. "[::]:443") and unbracketed IPv4/hostname
// forms parse correctly through net.SplitHostPort; an address with no
// explicit port (or one that fails to parse, e.g. a bare "0.0.0.0") falls back
// to 443, the HTTPS default.
func altSvcHeaderValue(addr string, maxAge int) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "443"
	}
	if maxAge < 0 {
		maxAge = 0
	}
	return fmt.Sprintf(`h3=":%s"; ma=%d`, port, maxAge)
}

// altSvcMiddleware wraps next so every response's Alt-Svc header reflects
// whichever state is current when headers are about to be committed — never
// a value fixed at listener-bind time. It sets the header before the handler
// runs so it survives even when the handler writes its own headers/body, and
// emits nothing at all in AltSvcNone (the common case: no HTTP/3, or a
// plaintext listener).
func altSvcMiddleware(next http.Handler, state *DynamicAltSvc) http.Handler {
	if state == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode, header := state.Load(); mode {
		case AltSvcAdvertise:
			w.Header().Set("Alt-Svc", header)
		case AltSvcClear:
			w.Header().Set("Alt-Svc", "clear")
		}
		next.ServeHTTP(w, r)
	})
}

// setAltSvc installs mode/header on state and, if AltSvcTransitionHook is set,
// reports the bounded destination state to it. Every production call site that
// changes a listener's Alt-Svc advertisement goes through this one method, so
// the hook cannot be forgotten on a future call site (#161).
func (s *Server) setAltSvc(state *DynamicAltSvc, mode AltSvcMode, header string) {
	state.Set(mode, header)
	if s.AltSvcTransitionHook != nil {
		s.AltSvcTransitionHook(altSvcModeString(mode))
	}
}

// altSvcModeString renders mode as the bounded, secret-free status string
// ("none", "advertise", "clear") used by BoundListenerInfo.AltSvcMode (#161).
func altSvcModeString(mode AltSvcMode) string {
	switch mode {
	case AltSvcAdvertise:
		return "advertise"
	case AltSvcClear:
		return "clear"
	default:
		return "none"
	}
}

// updateAltSvcState refreshes every retained listener's Alt-Svc advertisement
// to match next's alt_svc_max_age, without touching the TCP or UDP listener
// (#161). Unlike certificate rotation, building an Alt-Svc header cannot
// fail, so there is no Prepare/Abort phase: this runs directly at Publish,
// exactly like the admin auth snapshot and cache policy updates do.
//
// A newly added address is out of scope: buildListenerEntry computes its
// initial state (still AltSvcNone until Activate succeeds). A degraded
// listener (h3Degraded) is left cleared regardless of the candidate max-age —
// this issue does not attempt automatic recovery of a failed HTTP/3 listener.
// servers.*.http3.enabled stays restart-required (#102), so a retained
// address here never actually transitions HTTP/3 on/off; only its max-age can
// change.
func (s *Server) updateAltSvcState(next *config.Config) {
	cv := &Server{cfg: next}
	for _, addr := range uniqueListenAddrs(next.Servers) {
		s.mu.Lock()
		entry := s.listeners[addr]
		s.mu.Unlock()
		if entry == nil || entry.altSvc == nil || entry.h3 == nil {
			continue
		}
		if entry.h3Degraded.Load() {
			continue
		}
		s.setAltSvc(entry.altSvc, AltSvcAdvertise, altSvcHeaderValue(addr, cv.http3MaxAgeForAddr(addr)))
	}
}
