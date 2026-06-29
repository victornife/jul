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

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

// serveStreaming transcodes a streaming gRPC method. The wire shape depends on
// the method's streaming kind: server-streaming and bidirectional responses are
// framed (NDJSON or SSE, per the location's stream_mode) and flushed per
// message; a client-streaming response is a single JSON object.
func (t *Transcoder) serveStreaming(w http.ResponseWriter, r *http.Request, rt *route, vars map[string]string) {
	method := string(rt.method.FullName())
	clientStream := rt.method.IsStreamingClient()
	serverStream := rt.method.IsStreamingServer()

	ctx, cancel := context.WithCancel(outgoingContext(r))
	defer cancel()

	desc := &grpc.StreamDesc{
		StreamName:    string(rt.method.Name()),
		ServerStreams: serverStream,
		ClientStreams: clientStream,
	}
	cs, err := t.conn.NewStream(ctx, desc, grpcMethodPath(rt.method),
		grpc.MaxCallRecvMsgSize(t.maxMsg), grpc.MaxCallSendMsgSize(t.maxMsg))
	if err != nil {
		code := httpStatusFromCode(status.Code(err))
		t.writeError(w, code, status.Convert(err).Message())
		t.report(method, code)
		return
	}

	switch {
	case serverStream && !clientStream:
		t.serveServerStream(w, r, rt, vars, cs, method)
	case clientStream && !serverStream:
		t.serveClientStream(w, r, rt, vars, cs, cancel, method)
	default:
		t.serveBidiStream(w, r, rt, vars, cs, cancel, method)
	}
}

// serveServerStream sends a single request built from the body, path variables,
// and query, then streams each reply message to the client as a framed event.
func (t *Transcoder) serveServerStream(w http.ResponseWriter, r *http.Request, rt *route, vars map[string]string, cs grpc.ClientStream, method string) {
	req := dynamicpb.NewMessage(rt.method.Input())
	if err := t.buildRequest(req, rt, vars, r); err != nil {
		code := requestErrorStatus(err)
		t.writeError(w, code, err.Error())
		t.report(method, code)
		return
	}
	if err := cs.SendMsg(req); err != nil {
		t.streamSetupError(w, err, method)
		return
	}
	t.streamMsg(method, "sent")
	_ = cs.CloseSend()

	resp := newStreamResponder(w, t.streamMode)
	t.pumpReplies(resp, cs, rt, method)
}

// serveClientStream reads a sequence of JSON request frames (a JSON array or
// newline/whitespace-delimited objects), forwards each as a gRPC message, then
// returns the single reply as one JSON object.
func (t *Transcoder) serveClientStream(w http.ResponseWriter, r *http.Request, rt *route, vars map[string]string, cs grpc.ClientStream, cancel context.CancelFunc, method string) {
	if err := t.sendRequestFrames(r, rt, vars, cs); err != nil {
		cancel()
		var de *decodeError
		if errors.As(err, &de) {
			t.writeError(w, http.StatusBadRequest, de.Error())
			t.report(method, http.StatusBadRequest)
			return
		}
		t.streamSetupError(w, err, method)
		return
	}
	_ = cs.CloseSend()

	out := dynamicpb.NewMessage(rt.method.Output())
	if err := cs.RecvMsg(out); err != nil {
		t.streamSetupError(w, err, method)
		return
	}
	t.streamMsg(method, "recv")
	body, err := t.marshalReply(out)
	if err != nil {
		t.writeError(w, http.StatusInternalServerError, "encode response: "+err.Error())
		t.report(method, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	t.report(method, http.StatusOK)
}

// serveBidiStream pumps request frames to the backend while concurrently
// streaming reply frames back to the client over the same HTTP/2 request.
func (t *Transcoder) serveBidiStream(w http.ResponseWriter, r *http.Request, rt *route, vars map[string]string, cs grpc.ClientStream, cancel context.CancelFunc, method string) {
	var (
		mu      sync.Mutex
		sendErr error
	)
	setSendErr := func(err error) {
		mu.Lock()
		sendErr = err
		mu.Unlock()
		cancel()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := t.sendRequestFrames(r, rt, vars, cs); err != nil {
			setSendErr(err)
			return
		}
		_ = cs.CloseSend()
	}()

	resp := newStreamResponder(w, t.streamMode)
	for {
		out := dynamicpb.NewMessage(rt.method.Output())
		if err := cs.RecvMsg(out); err != nil {
			mu.Lock()
			se := sendErr
			mu.Unlock()
			if se != nil {
				t.finishStreamError(w, resp, se, method)
				<-done
				return
			}
			if errors.Is(err, io.EOF) {
				resp.end()
				t.report(method, http.StatusOK)
				<-done
				return
			}
			t.finishStreamError(w, resp, err, method)
			<-done
			return
		}
		body, mErr := t.marshalReply(out)
		if mErr != nil {
			t.finishStreamError(w, resp, mErr, method)
			cancel()
			<-done
			return
		}
		if err := resp.message(body); err != nil {
			cancel()
			<-done
			t.report(method, http.StatusOK)
			return
		}
		t.streamMsg(method, "recv")
	}
}

// pumpReplies streams every reply message from cs to the client, mapping a
// terminal gRPC error to an HTTP error (before the first frame) or an error
// frame (after streaming has started).
func (t *Transcoder) pumpReplies(resp *streamResponder, cs grpc.ClientStream, rt *route, method string) {
	for {
		out := dynamicpb.NewMessage(rt.method.Output())
		if err := cs.RecvMsg(out); err != nil {
			if errors.Is(err, io.EOF) {
				resp.end()
				t.report(method, http.StatusOK)
				return
			}
			t.finishStreamError(resp.w, resp, err, method)
			return
		}
		body, mErr := t.marshalReply(out)
		if mErr != nil {
			t.finishStreamError(resp.w, resp, mErr, method)
			return
		}
		if err := resp.message(body); err != nil {
			t.report(method, http.StatusOK)
			return
		}
		t.streamMsg(method, "recv")
	}
}

// sendRequestFrames decodes the request body as a stream of JSON messages and
// forwards each to the backend. A malformed frame is returned as a *decodeError
// so callers can map it to 400.
func (t *Transcoder) sendRequestFrames(r *http.Request, rt *route, vars map[string]string, cs grpc.ClientStream) error {
	fd, err := newFrameDecoder(r.Body)
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
// written to an HTTP error response.
func (t *Transcoder) streamSetupError(w http.ResponseWriter, err error, method string) {
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
func (s *streamResponder) end() {
	s.ensureStarted()
	if s.mode == "sse" {
		_, _ = io.WriteString(s.w, "event: end\ndata: {}\n\n")
		s.flush()
	}
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
