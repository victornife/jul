// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"jul/internal/config"
	"jul/internal/egress"
)

type dnsChallengeProvider interface {
	Present(context.Context, string, string) (dnsChallengeLease, error)
}

type dnsChallengeLease interface {
	Wait(context.Context) error
	admitCleanup(context.Context) (func(), error)
	cleanupAdmitted(context.Context) error
}

type rfc2136Provider struct {
	server             string
	zone               string
	keyName            string
	secret             string
	propagationTimeout time.Duration
	pollInterval       time.Duration
	dial               egress.DialFunc
	exchanges          chan struct{}
	regularExchanges   chan struct{}
	owners             *dnsOwnerLocks
}

type dnsOwnerLocks struct {
	mu      sync.Mutex
	entries map[string]*dnsOwnerLock
}

type dnsOwnerLock struct {
	held chan struct{}
	refs int
}

func (l *dnsOwnerLocks) acquire(ctx context.Context, owner string) (func(), error) {
	l.mu.Lock()
	entry := l.entries[owner]
	if entry == nil {
		entry = &dnsOwnerLock{held: make(chan struct{}, 1)}
		l.entries[owner] = entry
	}
	entry.refs++
	l.mu.Unlock()
	releaseRef := func() {
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.entries, owner)
		}
		l.mu.Unlock()
	}
	select {
	case entry.held <- struct{}{}:
		return func() {
			<-entry.held
			releaseRef()
		}, nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
}

type dnsChallengeError struct {
	action string
	class  string
	cause  error
}

func (e *dnsChallengeError) Error() string {
	switch e.class {
	case "egress":
		return "DNS-01 " + e.action + " blocked by egress policy; allow the configured DNS server"
	case "refused":
		return "DNS-01 " + e.action + " refused; check the TSIG key and zone update permissions"
	case "authentication":
		return "DNS-01 " + e.action + " failed TSIG authentication; check the key and server"
	case "not_authoritative":
		return "DNS-01 propagation server is not authoritative for the challenge name"
	case "conflict":
		return "DNS-01 ownership marker already exists; leave it unchanged and retry"
	case "timeout":
		return "DNS-01 " + e.action + " timed out; check the DNS server and propagation"
	case "cancelled":
		return "DNS-01 " + e.action + " was cancelled"
	default:
		return "DNS-01 " + e.action + " failed; check the DNS server and retry"
	}
}

func (e *dnsChallengeError) Unwrap() error { return e.cause }

func newRFC2136Provider(c *config.RFC2136Config, dial egress.DialFunc) (*rfc2136Provider, error) {
	if c == nil || dial == nil {
		return nil, errors.New("DNS-01 requires RFC 2136 settings and a managed egress dialer")
	}
	if c.TSIGSecret == "" || config.ContainsSecretRef(c.TSIGSecret) {
		return nil, errors.New("DNS-01 requires a resolved TSIG secret reference")
	}
	timeout, interval := c.PropagationTimeout.Std(), c.PollInterval.Std()
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	if interval == 0 {
		interval = 2 * time.Second
	}
	return &rfc2136Provider{
		server: c.Server, zone: dns.Fqdn(strings.ToLower(c.Zone)),
		keyName: dns.Fqdn(strings.ToLower(c.TSIGKeyName)), secret: c.TSIGSecret,
		propagationTimeout: timeout, pollInterval: interval, dial: dial,
		exchanges: make(chan struct{}, dnsAuthWorkers), regularExchanges: make(chan struct{}, dnsAuthWorkers-1),
		owners: &dnsOwnerLocks{entries: make(map[string]*dnsOwnerLock)},
	}, nil
}

type rfc2136Lease struct {
	provider *rfc2136Provider
	owner    string
	value    string
	marker   string
	observed bool
}

func (p *rfc2136Provider) Present(ctx context.Context, owner, value string) (dnsChallengeLease, error) {
	owner = dns.Fqdn(strings.ToLower(owner))
	if !dns.IsSubDomain(p.zone, owner) || len(value) == 0 || len(value) > 255 {
		return nil, &dnsChallengeError{action: "present", class: "invalid"}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, &dnsChallengeError{action: "present", class: "transient", cause: err}
	}
	lease := &rfc2136Lease{
		provider: p, owner: owner, value: value, marker: "jul-dns01-" + hex.EncodeToString(nonce[:]),
	}
	return lease, lease.update(ctx, false)
}

func (l *rfc2136Lease) admitCleanup(ctx context.Context) (func(), error) {
	if l.observed {
		return func() {}, nil
	}
	unlock, err := l.provider.owners.acquire(ctx, l.owner)
	if err != nil {
		return nil, &dnsChallengeError{action: "cleanup", class: dnsErrorClass(err), cause: err}
	}
	release, err := l.provider.acquireExchange(ctx, true)
	if err != nil {
		unlock()
		return nil, &dnsChallengeError{action: "cleanup", class: dnsErrorClass(err), cause: err}
	}
	return func() {
		release()
		unlock()
	}, nil
}

func (l *rfc2136Lease) cleanupAdmitted(ctx context.Context) error {
	if l.observed {
		return nil
	}
	return l.updateLocked(ctx, true)
}

func (l *rfc2136Lease) update(ctx context.Context, remove bool) error {
	p := l.provider
	action := "present"
	if remove {
		action = "cleanup"
	}
	unlock, err := p.owners.acquire(ctx, l.owner)
	if err != nil {
		return &dnsChallengeError{action: action, class: dnsErrorClass(err), cause: err}
	}
	defer unlock()
	return l.updateLocked(ctx, remove)
}

func (l *rfc2136Lease) updateLocked(ctx context.Context, remove bool) error {
	p := l.provider
	action := "present"
	if remove {
		action = "cleanup"
	}
	for attempt := 0; attempt < 3; attempt++ {
		var records []dns.RR
		var err error
		if remove {
			records, err = p.queryTXTAdmitted(ctx, l.owner, action)
		} else {
			records, err = p.queryTXT(ctx, l.owner, action)
		}
		if err != nil {
			class := dnsErrorClass(err)
			if class != "transient" || attempt == 2 {
				return &dnsChallengeError{action: action, class: class, cause: err}
			}
			if err := waitDNSInterval(ctx, time.Duration(1<<attempt)*200*time.Millisecond); err != nil {
				return &dnsChallengeError{action: action, class: dnsErrorClass(err), cause: err}
			}
			continue
		}
		hasValue, hasMarker := false, false
		for _, record := range records {
			txt := record.(*dns.TXT)
			text := strings.Join(txt.Txt, "")
			hasValue = hasValue || text == l.value
			hasMarker = hasMarker || text == l.marker
		}
		if remove && !hasMarker {
			return nil
		}
		if !remove {
			if hasValue && hasMarker {
				return nil
			}
			if hasValue {
				l.observed = true
				return nil
			}
			if hasMarker {
				return &dnsChallengeError{action: action, class: "conflict"}
			}
		}
		msg := new(dns.Msg)
		msg.SetUpdate(p.zone)
		rr := func(value string) dns.RR {
			return &dns.TXT{Hdr: dns.RR_Header{Name: l.owner, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 60}, Txt: []string{value}}
		}
		if len(records) == 0 {
			msg.RRsetNotUsed([]dns.RR{rr("")})
		} else {
			msg.Used(records)
		}
		if remove {
			toRemove := []dns.RR{rr(l.marker)}
			if hasValue {
				toRemove = append(toRemove, rr(l.value))
			}
			msg.Remove(toRemove)
		} else {
			msg.Insert([]dns.RR{rr(l.marker), rr(l.value)})
		}
		msg.SetTsig(p.keyName, dns.HmacSHA256, 300, time.Now().Unix())
		var resp *dns.Msg
		if remove {
			resp, err = p.exchangeAdmitted(ctx, msg, true)
		} else {
			resp, err = p.exchange(ctx, msg, true, false)
		}
		if err == nil {
			switch resp.Rcode {
			case dns.RcodeSuccess:
				return nil
			case dns.RcodeRefused, dns.RcodeNotAuth, dns.RcodeNotZone:
				return &dnsChallengeError{action: action, class: "refused"}
			case dns.RcodeYXRrset, dns.RcodeNXRrset:
				err = &dnsChallengeError{action: action, class: "transient"}
			default:
				err = &dnsChallengeError{action: action, class: "transient"}
			}
		}
		class := dnsErrorClass(err)
		if class != "transient" || attempt == 2 {
			return &dnsChallengeError{action: action, class: class, cause: err}
		}
		if err := waitDNSInterval(ctx, time.Duration(1<<attempt)*200*time.Millisecond); err != nil {
			return &dnsChallengeError{action: action, class: dnsErrorClass(err), cause: err}
		}
	}
	return &dnsChallengeError{action: action, class: "transient"}
}

func (l *rfc2136Lease) probeAdmitted(ctx context.Context) (bool, error) {
	records, err := l.provider.queryTXTAdmitted(ctx, l.owner, "propagation")
	if err != nil {
		if class := dnsErrorClass(err); class != "transient" {
			return false, &dnsChallengeError{action: "propagation", class: class, cause: err}
		}
		return false, nil
	}
	for _, record := range records {
		if strings.Join(record.(*dns.TXT).Txt, "") == l.value {
			return true, nil
		}
	}
	return false, nil
}

func (l *rfc2136Lease) Wait(ctx context.Context) error {
	remaining := l.provider.propagationTimeout
	first := true
	final := false
	for {
		if !first && !final {
			started := time.Now()
			pollCtx, cancel := context.WithTimeout(ctx, remaining)
			err := waitDNSInterval(pollCtx, l.provider.pollInterval)
			cancel()
			remaining -= time.Since(started)
			if err != nil && (!errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil) {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				return &dnsChallengeError{action: "propagation", class: dnsErrorClass(err), cause: err}
			}
			final = remaining <= 0
		}
		release, err := l.provider.acquireExchange(ctx, false)
		if err != nil {
			return &dnsChallengeError{action: "propagation", class: dnsErrorClass(err), cause: err}
		}
		started := time.Now()
		ready, err := l.probeAdmitted(ctx)
		release()
		if !first {
			remaining -= time.Since(started)
		}
		if err != nil || ready {
			return err
		}
		if !first && remaining <= 0 {
			if final {
				return &dnsChallengeError{action: "propagation", class: "timeout", cause: context.DeadlineExceeded}
			}
			// A slow probe may have observed DNS before the budget expired.
			final = true
		}
		first = false
	}
}

func (p *rfc2136Provider) queryTXT(ctx context.Context, owner, action string) ([]dns.RR, error) {
	release, err := p.acquireExchange(ctx, action == "cleanup")
	if err != nil {
		return nil, err
	}
	defer release()
	return p.queryTXTAdmitted(ctx, owner, action)
}

func (p *rfc2136Provider) queryTXTAdmitted(ctx context.Context, owner, action string) ([]dns.RR, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(owner, dns.TypeTXT)
	msg.RecursionDesired = false
	msg.SetTsig(p.keyName, dns.HmacSHA256, 300, time.Now().Unix())
	resp, err := p.exchangeAdmitted(ctx, msg, true)
	if err != nil {
		return nil, err
	}
	if resp.Rcode == dns.RcodeRefused || resp.Rcode == dns.RcodeNotAuth {
		return nil, &dnsChallengeError{action: action, class: "refused"}
	}
	if resp.Rcode != dns.RcodeSuccess && resp.Rcode != dns.RcodeNameError {
		return nil, &dnsChallengeError{action: action, class: "transient"}
	}
	if !resp.Authoritative {
		return nil, &dnsChallengeError{action: action, class: "not_authoritative"}
	}
	var records []dns.RR
	for _, answer := range resp.Answer {
		if txt, ok := answer.(*dns.TXT); ok && strings.EqualFold(txt.Hdr.Name, owner) {
			records = append(records, dns.Copy(txt))
		}
	}
	return records, nil
}

func (p *rfc2136Provider) exchange(ctx context.Context, msg *dns.Msg, signed, cleanup bool) (*dns.Msg, error) {
	release, err := p.acquireExchange(ctx, cleanup)
	if err != nil {
		return nil, err
	}
	defer release()
	return p.exchangeAdmitted(ctx, msg, signed)
}

func (p *rfc2136Provider) acquireExchange(ctx context.Context, cleanup bool) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !cleanup {
		select {
		case p.regularExchanges <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	select {
	case p.exchanges <- struct{}{}:
		return func() {
			<-p.exchanges
			if !cleanup {
				<-p.regularExchanges
			}
		}, nil
	case <-ctx.Done():
		if !cleanup {
			<-p.regularExchanges
		}
		return nil, ctx.Err()
	}
}

func (p *rfc2136Provider) exchangeAdmitted(ctx context.Context, msg *dns.Msg, signed bool) (*dns.Msg, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := p.dial(ctx, "tcp", p.server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	client := &dns.Client{Net: "tcp", Timeout: 10 * time.Second, TsigSecret: map[string]string{p.keyName: p.secret}}
	if signed {
		tsig := msg.IsTsig()
		if tsig == nil {
			return nil, &dnsChallengeError{action: "update", class: "authentication"}
		}
		// Admission or dialing can outlast the timestamp set on the message.
		tsig.TimeSigned = uint64(time.Now().Unix())
	}
	resp, _, err := client.ExchangeWithConnContext(ctx, msg, &dns.Conn{Conn: conn})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if signed && resp.IsTsig() == nil {
		return nil, &dnsChallengeError{action: "update", class: "authentication"}
	}
	return resp, nil
}

func dnsErrorClass(err error) string {
	var challengeErr *dnsChallengeError
	switch {
	case errors.As(err, &challengeErr):
		return challengeErr.class
	case errors.Is(err, egress.ErrBlocked):
		return "egress"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, dns.ErrSig), errors.Is(err, dns.ErrSecret):
		return "authentication"
	default:
		return "transient"
	}
}

func waitDNSInterval(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("DNS-01 polling interval must be positive")
	}
	jitter := time.Duration(mathrand.Int64N(int64(interval/4) + 1))
	timer := time.NewTimer(interval + jitter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
