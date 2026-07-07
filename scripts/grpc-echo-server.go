//go:build ignore

// grpc-echo-server.go is a self-contained soak-test backend that serves
// echo.EchoService on :50051.  It builds the service descriptor at runtime
// (no protoc-generated .pb.go needed) and registers both the service and
// gRPC server reflection, so it works with Jul transcoding (descriptor_set)
// and with Jul passthrough (server reflection is optional but useful).
//
// Run: go run scripts/grpc-echo-server.go [-port 50051]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var port = flag.String("port", "50051", "gRPC listen port")

func main() {
	flag.Parse()
	addr := "127.0.0.1:" + *port
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}

	fd := buildFileDescriptor()
	reqDesc, respDesc := serviceDescs(fd)

	// Register with the global registry so server reflection works.
	if _, err := protoregistry.GlobalFiles.FindFileByPath(fd.Path()); err != nil {
		if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
			log.Fatalf("register file: %v", err)
		}
	}

	g := grpc.NewServer()

	g.RegisterService(&grpc.ServiceDesc{
		ServiceName: "echo.EchoService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Echo",
			Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				in := dynamicpb.NewMessage(reqDesc)
				if err := dec(in); err != nil {
					return nil, err
				}
				msg := in.Get(reqDesc.Fields().ByName("message")).String()
				id := in.Get(reqDesc.Fields().ByName("id")).String()

				var text string
				if id != "" {
					text = fmt.Sprintf("id=%s msg=%s", id, msg)
				} else {
					text = msg
				}
				// If message contains "fail", return an error to exercise error mapping
				if msg == "fail" {
					return nil, fmt.Errorf("fail")
				}

				out := dynamicpb.NewMessage(respDesc)
				out.Set(respDesc.Fields().ByName("message"), protoreflect.ValueOfString(text))
				return out, nil
			},
		}},
		Metadata: fd.Path(),
	}, nil)

	reflection.Register(g)

	log.Printf("gRPC echo server listening on %s", addr)
	if err := g.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func buildFileDescriptor() protoreflect.FileDescriptor {
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    strPtr("echo/echo.proto"),
		Package: strPtr("echo"),
		Syntax:  strPtr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: strPtr("EchoRequest"), Field: []*descriptorpb.FieldDescriptorProto{
				strField("message", 1), strField("id", 2),
			}},
			{Name: strPtr("EchoReply"), Field: []*descriptorpb.FieldDescriptorProto{
				strField("message", 1),
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: strPtr("EchoService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       strPtr("Echo"),
				InputType:  strPtr(".echo.EchoRequest"),
				OutputType: strPtr(".echo.EchoReply"),
			}},
		}},
	}

	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		log.Fatalf("build descriptor: %v", err)
	}
	return fd
}

func strPtr(s string) *string { return &s }

func strField(name string, num int32) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     strPtr(name),
		Number:   int32Ptr(num),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		JsonName: strPtr(name),
	}
}

func int32Ptr(n int32) *int32 { return &n }

func serviceDescs(fd protoreflect.FileDescriptor) (protoreflect.MessageDescriptor, protoreflect.MessageDescriptor) {
	svc := fd.Services().Get(0)
	m := svc.Methods().Get(0)
	return m.Input(), m.Output()
}
