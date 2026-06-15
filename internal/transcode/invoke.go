//go:build grpc

package transcode

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"jul/internal/config"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// maxBodyBytes bounds the JSON request body the transcoder reads. The body-limit
// middleware may impose a smaller cap; this is a defensive ceiling.
const maxBodyBytes = 4 << 20 // 4 MiB

// Options carries optional hooks for a Transcoder.
type Options struct {
	Logger   *slog.Logger
	OnResult func(method, code string)
	// reflectTimeout bounds the reflection fetch at construction (default 10s).
	// It is unexported and used only by tests.
	reflectTimeout time.Duration
}

// Transcoder maps REST/JSON requests to unary gRPC calls on a backend. It
// implements http.Handler and io.Closer; closing it releases the backend
// connection when the configuration is replaced.
type Transcoder struct {
	routes        []*route
	conn          *grpc.ClientConn
	preserveNames bool
	log           *slog.Logger
	onResult      func(method, code string)
}

// New builds a Transcoder from a location's grpc_transcode config. It loads the
// method routing table from the configured descriptor source, dials the gRPC
// backend (h2c or TLS), and returns a handler. The caller closes the handler to
// release the connection when the configuration is replaced.
func New(cfg config.GRPCTranscodeConfig, upstreams map[string]config.UpstreamConfig, opts Options) (*Transcoder, error) {
	addr, err := resolveTarget(cfg.Target, upstreams)
	if err != nil {
		return nil, err
	}
	conn, err := dial(addr, cfg.TLS)
	if err != nil {
		return nil, err
	}

	var routes []*route
	if cfg.UseReflection {
		timeout := opts.reflectTimeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		routes, err = loadRoutesViaReflection(ctx, conn)
		cancel()
	} else {
		routes, err = loadRoutesFromFile(cfg.DescriptorSet)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("grpc_transcode %s: %w", cfg.Target, err)
	}

	return &Transcoder{
		routes:        routes,
		conn:          conn,
		preserveNames: cfg.PreserveNames,
		log:           opts.Logger,
		onResult:      opts.OnResult,
	}, nil
}

// Close releases the backend connection.
func (t *Transcoder) Close() error {
	return t.conn.Close()
}

// resolveTarget maps a configured target to a dial address. A name matching a
// configured upstream resolves to its first server (the MVP dials a single
// backend; balancing across multiple gRPC backends is a later enhancement);
// otherwise the target is used verbatim as host:port.
func resolveTarget(target string, upstreams map[string]config.UpstreamConfig) (string, error) {
	if up, ok := upstreams[target]; ok {
		if len(up.Servers) == 0 {
			return "", fmt.Errorf("grpc_transcode target upstream %q has no servers", target)
		}
		return up.Servers[0].Address, nil
	}
	return target, nil
}

// dial creates a lazy gRPC client connection to addr over TLS or plaintext
// HTTP/2 (h2c). The passthrough scheme dials the address directly without name
// resolution.
func dial(addr string, useTLS bool) (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
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
		t.writeError(w, http.StatusNotImplemented, "streaming methods are not supported by the transcoding MVP")
		t.report(method, http.StatusNotImplemented)
		return
	}

	req := dynamicpb.NewMessage(rt.method.Input())
	if err := t.buildRequest(req, rt, vars, r); err != nil {
		t.writeError(w, http.StatusBadRequest, err.Error())
		t.report(method, http.StatusBadRequest)
		return
	}

	resp := dynamicpb.NewMessage(rt.method.Output())
	if err := t.conn.Invoke(outgoingContext(r), grpcMethodPath(rt.method), req, resp); err != nil {
		code := httpStatusFromCode(status.Code(err))
		t.writeError(w, code, status.Convert(err).Message())
		t.report(method, code)
		return
	}

	out, err := protojson.MarshalOptions{
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
	_, _ = w.Write(out)
	t.report(method, http.StatusOK)
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
		body, err := readBody(r)
		if err != nil {
			return err
		}
		if len(body) > 0 {
			if err := protojson.Unmarshal(body, msg); err != nil {
				return fmt.Errorf("decode JSON body: %w", err)
			}
		}
	default:
		body, err := readBody(r)
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

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	return body, nil
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
func outgoingContext(r *http.Request) context.Context {
	md := metadata.MD{}
	if a := r.Header.Get("Authorization"); a != "" {
		md.Set("authorization", a)
	}
	if len(md) == 0 {
		return r.Context()
	}
	return metadata.NewOutgoingContext(r.Context(), md)
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
