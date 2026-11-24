// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"jul/internal/config"
	"jul/internal/egress"
)

const (
	testTSIGName   = "jul.example.test."
	testTSIGSecret = "MDEyMzQ1Njc4OWFiY2RlZg=="
	testOwner      = "_acme-challenge.example.test."
)

type testRFC2136Server struct {
	mu               sync.Mutex
	records          map[string]map[string]bool
	cnames           map[string]string
	queries          int
	updates          int
	cleanupAttempts  int
	delayQueries     int
	transientUpdates int
	refuseCleanup    bool
	dropResponses    int
	firstQuery       chan struct{}
}

func (s *testRFC2136Server) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Authoritative = true
	signed := req.IsTsig() != nil && w.TsigStatus() == nil
	if req.Opcode == dns.OpcodeUpdate {
		s.updates++
		switch {
		case !signed:
			resp.Rcode = dns.RcodeRefused
		case s.transientUpdates > 0:
			s.transientUpdates--
			resp.Rcode = dns.RcodeServerFailure
		default:
			expected := map[string]bool{}
			prereqName := ""
			for _, rr := range req.Answer {
				prereqName = strings.ToLower(rr.Header().Name)
				if rr.Header().Class == dns.ClassNONE {
					if rr.Header().Rrtype == dns.TypeTXT && len(s.records[prereqName]) != 0 ||
						rr.Header().Rrtype == dns.TypeCNAME && s.cnames[prereqName] != "" {
						resp.Rcode = dns.RcodeYXRrset
					}
				} else if txt, ok := rr.(*dns.TXT); ok {
					expected[strings.Join(txt.Txt, "")] = true
				}
			}
			if len(expected) != 0 {
				actual := s.records[prereqName]
				if len(expected) != len(actual) {
					resp.Rcode = dns.RcodeNXRrset
				}
				for value := range expected {
					if !actual[value] {
						resp.Rcode = dns.RcodeNXRrset
					}
				}
			}
			if resp.Rcode != dns.RcodeSuccess {
				break
			}
			for _, rr := range req.Ns {
				txt, ok := rr.(*dns.TXT)
				if !ok {
					resp.Rcode = dns.RcodeFormatError
					break
				}
				name, value := strings.ToLower(txt.Hdr.Name), strings.Join(txt.Txt, "")
				if txt.Hdr.Class == dns.ClassNONE {
					s.cleanupAttempts++
					if s.refuseCleanup {
						resp.Rcode = dns.RcodeRefused
						break
					}
					delete(s.records[name], value)
				} else {
					if s.records[name] == nil {
						s.records[name] = map[string]bool{}
					}
					s.records[name][value] = true
				}
			}
			if resp.Rcode == dns.RcodeSuccess && s.dropResponses > 0 {
				s.dropResponses--
				_ = w.Close()
				return
			}
		}
	} else if len(req.Question) == 1 {
		s.queries++
		if s.queries == 1 && s.firstQuery != nil {
			close(s.firstQuery)
		}
		if s.delayQueries > 0 {
			s.delayQueries--
		} else {
			name := strings.ToLower(req.Question[0].Name)
			if target := s.cnames[name]; target != "" {
				resp.Answer = append(resp.Answer, &dns.CNAME{
					Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60}, Target: target,
				})
			} else if req.Question[0].Qtype == dns.TypeTXT {
				for value := range s.records[name] {
					resp.Answer = append(resp.Answer, &dns.TXT{
						Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 60},
						Txt: []string{value},
					})
				}
			}
		}
	}
	if signed {
		resp.SetTsig(testTSIGName, dns.HmacSHA256, 300, time.Now().Unix())
	}
	_ = w.WriteMsg(resp)
}

func startRFC2136Server(t *testing.T) (*testRFC2136Server, *rfc2136Provider) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := &testRFC2136Server{records: map[string]map[string]bool{}, cnames: map[string]string{}}
	srv := &dns.Server{
		Listener: listener, Handler: handler,
		TsigSecret:    map[string]string{testTSIGName: testTSIGSecret},
		MsgAcceptFunc: func(dns.Header) dns.MsgAcceptAction { return dns.MsgAccept },
	}
	done := make(chan error, 1)
	go func() { done <- srv.ActivateAndServe() }()
	t.Cleanup(func() {
		if err := srv.Shutdown(); err != nil {
			t.Errorf("shutdown DNS test server: %v", err)
		}
		if err := <-done; err != nil {
			t.Errorf("DNS test server: %v", err)
		}
	})
	provider, err := newRFC2136Provider(&config.RFC2136Config{
		Server: listener.Addr().String(), Observers: []string{listener.Addr().String()}, Zone: "example.test", TSIGKeyName: testTSIGName,
		TSIGSecret: testTSIGSecret, PropagationTimeout: config.Duration(time.Second),
		PollInterval: config.Duration(5 * time.Millisecond),
	}, (&net.Dialer{}).DialContext)
	if err != nil {
		t.Fatal(err)
	}
	return handler, provider
}

func cleanupTestDNSLease(ctx context.Context, lease dnsChallengeLease) error {
	release, err := lease.admitCleanup(ctx)
	if err != nil {
		return err
	}
	defer release()
	return lease.cleanupAdmitted(ctx)
}

func TestRFC2136ObserversWaitForEveryAuthoritativeView(t *testing.T) {
	primary, provider := startRFC2136Server(t)
	secondary, secondaryProvider := startRFC2136Server(t)
	provider.observers = []string{provider.server, secondaryProvider.server}
	provider.pollInterval = 10 * time.Millisecond
	lease, err := provider.Present(context.Background(), testOwner, "challenge")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- lease.Wait(ctx) }()
	select {
	case err := <-result:
		t.Fatalf("primary visibility alone completed propagation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	secondary.mu.Lock()
	secondary.records[testOwner] = map[string]bool{"challenge": true}
	secondary.mu.Unlock()
	if err := <-result; err != nil {
		t.Fatalf("both observers did not complete propagation: %v", err)
	}
	if err := cleanupTestDNSLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	primary.mu.Lock()
	defer primary.mu.Unlock()
	if len(primary.records[testOwner]) != 0 {
		t.Fatalf("cleanup left owned records: %v", primary.records[testOwner])
	}
}

func TestRFC2136ExplicitCNAMEDelegation(t *testing.T) {
	const target = "validation.example.test."
	for _, scenario := range []string{"valid", "missing", "mismatch", "chain", "unconfigured"} {
		t.Run(scenario, func(t *testing.T) {
			server, provider := startRFC2136Server(t)
			if scenario != "unconfigured" {
				provider.delegations[testOwner] = target
			}
			server.mu.Lock()
			server.records[target] = map[string]bool{"unrelated": true}
			switch scenario {
			case "valid", "chain", "unconfigured":
				server.cnames[testOwner] = target
			case "mismatch":
				server.cnames[testOwner] = "other.example.test."
			}
			if scenario == "chain" {
				server.cnames[target] = "next.example.test."
			}
			server.mu.Unlock()
			lease, err := provider.Present(context.Background(), testOwner, "challenge")
			if scenario != "valid" {
				if err == nil || !strings.Contains(err.Error(), "CNAME") {
					t.Fatalf("expected actionable CNAME failure, got %v", err)
				}
				server.mu.Lock()
				defer server.mu.Unlock()
				if server.updates != 0 || !server.records[target]["unrelated"] {
					t.Fatalf("failed CNAME verification mutated DNS: updates=%d records=%v", server.updates, server.records[target])
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := cleanupTestDNSLease(context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			server.mu.Lock()
			defer server.mu.Unlock()
			if len(server.records[target]) != 1 || !server.records[target]["unrelated"] || len(server.records[testOwner]) != 0 {
				t.Fatalf("delegated cleanup changed unrelated records: %v", server.records)
			}
		})
	}
}

func TestRFC2136CNAMEChecksEveryObserverBeforeUpdateAndSubmission(t *testing.T) {
	const target = "validation.example.test."
	primary, provider := startRFC2136Server(t)
	secondary, secondaryProvider := startRFC2136Server(t)
	provider.observers = []string{provider.server, secondaryProvider.server}
	provider.delegations[testOwner] = target
	primary.mu.Lock()
	primary.cnames[testOwner] = target
	primary.mu.Unlock()
	secondary.mu.Lock()
	secondary.cnames[testOwner] = "other.example.test."
	secondary.mu.Unlock()
	if _, err := provider.Present(context.Background(), testOwner, "challenge"); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched second observer error = %v", err)
	}
	primary.mu.Lock()
	updates := primary.updates
	primary.mu.Unlock()
	if updates != 0 {
		t.Fatalf("mismatched second observer allowed %d updates", updates)
	}

	secondary.mu.Lock()
	secondary.cnames[testOwner] = target
	secondary.mu.Unlock()
	lease, err := provider.Present(context.Background(), testOwner, "challenge")
	if err != nil {
		t.Fatal(err)
	}
	secondary.mu.Lock()
	secondary.records[target] = map[string]bool{"challenge": true}
	secondary.cnames[testOwner] = "other.example.test."
	secondary.mu.Unlock()
	if err := lease.Wait(context.Background()); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("changed alias did not stop submission: %v", err)
	}
	if err := cleanupTestDNSLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
}

func TestRFC2136RecordOwnershipAndPropagation(t *testing.T) {
	server, provider := startRFC2136Server(t)
	server.mu.Lock()
	server.records[testOwner] = map[string]bool{"unrelated": true}
	server.mu.Unlock()
	ctx := context.Background()
	var leases []dnsChallengeLease
	for _, value := range []string{"challenge-one", "challenge-two"} {
		lease, err := provider.Present(ctx, testOwner, value)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
		owned := lease.(*rfc2136Lease)
		digest := sha256.Sum256([]byte(value))
		if !strings.HasPrefix(owned.marker, fmt.Sprintf("jul-dns01-%x-", digest)) {
			t.Fatalf("marker does not identify its owned TXT value: %q", owned.marker)
		}
	}
	server.mu.Lock()
	server.delayQueries = 2
	server.mu.Unlock()
	if err := leases[0].Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cleanupTestDNSLease(ctx, leases[0]); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.queries < 3 {
		t.Fatalf("propagation polling made %d queries", server.queries)
	}
	got := server.records[testOwner]
	if got["challenge-one"] || !got["challenge-two"] || !got["unrelated"] {
		t.Fatalf("record-specific cleanup did not preserve the other values: %v", got)
	}
}

func TestRFC2136MissingOwnershipMarkerDoesNotDeleteValue(t *testing.T) {
	server, provider := startRFC2136Server(t)
	lease, err := provider.Present(context.Background(), testOwner, "challenge")
	if err != nil {
		t.Fatal(err)
	}
	marker := lease.(*rfc2136Lease).marker
	server.mu.Lock()
	delete(server.records[testOwner], marker)
	server.records[testOwner]["unrelated"] = true
	server.mu.Unlock()
	if err := cleanupTestDNSLease(context.Background(), lease); err == nil || dnsErrorClass(err) != "marker_missing" {
		t.Fatalf("missing ownership marker was not surfaced: %v", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if !server.records[testOwner]["challenge"] || !server.records[testOwner]["unrelated"] {
		t.Fatalf("cleanup deleted unproven ownership: %v", server.records[testOwner])
	}
}

func TestRFC2136FailuresAndRecovery(t *testing.T) {
	server, provider := startRFC2136Server(t)
	server.mu.Lock()
	server.transientUpdates = 1
	server.mu.Unlock()
	lease, err := provider.Present(context.Background(), testOwner, "challenge")
	if err != nil {
		t.Fatalf("transient update did not recover: %v", err)
	}
	server.mu.Lock()
	server.refuseCleanup = true
	server.mu.Unlock()
	if err := cleanupTestDNSLease(context.Background(), lease); err == nil || dnsErrorClass(err) != "refused" {
		t.Fatalf("cleanup refusal: %v", err)
	}
	bad := *provider
	bad.secret = "ZmFrZS1kaWZmZXJlbnQta2V5"
	if _, err := bad.Present(context.Background(), testOwner, "challenge"); err == nil || dnsErrorClass(err) != "authentication" {
		t.Fatalf("bad TSIG secret: %v", err)
	}
	blocked := *provider
	blocked.dial = func(context.Context, string, string) (net.Conn, error) { return nil, egress.ErrBlocked }
	if _, err := blocked.Present(context.Background(), testOwner, "challenge"); err == nil || !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("egress denial: %v", err)
	}
}

func TestRFC2136ErrorsAreActionableWithoutProviderDetails(t *testing.T) {
	const secret = "synthetic private provider detail"
	tests := []struct {
		class string
		want  string
	}{
		{"egress", "blocked by egress"},
		{"refused", "refused"},
		{"authentication", "TSIG authentication"},
		{"not_authoritative", "not authoritative"},
		{"conflict", "ownership marker already exists"},
		{"timeout", "timed out"},
		{"cancelled", "cancelled"},
		{"transient", "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.class, func(t *testing.T) {
			cause := errors.New(secret)
			err := &dnsChallengeError{action: "present", class: tt.class, cause: cause}
			if got := err.Error(); !strings.Contains(got, tt.want) || strings.Contains(got, secret) {
				t.Fatalf("unsafe or unclear provider error: %q", got)
			}
			if !errors.Is(err, cause) {
				t.Fatal("provider failure cause was lost")
			}
		})
	}
}

func TestRFC2136RequiresManagedDialerAndResolvedCredential(t *testing.T) {
	dial := (&net.Dialer{}).DialContext
	for _, tt := range []struct {
		name   string
		config *config.RFC2136Config
		dial   egress.DialFunc
	}{
		{"missing config", nil, dial},
		{"missing dialer", &config.RFC2136Config{TSIGSecret: testTSIGSecret}, nil},
		{"missing credential", &config.RFC2136Config{}, dial},
		{"unresolved credential", &config.RFC2136Config{TSIGSecret: "${env:TEST_TSIG}"}, dial},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newRFC2136Provider(tt.config, tt.dial); err == nil {
				t.Fatal("unsafe provider configuration was accepted")
			}
		})
	}
}

func TestRFC2136PropagationTimeoutAndCancellation(t *testing.T) {
	_, provider := startRFC2136Server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	lease := &rfc2136Lease{provider: provider, owner: testOwner, value: "absent", marker: "unused"}
	err := lease.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || dnsErrorClass(err) != "timeout" {
		t.Fatalf("propagation timeout: %v", err)
	}

	client, peer := net.Pipe()
	defer peer.Close()
	blocked := *provider
	blocked.dial = func(context.Context, string, string) (net.Conn, error) { return client, nil }
	cancelled, stop := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, stop)
	start := time.Now()
	_, err = blocked.Present(cancelled, testOwner, "challenge")
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("in-flight cancellation: %v", err)
	}
}

func TestRFC2136ExistingValueAndConcurrentOwnership(t *testing.T) {
	server, provider := startRFC2136Server(t)
	server.mu.Lock()
	server.records[testOwner] = map[string]bool{"existing": true, "unrelated": true}
	server.mu.Unlock()

	lease, err := provider.Present(context.Background(), testOwner, "existing")
	if err != nil {
		t.Fatalf("pre-existing value: %v", err)
	}
	if err := lease.Wait(context.Background()); err != nil {
		t.Fatalf("pre-existing challenge was not observed: %v", err)
	}
	if err := cleanupTestDNSLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}

	type result struct {
		lease dnsChallengeLease
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			lease, err := provider.Present(context.Background(), testOwner, "shared")
			results <- result{lease, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent ownership results: %v, %v", first.err, second.err)
	}
	if first.lease.(*rfc2136Lease).observed {
		first, second = second, first
	}
	if !second.lease.(*rfc2136Lease).observed {
		t.Fatal("duplicate value did not retain a distinct owner")
	}
	if err := cleanupTestDNSLease(context.Background(), second.lease); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	preserved := server.records[testOwner]["existing"] && server.records[testOwner]["shared"] && server.records[testOwner]["unrelated"]
	server.mu.Unlock()
	if !preserved {
		t.Fatal("non-owner cleanup removed an existing record")
	}
	if err := cleanupTestDNSLease(context.Background(), first.lease); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.records[testOwner]["shared"] || !server.records[testOwner]["existing"] || !server.records[testOwner]["unrelated"] {
		t.Fatalf("owner cleanup damaged records: %v", server.records[testOwner])
	}
}

func TestRFC2136ExchangeConcurrencyAndQueuedCancellation(t *testing.T) {
	_, provider := startRFC2136Server(t)
	entered := make(chan struct{}, dnsAuthWorkers+1)
	release := make(chan struct{})
	releaseAll := sync.OnceFunc(func() { close(release) })
	defer releaseAll()
	provider.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return nil, errors.New("injected dial failure")
		}
	}
	results := make(chan error, dnsAuthWorkers)
	for range dnsAuthWorkers - 1 {
		go func() {
			_, err := provider.exchange(context.Background(), new(dns.Msg), false, false)
			results <- err
		}()
	}
	for range dnsAuthWorkers - 1 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("limited DNS exchanges did not start")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() {
		_, err := provider.exchange(ctx, new(dns.Msg), false, false)
		queued <- err
	}()
	select {
	case <-entered:
		t.Fatal("more than the permitted DNS exchanges started")
	case <-time.After(100 * time.Millisecond):
	}
	go func() {
		_, err := provider.exchange(context.Background(), new(dns.Msg), false, true)
		results <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup could not use the reserved DNS exchange")
	}
	cancel()
	if err := <-queued; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued DNS exchange did not cancel: %v", err)
	}
	releaseAll()
	for range dnsAuthWorkers {
		if err := <-results; err == nil {
			t.Fatal("injected dial failure was ignored")
		}
	}
}

type delayedDNSReadConn struct {
	net.Conn
	delay time.Duration
}

func (c *delayedDNSReadConn) Read(p []byte) (int, error) {
	time.Sleep(c.delay)
	return c.Conn.Read(p)
}

func TestRFC2136QueuedPropagationObservesReadyValues(t *testing.T) {
	server, provider := startRFC2136Server(t)
	server.mu.Lock()
	server.records[testOwner] = map[string]bool{"ready": true}
	server.mu.Unlock()
	provider.propagationTimeout = 60 * time.Millisecond
	baseDial := provider.dial
	provider.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := baseDial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &delayedDNSReadConn{Conn: conn, delay: 20 * time.Millisecond}, nil
	}
	lease := &rfc2136Lease{provider: provider, owner: testOwner, value: "ready"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan error, 24)
	for range cap(results) {
		go func() { results <- lease.Wait(ctx) }()
	}
	for range cap(results) {
		if err := <-results; err != nil {
			t.Fatalf("queued ready challenge was not observed: %v", err)
		}
	}
}

func TestRFC2136QueuedRetryObservesReadyValue(t *testing.T) {
	server, provider := startRFC2136Server(t)
	server.mu.Lock()
	server.records[testOwner] = map[string]bool{"ready": true}
	server.delayQueries = 1
	server.firstQuery = make(chan struct{})
	firstQuery := server.firstQuery
	server.mu.Unlock()
	provider.propagationTimeout = 300 * time.Millisecond
	provider.pollInterval = 250 * time.Millisecond
	lease := &rfc2136Lease{provider: provider, owner: testOwner, value: "ready"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- lease.Wait(ctx) }()
	select {
	case <-firstQuery:
	case <-ctx.Done():
		t.Fatal("first propagation query did not start")
	}
	for range cap(provider.regularExchanges) {
		provider.regularExchanges <- struct{}{}
	}
	release := sync.OnceFunc(func() {
		for range cap(provider.regularExchanges) {
			<-provider.regularExchanges
		}
	})
	defer release()
	select {
	case err := <-result:
		t.Fatalf("queued retry ended before DNS admission: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	release()
	if err := <-result; err != nil {
		t.Fatalf("queued retry failed to observe the ready value: %v", err)
	}
}

func TestRFC2136PollIntervalEqualToTimeoutStillRetries(t *testing.T) {
	server, provider := startRFC2136Server(t)
	server.mu.Lock()
	server.records[testOwner] = map[string]bool{"ready": true}
	server.delayQueries = 1
	server.mu.Unlock()
	provider.propagationTimeout = 80 * time.Millisecond
	provider.pollInterval = provider.propagationTimeout
	lease := &rfc2136Lease{provider: provider, owner: testOwner, value: "ready"}
	if err := lease.Wait(context.Background()); err != nil {
		t.Fatalf("final propagation observation failed: %v", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.queries != 2 {
		t.Fatalf("propagation queries = %d, want initial and final observations", server.queries)
	}
}

func TestRFC2136SlowNegativeProbeGetsOneFinalObservation(t *testing.T) {
	for _, ready := range []bool{true, false} {
		t.Run(fmt.Sprintf("ready=%t", ready), func(t *testing.T) {
			server, provider := startRFC2136Server(t)
			server.mu.Lock()
			if ready {
				server.records[testOwner] = map[string]bool{"challenge": true}
			}
			server.delayQueries = 2
			server.mu.Unlock()
			provider.propagationTimeout = 50 * time.Millisecond
			provider.pollInterval = 10 * time.Millisecond
			baseDial := provider.dial
			var calls atomic.Int32
			provider.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := baseDial(ctx, network, address)
				if err != nil {
					return nil, err
				}
				if calls.Add(1) == 2 {
					return &delayedDNSReadConn{Conn: conn, delay: 80 * time.Millisecond}, nil
				}
				return conn, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			lease := &rfc2136Lease{provider: provider, owner: testOwner, value: "challenge"}
			err := lease.Wait(ctx)
			if ready && err != nil || !ready && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("final observation returned %v, ready=%t", err, ready)
			}
			server.mu.Lock()
			defer server.mu.Unlock()
			if server.queries != 3 {
				t.Fatalf("propagation queries = %d, want initial, slow and final", server.queries)
			}
		})
	}
}

func TestRFC2136RefreshesTSIGAfterExchangeAdmission(t *testing.T) {
	_, provider := startRFC2136Server(t)
	for range cap(provider.regularExchanges) {
		provider.regularExchanges <- struct{}{}
	}
	release := sync.OnceFunc(func() {
		for range cap(provider.regularExchanges) {
			<-provider.regularExchanges
		}
	})
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	msg := new(dns.Msg)
	msg.SetQuestion(testOwner, dns.TypeTXT)
	msg.SetTsig(testTSIGName, dns.HmacSHA256, 300, time.Now().Add(-time.Hour).Unix())
	result := make(chan error, 1)
	go func() {
		_, err := provider.exchange(ctx, msg, true, false)
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("exchange bypassed admission: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	if err := <-result; err != nil {
		t.Fatalf("admitted exchange used the stale TSIG timestamp: %v", err)
	}
}

func TestRFC2136SignedExchangeRejectsMissingTSIG(t *testing.T) {
	_, provider := startRFC2136Server(t)
	msg := new(dns.Msg)
	msg.SetQuestion(testOwner, dns.TypeTXT)
	if _, err := provider.exchange(context.Background(), msg, true, false); dnsErrorClass(err) != "authentication" {
		t.Fatalf("unsigned exchange was not rejected: %v", err)
	}
}

func TestRFC2136CleanupTimeoutStartsAfterAdmission(t *testing.T) {
	for _, blocked := range []string{"owner", "DNS exchange"} {
		t.Run(blocked, func(t *testing.T) {
			server, provider := startRFC2136Server(t)
			server.mu.Lock()
			server.records[testOwner] = map[string]bool{"unrelated": true}
			server.mu.Unlock()
			lease, err := provider.Present(context.Background(), testOwner, "challenge")
			if err != nil {
				t.Fatal(err)
			}
			var release func()
			if blocked == "owner" {
				release, err = provider.owners.acquire(context.Background(), testOwner)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				for range cap(provider.exchanges) {
					provider.exchanges <- struct{}{}
				}
				release = func() {
					for range cap(provider.exchanges) {
						<-provider.exchanges
					}
				}
			}
			release = sync.OnceFunc(release)
			defer release()
			m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
			result := make(chan error, 1)
			go func() { result <- m.cleanupDNSLease(lease, 300*time.Millisecond) }()
			waitForDNSACMETest(t, "cleanup admission to wait", func() bool {
				provider.owners.mu.Lock()
				defer provider.owners.mu.Unlock()
				entry := provider.owners.entries[testOwner]
				return entry != nil && (blocked != "owner" || entry.refs == 2)
			})
			select {
			case err := <-result:
				t.Fatalf("cleanup timed out while waiting for admission: %v", err)
			case <-time.After(400 * time.Millisecond):
			}
			release()
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("admitted cleanup failed: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cleanup did not run after admission")
			}
			server.mu.Lock()
			defer server.mu.Unlock()
			if len(server.records[testOwner]) != 1 || !server.records[testOwner]["unrelated"] {
				t.Fatalf("cleanup did not preserve unrelated records: %v", server.records[testOwner])
			}
		})
	}
}

func TestRFC2136CleanupAdmissionCancellationReleasesOwner(t *testing.T) {
	_, provider := startRFC2136Server(t)
	lease, err := provider.Present(context.Background(), testOwner, "challenge")
	if err != nil {
		t.Fatal(err)
	}
	for range cap(provider.exchanges) {
		provider.exchanges <- struct{}{}
	}
	defer func() {
		for range cap(provider.exchanges) {
			<-provider.exchanges
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := lease.admitCleanup(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup admission cancellation: %v", err)
	}
	provider.owners.mu.Lock()
	defer provider.owners.mu.Unlock()
	if len(provider.owners.entries) != 0 {
		t.Fatalf("cleanup admission retained owner locks: %v", provider.owners.entries)
	}
}

func TestRFC2136ConcurrentUpdatesPreserveDistinctValues(t *testing.T) {
	server, provider := startRFC2136Server(t)
	server.mu.Lock()
	server.records[testOwner] = map[string]bool{"unrelated": true}
	server.mu.Unlock()
	type result struct {
		lease dnsChallengeLease
		err   error
	}
	results := make(chan result, 8)
	for i := range cap(results) {
		go func() {
			lease, err := provider.Present(context.Background(), testOwner, fmt.Sprintf("challenge-%d", i))
			results <- result{lease, err}
		}()
	}
	var leases []dnsChallengeLease
	var failures []error
	for range cap(results) {
		got := <-results
		if got.err != nil {
			failures = append(failures, got.err)
		} else {
			leases = append(leases, got.lease)
		}
	}
	if err := errors.Join(failures...); err != nil {
		t.Fatalf("concurrent update failed: %v", err)
	}
	server.mu.Lock()
	for i := range cap(results) {
		if !server.records[testOwner][fmt.Sprintf("challenge-%d", i)] {
			server.mu.Unlock()
			t.Fatalf("concurrent challenge %d was lost", i)
		}
	}
	server.mu.Unlock()
	for _, lease := range leases {
		if err := cleanupTestDNSLease(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.records[testOwner]) != 1 || !server.records[testOwner]["unrelated"] {
		t.Fatalf("cleanup damaged unrelated records: %v", server.records[testOwner])
	}
}

func TestRFC2136OwnerLockCancellationAndIsolation(t *testing.T) {
	_, provider := startRFC2136Server(t)
	unlock, err := provider.owners.acquire(context.Background(), testOwner)
	if err != nil {
		t.Fatal(err)
	}
	releaseOwner := sync.OnceFunc(unlock)
	defer releaseOwner()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan error, 1)
	go func() {
		release, err := provider.owners.acquire(ctx, testOwner)
		if err == nil {
			release()
		}
		waiting <- err
	}()
	other, err := provider.owners.acquire(context.Background(), "_acme-challenge.other.example.test.")
	if err != nil {
		t.Fatal(err)
	}
	other()
	select {
	case err := <-waiting:
		t.Fatalf("same-owner update was not serialized: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-waiting; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting owner lock did not cancel: %v", err)
	}
	releaseOwner()
	provider.owners.mu.Lock()
	defer provider.owners.mu.Unlock()
	if len(provider.owners.entries) != 0 {
		t.Fatalf("owner locks were retained: %v", provider.owners.entries)
	}
}

func TestRFC2136CleanupAfterAmbiguousPresent(t *testing.T) {
	server, provider := startRFC2136Server(t)
	server.mu.Lock()
	server.records[testOwner] = map[string]bool{"unrelated": true}
	server.dropResponses = 1
	server.mu.Unlock()
	baseDial := provider.dial
	var calls atomic.Int32
	provider.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if calls.Add(1) > 2 {
			return nil, egress.ErrBlocked
		}
		return baseDial(ctx, network, address)
	}
	lease, err := provider.Present(context.Background(), testOwner, "uncertain")
	if lease == nil || !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("ambiguous present: lease=%v error=%v", lease != nil, err)
	}
	provider.dial = baseDial
	if err := cleanupTestDNSLease(context.Background(), lease); err != nil {
		t.Fatalf("cleanup after ambiguous present: %v", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.records[testOwner]) != 1 || !server.records[testOwner]["unrelated"] {
		t.Fatalf("ambiguous cleanup left or removed records: %v", server.records[testOwner])
	}
}
