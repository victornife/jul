// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"sync"
	"time"
)

// certEventCap bounds the recent renewal events kept per domain.
const certEventCap = 16

// CertRenewalEvent is one certificate renewal observation (Console v2
// Milestone 5.6). It carries only certificate metadata — never private-key
// material.
type CertRenewalEvent struct {
	Time     time.Time `json:"time"`
	Success  bool      `json:"success"`
	Error    string    `json:"error,omitempty"`
	NotAfter time.Time `json:"not_after,omitempty"`
	Issuer   string    `json:"issuer,omitempty"`
	Staging  bool      `json:"staging"`
}

// CertRenewalHistory summarizes the renewal timeline for one domain.
type CertRenewalHistory struct {
	Domain        string             `json:"domain"`
	NextExpiry    time.Time          `json:"next_expiry,omitempty"`
	DaysLeft      int                `json:"days_left"`
	Issuer        string             `json:"issuer,omitempty"`
	Staging       bool               `json:"staging"`
	LastAttempt   time.Time          `json:"last_attempt,omitempty"`
	LastSuccess   time.Time          `json:"last_success,omitempty"`
	LastError     string             `json:"last_error,omitempty"`
	LastErrorTime time.Time          `json:"last_error_time,omitempty"`
	Recent        []CertRenewalEvent `json:"recent,omitempty"`
}

type certDomain struct {
	domain        string
	notAfter      time.Time
	issuer        string
	staging       bool
	lastAttempt   time.Time
	lastSuccess   time.Time
	lastError     string
	lastErrorTime time.Time
	recent        []CertRenewalEvent
}

// certHistoryTracker records certificate renewal lifecycle events for the
// Console v2 Certificate Renewal History panel. It is safe for concurrent use.
type certHistoryTracker struct {
	mu      sync.Mutex
	domains map[string]*certDomain
}

func newCertHistoryTracker() *certHistoryTracker {
	return &certHistoryTracker{domains: make(map[string]*certDomain)}
}

// recordRenewal records a successful renewal: the leaf expiry advanced.
func (t *certHistoryTracker) recordRenewal(domain string, notAfter time.Time, issuer string, staging bool) {
	now := time.Now().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()

	cd := t.get(domain)
	cd.notAfter = notAfter
	cd.issuer = issuer
	cd.staging = staging
	cd.lastAttempt = now
	cd.lastSuccess = now
	cd.append(CertRenewalEvent{Time: now, Success: true, NotAfter: notAfter, Issuer: issuer, Staging: staging})
}

// recordError records a failed renewal attempt with a redacted error string.
func (t *certHistoryTracker) recordError(domain, errMsg string) {
	now := time.Now().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()

	cd := t.get(domain)
	cd.lastAttempt = now
	cd.lastError = errMsg
	cd.lastErrorTime = now
	cd.append(CertRenewalEvent{Time: now, Success: false, Error: errMsg})
}

func (t *certHistoryTracker) get(domain string) *certDomain {
	cd, ok := t.domains[domain]
	if !ok {
		cd = &certDomain{domain: domain}
		t.domains[domain] = cd
	}
	return cd
}

func (cd *certDomain) append(ev CertRenewalEvent) {
	cd.recent = append(cd.recent, ev)
	if len(cd.recent) > certEventCap {
		cd.recent = cd.recent[len(cd.recent)-certEventCap:]
	}
}

// snapshot returns the per-domain renewal history.
func (t *certHistoryTracker) snapshot() []CertRenewalHistory {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]CertRenewalHistory, 0, len(t.domains))
	for _, cd := range t.domains {
		h := CertRenewalHistory{
			Domain:        cd.domain,
			NextExpiry:    cd.notAfter,
			Issuer:        cd.issuer,
			Staging:       cd.staging,
			LastAttempt:   cd.lastAttempt,
			LastSuccess:   cd.lastSuccess,
			LastError:     cd.lastError,
			LastErrorTime: cd.lastErrorTime,
		}
		if !cd.notAfter.IsZero() {
			h.DaysLeft = int(time.Until(cd.notAfter).Hours() / 24)
		}
		if len(cd.recent) > 0 {
			// Newest-first for display.
			rev := make([]CertRenewalEvent, len(cd.recent))
			for i, ev := range cd.recent {
				rev[len(cd.recent)-1-i] = ev
			}
			h.Recent = rev
		}
		out = append(out, h)
	}
	return out
}
