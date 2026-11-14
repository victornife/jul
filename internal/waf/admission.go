// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/corazawaf/coraza/v3/experimental/plugins/plugintypes"
	"github.com/corazawaf/coraza/v3/types"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/reqdeadline"
)

// readBufferBytes is the Jul read loop's scratch buffer ceiling, charged as
// buffer and counted by the route minimum (inspection.WAFReadBufferBytes).
const readBufferBytes = int(inspection.WAFReadBufferBytes)

// wafAdmission owns one WAF transaction's inspection-governor state (ADR 0032
// §3, §4 and the R5 amendment). With no governor every method is a no-op and
// the transaction runs ungoverned, as before the governor existed.
type wafAdmission struct {
	gov    *inspection.Governor
	lease  *inspection.Lease
	ctx    context.Context
	limits inspection.Limits
	mode   inspection.Mode

	r1, r2, r3, r4 *inspection.Charge
	r1Amount       uint64
	r2Amount       uint64
	r3Amount       uint64
	r4Amount       uint64
	buf            *inspection.Charge
	bufAmount      uint64
	charges        []*inspection.Charge
	audit          *auditTxCtx
	txID           string

	// Request shape known before phase 5, for the audit-record estimate.
	reqHeaderCount, reqHeaderBytes uint64
	bodyBytes                      uint64
	uri, host, client              int

	// drained reports that no unread request-body data remains on the
	// network, which makes a bounded FIFO slot wait eligible (ADR 0032 §3).
	drained bool
}

// preparedRequestBody is implemented by a body an earlier inspecting layer
// already read completely from the network (a plugin's prepared body).
type preparedRequestBody interface {
	InspectionPrepared() bool
}

func bodyDrained(r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return true
	}
	p, ok := r.Body.(preparedRequestBody)
	return ok && p.InspectionPrepared()
}

// admit acquires or reuses the request lease and reserves R5's fixed term
// before any WAF transaction exists. A refusal is answered here and reported
// as ok=false. done must run after the transaction is closed.
func (f *Firewall) admit(w http.ResponseWriter, r *http.Request) (a *wafAdmission, rr *http.Request, done func(), ok bool) {
	a = &wafAdmission{ctx: r.Context(), drained: bodyDrained(r)}
	done = func() {}
	gov := f.opts.Governor
	if gov == nil {
		return a, r, done, true
	}
	lease, ctx, owner, err := gov.AcquireLease(r.Context())
	if err != nil {
		writeAdmissionError(w, r, err)
		return nil, r, done, false
	}
	if owner {
		r = r.WithContext(ctx)
		done = func() { _ = lease.Close() }
	}
	limits := gov.Limits()
	a.gov, a.lease, a.ctx, a.limits, a.mode = gov, lease, r.Context(), limits, limits.Mode
	if f.audit.writer.hasOutput() {
		if err := gov.AdmitAuditOutput(); err != nil {
			writeAdmissionError(w, r, err)
			done()
			return nil, r, func() {}, false
		}
	}
	if _, err := a.charge(inspection.AuditBytes, inspection.WAFR5FixedBytes); err != nil {
		writeAdmissionError(w, r, err)
		done()
		return nil, r, func() {}, false
	}
	return a, r, done, true
}

// writeAdmissionError answers a governor refusal or a body-arrival failure
// with the shared inspection responses (ADR 0032 §5).
func writeAdmissionError(w http.ResponseWriter, r *http.Request, err error) int {
	switch {
	case inspection.IsOverload(err):
		inspection.WriteOverloaded(w, r)
		if inspection.IsGRPCRequest(r) {
			return http.StatusOK
		}
		return http.StatusServiceUnavailable
	case errors.Is(err, inspection.ErrBodyTimeout):
		inspection.WriteBodyTimeout(w, r)
		if inspection.IsGRPCRequest(r) {
			return http.StatusOK
		}
		return http.StatusRequestTimeout
	default:
		if r.ProtoMajor < 2 {
			// The body may be unread and nothing bounds an HTTP/1 read
			// here, so close instead of letting net/http drain it.
			w.Header().Set("Connection", "close")
		}
		http.Error(w, "inspection error", http.StatusInternalServerError)
		return http.StatusInternalServerError
	}
}

// refusal classifies an admission failure for the WAF transaction outcome.
func refusal(err error) (category string, status int) {
	switch {
	case inspection.IsOverload(err):
		return inspection.ReasonOverloaded, http.StatusServiceUnavailable
	case errors.Is(err, inspection.ErrBodyTimeout):
		return inspection.ReasonBodyTimeout, http.StatusRequestTimeout
	case errors.Is(err, inspection.ErrDeadlineUnsupported):
		return "deadline_unsupported", http.StatusInternalServerError
	default:
		return "inspection_admission_error", http.StatusInternalServerError
	}
}

func (a *wafAdmission) governed() bool { return a != nil && a.gov != nil }

func (a *wafAdmission) maxResponseHeaderBytes() uint64 {
	if a.governed() {
		if n := a.limits.MaxResponseHeaderBytes; n > 0 {
			return n
		}
	}
	return uint64(config.InspectionDefaultMaxResponseHeaderBytes)
}

func (a *wafAdmission) charge(class inspection.ByteClass, n uint64) (*inspection.Charge, error) {
	if !a.governed() {
		return nil, nil
	}
	c, err := a.lease.Charge(class, n)
	if err != nil {
		return nil, err
	}
	a.charges = append(a.charges, c)
	return c, nil
}

// chargeBuffer grows the transaction's buffer reservation by n before the
// represented bytes are allocated.
func (a *wafAdmission) chargeBuffer(n uint64) error {
	if !a.governed() || n == 0 {
		return nil
	}
	if a.buf == nil {
		c, err := a.lease.Charge(inspection.BufferBytes, n)
		if err != nil {
			return err
		}
		a.buf, a.bufAmount = c, n
		return nil
	}
	next := a.bufAmount + n
	if next < a.bufAmount {
		return &inspection.AdmissionError{Outcome: inspection.InvalidAccounting}
	}
	if err := a.buf.Resize(next); err != nil {
		return err
	}
	a.bufAmount = next
	return nil
}

func (a *wafAdmission) refundBuffer(n uint64) {
	if !a.governed() || a.buf == nil || n == 0 {
		return
	}
	n = min(n, a.bufAmount)
	if a.buf.Resize(a.bufAmount-n) == nil {
		a.bufAmount -= n
	}
}

// reserveState takes (or grows) a cumulative waf_state reservation and
// records its amount for a later true-down.
func (a *wafAdmission) reserveState(slot **inspection.Charge, amount *uint64, n uint64) error {
	if !a.governed() {
		return nil
	}
	if *slot == nil {
		c, err := a.lease.Charge(inspection.WAFStateBytes, n)
		if err != nil {
			return err
		}
		*slot, *amount = c, n
		return nil
	}
	if n <= *amount {
		return nil
	}
	if err := (*slot).Resize(n); err != nil {
		return err
	}
	*amount = n
	return nil
}

// trueDown lowers a phase reservation to its calibrated held-state estimate
// after the call returned. It never raises one.
func (a *wafAdmission) trueDown(c *inspection.Charge, amount *uint64, n uint64) {
	if !a.governed() || c == nil || n >= *amount {
		return
	}
	if c.Resize(n) == nil {
		*amount = n
	}
}

// evaluate runs one rule-evaluating Coraza call: it acquires a slot (a
// bounded FIFO wait only when no unread request-body data remains), then the
// phase reservation, and holds both until the synchronous call returns.
func (a *wafAdmission) evaluate(wait bool, reserve func() error, call func()) error {
	if !a.governed() {
		call()
		return nil
	}
	acquire := a.gov.TryEvaluation
	if wait {
		acquire = a.gov.AcquireEvaluation
	}
	token, _, err := acquire(a.ctx, a.lease)
	if err != nil {
		return err
	}
	defer func() { _ = token.Release() }()
	if reserve != nil {
		if err := reserve(); err != nil {
			return err
		}
	}
	call()
	return nil
}

// logging acquires the phase-5 slot. Phase 5 is never refused: overflow runs
// unslotted and is counted by the governor.
func (a *wafAdmission) logging() func() {
	if !a.governed() {
		return func() {}
	}
	policy := inspection.LoggingTry
	if a.drained {
		policy = inspection.LoggingWait
	}
	token, _, err := a.gov.AcquireLoggingEvaluation(a.ctx, a.lease, policy)
	if err != nil || token == nil {
		return func() {}
	}
	return func() { _ = token.Release() }
}

// release refunds every WAF-owned reservation. It runs only after tx.Close,
// when the represented transaction state and buffers are unreachable.
func (a *wafAdmission) release() {
	if !a.governed() {
		return
	}
	if a.txID != "" {
		auditCtxs.Delete(a.txID)
	}
	if a.audit != nil {
		a.audit.release()
	}
	for _, c := range []*inspection.Charge{a.r1, a.r2, a.r3, a.r4, a.buf} {
		if c != nil {
			_ = c.Release()
		}
	}
	for _, c := range a.charges {
		_ = c.Release()
	}
	a.r1, a.r2, a.r3, a.r4, a.buf, a.charges = nil, nil, nil, nil, nil, nil
}

// requestHeaderShape returns the header entries and bytes Jul adds to the
// transaction, plus the H term of R1 (URI, header and host bytes).
func requestHeaderShape(r *http.Request, uri string) (count, bytes, h uint64) {
	for k, vs := range r.Header {
		for _, v := range vs {
			count++
			bytes += uint64(len(k) + len(v))
		}
	}
	if r.Host != "" {
		count++
		bytes += uint64(len("Host") + len(r.Host))
	}
	for _, te := range r.TransferEncoding {
		count++
		bytes += uint64(len("Transfer-Encoding") + len(te))
	}
	return count, bytes, bytes + uint64(len(uri)+len(r.Host))
}

func responseHeaderShape(h http.Header) uint64 {
	var n uint64
	for name, values := range h {
		for _, value := range values {
			n = satAdd(n, uint64(len(name)), uint64(len(value)), 64)
		}
	}
	return n
}

// queryPairs bounds the ARGS_GET entries ProcessURI can parse.
func queryPairs(rawQuery string) uint64 {
	if rawQuery == "" {
		return 0
	}
	return uint64(strings.Count(rawQuery, "&")+strings.Count(rawQuery, ";")) + 1
}

func satMul(a, b uint64) uint64 {
	if a != 0 && b > ^uint64(0)/a {
		return ^uint64(0)
	}
	return a * b
}

func satAdd(vs ...uint64) uint64 {
	var n uint64
	for _, v := range vs {
		if n+v < n {
			return ^uint64(0)
		}
		n += v
	}
	return n
}

// r1Estimate is R1 for a held ARGS_GET count (ADR 0032 §4).
func r1Estimate(args, h uint64) uint64 {
	return satAdd(inspection.WAFStateBaseBytes, satMul(inspection.WAFArgumentBytes, args), satMul(inspection.WAFByteFactor, h))
}

// r2Estimate is R2 for a held ARGS_POST count and B_total.
func r2Estimate(args, bTotal uint64) uint64 {
	return satAdd(satMul(inspection.WAFArgumentBytes, args), satMul(inspection.WAFByteFactor, bTotal))
}

func r3Estimate(headerBytes uint64) uint64 {
	return satMul(inspection.WAFByteFactor, headerBytes)
}

func r4Estimate(args, responseBytes uint64) uint64 {
	return satAdd(satMul(inspection.WAFArgumentBytes, args), satMul(inspection.WAFByteFactor, responseBytes))
}

// txState exposes Coraza's experimental transaction state behind Jul's
// wrappers. If it is unavailable, reservations stay at their worst case.
func txState(tx types.Transaction) (plugintypes.TransactionState, bool) {
	cur := tx
	for {
		if s, ok := cur.(plugintypes.TransactionState); ok {
			return s, true
		}
		u, ok := cur.(unwrappedTx)
		if !ok {
			return nil, false
		}
		cur = u.Unwrap()
	}
}

func argsHeld(tx types.Transaction, get bool) (uint64, bool) {
	s, ok := txState(tx)
	if !ok {
		return 0, false
	}
	v := s.Variables()
	if get {
		return uint64(len(v.ArgsGet().FindAll())), true
	}
	return uint64(len(v.ArgsPost().FindAll())), true
}

func responseArgsHeld(tx types.Transaction) (uint64, bool) {
	s, ok := txState(tx)
	if !ok {
		return 0, false
	}
	return uint64(len(s.Variables().ResponseArgs().FindAll())), true
}

// txResponseBodyLimit returns the effective per-transaction limit after
// phase 3, falling back to the compiled limit if the Coraza wrapper changes.
func txResponseBodyLimit(tx types.Transaction, fallback int64) int64 {
	v, ok := innerTx(tx)
	if !ok {
		return fallback
	}
	f := v.FieldByName("ResponseBodyLimit")
	if !f.IsValid() || f.Kind() != reflect.Int64 {
		return fallback
	}
	return f.Int()
}

// txRequestBodyLimit is the transaction's effective request body limit after
// phase 1 (a ctl:requestBodyLimit can change it per transaction).
func txRequestBodyLimit(tx types.Transaction, fallback int64) int64 {
	v, ok := innerTx(tx)
	if !ok {
		return fallback
	}
	f := v.FieldByName("RequestBodyLimit")
	if !f.IsValid() || f.Kind() != reflect.Int64 {
		return fallback
	}
	return f.Int()
}

// bodyRead is the result of Jul's request-body read loop.
type bodyRead struct {
	it        *types.Interruption
	written   int64
	leftover  []byte
	readErr   error // client read failure (413 for MaxBytesError, else 400)
	writeErr  error
	admission error // governor refusal or body-arrival failure
}

// readRequestBody replaces Coraza's ReadRequestBodyFrom (ADR 0032 §3). It
// reads network chunks without an evaluation slot under the body-arrival
// deadline, charges buffered bytes before each write, and takes a slot and
// R2 only for the write that can reach the body limit, where
// ProcessPartial evaluates phase 2 implicitly. Like ReadRequestBodyFrom it
// reads at most up to the limit, leaving the rest of the body unread.
func (f *Firewall) readRequestBody(w http.ResponseWriter, r *http.Request, tx types.Transaction, a *wafAdmission) (res bodyRead, phase2 bool) {
	limit := txRequestBodyLimit(tx, f.state.requestBodyLimit)
	if limit <= 0 {
		return res, false
	}
	inMemory := f.state.requestBodyInMemoryLimit
	if inMemory <= 0 || inMemory > limit {
		inMemory = limit
	}
	if a.governed() {
		owner := reqdeadline.From(r.Context())
		if owner == nil {
			owner = reqdeadline.NewForRequest(w, r)
			defer owner.End()
		}
		owner.InterruptWith(r.Body)
		if err := owner.BeginBodyRead(a.limits.BodyTimeout, a.limits.Mode == inspection.Observe, a.gov.RecordBodyTimeout); err != nil {
			owner.EndBodyRead()
			res.admission = inspection.ErrDeadlineUnsupported
			if !errors.Is(err, reqdeadline.ErrUnsupported) {
				res.admission = err
			}
			return res, false
		}
		defer func() {
			switch owner.EndBodyRead() {
			case reqdeadline.BodyTimedOut:
				res = bodyRead{admission: inspection.ErrBodyTimeout}
				phase2 = false
			case reqdeadline.BodyUnsupported:
				res = bodyRead{admission: inspection.ErrDeadlineUnsupported}
				phase2 = false
			}
		}()
	}

	size := int64(readBufferBytes)
	if r.ContentLength >= 0 && r.ContentLength < size {
		size = r.ContentLength + 1
	}
	size = min(size, limit)
	if err := a.chargeBuffer(uint64(size)); err != nil {
		res.admission = err
		return res, false
	}
	buf := make([]byte, size)
	defer func() {
		if len(res.leftover) == 0 {
			// The scratch array is unreachable once the loop returns
			// without forwarding part of it.
			buf = nil
			a.refundBuffer(uint64(size))
		}
	}()

	for res.written < limit {
		n, rerr := r.Body.Read(buf[:min(int64(len(buf)), limit-res.written)])
		if n > 0 {
			chunk := buf[:n]
			keep := min(int64(n), max(0, inMemory-res.written))
			if err := a.chargeBuffer(2 * uint64(keep)); err != nil {
				res.admission = err
				return res, false
			}
			reaches := res.written+int64(n) >= limit
			var (
				it  *types.Interruption
				wn  int
				err error
			)
			write := func() { it, wn, err = tx.WriteRequestBody(chunk) }
			if reaches && !f.state.requestBodyLimitReject {
				// Implicit phase 2 under ProcessPartial. Unread network
				// data may remain, so the slot is only try-acquired.
				worst := r2Estimate(f.argumentLimit(), uint64(limit))
				if aerr := a.evaluate(false, func() error { return a.reserveState(&a.r2, &a.r2Amount, worst) }, write); aerr != nil {
					res.admission = aerr
					return res, false
				}
				phase2 = true
			} else {
				write()
			}
			res.written += int64(wn)
			res.it, res.writeErr = it, err
			if it != nil || err != nil {
				return res, phase2
			}
			// Bytes Coraza did not take are forwarded, never dropped.
			if reaches || wn < n {
				if wn < n {
					res.leftover = chunk[wn:]
				}
				return res, phase2
			}
		}
		if rerr == io.EOF {
			a.drained = true
			return res, phase2
		}
		if rerr != nil {
			res.readErr = rerr
			return res, phase2
		}
	}
	return res, phase2
}

func (f *Firewall) argumentLimit() uint64 {
	if f.state.argumentLimit <= 0 {
		return 0
	}
	return uint64(f.state.argumentLimit)
}
