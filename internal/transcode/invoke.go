// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"jul/internal/backendtls"
	"jul/internal/config"
	"jul/internal/upstream"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// maxBodyBytes bounds the JSON request body the transcoder reads. The body-limit
// middleware may impose a smaller cap; this is a defensive ceiling.
const maxBodyBytes = 4 << 20 // 4 MiB

// Options carries optional hooks for a Transcoder.
type Options struct {
	Logger   *slog.Logger
	OnResult func(method, code string)
	// BackendTLS is the resolved outbound trust policy for the gRPC backend.
	// nil keeps the language defaults, which is what a target with no
	// backend_tls block gets. The transcoder never parses public
	// configuration: it consumes only this resolved value.
	BackendTLS *backendtls.Policy
	// OnStreamMsg, when set, is called once per streamed message with the gRPC
	// method full name and direction ("sent"/"recv").
	OnStreamMsg func(method, direction string)
	// Retry carries the location's retry overrides. A zero field inherits the
	// pool policy, which is read per request so a reload takes effect without
	// rebuilding the transcoder.
	Retry upstream.RetryOverride
	// reflectTimeout bounds the reflection fetch at construction (default 10s).
	// It is unexported and used only by tests.
	reflectTimeout time.Duration
}

// Transcoder maps REST/JSON requests to gRPC calls on a backend — unary and,
// when streaming is enabled, server/client/bidi streaming. It implements
// http.Handler and io.Closer; closing it releases all backend connections when
// the configuration is replaced.
type Transcoder struct {
	routes        []*route
	pool          *upstream.Pool
	useTLS        bool
	tlsPolicy     *backendtls.Policy
	conns         sync.Map // address -> *grpc.ClientConn
	retry         upstream.RetryOverride
	preserveNames bool
	streaming     bool
	streamMode    string
	maxMsg        int
	log           *slog.Logger
	onResult      func(method, code string)
	onStreamMsg   func(method, direction string)

	// evictStop is closed by Close to stop the stale-connection eviction
	// goroutine. It is nil when the pool has no dynamic backend set.
	evictStop chan struct{}
	// evictOnce ensures evictStop is closed at most once.
	evictOnce sync.Once

	// retired holds connections whose backend address has left the pool but
	// which are kept alive for a grace period so in-flight requests and
	// streams can finish (R11-05).
	retired sync.Map // address -> retiredConn
}

// retiredConn is a connection that has left the active pool but is still
// usable until its grace period expires.
type retiredConn struct {
	conn      *grpc.ClientConn
	retiredAt time.Time
}

// retiredConnGrace is how long a removed backend's connection remains usable
// before it is closed. It is a variable so tests can shorten it.
var retiredConnGrace = 30 * time.Second

// New builds a Transcoder from a location's grpc_transcode config. It loads the
// method routing table from the configured descriptor source, dials the gRPC
// backend (h2c or TLS), and returns a handler. The caller closes the handler to
// release the connection when the configuration is replaced.
//
// reflectSnap, when non-nil, is the candidate-generation snapshot used for
// server reflection at build time. This ensures reflection sees the candidate
// backend set rather than the previous generation's live backends (R9-06). When
// nil, the pool's current snapshot is used as a fallback (test convenience).
func New(ctx context.Context, cfg config.GRPCTranscodeConfig, pool *upstream.Pool, reflectSnap *upstream.PoolSnapshot, opts Options) (*Transcoder, error) {
	var routes []*route
	var err error

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("grpc_transcode %s: %w", cfg.Target, err)
	}

	if cfg.UseReflection {
		timeout := opts.reflectTimeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		reflectCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		// Reflection needs one connection to discover methods. Use the candidate
		// snapshot when provided so the build sees the new generation's backends
		// (R9-06); fall back to the live pool snapshot for test convenience.
		snap := reflectSnap
		if snap == nil {
			snap = pool.Snapshot()
		}
		b, berr := snap.Pick()
		if berr != nil {
			return nil, fmt.Errorf("grpc_transcode %s: no available backend for reflection: %w", cfg.Target, berr)
		}
		defer b.Release()

		conn, derr := dial(b.Address, cfg.TLS, opts.BackendTLS)
		if derr != nil {
			return nil, fmt.Errorf("grpc_transcode %s: dial %s for reflection: %w", cfg.Target, b.Address, derr)
		}
		routes, err = loadRoutesViaReflection(reflectCtx, conn)
		_ = conn.Close()
		if err != nil {
			return nil, fmt.Errorf("grpc_transcode %s: %w", cfg.Target, err)
		}
	} else {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("grpc_transcode %s: %w", cfg.Target, err)
		}
		routes, err = loadRoutesFromFile(cfg.DescriptorSet)
		if err != nil {
			return nil, fmt.Errorf("grpc_transcode %s: %w", cfg.Target, err)
		}
	}

	tr := &Transcoder{
		routes:        routes,
		pool:          pool,
		useTLS:        cfg.TLS,
		tlsPolicy:     opts.BackendTLS,
		retry:         opts.Retry,
		preserveNames: cfg.PreserveNames,
		streaming:     cfg.Streaming,
		streamMode:    normalizeStreamMode(cfg.StreamMode),
		maxMsg:        maxMessageBytes(cfg.MaxMessageSize),
		log:           opts.Logger,
		onResult:      opts.OnResult,
		onStreamMsg:   opts.OnStreamMsg,
		evictStop:     make(chan struct{}),
	}
	go tr.evictLoop()
	return tr, nil
}

// evictLoop periodically removes cached connections whose backend address is no
// longer in the pool. This matters for dynamic upstreams (service discovery),
// where a removed backend would otherwise keep a gRPC connection open until the
// transcoder is closed on the next reload (R10-06).
func (t *Transcoder) evictLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.evictStop:
			return
		case <-ticker.C:
			t.evictStaleConns()
		}
	}
}

// evictStaleConns moves cached connections for addresses that are no longer in
// the pool into a retired grace-period map. Connections whose backend address
// becomes valid again are re-promoted to the active cache. Connections that
// have been retired for longer than retiredConnGrace are closed and removed.
// This keeps in-flight streams alive during backend churn (R11-05, R12-02).
func (t *Transcoder) evictStaleConns() {
	if t.pool == nil {
		return
	}
	valid := make(map[string]struct{}, 16)
	for _, b := range t.pool.Backends() {
		valid[b.Address] = struct{}{}
	}

	// Move newly stale connections from active cache to retired.
	t.conns.Range(func(key, value any) bool {
		addr := key.(string)
		if _, ok := valid[addr]; ok {
			return true
		}
		if c, ok := value.(*grpc.ClientConn); ok {
			t.retired.Store(addr, retiredConn{conn: c, retiredAt: time.Now()})
		}
		t.conns.Delete(addr)
		return true
	})

	// Re-promote retired connections whose backend is valid again, or close
	// those whose grace period has expired.
	now := time.Now()
	t.retired.Range(func(key, value any) bool {
		addr := key.(string)
		rc := value.(retiredConn)
		expired := now.Sub(rc.retiredAt) >= retiredConnGrace
		if _, ok := valid[addr]; ok && !expired {
			// Backend reappeared: atomically promote. If another connection
			// won the race, close the retired one (R13-01).
			t.retired.Delete(key)
			if actual, loaded := t.conns.LoadOrStore(addr, rc.conn); loaded {
				actualConn := actual.(*grpc.ClientConn)
				if actualConn != rc.conn {
					_ = rc.conn.Close()
				}
			}
			return true
		}
		if expired {
			_ = rc.conn.Close()
			t.retired.Delete(key)
		}
		return true
	})
}

// normalizeStreamMode lower-cases the configured stream mode and defaults a
// blank value to "ndjson".
func normalizeStreamMode(mode string) string {
	if m := strings.ToLower(strings.TrimSpace(mode)); m != "" {
		return m
	}
	return "ndjson"
}

// maxMessageBytes resolves the per-message ceiling, applying the default when
// the configured size is non-positive.
func maxMessageBytes(s config.Size) int {
	if n := s.Bytes(); n > 0 {
		return int(n)
	}
	return maxBodyBytes
}

// Close stops the eviction goroutine and releases all cached backend
// connections.
func (t *Transcoder) Close() error {
	t.evictOnce.Do(func() {
		if t.evictStop != nil {
			close(t.evictStop)
		}
	})
	t.conns.Range(func(_, v any) bool {
		if c, ok := v.(*grpc.ClientConn); ok {
			_ = c.Close()
		}
		return true
	})
	t.retired.Range(func(_, v any) bool {
		if rc, ok := v.(retiredConn); ok {
			_ = rc.conn.Close()
		}
		return true
	})
	return nil
}

// connFor returns a cached gRPC connection for addr, creating and caching one
// if absent. Connections survive across requests to the same backend. During
// the retired grace period, a connection for a removed backend is re-promoted
// to the active cache so in-flight streams can continue (R11-05).
func (t *Transcoder) connFor(addr string) (*grpc.ClientConn, error) {
	if v, ok := t.conns.Load(addr); ok {
		return v.(*grpc.ClientConn), nil
	}
	if v, ok := t.retired.Load(addr); ok {
		rc := v.(retiredConn)
		if time.Since(rc.retiredAt) < retiredConnGrace {
			// Atomically promote back to active. Only close the retired
			// connection if another goroutine stored a different connection
			// for the same address in the meantime (R12-02, R13-01).
			t.retired.Delete(addr)
			if actual, loaded := t.conns.LoadOrStore(addr, rc.conn); loaded {
				actualConn := actual.(*grpc.ClientConn)
				if actualConn != rc.conn {
					_ = rc.conn.Close()
				}
				return actualConn, nil
			}
			return rc.conn, nil
		}
		// Expired; close it lazily here and dial anew.
		_ = rc.conn.Close()
		t.retired.Delete(addr)
	}
	conn, err := dial(addr, t.useTLS, t.tlsPolicy)
	if err != nil {
		return nil, err
	}
	actual, loaded := t.conns.LoadOrStore(addr, conn)
	if loaded {
		_ = conn.Close() // lost the race; use the winner's connection
		return actual.(*grpc.ClientConn), nil
	}
	return conn, nil
}

// firstConn returns a connection to the first available backend. It is used by
// benchmarks that need a native gRPC baseline without the HTTP routing
// overhead.
func (t *Transcoder) firstConn() (*grpc.ClientConn, error) {
	b, err := t.pool.Pick()
	if err != nil {
		return nil, err
	}
	defer t.pool.Release(b)
	return t.connFor(b.Address)
}

// dial creates a lazy gRPC client connection to addr over TLS or plaintext
// HTTP/2 (h2c). The passthrough scheme dials the address directly without name
// resolution.
//
// When a backend TLS policy is resolved, its config decides the trust roots,
// the client certificate, the verified name and any peer identities. A nil
// policy yields exactly the previous behaviour — a TLS 1.2 floor and platform
// roots — because that is what ClientConfig returns for no policy. The address
// is only where to dial; the policy decides who must answer.
func dial(addr string, useTLS bool, policy *backendtls.Policy) (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if useTLS {
		creds = credentials.NewTLS(policy.ClientConfig())
	} else {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient("passthrough:///"+addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial grpc backend %q: %w", addr, err)
	}
	return conn, nil
}

func (t *Transcoder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt, vars := t.match(r.Method, r.URL.Path)
	if rt == nil {
		t.writeError(w, http.StatusNotFound, "no transcoding route matches "+r.Method+" "+r.URL.Path)
		t.report("", http.StatusNotFound)
		return
	}
	method := string(rt.method.FullName())

	if rt.streaming {
		t.serveStreamingRoute(w, r, rt, vars, method)
		return
	}
	t.serveUnary(w, r, rt, vars, method)
}

// serveStreamingRoute selects one backend and serves a streaming method on it.
//
// Streaming is never retried: by the time a stream can fail, framing has
// already been written, and re-running the call would replay it into a client
// that has begun consuming the first one.
func (t *Transcoder) serveStreamingRoute(w http.ResponseWriter, r *http.Request, rt *route, vars map[string]string, method string) {
	if !t.streaming {
		t.writeError(w, http.StatusNotImplemented, "streaming methods require streaming = true on this grpc_transcode location")
		t.report(method, http.StatusNotImplemented)
		return
	}
	// Prefer the generation-scoped snapshot when present so reloads cannot
	// shift an in-flight request to a newer backend set.
	backend, err := t.pool.PickCtx(r.Context())
	if err != nil {
		code := http.StatusServiceUnavailable
		t.writeError(w, code, "no available gRPC backend: "+err.Error())
		t.report(method, code)
		return
	}
	defer t.pool.Release(backend)

	conn, err := t.connFor(backend.Address)
	if err != nil {
		code := http.StatusBadGateway
		t.writeError(w, code, "grpc backend unreachable: "+err.Error())
		t.report(method, code)
		t.pool.MarkFailure(backend)
		return
	}
	t.serveStreaming(w, r, rt, vars, conn, backend)
}

// serveUnary performs a transcoded unary call, retrying it when the route is
// idempotent and the failure was a failure to reach a backend.
//
// Unary transcoding is the one gRPC surface Jul can retry honestly. The request
// is an in-memory dynamicpb.Message, so replay costs nothing and cannot fail;
// the response is fully unmarshalled before a byte is written, so the retry
// boundary is the whole call rather than a point inside a stream.
func (t *Transcoder) serveUnary(w http.ResponseWriter, r *http.Request, rt *route, vars map[string]string, method string) {
	req := dynamicpb.NewMessage(rt.method.Input())
	if err := t.buildRequest(req, rt, vars, r); err != nil {
		code := requestErrorStatus(err)
		t.writeError(w, code, err.Error())
		t.report(method, code)
		return
	}

	var resp *dynamicpb.Message
	_, err := t.pool.Do(r.Context(), t.pool.RetryRequestFor(t.retry, retryableRoute(rt)),
		func(ctx context.Context, b *upstream.Backend, n int) upstream.AttemptResult {
			conn, cerr := t.connFor(b.Address)
			if cerr != nil {
				t.pool.MarkFailure(b)
				return upstream.AttemptResult{Err: &backendDialError{err: cerr}}
			}
			// A fresh output message per attempt: reusing one would let a
			// partially unmarshalled failed response merge into the next.
			out := dynamicpb.NewMessage(rt.method.Output())
			if ierr := conn.Invoke(outgoingContext(r), grpcMethodPath(rt.method), req, out); ierr != nil {
				code := status.Code(ierr)
				if isBackendFailure(code) {
					t.pool.MarkFailure(b)
				}
				// Only Unavailable means "this backend could not take the
				// call". Every other code is the application's answer, and
				// asking a different backend the same question would just get
				// the same answer more expensively.
				return upstream.AttemptResult{Err: ierr, Terminal: code != codes.Unavailable}
			}
			t.pool.MarkSuccess(b)
			resp = out
			return upstream.AttemptResult{}
		})
	if err != nil {
		code, msg := unaryErrorResponse(err)
		t.writeError(w, code, msg)
		t.report(method, code)
		return
	}

	body, err := protojson.MarshalOptions{
		UseProtoNames:   t.preserveNames,
		EmitUnpopulated: true,
	}.Marshal(resp)
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

// retryableRoute applies ADR 0017's idempotency gate for transcoded unary
// calls: the HTTP method is retry-safe, or the method itself declares that
// repeating it is harmless.
//
// The proto annotation is consulted because the HTTP binding alone is too
// coarse. A method mapped to POST purely because it takes a request body may
// still be declared NO_SIDE_EFFECTS, and refusing to retry it would waste the
// author's explicit statement. The default, IDEMPOTENCY_UNKNOWN, is never
// retried: silence is not a promise.
func retryableRoute(rt *route) bool {
	if upstream.RetrySafeMethod(rt.httpMethod) {
		return true
	}
	opts, _ := rt.method.Options().(*descriptorpb.MethodOptions)
	switch opts.GetIdempotencyLevel() {
	case descriptorpb.MethodOptions_NO_SIDE_EFFECTS, descriptorpb.MethodOptions_IDEMPOTENT:
		return true
	default:
		return false
	}
}

// backendDialError marks a failure to establish a connection, which is distinct
// from a gRPC status: nothing was invoked, so it maps to 502 rather than to
// whatever code an absent response would decode as.
type backendDialError struct{ err error }

func (e *backendDialError) Error() string { return e.err.Error() }
func (e *backendDialError) Unwrap() error { return e.err }

// unaryErrorResponse maps a failed call to a client status. Backend supply,
// connection establishment and application errors are three different answers
// and are not collapsed into one.
func unaryErrorResponse(err error) (int, string) {
	var dial *backendDialError
	switch {
	case errors.Is(err, upstream.ErrNoAvailableBackend), errors.Is(err, upstream.ErrBackendAtCapacity):
		return http.StatusServiceUnavailable, "no available gRPC backend: " + err.Error()
	case errors.As(err, &dial):
		return http.StatusBadGateway, "grpc backend unreachable: " + err.Error()
	default:
		return httpStatusFromCode(status.Code(err)), status.Convert(err).Message()
	}
}

// match returns the first route whose HTTP method and path template match the
// request, along with the captured path variables.
func (t *Transcoder) match(httpMethod, path string) (*route, map[string]string) {
	for _, rt := range t.routes {
		if rt.httpMethod != httpMethod {
			continue
		}
		if vars, ok := rt.template.match(path); ok {
			return rt, vars
		}
	}
	return nil, nil
}

// buildRequest populates a dynamic request message from the HTTP request: the
// JSON body (per the binding's body mapping), then path variables (which
// override the body), then query parameters (when the body is not the whole
// message).
func (t *Transcoder) buildRequest(msg *dynamicpb.Message, rt *route, vars map[string]string, r *http.Request) error {
	switch rt.body {
	case "":
		// No body mapping.
	case "*":
		body, err := readBody(r, t.maxMsg)
		if err != nil {
			return err
		}
		if len(body) > 0 {
			if err := protojson.Unmarshal(body, msg); err != nil {
				return fmt.Errorf("decode JSON body: %w", err)
			}
		}
	default:
		body, err := readBody(r, t.maxMsg)
		if err != nil {
			return err
		}
		if len(body) > 0 {
			sub, err := mutableMessageField(msg, rt.body)
			if err != nil {
				return err
			}
			if err := protojson.Unmarshal(body, sub); err != nil {
				return fmt.Errorf("decode JSON body into %q: %w", rt.body, err)
			}
		}
	}

	for field, value := range vars {
		if err := setFieldByPath(msg.ProtoReflect(), strings.Split(field, "."), value); err != nil {
			return fmt.Errorf("path variable %q: %w", field, err)
		}
	}

	if rt.body != "*" {
		for key, values := range r.URL.Query() {
			if _, captured := vars[key]; captured {
				continue
			}
			path := strings.Split(key, ".")
			for _, v := range values {
				// Query parameters that don't map to a field are ignored for
				// leniency rather than failing the request.
				if err := setFieldByPath(msg.ProtoReflect(), path, v); err != nil {
					break
				}
			}
		}
	}
	return nil
}

func readBody(r *http.Request, limit int) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	// Read one byte past the limit so an oversize body is rejected (413) rather
	// than silently truncated into a malformed, partially decoded message.
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(body) > limit {
		return nil, errBodyTooLarge
	}
	return body, nil
}

// errBodyTooLarge marks a request body that exceeds the transcoder's per-message
// limit so callers can map it to 413 Request Entity Too Large.
var errBodyTooLarge = errors.New("request body exceeds max_message_size")

// requestErrorStatus maps a request-construction error to an HTTP status: an
// oversize body is 413, anything else is a 400 decode/validation error.
func requestErrorStatus(err error) int {
	if errors.Is(err, errBodyTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// mutableMessageField returns the singular message field named name as a
// proto.Message ready for protojson decoding.
func mutableMessageField(msg *dynamicpb.Message, name string) (proto.Message, error) {
	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return nil, fmt.Errorf("body field %q not found in %s", name, m.Descriptor().FullName())
	}
	if fd.IsList() || fd.IsMap() || fd.Message() == nil {
		return nil, fmt.Errorf("body field %q must be a singular message", name)
	}
	return m.Mutable(fd).Message().Interface(), nil
}

// setFieldByPath sets a scalar (or appends to a repeated scalar) at the given
// proto field path, descending into singular message fields and converting the
// string value to the leaf field's type.
func setFieldByPath(m protoreflect.Message, path []string, value string) error {
	for i := 0; i < len(path)-1; i++ {
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(path[i]))
		if fd == nil {
			return fmt.Errorf("unknown field %q", path[i])
		}
		if fd.IsList() || fd.IsMap() || fd.Message() == nil {
			return fmt.Errorf("field %q is not a singular message", path[i])
		}
		m = m.Mutable(fd).Message()
	}
	leaf := path[len(path)-1]
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(leaf))
	if fd == nil {
		return fmt.Errorf("unknown field %q", leaf)
	}
	if fd.IsMap() {
		return fmt.Errorf("field %q is a map and cannot be set from a path or query", leaf)
	}
	v, err := parseScalar(fd, value)
	if err != nil {
		return err
	}
	if fd.IsList() {
		m.Mutable(fd).List().Append(v)
		return nil
	}
	m.Set(fd, v)
	return nil
}

// parseScalar converts a string to a protoreflect.Value for a scalar (or enum)
// field.
func parseScalar(fd protoreflect.FieldDescriptor, s string) (protoreflect.Value, error) {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid bool %q", s)
		}
		return protoreflect.ValueOfBool(b), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid int32 %q", s)
		}
		return protoreflect.ValueOfInt32(int32(n)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid int64 %q", s)
		}
		return protoreflect.ValueOfInt64(n), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid uint32 %q", s)
		}
		return protoreflect.ValueOfUint32(uint32(n)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid uint64 %q", s)
		}
		return protoreflect.ValueOfUint64(n), nil
	case protoreflect.FloatKind:
		f, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid float %q", s)
		}
		return protoreflect.ValueOfFloat32(float32(f)), nil
	case protoreflect.DoubleKind:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid double %q", s)
		}
		return protoreflect.ValueOfFloat64(f), nil
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(s), nil
	case protoreflect.BytesKind:
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid base64 bytes %q", s)
		}
		return protoreflect.ValueOfBytes(b), nil
	case protoreflect.EnumKind:
		if ev := fd.Enum().Values().ByName(protoreflect.Name(s)); ev != nil {
			return protoreflect.ValueOfEnum(ev.Number()), nil
		}
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid enum value %q", s)
		}
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(n)), nil
	default:
		return protoreflect.Value{}, fmt.Errorf("unsupported field kind %v for path/query mapping", fd.Kind())
	}
}

// outgoingContext forwards a small set of request headers to the gRPC backend as
// metadata, preserving the request deadline already present on the context.
// The Authorization header and any "Grpc-Metadata-<key>" headers are mapped to
// gRPC metadata (the latter following the grpc-gateway convention).
func outgoingContext(r *http.Request) context.Context {
	md := metadata.MD{}
	if a := r.Header.Get("Authorization"); a != "" {
		md.Set("authorization", a)
	}
	for key, values := range r.Header {
		rest, ok := cutPrefixFold(key, "Grpc-Metadata-")
		if !ok || rest == "" {
			continue
		}
		md.Append(strings.ToLower(rest), values...)
	}
	if len(md) == 0 {
		return r.Context()
	}
	return metadata.NewOutgoingContext(r.Context(), md)
}

// cutPrefixFold reports whether s starts with prefix (case-insensitively) and
// returns the remainder. It avoids allocating a lower-cased copy of s.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) {
		return "", false
	}
	if !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// grpcMethodPath builds the "/package.Service/Method" path for a unary call.
func grpcMethodPath(md protoreflect.MethodDescriptor) string {
	return "/" + string(md.Parent().FullName()) + "/" + string(md.Name())
}

func (t *Transcoder) report(method string, code int) {
	if t.onResult != nil {
		t.onResult(method, strconv.Itoa(code))
	}
}

// writeError renders an RFC 7807-style problem document.
func (t *Transcoder) writeError(w http.ResponseWriter, code int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": code,
		"title":  http.StatusText(code),
		"detail": detail,
	})
}

// httpStatusFromCode maps a gRPC status code to an HTTP status, following the
// standard transcoding conventions.
func httpStatusFromCode(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.Canceled:
		return 499 // client closed request
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		return http.StatusConflict
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.Unknown, codes.Internal, codes.DataLoss:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// isBackendFailure checks if a gRPC status code indicates a backend failure
// that should be recorded by the pool's passive health mechanism.
func isBackendFailure(c codes.Code) bool {
	switch c {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Internal, codes.Unknown, codes.DataLoss:
		return true
	}
	return false
}
