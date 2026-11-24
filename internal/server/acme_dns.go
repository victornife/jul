// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"jul/internal/config"
	"jul/internal/egress"
)

const (
	dnsIssuanceTimeout = 20 * time.Minute
	dnsShutdownTimeout = 20 * time.Second
	dnsRetryInitial    = time.Minute
	dnsRetryMax        = time.Hour
	dnsCacheWorkers    = 2
	dnsIssueWorkers    = 4
	dnsAuthWorkers     = 4
	dnsAuthRetry       = time.Second
	dnsCleanupRetries  = 3
	dnsACMEHTTPTimeout = 15 * time.Second
	maxACMEResponse    = 1 << 20
	maxCachedCertBytes = 1 << 20
	maxDNSAuthStates   = 2048
)

type dnsKeyAlgorithm string

const (
	dnsECDSA dnsKeyAlgorithm = "ecdsa-p256"
	dnsRSA   dnsKeyAlgorithm = "rsa-2048"
)

type dnsCertificateVariant struct {
	algorithm       dnsKeyAlgorithm
	cert            *tls.Certificate
	loaded          bool
	inFlight        bool
	deferred        bool
	nextAttempt     time.Time
	failures        int
	lastFailure     string
	cleanupWarnings map[string]bool
}

type dnsCertificateGroup struct {
	names    []string
	variants [2]dnsCertificateVariant
	changed  chan struct{}
}

// dnsACMEManager owns a bounded worker pool; handshakes never initiate orders.
type dnsACMEManager struct {
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	mu          sync.Mutex
	groups      map[string]*dnsCertificateGroup
	wake        chan struct{}
	cacheWake   chan struct{}
	done        chan struct{}
	started     bool
	closed      bool
	cache       autocert.Cache
	client      *acme.Client
	accountMu   sync.Mutex
	registerMu  sync.Mutex
	registered  bool
	authMu      sync.Mutex
	authStates  map[string]*dnsAuthorizationState
	cleanup     chan struct{}
	cleanupJobs chan dnsCleanupTask
	orderMu     sync.Mutex
	orders      map[string]*dnsPendingOrder
	email       string
	provider    dnsChallengeProvider
	onIssue     func(string, time.Time)
	ocsp        atomic.Bool
	ocspClient  *http.Client
	log         *slog.Logger
	now         func() time.Time
	issue       func(context.Context, []string, dnsKeyAlgorithm) (*tls.Certificate, error)
}

func newDNSACMEManager(c *config.ACMEConfig, onIssue func(string, time.Time), acmeClient, ocspClient *http.Client, opts ACMEOptions) (*dnsACMEManager, error) {
	provider, err := newRFC2136Provider(c.RFC2136, opts.DNSDialContext)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	m := &dnsACMEManager{
		ctx: ctx, cancel: cancel, groups: make(map[string]*dnsCertificateGroup),
		authStates: make(map[string]*dnsAuthorizationState), orders: make(map[string]*dnsPendingOrder),
		cleanup: make(chan struct{}, dnsAuthWorkers), cleanupJobs: make(chan dnsCleanupTask, maxDNSAuthStates),
		wake: make(chan struct{}, 1), cacheWake: make(chan struct{}, 1), done: make(chan struct{}),
		cache: dnsDirectoryCache(c.CacheDir),
		client: &acme.Client{
			DirectoryURL: directoryURL(c.CA), HTTPClient: limitedDNSACMEClient(acmeClient),
			RetryBackoff: dnsACMERetryBackoff,
		},
		email: c.Email, provider: provider, onIssue: onIssue, ocspClient: ocspClient,
		log: logger, now: time.Now,
	}
	m.ocsp.Store(c.OCSPStaplingEnabled())
	m.issue = m.issueCertificate
	m.wg.Add(dnsAuthWorkers)
	for range dnsAuthWorkers {
		go m.runDNSCleanup()
	}
	go func() {
		m.wg.Wait()
		close(m.done)
	}()
	return m, nil
}

func limitedDNSACMEClient(original *http.Client) *http.Client {
	client := &http.Client{}
	if original != nil {
		*client = *original
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = &dnsACMELimitedTransport{next: transport, requests: make(chan struct{}, dnsAuthWorkers)}
	return client
}

type dnsACMELimitedTransport struct {
	next     http.RoundTripper
	requests chan struct{}
}

type dnsACMERetryContextKey struct{}

type dnsACMERetryCapture struct {
	retryAt        time.Time
	statusCode     int
	hasRetryAfter  bool
	disableRetries bool
}

func (t *dnsACMELimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	capture, _ := req.Context().Value(dnsACMERetryContextKey{}).(*dnsACMERetryCapture)
	if capture != nil {
		capture.retryAt = time.Time{}
		capture.statusCode = 0
		capture.hasRetryAfter = false
	}
	select {
	case t.requests <- struct{}{}:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	defer func() { <-t.requests }()
	ctx, cancel := context.WithTimeout(req.Context(), dnsACMEHTTPTimeout)
	defer cancel()
	response, err := t.next.RoundTrip(req.Clone(ctx))
	if capture != nil && err == nil && response != nil {
		capture.statusCode = response.StatusCode
	}
	if response != nil && response.Header.Get("Retry-After") != "" {
		delay, valid := dnsAuthorizationPollDelay(response.Header.Get("Retry-After"))
		if capture != nil && valid {
			capture.hasRetryAfter = true
			capture.retryAt = time.Now().Add(delay)
		}
		if response.StatusCode == http.StatusOK {
			// x/crypto's order poller multiplies Retry-After seconds without overflow checks.
			if !valid {
				delay = time.Second
			}
			seconds := int64(delay / time.Second)
			if delay%time.Second != 0 {
				seconds++
			}
			response.Header.Set("Retry-After", strconv.FormatInt(min(seconds, 1<<31-1), 10))
		}
	}
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxACMEResponse+1))
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	if len(body) > maxACMEResponse {
		return nil, errors.New("ACME response exceeds the DNS-01 size limit")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

func (t *dnsACMELimitedTransport) CloseIdleConnections() {
	if transport, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}

func (m *dnsACMEManager) ChallengeHandler(next http.Handler) http.Handler { return next }

func (m *dnsACMEManager) SetOCSPStapling(enabled bool) { m.ocsp.Store(enabled) }

func (m *dnsACMEManager) Provider(domains []string) CertProvider {
	return m.ProviderForListener(domains, domains)
}

func (m *dnsACMEManager) ProviderForListener(domains, serverNames []string) CertProvider {
	names := canonicalDNSNames(domains)
	patterns := canonicalDNSNames(serverNames)
	if len(patterns) == 0 {
		patterns = names
	}
	key := dnsNamesKey(names)
	m.mu.Lock()
	group := m.groups[key]
	if group == nil {
		group = &dnsCertificateGroup{
			names: names, changed: make(chan struct{}),
			variants: [2]dnsCertificateVariant{
				{algorithm: dnsECDSA, nextAttempt: m.now()},
				{algorithm: dnsRSA, nextAttempt: m.now()},
			},
		}
		m.groups[key] = group
	}
	if !m.started && !m.closed && len(names) != 0 {
		m.started = true
		m.wg.Add(dnsCacheWorkers + dnsIssueWorkers)
		for i := 0; i < dnsCacheWorkers; i++ {
			go m.run(true)
		}
		for i := 0; i < dnsIssueWorkers; i++ {
			go m.run(false)
		}
	}
	m.mu.Unlock()
	m.notifyWorker(true)

	base := &dnsACMEProvider{manager: m, group: group, patterns: patterns}
	return &dynamicOCSPProvider{
		enabled: &m.ocsp, base: base, stapler: newOCSPStapler(base, m.ocspClient),
	}
}

func (m *dnsACMEManager) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), dnsShutdownTimeout)
	defer cancel()
	return m.close(ctx)
}

func (m *dnsACMEManager) close(ctx context.Context) error {
	m.Stop()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return errors.New("DNS-01 certificate manager shutdown timed out")
	}
}

func (m *dnsACMEManager) Stop() {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
	}
	m.mu.Unlock()
	m.orderMu.Lock()
	orders := make([]*dnsPendingOrder, 0, len(m.orders))
	for _, order := range m.orders {
		orders = append(orders, order)
	}
	m.orderMu.Unlock()
	for _, order := range orders {
		m.abandonDNSOrder(order)
	}
	for {
		select {
		case job := <-m.cleanupJobs:
			m.unresolvedDNSCleanup(job, "DNS-01 cleanup interrupted by shutdown")
			job.finish()
		default:
			return
		}
	}
}

func (m *dnsACMEManager) notifyWorker(cache bool) {
	wake := m.wake
	if cache {
		wake = m.cacheWake
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (m *dnsACMEManager) run(cache bool) {
	defer m.wg.Done()
	wake := m.wake
	if cache {
		wake = m.cacheWake
	}
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
		now := m.now()
		var dueGroup *dnsCertificateGroup
		var dueVariant *dnsCertificateVariant
		var next time.Time
		for _, group := range m.groups {
			if len(group.names) == 0 {
				continue
			}
			for i := range group.variants {
				variant := &group.variants[i]
				if variant.inFlight || variant.loaded == cache {
					continue
				}
				if !variant.nextAttempt.After(now) {
					if dueVariant == nil || variant.nextAttempt.Before(dueVariant.nextAttempt) {
						dueGroup, dueVariant = group, variant
					}
					continue
				}
				if next.IsZero() || variant.nextAttempt.Before(next) {
					next = variant.nextAttempt
				}
			}
		}
		if dueVariant != nil {
			dueVariant.inFlight = true
			m.mu.Unlock()
			m.notifyWorker(cache)
			if cache {
				m.loadVariant(dueGroup, dueVariant)
			} else {
				m.refresh(dueGroup, dueVariant)
			}
			continue
		}
		m.mu.Unlock()

		if next.IsZero() {
			select {
			case <-m.ctx.Done():
				return
			case <-wake:
			}
			continue
		}
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-m.ctx.Done():
			timer.Stop()
			return
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (m *dnsACMEManager) loadVariant(group *dnsCertificateGroup, variant *dnsCertificateVariant) {
	ctx, cancel := context.WithTimeout(m.ctx, dnsIssuanceTimeout)
	defer cancel()

	if err := m.ensureAccountKey(ctx); err != nil {
		if m.ctx.Err() == nil {
			if ctx.Err() != nil {
				m.finishFailure(group, variant, ctx.Err())
			} else {
				m.finishFailure(group, variant, errCacheFailure)
			}
		}
		return
	}
	cert, err := m.loadCertificate(ctx, group.names, variant.algorithm)
	if ctx.Err() != nil {
		if m.ctx.Err() == nil {
			m.finishFailure(group, variant, ctx.Err())
		}
		return
	}
	switch {
	case errors.Is(err, errInvalidCachedCertificate):
		m.log.Warn("DNS-01 cached certificate is invalid; obtaining a replacement")
	case err != nil:
		if m.ctx.Err() == nil {
			m.finishFailure(group, variant, errCacheFailure)
		}
		return
	case cert != nil && renewAt(cert.Leaf).After(m.now()):
		m.finishSuccess(group, variant, cert)
		m.notifyWorker(false)
		return
	}
	m.mu.Lock()
	variant.cert = cert
	variant.loaded = true
	variant.inFlight = false
	variant.nextAttempt = m.now()
	m.signalGroup(group)
	m.mu.Unlock()
	m.notifyWorker(false)
}

func (m *dnsACMEManager) refresh(group *dnsCertificateGroup, variant *dnsCertificateVariant) {
	ctx, cancel := context.WithTimeout(m.ctx, dnsIssuanceTimeout)
	defer cancel()

	cert, err := m.issue(ctx, group.names, variant.algorithm)
	if errors.Is(err, errDNSAuthorizationBusy) {
		m.mu.Lock()
		variant.inFlight = false
		wasDeferred := variant.deferred
		variant.deferred = true
		var deferred *dnsAuthorizationDeferredError
		if errors.As(err, &deferred) {
			variant.lastFailure = dnsACMEErrorMessage(deferred.cause)
		} else if !wasDeferred {
			variant.lastFailure = ""
		}
		variant.nextAttempt = m.now().Add(dnsAuthRetry)
		m.signalGroup(group)
		m.mu.Unlock()
		m.notifyWorker(false)
		return
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		err = m.storeCertificate(ctx, group.names, variant.algorithm, cert)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			err = errCacheFailure
		}
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) || m.ctx.Err() == nil {
			m.finishFailure(group, variant, err)
		}
		return
	}
	m.finishSuccess(group, variant, cert)
}

func (m *dnsACMEManager) finishSuccess(group *dnsCertificateGroup, variant *dnsCertificateVariant, cert *tls.Certificate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	variant.cert = cert
	variant.loaded = true
	variant.inFlight = false
	variant.deferred = false
	variant.failures = 0
	variant.lastFailure = ""
	variant.nextAttempt = renewAt(cert.Leaf)
	if !variant.nextAttempt.After(m.now()) {
		variant.nextAttempt = m.now().Add(dnsRetryInitial)
	}
	m.signalGroup(group)
}

func (m *dnsACMEManager) finishFailure(group *dnsCertificateGroup, variant *dnsCertificateVariant, err error) {
	message := dnsACMEErrorMessage(err)
	m.mu.Lock()
	variant.inFlight = false
	variant.deferred = false
	variant.failures++
	variant.lastFailure = message
	retry := dnsRetryInitial
	for i := 1; i < variant.failures && retry < dnsRetryMax; i++ {
		retry *= 2
	}
	if retry > dnsRetryMax {
		retry = dnsRetryMax
	}
	if retry < dnsRetryMax {
		retry += time.Duration(mathrand.Int64N(int64(retry/4) + 1))
		if retry > dnsRetryMax {
			retry = dnsRetryMax
		}
	}
	variant.nextAttempt = m.now().Add(retry)
	m.signalGroup(group)
	m.mu.Unlock()
	m.log.Warn("DNS-01 certificate management failed", "algorithm", variant.algorithm, "reason", message, "retry_in", retry)
}

// signalGroup requires m.mu.
func (m *dnsACMEManager) signalGroup(group *dnsCertificateGroup) {
	close(group.changed)
	group.changed = make(chan struct{})
}

type dnsACMEProvider struct {
	manager  *dnsACMEManager
	group    *dnsCertificateGroup
	patterns []string
}

func (p *dnsACMEProvider) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := canonicalDNSName(hello.ServerName)
	if err := p.validateClientHello(hello); err != nil {
		return nil, err
	}
	ctx := hello.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		p.manager.mu.Lock()
		group := p.group
		now := p.manager.now()
		for i := range group.variants {
			variant := &group.variants[i]
			if certificateUsable(variant.cert, now) && hello.SupportsCertificate(variant.cert) == nil {
				cert := variant.cert
				p.manager.mu.Unlock()
				if p.manager.onIssue != nil {
					for _, identity := range group.names {
						if matchesDNSNames([]string{identity}, name) {
							p.manager.onIssue(identity, cert.Leaf.NotAfter)
							break
						}
					}
				}
				return cert, nil
			}
		}
		if p.manager.closed {
			p.manager.mu.Unlock()
			return nil, errors.New("DNS-01 certificate manager is shut down")
		}
		waiting := false
		var failures []string
		for i := range group.variants {
			variant := &group.variants[i]
			if variant.cert != nil {
				if hello.SupportsCertificate(variant.cert) != nil {
					continue
				}
			} else if !clientMaySupportDNSAlgorithm(hello, variant.algorithm) {
				continue
			}
			if variant.lastFailure != "" && !variant.deferred && !variant.inFlight && variant.nextAttempt.After(now) {
				failures = append(failures, string(variant.algorithm)+": "+variant.lastFailure)
			} else {
				waiting = true
			}
		}
		if !waiting {
			p.manager.mu.Unlock()
			if len(failures) != 0 {
				return nil, errors.New(strings.Join(failures, "; "))
			}
			return nil, errors.New("DNS-01 has no certificate compatible with the client")
		}
		changed := group.changed
		p.manager.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.manager.ctx.Done():
			return nil, errors.New("DNS-01 certificate manager is shut down")
		case <-changed:
		}
	}
}

func clientMaySupportDNSAlgorithm(hello *tls.ClientHelloInfo, algorithm dnsKeyAlgorithm) bool {
	if len(hello.SignatureSchemes) == 0 {
		return true
	}
	for _, scheme := range hello.SignatureSchemes {
		switch scheme {
		case tls.ECDSAWithP256AndSHA256, tls.ECDSAWithP384AndSHA384, tls.ECDSAWithP521AndSHA512, tls.ECDSAWithSHA1:
			if algorithm == dnsECDSA {
				return true
			}
		case tls.PSSWithSHA256, tls.PSSWithSHA384, tls.PSSWithSHA512,
			tls.PKCS1WithSHA256, tls.PKCS1WithSHA384, tls.PKCS1WithSHA512, tls.PKCS1WithSHA1:
			if algorithm == dnsRSA {
				return true
			}
		}
	}
	if algorithm == dnsRSA {
		for _, supported := range append(tls.CipherSuites(), tls.InsecureCipherSuites()...) {
			if !strings.HasPrefix(supported.Name, "TLS_RSA_WITH_") {
				continue
			}
			for _, offered := range hello.CipherSuites {
				if offered == supported.ID {
					return true
				}
			}
		}
	}
	return false
}

func (p *dnsACMEProvider) validateClientHello(hello *tls.ClientHelloInfo) error {
	name := canonicalDNSName(hello.ServerName)
	if name == "" || !matchesDNSNames(p.patterns, name) || !matchesDNSNames(p.group.names, name) {
		return errors.New("DNS-01: SNI is not configured for this listener and certificate")
	}
	return nil
}

func canonicalDNSName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

func canonicalDNSNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name = canonicalDNSName(name); name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func dnsNamesKey(names []string) string {
	sum := sha256.Sum256([]byte(strings.Join(names, "\x00")))
	return hex.EncodeToString(sum[:])
}

func matchesDNSNames(patterns []string, name string) bool {
	return (&x509.Certificate{DNSNames: patterns}).VerifyHostname(name) == nil
}

func renewAt(leaf *x509.Certificate) time.Time {
	before := leaf.NotAfter.Sub(leaf.NotBefore) / 3
	if before > 30*24*time.Hour {
		before = 30 * 24 * time.Hour
	}
	return leaf.NotAfter.Add(-before)
}

func certificateUsable(cert *tls.Certificate, now time.Time) bool {
	return cert != nil && cert.Leaf != nil && !now.Before(cert.Leaf.NotBefore) && now.Before(cert.Leaf.NotAfter)
}

var (
	errInvalidCachedCertificate = errors.New("invalid cached DNS-01 certificate")
	errCacheFailure             = errors.New("DNS-01 certificate cache failure")
)

func (m *dnsACMEManager) accountCacheKey() string {
	sum := sha256.Sum256([]byte(m.client.DirectoryURL + "\x00" + m.email))
	return "dns01-account-" + hex.EncodeToString(sum[:])
}

func (m *dnsACMEManager) ensureAccountKey(ctx context.Context) error {
	m.accountMu.Lock()
	defer m.accountMu.Unlock()
	if m.client.Key != nil {
		return nil
	}
	data, err := m.cache.Get(ctx, m.accountCacheKey())
	if errors.Is(err, autocert.ErrCacheMiss) {
		key, keyErr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if keyErr != nil {
			return keyErr
		}
		der, keyErr := x509.MarshalPKCS8PrivateKey(key)
		if keyErr != nil {
			return keyErr
		}
		data = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if err := m.cache.Put(ctx, m.accountCacheKey(), data); err != nil {
			return err
		}
		m.client.Key = key
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) > maxCachedCertBytes {
		return errCacheFailure
	}
	block, rest := pem.Decode(data)
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 || block.Type != "PRIVATE KEY" {
		return errCacheFailure
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	signer, ok := key.(*ecdsa.PrivateKey)
	if !ok || signer.Curve != elliptic.P256() {
		return errCacheFailure
	}
	m.client.Key = signer
	return nil
}

func (m *dnsACMEManager) certificateCacheKey(names []string, algorithm dnsKeyAlgorithm) (string, error) {
	pub, err := x509.MarshalPKIXPublicKey(m.client.Key.Public())
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(m.client.DirectoryURL))
	h.Write([]byte{0})
	h.Write(pub)
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(names, "\x00")))
	h.Write([]byte{0})
	h.Write([]byte(algorithm))
	return "dns01-cert-" + hex.EncodeToString(h.Sum(nil)), nil
}

func (m *dnsACMEManager) loadCertificate(ctx context.Context, names []string, algorithm dnsKeyAlgorithm) (*tls.Certificate, error) {
	key, err := m.certificateCacheKey(names, algorithm)
	if err != nil {
		return nil, err
	}
	data, err := m.cache.Get(ctx, key)
	if errors.Is(err, autocert.ErrCacheMiss) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > maxCachedCertBytes {
		return nil, errInvalidCachedCertificate
	}
	cert, err := tls.X509KeyPair(data, data)
	if err != nil || validateDNSCertificate(&cert, names, algorithm, m.now()) != nil {
		return nil, errInvalidCachedCertificate
	}
	return &cert, nil
}

func (m *dnsACMEManager) storeCertificate(ctx context.Context, names []string, algorithm dnsKeyAlgorithm, cert *tls.Certificate) error {
	if err := validateDNSCertificate(cert, names, algorithm, m.now()); err != nil {
		return err
	}
	key, err := m.certificateCacheKey(names, algorithm)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for _, raw := range cert.Certificate {
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})...)
	}
	return m.cache.Put(ctx, key, data)
}

func validateDNSCertificate(cert *tls.Certificate, names []string, algorithm dnsKeyAlgorithm, now time.Time) error {
	if cert == nil || len(cert.Certificate) == 0 || cert.PrivateKey == nil {
		return errInvalidCachedCertificate
	}
	chain := make([]*x509.Certificate, len(cert.Certificate))
	for i, raw := range cert.Certificate {
		parsed, err := x509.ParseCertificate(raw)
		if err != nil {
			return errInvalidCachedCertificate
		}
		chain[i] = parsed
	}
	leaf := chain[0]
	if leaf.IsCA || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) ||
		!equalDNSNames(canonicalDNSNames(leaf.DNSNames), names) ||
		len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 || len(leaf.URIs) != 0 {
		return errInvalidCachedCertificate
	}
	signer, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		return errInvalidCachedCertificate
	}
	switch public := signer.Public().(type) {
	case *ecdsa.PublicKey:
		if algorithm != dnsECDSA || public.Curve != elliptic.P256() {
			return errInvalidCachedCertificate
		}
	case *rsa.PublicKey:
		if algorithm != dnsRSA || public.N.BitLen() != 2048 {
			return errInvalidCachedCertificate
		}
	default:
		return errInvalidCachedCertificate
	}
	publicKey, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(publicKey, leaf.RawSubjectPublicKeyInfo) {
		return errInvalidCachedCertificate
	}
	if len(leaf.ExtKeyUsage) != 0 {
		usable := false
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
				usable = true
			}
		}
		if !usable {
			return errInvalidCachedCertificate
		}
	}
	for i := 0; i+1 < len(chain); i++ {
		if now.Before(chain[i+1].NotBefore) || !now.Before(chain[i+1].NotAfter) ||
			chain[i].CheckSignatureFrom(chain[i+1]) != nil {
			return errInvalidCachedCertificate
		}
	}
	if cert.Leaf == nil {
		cert.Leaf = leaf
	} else if !bytes.Equal(cert.Leaf.Raw, leaf.Raw) {
		return errInvalidCachedCertificate
	}
	return nil
}

func equalDNSNames(a, b []string) bool {
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

func dnsACMEErrorMessage(err error) string {
	var challengeErr *dnsChallengeError
	var cleanupErr *dnsCleanupError
	var acmeErr *acme.Error
	var responseErr *dnsACMERetryableResponseError
	switch {
	case errors.As(err, &cleanupErr):
		return "DNS-01 cleanup failed; inspect the authoritative DNS server for stale challenge records"
	case errors.Is(err, errDNSUnresolvedCleanup):
		return "DNS-01 validation or cleanup remains unresolved; owned TXT records were retained"
	case errors.As(err, &challengeErr):
		return challengeErr.Error()
	case errors.Is(err, errCacheFailure):
		return "DNS-01 certificate cache failed; check cache permissions and available space"
	case errors.Is(err, errDNSAuthorizationStateLimit):
		return "DNS-01 authorization capacity reached; retry after pending orders finish"
	case errors.Is(err, egress.ErrBlocked):
		return "DNS-01 ACME request blocked by egress policy; allow the CA endpoint"
	case errors.Is(err, context.DeadlineExceeded):
		return "DNS-01 issuance timed out; check ACME and DNS connectivity"
	case errors.Is(err, context.Canceled):
		return "DNS-01 issuance was cancelled"
	case errors.As(err, &acmeErr) && acmeErr.StatusCode == http.StatusTooManyRequests ||
		errors.As(err, &responseErr) && responseErr.statusCode == http.StatusTooManyRequests:
		return "DNS-01 CA rate limit reached; wait before retrying"
	default:
		return "DNS-01 issuance failed; check the CA, DNS configuration and connectivity"
	}
}

func (m *dnsACMEManager) certificateSummary(names, serverNames []string) CertSummary {
	key := dnsNamesKey(canonicalDNSNames(names))
	m.mu.Lock()
	defer m.mu.Unlock()
	group := m.groups[key]
	summary := CertSummary{ServerNames: append([]string(nil), serverNames...), Source: "acme", State: "pending"}
	if group == nil {
		return summary
	}
	now := m.now()
	var primary *tls.Certificate
	var available, failed bool
	var failures []string
	for i := range group.variants {
		variant := &group.variants[i]
		status := CertVariantSummary{Algorithm: string(variant.algorithm), State: "pending", Error: variant.lastFailure}
		if len(variant.cleanupWarnings) != 0 {
			status.CleanupWarning = "DNS-01 cleanup pending or failed; check the logs and recovery guide"
		}
		if variant.cert != nil && variant.cert.Leaf != nil {
			expiry := variant.cert.Leaf.NotAfter
			status.NotAfter = &expiry
			if certificateUsable(variant.cert, now) {
				status.State = "available"
				available = true
				if primary == nil || !certificateUsable(primary, now) {
					primary = variant.cert
				}
			} else {
				status.State = "failed"
				failed = true
				if primary == nil {
					primary = variant.cert
				}
			}
		}
		if variant.lastFailure != "" {
			if !variant.deferred {
				if status.State == "pending" {
					status.State = "failed"
				}
				failed = true
			}
			failures = append(failures, string(variant.algorithm)+": "+variant.lastFailure)
		}
		summary.Variants = append(summary.Variants, status)
	}
	if primary != nil {
		metadata := certSummaryFromCertificate(serverNames, primary)
		summary.Subject, summary.Issuer, summary.DNSNames = metadata.Subject, metadata.Issuer, metadata.DNSNames
		summary.NotBefore, summary.NotAfter = metadata.NotBefore, metadata.NotAfter
	}
	switch {
	case available:
		summary.State = "available"
	case failed:
		summary.State = "failed"
	}
	summary.Error = strings.Join(failures, "; ")
	return summary
}

var _ ACMEManager = (*dnsACMEManager)(nil)
var _ listenerScopedACMEManager = (*dnsACMEManager)(nil)
