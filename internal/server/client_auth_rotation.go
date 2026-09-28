// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/tls"
	"fmt"
	"sync/atomic"
	"time"

	"jul/internal/config"
)

// ComponentClientAuth is the prepared runtime component that swaps a retained
// TLS listener's client-certificate policy (mode, CA pool, CRLs, SAN
// allow-list) at Publish without rebinding (#486).
const ComponentClientAuth RuntimeComponent = ComponentStaticCertificates + 1

// dynamicClientAuth holds a listener's live client-auth bundle. Handshakes read
// it through GetConfigForClient, so a reload swaps it the way
// DynamicCertProvider swaps certificates; a nil bundle means client auth is off.
type dynamicClientAuth struct {
	current atomic.Pointer[clientAuthBundle]
}

func (d *dynamicClientAuth) set(b *clientAuthBundle) { d.current.Store(b) }

// configForClient returns the GetConfigForClient callback for base. Only the
// client-auth fields differ from base, and ticket keys stay base's (crypto/tls
// keeps the original Config's keys for a returned Config without its own).
func (d *dynamicClientAuth) configForClient(base *tls.Config) func(*tls.ClientHelloInfo) (*tls.Config, error) {
	return func(*tls.ClientHelloInfo) (*tls.Config, error) {
		b := d.current.Load()
		if b == nil {
			return nil, nil
		}
		c := base.Clone()
		c.GetConfigForClient = nil
		c.ClientAuth = b.mode
		c.ClientCAs = b.pool
		c.VerifyConnection = b.verifyConn
		return c, nil
	}
}

type clientAuthSwap struct {
	entry  *listenerEntry
	bundle *clientAuthBundle
	newFP  string
}

// clientAuthRotationComponent carries every retained address whose client-auth
// policy changes this reload, so the set commits or aborts together.
type clientAuthRotationComponent struct {
	swaps []clientAuthSwap
}

func (c *clientAuthRotationComponent) component() RuntimeComponent { return ComponentClientAuth }

func (c *clientAuthRotationComponent) commit() retirement {
	for _, sw := range c.swaps {
		sw.entry.clientAuth.set(sw.bundle)
		sw.entry.clientAuthFP = sw.newFP
	}
	return nil
}

func (c *clientAuthRotationComponent) abort() {}

// prepareClientAuthRotation loads and validates the candidate client-auth
// policy of every retained TLS listener whose policy or CA/CRL file content
// changed. A CA or CRL that fails to load or verify aborts the reload, leaving
// the live policy in place. Newly bound addresses read their policy at bind.
func (s *Server) prepareClientAuthRotation(next *config.Config) (*clientAuthRotationComponent, error) {
	var comp clientAuthRotationComponent
	for _, addr := range uniqueListenAddrs(next.Servers) {
		if _, _, tlsOK := tlsBindingsForAddr(next.Servers, addr); !tlsOK {
			continue
		}
		s.mu.Lock()
		entry := s.listeners[addr]
		s.mu.Unlock()
		if entry == nil || entry.clientAuth == nil {
			continue
		}
		newFP := mtlsConfigFingerprint(next.Servers, addr)
		if newFP == entry.clientAuthFP {
			continue
		}
		bundle, err := clientAuthForAddr(next.Servers, addr, s.MTLSResultHook)
		if err != nil {
			return nil, fmt.Errorf("client auth for %s: %w", addr, err)
		}
		comp.swaps = append(comp.swaps, clientAuthSwap{entry: entry, bundle: bundle, newFP: newFP})
	}
	if len(comp.swaps) == 0 {
		return nil, nil
	}
	return &comp, nil
}

// clientAuthUnchanged reports whether every retained TLS listener's live
// client-auth policy matches next, CA and CRL file contents included.
func (s *Server) clientAuthUnchanged(next *config.Config) bool {
	for _, addr := range uniqueListenAddrs(next.Servers) {
		if _, _, tlsOK := tlsBindingsForAddr(next.Servers, addr); !tlsOK {
			continue
		}
		s.mu.Lock()
		entry := s.listeners[addr]
		s.mu.Unlock()
		if entry == nil || entry.clientAuth == nil {
			return false
		}
		if mtlsConfigFingerprint(next.Servers, addr) != entry.clientAuthFP {
			return false
		}
	}
	return true
}

// reportCRLNextUpdates publishes the earliest NextUpdate of the live CRLs of
// each listener through CRLNextUpdateHook (the zero time means none is set) and
// warns about a CRL already past it: Jul keeps enforcing a stale CRL rather
// than failing every handshake, so the operator has to be told.
func (s *Server) reportCRLNextUpdates() {
	out := map[string]time.Time{}
	s.mu.Lock()
	for addr, entry := range s.listeners {
		if entry.clientAuth == nil {
			continue
		}
		b := entry.clientAuth.current.Load()
		if b == nil {
			continue
		}
		for _, c := range b.crls {
			if prev, ok := out[addr]; !ok || (!c.nextUpdate.IsZero() && (prev.IsZero() || c.nextUpdate.Before(prev))) {
				out[addr] = c.nextUpdate
			}
		}
	}
	s.mu.Unlock()
	now := time.Now()
	for addr, next := range out {
		if !next.IsZero() && now.After(next) && s.log != nil {
			s.log.Warn("mtls: client CRL is past its next update; update crl_file and reload", "addr", addr, "next_update", next.UTC().Format(time.RFC3339))
		}
	}
	if s.CRLNextUpdateHook != nil {
		s.CRLNextUpdateHook(out)
	}
}
