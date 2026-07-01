// Package admin exposes a separate operational HTTP listener bound to loopback
// by default. This file implements the gRPC route designer endpoints that let
// operators upload compiled protobuf descriptor sets and inspect the HTTP
// bindings (google.api.http annotations) inside them. It uses only pure-Go
// protobuf packages and has no build-tag restrictions.
package admin

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// transcodeMethod is one gRPC method with its HTTP binding extracted from a
// descriptor set. The frontend shows these in a selectable table so the operator
// can pick which methods to expose via REST/JSON transcoding.
type transcodeMethod struct {
	FullName   string `json:"full_name"`   // e.g. "echo.Echo.SayHello"
	Service    string `json:"service"`     // e.g. "echo.Echo"
	Method     string `json:"method"`      // e.g. "SayHello"
	HTTPMethod string `json:"http_method"` // e.g. "GET"
	Path       string `json:"path"`        // e.g. "/v1/hello/{name}"
	Body       string `json:"body"`        // "*", a field name, or ""
	Streaming  bool   `json:"streaming"`   // client and/or server streaming
}

// handleTranscodeDescriptorUpload serves POST /api/transcode/descriptor-upload.
// It accepts a multipart form with a "descriptor" field containing a
// protoc-generated FileDescriptorSet binary, validates it, extracts every
// method that carries a google.api.http annotation, and returns them as JSON.
func (s *Server) handleTranscodeDescriptorUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	const maxDescriptorSize = 16 << 20                               // 16 MiB
	r.Body = http.MaxBytesReader(w, r.Body, maxDescriptorSize+1<<20) // +1 MiB overhead for multipart boundaries
	if err := r.ParseMultipartForm(maxDescriptorSize); err != nil {
		if err.Error() == "multipart: message too large" || err.Error() == "http: request body too large" {
			http.Error(w, "descriptor exceeds size limit", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}
	defer func() {
		_ = r.MultipartForm.RemoveAll()
	}()

	file, _, err := r.FormFile("descriptor")
	if err != nil {
		http.Error(w, "missing descriptor field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, maxDescriptorSize+1))
	if err != nil {
		http.Error(w, "failed to read descriptor", http.StatusInternalServerError)
		return
	}
	if len(raw) > maxDescriptorSize {
		http.Error(w, "descriptor exceeds 16 MiB", http.StatusRequestEntityTooLarge)
		return
	}

	methods, parseErr := parseDescriptorMethods(raw)
	if parseErr != nil {
		http.Error(w, parseErr.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"methods": methods,
	})
}

// parseDescriptorMethods unmarshals a FileDescriptorSet and returns every
// method that has a google.api.http annotation.
func parseDescriptorMethods(raw []byte) ([]transcodeMethod, error) {
	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &fds); err != nil {
		return nil, fmt.Errorf("invalid FileDescriptorSet: %w", err)
	}

	files, err := buildFileRegistry(&fds)
	if err != nil {
		return nil, fmt.Errorf("invalid descriptors: %w", err)
	}

	var methods []transcodeMethod
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			svc := svcs.Get(i)
			mds := svc.Methods()
			for j := 0; j < mds.Len(); j++ {
				md := mds.Get(j)
				rule := httpRuleOf(md)
				if rule == nil {
					continue
				}
				bindings := flattenRule(rule)
				for _, b := range bindings {
					methods = append(methods, transcodeMethod{
						FullName:   string(md.FullName()),
						Service:    string(svc.FullName()),
						Method:     string(md.Name()),
						HTTPMethod: b.method,
						Path:       b.path,
						Body:       b.body,
						Streaming:  md.IsStreamingClient() || md.IsStreamingServer(),
					})
				}
			}
		}
		return true
	})

	if len(methods) == 0 {
		return nil, fmt.Errorf("no methods with google.api.http annotations were found")
	}
	return methods, nil
}

// buildFileRegistry builds a protoregistry.Files from a FileDescriptorSet,
// resolving imports from the set itself and falling back to the global
// registry for well-known types (e.g. google/api/annotations.proto).
func buildFileRegistry(set *descriptorpb.FileDescriptorSet) (*protoregistry.Files, error) {
	result := new(protoregistry.Files)
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
			if _, err := protoregistry.GlobalFiles.FindFileByPath(name); err == nil {
				done[name] = true
				return nil
			}
			return fmt.Errorf("missing imported file %q (regenerate with --include_imports)", name)
		}
		if inProgress[name] {
			return fmt.Errorf("import cycle involving %q", name)
		}
		inProgress[name] = true
		for _, dep := range fdp.GetDependency() {
			if err := register(dep); err != nil {
				return err
			}
		}
		inProgress[name] = false
		fd, err := protodesc.NewFile(fdp, &combinedResolver{primary: result, fallback: protoregistry.GlobalFiles})
		if err != nil {
			return fmt.Errorf("invalid file %q: %w", name, err)
		}
		if err := result.RegisterFile(fd); err != nil {
			return fmt.Errorf("register file %q: %w", name, err)
		}
		done[name] = true
		return nil
	}

	for _, fdp := range set.GetFile() {
		if err := register(fdp.GetName()); err != nil {
			return nil, err
		}
	}
	return result, nil
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

// httpBinding is one method<->HTTP mapping extracted from an HttpRule.
type httpBinding struct {
	method string
	path   string
	body   string
}

// flattenRule expands an HttpRule (primary pattern + additional_bindings) into
// concrete HTTP bindings.
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

// combinedResolver resolves descriptor imports from a primary registry first
// and falls back to the global registry.
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
