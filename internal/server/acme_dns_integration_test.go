// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/acme"

	"jul/internal/config"
	"jul/internal/egress"
)

type testACMEAuthorization struct {
	name, token string
	status      string
	expires     time.Time
	reads       int
	valid       bool
	invalid     bool
	accepted    bool
}

type testACMEOrder struct {
	names  []string
	auths  []string
	chain  []byte
	serial int64
}

type testDNSACMECA struct {
	mu                  sync.Mutex
	dns                 *testRFC2136Server
	issuer              *ocspTestCA
	server              *httptest.Server
	orders              map[string]*testACMEOrder
	auths               map[string]*testACMEAuthorization
	thumbprint          string
	registrations       int
	validations         int
	processingReads     int
	nonce               int
	fault               string
	omitChallenge       string
	authFailures        int
	authStatus          int
	authReads           int
	authAttempts        int
	authRetryAfter      string
	challengeFailures   int
	challengeStatus     int
	challengeAttempts   int
	challengeRetryAfter string
	challengeGate       <-chan struct{}
	challengeSeen       chan struct{}
	challengeOnce       sync.Once
	orderRetryAfter     string
	orderReads          int
}

func startTestDNSACMECA(t *testing.T, dns *testRFC2136Server, fault string) *testDNSACMECA {
	t.Helper()
	ca := &testDNSACMECA{
		dns: dns, issuer: newOCSPTestCA(t), fault: fault,
		orders: map[string]*testACMEOrder{}, auths: map[string]*testACMEAuthorization{},
	}
	ca.server = httptest.NewTLSServer(ca)
	t.Cleanup(ca.server.Close)
	return ca
}

func (ca *testDNSACMECA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	ca.nonce++
	w.Header().Set("Replay-Nonce", fmt.Sprintf("nonce-%d", ca.nonce))
	base := ca.server.URL
	if r.URL.Path == "/directory" {
		writeTestACMEJSON(w, http.StatusOK, map[string]any{
			"newNonce": base + "/nonce", "newAccount": base + "/account", "newOrder": base + "/new-order",
		})
		return
	}
	if r.URL.Path == "/nonce" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var envelope struct {
		Protected string `json:"protected"`
		Payload   string `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		http.Error(w, "invalid JWS", http.StatusBadRequest)
		return
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		http.Error(w, "invalid JWS payload", http.StatusBadRequest)
		return
	}
	switch {
	case r.URL.Path == "/account":
		protected, err := base64.RawURLEncoding.DecodeString(envelope.Protected)
		if err != nil {
			http.Error(w, "invalid protected header", http.StatusBadRequest)
			return
		}
		var header struct {
			JWK struct {
				Curve string `json:"crv"`
				Type  string `json:"kty"`
				X     string `json:"x"`
				Y     string `json:"y"`
			} `json:"jwk"`
		}
		if err := json.Unmarshal(protected, &header); err != nil || header.JWK.Type != "EC" || header.JWK.Curve != "P-256" {
			http.Error(w, "invalid account JWK", http.StatusBadRequest)
			return
		}
		canonical := fmt.Sprintf(`{"crv":%q,"kty":%q,"x":%q,"y":%q}`, header.JWK.Curve, header.JWK.Type, header.JWK.X, header.JWK.Y)
		hash := sha256.Sum256([]byte(canonical))
		ca.thumbprint = base64.RawURLEncoding.EncodeToString(hash[:])
		ca.registrations++
		w.Header().Set("Location", base+"/account/1")
		writeTestACMEJSON(w, http.StatusCreated, map[string]any{"status": "valid"})
	case r.URL.Path == "/new-order":
		var request struct {
			Identifiers []struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"identifiers"`
		}
		if err := json.Unmarshal(payload, &request); err != nil || len(request.Identifiers) == 0 {
			http.Error(w, "invalid order", http.StatusBadRequest)
			return
		}
		id := strconv.Itoa(len(ca.orders) + 1)
		order := &testACMEOrder{serial: int64(len(ca.orders) + 100)}
		for i, identifier := range request.Identifiers {
			if identifier.Type != "dns" {
				http.Error(w, "invalid identifier", http.StatusBadRequest)
				return
			}
			authID := id + "-" + strconv.Itoa(i)
			if ca.fault == "shared-authorization" {
				for existingID, existing := range ca.auths {
					if existing.name == identifier.Value {
						authID = existingID
						break
					}
				}
			}
			order.names = append(order.names, identifier.Value)
			order.auths = append(order.auths, authID)
			if ca.auths[authID] == nil {
				ca.auths[authID] = &testACMEAuthorization{name: identifier.Value, token: "token-" + authID}
			}
		}
		switch ca.fault {
		case "external-authorization":
			order.auths = nil
		case "extra-authorization":
			authID := id + "-extra"
			ca.auths[authID] = &testACMEAuthorization{name: order.names[0], token: "token-" + authID}
			order.auths = append(order.auths, authID)
		case "duplicate-authorization-url":
			order.auths = append(order.auths, order.auths[0])
		}
		ca.orders[id] = order
		w.Header().Set("Location", base+"/order/"+id)
		writeTestACMEJSON(w, http.StatusCreated, ca.orderResponse(id, order))
	case strings.HasPrefix(r.URL.Path, "/authz/"):
		ca.authAttempts++
		if ca.authRetryAfter != "" {
			w.Header().Set("Retry-After", ca.authRetryAfter)
		}
		if ca.authFailures > 0 {
			ca.authFailures--
			http.Error(w, "injected authorization failure", ca.authStatus)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/authz/")
		auth := ca.auths[id]
		if auth == nil {
			http.NotFound(w, r)
			return
		}
		ca.authReads++
		auth.reads++
		if auth.accepted && !auth.valid {
			ca.processingReads++
		}
		name := strings.TrimPrefix(auth.name, "*.")
		status := "pending"
		if auth.valid {
			status = "valid"
		}
		if ca.fault == "invalid-authorization" || auth.invalid {
			status = "invalid"
		}
		if auth.status != "" {
			status = auth.status
		}
		if ca.fault == "wrong-identifier" {
			name = "outside.example.test"
		}
		challengeStatus := status
		if auth.accepted && !auth.valid && !auth.invalid {
			challengeStatus = "processing"
		}
		challenges := []map[string]any{{
			"type": "dns-01", "url": base + "/challenge/" + id,
			"token": auth.token, "status": challengeStatus,
		}}
		if status == "invalid" {
			challenges[0]["error"] = map[string]any{
				"type": "urn:ietf:params:acme:error:unauthorized", "status": http.StatusForbidden,
				"detail": "injected challenge failure",
			}
		}
		if ca.fault == "missing-challenge" || auth.name == ca.omitChallenge {
			challenges = nil
		}
		response := map[string]any{
			"status": status, "identifier": map[string]string{"type": "dns", "value": name},
			"wildcard": strings.HasPrefix(auth.name, "*."), "challenges": challenges,
		}
		if !auth.expires.IsZero() {
			response["expires"] = auth.expires
		}
		writeTestACMEJSON(w, http.StatusOK, response)
	case strings.HasPrefix(r.URL.Path, "/challenge/"):
		ca.challengeAttempts++
		if ca.challengeRetryAfter != "" {
			w.Header().Set("Retry-After", ca.challengeRetryAfter)
		}
		if ca.challengeFailures > 0 {
			ca.challengeFailures--
			http.Error(w, "injected challenge failure", ca.challengeStatus)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/challenge/")
		auth := ca.auths[id]
		if auth == nil {
			http.NotFound(w, r)
			return
		}
		if ca.challengeGate != nil {
			ca.challengeOnce.Do(func() { close(ca.challengeSeen) })
			select {
			case <-r.Context().Done():
				return
			case <-ca.challengeGate:
			}
		}
		digest := sha256.Sum256([]byte(auth.token + "." + ca.thumbprint))
		expected := base64.RawURLEncoding.EncodeToString(digest[:])
		owner := "_acme-challenge." + strings.TrimPrefix(auth.name, "*.") + "."
		ca.dns.mu.Lock()
		records := ca.dns.records[owner]
		owned, marker := records[expected], false
		for value := range records {
			marker = marker || strings.HasPrefix(value, "jul-dns01-")
		}
		ca.dns.mu.Unlock()
		if !owned || !marker || ca.fault == "reject-challenge" {
			http.Error(w, "challenge not accepted", http.StatusBadRequest)
			return
		}
		auth.accepted = true
		if ca.fault == "batch-validation" {
			complete := true
			for _, candidate := range ca.auths {
				complete = complete && candidate.accepted
			}
			if complete {
				for _, candidate := range ca.auths {
					candidate.valid = true
				}
			}
		} else if ca.fault != "processing-validation" {
			auth.valid = true
		}
		ca.validations++
		status := "processing"
		if auth.valid {
			status = "valid"
		}
		writeTestACMEJSON(w, http.StatusOK, map[string]string{
			"type": "dns-01", "url": base + "/challenge/" + id, "token": auth.token, "status": status,
		})
	case strings.HasPrefix(r.URL.Path, "/order/"):
		ca.orderReads++
		if ca.orderRetryAfter != "" {
			w.Header().Set("Retry-After", ca.orderRetryAfter)
		}
		id := strings.TrimPrefix(r.URL.Path, "/order/")
		order := ca.orders[id]
		if order == nil {
			http.NotFound(w, r)
			return
		}
		writeTestACMEJSON(w, http.StatusOK, ca.orderResponse(id, order))
	case strings.HasPrefix(r.URL.Path, "/finalize/"):
		id := strings.TrimPrefix(r.URL.Path, "/finalize/")
		order := ca.orders[id]
		if order == nil {
			http.NotFound(w, r)
			return
		}
		if ca.fault == "reject-finalize" {
			http.Error(w, "finalization rejected", http.StatusBadRequest)
			return
		}
		var request struct {
			CSR string `json:"csr"`
		}
		if err := json.Unmarshal(payload, &request); err != nil {
			http.Error(w, "invalid finalization", http.StatusBadRequest)
			return
		}
		der, err := base64.RawURLEncoding.DecodeString(request.CSR)
		if err != nil {
			http.Error(w, "invalid CSR encoding", http.StatusBadRequest)
			return
		}
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil || csr.CheckSignature() != nil || !equalDNSNames(canonicalDNSNames(csr.DNSNames), canonicalDNSNames(order.names)) {
			http.Error(w, "invalid CSR", http.StatusBadRequest)
			return
		}
		now := time.Now()
		leaf, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
			SerialNumber: big.NewInt(order.serial), DNSNames: csr.DNSNames,
			NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}, ca.issuer.issuer, csr.PublicKey, ca.issuer.key)
		if err != nil {
			http.Error(w, "certificate signing failed", http.StatusInternalServerError)
			return
		}
		order.chain = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf}),
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.issuer.issuerDER})...)
		writeTestACMEJSON(w, http.StatusOK, ca.orderResponse(id, order))
	case strings.HasPrefix(r.URL.Path, "/certificate/"):
		id := strings.TrimPrefix(r.URL.Path, "/certificate/")
		order := ca.orders[id]
		if order == nil || len(order.chain) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(order.chain)
	default:
		http.NotFound(w, r)
	}
}

func writeTestACMEJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (ca *testDNSACMECA) orderResponse(id string, order *testACMEOrder) map[string]any {
	status := "ready"
	authURLs := make([]string, 0, len(order.auths))
	for _, authID := range order.auths {
		authURLs = append(authURLs, ca.server.URL+"/authz/"+authID)
		if !ca.auths[authID].valid {
			status = "pending"
		}
	}
	if len(order.chain) != 0 {
		status = "valid"
	}
	if ca.fault == "missing-authorization" {
		authURLs = nil
	}
	return map[string]any{
		"status": status, "authorizations": authURLs, "finalize": ca.server.URL + "/finalize/" + id,
		"certificate": ca.server.URL + "/certificate/" + id,
	}
}

func testIntegratedDNSManager(t *testing.T, ca *testDNSACMECA, provider *rfc2136Provider, dir string) *dnsACMEManager {
	t.Helper()
	m, err := newDNSACMEManager(&config.ACMEConfig{
		Email: "ops@example.test", CA: ca.server.URL + "/directory", CacheDir: dir,
		RFC2136: &config.RFC2136Config{
			Server: provider.server, Zone: "example.test", TSIGKeyName: testTSIGName, TSIGSecret: testTSIGSecret,
			PropagationTimeout: config.Duration(time.Second), PollInterval: config.Duration(5 * time.Millisecond),
		},
	}, nil, ca.server.Client(), nil, ACMEOptions{DNSDialContext: (&net.Dialer{}).DialContext})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("close DNS manager: %v", err)
		}
	})
	return m
}

func assertIntegratedDNSClean(t *testing.T, dns *testRFC2136Server) {
	t.Helper()
	dns.mu.Lock()
	defer dns.mu.Unlock()
	records := dns.records[testOwner]
	if len(records) != 1 || !records["unrelated"] {
		t.Fatalf("owned challenge records were not cleaned up safely: %v", records)
	}
}

func TestDNS01InProcessACMEIssuanceRenewalAndRestart(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	dns.mu.Lock()
	dns.records[testOwner] = map[string]bool{"unrelated": true}
	dns.mu.Unlock()
	ca := startTestDNSACMECA(t, dns, "")
	dir := t.TempDir()
	m := testIntegratedDNSManager(t, ca, provider, dir)
	names := []string{"*.example.test", "example.test"}
	serving := m.Provider(names)
	hellos := []*tls.ClientHelloInfo{dnsTestHello("www.example.test"), dnsTestRSAHello("www.example.test")}
	first := make([]*tls.Certificate, len(hellos))
	for i, hello := range hellos {
		var err error
		first[i], err = serving.GetCertificate(hello)
		if err != nil {
			t.Fatal(err)
		}
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.issuer.issuer)
	for _, cert := range first {
		for _, name := range []string{"example.test", "www.example.test"} {
			if _, err := cert.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
				t.Fatalf("verify %s: %v", name, err)
			}
		}
	}
	assertIntegratedDNSClean(t, dns)
	m.mu.Lock()
	group := m.groups[dnsNamesKey(canonicalDNSNames(names))]
	for i := range group.variants {
		group.variants[i].nextAttempt = time.Time{}
	}
	m.mu.Unlock()
	m.notifyWorker(false)
	renewed := make([]*tls.Certificate, len(hellos))
	for i, hello := range hellos {
		deadline := time.Now().Add(10 * time.Second)
		var err error
		for time.Now().Before(deadline) {
			renewed[i], err = serving.GetCertificate(hello)
			if err == nil && renewed[i].Leaf.SerialNumber.Cmp(first[i].Leaf.SerialNumber) != 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if err != nil || renewed[i].Leaf.SerialNumber.Cmp(first[i].Leaf.SerialNumber) == 0 {
			t.Fatalf("variant %d did not renew: %v", i, err)
		}
	}
	assertIntegratedDNSClean(t, dns)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := testIntegratedDNSManager(t, ca, provider, dir)
	for i, hello := range hellos {
		cached, err := restarted.Provider(names).GetCertificate(hello)
		if err != nil || cached.Leaf.SerialNumber.Cmp(renewed[i].Leaf.SerialNumber) != 0 {
			t.Fatalf("restarted manager did not serve cached variant %d: %v", i, err)
		}
	}
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if ca.registrations != 1 || len(ca.orders) != 4 || ca.validations != 8 {
		t.Fatalf("registrations=%d orders=%d validations=%d", ca.registrations, len(ca.orders), ca.validations)
	}
}

func TestDNS01InProcessACMEFailureCleanup(t *testing.T) {
	for _, fault := range []string{
		"missing-authorization", "wrong-identifier", "invalid-authorization",
		"missing-challenge", "reject-challenge", "reject-finalize",
	} {
		t.Run(fault, func(t *testing.T) {
			dns, provider := startRFC2136Server(t)
			dns.mu.Lock()
			dns.records[testOwner] = map[string]bool{"unrelated": true}
			dns.mu.Unlock()
			ca := startTestDNSACMECA(t, dns, fault)
			m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := m.ensureAccountKey(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := m.issueCertificate(ctx, []string{"example.test"}, dnsECDSA); err == nil {
				t.Fatal("invalid ACME response was accepted")
			}
			assertIntegratedDNSClean(t, dns)
		})
	}
}

type concurrentAuthorizationProvider struct {
	dnsChallengeProvider
	presented atomic.Int32
	waiting   atomic.Int32
	ready     chan struct{}
	target    int32
	failWait  bool
}

func (p *concurrentAuthorizationProvider) Present(ctx context.Context, owner, value string) (dnsChallengeLease, error) {
	lease, err := p.dnsChallengeProvider.Present(ctx, owner, value)
	if err != nil {
		return lease, err
	}
	number := p.presented.Add(1)
	if number == p.target {
		close(p.ready)
	}
	return &concurrentAuthorizationLease{
		dnsChallengeLease: lease, ready: p.ready, failWait: p.failWait, waiting: &p.waiting,
	}, nil
}

type concurrentAuthorizationLease struct {
	dnsChallengeLease
	ready    <-chan struct{}
	failWait bool
	waiting  *atomic.Int32
}

type blockingAuthorizationProvider struct {
	dnsChallengeProvider
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingAuthorizationProvider) Present(ctx context.Context, owner, value string) (dnsChallengeLease, error) {
	lease, err := p.dnsChallengeProvider.Present(ctx, owner, value)
	if err != nil || owner != "_acme-challenge.example.test" {
		return lease, err
	}
	return &blockingAuthorizationLease{dnsChallengeLease: lease, provider: p}, nil
}

type blockingAuthorizationLease struct {
	dnsChallengeLease
	provider *blockingAuthorizationProvider
}

type blockingCleanupProvider struct {
	dnsChallengeProvider
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingCleanupProvider) Present(ctx context.Context, owner, value string) (dnsChallengeLease, error) {
	lease, err := p.dnsChallengeProvider.Present(ctx, owner, value)
	if err != nil {
		return lease, err
	}
	return &blockingCleanupLease{dnsChallengeLease: lease, provider: p}, nil
}

type blockingCleanupLease struct {
	dnsChallengeLease
	provider *blockingCleanupProvider
}

func (l *blockingCleanupLease) admitCleanup(ctx context.Context) (func(), error) {
	l.provider.once.Do(func() { close(l.provider.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.provider.release:
		return l.dnsChallengeLease.admitCleanup(ctx)
	}
}

type hookedAuthorizationProvider struct {
	dnsChallengeProvider
	presented func(string)
	wait      func(context.Context, string, dnsChallengeLease) error
}

func (p *hookedAuthorizationProvider) Present(ctx context.Context, owner, value string) (dnsChallengeLease, error) {
	lease, err := p.dnsChallengeProvider.Present(ctx, owner, value)
	if err != nil {
		return lease, err
	}
	if p.presented != nil {
		p.presented(owner)
	}
	return &hookedAuthorizationLease{dnsChallengeLease: lease, owner: owner, wait: p.wait}, nil
}

type hookedAuthorizationLease struct {
	dnsChallengeLease
	owner string
	wait  func(context.Context, string, dnsChallengeLease) error
}

type signallingAuthorizationLease struct {
	dnsChallengeLease
	waiting chan struct{}
}

func (l *signallingAuthorizationLease) Wait(ctx context.Context) error {
	close(l.waiting)
	return l.dnsChallengeLease.Wait(ctx)
}

type droppedAcceptanceTransport struct {
	next        http.RoundTripper
	afterSend   bool
	passAccept  bool
	failObserve bool
	attempts    atomic.Int32
	observed    atomic.Bool
}

type staleRetryAfterTransport struct {
	next     http.RoundTripper
	attempts atomic.Int32
}

type blockedAuthorizationTransport struct{ next http.RoundTripper }

func (t *blockedAuthorizationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.Path, "/authz/") {
		return nil, egress.ErrBlocked
	}
	return t.next.RoundTrip(req)
}

func (t *staleRetryAfterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(req.URL.Path, "/authz/") {
		return t.next.RoundTrip(req)
	}
	if t.attempts.Add(1) != 1 {
		return nil, errors.New("injected authorization network failure")
	}
	response, err := t.next.RoundTrip(req)
	if err == nil {
		response.StatusCode = http.StatusTooManyRequests
		response.Status = "429 Too Many Requests"
		response.Header.Set("Retry-After", "2")
	}
	return response, err
}

func (t *droppedAcceptanceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.Path, "/authz/") && t.attempts.Load() != 0 &&
		t.failObserve && !t.observed.Swap(true) {
		return nil, errors.New("injected lost authorization observation")
	}
	if !strings.HasPrefix(req.URL.Path, "/challenge/") || t.attempts.Add(1) != 1 || t.passAccept {
		return t.next.RoundTrip(req)
	}
	if t.afterSend {
		response, err := t.next.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		if err := errors.Join(readErr, response.Body.Close()); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("injected lost acceptance response")
}

func (l *hookedAuthorizationLease) Wait(ctx context.Context) error {
	if l.wait != nil {
		return l.wait(ctx, l.owner, l.dnsChallengeLease)
	}
	return l.dnsChallengeLease.Wait(ctx)
}

func waitForDNSACMETest(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (l *blockingAuthorizationLease) Wait(ctx context.Context) error {
	l.provider.once.Do(func() { close(l.provider.entered) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.provider.release:
		return l.dnsChallengeLease.Wait(ctx)
	}
}

func (l *concurrentAuthorizationLease) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.ready:
		if l.failWait {
			if l.waiting.Add(1) == 2 {
				return errors.New("injected propagation failure")
			}
			<-ctx.Done()
			return ctx.Err()
		}
		return l.dnsChallengeLease.Wait(ctx)
	}
}

func TestDNS01InProcessAuthorizationsPropagateConcurrently(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	dns.mu.Lock()
	dns.records[testOwner] = map[string]bool{"unrelated": true}
	dns.mu.Unlock()
	ca := startTestDNSACMECA(t, dns, "batch-validation")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	barrier := &concurrentAuthorizationProvider{dnsChallengeProvider: m.provider, ready: make(chan struct{}), target: 6}
	m.provider = barrier
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	names := canonicalDNSNames([]string{"*.example.test", "example.test", "a.example.test", "b.example.test", "c.example.test", "d.example.test"})
	if _, err := m.issueCertificate(ctx, names, dnsECDSA); err != nil {
		ca.mu.Lock()
		validations := ca.validations
		ca.mu.Unlock()
		t.Fatalf("concurrent authorization failed after %d presentations and %d validations: %v", barrier.presented.Load(), validations, err)
	}
	if barrier.presented.Load() != int32(len(names)) {
		t.Fatalf("presented %d authorizations, want %d", barrier.presented.Load(), len(names))
	}
	assertIntegratedDNSClean(t, dns)
	dns.mu.Lock()
	defer dns.mu.Unlock()
	for owner, values := range dns.records {
		if owner != testOwner && len(values) != 0 {
			t.Fatalf("challenge values remain at %s: %v", owner, values)
		}
	}
}

func TestDNS01InProcessSlowPropagationDoesNotStarveReadyChallenge(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	release := make(chan struct{})
	releaseAll := sync.OnceFunc(func() { close(release) })
	defer releaseAll()
	entered := make(chan string, 4)
	m.provider = &hookedAuthorizationProvider{dnsChallengeProvider: m.provider, wait: func(ctx context.Context, owner string, lease dnsChallengeLease) error {
		if owner != "_acme-challenge.z.example.test" {
			entered <- owner
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
			}
		}
		return lease.Wait(ctx)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"a.example.test", "b.example.test", "c.example.test", "d.example.test", "z.example.test"}, dnsECDSA)
		result <- err
	}()
	for range 4 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("slow challenges did not start")
		}
	}
	waitForDNSACMETest(t, "the later ready challenge to be accepted", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		for _, auth := range ca.auths {
			if auth.name == "z.example.test" {
				return auth.accepted
			}
		}
		return false
	})
	releaseAll()
	if err := <-result; err != nil {
		t.Fatalf("order did not finish after slow propagation recovered: %v", err)
	}
}

func TestDNS01InProcessReleasesValidAuthorizationBeforeSlowSibling(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "shared-authorization")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	release := make(chan struct{})
	releaseAll := sync.OnceFunc(func() { close(release) })
	defer releaseAll()
	entered := make(chan struct{})
	var enteredOnce sync.Once
	m.provider = &hookedAuthorizationProvider{dnsChallengeProvider: m.provider, wait: func(ctx context.Context, owner string, lease dnsChallengeLease) error {
		if owner == "_acme-challenge.b.example.test" {
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
			}
		}
		return lease.Wait(ctx)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"a.example.test", "b.example.test"}, dnsECDSA)
		first <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("slow sibling did not start")
	}
	var sharedURL string
	waitForDNSACMETest(t, "the shared authorization to become valid", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		for id, auth := range ca.auths {
			if auth.name == "a.example.test" && auth.valid {
				sharedURL = ca.server.URL + "/authz/" + id
				return true
			}
		}
		return false
	})
	waitForDNSACMETest(t, "the valid authorization lock to be released", func() bool {
		m.authMu.Lock()
		defer m.authMu.Unlock()
		state := m.authStates[sharedURL]
		return state == nil || !state.locked
	})
	if _, err := m.issueCertificate(ctx, []string{"a.example.test", "c.example.test"}, dnsECDSA); err != nil {
		t.Fatalf("order reusing the valid authorization failed: %v", err)
	}
	select {
	case err := <-first:
		t.Fatalf("slow order completed before its challenge was released: %v", err)
	default:
	}
	releaseAll()
	if err := <-first; err != nil {
		t.Fatalf("slow order did not finish: %v", err)
	}
}

func TestDNS01InProcessBusyAuthorizationDoesNotDelayIndependentChallenge(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "shared-authorization")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	sharedGate, independentGate := make(chan struct{}), make(chan struct{})
	releaseShared := sync.OnceFunc(func() { close(sharedGate) })
	releaseIndependent := sync.OnceFunc(func() { close(independentGate) })
	defer releaseShared()
	defer releaseIndependent()
	sharedStarted, independentStarted := make(chan struct{}), make(chan struct{})
	var sharedOnce, independentOnce sync.Once
	m.provider = &hookedAuthorizationProvider{dnsChallengeProvider: m.provider, wait: func(ctx context.Context, owner string, lease dnsChallengeLease) error {
		var gate <-chan struct{}
		switch owner {
		case "_acme-challenge.a.example.test":
			sharedOnce.Do(func() { close(sharedStarted) })
			gate = sharedGate
		case "_acme-challenge.b.example.test":
			independentOnce.Do(func() { close(independentStarted) })
			gate = independentGate
		}
		if gate != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-gate:
			}
		}
		return lease.Wait(ctx)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"a.example.test"}, dnsECDSA)
		first <- err
	}()
	select {
	case <-sharedStarted:
	case <-ctx.Done():
		t.Fatal("shared authorization did not start")
	}
	second := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"a.example.test", "b.example.test"}, dnsECDSA)
		second <- err
	}()
	select {
	case <-independentStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("busy shared authorization delayed the independent challenge")
	}
	releaseIndependent()
	if err := <-second; !errors.Is(err, errDNSAuthorizationBusy) {
		t.Fatalf("order did not retain the busy authorization: %v", err)
	}
	releaseShared()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if _, err := m.issueCertificate(ctx, []string{"a.example.test", "b.example.test"}, dnsECDSA); err != nil {
		t.Fatalf("retained order did not complete: %v", err)
	}
	ca.mu.Lock()
	orders, validations := len(ca.orders), ca.validations
	ca.mu.Unlock()
	if orders != 2 || validations != 2 {
		t.Fatalf("orders=%d validations=%d, want 2 of each", orders, validations)
	}
}

func TestDNS01InProcessUnstartedPreparationDoesNotLockAuthorization(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "shared-authorization")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	names := []string{"a.example.test", "b.example.test", "c.example.test", "d.example.test", "e.example.test"}
	firstOrder, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, dnsAuthWorkers)
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	m.provider = &hookedAuthorizationProvider{dnsChallengeProvider: m.provider, presented: func(owner string) {
		if owner != "_acme-challenge.e.example.test" {
			entered <- struct{}{}
			<-gate
		}
	}}
	first := make(chan error, 1)
	go func() {
		_, err := m.authorizeDNSOrder(ctx, &dnsPendingOrder{order: firstOrder}, names)
		first <- err
	}()
	for range dnsAuthWorkers {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("preparation workers did not become blocked")
		}
	}
	secondOrder, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs(names[4]))
	if err != nil {
		t.Fatal(err)
	}
	if secondOrder.AuthzURLs[0] != firstOrder.AuthzURLs[4] {
		t.Fatal("orders did not share the queued authorization")
	}
	if _, err := m.authorizeDNSOrder(ctx, &dnsPendingOrder{order: secondOrder}, names[4:]); err != nil {
		t.Fatalf("unstarted preparation blocked another order: %v", err)
	}
	select {
	case err := <-first:
		t.Fatalf("blocked order completed before release: %v", err)
	default:
	}
	release()
	if err := <-first; err != nil {
		t.Fatalf("blocked order did not complete after release: %v", err)
	}
}

func TestDNS01InProcessSiblingFailureKeepsProcessingChallenge(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "processing-validation")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	fail := make(chan struct{})
	failed := make(chan struct{})
	m.provider = &hookedAuthorizationProvider{dnsChallengeProvider: m.provider, wait: func(ctx context.Context, owner string, lease dnsChallengeLease) error {
		if owner == "_acme-challenge.b.example.test" {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-fail:
				close(failed)
				return errors.New("injected propagation failure")
			}
		}
		return lease.Wait(ctx)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"a.example.test", "b.example.test"}, dnsECDSA)
		result <- err
	}()
	var authID, token string
	waitForDNSACMETest(t, "the first challenge to be accepted", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		for id, auth := range ca.auths {
			if auth.name == "a.example.test" && auth.accepted {
				authID = id
				token = auth.token
				return true
			}
		}
		return false
	})
	value, err := m.client.DNS01ChallengeRecord(token)
	if err != nil {
		t.Fatal(err)
	}
	close(fail)
	select {
	case <-failed:
	case <-ctx.Done():
		t.Fatal("sibling did not fail")
	}
	select {
	case err := <-result:
		t.Fatalf("order abandoned a processing authorization: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	dns.mu.Lock()
	preserved := dns.records["_acme-challenge.a.example.test."][value]
	dns.mu.Unlock()
	if !preserved {
		t.Fatal("sibling failure removed the processing authorization's TXT value")
	}
	ca.mu.Lock()
	ca.auths[authID].valid = true
	ca.mu.Unlock()
	if err := <-result; err == nil {
		t.Fatal("order did not report its sibling's propagation failure")
	}
	dns.mu.Lock()
	defer dns.mu.Unlock()
	if dns.records["_acme-challenge.a.example.test."][value] {
		t.Fatal("terminal authorization's owned TXT value was not cleaned")
	}
}

func TestDNS01InProcessSiblingFailureDoesNotCancelAcceptInFlight(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	ca.challengeGate, ca.challengeSeen = gate, make(chan struct{})
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	failed := make(chan struct{})
	m.provider = &hookedAuthorizationProvider{dnsChallengeProvider: m.provider, wait: func(ctx context.Context, owner string, lease dnsChallengeLease) error {
		if owner == "_acme-challenge.b.example.test" {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ca.challengeSeen:
				close(failed)
				return errors.New("injected propagation failure")
			}
		}
		return lease.Wait(ctx)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"a.example.test", "b.example.test"}, dnsECDSA)
		result <- err
	}()
	select {
	case <-failed:
	case <-ctx.Done():
		t.Fatal("sibling did not fail while the acceptance request was in flight")
	}
	release()
	select {
	case err := <-result:
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("order did not report the sibling failure promptly: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelling the in-flight acceptance stranded the authorization")
	}
	ca.mu.Lock()
	validations := ca.validations
	ca.mu.Unlock()
	if validations != 1 {
		t.Fatalf("in-flight challenge acceptances=%d, want 1", validations)
	}
}

func TestDNS01InProcessSiblingFailureDoesNotCancelPendingChallenge(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	started, failed, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m.provider = &hookedAuthorizationProvider{dnsChallengeProvider: m.provider, wait: func(ctx context.Context, owner string, lease dnsChallengeLease) error {
		if owner == "_acme-challenge.b.example.test" {
			close(started)
			select {
			case <-ctx.Done():
				close(cancelled)
				return ctx.Err()
			case <-gate:
				return lease.Wait(ctx)
			}
		}
		<-started
		close(failed)
		return errors.New("injected sibling failure")
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"a.example.test", "b.example.test"}, dnsECDSA)
		result <- err
	}()
	select {
	case <-failed:
	case <-ctx.Done():
		t.Fatal("sibling did not fail")
	}
	select {
	case <-cancelled:
		t.Fatal("a sibling failure cancelled the still-pending challenge")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-result; err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("order did not report the sibling failure after independent progress: %v", err)
	}
	ca.mu.Lock()
	validations := ca.validations
	ca.mu.Unlock()
	if validations != 1 {
		t.Fatalf("independent challenge validations=%d, want 1", validations)
	}
}

func TestDNS01InProcessReconcilesAmbiguousAcceptance(t *testing.T) {
	for _, tt := range []struct {
		name        string
		afterSend   bool
		passAccept  bool
		failObserve bool
		attempts    int32
	}{
		{name: "request lost", attempts: 2},
		{name: "response lost", afterSend: true, attempts: 1},
		{name: "response and observation lost", afterSend: true, failObserve: true, attempts: 1},
		{name: "authorization polling recovers", passAccept: true, failObserve: true, attempts: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dns, provider := startRFC2136Server(t)
			ca := startTestDNSACMECA(t, dns, "")
			m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
			limited := m.client.HTTPClient.Transport.(*dnsACMELimitedTransport)
			flaky := &droppedAcceptanceTransport{
				next: limited.next, afterSend: tt.afterSend, passAccept: tt.passAccept, failObserve: tt.failObserve,
			}
			limited.next = flaky
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			if err := m.ensureAccountKey(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := m.issueCertificate(ctx, []string{"example.test"}, dnsECDSA); err != nil {
				t.Fatalf("ambiguous acceptance did not recover: %v", err)
			}
			ca.mu.Lock()
			validations := ca.validations
			ca.mu.Unlock()
			if flaky.attempts.Load() != tt.attempts || validations != 1 {
				t.Fatalf("attempts=%d validations=%d, want %d and 1", flaky.attempts.Load(), validations, tt.attempts)
			}
			dns.mu.Lock()
			defer dns.mu.Unlock()
			if len(dns.records[testOwner]) != 0 {
				t.Fatalf("challenge records remain: %v", dns.records[testOwner])
			}
		})
	}
}

func TestDNS01InProcessPermanentAuthorizationErrorsFailPromptly(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	if err := m.ensureAccountKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(context.Background()); err != nil {
		t.Fatal(err)
	}
	initial := errors.New("injected ambiguous acceptance")
	for _, mode := range []string{"wait", "reconcile"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			url := ca.server.URL + "/authz/missing"
			var err error
			if mode == "wait" {
				_, err = m.waitDNSAuthorization(ctx, url, initial, time.Time{})
			} else {
				_, err = m.reconcileDNSAcceptance(ctx, url, &acme.Challenge{}, initial, time.Time{}, time.Time{})
			}
			var acmeErr *acme.Error
			if !errors.As(err, &acmeErr) || acmeErr.StatusCode != http.StatusNotFound ||
				!errors.Is(err, initial) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("permanent authorization error was not returned promptly: %v", err)
			}
		})
	}
}

func TestDNS01InProcessTransientAuthorizationHTTPErrorRecovers(t *testing.T) {
	for _, mode := range []string{"wait", "reconcile"} {
		t.Run(mode, func(t *testing.T) {
			dns, provider := startRFC2136Server(t)
			ca := startTestDNSACMECA(t, dns, "")
			m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.ensureAccountKey(ctx); err != nil {
				t.Fatal(err)
			}
			if err := m.ensureRegistered(ctx); err != nil {
				t.Fatal(err)
			}
			order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
			if err != nil {
				t.Fatal(err)
			}
			id := strings.TrimPrefix(order.AuthzURLs[0], ca.server.URL+"/authz/")
			ca.mu.Lock()
			ca.auths[id].valid = true
			ca.authFailures = 1
			ca.authStatus = http.StatusRequestTimeout
			ca.mu.Unlock()
			if mode == "wait" {
				_, err = m.waitDNSAuthorization(ctx, order.AuthzURLs[0], nil, time.Time{})
			} else {
				_, err = m.reconcileDNSAcceptance(ctx, order.AuthzURLs[0], &acme.Challenge{}, errors.New("acceptance response lost"), time.Time{}, time.Time{})
			}
			if err != nil {
				t.Fatalf("transient authorization HTTP error did not recover: %v", err)
			}
		})
	}
}

func TestDNS01InProcessAuthorizationPollingHonorsRetryAfter(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value func() string
	}{
		{"seconds", func() string { return "2" }},
		{"date", func() string { return time.Now().Add(4 * time.Second).UTC().Format(http.TimeFormat) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dns, provider := startRFC2136Server(t)
			ca := startTestDNSACMECA(t, dns, "")
			m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			if err := m.ensureAccountKey(ctx); err != nil {
				t.Fatal(err)
			}
			if err := m.ensureRegistered(ctx); err != nil {
				t.Fatal(err)
			}
			order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
			if err != nil {
				t.Fatal(err)
			}
			id := strings.TrimPrefix(order.AuthzURLs[0], ca.server.URL+"/authz/")
			ca.mu.Lock()
			ca.authRetryAfter = tt.value()
			ca.mu.Unlock()
			result := make(chan error, 1)
			go func() {
				_, err := m.waitDNSAuthorization(ctx, order.AuthzURLs[0], nil, time.Time{})
				result <- err
			}()
			waitForDNSACMETest(t, "the initial authorization observation", func() bool {
				ca.mu.Lock()
				defer ca.mu.Unlock()
				return ca.authReads == 1
			})
			ca.mu.Lock()
			ca.auths[id].valid = true
			ca.mu.Unlock()
			select {
			case err := <-result:
				t.Fatalf("authorization was polled before Retry-After: %v", err)
			case <-time.After(1500 * time.Millisecond):
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("authorization did not recover after Retry-After: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("authorization did not resume after Retry-After")
			}
		})
	}
}

func TestDNSAuthorizationPollingBoundsUntrustedRetryAfter(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   time.Duration
		valid  bool
	}{
		{"18446744073709551616", maxDNSRetryDelay, true},
		{"999999999999", maxDNSRetryDelay, true},
		{"3600", time.Hour, true},
		{"0", time.Second, true},
		{"not-a-date", 0, false},
		{"-1", 0, false},
		{time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), time.Second, true},
	} {
		if got, valid := dnsAuthorizationPollDelay(tt.header); got != tt.want || valid != tt.valid {
			t.Errorf("Retry-After %q: delay %v, valid %t; want %v, %t", tt.header, got, valid, tt.want, tt.valid)
		}
	}
}

func TestDNSAuthorizationPollingJitterFitsDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	delay := time.Hour - time.Second
	for range 1000 {
		start := time.Now()
		next := nextDNSAuthorizationPoll(ctx, delay)
		if next.Before(start.Add(delay)) || !next.Before(deadline) {
			t.Fatalf("next poll %v must respect delay %v and deadline %v", next, delay, deadline)
		}
	}
}

func TestDNS01InProcessTerminalAuthorizationAtDiscovery(t *testing.T) {
	for _, status := range []string{"invalid", "deactivated", "expired", "revoked"} {
		t.Run(status, func(t *testing.T) {
			dns, provider := startRFC2136Server(t)
			ca := startTestDNSACMECA(t, dns, "")
			m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.ensureAccountKey(ctx); err != nil {
				t.Fatal(err)
			}
			if err := m.ensureRegistered(ctx); err != nil {
				t.Fatal(err)
			}
			order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
			if err != nil {
				t.Fatal(err)
			}
			id := strings.TrimPrefix(order.AuthzURLs[0], ca.server.URL+"/authz/")
			ca.mu.Lock()
			ca.auths[id].status = status
			ca.mu.Unlock()
			_, err = m.authorizeDNSOrder(ctx, &dnsPendingOrder{order: order}, []string{"example.test"})
			var authErr *acme.AuthorizationError
			if !errors.As(err, &authErr) || authErr.URI != order.AuthzURLs[0] || authErr.Identifier != "example.test" {
				t.Fatalf("terminal authorization was not reported with its identity: %v", err)
			}
			if len(authErr.Errors) != 1 {
				t.Fatalf("terminal authorization lost its reason: %v", authErr)
			}
			if status == "invalid" {
				if _, ok := authErr.Errors[0].(*acme.Error); !ok {
					t.Fatalf("invalid authorization challenge error = %T", authErr.Errors[0])
				}
			}
		})
	}
}

func TestDNS01InProcessDiscoveryRetryAfterAppliesToFirstPoll(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	ca.mu.Lock()
	ca.authRetryAfter = "2"
	ca.mu.Unlock()
	result := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"example.test"}, dnsECDSA)
		result <- err
	}()
	waitForDNSACMETest(t, "challenge acceptance", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		return ca.validations == 1
	})
	select {
	case err := <-result:
		t.Fatalf("authorization was polled before discovery Retry-After: %v", err)
	case <-time.After(1500 * time.Millisecond):
	}
	ca.mu.Lock()
	reads := ca.authReads
	ca.mu.Unlock()
	if reads != 1 {
		t.Fatalf("authorization was read %d times before Retry-After, want 1", reads)
	}
	if err := <-result; err != nil {
		t.Fatalf("authorization did not finish after Retry-After: %v", err)
	}
}

func TestDNS01InProcessReconciliationCarriesRetryAfter(t *testing.T) {
	for _, mode := range []string{"processing", "failed acceptance retry"} {
		t.Run(mode, func(t *testing.T) {
			dns, provider := startRFC2136Server(t)
			ca := startTestDNSACMECA(t, dns, "")
			m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			if err := m.ensureAccountKey(ctx); err != nil {
				t.Fatal(err)
			}
			if err := m.ensureRegistered(ctx); err != nil {
				t.Fatal(err)
			}
			order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
			if err != nil {
				t.Fatal(err)
			}
			url := order.AuthzURLs[0]
			id := strings.TrimPrefix(url, ca.server.URL+"/authz/")
			challenge := &acme.Challenge{Type: "dns-01", URI: ca.server.URL + "/challenge/" + id, Token: "token-" + id}
			if mode == "failed acceptance retry" {
				value, err := m.client.DNS01ChallengeRecord(challenge.Token)
				if err != nil {
					t.Fatal(err)
				}
				lease, err := m.provider.Present(ctx, "_acme-challenge.example.test", value)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := m.cleanupDNSLease(lease, 5*time.Second); err != nil {
						t.Errorf("cleanup challenge: %v", err)
					}
				}()
				limited := m.client.HTTPClient.Transport.(*dnsACMELimitedTransport)
				limited.next = &droppedAcceptanceTransport{next: limited.next, afterSend: true}
			}
			ca.mu.Lock()
			ca.authRetryAfter = "2"
			if mode == "processing" {
				ca.auths[id].accepted = true
			}
			ca.mu.Unlock()
			result := make(chan error, 1)
			go func() {
				_, err := m.reconcileDNSAcceptance(ctx, url, challenge, errors.New("acceptance response lost"), time.Time{}, time.Time{})
				result <- err
			}()
			waitForDNSACMETest(t, "reconciliation observation", func() bool {
				ca.mu.Lock()
				defer ca.mu.Unlock()
				return ca.authReads == 1
			})
			observed := time.Now()
			if mode == "processing" {
				ca.mu.Lock()
				ca.auths[id].valid = true
				ca.mu.Unlock()
			} else {
				waitForDNSACMETest(t, "retried acceptance", func() bool {
					ca.mu.Lock()
					defer ca.mu.Unlock()
					return ca.validations == 1
				})
				if time.Since(observed) > 1500*time.Millisecond {
					t.Fatal("authorization polling hint delayed the separate challenge request")
				}
			}
			select {
			case err := <-result:
				t.Fatalf("authorization was repolled before Retry-After: %v", err)
			case <-time.After(1500 * time.Millisecond):
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("reconciliation did not finish: %v", err)
				}
			case <-time.After(1500 * time.Millisecond):
				t.Fatal("reconciliation waited the same Retry-After twice")
			}
		})
	}
}

func TestDNS01InProcessValidAuthorizationReusedDuringBlockedCleanup(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "shared-authorization")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	blocked := &blockingCleanupProvider{
		dnsChallengeProvider: m.provider, entered: make(chan struct{}), release: make(chan struct{}),
	}
	m.provider = blocked
	release := sync.OnceFunc(func() { close(blocked.release) })
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"example.test"}, dnsECDSA)
		first <- err
	}()
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal("first order did not reach cleanup")
	}
	second := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"example.test"}, dnsRSA)
		second <- err
	}()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("shared valid authorization was blocked by cleanup: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shared valid authorization waited for cleanup")
	}
	select {
	case err := <-first:
		t.Fatalf("cleanup unexpectedly finished: %v", err)
	default:
	}
	ca.mu.Lock()
	var sharedURL string
	for id := range ca.auths {
		sharedURL = ca.server.URL + "/authz/" + id
	}
	attempts, validations := ca.authAttempts, ca.validations
	ca.mu.Unlock()
	if attempts != 2 || validations != 1 {
		t.Fatalf("shared authorization attempts=%d validations=%d, want 2 and 1", attempts, validations)
	}
	_, err := m.authorizeDNSOrder(ctx, &dnsPendingOrder{
		order: &acme.Order{AuthzURLs: []string{sharedURL}},
	}, []string{"other.example.test"})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("shared authorization accepted a different identity: %v", err)
	}
	release()
	if err := <-first; err != nil {
		t.Fatalf("first order failed after cleanup: %v", err)
	}
}

func TestDNS01InProcessExpiredOrderAuthorizationIsRediscovered(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	url := order.AuthzURLs[0]
	id := strings.TrimPrefix(url, ca.server.URL+"/authz/")
	expires := time.Now().Add(time.Hour)
	ca.mu.Lock()
	ca.auths[id].valid = true
	ca.auths[id].expires = expires
	ca.mu.Unlock()
	current := &dnsPendingOrder{
		order: order, authorizations: map[string]time.Time{url: time.Now().Add(-time.Second)},
	}
	if _, err := m.authorizeDNSOrder(ctx, current, []string{"example.test"}); err != nil {
		t.Fatalf("expired order authorization was not rediscovered: %v", err)
	}
	ca.mu.Lock()
	reads := ca.authReads
	ca.mu.Unlock()
	if reads != 1 || !current.authorizations[url].After(time.Now()) {
		t.Fatalf("rediscovery reads=%d retained expiry=%v", reads, current.authorizations[url])
	}
}

func TestDNS01InProcessDiscoveryRetryAfterSharedAcrossOrders(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "shared-authorization")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	ca.mu.Lock()
	ca.authFailures = 1
	ca.authStatus = http.StatusRequestTimeout
	ca.authRetryAfter = "2"
	ca.mu.Unlock()
	names := []string{"example.test"}
	for _, algorithm := range []dnsKeyAlgorithm{dnsECDSA, dnsRSA} {
		if _, err := m.issueCertificate(ctx, names, algorithm); !errors.Is(err, errDNSAuthorizationBusy) {
			t.Fatalf("%s order did not defer discovery: %v", algorithm, err)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	for _, algorithm := range []dnsKeyAlgorithm{dnsECDSA, dnsRSA} {
		if _, err := m.issueCertificate(ctx, names, algorithm); !errors.Is(err, errDNSAuthorizationBusy) {
			t.Fatalf("%s order retried before Retry-After: %v", algorithm, err)
		}
	}
	ca.mu.Lock()
	attempts, orders := ca.authAttempts, len(ca.orders)
	ca.authRetryAfter = ""
	ca.mu.Unlock()
	if attempts != 1 || orders != 2 {
		t.Fatalf("discovery attempts=%d orders=%d, want 1 and 2", attempts, orders)
	}
	for {
		_, err := m.issueCertificate(ctx, names, dnsECDSA)
		if err == nil {
			break
		}
		if !errors.Is(err, errDNSAuthorizationBusy) {
			t.Fatalf("deferred order did not recover: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("deferred order did not resume")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if _, err := m.issueCertificate(ctx, names, dnsRSA); err != nil {
		t.Fatalf("second shared order did not recover: %v", err)
	}
	ca.mu.Lock()
	orders = len(ca.orders)
	ca.mu.Unlock()
	if orders != 2 {
		t.Fatalf("transient discovery failures created %d orders, want 2", orders)
	}
}

func TestDNS01InProcessDiscoveryEgressBlockFailsPromptly(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	limited := m.client.HTTPClient.Transport.(*dnsACMELimitedTransport)
	limited.next = &blockedAuthorizationTransport{next: limited.next}
	_, err := m.issueCertificate(ctx, []string{"example.test"}, dnsECDSA)
	if !errors.Is(err, egress.ErrBlocked) || errors.Is(err, errDNSAuthorizationBusy) {
		t.Fatalf("blocked ACME discovery was not surfaced as a failure: %v", err)
	}
	m.orderMu.Lock()
	orders := len(m.orders)
	m.orderMu.Unlock()
	if orders != 0 {
		t.Fatalf("permanently blocked order was retained: %d", orders)
	}
}

func TestDNS01InProcessChallengeRetryAfterIsSeparateFromPolling(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	url := order.AuthzURLs[0]
	id := strings.TrimPrefix(url, ca.server.URL+"/authz/")
	challenge := &acme.Challenge{Type: "dns-01", URI: ca.server.URL + "/challenge/" + id, Token: "token-" + id}
	value, err := m.client.DNS01ChallengeRecord(challenge.Token)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := m.provider.Present(ctx, "_acme-challenge.example.test", value)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := m.cleanupDNSLease(lease, 5*time.Second); err != nil {
			t.Errorf("cleanup challenge: %v", err)
		}
	}()
	ca.mu.Lock()
	ca.challengeFailures = 1
	ca.challengeStatus = http.StatusRequestTimeout
	ca.challengeRetryAfter = "2"
	ca.authRetryAfter = "5"
	ca.mu.Unlock()
	started := time.Now()
	nextAccept, err := m.acceptDNSChallenge(ctx, url, challenge)
	if err == nil || nextAccept.IsZero() {
		t.Fatalf("challenge failure did not retain Retry-After: next=%v err=%v", nextAccept, err)
	}
	result := make(chan error, 1)
	go func() {
		_, reconcileErr := m.reconcileDNSAcceptance(ctx, url, challenge, err, time.Time{}, nextAccept)
		result <- reconcileErr
	}()
	waitForDNSACMETest(t, "the reconciliation authorization observation", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		return ca.authReads == 1
	})
	time.Sleep(1500 * time.Millisecond)
	ca.mu.Lock()
	attempts := ca.challengeAttempts
	ca.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("challenge retried before its Retry-After: %d attempts", attempts)
	}
	waitForDNSACMETest(t, "the challenge retry", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		return ca.validations == 1
	})
	elapsed := time.Since(started)
	if elapsed < 2*time.Second || elapsed > 4*time.Second {
		t.Fatalf("challenge retry took %v; want its 2s hint, not the authorization's 5s hint", elapsed)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("authorization did not finish: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("authorization did not finish after its polling hint")
	}
}

func TestDNS01InProcessChallengeRetryAfterSurvivesOrderCancellation(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "shared-authorization")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	ca.mu.Lock()
	ca.challengeFailures = 1
	ca.challengeStatus = http.StatusRequestTimeout
	ca.challengeRetryAfter = "4"
	ca.mu.Unlock()
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	first := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := m.issueCertificate(firstCtx, []string{"example.test"}, dnsECDSA)
		first <- err
	}()
	waitForDNSACMETest(t, "the failed challenge and reconciliation observation", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		return ca.challengeAttempts == 1 && ca.authReads >= 2
	})
	cancelFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled order did not stop: %v", err)
	}
	second := make(chan error, 1)
	go func() {
		for {
			_, err := m.issueCertificate(ctx, []string{"example.test"}, dnsRSA)
			if !errors.Is(err, errDNSAuthorizationBusy) {
				second <- err
				return
			}
			select {
			case <-ctx.Done():
				second <- ctx.Err()
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	timer := time.NewTimer(max(0, time.Until(started.Add(3500*time.Millisecond))))
	defer timer.Stop()
	select {
	case err := <-second:
		t.Fatalf("another order completed before the challenge retry deadline: %v", err)
	case <-timer.C:
	}
	ca.mu.Lock()
	attempts := ca.challengeAttempts
	ca.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("another order retried the challenge before Retry-After: %d attempts", attempts)
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("another order did not resume after Retry-After: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("another order did not resume after the challenge retry deadline")
	}
}

func TestDNS01InProcessElapsedRetryAfterIsNotAppliedTwice(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	limited := m.client.HTTPClient.Transport.(*dnsACMELimitedTransport)
	flaky := &staleRetryAfterTransport{next: limited.next}
	limited.next = flaky
	m.client.RetryBackoff = func(int, *http.Request, *http.Response) time.Duration { return 2100 * time.Millisecond }
	started := time.Now()
	_, next, err := m.observeDNSAuthorization(ctx, order.AuthzURLs[0])
	if err == nil || flaky.attempts.Load() != 2 || time.Since(started) < 2*time.Second {
		t.Fatalf("internal retry did not encounter the expected network failure: attempts=%d err=%v", flaky.attempts.Load(), err)
	}
	if remaining := time.Until(next); remaining > 1500*time.Millisecond {
		t.Fatalf("elapsed Retry-After was applied again: next attempt in %v", remaining)
	}
}

func TestDNS01InProcessBoundsOversizedAuthorizationRetryAfter(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	if err := m.ensureAccountKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(context.Background()); err != nil {
		t.Fatal(err)
	}
	order, err := m.client.AuthorizeOrder(context.Background(), acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	ca.mu.Lock()
	ca.authFailures = 100
	ca.authStatus = http.StatusTooManyRequests
	ca.authRetryAfter = "18446744073709551616"
	ca.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, next, err := m.observeDNSAuthorization(ctx, order.AuthzURLs[0])
	ca.mu.Lock()
	attempts := ca.authAttempts
	ca.mu.Unlock()
	if err == nil || attempts != 1 || time.Until(next) < time.Hour {
		t.Fatalf("oversized Retry-After was not bounded: attempts=%d next=%v err=%v", attempts, next, err)
	}
}

func TestDNS01InProcessRetriesServerErrorWithoutHint(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	ca.mu.Lock()
	ca.authFailures = 1
	ca.authStatus = http.StatusServiceUnavailable
	ca.auths[strings.TrimPrefix(order.AuthzURLs[0], ca.server.URL+"/authz/")].valid = true
	ca.mu.Unlock()
	started := time.Now()
	_, err = m.waitDNSAuthorization(ctx, order.AuthzURLs[0], nil, time.Time{})
	ca.mu.Lock()
	attempts := ca.authAttempts
	ca.mu.Unlock()
	if err != nil || attempts != 2 || time.Since(started) < time.Second {
		t.Fatalf("server error retry was not bounded: attempts=%d elapsed=%v err=%v", attempts, time.Since(started), err)
	}
}

func TestDNS01InProcessPollsWhileChallengeRetryIsDeferred(t *testing.T) {
	for _, mode := range []string{"handoff", "throttled-acceptance"} {
		t.Run(mode, func(t *testing.T) {
			dns, provider := startRFC2136Server(t)
			ca := startTestDNSACMECA(t, dns, "")
			m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			if err := m.ensureAccountKey(ctx); err != nil {
				t.Fatal(err)
			}
			if err := m.ensureRegistered(ctx); err != nil {
				t.Fatal(err)
			}
			order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
			if err != nil {
				t.Fatal(err)
			}
			url := order.AuthzURLs[0]
			unlock, _, err := m.lockAuthorization(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			id := strings.TrimPrefix(url, ca.server.URL+"/authz/")
			challenge := &acme.Challenge{Type: "dns-01", Status: acme.StatusPending, URI: ca.server.URL + "/challenge/" + id, Token: "token-" + id}
			value, err := m.client.DNS01ChallengeRecord(challenge.Token)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := m.provider.Present(ctx, "_acme-challenge.example.test", value)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := m.cleanupDNSLease(lease, 5*time.Second); err != nil {
					t.Errorf("cleanup challenge: %v", err)
				}
			}()
			pending := &dnsPendingAuthorization{url: url, challenge: challenge, lease: lease}
			expectedAttempts := 0
			if mode == "handoff" {
				pending.nextAccept = time.Now().Add(4 * time.Second)
			} else {
				expectedAttempts = 1
				ca.mu.Lock()
				ca.challengeFailures = 1
				ca.challengeStatus = http.StatusTooManyRequests
				ca.challengeRetryAfter = "4"
				ca.mu.Unlock()
			}
			result := make(chan error, 1)
			go func() {
				auth, err := m.completeDNSAuthorization(ctx, url, pending)
				if err == nil && auth.Status != acme.StatusValid {
					err = errors.New("authorization was not valid")
				}
				result <- err
			}()
			waitForDNSACMETest(t, "an authorization poll during the challenge retry delay", func() bool {
				ca.mu.Lock()
				defer ca.mu.Unlock()
				return ca.authReads >= 1 && ca.challengeAttempts == expectedAttempts
			})
			ca.mu.Lock()
			ca.auths[id].valid = true
			ca.mu.Unlock()
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("valid authorization was not observed: %v", err)
				}
			case <-time.After(2500 * time.Millisecond):
				cancel()
				<-result
				t.Fatal("authorization polling was blocked by the challenge retry deadline")
			}
			ca.mu.Lock()
			attempts := ca.challengeAttempts
			ca.mu.Unlock()
			if attempts != expectedAttempts {
				t.Fatalf("retried a challenge after authorization became valid: %d attempts", attempts)
			}
		})
	}
}

func TestDNS01InProcessChallengeRejectionRetriesBeforeAuthorizationPoll(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	ca.mu.Lock()
	ca.authRetryAfter = "4"
	ca.challengeFailures = 1
	ca.challengeStatus = http.StatusTooManyRequests
	ca.challengeRetryAfter = "1"
	ca.mu.Unlock()
	result := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"example.test"}, dnsECDSA)
		result <- err
	}()
	waitForDNSACMETest(t, "a challenge retry before the authorization poll", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		return ca.validations == 1
	})
	ca.mu.Lock()
	attempts, reads := ca.challengeAttempts, ca.authReads
	ca.mu.Unlock()
	if attempts != 2 || reads != 1 {
		t.Fatalf("challenge retry was delayed by authorization polling: attempts=%d auth reads=%d", attempts, reads)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("order did not finish after the independent retry: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("order did not finish after the independent retry")
	}
}

func TestDNS01InProcessLongAuthorizationRetrySurvivesOrderDeadline(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	url := order.AuthzURLs[0]
	unlock, _, err := m.lockAuthorization(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	ca.mu.Lock()
	ca.authRetryAfter = "3600"
	ca.mu.Unlock()
	_, next, err := m.observeDNSAuthorization(ctx, url)
	unlock()
	cancel()
	if err != nil || time.Until(next) < 59*time.Minute {
		t.Fatalf("long retry deadline was shortened: next=%v err=%v", next, err)
	}
	if _, _, err := m.lockAuthorization(context.Background(), url); !errors.Is(err, errDNSAuthorizationBusy) {
		t.Fatalf("another order ignored the retained deadline: %v", err)
	}
}

func TestDNS01InProcessDoesNotRetryDisappearedChallenge(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	url := order.AuthzURLs[0]
	id := strings.TrimPrefix(url, ca.server.URL+"/authz/")
	challenge := &acme.Challenge{Type: "dns-01", Status: acme.StatusPending, URI: ca.server.URL + "/challenge/" + id, Token: "token-" + id}
	retryAt := time.Now().Add(3 * time.Second)
	result := make(chan error, 1)
	go func() {
		_, err := m.reconcileDNSAcceptance(ctx, url, challenge, errDNSChallengeRetryDeferred, time.Time{}, retryAt)
		result <- err
	}()
	waitForDNSACMETest(t, "the initial challenge observation", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		return ca.authReads >= 1
	})
	ca.mu.Lock()
	ca.omitChallenge = "example.test"
	ca.mu.Unlock()
	waitForDNSACMETest(t, "the missing challenge observation", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		return ca.authReads >= 2
	})
	timer := time.NewTimer(max(0, time.Until(retryAt.Add(250*time.Millisecond))))
	<-timer.C
	ca.mu.Lock()
	attempts := ca.challengeAttempts
	ca.mu.Unlock()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("reconciliation did not stop on cancellation: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("retried a challenge after it disappeared: %d attempts", attempts)
	}
}

func TestDNS01InProcessTransientObservationBackoff(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	url := order.AuthzURLs[0]
	unlock, _, err := m.lockAuthorization(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ca.mu.Lock()
	ca.authFailures = 3
	ca.authStatus = http.StatusRequestTimeout
	ca.authRetryAfter = "invalid"
	ca.mu.Unlock()
	for _, base := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		start := time.Now()
		_, next, err := m.observeDNSAuthorization(ctx, url)
		delay := next.Sub(start)
		if err == nil || delay < base || delay > base+time.Second {
			t.Fatalf("transient observation backoff = %v, want at least %v: %v", delay, base, err)
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ca.server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := &http.Response{Header: http.Header{"Retry-After": []string{"invalid"}}}
	if delay := dnsACMERetryBackoff(4, req, response); delay < 8*time.Second || delay > 11*time.Second {
		t.Fatalf("invalid retry hint did not use exponential backoff: %v", delay)
	}
}

func TestDNS01InProcessOrderPollingBoundsMalformedRetryAfter(t *testing.T) {
	for _, hint := range []string{"9999999999", "-1"} {
		t.Run(hint, func(t *testing.T) {
			dns, provider := startRFC2136Server(t)
			ca := startTestDNSACMECA(t, dns, "")
			m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := m.ensureAccountKey(ctx); err != nil {
				t.Fatal(err)
			}
			if err := m.ensureRegistered(ctx); err != nil {
				t.Fatal(err)
			}
			order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
			if err != nil {
				t.Fatal(err)
			}
			ca.mu.Lock()
			ca.orderRetryAfter = hint
			ca.mu.Unlock()
			waitCtx, stop := context.WithTimeout(ctx, 300*time.Millisecond)
			defer stop()
			if _, err := m.client.WaitOrder(waitCtx, order.URI); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("pending order did not honor its context: %v", err)
			}
			ca.mu.Lock()
			reads := ca.orderReads
			ca.mu.Unlock()
			if reads != 1 {
				t.Fatalf("order polled %d times before its retry deadline", reads)
			}
		})
	}
}

func TestDNS01InProcessCompletedChallengeReleasesRetryState(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	url := order.AuthzURLs[0]
	unlock, _, err := m.lockAuthorization(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(url, ca.server.URL+"/authz/")
	challenge := &acme.Challenge{Type: "dns-01", URI: ca.server.URL + "/challenge/" + id, Token: "token-" + id}
	value, err := m.client.DNS01ChallengeRecord(challenge.Token)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := m.provider.Present(ctx, "_acme-challenge.example.test", value)
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	defer func() {
		if !cleaned {
			if err := m.cleanupDNSLease(lease, 5*time.Second); err != nil {
				t.Errorf("cleanup challenge: %v", err)
			}
		}
	}()
	ca.mu.Lock()
	ca.challengeRetryAfter = "3600"
	ca.mu.Unlock()
	next, err := m.acceptDNSChallenge(ctx, url, challenge)
	if err != nil || !next.IsZero() {
		t.Fatalf("completed challenge retained a retry deadline: next=%v err=%v", next, err)
	}
	m.authMu.Lock()
	m.authStates[url].nextPoll = time.Now().Add(time.Hour)
	m.authStates[url].nextAccept = time.Now().Add(time.Hour)
	m.authMu.Unlock()
	m.rememberValidDNSAuthorization(url, "example.test", time.Now().Add(time.Hour))
	if err := m.cleanupDNSLease(lease, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	cleaned = true
	unlock()
	m.authMu.Lock()
	_, retained := m.authStates[url]
	m.authMu.Unlock()
	if retained {
		t.Fatal("completed challenge occupied authorization capacity after cleanup")
	}
}

func TestDNS01InProcessBusyOrderDoesNotRefetchAuthorizations(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "shared-authorization")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	blocked := &blockingAuthorizationProvider{
		dnsChallengeProvider: m.provider, entered: make(chan struct{}), release: make(chan struct{}),
	}
	m.provider = blocked
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() {
		select {
		case <-blocked.release:
		default:
			close(blocked.release)
		}
	}()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.authorizeDNSOrder(ctx, &dnsPendingOrder{order: first}, []string{"example.test"})
		result <- err
	}()
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal("first authorization did not start")
	}
	names := []string{"example.test", "b.example.test"}
	second, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		t.Fatal(err)
	}
	current := &dnsPendingOrder{order: second}
	for attempt := 0; attempt < 4; attempt++ {
		if _, err := m.authorizeDNSOrder(ctx, current, names); !errors.Is(err, errDNSAuthorizationBusy) {
			t.Fatalf("attempt %d did not defer the shared authorization: %v", attempt, err)
		}
		ca.mu.Lock()
		shared := ca.auths[strings.TrimPrefix(second.AuthzURLs[0], ca.server.URL+"/authz/")]
		independent := ca.auths[strings.TrimPrefix(second.AuthzURLs[1], ca.server.URL+"/authz/")]
		sharedReads, independentReads, independentValid := shared.reads, independent.reads, independent.valid
		ca.mu.Unlock()
		if sharedReads != 1 || independentReads != 2 || !independentValid {
			t.Fatalf("attempt %d: shared reads=%d independent reads=%d valid=%v", attempt, sharedReads, independentReads, independentValid)
		}
	}
	close(blocked.release)
	if err := <-result; err != nil {
		t.Fatalf("first authorization failed: %v", err)
	}
	if _, err := m.authorizeDNSOrder(ctx, current, names); err != nil {
		t.Fatalf("deferred order did not finish: %v", err)
	}
}

func TestDNS01InProcessFailedPreparationReleasesItsAuthorizationLock(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "processing-validation")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	names := []string{"a.example.test", "b.example.test"}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		t.Fatal(err)
	}
	ca.mu.Lock()
	ca.omitChallenge = names[0]
	id := strings.TrimPrefix(order.AuthzURLs[1], ca.server.URL+"/authz/")
	ca.auths[id].accepted = true
	ca.mu.Unlock()
	waiting := make(chan struct{})
	m.provider = testDNSChallengeProvider{lease: &signallingAuthorizationLease{
		dnsChallengeLease: &testDNSChallengeLease{}, waiting: waiting,
	}}
	result := make(chan error, 1)
	go func() {
		_, err := m.authorizeDNSOrder(ctx, &dnsPendingOrder{order: order}, names)
		result <- err
	}()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("sibling authorization did not reach its waiter")
	}
	m.authMu.Lock()
	failed, processing := m.authStates[order.AuthzURLs[0]], m.authStates[order.AuthzURLs[1]]
	failedHeld, processingHeld := failed != nil && failed.locked, processing != nil && processing.locked
	m.authMu.Unlock()
	if failedHeld || !processingHeld {
		t.Fatalf("authorization locks: failed=%v processing=%v", failedHeld, processingHeld)
	}
	cancel()
	if err := <-result; err == nil {
		t.Fatal("failed preparation was not reported")
	}
}

func TestDNS01InProcessValidProcessingAuthorizationClearsPresentationFailure(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "processing-validation")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(order.AuthzURLs[0], ca.server.URL+"/authz/")
	ca.mu.Lock()
	ca.auths[id].accepted = true
	ca.mu.Unlock()
	lease := &testDNSChallengeLease{}
	waiting := make(chan struct{})
	m.provider = testDNSChallengeProvider{
		lease: &signallingAuthorizationLease{dnsChallengeLease: lease, waiting: waiting},
		err:   errors.New("injected presentation failure"),
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.authorizeDNSOrder(ctx, &dnsPendingOrder{order: order}, []string{"example.test"})
		result <- err
	}()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("processing authorization did not reach its waiter")
	}
	ca.mu.Lock()
	ca.auths[id].valid = true
	ca.mu.Unlock()
	if err := <-result; err != nil {
		t.Fatalf("valid authorization retained a presentation failure: %v", err)
	}
	if lease.cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", lease.cleanupCalls)
	}
}

func TestDNS01InProcessLostAcceptanceResponseWaitsForProcessing(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "processing-validation")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	limited := m.client.HTTPClient.Transport.(*dnsACMELimitedTransport)
	limited.next = &droppedAcceptanceTransport{next: limited.next, afterSend: true}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.issueCertificate(ctx, []string{"example.test"}, dnsECDSA)
		result <- err
	}()
	var authID string
	waitForDNSACMETest(t, "the ambiguous acceptance to be observed processing", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		for id, auth := range ca.auths {
			if auth.accepted && ca.processingReads > 0 {
				authID = id
				return true
			}
		}
		return false
	})
	dns.mu.Lock()
	recordCount := len(dns.records[testOwner])
	dns.mu.Unlock()
	if recordCount != 2 {
		t.Fatalf("processing challenge lost its TXT value or marker: %d records", recordCount)
	}
	ca.mu.Lock()
	ca.auths[authID].valid = true
	ca.mu.Unlock()
	if err := <-result; err != nil {
		t.Fatalf("valid authorization did not recover from the lost response: %v", err)
	}
	dns.mu.Lock()
	defer dns.mu.Unlock()
	if len(dns.records[testOwner]) != 0 {
		t.Fatalf("validated challenge was not cleaned: %v", dns.records[testOwner])
	}
}

func TestDNS01InProcessProcessingFailureOrCancellationCleansTXT(t *testing.T) {
	for _, action := range []string{"invalid", "deactivated", "expired", "revoked", "cancelled"} {
		t.Run(action, func(t *testing.T) {
			dns, provider := startRFC2136Server(t)
			ca := startTestDNSACMECA(t, dns, "processing-validation")
			m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			if err := m.ensureAccountKey(ctx); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_, err := m.issueCertificate(ctx, []string{"example.test"}, dnsECDSA)
				result <- err
			}()
			var authID string
			waitForDNSACMETest(t, "the challenge to begin processing", func() bool {
				ca.mu.Lock()
				defer ca.mu.Unlock()
				for id, auth := range ca.auths {
					if auth.accepted && ca.processingReads > 0 {
						authID = id
						return true
					}
				}
				return false
			})
			if action != "cancelled" {
				ca.mu.Lock()
				ca.auths[authID].status = action
				ca.mu.Unlock()
			} else {
				cancel()
			}
			var err error
			select {
			case err = <-result:
			case <-time.After(3 * time.Second):
				t.Fatal("terminal authorization did not release its challenge promptly")
			}
			var authErr *acme.AuthorizationError
			if action != "cancelled" && (!errors.As(err, &authErr) || errors.Is(err, context.DeadlineExceeded)) ||
				action == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("processing %s was not reported: %v", action, err)
			}
			if action == "invalid" {
				if len(authErr.Errors) != 1 {
					t.Fatalf("invalid authorization lost challenge error: %v", authErr)
				}
				if _, ok := authErr.Errors[0].(*acme.Error); !ok {
					t.Fatalf("invalid authorization challenge error = %T", authErr.Errors[0])
				}
			}
			dns.mu.Lock()
			defer dns.mu.Unlock()
			if len(dns.records[testOwner]) != 0 {
				t.Fatalf("processing %s left owned TXT: %v", action, dns.records[testOwner])
			}
		})
	}
}

func TestDNS01InProcessRecoversProcessingChallengeWithoutOwningExistingTXT(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "processing-validation")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs("example.test"))
	if err != nil {
		t.Fatal(err)
	}
	authID := strings.TrimPrefix(order.AuthzURLs[0], ca.server.URL+"/authz/")
	ca.mu.Lock()
	auth := ca.auths[authID]
	auth.accepted = true
	value, err := m.client.DNS01ChallengeRecord(auth.token)
	ca.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	const marker = "jul-dns01-external-owner"
	dns.mu.Lock()
	dns.records[testOwner] = map[string]bool{value: true, marker: true, "unrelated": true}
	dns.mu.Unlock()
	presented := make(chan struct{})
	var once sync.Once
	m.provider = &hookedAuthorizationProvider{dnsChallengeProvider: m.provider, presented: func(string) {
		once.Do(func() { close(presented) })
	}}
	result := make(chan error, 1)
	go func() {
		_, err := m.authorizeDNSOrder(ctx, &dnsPendingOrder{order: order}, []string{"example.test"})
		result <- err
	}()
	select {
	case <-presented:
	case <-ctx.Done():
		t.Fatal("processing authorization did not observe the existing TXT")
	}
	ca.mu.Lock()
	auth.valid = true
	ca.mu.Unlock()
	if err := <-result; err != nil {
		t.Fatalf("processing authorization did not recover: %v", err)
	}
	dns.mu.Lock()
	defer dns.mu.Unlock()
	if !dns.records[testOwner][value] || !dns.records[testOwner][marker] || !dns.records[testOwner]["unrelated"] {
		t.Fatalf("recovery modified another owner's records: %v", dns.records[testOwner])
	}
}

func TestDNS01InProcessConcurrentAuthorizationFailureCleansAllLeases(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	dns.mu.Lock()
	dns.records[testOwner] = map[string]bool{"unrelated": true}
	dns.mu.Unlock()
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	m.provider = &concurrentAuthorizationProvider{
		dnsChallengeProvider: m.provider, ready: make(chan struct{}), target: 6, failWait: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.ensureAccountKey(ctx); err != nil {
		t.Fatal(err)
	}
	names := canonicalDNSNames([]string{"*.example.test", "example.test", "a.example.test", "b.example.test", "c.example.test", "d.example.test"})
	if _, err := m.issueCertificate(ctx, names, dnsECDSA); err == nil {
		t.Fatal("injected propagation failure was accepted")
	}
	assertIntegratedDNSClean(t, dns)
	dns.mu.Lock()
	defer dns.mu.Unlock()
	for owner, values := range dns.records {
		if owner != testOwner && len(values) != 0 {
			t.Fatalf("challenge values remain at %s: %v", owner, values)
		}
	}
}

func TestDNS01InProcessAuthorizationCardinality(t *testing.T) {
	for _, tt := range []struct {
		fault       string
		validations int
	}{
		{"external-authorization", 0},
		{"extra-authorization", 2},
		{"duplicate-authorization-url", 1},
	} {
		t.Run(tt.fault, func(t *testing.T) {
			dns, provider := startRFC2136Server(t)
			dns.mu.Lock()
			dns.records[testOwner] = map[string]bool{"unrelated": true}
			dns.mu.Unlock()
			ca := startTestDNSACMECA(t, dns, tt.fault)
			m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.ensureAccountKey(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := m.issueCertificate(ctx, []string{"example.test"}, dnsECDSA); err != nil {
				t.Fatalf("order failed: %v", err)
			}
			assertIntegratedDNSClean(t, dns)
			ca.mu.Lock()
			defer ca.mu.Unlock()
			if ca.validations != tt.validations {
				t.Fatalf("validations=%d, want %d", ca.validations, tt.validations)
			}
		})
	}
}

func TestDNS01InProcessSharedAuthorizationAcrossVariants(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	dns.mu.Lock()
	dns.records[testOwner] = map[string]bool{"unrelated": true}
	dns.mu.Unlock()
	ca := startTestDNSACMECA(t, dns, "shared-authorization")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	serving := m.Provider([]string{"example.test"})
	for _, hello := range []*tls.ClientHelloInfo{dnsTestHello("example.test"), dnsTestRSAHello("example.test")} {
		if _, err := serving.GetCertificate(hello); err != nil {
			t.Fatal(err)
		}
	}
	assertIntegratedDNSClean(t, dns)
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if ca.registrations != 1 || len(ca.orders) != 2 || ca.validations != 1 {
		t.Fatalf("shared authorization: registrations=%d orders=%d validations=%d", ca.registrations, len(ca.orders), ca.validations)
	}
}

func TestDNS01InProcessBusyAuthorizationDoesNotStarveOtherOrders(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "shared-authorization")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	blocked := &blockingAuthorizationProvider{
		dnsChallengeProvider: m.provider, entered: make(chan struct{}), release: make(chan struct{}),
	}
	m.provider = blocked
	defer close(blocked.release)
	m.Provider([]string{"example.test"})
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("shared authorization did not start")
	}
	m.Provider([]string{"example.test", "b.example.test"})
	m.Provider([]string{"example.test", "c.example.test"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		ca.mu.Lock()
		count := len(ca.orders)
		ca.mu.Unlock()
		if count >= 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shared orders occupied the issuance workers: only %d orders started", count)
		}
		time.Sleep(10 * time.Millisecond)
	}
	other := m.Provider([]string{"other.example.test"})
	result := make(chan error, 1)
	go func() {
		_, err := other.GetCertificate(dnsTestHello("other.example.test"))
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("unrelated issuance failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shared authorizations starved an unrelated certificate")
	}
	time.Sleep(2 * dnsAuthRetry)
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if count := len(ca.orders); count > 8 {
		t.Fatalf("waiting for shared authorization created %d orders, want at most 8", count)
	}
}

func TestDNS01InProcessACMEConcurrentCertificateGroups(t *testing.T) {
	dns, provider := startRFC2136Server(t)
	ca := startTestDNSACMECA(t, dns, "")
	m := testIntegratedDNSManager(t, ca, provider, t.TempDir())
	const groups = 4
	results := make(chan error, groups)
	for i := 0; i < groups; i++ {
		name := fmt.Sprintf("group-%d.example.test", i)
		serving := m.Provider([]string{name})
		go func() {
			for _, hello := range []*tls.ClientHelloInfo{dnsTestHello(name), dnsTestRSAHello(name)} {
				if _, err := serving.GetCertificate(hello); err != nil {
					results <- err
					return
				}
			}
			results <- nil
		}()
	}
	for i := 0; i < groups; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("concurrent certificate groups did not finish")
		}
	}
	ca.mu.Lock()
	if ca.registrations != 1 || len(ca.orders) != 2*groups || ca.validations != 2*groups {
		t.Errorf("registrations=%d orders=%d validations=%d", ca.registrations, len(ca.orders), ca.validations)
	}
	ca.mu.Unlock()
	dns.mu.Lock()
	defer dns.mu.Unlock()
	for owner, records := range dns.records {
		if len(records) != 0 {
			t.Errorf("concurrent challenge owner %s was not cleaned: %v", owner, records)
		}
	}
}
