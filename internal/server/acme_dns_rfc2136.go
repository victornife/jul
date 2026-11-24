// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	observers          []string
	delegations        map[string]string
	zone               string
	keyName            string
	secret             string
	propagationTimeout time.Duration
	pollInterval       time.Duration
	dial               egress.DialFunc
	now                func() time.Time
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
	case "clock":
		return "DNS-01 " + e.action + " rejected the TSIG timestamp; synchronize the Jul and DNS server clocks"
	case "not_authoritative":
		return "DNS-01 DNS server is not authoritative for the challenge name; check the update zone and observers"
	case "cname":
		return "DNS-01 found an unconfigured CNAME or CNAME chain; configure a one-hop mapping or use direct TXT/NS delegation"
	case "alias":
		return "DNS-01 published CNAME is missing or does not match the configured target; check the challenge delegation"
	case "conflict":
		return "DNS-01 ownership marker already exists; leave it unchanged and retry"
	case "marker_missing":
		return "DNS-01 ownership marker is missing; inspect the challenge value before manual recovery"
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
	if len(c.Observers) == 0 || len(c.Observers) > config.MaxRFC2136Observers {
		return nil, errors.New("DNS-01 requires 1 to 8 CA-facing authoritative DNS observers")
	}
	zone := dns.Fqdn(strings.ToLower(c.Zone))
	delegations := make(map[string]string, len(c.CNAMEDelegations))
	for owner, target := range c.CNAMEDelegations {
		owner, target = dns.Fqdn(strings.ToLower(owner)), dns.Fqdn(strings.ToLower(target))
		if owner == target || !dns.IsSubDomain(zone, target) {
			return nil, errors.New("DNS-01 CNAME target must be distinct and inside the update zone")
		}
		delegations[owner] = target
	}
	timeout, interval := c.PropagationTimeout.Std(), c.PollInterval.Std()
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	if interval == 0 {
		interval = 2 * time.Second
	}
	return &rfc2136Provider{
		server: c.Server, observers: append([]string(nil), c.Observers...), zone: zone, delegations: delegations,
		keyName: dns.Fqdn(strings.ToLower(c.TSIGKeyName)), secret: c.TSIGSecret,
		propagationTimeout: timeout, pollInterval: interval, dial: dial, now: time.Now,
		exchanges: make(chan struct{}, dnsAuthWorkers), regularExchanges: make(chan struct{}, dnsAuthWorkers-1),
		owners: &dnsOwnerLocks{entries: make(map[string]*dnsOwnerLock)},
	}, nil
}

type rfc2136Lease struct {
	provider *rfc2136Provider
	owner    string
	alias    string
	value    string
	marker   string
	observed bool
}

func (p *rfc2136Provider) Present(ctx context.Context, owner, value string) (dnsChallengeLease, error) {
	owner = dns.Fqdn(strings.ToLower(owner))
	target, delegated := p.delegations[owner]
	if !delegated {
		target = owner
	}
	if !dns.IsSubDomain(p.zone, target) || len(value) == 0 || len(value) > 255 {
		return nil, &dnsChallengeError{action: "present", class: "invalid"}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, &dnsChallengeError{action: "present", class: "transient", cause: err}
	}
	digest := sha256.Sum256([]byte(value))
	lease := &rfc2136Lease{
		provider: p, owner: target, value: value, marker: "jul-dns01-" + hex.EncodeToString(digest[:]) + "-" + hex.EncodeToString(nonce[:]),
	}
	if delegated {
		lease.alias = owner
	}
	for _, observer := range p.observers {
		if _, err := lease.observe(ctx, observer); err != nil {
			return nil, &dnsChallengeError{action: "propagation", class: dnsErrorClass(err), cause: err}
		}
	}
	return lease, lease.update(ctx, false)
}

func (l *rfc2136Lease) cleanupIdentity() (string, string) {
	if l.observed {
		return "", ""
	}
	return l.owner, l.marker
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
			if hasValue {
				return &dnsChallengeError{action: action, class: "marker_missing"}
			}
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
		if !remove {
			msg.RRsetNotUsed([]dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: l.owner, Rrtype: dns.TypeCNAME, Class: dns.ClassINET}}})
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
	for _, observer := range l.provider.observers {
		records, err := l.observeAdmitted(ctx, observer)
		if err != nil {
			if class := dnsErrorClass(err); class != "transient" {
				return false, &dnsChallengeError{action: "propagation", class: class, cause: err}
			}
			return false, nil
		}
		found := false
		for _, record := range records {
			if strings.Join(record.(*dns.TXT).Txt, "") == l.value {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
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
	return p.queryTXTAtAdmitted(ctx, p.server, owner, action, true)
}

func (l *rfc2136Lease) observe(ctx context.Context, observer string) ([]dns.RR, error) {
	release, err := l.provider.acquireExchange(ctx, false)
	if err != nil {
		return nil, err
	}
	defer release()
	return l.observeAdmitted(ctx, observer)
}

func (l *rfc2136Lease) observeAdmitted(ctx context.Context, observer string) ([]dns.RR, error) {
	if l.alias != "" {
		resp, err := l.provider.queryAtAdmitted(ctx, observer, l.alias, dns.TypeCNAME, "propagation", false)
		if err != nil {
			return nil, err
		}
		matches := 0
		for _, answer := range resp.Answer {
			if cname, ok := answer.(*dns.CNAME); ok && strings.EqualFold(cname.Hdr.Name, l.alias) {
				if !strings.EqualFold(dns.Fqdn(cname.Target), l.owner) {
					return nil, &dnsChallengeError{action: "propagation", class: "alias"}
				}
				matches++
			}
		}
		if matches != 1 {
			return nil, &dnsChallengeError{action: "propagation", class: "alias"}
		}
	}
	return l.provider.queryTXTAtAdmitted(ctx, observer, l.owner, "propagation", false)
}

func (p *rfc2136Provider) queryTXTAtAdmitted(ctx context.Context, server, owner, action string, signed bool) ([]dns.RR, error) {
	resp, err := p.queryAtAdmitted(ctx, server, owner, dns.TypeTXT, action, signed)
	if err != nil {
		return nil, err
	}
	var records []dns.RR
	for _, answer := range resp.Answer {
		if cname, ok := answer.(*dns.CNAME); ok && strings.EqualFold(cname.Hdr.Name, owner) {
			return nil, &dnsChallengeError{action: action, class: "cname"}
		}
		if txt, ok := answer.(*dns.TXT); ok && strings.EqualFold(txt.Hdr.Name, owner) {
			records = append(records, dns.Copy(txt))
		}
	}
	return records, nil
}

func (p *rfc2136Provider) queryAtAdmitted(ctx context.Context, server, owner string, qtype uint16, action string, signed bool) (*dns.Msg, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(owner, qtype)
	msg.RecursionDesired = false
	if signed {
		msg.SetTsig(p.keyName, dns.HmacSHA256, 300, time.Now().Unix())
	}
	resp, err := p.exchangeAtAdmitted(ctx, server, msg, signed)
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
	return resp, nil
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
	return p.exchangeAtAdmitted(ctx, p.server, msg, signed)
}

func (p *rfc2136Provider) exchangeAtAdmitted(ctx context.Context, server string, msg *dns.Msg, signed bool) (*dns.Msg, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := p.dial(ctx, "tcp", server)
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
		tsig.TimeSigned = uint64(p.now().Unix())
	}
	resp, _, err := client.ExchangeWithConnContext(ctx, msg, &dns.Conn{Conn: conn})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// The DNS library reports NOTAUTH before verifying its TSIG. Treat
		// BADTIME as a diagnostic only; never accept the response as success.
		if signed && errors.Is(err, dns.ErrAuth) && resp != nil {
			if tsig := resp.IsTsig(); tsig != nil && tsig.Error == dns.RcodeBadTime {
				return nil, &dnsChallengeError{action: "update", class: "clock", cause: err}
			}
		}
		return nil, err
	}
	if signed {
		tsig := resp.IsTsig()
		if tsig == nil {
			return nil, &dnsChallengeError{action: "update", class: "authentication"}
		}
		switch tsig.Error {
		case dns.RcodeBadTime:
			return nil, &dnsChallengeError{action: "update", class: "clock", cause: dns.ErrTime}
		case dns.RcodeBadKey, dns.RcodeBadSig:
			return nil, &dnsChallengeError{action: "update", class: "authentication"}
		}
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
	case errors.Is(err, dns.ErrTime):
		return "clock"
	case errors.Is(err, dns.ErrAuth), errors.Is(err, dns.ErrSig), errors.Is(err, dns.ErrSecret),
		errors.Is(err, dns.ErrKey), errors.Is(err, dns.ErrKeyAlg):
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
