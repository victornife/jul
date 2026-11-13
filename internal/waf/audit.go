// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/experimental/plugins"
	"github.com/corazawaf/coraza/v3/experimental/plugins/plugintypes"
	"github.com/corazawaf/coraza/v3/types"

	"jul/internal/inspection"
)

// Jul replaces Coraza's built-in "serial" audit writer with this adapter
// (ADR 0032 R5, writer contract). Coraza's package init registers its own
// writers first, so this registration wins for every WAF Jul compiles. The
// adapter formats a detached record synchronously, then hands the bytes to a
// bounded writer call that returns to Coraza at auditWriteTimeout; an
// abandoned call stays charged until it actually returns.
func init() {
	plugins.RegisterAuditLogWriter("serial", func() plugintypes.AuditLogWriter { return &auditWriter{} })
}

// auditWriteTimeout bounds one audit writer call seen by Coraza's
// ProcessLogging (and therefore its phase-5 slot). Tests shorten it.
var auditWriteTimeout = 5 * time.Second

type formatterKind uint8

const (
	formatterNone formatterKind = iota
	formatterNative
	formatterJSON
	formatterUnsupported
)

// auditWriter is the Jul-owned serial audit writer. Without a target it has no
// output and Jul skips record construction entirely.
type auditWriter struct {
	mu        sync.Mutex
	out       io.Writer
	closer    io.Closer
	closeOnce sync.Once
	closeErr  error
	formatter plugintypes.AuditLogFormatter
	kind      formatterKind
}

func classifyFormatter(f plugintypes.AuditLogFormatter) formatterKind {
	if f == nil {
		return formatterNone
	}
	t := reflect.TypeOf(f)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.PkgPath() == "github.com/corazawaf/coraza/v3/internal/auditlog" {
		switch t.Name() {
		case "nativeFormatter":
			return formatterNative
		case "jsonFormatter":
			return formatterJSON
		}
	}
	return formatterUnsupported
}

func (w *auditWriter) Init(c plugintypes.AuditLogConfig) error {
	if c.Target == "" {
		return nil
	}
	var out io.Writer
	switch c.Target {
	case "/dev/stdout":
		out = os.Stdout
	case "/dev/stderr":
		out = os.Stderr
	default:
		f, err := os.OpenFile(c.Target, os.O_APPEND|os.O_CREATE|os.O_WRONLY, c.FileMode)
		if err != nil {
			return err
		}
		out, w.closer = f, f
	}
	w.out = out
	w.formatter = c.Formatter
	w.kind = classifyFormatter(c.Formatter)
	return nil
}

// hasOutput reports whether a Write can produce output.
func (w *auditWriter) hasOutput() bool {
	return w != nil && w.out != nil && w.formatter != nil
}

// Close closes the target file. It does not take mu: an abandoned write may
// hold it indefinitely, and closing the file is what unblocks or fails it.
func (w *auditWriter) Close() error {
	if w == nil || w.closer == nil {
		return nil
	}
	w.closeOnce.Do(func() { w.closeErr = w.closer.Close() })
	return w.closeErr
}

func (w *auditWriter) Write(al plugintypes.AuditLog) error {
	if !w.hasOutput() {
		return nil
	}
	ac := lookupAuditCtx(al)
	if ac == nil {
		// Ungoverned (no inspection governor): Coraza's serial behaviour.
		bts, err := w.formatter.Format(al)
		if err != nil || len(bts) == 0 {
			return err
		}
		return w.writeLine(bts)
	}
	shape := measureAuditRecord(al)
	ac.reconcileConstruction(shape.construction)
	fmtCharge, err := ac.lease.Charge(inspection.AuditBytes, formatEstimate(shape, w.kind))
	if err != nil {
		ac.dropped = true
		ac.gov.NoteAuditDrop(inspection.AuditDropBudget)
		return nil
	}
	defer func() { _ = fmtCharge.Release() }()
	bts, err := w.formatter.Format(al)
	if err != nil || len(bts) == 0 {
		return err
	}
	write, ok := ac.gov.BeginAuditWrite(uint64(len(bts)) + 1)
	if !ok {
		ac.dropped = true
		return nil
	}
	done := make(chan error, 1)
	go func() {
		err := w.writeLine(bts)
		write.Finish()
		done <- err
	}()
	timer := time.NewTimer(auditWriteTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		write.Abandon()
		return errAuditWriteAbandoned
	}
}

var errAuditWriteAbandoned = errors.New("waf: audit writer call abandoned at its deadline")

func (w *auditWriter) writeLine(bts []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.out == nil {
		return nil
	}
	if _, err := w.out.Write(bts); err != nil {
		return err
	}
	_, err := w.out.Write([]byte{'\n'})
	return err
}

// auditTxCtx is the governed context of one transaction's ProcessLogging
// call. The writer runs synchronously inside that call on the request
// goroutine, so the fields need no lock.
type auditTxCtx struct {
	gov        *inspection.Governor
	lease      *inspection.Lease
	precharged uint64
	extra      []*inspection.Charge
	dropped    bool
	overran    bool
}

// reconcileConstruction charges, non-refusably, construction bytes beyond the
// pre-logging estimate (a phase-5 ctl or match outside the calibrated scope).
func (ac *auditTxCtx) reconcileConstruction(actual uint64) {
	if actual <= ac.precharged {
		return
	}
	c, err := ac.lease.ChargeCommitted(inspection.AuditBytes, actual-ac.precharged)
	if err == nil {
		ac.extra = append(ac.extra, c)
	}
	ac.precharged = actual
	ac.overran = true
}

func (ac *auditTxCtx) release() {
	for _, c := range ac.extra {
		_ = c.Release()
	}
	ac.extra = nil
}

var auditCtxs sync.Map // transaction ID -> *auditTxCtx

func lookupAuditCtx(al plugintypes.AuditLog) *auditTxCtx {
	t := al.Transaction()
	if t == nil {
		return nil
	}
	v, ok := auditCtxs.Load(t.ID())
	if !ok {
		return nil
	}
	return v.(*auditTxCtx)
}

// auditConfig is the compiled, request-independent audit configuration.
type auditConfig struct {
	writer *auditWriter
	// relevantStatus is SecAuditLogRelevantStatus; nil means unset.
	relevantStatus *regexp.Regexp
}

// compiledAuditConfig reads the compiled audit writer and relevant-status
// pattern. A writer that is not Jul's adapter, or a formatter other than
// Native or JSON, is unsupported (ADR 0032 writer contract).
func compiledAuditConfig(w coraza.WAF) (auditConfig, error) {
	engine, err := compiledEngine(w)
	if err != nil {
		return auditConfig{}, err
	}
	if !engine.CanAddr() {
		return auditConfig{}, fmt.Errorf("waf: incompatible coraza.WAF internal layout: unaddressable engine in %T", w)
	}
	// engine was reached through an unexported field, so re-derive a
	// pointer that may call methods.
	ptr := reflect.NewAt(engine.Type(), unsafe.Pointer(engine.UnsafeAddr()))
	getter := ptr.MethodByName("AuditLogWriter")
	re := ptr.Elem().FieldByName("AuditLogRelevantStatus")
	if !getter.IsValid() || getter.Type().NumIn() != 0 || getter.Type().NumOut() != 1 ||
		!re.IsValid() || re.Type() != reflect.TypeOf((*regexp.Regexp)(nil)) {
		return auditConfig{}, fmt.Errorf("waf: incompatible coraza.WAF internal layout: missing audit writer settings on %T", w)
	}
	var cfg auditConfig
	if !re.IsNil() {
		cfg.relevantStatus = re.Interface().(*regexp.Regexp)
	}
	out := getter.Call(nil)[0]
	aw, ok := out.Interface().(*auditWriter)
	if !ok {
		return cfg, fmt.Errorf("waf: SecAuditLogType selects an unsupported audit writer (%s); only the Jul serial adapter is supported under the inspection governor", out.Elem().Type())
	}
	if aw.hasOutput() && aw.kind == formatterUnsupported {
		return cfg, fmt.Errorf("waf: SecAuditLogFormat selects an unsupported audit formatter (%T); use Native or JSON", aw.formatter)
	}
	cfg.writer = aw
	return cfg, nil
}

// innerTx returns Coraza's transaction struct behind Jul's wrappers.
func innerTx(tx types.Transaction) (reflect.Value, bool) {
	cur := tx
	for {
		u, ok := cur.(unwrappedTx)
		if !ok {
			break
		}
		cur = u.Unwrap()
	}
	v := reflect.ValueOf(cur)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	return v.Elem(), true
}

// txAuditState is the per-transaction audit configuration ProcessLogging
// will use. Phase-5 rules can still change it (ctl); that is outside the
// calibrated scope and reconciled in the writer.
type txAuditState struct {
	engine types.AuditEngineStatus
	parts  types.AuditLogParts
}

func readTxAuditState(tx types.Transaction) (txAuditState, error) {
	v, ok := innerTx(tx)
	if !ok {
		return txAuditState{}, fmt.Errorf("waf: incompatible coraza transaction layout (%T)", tx)
	}
	engine := v.FieldByName("AuditEngine")
	parts := v.FieldByName("AuditLogParts")
	if !engine.IsValid() || engine.Type() != reflect.TypeOf(types.AuditEngineOff) ||
		!parts.IsValid() || parts.Type() != reflect.TypeOf(types.AuditLogParts(nil)) {
		return txAuditState{}, fmt.Errorf("waf: incompatible coraza transaction layout: missing audit fields on %T", tx)
	}
	return txAuditState{
		engine: types.AuditEngineStatus(engine.Int()),
		parts:  append(types.AuditLogParts(nil), parts.Interface().(types.AuditLogParts)...),
	}, nil
}

// disableTxAudit turns the transaction's audit engine off before
// ProcessLogging. Phase-5 rules still run; no record is built.
func disableTxAudit(tx types.Transaction) bool {
	v, ok := innerTx(tx)
	if !ok {
		return false
	}
	engine := v.FieldByName("AuditEngine")
	if !engine.IsValid() || !engine.CanSet() || engine.Type() != reflect.TypeOf(types.AuditEngineOff) {
		return false
	}
	engine.SetInt(int64(types.AuditEngineOff))
	return true
}

func hasPart(parts types.AuditLogParts, p types.AuditLogPart) bool {
	for _, q := range parts {
		if q == p {
			return true
		}
	}
	return false
}

// auditRelevant mirrors ProcessLogging's RelevantOnly decision for the
// status known before phase 5. With no pattern, the decision depends on the
// transaction's private audit flag, so it is assumed relevant.
func auditRelevant(st txAuditState, re *regexp.Regexp, status string) bool {
	switch st.engine {
	case types.AuditEngineOff:
		return false
	case types.AuditEngineRelevantOnly:
		return re == nil || re.MatchString(status)
	default:
		return true
	}
}

// Calibrated audit-record cost terms (internal/waf/audit_calibration_test.go
// checks them against the pinned Coraza with CRS).
const (
	auditRecordBaseBytes   = 4 << 10
	auditHeaderEntryBytes  = 128
	auditFileEntryBytes    = 512
	auditMessageEntryBytes = 512
	// auditErrorLogFixed covers ErrorLog's literal text, numbers and quotes.
	auditErrorLogFixed = 512
	// auditErrorLogExtra covers one [msg_match_n] [data_match_n] pair's text.
	auditErrorLogExtra = 48
	// auditMaxLogField is Coraza's maxSizeLogMessage truncation.
	auditMaxLogField = 280
	// auditErrorLogAlloc is the bytes one ErrorLog() call allocates per byte
	// of its result (strings.Builder growth; measured below 5).
	auditErrorLogAlloc = 6
)

// auditPrecheck is what Jul knows before ProcessLogging builds a record.
type auditPrecheck struct {
	parts             types.AuditLogParts
	reqHeaderCount    uint64
	reqHeaderBytes    uint64
	bodyBytes         uint64
	respHeaderCount   uint64
	respHeaderBytes   uint64
	respBodyBytes     uint64
	files             uint64
	uri, host, client int
	txID              int
	matched           []types.MatchedRule
}

type auditedRule interface {
	Audit() bool
}

// errorLogBound bounds len(mr.ErrorLog()): each quoted field expands at
// most fourfold under %q, msg and data are truncated to 280 B, and every
// further match with a msg or data appends one quoted pair.
func errorLogBound(mr types.MatchedRule, p auditPrecheck) uint64 {
	trunc := func(v string) uint64 { return uint64(min(len(v), auditMaxLogField)) }
	r := mr.Rule()
	mds := mr.MatchedDatas()
	fields := uint64(len(r.File())+len(r.Revision())+len(r.Version())+len(r.Severity().String())) +
		uint64(p.host+p.uri+p.txID+p.client)
	var msg uint64
	for i, md := range mds {
		if i == 0 {
			fields += trunc(md.Message()) + trunc(md.Data())
			msg = trunc(md.Message())
		}
		if md.Message() != "" {
			msg = trunc(md.Message())
			break
		}
	}
	n := uint64(auditErrorLogFixed) + msg + 4*fields
	for _, t := range r.Tags() {
		n += 16 + 4*uint64(len(t))
	}
	for _, md := range mds[min(1, len(mds)):] {
		if md.Message() != "" || md.Data() != "" {
			n += auditErrorLogExtra + 4*(trunc(md.Message())+trunc(md.Data()))
		}
	}
	return n
}

// partIndex is p's position in parts, or -1.
func partIndex(parts types.AuditLogParts, p types.AuditLogPart) int {
	for i, q := range parts {
		if q == p {
			return i
		}
	}
	return -1
}

// constructionEstimate bounds the bytes tx.AuditLog() allocates for the
// declared parts.
func constructionEstimate(p auditPrecheck) uint64 {
	n := uint64(auditRecordBaseBytes)
	if hasPart(p.parts, types.AuditLogPartRequestHeaders) {
		n += auditHeaderEntryBytes*p.reqHeaderCount + 4*p.reqHeaderBytes
	}
	if hasPart(p.parts, types.AuditLogPartRequestBody) {
		n += 4 * p.bodyBytes
	}
	if hasPart(p.parts, types.AuditLogPartIntermediaryResponseBody) {
		n += 2 * p.respBodyBytes
	}
	if hasPart(p.parts, types.AuditLogPartResponseHeaders) {
		n += auditHeaderEntryBytes*p.respHeaderCount + 4*p.respHeaderBytes
	}
	if hasPart(p.parts, types.AuditLogPartUploadedFiles) {
		n += auditFileEntryBytes * p.files
	}
	ki := partIndex(p.parts, types.AuditLogPartRulesMatched)
	hi := partIndex(p.parts, types.AuditLogPartAuditLogTrailer)
	k, h := ki >= 0, hi >= 0
	if !k && !h {
		return n
	}
	for _, mr := range p.matched {
		ar, ok := mr.(auditedRule)
		if mr == nil || mr.Rule() == nil || !ok || !ar.Audit() {
			continue
		}
		m := uint64(len(mr.MatchedDatas()))
		switch {
		case k && h && hi < ki:
			// Coraza fills a K entry's ErrorLog only when H precedes K in
			// the parts; each one re-walks all of the rule's matches, so
			// the cost is quadratic in m.
			n += m * (auditMessageEntryBytes + auditErrorLogAlloc*errorLogBound(mr, p))
		case k:
			n += m * auditMessageEntryBytes
		default:
			n += auditMessageEntryBytes/2 + auditErrorLogAlloc*errorLogBound(mr, p)
		}
	}
	return n
}

// auditShape measures a built record: its string bytes, entries and JSON
// escaped length, and the construction cost implied by its contents.
type auditShape struct {
	strBytes     uint64
	jsonBytes    uint64
	entries      uint64
	construction uint64
}

func (s *auditShape) str(v string) {
	s.strBytes += uint64(len(v))
	s.jsonBytes += jsonEscapedLen(v)
}

func (s *auditShape) headers(h map[string][]string) uint64 {
	var bytes uint64
	for k, vs := range h {
		for _, v := range vs {
			s.str(k)
			s.str(v)
			s.entries++
			bytes += uint64(len(k) + len(v))
		}
	}
	return bytes
}

func measureAuditRecord(al plugintypes.AuditLog) auditShape {
	var s auditShape
	s.construction = auditRecordBaseBytes
	t := al.Transaction()
	if t == nil {
		return s
	}
	s.str(t.Timestamp())
	s.str(t.ID())
	s.str(t.ClientIP())
	s.str(t.HostIP())
	s.str(t.ServerID())
	s.entries += 8
	if t.HasRequest() {
		req := t.Request()
		s.str(req.Method())
		s.str(req.Protocol())
		s.str(req.URI())
		s.str(req.HTTPVersion())
		before := s.entries
		b := s.headers(req.Headers())
		s.construction += auditHeaderEntryBytes*(s.entries-before) + 4*b
		body := req.Body()
		s.str(body)
		s.construction += 4 * uint64(len(body))
		for _, f := range req.Files() {
			s.str(f.Name())
			s.str(f.Mime())
			s.entries += 2
			s.construction += auditFileEntryBytes
		}
	}
	if t.HasResponse() {
		resp := t.Response()
		s.str(resp.Protocol())
		before := s.entries
		b := s.headers(resp.Headers())
		s.construction += auditHeaderEntryBytes*(s.entries-before) + 4*b
		body := resp.Body()
		s.str(body)
		s.construction += 2 * uint64(len(body))
	}
	if p := t.Producer(); p != nil && !isNilInterface(p) {
		s.str(p.Connector())
		s.str(p.Version())
		s.str(p.Server())
		s.str(p.RuleEngine())
		s.str(p.Stopwatch())
		for _, r := range p.Rulesets() {
			s.str(r)
		}
		s.entries += 8
	}
	for _, m := range al.Messages() {
		s.entries += 8
		s.construction += auditMessageEntryBytes
		s.str(m.Actionset())
		s.str(m.Message())
		if em, ok := m.(interface{ ErrorMessage() string }); ok {
			e := em.ErrorMessage()
			s.str(e)
			s.construction += auditErrorLogAlloc * uint64(len(e))
		}
		d := m.Data()
		if d == nil || isNilInterface(d) {
			continue
		}
		s.str(d.File())
		s.str(d.Rev())
		s.str(d.Msg())
		s.str(d.Data())
		s.str(d.Ver())
		s.str(d.Raw())
		for _, tag := range d.Tags() {
			s.str(tag)
			s.entries++
		}
	}
	return s
}

func isNilInterface(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return rv.IsNil()
	}
	return false
}

// jsonEscapedLen bounds encoding/json's output length for s, including its
// HTML escaping and invalid-UTF-8 replacement.
func jsonEscapedLen(s string) uint64 {
	var n uint64
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t':
				n += 2
			case c < 0x20 || c == '<' || c == '>' || c == '&':
				n += 6
			default:
				n++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			n += 6
		} else if r == '\u2028' || r == '\u2029' {
			n += 6
		} else {
			n += uint64(size)
		}
		i += size
	}
	return n
}

// formatEstimate bounds the formatter's allocations for a measured record.
func formatEstimate(s auditShape, kind formatterKind) uint64 {
	var out uint64
	switch kind {
	case formatterJSON:
		out = s.jsonBytes + 64*s.entries + 4<<10
	default:
		out = 2*s.strBytes + 32*s.entries + 4<<10
	}
	return 8*out + 160*s.entries + 16<<10
}

// prepareAudit makes the R5 decision before ProcessLogging builds a record
// (ADR 0032 R5): with no writer output the record is never built; otherwise
// its construction estimate is reserved, and a refused reservation drops the
// record (reason budget) while phase-5 rules still run.
func (f *Firewall) prepareAudit(tx types.Transaction, a *wafAdmission, respHeader http.Header, ic *wafResponseInterceptor, status int) {
	if !a.governed() {
		return
	}
	if !f.audit.writer.hasOutput() {
		disableTxAudit(tx)
		return
	}
	st, err := readTxAuditState(tx)
	if err != nil {
		disableTxAudit(tx)
		a.gov.NoteAuditDrop(inspection.AuditDropBudget)
		return
	}
	if !auditRelevant(st, f.audit.relevantStatus, statusString(status)) {
		// Coraza writes nothing; a phase-5 change is reconciled in Write.
		return
	}
	p := auditPrecheck{
		parts:          st.parts,
		reqHeaderCount: a.reqHeaderCount,
		reqHeaderBytes: a.reqHeaderBytes,
		bodyBytes:      a.bodyBytes,
		uri:            a.uri,
		host:           a.host,
		client:         a.client,
		txID:           len(tx.ID()),
		matched:        tx.MatchedRules(),
	}
	for k, vs := range respHeader {
		for _, v := range vs {
			p.respHeaderCount++
			p.respHeaderBytes += uint64(len(k) + len(v))
		}
	}
	if ic != nil {
		p.respBodyBytes = ic.respBodyBytes
	}
	if s, ok := txState(tx); ok {
		p.files = uint64(len(s.Variables().Files().FindAll()))
	}
	est := constructionEstimate(p)
	if _, err := a.charge(inspection.AuditBytes, est); err != nil {
		disableTxAudit(tx)
		a.gov.NoteAuditDrop(inspection.AuditDropBudget)
		return
	}
	a.audit.precharged = est
}

func statusString(code int) string {
	if code <= 0 {
		return ""
	}
	return strconv.Itoa(code)
}
