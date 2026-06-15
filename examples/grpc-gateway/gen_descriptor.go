//go:build ignore

// gen_descriptor.go writes a compiled FileDescriptorSet for echo.proto without
// needing protoc installed. It is the programmatic equivalent of:
//
//	protoc --include_imports --descriptor_set_out=api.pb --proto_path=. echo.proto
//
// Run it from this directory:
//
//	go run gen_descriptor.go            # writes ./api.pb
//	go run gen_descriptor.go out.pb     # custom output path
package main

import (
	"os"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func strField(name string, num int32) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Number:   proto.Int32(num),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		JsonName: proto.String(name),
	}
}

func main() {
	out := "api.pb"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}

	// option (google.api.http) = { post: "/v1/echo" body: "*"
	//                              additional_bindings { get: "/v1/echo/{id}" } };
	methodOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(methodOpts, annotations.E_Http, &annotations.HttpRule{
		Pattern: &annotations.HttpRule_Post{Post: "/v1/echo"},
		Body:    "*",
		AdditionalBindings: []*annotations.HttpRule{
			{Pattern: &annotations.HttpRule_Get{Get: "/v1/echo/{id}"}},
		},
	})

	fdp := &descriptorpb.FileDescriptorProto{
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

	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	raw, err := proto.Marshal(set)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		panic(err)
	}
}
