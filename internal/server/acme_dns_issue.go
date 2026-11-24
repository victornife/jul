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

var (
	errDNSAuthorizationBusy       = errors.New("DNS-01 authorization is already being processed")
	errDNSAuthorizationStateLimit = errors.New("DNS-01 authorization state limit exceeded")
	errDNSChallengeRetryDeferred  = errors.New("DNS-01 challenge retry deferred")
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
	authorizations map[string]time.Time
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
		pending = &dnsPendingOrder{order: order, deadline: deadline}
		m.orderMu.Lock()
		m.orders[orderKey] = pending
		m.orderMu.Unlock()
	}
	defer func() {
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
				if !existing.locked && !existing.nextPoll.After(now) && !existing.nextAccept.After(now) {
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
		if !state.nextPoll.After(now) && !state.nextAccept.After(now) {
			delete(m.authStates, url)
		}
		m.authMu.Unlock()
	}, dnsValidAuthorization{}, nil
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
		auth, nextPoll, err := m.observeDNSAuthorization(ctx, urls[i])
		if err != nil && ctx.Err() == nil && !permanentDNSACMEError(err) {
			deferred[i] = err
			busy.Store(true)
		} else if err != nil {
			failures[i] = err
		} else {
			var name string
			name, failures[i] = validateDNSAuthorization(urls[i], auth, expected)
			if failures[i] == nil && auth.Status == acme.StatusValid {
				valid[i] = m.rememberValidDNSAuthorization(urls[i], name, auth.Expires)
			} else if failures[i] == nil {
				pending[i], failures[i] = m.prepareDNSAuthorization(ctx, auth)
				if pending[i] != nil {
					pending[i].url = urls[i]
					pending[i].name = name
					pending[i].nextPoll = nextPoll
					pending[i].nextAccept = m.dnsChallengeRetry(urls[i])
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
	cleanups := make(chan dnsCleanupJob, len(pending))
	cleanupWorkers := m.startDNSCleanupWorkers(cleanups, 15*time.Second)
	var waiters sync.WaitGroup
	for i, auth := range pending {
		if auth == nil {
			continue
		}
		waiters.Add(1)
		go func(i int, auth *dnsPendingAuthorization) {
			defer waiters.Done()
			if failures[i] == nil || auth.challenge.Status != acme.StatusPending {
				completed, err := m.completeDNSAuthorization(ctx, urls[i], auth)
				if err == nil {
					name, validationErr := validateDNSAuthorization(urls[i], completed, expected)
					if validationErr != nil {
						err = validationErr
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
			cleanups <- dnsCleanupJob{lease: auth.lease, done: func(err error) {
				failures[i] = errors.Join(failures[i], err)
				unlocks[i]()
				unlocks[i] = nil
			}}
		}(i, auth)
	}
	waiters.Wait()
	close(cleanups)
	cleanupWorkers.Wait()
	if err := errors.Join(append([]error{prepareErr}, failures...)...); err != nil {
		return nil, err
	}
	for i, complete := range valid {
		if !complete.IsZero() {
			current.authorizations[urls[i]] = complete
		}
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
}

func (m *dnsACMEManager) prepareDNSAuthorization(ctx context.Context, auth *acme.Authorization) (*dnsPendingAuthorization, error) {
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
		pending.nextAccept, err = m.acceptDNSChallenge(ctx, pending.url, pending.challenge)
		if err != nil {
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
		return m.reconcileDNSAcceptance(ctx, url, pending.challenge, err, pending.nextPoll, pending.nextAccept)
	}
	return m.waitDNSAuthorization(ctx, url, err, pending.nextPoll)
}

func (m *dnsACMEManager) acceptDNSChallenge(ctx context.Context, url string, challenge *acme.Challenge) (time.Time, error) {
	capture := &dnsACMERetryCapture{disableRetries: true}
	_, err := m.client.Accept(context.WithValue(ctx, dnsACMERetryContextKey{}, capture), challenge)
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

func (m *dnsACMEManager) reconcileDNSAcceptance(ctx context.Context, url string, challenge *acme.Challenge, lastErr error, nextPoll, nextAccept time.Time) (*acme.Authorization, error) {
	var observationErr error
	retryAllowed := errors.Is(lastErr, errDNSChallengeRetryDeferred) || retryableDNSChallengeRejection(lastErr)
	if errors.Is(lastErr, errDNSChallengeRetryDeferred) {
		lastErr = nil
	}
	for ctx.Err() == nil {
		if retryAllowed && nextPoll.After(time.Now()) && (nextAccept.IsZero() || nextAccept.Before(nextPoll)) {
			if err := waitDNSAuthorizationPoll(ctx, nextAccept); err != nil {
				return nil, errors.Join(lastErr, observationErr, err)
			}
			nextAccept, lastErr = m.acceptDNSChallenge(ctx, url, challenge)
			if lastErr == nil {
				return m.waitDNSAuthorization(ctx, url, nil, nextPoll)
			}
			retryAllowed = retryableDNSChallengeRejection(lastErr)
			continue
		}
		if err := waitDNSAuthorizationPoll(ctx, nextPoll); err != nil {
			return nil, errors.Join(lastErr, observationErr, err)
		}
		auth, next, getErr := m.observeDNSAuthorization(ctx, url)
		nextPoll = next
		if getErr == nil {
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
						return nil, errors.Join(lastErr, candidate.Error)
					case acme.StatusPending:
						if permanentDNSACMEError(lastErr) {
							return nil, lastErr
						}
						retryAllowed = true
						break challenges
					}
				}
			case acme.StatusInvalid, acme.StatusDeactivated, acme.StatusExpired, acme.StatusRevoked:
				return nil, errors.Join(lastErr, failedDNSAuthorization(url, auth))
			default:
				return nil, errors.Join(lastErr, errors.New("ACME authorization has an unsupported status"))
			}
		} else {
			if permanentDNSACMEError(getErr) {
				return nil, errors.Join(lastErr, getErr)
			}
			observationErr = getErr
		}
	}
	return nil, errors.Join(lastErr, observationErr, ctx.Err())
}

func (m *dnsACMEManager) waitDNSAuthorization(ctx context.Context, url string, lastErr error, nextPoll time.Time) (*acme.Authorization, error) {
	initialErr := lastErr
	for {
		if err := waitDNSAuthorizationPoll(ctx, nextPoll); err != nil {
			return nil, errors.Join(initialErr, lastErr, err)
		}
		auth, next, err := m.observeDNSAuthorization(ctx, url)
		nextPoll = next
		if err != nil {
			if permanentDNSACMEError(err) || ctx.Err() != nil {
				return nil, errors.Join(initialErr, lastErr, err)
			}
			lastErr = err
		} else {
			lastErr = nil
			switch auth.Status {
			case acme.StatusValid:
				return auth, nil
			case acme.StatusPending:
			case acme.StatusInvalid, acme.StatusDeactivated, acme.StatusExpired, acme.StatusRevoked:
				return nil, errors.Join(initialErr, failedDNSAuthorization(url, auth))
			default:
				return nil, errors.Join(initialErr, errors.New("ACME authorization has an unsupported status"))
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
	var acmeErr *acme.Error
	return errors.As(err, &acmeErr) && !permanentDNSACMEError(err)
}

func (m *dnsACMEManager) cleanupDNSLease(lease dnsChallengeLease, timeout time.Duration) error {
	if lease == nil {
		return nil
	}
	admission, cancel := context.WithTimeout(context.Background(), dnsIssuanceTimeout)
	defer cancel()
	release, err := lease.admitCleanup(admission)
	if err == nil {
		defer release()
		select {
		case m.cleanup <- struct{}{}:
			defer func() { <-m.cleanup }()
			ctx, stop := context.WithTimeout(context.Background(), timeout)
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
