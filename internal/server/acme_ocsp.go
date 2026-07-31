// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/ocsp"
)

// OCSP stapling tunables. The refresh window and retry backoff keep a busy
// responder from being hammered while ensuring a staple is renewed well before
// it expires.
const (
	ocspRefreshWindow    = 48 * time.Hour   // refresh this long before NextUpdate
	ocspMinRefresh       = time.Hour        // floor for the next attempt after success
	ocspRetryBackoff     = 5 * time.Minute  // wait this long after a failed fetch
	ocspFetchTimeout     = 10 * time.Second // per-request HTTP timeout
	ocspMaxResponseBytes = 1 << 20          // 1 MiB cap on a responder reply
)

// ocspStapler wraps a CertProvider and attaches a stapled OCSP response to each
// served certificate so clients can verify revocation without a separate
// round-trip to the CA. Staples are fetched lazily and refreshed in the
// background; any failure degrades gracefully — the certificate is served
// unstapled rather than failing the handshake. A cached staple is reused until
// it nears its NextUpdate.
type ocspStapler struct {
	base  CertProvider
	now   func() time.Time
	fetch func(ctx context.Context, reqDER []byte, responder string) ([]byte, error)

	mu    sync.Mutex
	cache map[string]*ocspStaple // keyed by leaf certificate fingerprint
}

// ocspStaple is a cached OCSP response plus the freshness metadata that decides
// when it must be refreshed.
type ocspStaple struct {
	raw         []byte
	nextUpdate  time.Time // response validity end; serve raw only before this
	nextAttempt time.Time // earliest time to refetch
	refreshing  bool
}

// newOCSPStapler wraps base with HTTP OCSP fetching and a real clock. client,
// when non-nil, guards the responder fetch through the egress allow-list; nil
// uses the default client so an egress-disabled build is unchanged.
func newOCSPStapler(base CertProvider, client *http.Client) *ocspStapler {
	return &ocspStapler{
		base:  base,
		now:   time.Now,
		fetch: ocspFetchWith(client),
		cache: make(map[string]*ocspStaple),
	}
}

// GetCertificate returns the base provider's certificate with an OCSP staple
// attached when a fresh one is cached, refreshing in the background otherwise.
// Challenge certificates, certificates without a parsed leaf, without an OCSP
// responder, or without an issuer in the chain are passed through untouched.
func (s *ocspStapler) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert, err := s.base.GetCertificate(hello)
	if err != nil || cert == nil {
		return cert, err
	}
	if isACMEChallenge(hello) || cert.Leaf == nil ||
		len(cert.Leaf.OCSPServer) == 0 || len(cert.Certificate) < 2 {
		return cert, nil
	}
	key := ocspCacheKey(cert.Leaf)
	now := s.now()

	s.mu.Lock()
	st := s.cache[key]
	var raw []byte
	if st != nil && len(st.raw) > 0 && now.Before(st.nextUpdate) {
		raw = st.raw
	}
	if st == nil || (now.After(st.nextAttempt) && !st.refreshing) {
		if st == nil {
			st = &ocspStaple{}
			s.cache[key] = st
		}
		st.refreshing = true
		// cert.Leaf and cert.Certificate are read-only after issuance, so they
		// are safe to hand to the background refresh.
		go s.refresh(key, cert.Leaf, cert.Certificate[1])
	}
	s.mu.Unlock()

	if len(raw) == 0 {
		return cert, nil // nothing fresh cached yet; serve unstapled
	}
	// Return a shallow copy so autocert's shared certificate is never mutated.
	stapled := *cert
	stapled.OCSPStaple = raw
	return &stapled, nil
}

// refresh fetches a fresh OCSP response for leaf and stores it in the cache. It
// is safe to call from a goroutine: the refreshing flag dedups concurrent
// refreshes for the same certificate. On failure it schedules a backoff and
// keeps any previous staple so the next handshake retries.
func (s *ocspStapler) refresh(key string, leaf *x509.Certificate, issuerDER []byte) {
	raw, nextUpdate, err := s.fetchStaple(leaf, issuerDER)
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.cache[key]
	if st == nil {
		st = &ocspStaple{}
		s.cache[key] = st
	}
	st.refreshing = false
	if err != nil {
		st.nextAttempt = now.Add(ocspRetryBackoff)
		return
	}
	st.raw = raw
	st.nextUpdate = nextUpdate
	refreshAt := nextUpdate.Add(-ocspRefreshWindow)
	if !refreshAt.After(now) {
		refreshAt = now.Add(ocspMinRefresh)
	}
	st.nextAttempt = refreshAt
}

// fetchStaple builds an OCSP request for leaf, posts it to leaf's responder, and
// parses the reply. A non-Good status is treated as an error so a revoked or
// unknown certificate is served unstapled (clients then query OCSP directly).
func (s *ocspStapler) fetchStaple(leaf *x509.Certificate, issuerDER []byte) ([]byte, time.Time, error) {
	issuer, err := x509.ParseCertificate(issuerDER)
	if err != nil {
		return nil, time.Time{}, err
	}
	reqDER, err := ocsp.CreateRequest(leaf, issuer, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ocspFetchTimeout)
	defer cancel()
	respDER, err := s.fetch(ctx, reqDER, leaf.OCSPServer[0])
	if err != nil {
		return nil, time.Time{}, err
	}
	resp, err := ocsp.ParseResponse(respDER, issuer)
	if err != nil {
		return nil, time.Time{}, err
	}
	if resp.Status != ocsp.Good {
		return nil, time.Time{}, fmt.Errorf("ocsp: responder reported status %d", resp.Status)
	}
	return respDER, resp.NextUpdate, nil
}

// ocspCacheKey identifies a leaf certificate for staple caching by its SHA-256
// fingerprint, which is stable across the certificate's validity and unique per
// certificate.
func ocspCacheKey(leaf *x509.Certificate) string {
	sum := sha256.Sum256(leaf.Raw)
	return string(sum[:])
}

// ocspFetchWith returns an OCSP fetch function bound to client. A nil client
// uses http.DefaultClient, matching the pre-egress behavior. The returned
// function POSTs a DER-encoded OCSP request to responder and returns the raw DER
// response; tests substitute the ocspStapler.fetch field directly.
func ocspFetchWith(client *http.Client) func(ctx context.Context, reqDER []byte, responder string) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return func(ctx context.Context, reqDER []byte, responder string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, responder, bytes.NewReader(reqDER))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/ocsp-request")
		req.Header.Set("Accept", "application/ocsp-response")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("ocsp: responder returned %s", resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, ocspMaxResponseBytes))
	}
}
