// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"jul/internal/config"
)

// registeredExtractors maps every lifecycle registry path to a function that
// returns its effective value in a serialization-stable form. The map is built
// automatically from the registry and the config schema, so a newly added
// registry entry gets an extractor without a hand-written case.
var registeredExtractors = map[string]func(*config.Config) any{}

func init() {
	for _, e := range Registry {
		path := e.Path
		if fn, ok := specialExtractor(path); ok {
			registeredExtractors[path] = fn
			continue
		}
		registeredExtractors[path] = func(cfg *config.Config) any {
			return extractGeneric(cfg, path)
		}
	}
}

// extractGeneric navigates a config value from a dotted registry path using
// reflection over the TOML-tagged schema. Wildcard segments iterate over the
// matching slice or map and return a map keyed by the element's identity
// (server key, location key, upstream name, stream key, or map key).
func extractGeneric(cfg *config.Config, path string) any {
	return walkPath(reflect.ValueOf(cfg), strings.Split(path, "."))
}

// walkPath recursively navigates v along the remaining path segments and
// returns a normalized representation.
func walkPath(v reflect.Value, parts []string) any {
	if len(parts) == 0 {
		return normalizeValue(v)
	}

	part := parts[0]
	rest := parts[1:]

	if part == "*" {
		switch v.Kind() {
		case reflect.Slice, reflect.Array:
			out := make(map[string]any, v.Len())
			for i := 0; i < v.Len(); i++ {
				elem := v.Index(i)
				key := collectionKey(elem)
				out[key] = walkPath(elem, rest)
			}
			return out
		case reflect.Map:
			out := make(map[string]any, v.Len())
			for _, k := range v.MapKeys() {
				key := fmt.Sprint(k.Interface())
				out[key] = walkPath(v.MapIndex(k), rest)
			}
			return out
		}
		return nil
	}

	v = deref(v)
	if v.Kind() != reflect.Struct {
		return nil
	}
	f := fieldByTOML(v, part)
	if !f.IsValid() {
		return nil
	}
	return walkPath(f, rest)
}

// normalizeValue converts a reflect.Value into a deep-equal-stable form:
// strings, ints, bools, []any, and map[string]any. Structs become
// map[string]any using their TOML tag names.
func normalizeValue(v reflect.Value) any {
	v = deref(v)
	if !v.IsValid() {
		return nil
	}

	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(v.Uint())
	case reflect.Bool:
		return v.Bool()
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = normalizeValue(v.Index(i))
		}
		return out
	case reflect.Map:
		out := make(map[string]any, v.Len())
		for _, k := range v.MapKeys() {
			out[fmt.Sprint(k.Interface())] = normalizeValue(v.MapIndex(k))
		}
		return out
	case reflect.Struct:
		out := make(map[string]any)
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("toml")
			if tag == "" || tag == "-" {
				continue
			}
			name := strings.Split(tag, ",")[0]
			out[name] = normalizeValue(v.Field(i))
		}
		return out
	default:
		return fmt.Sprint(v.Interface())
	}
}

// deref follows pointers, returning the zero value if a nil pointer is hit.
func deref(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

// fieldByTOML returns the struct field whose toml tag matches name.
func fieldByTOML(v reflect.Value, name string) reflect.Value {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		if strings.Split(tag, ",")[0] == name {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

// collectionKey returns the identity key for a slice element. It is used when
// a wildcard segment expands a collection so that the resulting map is keyed
// the same way the operational code addresses the element.
func collectionKey(elem reflect.Value) string {
	elem = deref(elem)
	if !elem.IsValid() {
		return ""
	}

	switch elem.Type() {
	case reflect.TypeOf(config.ServerConfig{}):
		return serverKey(elem.Addr().Interface().(*config.ServerConfig))
	case reflect.TypeOf(config.LocationConfig{}):
		return locationKey(elem.Addr().Interface().(*config.LocationConfig))
	case reflect.TypeOf(config.UpstreamConfig{}):
		return elem.FieldByName("Name").String()
	case reflect.TypeOf(config.StreamServer{}):
		s := elem.Addr().Interface().(*config.StreamServer)
		return normalizeStreamProtocol(s.Protocol) + "/" + strings.TrimSpace(s.Listen)
	default:
		return fmt.Sprint(elem.Interface())
	}
}

// serverKey mirrors the operational identity of a server block: the first
// server_name plus listen address, or just the listen address for the
// catch-all block.
func serverKey(s *config.ServerConfig) string {
	key := s.Listen
	if len(s.ServerNames) > 0 {
		key = s.ServerNames[0] + ":" + s.Listen
	}
	return key
}

// locationKey mirrors the operational identity of a location: match type and
// path.
func locationKey(l *config.LocationConfig) string {
	t := l.Match.Type
	if t == "" {
		t = "prefix"
	}
	return t + " " + l.Match.Path
}

// sortedCopy returns a sorted copy of a string slice.
func sortedCopy(xs []string) []string {
	cp := append([]string(nil), xs...)
	sort.Strings(cp)
	return cp
}

// specialExtractor returns manual extractors for paths where the generic
// schema walk would not produce the canonical comparison shape (order-
// independent lists, weighted backend sets, etc.).
func specialExtractor(path string) (func(*config.Config) any, bool) {
	switch path {
	case "servers.*.server_names":
		return func(cfg *config.Config) any {
			out := make(map[string]any, len(cfg.Servers))
			for i := range cfg.Servers {
				s := &cfg.Servers[i]
				out[serverKey(s)] = sortedCopy(s.ServerNames)
			}
			return out
		}, true
	case "servers.*.plugins":
		return func(cfg *config.Config) any {
			out := make(map[string]any, len(cfg.Servers))
			for i := range cfg.Servers {
				s := &cfg.Servers[i]
				out[serverKey(s)] = sortedCopy(s.Plugins)
			}
			return out
		}, true
	case "servers.*.locations.*.plugins":
		return func(cfg *config.Config) any {
			out := make(map[string]map[string]any, len(cfg.Servers))
			for i := range cfg.Servers {
				s := &cfg.Servers[i]
				locMap := make(map[string]any, len(s.Locations))
				for j := range s.Locations {
					l := &s.Locations[j]
					locMap[locationKey(l)] = sortedCopy(l.Plugins)
				}
				out[serverKey(s)] = locMap
			}
			return out
		}, true
	case "upstreams.*.servers":
		return func(cfg *config.Config) any {
			out := make(map[string]any, len(cfg.Upstreams))
			for i := range cfg.Upstreams {
				u := &cfg.Upstreams[i]
				servers := make([]map[string]any, len(u.Servers))
				for j, srv := range u.Servers {
					servers[j] = map[string]any{
						"address": srv.Address,
						"weight":  srv.Weight,
					}
				}
				sort.Slice(servers, func(a, b int) bool {
					return fmt.Sprint(servers[a]["address"]) < fmt.Sprint(servers[b]["address"])
				})
				out[u.Name] = servers
			}
			return out
		}, true
	case "compression.types":
		return func(cfg *config.Config) any {
			return sortedCopy(cfg.Compression.Types)
		}, true
	}
	return nil, false
}
