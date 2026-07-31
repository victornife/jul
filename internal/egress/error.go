// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package egress

import (
	"errors"
	"fmt"
)

// ErrBlocked is the sentinel every egress denial wraps. Callers test for a
// policy refusal with errors.Is(err, egress.ErrBlocked) without importing the
// concrete BlockError, and recover structured detail with errors.As.
var ErrBlocked = errors.New("egress blocked: destination not in the [egress] allow-list")

// Reason is the bounded, low-cardinality cause of a block. It is safe to use as
// a metric label; the destination host and IP are never labels.
type Reason string

const (
	// ReasonHostNotAllowed: a hostname is neither listed by name nor resolves
	// entirely into an allowed CIDR.
	ReasonHostNotAllowed Reason = "host_not_allowed"
	// ReasonIPNotAllowed: an IP literal falls outside every allowed CIDR.
	ReasonIPNotAllowed Reason = "ip_not_allowed"
	// ReasonMixedDNS: a hostname resolved to a mix of allowed and disallowed
	// addresses (a rebinding-shaped answer), so the connection is refused rather
	// than raced.
	ReasonMixedDNS Reason = "mixed_dns_answers"
	// ReasonNoDNSAnswers: a hostname resolved to no addresses.
	ReasonNoDNSAnswers Reason = "no_dns_answers"
	// ReasonInvalidAddress: the dial target could not be parsed into a usable
	// host, so it fails closed.
	ReasonInvalidAddress Reason = "invalid_address"
)

// BlockError describes a single refused destination. Its message names the
// subsystem, normalized host, and reason so an operator can act on it, but it
// never includes credentials or query strings. Unwrap yields ErrBlocked.
type BlockError struct {
	Subsystem string // bounded subsystem name (see the Subsystem* constants)
	Host      string // normalized hostname or IP literal that was refused
	IP        string // a resolved/target IP when relevant; may be empty
	Reason    Reason
}

func (e *BlockError) Error() string {
	dest := e.Host
	if e.IP != "" && e.IP != e.Host {
		dest = fmt.Sprintf("%s (%s)", e.Host, e.IP)
	}
	sub := e.Subsystem
	if sub == "" {
		sub = "egress"
	}
	return fmt.Sprintf("egress blocked: %s destination %q not in the [egress] allow-list (%s)", sub, dest, e.Reason)
}

// Unwrap ties every BlockError to the ErrBlocked sentinel.
func (e *BlockError) Unwrap() error { return ErrBlocked }

// Result is the coarse outcome of a policy evaluation for the decisions metric.
type Result string

const (
	ResultAllow Result = "allow"
	ResultBlock Result = "block"
)

// Decision is reported to a Policy observer for each evaluation so the
// observability layer can maintain bounded counters and structured logs. Host
// and IP are for logs only and must never become metric labels; Subsystem,
// Result, and Reason are the bounded label set.
type Decision struct {
	Subsystem string
	Result    Result
	Reason    Reason // set only when Result is ResultBlock
	Host      string // normalized hostname or IP, for logs
	IP        string // resolved/target IP, for logs; may be empty
	// DNSAnswers is the number of resolved addresses evaluated for a
	// CIDR-only hostname; 0 when no DNS lookup was performed.
	DNSAnswers int
}
