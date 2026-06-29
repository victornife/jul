//go:build grpc

package transcode

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/upstream"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// streamingFileDescriptorProto builds a descriptor for a tiny streaming service:
//
//	message Item { string value = 1; }
//	service StreamEcho {
//	  rpc Down(Item) returns (stream Item)        { post: "/v1/down"  body: "*" } // server-streaming
//	  rpc Up(stream Item) returns (Item)          { post: "/v1/up"    body: "*" } // client-streaming
//	  rpc Both(stream Item) returns (stream Item) { post: "/v1/both"  body: "*" } // bidirectional
//	}
func streamingFileDescriptorProto(t *testing.T) *descriptorpb.FileDescriptorProto {
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
	httpOpts := func(path string) *descriptorpb.MethodOptions {
		o := &descriptorpb.MethodOptions{}
		proto.SetExtension(o, annotations.E_Http, &annotations.HttpRule{
			Pattern: &annotations.HttpRule_Post{Post: path},
			Body:    "*",
		})
		return o
	}
	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String("streamecho/streamecho.proto"),
		Package:    proto.String("streamecho"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/api/annotations.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Item"), Field: []*descriptorpb.FieldDescriptorProto{strField("value", 1)}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("StreamEcho"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{
					Name:            proto.String("Down"),
					InputType:       proto.String(".streamecho.Item"),
					OutputType:      proto.String(".streamecho.Item"),
					ServerStreaming: proto.Bool(true),
					Options:         httpOpts("/v1/down"),
				},
				{
					Name:            proto.String("Up"),
					InputType:       proto.String(".streamecho.Item"),
					OutputType:      proto.String(".streamecho.Item"),
					ClientStreaming: proto.Bool(true),
					Options:         httpOpts("/v1/up"),
				},
				{
					Name:            proto.String("Both"),
					InputType:       proto.String(".streamecho.Item"),
					OutputType:      proto.String(".streamecho.Item"),
					ClientStreaming: proto.Bool(true),
					ServerStreaming: proto.Bool(true),
					Options:         httpOpts("/v1/both"),
				},
			},
		}},
	}
}

// startStreamEchoServer serves the StreamEcho service with dynamic handlers:
// Down splits the request value on commas and streams a message per part; Up
// joins all received values with "+" and returns one message; Both echoes each
// received message with a "echo:" prefix.
func startStreamEchoServer(t *testing.T, fd protoreflect.FileDescriptor) string {
	t.Helper()
	itemDesc := fd.Messages().ByName("Item")
	valueField := itemDesc.Fields().ByName("value")
	newItem := func(v string) *dynamicpb.Message {
		m := dynamicpb.NewMessage(itemDesc)
		m.Set(valueField, protoreflect.ValueOfString(v))
		return m
	}
	valueOf := func(m *dynamicpb.Message) string {
		return m.Get(valueField).String()
	}

	down := func(_ any, stream grpc.ServerStream) error {
		in := dynamicpb.NewMessage(itemDesc)
		if err := stream.RecvMsg(in); err != nil {
			return err
		}
		for _, part := range strings.Split(valueOf(in), ",") {
			if err := stream.SendMsg(newItem(part)); err != nil {
				return err
			}
		}
		return nil
	}
	up := func(_ any, stream grpc.ServerStream) error {
		var parts []string
		for {
			in := dynamicpb.NewMessage(itemDesc)
			if err := stream.RecvMsg(in); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return err
			}
			parts = append(parts, valueOf(in))
		}
		return stream.SendMsg(newItem(strings.Join(parts, "+")))
	}
	both := func(_ any, stream grpc.ServerStream) error {
		for {
			in := dynamicpb.NewMessage(itemDesc)
			if err := stream.RecvMsg(in); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			if err := stream.SendMsg(newItem("echo:" + valueOf(in))); err != nil {
				return err
			}
		}
	}

	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "streamecho.StreamEcho",
		HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{
			{StreamName: "Down", Handler: down, ServerStreams: true},
			{StreamName: "Up", Handler: up, ClientStreams: true},
			{StreamName: "Both", Handler: both, ServerStreams: true, ClientStreams: true},
		},
		Metadata: "streamecho/streamecho.proto",
	}, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// newStreamTranscoder builds a Transcoder for the streaming echo service with
// the given stream mode, using a descriptor file (no reflection).
func newStreamTranscoder(t *testing.T, streaming bool, mode string) *Transcoder {
	t.Helper()
	fdp := streamingFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("streamecho/streamecho.proto")
	if err != nil {
		t.Fatalf("find file: %v", err)
	}
	addr := startStreamEchoServer(t, fd)

	descFile := filepath.Join(t.TempDir(), "streamecho.pb")
	raw, err := proto.Marshal(set)
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	if err := os.WriteFile(descFile, raw, 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	cfg := config.GRPCTranscodeConfig{
		Target:        addr,
		DescriptorSet: descFile,
		Streaming:     streaming,
		StreamMode:    mode,
	}
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "test-stream",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails: 3,
	}, "http")
	if err != nil {
		t.Fatalf("create test pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	tr, err := New(cfg, pool, Options{})
	if err != nil {
		t.Fatalf("New transcoder: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestStreamServerStreamNDJSON(t *testing.T) {
	tr := newStreamTranscoder(t, true, "ndjson")
	res, body := doRequest(t, tr, http.MethodPost, "/v1/down", `{"value":"a,b,c"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("content-type = %q, want application/x-ndjson", ct)
	}
	got := ndjsonValues(t, body)
	want := []string{"a", "b", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("messages = %v, want %v", got, want)
	}
}

func TestStreamServerStreamSSE(t *testing.T) {
	tr := newStreamTranscoder(t, true, "sse")
	res, body := doRequest(t, tr, http.MethodPost, "/v1/down", `{"value":"x,y"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	if c := strings.Count(body, "data: "); c < 2 {
		t.Errorf("expected >=2 data frames, body = %q", body)
	}
	if !strings.Contains(body, "event: end") {
		t.Errorf("expected an end event, body = %q", body)
	}
}

func TestStreamClientStreamArray(t *testing.T) {
	tr := newStreamTranscoder(t, true, "ndjson")
	res, body := doRequest(t, tr, http.MethodPost, "/v1/up", `[{"value":"a"},{"value":"b"},{"value":"c"}]`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessageField(t, body, "value"); got != "a+b+c" {
		t.Errorf("reply = %q, want %q", got, "a+b+c")
	}
}

func TestStreamClientArrayTrailingRejected(t *testing.T) {
	tr := newStreamTranscoder(t, true, "ndjson")
	for _, body := range []string{`[{"value":"a"}]{"value":"b"}`, `[{"value":"a"}]5`, `[{"value":"a"}] true`} {
		res, rb := doRequest(t, tr, http.MethodPost, "/v1/up", body, nil)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400; reply = %s", body, res.StatusCode, rb)
		}
	}
}

func TestStreamClientStreamNDJSONBody(t *testing.T) {
	tr := newStreamTranscoder(t, true, "ndjson")
	res, body := doRequest(t, tr, http.MethodPost, "/v1/up", "{\"value\":\"one\"}\n{\"value\":\"two\"}\n", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessageField(t, body, "value"); got != "one+two" {
		t.Errorf("reply = %q, want %q", got, "one+two")
	}
}

func TestStreamBidi(t *testing.T) {
	tr := newStreamTranscoder(t, true, "ndjson")
	res, body := doRequest(t, tr, http.MethodPost, "/v1/both", `[{"value":"a"},{"value":"b"}]`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	got := ndjsonValues(t, body)
	want := []string{"echo:a", "echo:b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("messages = %v, want %v", got, want)
	}
}

func TestStreamDisabledReturns501(t *testing.T) {
	tr := newStreamTranscoder(t, false, "ndjson")
	res, body := doRequest(t, tr, http.MethodPost, "/v1/down", `{"value":"a,b"}`, nil)
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body = %s", res.StatusCode, body)
	}
}

func TestStreamMsgCounter(t *testing.T) {
	var sent, recv int
	fdp := streamingFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, _ := files.FindFileByPath("streamecho/streamecho.proto")
	addr := startStreamEchoServer(t, fd)
	descFile := filepath.Join(t.TempDir(), "s.pb")
	raw, _ := proto.Marshal(set)
	if err := os.WriteFile(descFile, raw, 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}
	tr, err := New(config.GRPCTranscodeConfig{Target: addr, DescriptorSet: descFile, Streaming: true},
		func() *upstream.Pool {
			pool, err := upstream.NewPool(config.UpstreamConfig{
				Name:     "test-msgcounter",
				Strategy: "round_robin",
				Servers:  []config.UpstreamServer{{Address: addr, Weight: 1}},
				MaxFails: 3,
			}, "http")
			if err != nil {
				t.Fatalf("create test pool: %v", err)
			}
			t.Cleanup(func() { pool.Close() })
			return pool
		}(),
		Options{OnStreamMsg: func(_, direction string) {
			switch direction {
			case "sent":
				sent++
			case "recv":
				recv++
			}
		}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	req := httptest.NewRequest(http.MethodPost, "/v1/down", strings.NewReader(`{"value":"a,b,c"}`))
	rec := httptest.NewRecorder()
	tr.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if sent != 1 {
		t.Errorf("sent = %d, want 1", sent)
	}
	if recv != 3 {
		t.Errorf("recv = %d, want 3", recv)
	}
}

// ndjsonValues parses an NDJSON stream body and returns the "value" of each
// non-empty line.
func ndjsonValues(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, replyMessageField(t, line, "value"))
	}
	return out
}

// replyMessageField decodes a single JSON object and returns the named string
// field.
func replyMessageField(t *testing.T, body, field string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode reply %q: %v", body, err)
	}
	s, _ := m[field].(string)
	return s
}

func TestStreamClientDecodeErrorDoesNotClearFailures(t *testing.T) {
	tr := newStreamTranscoder(t, true, "ndjson")
	backend := tr.pool.Backends()[0]
	// Pre-mark the backend with passive failures.
	tr.pool.MarkFailure(backend)
	tr.pool.MarkFailure(backend)
	wantFails := backend.FailCount()

	req := httptest.NewRequest(http.MethodPost, "/v1/down", strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	tr.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if backend.FailCount() != wantFails {
		t.Fatalf("fail count changed from %d to %d, expected unchanged after client decode error", wantFails, backend.FailCount())
	}
}

func TestStreamHappyPathClearsFailures(t *testing.T) {
	tr := newStreamTranscoder(t, true, "ndjson")
	backend := tr.pool.Backends()[0]
	// Pre-mark the backend with passive failures.
	tr.pool.MarkFailure(backend)
	tr.pool.MarkFailure(backend)

	res, _ := doRequest(t, tr, http.MethodPost, "/v1/down", `{"value":"a,b"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if backend.FailCount() != 0 {
		t.Fatalf("fail count = %d, want 0 after successful stream request", backend.FailCount())
	}
}

func TestStreamClientStreamDecodeErrorNeutralHealth(t *testing.T) {
	tr := newStreamTranscoder(t, true, "ndjson")
	backend := tr.pool.Backends()[0]
	tr.pool.MarkFailure(backend)
	tr.pool.MarkFailure(backend)
	wantFails := backend.FailCount()

	// Send malformed NDJSON body to a client-streaming endpoint.
	res, _ := doRequest(t, tr, http.MethodPost, "/v1/up", `not json`, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if backend.FailCount() != wantFails {
		t.Fatalf("fail count changed from %d to %d, expected unchanged after client decode error", wantFails, backend.FailCount())
	}
}
