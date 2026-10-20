// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package respwriter

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ClientCertVerifier evaluates cs against the listener's current live
// client_auth policy (mode, CA pool, CRLs, and SAN allow-list) and the
// location's require_client_cert flag on every request (#642, #563).
type ClientCertVerifier func(cs *tls.ConnectionState, locRequire bool) (bool, error)

type clientCertVerifierCtxKey struct{}

// WithClientCertVerifier attaches verifier to ctx.
func WithClientCertVerifier(ctx context.Context, verifier ClientCertVerifier) context.Context {
	if verifier == nil {
		return ctx
	}
	return context.WithValue(ctx, clientCertVerifierCtxKey{}, verifier)
}

// ClientCertVerifierFrom extracts the per-request ClientCertVerifier from ctx.
func ClientCertVerifierFrom(ctx context.Context) ClientCertVerifier {
	fn, _ := ctx.Value(clientCertVerifierCtxKey{}).(ClientCertVerifier)
	return fn
}

// DefaultRetirementGrace and DefaultRetirementBound are the server-enforced
// §8.4 / #642 / #563 retirement deadlines from Publish: a 1s maximum graceful
// protocol close window and a 2s absolute forced-closure deadline.
const (
	DefaultRetirementGrace = 1 * time.Second
	DefaultRetirementBound = 2 * time.Second

	// RFC 6455 §7.4.1 status codes originated by Jul on owned H1 WebSockets (#562).
	WSCloseGoingAway       uint16 = 1001
	WSClosePolicyViolation uint16 = 1008
)

// EntryCoord identifies a route location in a specific handler generation.
type EntryCoord struct {
	GenID         uint64
	ListenAddr    string
	ServerIndex   int
	LocationIndex int
	RouteID       string
}

type blockLocCoord struct {
	ServerIndex   int
	LocationIndex int
}

// EntryTransition describes how an entry in the retiring generation maps to
// the newly published generation, and whether it must be retired.
type EntryTransition struct {
	FromServerIndex   int
	FromLocationIndex int
	FromRouteID       string

	// Removed is true when the entry no longer exists in the new generation.
	Removed         bool
	ToServerIndex   int
	ToLocationIndex int
	ToRouteID       string

	// Retire is true when open exchanges on this entry must be ended.
	Retire bool
	// Mandatory is true when retirement is triggered by a provable narrowing,
	// withdrawal gate, or entry removal (aborting streams immediately at Publish),
	// and false when triggered solely by an uncertain change (which grants in-flight
	// HTTP requests the 1s graceful drain window before forced abort at 1s..2s).
	Mandatory bool
	// KeepOpen is true when an eligible uncertain change was exempted via
	// keep_open_entries (SD-09). It never disarms an already-armed deadline.
	KeepOpen bool
	// CloseCode is the RFC 6455 WebSocket status code sent when retiring owned
	// H1 WebSocket sessions for this transition (1008 Policy Violation on
	// narrowing/withdrawal, 1001 Going Away when the listener itself is removed;
	// defaults to WSClosePolicyViolation when 0).
	CloseCode uint16
	// DisplayLabel is the human-readable entry label for retirement reporting.
	DisplayLabel string
}

func (tr EntryTransition) closeCode() uint16 {
	if tr.CloseCode != 0 {
		return tr.CloseCode
	}
	return WSClosePolicyViolation
}

// EntryRetirementResult reports the bounded outcome of retiring one entry's
// open exchanges on a publish (spec §8.4, ADR 0026 #642 amendment).
type EntryRetirementResult struct {
	ServerIndex          int    `json:"server_index"`
	LocationIndex        int    `json:"location_index"`
	RouteID              string `json:"route_id,omitempty"`
	Removed              bool   `json:"removed,omitempty"`
	DisplayLabel         string `json:"display_label,omitempty"`
	EndedExchanges       int    `json:"ended_exchanges"`
	GracefulCount        int    `json:"graceful_count"`
	ForcedCount          int    `json:"forced_count"`
	CompletedWithinBound bool   `json:"completed_within_bound"`
	CollateralCount      int    `json:"collateral_count"`
	KeepOpen             bool   `json:"keep_open,omitempty"`
	RetirementBound      string `json:"retirement_bound"`
	Claim                string `json:"claim,omitempty"`
}

type entryTracker struct {
	tr        EntryTransition
	exchanges []*ActiveExchange
}

type retirementBatch struct {
	mu      sync.Mutex
	pending int
	doneCh  chan struct{}
}

func newRetirementBatch() *retirementBatch {
	ch := make(chan struct{})
	close(ch)
	return &retirementBatch{doneCh: ch}
}

func (b *retirementBatch) add() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.pending == 0 {
		b.doneCh = make(chan struct{})
	}
	b.pending++
	b.mu.Unlock()
}

func (b *retirementBatch) done() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.pending > 0 {
		b.pending--
		if b.pending == 0 {
			close(b.doneCh)
		}
	}
	b.mu.Unlock()
}

func (b *retirementBatch) wait(deadline time.Time) {
	if b == nil {
		return
	}
	for {
		b.mu.Lock()
		p := b.pending
		ch := b.doneCh
		b.mu.Unlock()
		if p == 0 {
			return
		}
		waitDur := time.Until(deadline) + 50*time.Millisecond
		if waitDur <= 0 {
			return
		}
		select {
		case <-ch:
		case <-time.After(waitDur):
			return
		}
	}
}

type genStep struct {
	fromGen         uint64
	toGen           uint64
	publishTime     time.Time
	grace           time.Duration
	bound           time.Duration
	byCoord         map[blockLocCoord]EntryTransition
	byRouteID       map[string]EntryTransition
	trackersByCoord map[blockLocCoord]*entryTracker
	trackersByID    map[string]*entryTracker
	batch           *retirementBatch
	recompute       func()
}

type exchangeCtxKey struct{}
type genIDCtxKey struct{}
type exchangeSlotCtxKey struct{}

// WithExchangeRegistry attaches reg and genID to ctx so Router.For and
// proxy handlers can register active exchanges and 101 upgraded sessions.
func WithExchangeRegistry(ctx context.Context, reg *ExchangeRegistry, genID uint64) context.Context {
	if reg == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, exchangeCtxKey{}, reg)
	ctx = context.WithValue(ctx, genIDCtxKey{}, genID)
	ctx = context.WithValue(ctx, exchangeSlotCtxKey{}, &atomic.Pointer[ActiveExchange]{})
	return ctx
}

// ExchangeRegistryFrom extracts the *ExchangeRegistry and generation ID from ctx.
func ExchangeRegistryFrom(ctx context.Context) (*ExchangeRegistry, uint64) {
	reg, _ := ctx.Value(exchangeCtxKey{}).(*ExchangeRegistry)
	genID, _ := ctx.Value(genIDCtxKey{}).(uint64)
	return reg, genID
}

type activeExchangeCtxKey struct{}

// ActiveExchangeFrom extracts the current request's *ActiveExchange from ctx.
func ActiveExchangeFrom(ctx context.Context) *ActiveExchange {
	if ex, ok := ctx.Value(activeExchangeCtxKey{}).(*ActiveExchange); ok && ex != nil {
		return ex
	}
	if slot, ok := ctx.Value(exchangeSlotCtxKey{}).(*atomic.Pointer[ActiveExchange]); ok && slot != nil {
		return slot.Load()
	}
	return nil
}

// ExchangeRegistry owns active HTTP/1.1, HTTP/2, HTTP/3 streams and hijacked
// HTTP/1.1 101 upgraded sessions across handler generations (#642, #563, #562).
type ExchangeRegistry struct {
	mu              sync.Mutex
	nextID          uint64
	publishedGen    uint64
	shuttingDown    bool
	closedAddrAtGen map[string]uint64
	active          map[uint64]*ActiveExchange
	history         []genStep

	Grace time.Duration
	Bound time.Duration

	latestPublishSeq uint64
	lastResults      []EntryRetirementResult
	lastRecomputeFn  func()
}

// NewExchangeRegistry constructs a process-lifetime exchange and upgrade registry.
func NewExchangeRegistry() *ExchangeRegistry {
	return &ExchangeRegistry{
		closedAddrAtGen: make(map[string]uint64),
		active:          make(map[uint64]*ActiveExchange),
		Grace:           DefaultRetirementGrace,
		Bound:           DefaultRetirementBound,
	}
}

func (r *ExchangeRegistry) graceDuration() time.Duration {
	if r != nil && r.Grace > 0 {
		return r.Grace
	}
	return DefaultRetirementGrace
}

func (r *ExchangeRegistry) boundDuration() time.Duration {
	if r != nil && r.Bound > 0 {
		return r.Bound
	}
	return DefaultRetirementBound
}

// SetInitialGeneration records the startup handler generation ID.
func (r *ExchangeRegistry) SetInitialGeneration(genID uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.publishedGen == 0 {
		r.publishedGen = genID
	}
	r.mu.Unlock()
}

// SnapshotCounts returns the current count of open exchanges per (serverIndex, locationIndex)
// and per literal route_id in the currently published generation.
func (r *ExchangeRegistry) SnapshotCounts() (byCoord map[[2]int]int, byRouteID map[string]int) {
	byCoord = make(map[[2]int]int)
	byRouteID = make(map[string]int)
	if r == nil {
		return byCoord, byRouteID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ex := range r.active {
		if ex.closed.Load() {
			continue
		}
		key := [2]int{ex.curCoord.ServerIndex, ex.curCoord.LocationIndex}
		byCoord[key]++
		if ex.curCoord.RouteID != "" {
			byRouteID[ex.curCoord.RouteID]++
		}
	}
	return byCoord, byRouteID
}

// LastRetirementResults returns the most recent publish's per-entry retirement results.
func (r *ExchangeRegistry) LastRetirementResults() []EntryRetirementResult {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EntryRetirementResult, len(r.lastResults))
	copy(out, r.lastResults)
	return out
}

// ActiveCount returns the total number of active exchanges and owned upgrades.
func (r *ExchangeRegistry) ActiveCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

// Admit registers an incoming request on coord, synchronizing with Publish so
// that a request that acquired an older generation before Publish either
// advances to the current generation's coordinates or immediately inherits the
// original Publish retirement deadline (ADR 0026 #642 amendment Point 2).
func (r *ExchangeRegistry) Admit(w http.ResponseWriter, req *http.Request, coord EntryCoord) (http.ResponseWriter, *http.Request, *ActiveExchange, func()) {
	if r == nil {
		return w, req, nil, func() {}
	}
	exCtx, exCancel := context.WithCancel(req.Context())
	var rawConn net.Conn
	if stc, ok := req.Context().Value(sendTimeoutContextKey{}).(*sendTimeoutConnection); ok {
		rawConn = stc.Conn
	}
	ex := &ActiveExchange{
		reg:        r,
		origCoord:  coord,
		curCoord:   coord,
		protoMajor: req.ProtoMajor,
		reqMethod:  req.Method,
		reqUpgrade: strings.TrimSpace(req.Header.Get("Upgrade")),
		ctx:        exCtx,
		cancel:     exCancel,
		rawH1Conn:  rawConn,
		controller: http.NewResponseController(w),
		doneCh:     make(chan struct{}),
	}

	var immediateRetire bool
	var immediateGrace time.Time
	var immediateBound time.Time
	var immediateCode uint16
	var lateBatches []*retirementBatch
	var lateRecomputes []func()

	r.mu.Lock()
	r.nextID++
	ex.id = r.nextID
	if r.publishedGen == 0 && coord.GenID != 0 {
		r.publishedGen = coord.GenID
	}

	// Walk any generation transitions that occurred after coord.GenID was acquired.
	cur := coord
	removed := false
	for _, step := range r.history {
		if step.fromGen != cur.GenID {
			continue
		}
		cKey := blockLocCoord{cur.ServerIndex, cur.LocationIndex}
		tr, ok := step.byCoord[cKey]
		if !ok && cur.RouteID != "" {
			tr, ok = step.byRouteID[cur.RouteID]
		}
		if !ok || tr.Removed || tr.Retire {
			immediateRetire = true
			g := step.publishTime.Add(step.grace)
			b := step.publishTime.Add(step.bound)
			if immediateGrace.IsZero() || g.Before(immediateGrace) {
				immediateGrace = g
			}
			if immediateBound.IsZero() || b.Before(immediateBound) {
				immediateBound = b
			}
			if ok {
				immediateCode = tr.closeCode()
				et := step.trackersByCoord[cKey]
				if et == nil && cur.RouteID != "" {
					et = step.trackersByID[cur.RouteID]
				}
				if et != nil {
					et.exchanges = append(et.exchanges, ex)
				}
			} else {
				immediateCode = WSClosePolicyViolation
			}
			if step.batch != nil {
				lateBatches = append(lateBatches, step.batch)
			}
			if step.recompute != nil {
				lateRecomputes = append(lateRecomputes, step.recompute)
			}
			if !ok || tr.Removed {
				removed = true
				break
			}
		}
		if !tr.Removed {
			cur = EntryCoord{
				GenID:         step.toGen,
				ListenAddr:    cur.ListenAddr,
				ServerIndex:   tr.ToServerIndex,
				LocationIndex: tr.ToLocationIndex,
				RouteID:       tr.ToRouteID,
			}
		}
	}
	// F7 / R2 invariant: if coord.GenID is an older generation (< r.publishedGen)
	// whose transition chain predates the retained history window and could not
	// be advanced to r.publishedGen, fail closed immediately. A request carrying
	// a newly stored handler generation (coord.GenID > r.publishedGen) is
	// already evaluating the new policy and must not be treated as stale.
	if !removed && coord.GenID != 0 && r.publishedGen != 0 && cur.GenID < r.publishedGen {
		immediateRetire = true
		now := time.Now()
		if immediateGrace.IsZero() || now.Before(immediateGrace) {
			immediateGrace = now
		}
		if immediateBound.IsZero() || now.Before(immediateBound) {
			immediateBound = now
		}
		if immediateCode == 0 {
			immediateCode = WSClosePolicyViolation
		}
	}
	ex.curCoord = cur

	closedGen, addrClosed := r.closedAddrAtGen[coord.ListenAddr]
	if r.shuttingDown || (addrClosed && (coord.GenID == 0 || coord.GenID <= closedGen)) {
		immediateRetire = true
		now := time.Now()
		g := now.Add(r.graceDuration())
		b := now.Add(r.boundDuration())
		if immediateGrace.IsZero() || g.Before(immediateGrace) {
			immediateGrace = g
		}
		if immediateBound.IsZero() || b.Before(immediateBound) {
			immediateBound = b
		}
		if immediateCode == 0 {
			immediateCode = WSCloseGoingAway
		}
	}

	r.active[ex.id] = ex
	r.mu.Unlock()

	if immediateRetire {
		ex.armRetirement(immediateGrace, immediateBound, immediateCode, true, lateBatches...)
		for _, fn := range lateRecomputes {
			fn()
		}
	}

	ew := &exchangeResponseWriter{
		ResponseWriter: w,
		ex:             ex,
	}
	wrappedW := Wrap(ew, w)
	if slot, ok := req.Context().Value(exchangeSlotCtxKey{}).(*atomic.Pointer[ActiveExchange]); ok && slot != nil {
		slot.Store(ex)
	}
	reqWithEx := req.WithContext(context.WithValue(exCtx, activeExchangeCtxKey{}, ex))

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			ex.finishRequest()
		})
	}
	return wrappedW, reqWithEx, ex, release
}

// PublishGeneration atomically advances surviving exchanges from fromGen (and
// any older generations mapped to fromGen) to toGen and arms retirement on
// every exchange belonging to an entry with Retire == true or Removed == true.
// It returns a closure that waits up to Bound (2s) for all armed retirements of
// this publish to complete and returns the per-entry retirement results.
func (r *ExchangeRegistry) PublishGeneration(fromGen, toGen uint64, transitions []EntryTransition) func() []EntryRetirementResult {
	if r == nil {
		return func() []EntryRetirementResult { return nil }
	}
	now := time.Now()
	grace := r.graceDuration()
	bound := r.boundDuration()
	graceDeadline := now.Add(grace)
	boundDeadline := now.Add(bound)

	byCoord := make(map[blockLocCoord]EntryTransition, len(transitions))
	byRouteID := make(map[string]EntryTransition, len(transitions))
	for _, tr := range transitions {
		byCoord[blockLocCoord{tr.FromServerIndex, tr.FromLocationIndex}] = tr
		if tr.FromRouteID != "" {
			byRouteID[tr.FromRouteID] = tr
		}
	}

	trackers := make([]*entryTracker, 0, len(transitions))
	trackerByFromCoord := make(map[blockLocCoord]*entryTracker, len(transitions))
	trackerByRouteID := make(map[string]*entryTracker, len(transitions))
	for _, tr := range transitions {
		if !tr.Retire && !tr.Removed && !tr.KeepOpen {
			continue
		}
		et := &entryTracker{tr: tr}
		trackers = append(trackers, et)
		trackerByFromCoord[blockLocCoord{tr.FromServerIndex, tr.FromLocationIndex}] = et
		if tr.FromRouteID != "" {
			trackerByRouteID[tr.FromRouteID] = et
		}
	}

	type armItem struct {
		ex        *ActiveExchange
		mandatory bool
		code      uint16
	}
	batch := newRetirementBatch()
	var toArm []armItem

	var pubSeq uint64
	computeResults := func() []EntryRetirementResult {
		results := make([]EntryRetirementResult, 0, len(trackers))
		for _, et := range trackers {
			res := EntryRetirementResult{
				ServerIndex:          et.tr.ToServerIndex,
				LocationIndex:        et.tr.ToLocationIndex,
				RouteID:              et.tr.ToRouteID,
				Removed:              et.tr.Removed,
				DisplayLabel:         et.tr.DisplayLabel,
				CompletedWithinBound: true,
				KeepOpen:             et.tr.KeepOpen && !et.tr.Retire && !et.tr.Removed,
				RetirementBound:      "2s",
			}
			if et.tr.Removed {
				res.ServerIndex = et.tr.FromServerIndex
				res.LocationIndex = et.tr.FromLocationIndex
				res.RouteID = et.tr.FromRouteID
			}
			r.mu.Lock()
			exList := append([]*ActiveExchange(nil), et.exchanges...)
			r.mu.Unlock()
			for _, ex := range exList {
				graceful, forced, withinBound, collateral := ex.outcome(boundDeadline)
				if graceful {
					res.GracefulCount++
				} else if forced {
					res.ForcedCount++
				}
				if !withinBound {
					res.CompletedWithinBound = false
				}
				res.CollateralCount += collateral
			}
			res.EndedExchanges = res.GracefulCount + res.ForcedCount
			switch {
			case res.KeepOpen:
				res.Claim = "new access updated; open connections kept by operator exception"
			case !res.CompletedWithinBound:
				res.Claim = "new access stopped; open connections not confirmed ended within bound"
			case res.CollateralCount > 0:
				res.Claim = "new access stopped; open connections ended with collateral interruption"
			default:
				res.Claim = "complete_withdrawal"
			}
			results = append(results, res)
		}

		r.mu.Lock()
		if r.latestPublishSeq == pubSeq {
			r.lastResults = results
		}
		r.mu.Unlock()
		return results
	}
	recomputeFn := func() {
		_ = computeResults()
	}

	r.mu.Lock()
	if fromGen == 0 {
		fromGen = r.publishedGen
	}
	r.publishedGen = toGen
	r.latestPublishSeq++
	pubSeq = r.latestPublishSeq
	r.lastRecomputeFn = recomputeFn
	r.history = append(r.history, genStep{
		fromGen:         fromGen,
		toGen:           toGen,
		publishTime:     now,
		grace:           grace,
		bound:           bound,
		byCoord:         byCoord,
		byRouteID:       byRouteID,
		trackersByCoord: trackerByFromCoord,
		trackersByID:    trackerByRouteID,
		batch:           batch,
		recompute:       recomputeFn,
	})
	if len(r.history) > 32 {
		r.history = append([]genStep(nil), r.history[len(r.history)-32:]...)
	}

	for _, ex := range r.active {
		if ex.closed.Load() || (toGen != 0 && ex.curCoord.GenID >= toGen) {
			continue
		}
		cKey := blockLocCoord{ex.curCoord.ServerIndex, ex.curCoord.LocationIndex}
		tr, ok := byCoord[cKey]
		if !ok && ex.curCoord.RouteID != "" {
			tr, ok = byRouteID[ex.curCoord.RouteID]
		}
		if !ok {
			continue
		}
		if tr.Retire || tr.Removed {
			et := trackerByFromCoord[cKey]
			if et == nil && ex.curCoord.RouteID != "" {
				et = trackerByRouteID[ex.curCoord.RouteID]
			}
			if et != nil {
				et.exchanges = append(et.exchanges, ex)
			}
			toArm = append(toArm, armItem{ex: ex, mandatory: tr.Mandatory || tr.Removed, code: tr.closeCode()})
		}
		if !tr.Removed {
			ex.curCoord = EntryCoord{
				GenID:         toGen,
				ListenAddr:    ex.curCoord.ListenAddr,
				ServerIndex:   tr.ToServerIndex,
				LocationIndex: tr.ToLocationIndex,
				RouteID:       tr.ToRouteID,
			}
		}
	}
	r.mu.Unlock()

	for _, item := range toArm {
		immediate := item.mandatory || item.ex.hasWrittenHeader()
		item.ex.armRetirement(graceDeadline, boundDeadline, item.code, immediate, batch)
	}
	_ = computeResults()

	return func() []EntryRetirementResult {
		batch.wait(boundDeadline)
		return computeResults()
	}
}

// DrainAddr initiates graceful/forced retirement for all active exchanges and
// owned H1 upgraded sessions on listenAddr (used when a listener is removed on
// reload, #562).
func (r *ExchangeRegistry) DrainAddr(ctx context.Context, listenAddr string) {
	if r == nil {
		return
	}
	grace, bound := r.shutdownDeadlines(ctx)
	batch := newRetirementBatch()
	var toArm []*ActiveExchange

	r.mu.Lock()
	r.closedAddrAtGen[listenAddr] = r.publishedGen
	for _, ex := range r.active {
		if !ex.closed.Load() && ex.curCoord.ListenAddr == listenAddr {
			toArm = append(toArm, ex)
		}
	}
	r.mu.Unlock()

	if len(toArm) == 0 {
		return
	}
	for _, ex := range toArm {
		ex.armRetirement(grace, bound, WSCloseGoingAway, true, batch)
	}
	waitBatchContext(ctx, batch, bound)
}

// DrainAll initiates graceful/forced retirement for all active exchanges and
// owned H1 upgraded sessions across all listeners (used on server shutdown, #562).
func (r *ExchangeRegistry) DrainAll(ctx context.Context) {
	if r == nil {
		return
	}
	grace, bound := r.shutdownDeadlines(ctx)
	batch := newRetirementBatch()
	var toArm []*ActiveExchange

	r.mu.Lock()
	r.shuttingDown = true
	for _, ex := range r.active {
		if !ex.closed.Load() {
			toArm = append(toArm, ex)
		}
	}
	r.mu.Unlock()

	if len(toArm) == 0 {
		return
	}
	for _, ex := range toArm {
		ex.armRetirement(grace, bound, WSCloseGoingAway, true, batch)
	}
	waitBatchContext(ctx, batch, bound)
}

func (r *ExchangeRegistry) shutdownDeadlines(ctx context.Context) (time.Time, time.Time) {
	now := time.Now()
	graceDur := r.graceDuration()
	boundDur := r.boundDuration()
	if dl, ok := ctx.Deadline(); ok {
		rem := time.Until(dl)
		if rem <= 0 {
			return now, now
		}
		if rem < boundDur {
			boundDur = rem
			graceDur = rem / 2
		}
	}
	return now.Add(graceDur), now.Add(boundDur)
}

func waitBatchContext(ctx context.Context, batch *retirementBatch, bound time.Time) {
	done := make(chan struct{})
	go func() {
		batch.wait(bound)
		close(done)
	}()
	waitDur := time.Until(bound) + 100*time.Millisecond
	if waitDur < 20*time.Millisecond {
		waitDur = 20 * time.Millisecond
	}
	select {
	case <-done:
	case <-ctx.Done():
	case <-time.After(waitDur):
	}
}

// ActiveExchange represents one in-flight HTTP/1.1, HTTP/2, or HTTP/3 request
// stream or one owned HTTP/1.1 101 upgraded session.
type ActiveExchange struct {
	id         uint64
	reg        *ExchangeRegistry
	origCoord  EntryCoord
	curCoord   EntryCoord
	protoMajor int
	reqMethod  string
	reqUpgrade string

	ctx        context.Context
	cancel     context.CancelFunc
	rawH1Conn  net.Conn
	controller *http.ResponseController

	mu            sync.Mutex
	retiring      bool
	abortNow      bool
	graceDeadline time.Time
	boundDeadline time.Time
	closeCode     uint16
	wroteHeader   bool
	statusCode    int
	hijacked      bool
	upgraded      *upgradedSession
	upgradedBack  *upgradedBackConn
	batches       []*retirementBatch

	closed       atomic.Bool
	closedAt     atomic.Int64
	forcedClose  atomic.Bool
	gracefulDone atomic.Bool
	abortStream  atomic.Bool
	doneCh       chan struct{}
	doneOnce     sync.Once
}

func (ex *ActiveExchange) isUpgraded() bool {
	if ex == nil {
		return false
	}
	ex.mu.Lock()
	defer ex.mu.Unlock()
	return ex.hijacked || ex.upgraded != nil || ex.reqUpgrade != ""
}

func (ex *ActiveExchange) hasWrittenHeader() bool {
	if ex == nil {
		return false
	}
	ex.mu.Lock()
	defer ex.mu.Unlock()
	return ex.wroteHeader
}

// IsRetired reports whether this exchange has been marked for immediate abort
// (mandatory narrowing/withdrawal or after the graceful drain window expired).
func (ex *ActiveExchange) IsRetired() bool {
	if ex == nil {
		return false
	}
	ex.mu.Lock()
	defer ex.mu.Unlock()
	return ex.abortNow
}

// ShouldAbortStream reports whether the outermost HTTP handler should panic with
// http.ErrAbortHandler to reset an in-flight HTTP/2 or HTTP/3 stream (or close
// an in-flight HTTP/1.1 stream) that was retired.
func (ex *ActiveExchange) ShouldAbortStream() bool {
	if ex == nil {
		return false
	}
	return ex.abortStream.Load()
}

func (ex *ActiveExchange) triggerStreamAbort(forceConnClose bool) {
	ex.mu.Lock()
	ex.abortNow = true
	wroteHdr := ex.wroteHeader
	ex.mu.Unlock()

	ex.cancel()
	if wroteHdr || ex.protoMajor >= 2 {
		ex.abortStream.Store(true)
	}
	if ex.controller != nil {
		now := time.Now()
		_ = ex.controller.SetReadDeadline(now)
		_ = ex.controller.SetWriteDeadline(now)
	}
	if forceConnClose && ex.protoMajor == 1 && ex.rawH1Conn != nil {
		ex.forcedClose.Store(true)
		ex.gracefulDone.Store(false)
		_ = ex.rawH1Conn.Close()
	}
}

func (ex *ActiveExchange) armRetirement(grace, bound time.Time, code uint16, immediate bool, batches ...*retirementBatch) {
	if ex == nil {
		return
	}
	ex.mu.Lock()
	if ex.closed.Load() {
		ex.mu.Unlock()
		return
	}
	firstArm := !ex.retiring
	ex.retiring = true
	if immediate {
		ex.abortNow = true
	}
	if ex.graceDeadline.IsZero() || grace.Before(ex.graceDeadline) {
		ex.graceDeadline = grace
	}
	if ex.boundDeadline.IsZero() || bound.Before(ex.boundDeadline) {
		ex.boundDeadline = bound
	}
	if ex.closeCode == 0 {
		ex.closeCode = code
	}
	effGrace := ex.graceDeadline
	effBound := ex.boundDeadline
	effCode := ex.closeCode
	up := ex.upgraded
	hijacked := ex.hijacked
	for _, b := range batches {
		if b != nil {
			b.add()
			ex.batches = append(ex.batches, b)
		}
	}
	ex.mu.Unlock()

	if up != nil {
		up.initiateClose(effGrace, effBound, effCode)
		return
	}
	if !hijacked {
		ex.gracefulDone.Store(true)
		if immediate {
			ex.triggerStreamAbort(false)
		}
		if firstArm {
			go func() {
				wait := time.Until(effGrace)
				if wait > 0 {
					select {
					case <-ex.doneCh:
						return
					case <-time.After(wait):
					}
				}
				// Grace window expired: force stream abort and H1 socket closure.
				ex.triggerStreamAbort(true)
			}()
		}
	}
}

func (ex *ActiveExchange) finishRequest() {
	if ex == nil {
		return
	}
	ex.mu.Lock()
	up := ex.upgraded
	ex.mu.Unlock()
	if up != nil {
		up.waitClosed()
	}
	ex.markClosed()
}

func (ex *ActiveExchange) markClosed() {
	if ex == nil {
		return
	}
	if ex.closed.CompareAndSwap(false, true) {
		ex.closedAt.Store(time.Now().UnixNano())
		ex.cancel()
		ex.reg.mu.Lock()
		delete(ex.reg.active, ex.id)
		recompute := ex.reg.lastRecomputeFn
		ex.reg.mu.Unlock()

		ex.mu.Lock()
		batches := ex.batches
		ex.batches = nil
		ex.mu.Unlock()
		for _, b := range batches {
			b.done()
		}
		if recompute != nil {
			recompute()
		}
		ex.doneOnce.Do(func() {
			close(ex.doneCh)
		})
	}
}

func (ex *ActiveExchange) outcome(boundDeadline time.Time) (graceful bool, forced bool, withinBound bool, collateral int) {
	closedNs := ex.closedAt.Load()
	if closedNs == 0 {
		return false, false, false, 0
	}
	closedTime := time.Unix(0, closedNs)
	withinBound = !closedTime.After(boundDeadline.Add(50 * time.Millisecond))

	ex.mu.Lock()
	up := ex.upgraded
	ex.mu.Unlock()
	if up != nil {
		if up.completedCleanHandshake() {
			return true, false, withinBound, 0
		}
		return false, true, withinBound, 0
	}
	if ex.forcedClose.Load() {
		return false, true, withinBound, 0
	}
	return true, false, withinBound, 0
}

type exchangeResponseWriter struct {
	http.ResponseWriter
	ex *ActiveExchange
}

func (w *exchangeResponseWriter) WriteHeader(code int) {
	ex := w.ex
	if ex.IsRetired() && code != http.StatusSwitchingProtocols {
		ex.abortStream.Store(true)
		return
	}
	ex.mu.Lock()
	if !ex.wroteHeader && (code < 100 || code >= 200 || code == http.StatusSwitchingProtocols) {
		ex.wroteHeader = true
		ex.statusCode = code
	}
	ex.mu.Unlock()
	w.ResponseWriter.WriteHeader(code)
}

func (w *exchangeResponseWriter) Write(p []byte) (int, error) {
	ex := w.ex
	if ex.IsRetired() {
		ex.abortStream.Store(true)
		return 0, io.ErrClosedPipe
	}
	ex.mu.Lock()
	if !ex.wroteHeader {
		ex.wroteHeader = true
		ex.statusCode = http.StatusOK
	}
	ex.mu.Unlock()
	return w.ResponseWriter.Write(p)
}

func (w *exchangeResponseWriter) Flush() {
	if w.ex.IsRetired() {
		w.ex.abortStream.Store(true)
		return
	}
	if fl, ok := w.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func (w *exchangeResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	rc := http.NewResponseController(w.ResponseWriter)
	conn, brw, err := rc.Hijack()
	if err != nil {
		return nil, nil, err
	}
	isWS := strings.EqualFold(w.ex.reqUpgrade, "websocket") ||
		strings.EqualFold(strings.TrimSpace(w.Header().Get("Upgrade")), "websocket")
	w.ex.mu.Lock()
	if w.ex.upgradedBack != nil && w.ex.upgradedBack.isWebSocket {
		isWS = true
	}
	up := newUpgradedSession(w.ex, conn, brw, w.ex.upgradedBack, isWS)
	w.ex.hijacked = true
	w.ex.upgraded = up
	retiring := w.ex.retiring
	grace := w.ex.graceDeadline
	bound := w.ex.boundDeadline
	code := w.ex.closeCode
	w.ex.mu.Unlock()

	if isWS {
		up.attachSessionToBack(w.ex.upgradedBack)
	} else if w.ex.upgradedBack != nil {
		up.attachSessionToBack(w.ex.upgradedBack)
	}
	if retiring {
		up.initiateClose(grace, bound, code)
	}
	return up.clientConn(), up.clientReadWriter(), nil
}

// AttachUpgradedBackend associates the upstream 101 ReadWriteCloser with the
// current request's ActiveExchange so narrowing retirement and shutdown can
// send a masked WebSocket Close frame (when WebSocket) and close the upstream
// leg deterministically (#562).
func AttachUpgradedBackend(ctx context.Context, back io.ReadWriteCloser, resHeader http.Header) io.ReadWriteCloser {
	ex := ActiveExchangeFrom(ctx)
	if ex == nil {
		return back
	}
	isWS := strings.EqualFold(strings.TrimSpace(resHeader.Get("Upgrade")), "websocket")
	wb := &upgradedBackConn{
		ReadWriteCloser: back,
		ex:              ex,
		isWebSocket:     isWS,
		holdCh:          make(chan struct{}),
	}
	ex.mu.Lock()
	ex.upgradedBack = wb
	if ex.upgraded != nil {
		ex.upgraded.attachBackend(wb, isWS)
	}
	ex.mu.Unlock()
	return wb
}
