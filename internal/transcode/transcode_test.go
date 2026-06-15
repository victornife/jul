//go:build grpc

package transcode

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestHTTPStatusFromCode(t *testing.T) {
	cases := map[codes.Code]int{
		codes.OK:                 http.StatusOK,
		codes.InvalidArgument:    http.StatusBadRequest,
		codes.NotFound:           http.StatusNotFound,
		codes.AlreadyExists:      http.StatusConflict,
		codes.PermissionDenied:   http.StatusForbidden,
		codes.Unauthenticated:    http.StatusUnauthorized,
		codes.ResourceExhausted:  http.StatusTooManyRequests,
		codes.Unimplemented:      http.StatusNotImplemented,
		codes.Unavailable:        http.StatusServiceUnavailable,
		codes.DeadlineExceeded:   http.StatusGatewayTimeout,
		codes.Internal:           http.StatusInternalServerError,
		codes.FailedPrecondition: http.StatusBadRequest,
	}
	for code, want := range cases {
		if got := httpStatusFromCode(code); got != want {
			t.Errorf("httpStatusFromCode(%v) = %d, want %d", code, got, want)
		}
	}
}

// echoFileDescriptorProto builds the descriptor for a tiny annotated echo
// service equivalent to:
//
//	package echo;
//	message EchoRequest { string message = 1; string id = 2; }
//	message EchoReply   { string message = 1; }
//	service EchoService {
//	  rpc Echo(EchoRequest) returns (EchoReply) {
//	    option (google.api.http) = {
//	      post: "/v1/echo" body: "*"
//	      additional_bindings { get: "/v1/echo/{id}" }
//	    };
//	  }
//	}
func echoFileDescriptorProto(t *testing.T) *descriptorpb.FileDescriptorProto {
	t.Helper()
	strField := func(name string, num int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String(name),
		}
	}
	methodOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(methodOpts, annotations.E_Http, &annotations.HttpRule{
		Pattern: &annotations.HttpRule_Post{Post: "/v1/echo"},
		Body:    "*",
		AdditionalBindings: []*annotations.HttpRule{
			{Pattern: &annotations.HttpRule_Get{Get: "/v1/echo/{id}"}},
		},
	})
	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String("echo/echo.proto"),
		Package:    proto.String("echo"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/api/annotations.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("EchoRequest"), Field: []*descriptorpb.FieldDescriptorProto{strField("message", 1), strField("id", 2)}},
			{Name: proto.String("EchoReply"), Field: []*descriptorpb.FieldDescriptorProto{strField("message", 1)}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("EchoService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Echo"),
				InputType:  proto.String(".echo.EchoRequest"),
				OutputType: proto.String(".echo.EchoReply"),
				Options:    methodOpts,
			}},
		}},
	}
}

// startEchoServer registers a dynamic gRPC echo service backed by the given
// message descriptors and serves it on a local port. When withReflection is
// set, the file is registered globally and gRPC server reflection is enabled.
func startEchoServer(t *testing.T, fd protoreflect.FileDescriptor, withReflection bool) string {
	t.Helper()
	reqDesc := fd.Services().Get(0).Methods().Get(0).Input()
	respDesc := fd.Services().Get(0).Methods().Get(0).Output()

	handler := func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		in := dynamicpb.NewMessage(reqDesc)
		if err := dec(in); err != nil {
			return nil, err
		}
		m := in.ProtoReflect()
		message := m.Get(reqDesc.Fields().ByName("message")).String()
		id := m.Get(reqDesc.Fields().ByName("id")).String()

		var text string
		switch {
		case message == "fail":
			return nil, status.Error(codes.NotFound, "item not found")
		case message == "whoami":
			if md, ok := metadata.FromIncomingContext(ctx); ok {
				if v := md.Get("authorization"); len(v) > 0 {
					text = v[0]
				}
			}
		case id != "":
			text = "id=" + id + " msg=" + message
		default:
			text = message
		}

		reply := dynamicpb.NewMessage(respDesc)
		reply.ProtoReflect().Set(respDesc.Fields().ByName("message"), protoreflect.ValueOfString(text))
		return reply, nil
	}

	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "echo.EchoService",
		HandlerType: (*any)(nil),
		Methods:     []grpc.MethodDesc{{MethodName: "Echo", Handler: handler}},
		Metadata:    "echo/echo.proto",
	}, nil)

	if withReflection {
		if _, err := protoregistry.GlobalFiles.FindFileByPath("echo/echo.proto"); err != nil {
			if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
				t.Fatalf("register echo file globally: %v", err)
			}
		}
		reflection.Register(srv)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// newEchoTranscoder builds descriptors, starts the echo server, and returns a
// Transcoder pointed at it. When reflect is true it omits the descriptor file
// and uses server reflection instead.
func newEchoTranscoder(t *testing.T, reflect bool) *Transcoder {
	t.Helper()
	fdp := echoFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("echo/echo.proto")
	if err != nil {
		t.Fatalf("find echo file: %v", err)
	}

	addr := startEchoServer(t, fd, reflect)

	cfg := config.GRPCTranscodeConfig{Target: addr}
	if reflect {
		cfg.UseReflection = true
	} else {
		descFile := filepath.Join(t.TempDir(), "echo.pb")
		raw, err := proto.Marshal(set)
		if err != nil {
			t.Fatalf("marshal set: %v", err)
		}
		if err := os.WriteFile(descFile, raw, 0o600); err != nil {
			t.Fatalf("write descriptor: %v", err)
		}
		cfg.DescriptorSet = descFile
	}

	tr, err := New(cfg, nil, Options{})
	if err != nil {
		t.Fatalf("New transcoder: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func doRequest(t *testing.T, tr *Transcoder, method, target, body string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, rdr)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	tr.ServeHTTP(rec, req)
	res := rec.Result()
	return res, rec.Body.String()
}

func replyMessage(t *testing.T, body string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode reply %q: %v", body, err)
	}
	s, _ := m["message"].(string)
	return s
}

func TestTranscodePostBody(t *testing.T) {
	tr := newEchoTranscoder(t, false)
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"hi"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessage(t, body); got != "hi" {
		t.Errorf("reply message = %q, want %q", got, "hi")
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}

func TestTranscodeGetPathVar(t *testing.T) {
	tr := newEchoTranscoder(t, false)
	res, body := doRequest(t, tr, http.MethodGet, "/v1/echo/77", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessage(t, body); got != "id=77 msg=" {
		t.Errorf("reply message = %q, want %q", got, "id=77 msg=")
	}
}

func TestTranscodeNotFoundRoute(t *testing.T) {
	tr := newEchoTranscoder(t, false)
	res, _ := doRequest(t, tr, http.MethodGet, "/v1/nope", "", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content-type = %q, want application/problem+json", ct)
	}
}

func TestTranscodeGRPCErrorMapped(t *testing.T) {
	tr := newEchoTranscoder(t, false)
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"fail"}`, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (grpc NotFound); body = %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "item not found") {
		t.Errorf("body %q missing grpc status message", body)
	}
}

func TestTranscodeForwardsAuthorization(t *testing.T) {
	tr := newEchoTranscoder(t, false)
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"whoami"}`,
		map[string]string{"Authorization": "Bearer xyz"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessage(t, body); got != "Bearer xyz" {
		t.Errorf("forwarded authorization = %q, want %q", got, "Bearer xyz")
	}
}

func TestTranscodeViaReflection(t *testing.T) {
	tr := newEchoTranscoder(t, true)
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"reflected"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessage(t, body); got != "reflected" {
		t.Errorf("reply message = %q, want %q", got, "reflected")
	}
}
