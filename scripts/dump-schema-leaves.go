// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build ignore

// dump-schema-leaves prints every TOML-tagged config path as JSON for
// external validators (scripts/docs-check.py). It is not part of the server
// binary.
//
// Path generation rules:
//   - Struct fields with a non-empty toml tag become a path segment.
//   - Slices of structs collapse to "*" (e.g. []ServerConfig -> servers.*).
//   - Pointer-to-struct and struct fields are recursed into.
//   - Maps are emitted as both the map key (e.g. servers.*.error_pages)
//     and, for known value types, their leaves.
//   - Fields tagged "-" or with no toml tag are ignored.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

	"jul/internal/config"
)

type leafJSON struct {
	Path      string `json:"path"`
	Type      string `json:"type"`
	Container bool   `json:"container"`
}

func main() {
	var out []leafJSON
	walk(reflect.TypeOf(config.Config{}), "", &out)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func walk(t reflect.Type, prefix string, out *[]leafJSON) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		path := joinPath(prefix, name)
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		switch ft.Kind() {
		case reflect.Slice:
			et := ft.Elem()
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				*out = append(*out, leafJSON{Path: path, Type: "[]" + et.Name(), Container: true})
				walk(et, path+".*", out)
			} else {
				*out = append(*out, leafJSON{Path: path, Type: ft.String(), Container: false})
			}
		case reflect.Struct:
			*out = append(*out, leafJSON{Path: path, Type: ft.String(), Container: true})
			walk(ft, path, out)
		case reflect.Map:
			// Emit the map itself as a container leaf; also walk scalar values.
			*out = append(*out, leafJSON{Path: path, Type: ft.String(), Container: true})
			if ft.Elem().Kind() == reflect.Struct {
				walk(ft.Elem(), path+".*", out)
			} else {
				*out = append(*out, leafJSON{Path: path + ".*", Type: ft.Elem().String(), Container: false})
			}
		default:
			*out = append(*out, leafJSON{Path: path, Type: ft.String(), Container: false})
		}
	}
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
