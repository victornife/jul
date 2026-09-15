// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"jul/internal/upstream"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

// streamHealth indicates whether a streaming call completed successfully or
// already recorded a typed failure/neutral result at the point it occurred.
type streamHealth int

const (
	healthRecorded streamHealth = iota
	healthSuccess
)

// serveStreaming transcodes a streaming gRPC method. The wire shape depends on
// the method's streaming kind: server-streaming and bidirectional responses are
// framed (NDJSON or SSE, per the location's stream_mode) and flushed per
// message; a client-streaming response is a single JSON object.
func (t *Transcoder) serveStreaming(w http.ResponseWriter, r *http.Request, rt *route, vars map[string]string, conn *grpc.ClientConn, backend upstream.Attempt) {
	method := string(rt.method.FullName())
	clientStream := rt.method.IsStreamingClient()
	serverStream := rt.method.IsStreamingServer()

	base := outgoingContext(r)
	retry := t.pool.RetryRequestFor(t.retry, false)
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if retry.Deadline > 0 {
		ctx, cancel = context.WithTimeout(base, retry.Deadline)
	} else {
		ctx, cancel = context.WithCancel(base)
	}
	defer cancel()

	desc := &grpc.StreamDesc{
		StreamName:    string(rt.method.Name()),
		ServerStreams: serverStream,
		ClientStreams: clientStream,
	}
	cs, err := conn.NewStream(ctx, desc, grpcMethodPath(rt.method),
		grpc.MaxCallRecvMsgSize(t.maxMsg), grpc.MaxCallSendMsgSize(t.maxMsg))
	if err != nil {
		code := httpStatusFromCode(status.Code(err))
		t.writeError(w, code, status.Convert(err).Message())
		t.report(method, code)
		t.pool.RecordAttempt(backend, classifyGRPCAttempt(err, r.Context(), ctx))
		return
	}

	var health streamHealth
	switch {
	case serverStream && !clientStream:
		health = t.serveServerStream(w, r, rt, vars, cs, ctx, method, backend)
	case clientStream && !serverStream:
		health = t.serveClientStream(w, r, rt, vars, cs, ctx, cancel, method, backend)
	default:
		health = t.serveBidiStream(w, r, rt, vars, cs, ctx, cancel, method, backend)
	}
	if health == healthSuccess {
		t.pool.RecordAttempt(backend, upstream.SuccessfulAttempt())
	}
}

// serveServerStream sends a single request built from the body, path variables,
// and query, then streams each reply message to the client as a framed event.
func (t *Transcoder) serveServerStream(w http.ResponseWriter, r *http.Request, rt *route, vars map[string]string, cs grpc.ClientStream, attempt context.Context, method string, backend upstream.Attempt) streamHealth {
	req := dynamicpb.NewMessage(rt.method.Input())
	if err := t.buildRequest(req, rt, vars, r); err != nil {
		code := requestErrorStatus(err)
		t.writeError(w, code, err.Error())
		t.report(method, code)
		t.pool.RecordAttempt(backend, upstream.JulPolicyFailure(""))
		return healthRecorded
	}
	if err := cs.SendMsg(req); err != nil {
		t.streamSetupError(w, err, method)
		t.pool.RecordAttempt(backend, classifyGRPCAttempt(err, r.Context(), attempt))
		return healthRecorded
	}
	t.streamMsg(method, "sent")
	if err := cs.CloseSend(); err != nil {
		t.streamSetupError(w, err, method)
		t.pool.RecordAttempt(backend, classifyGRPCAttempt(err, r.Context(), attempt))
		return healthRecorded
	}

	resp := newStreamResponder(w, t.streamMode)
	return t.pumpReplies(resp, cs, rt, method, backend, r.Context(), attempt)
}

// serveClientStream reads a sequence of JSON request frames (a JSON array or
// newline/whitespace-delimited objects), forwards each as a gRPC message, and
// returns the single reply as one JSON object.
//
// Sending and receiving run concurrently: grpc-go permits one goroutine
// calling SendMsg while another calls RecvMsg on the same stream. This lets a
// backend that answers (or fails) before the upload finishes be observed
// immediately, instead of only after a downstream body read that may never
// unblock on its own. Once RecvMsg returns for any reason, the request body
// is aborted so a blocked sender goroutine always exits before the handler
// returns; the gRPC attempt itself is left to the sender's own classified
// cancellation (or the caller's deferred cancel) so a coordinating cancel
// here never races the classification of an independently failing sender.
func (t *Transcoder) serveClientStream(w http.ResponseWriter, r *http.Request, rt *route, vars map[string]string, cs grpc.ClientStream, attempt context.Context, cancel context.CancelFunc, method string, backend upstream.Attempt) streamHealth {
	body := newAbortableBody(r.Body)
	defer body.closeIfNeeded()

	var (
		mu           sync.Mutex
		sendErr      error
		sendSet      bool
		sendIsDecode bool
		sendClass    upstream.AttemptClassification
	)
	// setSendErr classifies its error using r.Context()/attempt BEFORE calling
	// cancel(): cancel() makes attempt.Err() non-nil, and classification must
	// see the state that produced the error, not the coordination cancel that
	// follows it (which would otherwise be misread as Jul's own deadline).
	setSendErr := func(err error) {
		mu.Lock()
		if !sendSet {
			sendSet = true
			sendErr = err
			switch {
			case isUploadAborted(err):
				// Jul's own abort produced this; the real cause is owned by
				// whichever path called abort, not by this send.
			case isDecodeError(err):
				sendIsDecode = true
			default:
				sendClass = classifyGRPCAttempt(err, r.Context(), attempt)
			}
		}
		mu.Unlock()
		cancel()
		body.abort()
	}

	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		if err := t.sendRequestFrames(body, rt, vars, cs); err != nil {
			setSendErr(err)
			return
		}
		if err := cs.CloseSend(); err != nil {
			setSendErr(err)
		}
	}()

	out := dynamicpb.NewMessage(rt.method.Output())
	recvErr := cs.RecvMsg(out)
	var recvClassification upstream.AttemptClassification
	if recvErr != nil {
		recvClassification = classifyGRPCAttempt(recvErr, r.Context(), attempt)
	}

	// Whatever just happened, unblock a sender that may still be blocked
	// reading the downstream body (the reported deadlock is a blocked body
	// read, not a blocked gRPC call, so this alone guarantees sendDone
	// closes). The attempt context is left to the sender's own setSendErr,
	// or the caller's deferred cancel, so this can never race a fresh
	// classification with a coordinating cancel it didn't ask for.
	body.abort()
	<-sendDone

	mu.Lock()
	se, sSet, seIsDecode, seClass := sendErr, sendSet, sendIsDecode, sendClass
	mu.Unlock()

	if sSet && !isUploadAborted(se) {
		// The sender saw its own terminal cause (a decode failure, or a
		// genuine send/close failure) independent of our own cancellation;
		// that is the real cause even when RecvMsg also failed as a symptom
		// of the same cancellation.
		if seIsDecode {
			t.writeError(w, http.StatusBadRequest, se.Error())
			t.report(method, http.StatusBadRequest)
			t.pool.RecordAttempt(backend, upstream.JulPolicyFailure(""))
			return healthRecorded
		}
		t.pool.RecordAttempt(backend, seClass)
		t.streamSetupError(w, se, method)
		return healthRecorded
	}

	if recvErr != nil {
		t.pool.RecordAttempt(backend, recvClassification)
		t.streamSetupError(w, recvErr, method)
		return healthRecorded
	}

	t.streamMsg(method, "recv")
	respBody, err := t.marshalReply(out)
	if err != nil {
		t.writeError(w, http.StatusInternalServerError, "encode response: "+err.Error())
		t.report(method, http.StatusInternalServerError)
		t.pool.RecordAttempt(backend, upstream.JulPolicyFailure(""))
		return healthRecorded
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if n, writeErr := w.Write(respBody); writeErr != nil || n != len(respBody) {
		t.pool.RecordAttempt(backend, upstream.ClientCancellationResult())
		t.report(method, http.StatusOK)
		return healthRecorded
	}
	t.report(method, http.StatusOK)
	return healthSuccess
}

// serveBidiStream pumps request frames to the backend while concurrently
// streaming reply frames back to the client over the same HTTP/2 request.
//
// Every receive-side terminal path (backend error, clean EOF, marshal
// failure, downstream write failure) must guarantee the sender can exit
// before waiting on done: aborting the body unblocks a blocked downstream
// Read, which is the reported deadlock (a stalled client upload blocks on
// the request body, not on the gRPC stream). The attempt context itself is
// cancelled only by the sender's own classified failure or the caller's
// deferred cancel, never as a bare coordination signal here, so a fresh
// classification never races a cancel it didn't ask for.
func (t *Transcoder) serveBidiStream(w http.ResponseWriter, r *http.Request, rt *route, vars map[string]string, cs grpc.ClientStream, attempt context.Context, cancel context.CancelFunc, method string, backend upstream.Attempt) streamHealth {
	body := newAbortableBody(r.Body)
	defer body.closeIfNeeded()

	var (
		mu                 sync.Mutex
		sendErr            error
		sendClassification upstream.AttemptClassification
	)
	setSendErr := func(err error) {
		mu.Lock()
		sendErr = err
		if isDecodeError(err) {
			sendClassification = upstream.JulPolicyFailure("")
		} else {
			sendClassification = classifyGRPCAttempt(err, r.Context(), attempt)
		}
		mu.Unlock()
		cancel()
		body.abort()
	}
	// stopSender unblocks a sender that may still be blocked reading the
	// downstream body before any receive path waits on <-done. It does not
	// cancel the attempt here: classification of a fresh error (below) must
	// see attempt.Err() as it was when the error occurred, not as mutated by
	// a coordinating cancel this function issues for an unrelated reason.
	stopSender := func() {
		body.abort()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := t.sendRequestFrames(body, rt, vars, cs); err != nil {
			setSendErr(err)
			return
		}
		if err := cs.CloseSend(); err != nil {
			setSendErr(err)
		}
	}()

	resp := newStreamResponder(w, t.streamMode)
	for {
		out := dynamicpb.NewMessage(rt.method.Output())
		if err := cs.RecvMsg(out); err != nil {
			// Read the sender's own state and classify this error (when it
			// will actually be used) BEFORE stopSender(): cancel() makes
			// attempt.Err() non-nil, which would otherwise be misread as a
			// Jul-owned timeout rather than the real terminal cause.
			mu.Lock()
			se := sendErr
			sc := sendClassification
			mu.Unlock()
			if se != nil && !isUploadAborted(se) {
				stopSender()
				t.pool.RecordAttempt(backend, sc)
				t.finishStreamError(w, resp, se, method)
				<-done
				return healthRecorded
			}
			if errors.Is(err, io.EOF) {
				stopSender()
				if err := resp.end(); err != nil {
					t.pool.RecordAttempt(backend, upstream.ClientCancellationResult())
					t.report(method, http.StatusOK)
					<-done
					return healthRecorded
				}
				t.report(method, http.StatusOK)
				<-done
				return healthSuccess
			}
			classification := classifyGRPCAttempt(err, r.Context(), attempt)
			stopSender()
			t.pool.RecordAttempt(backend, classification)
			t.finishStreamError(w, resp, err, method)
			<-done
			return healthRecorded
		}
		respBody, mErr := t.marshalReply(out)
		if mErr != nil {
			t.finishStreamError(w, resp, mErr, method)
			stopSender()
			<-done
			t.pool.RecordAttempt(backend, upstream.JulPolicyFailure(""))
			return healthRecorded
		}
		if err := resp.message(respBody); err != nil {
			stopSender()
			<-done
			t.report(method, http.StatusOK)
			t.pool.RecordAttempt(backend, upstream.ClientCancellationResult())
			return healthRecorded
		}
		t.streamMsg(method, "recv")
	}
}

// pumpReplies streams every reply message from cs to the client, mapping a
// terminal gRPC error to an HTTP error (before the first frame) or an error
// frame (after streaming has started).
func (t *Transcoder) pumpReplies(resp *streamResponder, cs grpc.ClientStream, rt *route, method string, backend upstream.Attempt, inbound, attempt context.Context) streamHealth {
	for {
		out := dynamicpb.NewMessage(rt.method.Output())
		if err := cs.RecvMsg(out); err != nil {
			if errors.Is(err, io.EOF) {
				if err := resp.end(); err != nil {
					t.pool.RecordAttempt(backend, upstream.ClientCancellationResult())
					t.report(method, http.StatusOK)
					return healthRecorded
				}
				t.report(method, http.StatusOK)
				return healthSuccess
			}
			t.pool.RecordAttempt(backend, classifyGRPCAttempt(err, inbound, attempt))
			t.finishStreamError(resp.w, resp, err, method)
			return healthRecorded
		}
		body, mErr := t.marshalReply(out)
		if mErr != nil {
			t.finishStreamError(resp.w, resp, mErr, method)
			t.pool.RecordAttempt(backend, upstream.JulPolicyFailure(""))
			return healthRecorded
		}
		if err := resp.message(body); err != nil {
			t.report(method, http.StatusOK)
			t.pool.RecordAttempt(backend, upstream.ClientCancellationResult())
			return healthRecorded
		}
		t.streamMsg(method, "recv")
	}
}

// sendRequestFrames decodes the request body as a stream of JSON messages and
// forwards each to the backend. A malformed frame is returned as a *decodeError
// so callers can map it to 400. body is the downstream request body (directly,
// or wrapped so it can be aborted once a terminal event elsewhere makes
// further upload unnecessary).
func (t *Transcoder) sendRequestFrames(body io.Reader, rt *route, vars map[string]string, cs grpc.ClientStream) error {
	fd, err := newFrameDecoder(body)
	if err != nil {
		return &decodeError{err}
	}
	for {
		raw, err := fd.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return &decodeError{err}
		}
		msg := dynamicpb.NewMessage(rt.method.Input())
		if len(raw) > 0 {
			if err := protojson.Unmarshal(raw, msg); err != nil {
				return &decodeError{fmt.Errorf("decode JSON message: %w", err)}
			}
		}
		if err := applyPathVars(msg, vars); err != nil {
			return &decodeError{err}
		}
		if err := cs.SendMsg(msg); err != nil {
			return err
		}
		t.streamMsg(string(rt.method.FullName()), "sent")
	}
}

// streamSetupError maps a gRPC error that occurs before any response bytes are
// written to an HTTP error response. A local frame-decode failure always maps
// to 400, regardless of what status.Code an incidental wrapping might imply.
func (t *Transcoder) streamSetupError(w http.ResponseWriter, err error, method string) {
	if isDecodeError(err) {
		t.writeError(w, http.StatusBadRequest, err.Error())
		t.report(method, http.StatusBadRequest)
		return
	}
	code := httpStatusFromCode(status.Code(err))
	t.writeError(w, code, status.Convert(err).Message())
	t.report(method, code)
}

// finishStreamError ends a (possibly already started) streamed response on an
// error: a proper HTTP status if nothing has been written yet, otherwise a
// terminal error frame.
func (t *Transcoder) finishStreamError(w http.ResponseWriter, resp *streamResponder, err error, method string) {
	if !resp.started {
		t.streamSetupError(w, err, method)
		return
	}
	if isDecodeError(err) {
		resp.errorFrame(codes.InvalidArgument, err.Error())
		t.report(method, http.StatusOK)
		return
	}
	st := status.Convert(err)
	resp.errorFrame(st.Code(), st.Message())
	t.report(method, http.StatusOK)
}

func (t *Transcoder) streamMsg(method, direction string) {
	if t.onStreamMsg != nil {
		t.onStreamMsg(method, direction)
	}
}

func (t *Transcoder) marshalReply(m proto.Message) ([]byte, error) {
	return protojson.MarshalOptions{
		UseProtoNames:   t.preserveNames,
		EmitUnpopulated: true,
	}.Marshal(m)
}

// applyPathVars sets captured path variables on a message (they override any
// body-provided values), mirroring the unary path-variable precedence.
func applyPathVars(msg *dynamicpb.Message, vars map[string]string) error {
	for field, value := range vars {
		if err := setFieldByPath(msg.ProtoReflect(), strings.Split(field, "."), value); err != nil {
			return fmt.Errorf("path variable %q: %w", field, err)
		}
	}
	return nil
}

// decodeError marks a request-frame decoding failure so it maps to HTTP 400.
type decodeError struct{ err error }

func (e *decodeError) Error() string { return e.err.Error() }
func (e *decodeError) Unwrap() error { return e.err }

func isDecodeError(err error) bool {
	var de *decodeError
	return errors.As(err, &de)
}

// abortableBody lets Jul stop a blocked downstream request-body read once a
// terminal event elsewhere in the call makes further upload unnecessary,
// without turning the resulting read error into backend or client
// attribution evidence.
//
// Cancelling the gRPC attempt context alone does not unblock a body read: the
// request body is tied to the inbound HTTP connection, not to Jul's outbound
// attempt context. abort closes the underlying body so a blocked Read
// actually returns, and marks any resulting error as Jul-induced so callers
// can ignore it instead of misclassifying it as a backend or client fault.
type abortableBody struct {
	rc        io.ReadCloser
	closeOnce sync.Once
	aborted   atomic.Bool
}

func newAbortableBody(rc io.ReadCloser) *abortableBody {
	return &abortableBody{rc: rc}
}

// Read satisfies io.Reader. A genuine clean end-of-body (io.EOF) is always
// reported as-is so a normal upload racing with abort still completes
// normally. Any other error is reported as errUploadAborted once abort has
// been called, regardless of what the underlying Close produced.
func (b *abortableBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && b.aborted.Load() {
		return n, errUploadAborted
	}
	return n, err
}

// abort closes the underlying body at most once so a concurrently blocked
// Read unblocks, and marks the upload as intentionally, locally terminated.
func (b *abortableBody) abort() {
	b.aborted.Store(true)
	b.closeOnce.Do(func() { _ = b.rc.Close() })
}

// closeIfNeeded closes the underlying body at most once for the ordinary
// completion path. net/http may close the request body again itself; that is
// tolerated (Close is idempotent from this type's perspective).
func (b *abortableBody) closeIfNeeded() {
	b.closeOnce.Do(func() { _ = b.rc.Close() })
}

// errUploadAborted marks a request-body read that failed only because Jul
// intentionally closed the body after a terminal event elsewhere in the call
// (backend termination, downstream cancellation, or a local failure). It must
// never be classified as a backend or client fault.
var errUploadAborted = errors.New("transcode: downstream upload stopped after terminal result")

func isUploadAborted(err error) bool { return errors.Is(err, errUploadAborted) }

// streamResponder writes framed streaming responses (NDJSON or SSE) and flushes
// after each frame. Headers are written lazily on the first frame so a failure
// before any output can still produce a proper HTTP error status.
type streamResponder struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mode    string
	started bool
}

func newStreamResponder(w http.ResponseWriter, mode string) *streamResponder {
	fl, _ := w.(http.Flusher)
	return &streamResponder{w: w, flusher: fl, mode: mode}
}

func (s *streamResponder) ensureStarted() {
	if s.started {
		return
	}
	s.started = true
	h := s.w.Header()
	if s.mode == "sse" {
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
	} else {
		h.Set("Content-Type", "application/x-ndjson")
	}
	h.Set("X-Content-Type-Options", "nosniff")
	s.w.WriteHeader(http.StatusOK)
}

// message writes one JSON reply frame and flushes it.
func (s *streamResponder) message(b []byte) error {
	s.ensureStarted()
	var err error
	if s.mode == "sse" {
		_, err = fmt.Fprintf(s.w, "data: %s\n\n", b)
	} else {
		if _, err = s.w.Write(b); err == nil {
			_, err = io.WriteString(s.w, "\n")
		}
	}
	s.flush()
	return err
}

// errorFrame writes a terminal error frame for an error that occurs after
// streaming has started (the HTTP status is already 200).
func (s *streamResponder) errorFrame(code codes.Code, msg string) {
	s.ensureStarted()
	payload, _ := json.Marshal(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code.String(), Message: msg})
	if s.mode == "sse" {
		_, _ = fmt.Fprintf(s.w, "event: error\ndata: %s\n\n", payload)
	} else {
		_, _ = fmt.Fprintf(s.w, "{\"error\":%s}\n", payload)
	}
	s.flush()
}

// end terminates a successfully completed stream. NDJSON needs no terminator;
// SSE emits an explicit end event so clients can distinguish completion from a
// dropped connection.
func (s *streamResponder) end() error {
	s.ensureStarted()
	if s.mode == "sse" {
		if _, err := io.WriteString(s.w, "event: end\ndata: {}\n\n"); err != nil {
			return err
		}
		s.flush()
	}
	return nil
}

func (s *streamResponder) flush() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// frameDecoder reads a stream of JSON request messages from a body that is
// either a single JSON array of objects or a sequence of whitespace/newline
// -delimited JSON values (NDJSON). The total size is bounded by the body-limit
// middleware; per-message size by the gRPC max send size.
type frameDecoder struct {
	dec    *json.Decoder
	array  bool
	opened bool
	empty  bool
}

func newFrameDecoder(body io.Reader) (*frameDecoder, error) {
	if body == nil {
		return &frameDecoder{empty: true}, nil
	}
	br := bufio.NewReader(body)
	// Skip leading whitespace to inspect the first significant byte.
	for {
		b, err := br.Peek(1)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return &frameDecoder{empty: true}, nil
			}
			return nil, err
		}
		if isJSONSpace(b[0]) {
			_, _ = br.ReadByte()
			continue
		}
		fd := &frameDecoder{dec: json.NewDecoder(br), array: b[0] == '['}
		return fd, nil
	}
}

func (fd *frameDecoder) next() (json.RawMessage, error) {
	if fd.empty {
		return nil, io.EOF
	}
	if fd.array {
		if !fd.opened {
			if _, err := fd.dec.Token(); err != nil { // consume '['
				return nil, err
			}
			fd.opened = true
		}
		if !fd.dec.More() {
			// Consume the closing ']' and reject any trailing tokens so a body
			// like [..]{..} cannot smuggle silently ignored extra data.
			if _, err := fd.dec.Token(); err != nil { // ']'
				return nil, err
			}
			// Decoder.More is unreliable for detecting trailing top-level junk;
			// attempt one more decode and require EOF so [..]{..} or [..]5 fail.
			var trailing json.RawMessage
			if err := fd.dec.Decode(&trailing); err != io.EOF {
				return nil, fmt.Errorf("unexpected trailing data after JSON array")
			}
			return nil, io.EOF
		}
	}
	var raw json.RawMessage
	if err := fd.dec.Decode(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
