//go:build grpc

package transcode

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	refv1 "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// route binds an HTTP method + path template to a unary gRPC method.
type route struct {
	httpMethod string
	template   *pathTemplate
	body       string // "*", a request field name, or "" (no body)
	method     protoreflect.MethodDescriptor
	streaming  bool
}

// httpBinding is one method<->HTTP mapping extracted from an HttpRule (the
// primary pattern or one of its additional_bindings).
type httpBinding struct {
	method string
	path   string
	body   string
}

// loadRoutesFromFile reads a protoc FileDescriptorSet and builds the route table
// from the google.api.http annotations it carries.
func loadRoutesFromFile(path string) ([]*route, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read descriptor_set: %w", err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("parse descriptor_set: %w", err)
	}
	return routesFromSet(&set)
}

// loadRoutesViaReflection fetches descriptors from a backend over gRPC server
// reflection and builds the route table.
func loadRoutesViaReflection(ctx context.Context, conn *grpc.ClientConn) ([]*route, error) {
	set, err := fetchDescriptorSet(ctx, conn)
	if err != nil {
		return nil, err
	}
	return routesFromSet(set)
}

func routesFromSet(set *descriptorpb.FileDescriptorSet) ([]*route, error) {
	files, err := filesFromSet(set)
	if err != nil {
		return nil, err
	}
	return routesFromFiles(files)
}

// routesFromFiles scans every method in every file for a google.api.http
// annotation and compiles the resulting bindings into routes.
func routesFromFiles(files *protoregistry.Files) ([]*route, error) {
	var routes []*route
	var firstErr error
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			methods := svcs.Get(i).Methods()
			for j := 0; j < methods.Len(); j++ {
				md := methods.Get(j)
				rule := httpRuleOf(md)
				if rule == nil {
					continue
				}
				for _, b := range flattenRule(rule) {
					tmpl, err := parseTemplate(b.path)
					if err != nil {
						firstErr = fmt.Errorf("%s: %w", md.FullName(), err)
						return false
					}
					routes = append(routes, &route{
						httpMethod: b.method,
						template:   tmpl,
						body:       b.body,
						method:     md,
						streaming:  md.IsStreamingClient() || md.IsStreamingServer(),
					})
				}
			}
		}
		return true
	})
	if firstErr != nil {
		return nil, firstErr
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("no methods with google.api.http annotations were found in the descriptors")
	}
	return routes, nil
}

// httpRuleOf returns the google.api.http annotation on a method, or nil.
func httpRuleOf(md protoreflect.MethodDescriptor) *annotations.HttpRule {
	opts := md.Options()
	if opts == nil || !proto.HasExtension(opts, annotations.E_Http) {
		return nil
	}
	rule, _ := proto.GetExtension(opts, annotations.E_Http).(*annotations.HttpRule)
	return rule
}

// flattenRule expands an HttpRule (its primary pattern plus any
// additional_bindings) into concrete HTTP bindings.
func flattenRule(rule *annotations.HttpRule) []httpBinding {
	var out []httpBinding
	if b, ok := bindingOf(rule); ok {
		out = append(out, b)
	}
	for _, ab := range rule.GetAdditionalBindings() {
		if b, ok := bindingOf(ab); ok {
			out = append(out, b)
		}
	}
	return out
}

func bindingOf(rule *annotations.HttpRule) (httpBinding, bool) {
	b := httpBinding{body: rule.GetBody()}
	switch p := rule.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		b.method, b.path = "GET", p.Get
	case *annotations.HttpRule_Put:
		b.method, b.path = "PUT", p.Put
	case *annotations.HttpRule_Post:
		b.method, b.path = "POST", p.Post
	case *annotations.HttpRule_Delete:
		b.method, b.path = "DELETE", p.Delete
	case *annotations.HttpRule_Patch:
		b.method, b.path = "PATCH", p.Patch
	case *annotations.HttpRule_Custom:
		b.method, b.path = strings.ToUpper(p.Custom.GetKind()), p.Custom.GetPath()
	default:
		return httpBinding{}, false
	}
	if b.method == "" || b.path == "" {
		return httpBinding{}, false
	}
	return b, true
}

// combinedResolver resolves descriptor imports from a primary registry first and
// falls back to the global registry. The fallback lets descriptor sets that omit
// well-known imports (e.g. google/api/annotations.proto, which is linked into
// the binary) still build.
type combinedResolver struct {
	primary  *protoregistry.Files
	fallback *protoregistry.Files
}

func (c *combinedResolver) FindFileByPath(path string) (protoreflect.FileDescriptor, error) {
	if fd, err := c.primary.FindFileByPath(path); err == nil {
		return fd, nil
	}
	return c.fallback.FindFileByPath(path)
}

func (c *combinedResolver) FindDescriptorByName(name protoreflect.FullName) (protoreflect.Descriptor, error) {
	if d, err := c.primary.FindDescriptorByName(name); err == nil {
		return d, nil
	}
	return c.fallback.FindDescriptorByName(name)
}

// filesFromSet builds a file registry from a FileDescriptorSet, registering each
// file after its dependencies (resolving missing well-known imports from the
// global registry).
func filesFromSet(set *descriptorpb.FileDescriptorSet) (*protoregistry.Files, error) {
	result := new(protoregistry.Files)
	resolver := &combinedResolver{primary: result, fallback: protoregistry.GlobalFiles}

	byName := make(map[string]*descriptorpb.FileDescriptorProto, len(set.GetFile()))
	for _, fdp := range set.GetFile() {
		byName[fdp.GetName()] = fdp
	}

	done := make(map[string]bool)
	inProgress := make(map[string]bool)
	var register func(name string) error
	register = func(name string) error {
		if done[name] {
			return nil
		}
		fdp, ok := byName[name]
		if !ok {
			// Not in the set: expect it from the global registry (e.g. an import
			// of google/api/annotations.proto when generated without
			// --include_imports).
			if _, err := protoregistry.GlobalFiles.FindFileByPath(name); err == nil {
				done[name] = true
				return nil
			}
			return fmt.Errorf("descriptor set is missing imported file %q (regenerate with --include_imports)", name)
		}
		if inProgress[name] {
			return fmt.Errorf("descriptor set has an import cycle involving %q", name)
		}
		inProgress[name] = true
		for _, dep := range fdp.GetDependency() {
			if err := register(dep); err != nil {
				return err
			}
		}
		fd, err := protodesc.NewFile(fdp, resolver)
		if err != nil {
			return fmt.Errorf("build file %q: %w", name, err)
		}
		if err := result.RegisterFile(fd); err != nil {
			return fmt.Errorf("register file %q: %w", name, err)
		}
		delete(inProgress, name)
		done[name] = true
		return nil
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic registration and error reporting
	for _, n := range names {
		if err := register(n); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// fetchDescriptorSet retrieves the descriptors for every service exposed by a
// backend via gRPC server reflection, resolving transitive imports by filename.
func fetchDescriptorSet(ctx context.Context, conn *grpc.ClientConn) (*descriptorpb.FileDescriptorSet, error) {
	stream, err := refv1.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("reflection: %w", err)
	}
	defer func() { _ = stream.CloseSend() }()

	send := func(req *refv1.ServerReflectionRequest) (*refv1.ServerReflectionResponse, error) {
		if err := stream.Send(req); err != nil {
			return nil, err
		}
		return stream.Recv()
	}

	resp, err := send(&refv1.ServerReflectionRequest{
		MessageRequest: &refv1.ServerReflectionRequest_ListServices{ListServices: ""},
	})
	if err != nil {
		return nil, fmt.Errorf("reflection list services: %w", err)
	}
	list := resp.GetListServicesResponse()
	if list == nil {
		return nil, fmt.Errorf("reflection: unexpected response to ListServices")
	}

	collected := make(map[string]*descriptorpb.FileDescriptorProto)
	seen := make(map[string]bool)
	var pending []string

	addFiles := func(fdResp *refv1.FileDescriptorResponse) error {
		for _, raw := range fdResp.GetFileDescriptorProto() {
			fdp := &descriptorpb.FileDescriptorProto{}
			if err := proto.Unmarshal(raw, fdp); err != nil {
				return fmt.Errorf("reflection: decode file descriptor: %w", err)
			}
			if _, ok := collected[fdp.GetName()]; ok {
				continue
			}
			collected[fdp.GetName()] = fdp
			for _, dep := range fdp.GetDependency() {
				if !seen[dep] {
					seen[dep] = true
					pending = append(pending, dep)
				}
			}
		}
		return nil
	}

	for _, svc := range list.GetService() {
		switch svc.GetName() {
		case "grpc.reflection.v1.ServerReflection", "grpc.reflection.v1alpha.ServerReflection":
			continue
		}
		resp, err := send(&refv1.ServerReflectionRequest{
			MessageRequest: &refv1.ServerReflectionRequest_FileContainingSymbol{FileContainingSymbol: svc.GetName()},
		})
		if err != nil {
			return nil, fmt.Errorf("reflection file for %s: %w", svc.GetName(), err)
		}
		if e := resp.GetErrorResponse(); e != nil {
			return nil, fmt.Errorf("reflection file for %s: %s", svc.GetName(), e.GetErrorMessage())
		}
		if err := addFiles(resp.GetFileDescriptorResponse()); err != nil {
			return nil, err
		}
	}

	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		if _, ok := collected[name]; ok {
			continue
		}
		resp, err := send(&refv1.ServerReflectionRequest{
			MessageRequest: &refv1.ServerReflectionRequest_FileByFilename{FileByFilename: name},
		})
		if err != nil {
			return nil, fmt.Errorf("reflection file %q: %w", name, err)
		}
		if resp.GetErrorResponse() != nil {
			// A well-known import the server does not serve may still be linked
			// into our binary; filesFromSet's fallback resolver handles it.
			continue
		}
		if err := addFiles(resp.GetFileDescriptorResponse()); err != nil {
			return nil, err
		}
	}

	set := &descriptorpb.FileDescriptorSet{}
	for _, fdp := range collected {
		set.File = append(set.File, fdp)
	}
	return set, nil
}
