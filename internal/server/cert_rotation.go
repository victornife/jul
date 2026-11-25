// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"fmt"

	"jul/internal/config"
)

const (
	// ComponentStaticCertificates is #90's first production RuntimeComponent:
	// the per-retained-address candidate certificate provider built and
	// validated in Prepare, then swapped atomically into the address's
	// existing dynamicCertProvider at Publish (#100). #98 (access-log sinks)
	// adds the second value when it lands.
	ComponentStaticCertificates RuntimeComponent = iota
)

// certProviderSwap pairs a retained listener's entry with the candidate
// provider that will replace its live one, and the identity fingerprint the
// swap advances to.
type certProviderSwap struct {
	entry    *listenerEntry
	provider CertProvider
	newFP    string
}

// certRotationComponent is the preparedComponent for certificate-provider
// rotation. A single instance carries every retained address that needs a
// swap this reload, so the whole candidate set commits or aborts together
// (the "multi-address candidate is prepared completely before any provider
// swap" requirement) without needing one RuntimeComponent slot per address.
type certRotationComponent struct {
	swaps []certProviderSwap
}

func (c *certRotationComponent) component() RuntimeComponent { return ComponentStaticCertificates }

// commit installs every candidate provider live. It cannot fail: every
// fallible step (loading and parsing the cert/key pair) already happened in
// prepareCertRotation. Immutable certificate providers need no active close
// once replaced (#100), so this returns no retirement.
func (c *certRotationComponent) commit() retirement {
	for _, sw := range c.swaps {
		sw.entry.provider.Set(sw.provider)
		sw.entry.certFingerprint = sw.newFP
	}
	return nil
}

// abort releases nothing: candidate providers are plain values never
// installed anywhere, so they are simply left for the garbage collector.
func (c *certRotationComponent) abort() {}

// prepareCertRotation builds and validates a candidate certificate provider
// for every retained, file-backed TLS address whose certificate identity
// (server-name-to-cert-pair mapping, digested by file content) differs from
// what is currently live. It returns nil (not an error) when no retained
// address needs a swap, and a non-nil error when any candidate's cert/key
// pair fails to load — which must abort the whole reload before persistence
// or any live mutation, matching the other Prepare-phase failures ReloadPlan
// already aborts on.
//
// A newly added address is built by buildListenerEntry. DNS-01 providers are
// also replaced when the live listener-name scope changes, without replacing
// the shared manager or its certificate identity.
func (s *Server) prepareCertRotation(next *config.Config) (*certRotationComponent, error) {
	var comp certRotationComponent
	for _, addr := range uniqueListenAddrs(next.Servers) {
		bindings, _, tlsOK := tlsBindingsForAddr(next.Servers, addr)
		if !tlsOK {
			continue
		}
		s.mu.Lock()
		entry := s.listeners[addr]
		s.mu.Unlock()
		if entry == nil || entry.provider == nil {
			continue // newly added or non-TLS address: bind() builds its provider fresh.
		}
		if acmeEnabledForAddr(next.Servers, addr) {
			scoped, ok := s.ACME.(listenerScopedACMEManager)
			if !ok {
				continue
			}
			newFP := acmeListenerFingerprint(next.Servers, addr)
			if newFP != entry.certFingerprint {
				provider := scoped.ProviderForListener(acmeDomainsForAddr(next.Servers, addr), acmeServerNamesForAddr(next.Servers, addr))
				comp.swaps = append(comp.swaps, certProviderSwap{entry: entry, provider: provider, newFP: newFP})
			}
			continue
		}
		newFP := tlsIdentityFingerprint(bindings)
		if newFP == entry.certFingerprint {
			continue // unchanged: nothing to rotate.
		}
		provider, err := newFileCertProvider(bindings)
		if err != nil {
			return nil, fmt.Errorf("certificate for %s: %w", addr, err)
		}
		loadedFP := newFP
		if provider.fp != "" {
			loadedFP = provider.fp
		}
		comp.swaps = append(comp.swaps, certProviderSwap{entry: entry, provider: provider, newFP: loadedFP})
	}
	if len(comp.swaps) == 0 {
		return nil, nil
	}
	return &comp, nil
}
