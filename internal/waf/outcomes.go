// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/corazawaf/coraza/v3/experimental"
	"github.com/corazawaf/coraza/v3/types"

	"jul/internal/config"
	"jul/internal/middleware"
)

type detectionOnlyInterrupter interface {
	DetectionOnlyInterruption() *types.Interruption
}

type loggedMatchedRule interface {
	Log() bool
}

type unwrappedTx interface {
	Unwrap() types.Transaction
}

func (f *Firewall) newTransaction(r *http.Request) types.Transaction {
	if ctxwaf, ok := f.waf.(experimental.WAFWithOptions); ok {
		return ctxwaf.NewTransactionWithOptions(experimental.Options{
			Context: r.Context(),
		})
	}
	return f.waf.NewTransaction()
}

func detectionOnlyInterruption(tx types.Transaction) *types.Interruption {
	cur := tx
	for cur != nil {
		if d, ok := cur.(detectionOnlyInterrupter); ok {
			return d.DetectionOnlyInterruption()
		}
		u, ok := cur.(unwrappedTx)
		if !ok {
			break
		}
		cur = u.Unwrap()
	}
	return nil
}

func interruptionStatus(it *types.Interruption, defaultBlockStatus int) int {
	if defaultBlockStatus < 100 || defaultBlockStatus > 599 {
		defaultBlockStatus = http.StatusForbidden
	}
	if it == nil {
		return defaultBlockStatus
	}
	if it.Status >= 100 && it.Status <= 599 && it.Status != http.StatusOK {
		return it.Status
	}
	return defaultBlockStatus
}

func isBodyLimitInterruption(it *types.Interruption) bool {
	if it == nil {
		return false
	}
	return it.RuleID == 0 && (it.Status == http.StatusRequestEntityTooLarge || it.Status == http.StatusInternalServerError)
}

func classifyRuleSource(id int, file string) string {
	if id == 900000 || id == config.WAFArgumentLimitGuardRuleID || (id >= 890001 && id <= 899999) {
		return "generated"
	}
	switch {
	case file == "" || file == "_inline_":
		return "inline"
	case strings.HasPrefix(file, "@"):
		return "embedded"
	default:
		return "external"
	}
}

func extractMatchedRules(matched []types.MatchedRule, it, detIt *types.Interruption) ([]MatchedRuleSummary, int, bool) {
	interruptedID := 0
	if it != nil && it.RuleID > 0 {
		interruptedID = it.RuleID
	} else if detIt != nil && detIt.RuleID > 0 {
		interruptedID = detIt.RuleID
	}

	var out []MatchedRuleSummary
	totalRelevant := 0
	seenInterrupted := false

	for _, mr := range matched {
		if mr == nil || mr.Rule() == nil {
			continue
		}
		r := mr.Rule()
		id := r.ID()
		if id <= 0 {
			continue
		}
		logged := false
		if lr, ok := mr.(loggedMatchedRule); ok {
			logged = lr.Log()
		}
		isInterrupting := interruptedID > 0 && id == interruptedID
		if !logged && !isInterrupting {
			continue
		}
		if isInterrupting {
			seenInterrupted = true
		}
		totalRelevant++
		if len(out) >= MaxMatchedRulesPerEvent {
			continue
		}

		var targets []string
		for _, md := range mr.MatchedDatas() {
			if md == nil {
				continue
			}
			vName := md.Variable().Name()
			if vName == "" {
				continue
			}
			clean := SanitizeMatchedTarget(vName, md.Key())
			if clean != "" && !slices.Contains(targets, clean) && len(targets) < 8 {
				targets = append(targets, clean)
			}
		}

		var tags []string
		for _, tag := range r.Tags() {
			if len(tags) >= 8 {
				break
			}
			if t := strings.TrimSpace(tag); t != "" {
				tags = append(tags, t)
			}
		}

		sev := r.Severity().String()
		if sev == "unknown" || sev == "" {
			sev = ""
		}

		out = append(out, MatchedRuleSummary{
			RuleID:         id,
			Phase:          int(r.Phase()),
			Severity:       sev,
			SourceClass:    classifyRuleSource(id, r.File()),
			Disruptive:     isInterrupting,
			Logged:         logged,
			Tags:           tags,
			MatchedTargets: targets,
		})
	}

	// Ensure a nolog interrupting rule is always represented even if Coraza did
	// not append a MatchedRule entry for it.
	if interruptedID > 0 && !seenInterrupted {
		totalRelevant++
		if len(out) < MaxMatchedRulesPerEvent {
			out = append(out, MatchedRuleSummary{
				RuleID:      interruptedID,
				SourceClass: "unknown",
				Disruptive:  true,
				Logged:      false,
			})
		}
	}

	truncated := totalRelevant > len(out)
	return out, totalRelevant, truncated
}

type bodyReadCloser struct {
	io.Reader
	io.Closer
}

func (f *Firewall) serveHTTP(w http.ResponseWriter, r *http.Request, next http.Handler, route RouteAttribution, genFn func() uint64) {
	startTime := time.Now().UTC()
	// Strip any client- or outer-supplied WAF event header so only a genuine
	// WAF interruption on this transaction can emit HeaderWAFEvent.
	r.Header.Del(HeaderWAFEvent)
	w.Header().Del(HeaderWAFEvent)

	tx := f.newTransaction(r)
	if tx.IsRuleEngineOff() {
		_ = tx.Close()
		next.ServeHTTP(w, r)
		return
	}

	var (
		outcome                  string
		errorCategory            string
		eventID                  string
		supportHeaderEmitted     bool
		originReached            bool
		originStatus             int
		clientStatus             int
		partialResponseDelivered bool
		hijacked                 bool
		responseInterrupted      bool
		phases                   = make([]string, 0, 5)
	)

	addPhase := func(p string) {
		if !slices.Contains(phases, p) {
			phases = append(phases, p)
		}
	}

	mintSupportHeader := func(hdr http.Header) {
		if eventID == "" {
			if f.opts.Investigation != nil {
				eventID = f.opts.Investigation.NewEventID()
			} else {
				eventID = (*InvestigationStore)(nil).NewEventID()
			}
		}
		hdr.Set(HeaderWAFEvent, eventID)
		supportHeaderEmitted = true
	}

	defer func() {
		addPhase("logging")
		tx.ProcessLogging()

		it := tx.Interruption()
		detIt := detectionOnlyInterruption(tx)
		rules, totalMatched, matchesTruncated := extractMatchedRules(tx.MatchedRules(), it, detIt)

		wouldInterrupt := false
		interruptedAction := ""
		interruptedRuleID := 0
		if it != nil {
			wouldInterrupt = true
			interruptedAction = it.Action
			interruptedRuleID = it.RuleID
		} else if detIt != nil {
			wouldInterrupt = true
			interruptedAction = detIt.Action
			interruptedRuleID = detIt.RuleID
		}

		if outcome == "" {
			switch {
			case responseInterrupted:
				if isBodyLimitInterruption(it) {
					outcome = OutcomeRejectedBodyLimit
					if errorCategory == "" {
						errorCategory = "response_body_limit"
					}
				} else {
					outcome = OutcomeInterruptedResponse
				}
			case errorCategory != "":
				outcome = OutcomeInspectionError
			case totalMatched > 0 || detIt != nil:
				outcome = OutcomeMatchedObserved
			default:
				outcome = OutcomeClean
			}
		}

		if f.opts.Hooks.OnTransaction != nil {
			f.opts.Hooks.OnTransaction(f.info.Mode, outcome)
		}

		if outcome != OutcomeClean && f.opts.Investigation != nil && f.opts.Investigation.Enabled() {
			if eventID == "" {
				eventID = f.opts.Investigation.NewEventID()
			}
			var genID uint64
			if genFn != nil {
				genID = genFn()
			}
			ev := TransactionEvent{
				EventID:                  eventID,
				Time:                     startTime,
				Outcome:                  outcome,
				Mode:                     f.info.Mode,
				WouldInterrupt:           wouldInterrupt,
				InterruptedAction:        interruptedAction,
				InterruptedRuleID:        interruptedRuleID,
				ClientStatus:             clientStatus,
				OriginReached:            originReached,
				OriginStatus:             originStatus,
				PartialResponseDelivered: partialResponseDelivered,
				Hijacked:                 hijacked,
				PhasesEvaluated:          phases,
				Method:                   r.Method,
				Host:                     resolveTransactionHost(r, route),
				Path:                     r.URL.Path,
				QueryOmitted:             r.URL.RawQuery != "" || r.URL.Fragment != "",
				Route:                    route,
				Generation:               genID,
				PolicyDigest:             f.info.PolicyDigest,
				ExternalDigest:           f.info.ExternalDigest,
				CRSVersion:               f.info.CRSVersion,
				Paranoia:                 f.info.Paranoia,
				MatchedRules:             rules,
				TotalMatchedRules:        totalMatched,
				MatchesTruncated:         matchesTruncated,
				SupportHeaderEmitted:     supportHeaderEmitted,
				RequestIDHint:            middleware.RequestIDFrom(r.Context()),
				TraceIDHint:              middleware.TraceIDFrom(r.Context()),
				ErrorCategory:            errorCategory,
			}
			if res, ok := f.opts.Investigation.Record(ev); ok && f.opts.Hooks.OnInvestigationEvent != nil {
				f.opts.Hooks.OnInvestigationEvent(res)
			}
		}

		_ = tx.Close()
	}()

	// Phase 1: connection, URI, request headers.
	addPhase("request_headers")
	var client string
	var cport int
	if idx := strings.LastIndexByte(r.RemoteAddr, ':'); idx != -1 {
		client = r.RemoteAddr[:idx]
		cport, _ = strconv.Atoi(r.RemoteAddr[idx+1:])
	}
	tx.ProcessConnection(client, cport, "", 0)
	tx.ProcessURI(requestTargetURI(r), r.Method, r.Proto)
	for k, vr := range r.Header {
		for _, v := range vr {
			tx.AddRequestHeader(k, v)
		}
	}
	if r.Host != "" {
		tx.AddRequestHeader("Host", r.Host)
		tx.SetServerName(r.Host)
	}
	for _, te := range r.TransferEncoding {
		tx.AddRequestHeader("Transfer-Encoding", te)
	}

	if it := tx.ProcessRequestHeaders(); it != nil {
		outcome = OutcomeInterruptedRequest
		clientStatus = interruptionStatus(it, f.info.BlockStatus)
		mintSupportHeader(w.Header())
		w.WriteHeader(clientStatus)
		return
	}

	// Phase 2: request body buffering and inspection.
	addPhase("request_body")
	if tx.IsRequestBodyAccessible() && r.Body != nil && r.Body != http.NoBody {
		origBody := r.Body
		it, _, err := tx.ReadRequestBodyFrom(origBody)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				outcome = OutcomeRejectedBodyLimit
				errorCategory = "request_body_limit"
				clientStatus = http.StatusRequestEntityTooLarge
			} else {
				outcome = OutcomeInspectionError
				errorCategory = "request_body_read_error"
				clientStatus = http.StatusBadRequest
			}
			mintSupportHeader(w.Header())
			w.WriteHeader(clientStatus)
			return
		}
		if it != nil {
			if isBodyLimitInterruption(it) {
				outcome = OutcomeRejectedBodyLimit
				errorCategory = "request_body_limit"
			} else {
				outcome = OutcomeInterruptedRequest
			}
			clientStatus = interruptionStatus(it, f.info.BlockStatus)
			mintSupportHeader(w.Header())
			w.WriteHeader(clientStatus)
			return
		}
		rbr, err := tx.RequestBodyReader()
		if err != nil {
			outcome = OutcomeInspectionError
			errorCategory = "request_body_buffer_error"
			clientStatus = http.StatusBadRequest
			mintSupportHeader(w.Header())
			w.WriteHeader(clientStatus)
			return
		}
		r.Body = bodyReadCloser{
			Reader: io.MultiReader(rbr, origBody),
			Closer: origBody,
		}
	}

	it, err := tx.ProcessRequestBody()
	if err != nil && it == nil {
		outcome = OutcomeInspectionError
		errorCategory = "request_body_process_error"
		clientStatus = http.StatusBadRequest
		mintSupportHeader(w.Header())
		w.WriteHeader(clientStatus)
		return
	}
	if it != nil {
		if isBodyLimitInterruption(it) {
			outcome = OutcomeRejectedBodyLimit
			errorCategory = "request_body_limit"
		} else {
			outcome = OutcomeInterruptedRequest
			if it.RuleID == 200002 || it.RuleID == 200003 {
				errorCategory = "request_body_parse_error"
			}
		}
		clientStatus = interruptionStatus(it, f.info.BlockStatus)
		mintSupportHeader(w.Header())
		w.WriteHeader(clientStatus)
		return
	}

	// Request passed WAF phases 1 & 2; invoke the inner handler / origin.
	originReached = true
	ic, wrappedWriter := newWAFResponseInterceptor(w, r, tx, f.info.BlockStatus, addPhase, mintSupportHeader)
	next.ServeHTTP(wrappedWriter, r)
	_ = ic.finishResponse()

	originStatus = ic.originStatus
	clientStatus = ic.flushedStatus
	if clientStatus == 0 {
		clientStatus = ic.statusCode
	}
	partialResponseDelivered = ic.partialDelivered && (ic.responseInterrupted || ic.errCategory != "")
	hijacked = ic.isHijacked
	responseInterrupted = ic.responseInterrupted
	if ic.errCategory != "" && errorCategory == "" {
		errorCategory = ic.errCategory
	}
}

func resolveTransactionHost(r *http.Request, route RouteAttribution) string {
	if len(route.ServerNames) > 0 && route.ServerNames[0] != "" && !strings.Contains(route.ServerNames[0], "*") {
		return strings.ToLower(route.ServerNames[0])
	}
	h := strings.TrimSpace(r.Host)
	if h == "" {
		return "_unmatched"
	}
	if hostOnly, _, err := net.SplitHostPort(h); err == nil && hostOnly != "" {
		h = hostOnly
	}
	return sanitizeEventHost(h)
}

func requestTargetURI(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "/"
	}
	if reqURI := r.URL.RequestURI(); reqURI != "" {
		return reqURI
	}
	return r.URL.String()
}

type wafHijackerTracker struct {
	hijacker    http.Hijacker
	interceptor *wafResponseInterceptor
}

func (h *wafHijackerTracker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := h.hijacker.Hijack()
	if err != nil {
		return conn, rw, err
	}
	h.interceptor.isHijacked = true
	return conn, rw, nil
}

type wafResponseInterceptor struct {
	w                             http.ResponseWriter
	tx                            types.Transaction
	proto                         string
	blockStatus                   int
	addPhase                      func(string)
	mintSupportHeader             func(http.Header)
	originStatus                  int
	statusCode                    int
	flushedStatus                 int
	isWriteHeaderFlush            bool
	wroteHeader                   bool
	wroteBufferedBodyToDownstream bool
	partialDelivered              bool
	isHijacked                    bool
	allowFlushing                 bool
	responseInterrupted           bool
	errCategory                   string
}

type wafWrappedResponseWriter interface {
	http.ResponseWriter
	io.ReaderFrom
	http.Flusher
}

var _ wafWrappedResponseWriter = (*wafResponseInterceptor)(nil)

func newWAFResponseInterceptor(
	w http.ResponseWriter,
	r *http.Request,
	tx types.Transaction,
	blockStatus int,
	addPhase func(string),
	mintSupportHeader func(http.Header),
) (*wafResponseInterceptor, http.ResponseWriter) {
	ic := &wafResponseInterceptor{
		w:                 w,
		tx:                tx,
		proto:             r.Proto,
		blockStatus:       blockStatus,
		addPhase:          addPhase,
		mintSupportHeader: mintSupportHeader,
		originStatus:      http.StatusOK,
		statusCode:        http.StatusOK,
	}

	hijacker, isHijacker := w.(http.Hijacker)
	pusher, isPusher := w.(http.Pusher)
	switch {
	case !isHijacker && isPusher:
		return ic, struct {
			wafWrappedResponseWriter
			http.Pusher
		}{ic, pusher}
	case isHijacker && !isPusher:
		return ic, struct {
			wafWrappedResponseWriter
			http.Hijacker
		}{ic, &wafHijackerTracker{hijacker: hijacker, interceptor: ic}}
	case isHijacker && isPusher:
		return ic, struct {
			wafWrappedResponseWriter
			http.Hijacker
			http.Pusher
		}{ic, &wafHijackerTracker{hijacker: hijacker, interceptor: ic}, pusher}
	default:
		return ic, struct {
			wafWrappedResponseWriter
		}{ic}
	}
}

func (i *wafResponseInterceptor) Header() http.Header {
	return i.w.Header()
}

func (i *wafResponseInterceptor) WriteHeader(statusCode int) {
	if i.wroteHeader {
		return
	}
	if statusCode >= 100 && statusCode < 200 && statusCode != http.StatusSwitchingProtocols {
		// Prevent an upstream from forging a WAF event support header on 1xx
		// informational responses (such as 103 Early Hints), and forward the
		// informational header without latching final response state or running
		// phase-3 response-header inspection prematurely.
		i.w.Header().Del(HeaderWAFEvent)
		i.w.WriteHeader(statusCode)
		return
	}
	i.wroteHeader = true
	i.originStatus = statusCode
	// Prevent an upstream from forging a WAF event support header.
	i.w.Header().Del(HeaderWAFEvent)

	i.addPhase("response_headers")
	for k, vv := range i.w.Header() {
		for _, v := range vv {
			i.tx.AddResponseHeader(k, v)
		}
	}

	i.statusCode = statusCode
	if it := i.tx.ProcessResponseHeaders(statusCode, i.proto); it != nil {
		i.responseInterrupted = true
		i.cleanHeaders()
		i.w.Header().Set("Content-Length", "0")
		i.statusCode = interruptionStatus(it, i.blockStatus)
		if !i.isWriteHeaderFlush {
			i.mintSupportHeader(i.w.Header())
		}
		i.flushWriteHeader()
		return
	}

	if statusCode == http.StatusSwitchingProtocols {
		i.flushWriteHeader()
	}
	if !i.tx.IsResponseBodyAccessible() || !i.tx.IsResponseBodyProcessable() {
		i.allowFlushing = true
	}
}

func (i *wafResponseInterceptor) Write(b []byte) (int, error) {
	if i.tx.IsInterrupted() {
		return len(b), nil
	}
	if !i.wroteHeader {
		i.WriteHeader(http.StatusOK)
		if i.tx.IsInterrupted() {
			return len(b), nil
		}
	}

	if i.tx.IsResponseBodyAccessible() && i.tx.IsResponseBodyProcessable() && !i.wroteBufferedBodyToDownstream {
		i.addPhase("response_body")
		it, n, err := i.tx.WriteResponseBody(b)
		if it != nil {
			i.responseInterrupted = true
			i.cleanHeaders()
			i.w.Header().Set("Content-Length", "0")
			i.statusCode = interruptionStatus(it, i.blockStatus)
			if !i.isWriteHeaderFlush {
				i.mintSupportHeader(i.w.Header())
			}
			i.flushWriteHeader()
			return len(b), nil
		}
		if err != nil {
			i.errCategory = "response_body_buffer_error"
			return n, err
		}
		if n == len(b) {
			return n, nil
		}
		if err := i.writeBufferedResponseBodyToDownstream(); err != nil {
			return n, err
		}
		n2, err := i.w.Write(b[n:])
		if n2 > 0 {
			i.partialDelivered = true
		}
		return n + n2, err
	}

	i.flushWriteHeader()
	n, err := i.w.Write(b)
	if n > 0 {
		i.partialDelivered = true
	}
	return n, err
}

func (i *wafResponseInterceptor) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{i}, r)
}

func (i *wafResponseInterceptor) Flush() {
	if !i.wroteHeader {
		i.WriteHeader(http.StatusOK)
	}
	if i.allowFlushing {
		i.flushWriteHeader()
		if fl, ok := i.w.(http.Flusher); ok {
			fl.Flush()
		}
	}
}

func (i *wafResponseInterceptor) finishResponse() error {
	if i.isHijacked {
		return nil
	}
	if i.tx.IsInterrupted() {
		return nil
	}
	if !i.wroteHeader {
		i.WriteHeader(http.StatusOK)
		if i.tx.IsInterrupted() {
			return nil
		}
	}

	if i.tx.IsResponseBodyAccessible() && i.tx.IsResponseBodyProcessable() && !i.wroteBufferedBodyToDownstream {
		i.addPhase("response_body")
		it, err := i.tx.ProcessResponseBody()
		if err != nil {
			i.errCategory = "response_body_process_error"
			i.statusCode = http.StatusInternalServerError
			if !i.isWriteHeaderFlush {
				i.mintSupportHeader(i.w.Header())
			}
			i.flushWriteHeader()
			return err
		}
		if it != nil {
			i.responseInterrupted = true
			i.cleanHeaders()
			i.w.Header().Set("Content-Length", "0")
			i.statusCode = interruptionStatus(it, i.blockStatus)
			if !i.isWriteHeaderFlush {
				i.mintSupportHeader(i.w.Header())
			}
			i.flushWriteHeader()
			return nil
		}
		return i.writeBufferedResponseBodyToDownstream()
	}

	i.allowFlushing = true
	i.flushWriteHeader()
	return nil
}

func (i *wafResponseInterceptor) writeBufferedResponseBodyToDownstream() error {
	if i.wroteBufferedBodyToDownstream {
		return nil
	}
	reader, err := i.tx.ResponseBodyReader()
	if err != nil {
		i.errCategory = "response_body_reader_error"
		i.statusCode = http.StatusInternalServerError
		if !i.isWriteHeaderFlush {
			i.mintSupportHeader(i.w.Header())
		}
		i.flushWriteHeader()
		return fmt.Errorf("failed to release the response body reader: %v", err)
	}
	i.flushWriteHeader()
	written, err := io.Copy(i.w, reader)
	if written > 0 {
		i.partialDelivered = true
	}
	if err != nil {
		return fmt.Errorf("failed to copy the response body: %v", err)
	}
	i.wroteBufferedBodyToDownstream = true
	i.allowFlushing = true
	return nil
}

func (i *wafResponseInterceptor) flushWriteHeader() {
	if !i.isWriteHeaderFlush {
		i.w.WriteHeader(i.statusCode)
		i.flushedStatus = i.statusCode
		i.isWriteHeaderFlush = true
	}
}

func (i *wafResponseInterceptor) cleanHeaders() {
	for k := range i.w.Header() {
		i.w.Header().Del(k)
	}
}
