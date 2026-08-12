// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/experimental"
	"github.com/corazawaf/coraza/v3/types"

	"jul/internal/clientaddr"
)

// clientAddrWAF makes Coraza's REMOTE_ADDR the canonical client address without
// mutating http.Request.RemoteAddr.
//
// Coraza's WrapHandler derives REMOTE_ADDR itself, inside an unexported
// processRequest that reads req.RemoteAddr and offers no injection point. It
// does, however, build the transaction through NewTransactionWithOptions when
// the WAF satisfies experimental.WAFWithOptions, passing the request context —
// which already carries the identity derived by internal/clientaddr. Jul
// therefore wraps the engine, reads the identity from that context, and returns
// a transaction that overrides only ProcessConnection. No RemoteAddr mutation,
// no fork of the response interceptor, and no upstream dependency.
//
// It also sidesteps Coraza's strings.LastIndexByte(':') address split, which
// leaves the brackets on an IPv6 peer: the parser is bypassed entirely, so
// rules see a normalized address for IPv6 as well as IPv4.
type clientAddrWAF struct {
	coraza.WAF
}

// clientAddrWAF must satisfy the experimental interface or WrapHandler silently
// falls back to NewTransaction and the substitution never happens.
var _ experimental.WAFWithOptions = (*clientAddrWAF)(nil)

// NewTransactionWithOptions builds a transaction whose connection phase reports
// the canonical client. When the inner engine predates the experimental
// interface, or no identity is present (no policy installed, as on the admin
// listener), the transaction is returned unchanged and Coraza's own parsing of
// RemoteAddr applies.
func (w *clientAddrWAF) NewTransactionWithOptions(opts experimental.Options) types.Transaction {
	inner, ok := w.WAF.(experimental.WAFWithOptions)
	if !ok {
		return w.NewTransaction()
	}
	tx := inner.NewTransactionWithOptions(opts)
	if opts.Context == nil {
		return tx
	}
	id, found := clientaddr.FromContext(opts.Context)
	if !found || !id.Client.IsValid() {
		return tx
	}
	return &clientAddrTx{Transaction: tx, client: id.Client.String()}
}

// clientAddrTx overrides exactly one method of the wrapped transaction.
type clientAddrTx struct {
	types.Transaction
	client string
}

// ProcessConnection substitutes the canonical client address. The port is
// dropped: an address asserted by a proxy has no port on this connection, and
// reporting the proxy's port next to the client's address would be a lie that
// rules could match on.
func (t *clientAddrTx) ProcessConnection(_ string, _ int, serverHost string, serverPort int) {
	t.Transaction.ProcessConnection(t.client, 0, serverHost, serverPort)
}
