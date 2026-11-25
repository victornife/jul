// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	mathrand "math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme"

	"jul/internal/egress"
)

type dnsCleanupError struct{ cause error }

func (e *dnsCleanupError) Error() string { return "DNS-01 challenge cleanup failed" }
func (e *dnsCleanupError) Unwrap() error { return e.cause }

type dnsACMERetryableResponseError struct {
	statusCode int
	cause      error
}

func (*dnsACMERetryableResponseError) Error() string {
	return "DNS-01 ACME retryable response could not be processed"
}
func (e *dnsACMERetryableResponseError) Unwrap() error { return e.cause }

var (
	errDNSAuthorizationBusy       = errors.New("DNS-01 authorization is already being processed")
	errDNSAuthorizationStateLimit = errors.New("DNS-01 authorization state limit exceeded")
	errDNSChallengeRetryDeferred  = errors.New("DNS-01 challenge retry deferred")
	errDNSUnresolvedCleanup       = errors.New("DNS-01 validation or cleanup remains unresolved")
)

const (
	maxDNSRetryDelay   = time.Duration(1<<63 - 1)
	maxDNSFailureCount = 1<<30 - 1
)

type dnsAuthorizationDeferredError struct{ cause error }

func (*dnsAuthorizationDeferredError) Error() string {
	return "DNS-01 authorization discovery deferred"
}
func (e *dnsAuthorizationDeferredError) Unwrap() []error {
	return []error{errDNSAuthorizationBusy, e.cause}
}

type dnsPendingOrder struct {
	order          *acme.Order
	deadline       time.Time
	reconcileUntil time.Time
	authorizations map[string]time.Time
	groupKey       string
	algorithm      dnsKeyAlgorithm
}

type dnsValidAuthorization struct {
	name  string
	until time.Time
}

type dnsAuthorizationState struct {
	locked         bool
	nextPoll       time.Time
	nextAccept     time.Time
	pollFailures   int
	acceptFailures int
	valid          dnsValidAuthorization
	lease          dnsChallengeLease
	challengeURI   string
	challengeToken string
	submitted      bool
}

func (m *dnsACMEManager) issueCertificate(ctx context.Context, names []string, algorithm dnsKeyAlgorithm) (result *tls.Certificate, resultErr error) {
	if err := m.ensureRegistered(ctx); err != nil {
		return nil, err
	}

	orderKey := dnsNamesKey(names) + ":" + string(algorithm)
	m.orderMu.Lock()
	pending := m.orders[orderKey]
	m.orderMu.Unlock()
	if pending == nil {
		order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs(names...))
		if err != nil {
			return nil, err
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			deadline = time.Now().Add(dnsIssuanceTimeout)
		}
		pending = &dnsPendingOrder{order: order, deadline: deadline, groupKey: dnsNamesKey(names), algorithm: algorithm}
		m.orderMu.Lock()
		m.orders[orderKey] = pending
		m.orderMu.Unlock()
	}
	defer func() {
		if resultErr != nil && m.orderHasSubmittedDNSLease(pending) {
			resultErr = errors.Join(resultErr, errDNSUnresolvedCleanup)
			if m.ctx.Err() == nil {
				if pending.reconcileUntil.IsZero() {
					pending.reconcileUntil = time.Now().Add(dnsIssuanceTimeout)
				}
				if time.Now().Before(pending.reconcileUntil) {
					pending.deadline = pending.reconcileUntil
					resultErr = &dnsAuthorizationDeferredError{cause: resultErr}
				} else {
					m.abandonDNSOrder(pending)
					resultErr = errors.Join(errDNSUnresolvedCleanup, context.DeadlineExceeded)
				}
			} else {
				m.abandonDNSOrder(pending)
				resultErr = errors.Join(errDNSUnresolvedCleanup, m.ctx.Err())
			}
		}
		if !errors.Is(resultErr, errDNSAuthorizationBusy) {
			m.orderMu.Lock()
			delete(m.orders, orderKey)
			m.orderMu.Unlock()
		}
	}()
	ctx, cancel := context.WithDeadline(ctx, pending.deadline)
	defer cancel()
	order, err := m.authorizeDNSOrder(ctx, pending, names)
	if err != nil {
		return nil, err
	}
	if order.Status != acme.StatusReady {
		return nil, errors.New("ACME order is not ready")
	}
	var key crypto.Signer
	switch algorithm {
	case dnsECDSA:
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case dnsRSA:
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	default:
		return nil, errors.New("unsupported DNS-01 certificate algorithm")
	}
	if err != nil {
		return nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: names}, key)
	if err != nil {
		return nil, err
	}
	chain, _, err := m.client.CreateCertFromOrder(ctx, order, csr, true)
	if err != nil {
		return nil, err
	}
	cert := &tls.Certificate{Certificate: chain, PrivateKey: key}
	if err := validateDNSCertificate(cert, names, algorithm, m.now()); err != nil {
		return nil, err
	}
	return cert, nil
}

func (m *dnsACMEManager) lockAuthorization(ctx context.Context, url string) (func(), dnsValidAuthorization, error) {
	if err := ctx.Err(); err != nil {
		return nil, dnsValidAuthorization{}, err
	}
	m.authMu.Lock()
	defer m.authMu.Unlock()
	now := time.Now()
	state := m.authStates[url]
	if state != nil && state.valid.name != "" && state.valid.until.After(now) {
		return nil, state.valid, nil
	}
	if state != nil && (state.locked || state.nextPoll.After(now)) {
		return nil, dnsValidAuthorization{}, errDNSAuthorizationBusy
	}
	if state == nil {
		if len(m.authStates) >= maxDNSAuthStates {
			for key, existing := range m.authStates {
				if !existing.locked && existing.lease == nil && !existing.nextPoll.After(now) && !existing.nextAccept.After(now) {
					delete(m.authStates, key)
				}
			}
		}
		if len(m.authStates) >= maxDNSAuthStates {
			return nil, dnsValidAuthorization{}, errDNSAuthorizationStateLimit
		}
		state = &dnsAuthorizationState{}
		m.authStates[url] = state
	}
	state.locked = true
	return func() {
		m.authMu.Lock()
		state.locked = false
		now := time.Now()
		if state.lease == nil && !state.nextPoll.After(now) && !state.nextAccept.After(now) {
			delete(m.authStates, url)
		}
		m.authMu.Unlock()
	}, dnsValidAuthorization{}, nil
}

type dnsRetainedLease struct {
	lease          dnsChallengeLease
	challengeURI   string
	challengeToken string
	submitted      bool
}

func (m *dnsACMEManager) retainedDNSLease(url string) dnsRetainedLease {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if state := m.authStates[url]; state != nil {
		return dnsRetainedLease{
			lease: state.lease, challengeURI: state.challengeURI,
			challengeToken: state.challengeToken, submitted: state.submitted,
		}
	}
	return dnsRetainedLease{}
}

func (m *dnsACMEManager) rememberDNSLease(url string, pending *dnsPendingAuthorization) {
	if pending == nil || pending.lease == nil {
		return
	}
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if state := m.authStates[url]; state != nil {
		state.lease = pending.lease
		state.challengeURI = pending.challenge.URI
		state.challengeToken = pending.challenge.Token
		state.submitted = pending.submitted
	}
}

func (m *dnsACMEManager) takeDNSLease(url string) dnsChallengeLease {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if state := m.authStates[url]; state != nil {
		lease := state.lease
		state.lease = nil
		state.challengeURI = ""
		state.challengeToken = ""
		state.submitted = false
		return lease
	}
	return nil
}

func (m *dnsACMEManager) orderHasSubmittedDNSLease(order *dnsPendingOrder) bool {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	for _, url := range order.order.AuthzURLs {
		if state := m.authStates[url]; state != nil && state.lease != nil && state.submitted {
			return true
		}
	}
	return false
}

func (m *dnsACMEManager) abandonDNSOrder(order *dnsPendingOrder) {
	for _, url := range order.order.AuthzURLs {
		if m.ctx.Err() == nil && m.otherDNSOrderUsesAuthorization(order, url) {
			continue
		}
		m.authMu.Lock()
		state := m.authStates[url]
		var lease dnsChallengeLease
		if state != nil && !state.locked && state.submitted {
			lease = state.lease
			state.lease = nil
			state.submitted = false
			if !state.nextPoll.After(time.Now()) && !state.nextAccept.After(time.Now()) {
				delete(m.authStates, url)
			}
		}
		m.authMu.Unlock()
		if lease != nil {
			m.unresolvedDNSCleanup(dnsCleanupTask{lease: lease, id: url, groupKey: order.groupKey, algorithm: order.algorithm},
				"DNS-01 reconciliation ended before the CA reached a terminal state")
		}
	}
}

func (m *dnsACMEManager) otherDNSOrderUsesAuthorization(order *dnsPendingOrder, url string) bool {
	m.orderMu.Lock()
	defer m.orderMu.Unlock()
	for _, other := range m.orders {
		if other == order {
			continue
		}
		for _, otherURL := range other.order.AuthzURLs {
			if otherURL == url {
				return true
			}
		}
	}
	return false
}

func (m *dnsACMEManager) dnsChallengeRetry(url string) time.Time {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if state := m.authStates[url]; state != nil {
		return state.nextAccept
	}
	return time.Time{}
}

func (m *dnsACMEManager) rememberValidDNSAuthorization(url, name string, expires time.Time) time.Time {
	until := time.Now().Add(dnsIssuanceTimeout)
	if !expires.IsZero() && expires.Before(until) {
		until = expires
	}
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if state := m.authStates[url]; state != nil && state.locked {
		state.valid = dnsValidAuthorization{name: name, until: until}
		state.nextPoll = time.Time{}
		state.nextAccept = time.Time{}
		state.pollFailures = 0
		state.acceptFailures = 0
	}
	return until
}

func (m *dnsACMEManager) authorizeDNSOrder(ctx context.Context, current *dnsPendingOrder, names []string) (*acme.Order, error) {
	expected := make(map[string]bool, len(names))
	for _, name := range names {
		expected[name] = true
	}
	order := current.order
	if len(order.AuthzURLs) > 200 {
		return nil, errors.New("ACME order exceeds the DNS-01 authorization limit")
	}
	if current.authorizations == nil {
		current.authorizations = make(map[string]time.Time, len(order.AuthzURLs))
	}
	urls := append([]string(nil), order.AuthzURLs...)
	sort.Strings(urls)
	failures := make([]error, len(urls))
	deferred := make([]error, len(urls))
	valid := make([]time.Time, len(urls))
	unlocks := make([]func(), len(urls))
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			if unlocks[i] != nil {
				unlocks[i]()
			}
		}
	}()
	var busy atomic.Bool
	pending := make([]*dnsPendingAuthorization, len(urls))
	prepareErr := runDNSAuthorizationWorkers(ctx, len(urls), func(ctx context.Context, i int) error {
		if i > 0 && urls[i] == urls[i-1] || current.authorizations[urls[i]].After(time.Now()) {
			return nil
		}
		unlock, shared, err := m.lockAuthorization(ctx, urls[i])
		if errors.Is(err, errDNSAuthorizationBusy) {
			busy.Store(true)
			return nil
		}
		if err != nil {
			return err
		}
		if shared.name != "" {
			if expected[shared.name] {
				valid[i] = shared.until
			} else {
				failures[i] = errors.New("ACME authorization does not match the requested identities")
			}
			return nil
		}
		unlocks[i] = unlock
		retained := m.retainedDNSLease(urls[i])
		auth, nextPoll, err := m.observeDNSAuthorization(ctx, urls[i])
		if err != nil && ctx.Err() == nil && !permanentDNSACMEError(err) {
			deferred[i] = err
			busy.Store(true)
		} else if err != nil {
			failures[i] = err
		} else {
			var name string
			name, failures[i] = validateDNSAuthorization(urls[i], auth, expected)
			identity, identityErr := validateDNSAuthorizationIdentity(auth, expected)
			if retained.lease != nil && identityErr == nil && dnsAuthorizationTerminal(auth, retained.challengeURI) {
				m.queueDNSCleanup(dnsCleanupTask{
					lease: m.takeDNSLease(urls[i]), id: urls[i], groupKey: current.groupKey, algorithm: current.algorithm,
				})
				retained = dnsRetainedLease{}
			}
			if failures[i] == nil && auth.Status == acme.StatusValid {
				valid[i] = m.rememberValidDNSAuthorization(urls[i], name, auth.Expires)
			} else if failures[i] == nil {
				if retained.lease != nil && !dnsChallengeMatches(auth, retained.challengeURI, retained.challengeToken) {
					failures[i] = errors.Join(errDNSUnresolvedCleanup, errors.New("ACME DNS-01 challenge changed during reconciliation"))
					m.unresolvedDNSCleanup(dnsCleanupTask{
						lease: retained.lease, id: urls[i], groupKey: current.groupKey, algorithm: current.algorithm,
					}, "DNS-01 challenge changed before reconciliation finished")
				} else {
					pending[i], failures[i] = m.prepareDNSAuthorizationWithLease(ctx, auth, retained.lease)
				}
				if pending[i] != nil {
					pending[i].url = urls[i]
					pending[i].name = identity
					pending[i].nextPoll = nextPoll
					pending[i].nextAccept = m.dnsChallengeRetry(urls[i])
					pending[i].submitted = retained.submitted || pending[i].challenge.Status != acme.StatusPending
					m.rememberDNSLease(urls[i], pending[i])
				}
			}
		}
		if pending[i] == nil {
			unlocks[i]()
			unlocks[i] = nil
		}
		return nil
	})

	// Authorization count and the number of issuance workers bound these waiters;
	// network requests have separate fixed limits and release permits between polls.
	var waiters sync.WaitGroup
	for i, auth := range pending {
		if auth == nil {
			continue
		}
		waiters.Add(1)
		go func(i int, auth *dnsPendingAuthorization) {
			defer waiters.Done()
			var completed *acme.Authorization
			if failures[i] == nil || auth.challenge.Status != acme.StatusPending {
				var err error
				completed, err = m.completeDNSAuthorization(ctx, urls[i], auth)
				if err == nil {
					name, validationErr := validateDNSAuthorization(urls[i], completed, expected)
					if validationErr != nil {
						err = validationErr
					} else if completed.Status != acme.StatusValid {
						err = errors.New("ACME authorization is not valid")
					} else if name != auth.name {
						err = errors.New("ACME authorization changed its DNS identifier")
					} else {
						valid[i] = m.rememberValidDNSAuthorization(urls[i], name, completed.Expires)
					}
				}
				if err == nil && failures[i] != nil {
					m.log.Warn("DNS-01 presentation failed, but the authorization became valid", "reason", dnsACMEErrorMessage(failures[i]))
					failures[i] = nil
				} else {
					failures[i] = errors.Join(failures[i], err)
				}
			}
			terminal := false
			if completed != nil {
				name, err := validateDNSAuthorizationIdentity(completed, expected)
				terminal = err == nil && name == auth.name && dnsAuthorizationTerminal(completed, auth.challenge.URI)
			}
			if auth.lease != nil {
				if terminal || !auth.submitted {
					lease := m.takeDNSLease(urls[i])
					done := unlocks[i]
					unlocks[i] = nil
					m.queueDNSCleanup(dnsCleanupTask{
						lease: lease, id: urls[i], groupKey: current.groupKey, algorithm: current.algorithm, done: done,
					})
				} else {
					m.rememberDNSLease(urls[i], auth)
					failures[i] = errors.Join(failures[i], errDNSUnresolvedCleanup)
					m.unresolvedDNSCleanup(dnsCleanupTask{
						lease: auth.lease, id: urls[i], groupKey: current.groupKey, algorithm: current.algorithm,
					}, "DNS-01 validation remains unresolved; owned TXT records were retained")
				}
			}
			if unlocks[i] != nil {
				unlocks[i]()
				unlocks[i] = nil
			}
		}(i, auth)
	}
	waiters.Wait()
	for i, complete := range valid {
		if !complete.IsZero() {
			current.authorizations[urls[i]] = complete
		}
	}
	if err := errors.Join(append([]error{prepareErr}, failures...)...); err != nil {
		return nil, err
	}
	if busy.Load() {
		if err := errors.Join(deferred...); err != nil {
			return nil, &dnsAuthorizationDeferredError{cause: err}
		}
		return nil, errDNSAuthorizationBusy
	}
	return m.client.WaitOrder(ctx, order.URI)
}

func runDNSAuthorizationWorkers(ctx context.Context, count int, work func(context.Context, int) error) error {
	var failures []error
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var workers sync.WaitGroup
	for i := 0; i < dnsAuthWorkers && i < count; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				err := work(ctx, i)
				if err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
					cancel()
					return
				}
			}
		}()
	}
send:
	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			break send
		case jobs <- i:
		}
	}
	close(jobs)
	workers.Wait()
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func validateDNSAuthorization(url string, auth *acme.Authorization, expected map[string]bool) (string, error) {
	name, err := validateDNSAuthorizationIdentity(auth, expected)
	if err != nil {
		return "", err
	}
	switch auth.Status {
	case acme.StatusValid, acme.StatusPending:
	case acme.StatusInvalid, acme.StatusDeactivated, acme.StatusExpired, acme.StatusRevoked:
		return "", failedDNSAuthorization(url, auth)
	default:
		return "", errors.New("ACME authorization is not pending or valid")
	}
	if auth.Status == acme.StatusValid && !auth.Expires.IsZero() && !auth.Expires.After(time.Now()) {
		return "", errors.New("ACME authorization has expired")
	}
	return name, nil
}

func validateDNSAuthorizationIdentity(auth *acme.Authorization, expected map[string]bool) (string, error) {
	if auth == nil {
		return "", errors.New("ACME authorization response is empty")
	}
	name := canonicalDNSName(auth.Identifier.Value)
	if auth.Identifier.Type != "dns" || name == "" || strings.HasPrefix(name, "*.") {
		return "", errors.New("ACME authorization has an invalid DNS identifier")
	}
	if auth.Wildcard {
		name = "*." + name
	}
	if !expected[name] {
		return "", errors.New("ACME authorization does not match the requested identities")
	}
	return name, nil
}

func dnsAuthorizationTerminal(auth *acme.Authorization, challengeURI string) bool {
	if auth == nil {
		return false
	}
	switch auth.Status {
	case acme.StatusValid, acme.StatusInvalid, acme.StatusDeactivated, acme.StatusExpired, acme.StatusRevoked:
		return true
	}
	for _, challenge := range auth.Challenges {
		if challenge.URI == challengeURI && (challenge.Status == acme.StatusValid || challenge.Status == acme.StatusInvalid) {
			return true
		}
	}
	return false
}

func dnsChallengeMatches(auth *acme.Authorization, uri, token string) bool {
	for _, challenge := range auth.Challenges {
		if challenge.Type == "dns-01" && challenge.URI == uri && challenge.Token == token {
			return true
		}
	}
	return false
}

func (m *dnsACMEManager) ensureRegistered(ctx context.Context) error {
	m.registerMu.Lock()
	defer m.registerMu.Unlock()
	if !m.registered {
		account := &acme.Account{Contact: []string{"mailto:" + m.email}}
		_, err := m.client.Register(ctx, account, acme.AcceptTOS)
		var acmeErr *acme.Error
		if err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) &&
			(!errors.As(err, &acmeErr) || acmeErr.StatusCode != http.StatusConflict) {
			return err
		}
		m.registered = true
	}
	return nil
}

type dnsPendingAuthorization struct {
	challenge  *acme.Challenge
	lease      dnsChallengeLease
	url        string
	name       string
	nextPoll   time.Time
	nextAccept time.Time
	submitted  bool
	rejected   bool
}

func (m *dnsACMEManager) prepareDNSAuthorization(ctx context.Context, auth *acme.Authorization) (*dnsPendingAuthorization, error) {
	return m.prepareDNSAuthorizationWithLease(ctx, auth, nil)
}

func (m *dnsACMEManager) prepareDNSAuthorizationWithLease(ctx context.Context, auth *acme.Authorization, lease dnsChallengeLease) (*dnsPendingAuthorization, error) {
	var challenge *acme.Challenge
	for _, candidate := range auth.Challenges {
		if candidate.Type == "dns-01" {
			challenge = candidate
			break
		}
	}
	if challenge == nil {
		return nil, errors.New("ACME authorization does not offer DNS-01")
	}
	pending := &dnsPendingAuthorization{challenge: challenge}
	if challenge.Status == acme.StatusValid {
		return pending, nil
	}
	if challenge.Status != acme.StatusPending && challenge.Status != acme.StatusProcessing {
		return nil, errors.New("ACME DNS-01 challenge is not pending")
	}
	if lease != nil {
		pending.lease = lease
		return pending, nil
	}
	value, err := m.client.DNS01ChallengeRecord(challenge.Token)
	if err != nil {
		return nil, err
	}
	owner := "_acme-challenge." + canonicalDNSName(auth.Identifier.Value)
	pending.lease, err = m.provider.Present(ctx, owner, value)
	if err != nil {
		return pending, err
	}
	if pending.lease == nil {
		return nil, errors.New("DNS-01 provider returned no challenge lease")
	}
	return pending, nil
}

func (m *dnsACMEManager) startDNSAuthorization(ctx context.Context, pending *dnsPendingAuthorization) error {
	_, err := m.beginDNSAuthorization(ctx, pending)
	return err
}

func (m *dnsACMEManager) beginDNSAuthorization(ctx context.Context, pending *dnsPendingAuthorization) (bool, error) {
	if pending.submitted {
		if pending.challenge.Status == acme.StatusPending {
			return true, errDNSChallengeRetryDeferred
		}
		return true, nil
	}
	if pending.lease != nil {
		if err := pending.lease.Wait(ctx); err != nil {
			return pending.challenge.Status != acme.StatusPending, err
		}
	}
	if pending.challenge.Status == acme.StatusPending {
		if pending.nextAccept.After(time.Now()) {
			return true, errDNSChallengeRetryDeferred
		}
		var err error
		previouslySubmitted := pending.submitted
		pending.submitted = true
		pending.nextAccept, err = m.acceptDNSChallenge(ctx, pending.url, pending.challenge)
		if err != nil {
			var acmeErr *acme.Error
			pending.rejected = !previouslySubmitted && errors.As(err, &acmeErr) && permanentDNSACMEError(err)
			return true, err
		}
	}
	return true, nil
}

func (m *dnsACMEManager) completeDNSAuthorization(ctx context.Context, url string, pending *dnsPendingAuthorization) (*acme.Authorization, error) {
	started, err := m.beginDNSAuthorization(ctx, pending)
	if !started {
		return nil, err
	}
	if err != nil && pending.challenge.Status == acme.StatusPending {
		auth, reconcileErr := m.reconcileDNSAcceptance(ctx, url, pending.challenge, pending.lease, err, pending.nextPoll, pending.nextAccept, func() {
			pending.submitted = true
			pending.rejected = false
		})
		if pending.rejected && auth != nil && auth.Status == acme.StatusPending {
			matches := 0
			stillPending := false
			for _, challenge := range auth.Challenges {
				if challenge.URI == pending.challenge.URI {
					matches++
					stillPending = challenge.Type == "dns-01" && challenge.Token == pending.challenge.Token &&
						challenge.Status == acme.StatusPending
				}
			}
			if matches == 1 && stillPending {
				pending.submitted = false
			}
		}
		return auth, reconcileErr
	}
	return m.waitDNSAuthorization(ctx, url, err, pending.nextPoll)
}

func (m *dnsACMEManager) acceptDNSChallenge(ctx context.Context, url string, challenge *acme.Challenge) (time.Time, error) {
	capture := &dnsACMERetryCapture{disableRetries: true}
	_, err := m.client.Accept(context.WithValue(ctx, dnsACMERetryContextKey{}, capture), challenge)
	if err != nil && !permanentDNSACMEError(err) &&
		(capture.statusCode == http.StatusRequestTimeout || capture.statusCode == http.StatusTooManyRequests || capture.statusCode >= 500) {
		err = &dnsACMERetryableResponseError{statusCode: capture.statusCode, cause: err}
	}
	m.authMu.Lock()
	defer m.authMu.Unlock()
	state := m.authStates[url]
	if err == nil {
		if state != nil {
			state.nextAccept = time.Time{}
			state.acceptFailures = 0
		}
		return time.Time{}, nil
	}
	failures := 1
	if state != nil {
		state.acceptFailures = min(state.acceptFailures+1, maxDNSFailureCount)
		failures = state.acceptFailures
	}
	next := capture.next(ctx, dnsTransientRetryDelay(failures))
	if state != nil && next.After(state.nextAccept) {
		state.nextAccept = next
	}
	return next, err
}

func (m *dnsACMEManager) reconcileDNSAcceptance(ctx context.Context, url string, challenge *acme.Challenge, lease dnsChallengeLease, lastErr error, nextPoll, nextAccept time.Time, onSubmit func()) (*acme.Authorization, error) {
	var observationErr error
	var lastAuth *acme.Authorization
	var propagation <-chan error
	var cancelWait context.CancelFunc
	defer func() {
		if cancelWait != nil {
			cancelWait()
		}
	}()
	retryAllowed := errors.Is(lastErr, errDNSChallengeRetryDeferred) || retryableDNSChallengeRejection(lastErr)
	if errors.Is(lastErr, errDNSChallengeRetryDeferred) {
		lastErr = nil
	}
	for ctx.Err() == nil {
		if retryAllowed && nextPoll.After(time.Now()) && (nextAccept.IsZero() || nextAccept.Before(nextPoll)) {
			if err := waitDNSAuthorizationPoll(ctx, nextAccept); err != nil {
				return lastAuth, errors.Join(lastErr, observationErr, err)
			}
			if propagation == nil {
				if lease == nil {
					return lastAuth, errors.Join(lastErr, errors.New("DNS-01 retry requires a challenge lease"))
				}
				result := make(chan error, 1)
				propagation = result
				var waitCtx context.Context
				waitCtx, cancelWait = context.WithCancel(ctx)
				go func() { result <- lease.Wait(waitCtx) }()
			}
			timer := time.NewTimer(max(0, time.Until(nextPoll)))
			select {
			case err := <-propagation:
				timer.Stop()
				cancelWait()
				cancelWait = nil
				propagation = nil
				if err != nil {
					lastErr = errors.Join(lastErr, err)
					retryAllowed = false
					continue
				}
				if onSubmit != nil {
					onSubmit()
				}
				nextAccept, lastErr = m.acceptDNSChallenge(ctx, url, challenge)
				if lastErr == nil {
					return m.waitDNSAuthorization(ctx, url, nil, nextPoll)
				}
				retryAllowed = retryableDNSChallengeRejection(lastErr)
				continue
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return lastAuth, errors.Join(lastErr, observationErr, ctx.Err())
			}
		}
		if err := waitDNSAuthorizationPoll(ctx, nextPoll); err != nil {
			return lastAuth, errors.Join(lastErr, observationErr, err)
		}
		auth, next, getErr := m.observeDNSAuthorization(ctx, url)
		nextPoll = next
		if getErr == nil {
			lastAuth = auth
			observationErr = nil
			switch auth.Status {
			case acme.StatusValid:
				return auth, nil
			case acme.StatusPending:
				retryAllowed = false
			challenges:
				for _, candidate := range auth.Challenges {
					if candidate.URI != challenge.URI {
						continue
					}
					switch candidate.Status {
					case acme.StatusProcessing, acme.StatusValid:
						return m.waitDNSAuthorization(ctx, url, lastErr, nextPoll)
					case acme.StatusInvalid:
						return auth, errors.Join(lastErr, candidate.Error, errors.New("ACME DNS-01 challenge is invalid"))
					case acme.StatusPending:
						if permanentDNSACMEError(lastErr) {
							return auth, lastErr
						}
						retryAllowed = true
						break challenges
					}
				}
				if !retryAllowed && cancelWait != nil {
					cancelWait()
					cancelWait = nil
					propagation = nil
				}
			case acme.StatusInvalid, acme.StatusDeactivated, acme.StatusExpired, acme.StatusRevoked:
				return auth, errors.Join(lastErr, failedDNSAuthorization(url, auth))
			default:
				return auth, errors.Join(lastErr, errors.New("ACME authorization has an unsupported status"))
			}
		} else {
			if permanentDNSACMEError(getErr) {
				return lastAuth, errors.Join(lastErr, getErr)
			}
			observationErr = getErr
		}
	}
	return lastAuth, errors.Join(lastErr, observationErr, ctx.Err())
}

func (m *dnsACMEManager) waitDNSAuthorization(ctx context.Context, url string, lastErr error, nextPoll time.Time) (*acme.Authorization, error) {
	initialErr := lastErr
	var lastAuth *acme.Authorization
	for {
		if err := waitDNSAuthorizationPoll(ctx, nextPoll); err != nil {
			return lastAuth, errors.Join(initialErr, lastErr, err)
		}
		auth, next, err := m.observeDNSAuthorization(ctx, url)
		nextPoll = next
		if err != nil {
			if permanentDNSACMEError(err) || ctx.Err() != nil {
				return lastAuth, errors.Join(initialErr, lastErr, err)
			}
			lastErr = err
		} else {
			lastAuth = auth
			lastErr = nil
			switch auth.Status {
			case acme.StatusValid:
				return auth, nil
			case acme.StatusPending:
			case acme.StatusInvalid, acme.StatusDeactivated, acme.StatusExpired, acme.StatusRevoked:
				return auth, errors.Join(initialErr, failedDNSAuthorization(url, auth))
			default:
				return auth, errors.Join(initialErr, errors.New("ACME authorization has an unsupported status"))
			}
		}
	}
}

func (m *dnsACMEManager) observeDNSAuthorization(ctx context.Context, url string) (*acme.Authorization, time.Time, error) {
	// GetAuthorization does not expose headers; capture Retry-After per request.
	capture := &dnsACMERetryCapture{disableRetries: true}
	auth, err := m.client.GetAuthorization(context.WithValue(ctx, dnsACMERetryContextKey{}, capture), url)
	m.authMu.Lock()
	state := m.authStates[url]
	failures := 0
	if err != nil && ctx.Err() == nil && !permanentDNSACMEError(err) {
		failures = 1
		if state != nil {
			state.pollFailures = min(state.pollFailures+1, maxDNSFailureCount)
			failures = state.pollFailures
		}
	} else if err == nil && state != nil {
		state.pollFailures = 0
	}
	next := capture.next(ctx, dnsTransientRetryDelay(failures))
	if state != nil && next.After(state.nextPoll) {
		state.nextPoll = next
	}
	m.authMu.Unlock()
	if failures > 0 && failures&(failures-1) == 0 {
		m.log.Warn("DNS-01 authorization observation deferred", "reason", dnsACMEErrorMessage(err))
	}
	return auth, next, err
}

func (capture *dnsACMERetryCapture) next(ctx context.Context, fallback time.Duration) time.Time {
	if !capture.hasRetryAfter {
		if fallback > 0 {
			return nextDNSAuthorizationPoll(ctx, fallback)
		}
		return time.Time{}
	}
	delay := time.Until(capture.retryAt)
	if delay <= 0 {
		return capture.retryAt
	}
	return nextDNSAuthorizationPoll(ctx, delay)
}

func dnsACMERetryBackoff(attempt int, request *http.Request, response *http.Response) time.Duration {
	capture, _ := request.Context().Value(dnsACMERetryContextKey{}).(*dnsACMERetryCapture)
	if capture != nil && capture.disableRetries {
		return 0
	}
	delay, valid := dnsAuthorizationPollDelay(response.Header.Get("Retry-After"))
	if !valid {
		delay = dnsTransientRetryDelay(attempt)
	}
	return max(time.Millisecond, time.Until(nextDNSAuthorizationPoll(request.Context(), delay)))
}

func dnsTransientRetryDelay(failures int) time.Duration {
	return min(dnsRetryInitial, time.Second<<uint(min(max(failures-1, 0), 6)))
}

func nextDNSAuthorizationPoll(ctx context.Context, delay time.Duration) time.Time {
	now := time.Now()
	jitterLimit := min(delay/4, maxDNSRetryDelay-delay)
	if deadline, ok := ctx.Deadline(); ok {
		remaining := deadline.Sub(now)
		if remaining <= delay {
			jitterLimit = 0
		} else {
			jitterLimit = min(jitterLimit, (remaining-delay)/2)
		}
	}
	jitter := time.Duration(mathrand.Int64N(int64(jitterLimit) + 1))
	return now.Add(delay + jitter)
}

func waitDNSAuthorizationPoll(ctx context.Context, nextPoll time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delay := time.Until(nextPoll)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func dnsAuthorizationPollDelay(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	seconds, err := strconv.ParseUint(value, 10, 64)
	if errors.Is(err, strconv.ErrRange) || err == nil && seconds > uint64(maxDNSRetryDelay/time.Second) {
		return maxDNSRetryDelay, true
	}
	if err == nil {
		return max(time.Second, time.Duration(seconds)*time.Second), true
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(time.Second, time.Until(at)), true
	}
	return 0, false
}

func failedDNSAuthorization(url string, auth *acme.Authorization) *acme.AuthorizationError {
	failure := &acme.AuthorizationError{URI: url, Identifier: auth.Identifier.Value}
	for _, challenge := range auth.Challenges {
		if challenge.Error != nil {
			failure.Errors = append(failure.Errors, challenge.Error)
		}
	}
	if len(failure.Errors) == 0 {
		failure.Errors = append(failure.Errors, errors.New("ACME authorization is "+auth.Status))
	}
	return failure
}

func permanentDNSACMEError(err error) bool {
	if errors.Is(err, egress.ErrBlocked) {
		return true
	}
	var acmeErr *acme.Error
	if errors.As(err, &acmeErr) {
		return !strings.HasSuffix(strings.ToLower(acmeErr.ProblemType), ":badnonce") &&
			acmeErr.StatusCode >= 400 && acmeErr.StatusCode < 500 &&
			acmeErr.StatusCode != http.StatusRequestTimeout && acmeErr.StatusCode != http.StatusTooManyRequests
	}
	return false
}

func retryableDNSChallengeRejection(err error) bool {
	if permanentDNSACMEError(err) {
		return false
	}
	var acmeErr *acme.Error
	var responseErr *dnsACMERetryableResponseError
	return errors.As(err, &acmeErr) || errors.As(err, &responseErr)
}

func (m *dnsACMEManager) cleanupDNSLease(lease dnsChallengeLease, timeout time.Duration) error {
	return m.cleanupDNSLeaseContext(context.Background(), lease, timeout)
}

func (m *dnsACMEManager) cleanupDNSLeaseContext(parent context.Context, lease dnsChallengeLease, timeout time.Duration) error {
	if lease == nil {
		return nil
	}
	admission, cancel := context.WithTimeout(parent, dnsIssuanceTimeout)
	defer cancel()
	release, err := lease.admitCleanup(admission)
	if err == nil {
		defer release()
		select {
		case m.cleanup <- struct{}{}:
			defer func() { <-m.cleanup }()
			ctx, stop := context.WithTimeout(admission, timeout)
			err = lease.cleanupAdmitted(ctx)
			stop()
		case <-admission.Done():
			err = admission.Err()
		}
	}
	if err == nil {
		return nil
	}
	cleanupErr := &dnsCleanupError{cause: err}
	m.log.Warn("DNS-01 challenge cleanup failed", "reason", dnsACMEErrorMessage(cleanupErr))
	return cleanupErr
}

type dnsCleanupTask struct {
	lease     dnsChallengeLease
	id        string
	groupKey  string
	algorithm dnsKeyAlgorithm
	done      func()
}

func (job dnsCleanupTask) finish() {
	if job.done != nil {
		job.done()
	}
}

func (m *dnsACMEManager) setDNSCleanupWarning(job dnsCleanupTask, warning bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	group := m.groups[job.groupKey]
	if group == nil {
		return
	}
	for i := range group.variants {
		variant := &group.variants[i]
		if variant.algorithm != job.algorithm {
			continue
		}
		if warning {
			if variant.cleanupWarnings == nil {
				variant.cleanupWarnings = make(map[string]bool)
			}
			if len(variant.cleanupWarnings) >= 200 && !variant.cleanupWarnings[job.id] {
				variant.cleanupWarnings["additional"] = true
			} else {
				variant.cleanupWarnings[job.id] = true
			}
		} else {
			delete(variant.cleanupWarnings, job.id)
		}
		m.signalGroup(group)
		return
	}
}

func (m *dnsACMEManager) unresolvedDNSCleanup(job dnsCleanupTask, message string) {
	m.setDNSCleanupWarning(job, true)
	owner, marker := "", ""
	if identity, ok := job.lease.(interface{ cleanupIdentity() (string, string) }); ok {
		owner, marker = identity.cleanupIdentity()
		if owner == "" {
			m.setDNSCleanupWarning(job, false)
			return
		}
	}
	m.log.Warn(message, "owner", owner, "marker", marker,
		"recovery", "retain the owned TXT until the CA is terminal; see DNS-01 manual recovery")
}

func (m *dnsACMEManager) queueDNSCleanup(job dnsCleanupTask) {
	if job.lease == nil {
		job.finish()
		return
	}
	m.setDNSCleanupWarning(job, true)
	m.mu.Lock()
	queued := false
	if !m.closed {
		select {
		case m.cleanupJobs <- job:
			queued = true
		default:
		}
	}
	m.mu.Unlock()
	if !queued {
		m.unresolvedDNSCleanup(job, "DNS-01 cleanup could not be queued")
		job.finish()
	}
}

func (m *dnsACMEManager) runDNSCleanup() {
	defer m.wg.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case job := <-m.cleanupJobs:
			if m.ctx.Err() != nil {
				m.unresolvedDNSCleanup(job, "DNS-01 cleanup interrupted by shutdown")
				job.finish()
				continue
			}
			m.retryDNSCleanup(job)
			job.finish()
		}
	}
}

func (m *dnsACMEManager) retryDNSCleanup(job dnsCleanupTask) {
	for attempt := 0; attempt < dnsCleanupRetries; attempt++ {
		err := m.cleanupDNSLeaseContext(m.ctx, job.lease, 15*time.Second)
		if err == nil {
			m.setDNSCleanupWarning(job, false)
			break
		}
		if attempt+1 == dnsCleanupRetries || m.ctx.Err() != nil {
			m.unresolvedDNSCleanup(job, "DNS-01 cleanup retry did not finish")
			break
		}
		m.unresolvedDNSCleanup(job, "DNS-01 cleanup failed; retrying")
		if err := waitDNSInterval(m.ctx, time.Second<<attempt); err != nil {
			m.unresolvedDNSCleanup(job, "DNS-01 cleanup interrupted by shutdown")
			break
		}
	}
}

type dnsCleanupJob struct {
	lease dnsChallengeLease
	done  func(error)
}

func (m *dnsACMEManager) startDNSCleanupWorkers(jobs <-chan dnsCleanupJob, timeout time.Duration) *sync.WaitGroup {
	workers := &sync.WaitGroup{}
	for range dnsAuthWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				job.done(m.cleanupDNSLease(job.lease, timeout))
			}
		}()
	}
	return workers
}

func (m *dnsACMEManager) cleanupDNSAuthorizations(pending []*dnsPendingAuthorization, timeout time.Duration) error {
	jobs := make(chan dnsCleanupJob, len(pending))
	workers := m.startDNSCleanupWorkers(jobs, timeout)
	var mu sync.Mutex
	var failures []error
	for _, auth := range pending {
		if auth == nil || auth.lease == nil {
			continue
		}
		jobs <- dnsCleanupJob{lease: auth.lease, done: func(err error) {
			if err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}}
	}
	close(jobs)
	workers.Wait()
	return errors.Join(failures...)
}
