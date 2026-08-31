// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package apicontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ArtifactPath is the committed OpenAPI document, relative to the output
// directory. There is exactly one: config.schema.json and openapi.json describe
// different contracts — the configuration document and the HTTP API — and
// neither is generated from the other (ADR 0019 §29).
const ArtifactPath = "generated/openapi.json"

// RegenerateCommand is the exact command a stale-artifact failure prints, so
// the remedy never has to be looked up.
const RegenerateCommand = "make api-contract-generate"

// Render produces the committed bytes. It is deterministic: encoding/json sorts
// object keys, every slice the builder produces is in catalog or lexical order,
// and nothing here reads a clock, an environment variable, a hostname or an
// absolute path. Two clean checkouts produce identical bytes.
func Render(doc *Document) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// The document contains no HTML and no user-controlled text, and escaping
	// would render the section signs in the descriptions unreadable.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("render openapi: %w", err)
	}
	return buf.Bytes(), nil
}

// Generate builds and renders in one step.
func Generate() ([]byte, error) {
	doc, err := Build()
	if err != nil {
		return nil, err
	}
	if err := checkNoLeakedHostOrPath(doc); err != nil {
		return nil, err
	}
	return Render(doc)
}

// checkNoLeakedHostOrPath refuses to publish a document containing a local
// path, a real host or anything resembling a credential (ADR 0019 §29). It runs
// on the rendered bytes rather than on the model, so a leak introduced through
// any field is caught rather than only the fields someone remembered to check.
//
// The absence of a `servers` block is not checked here: Document has no such
// field, so it is a property of the type rather than of the output, and
// TestDocumentCannotDeclareAServersBlock asserts it where it is provable.
func checkNoLeakedHostOrPath(doc *Document) error {
	b, err := Render(doc)
	if err != nil {
		return err
	}
	text := string(b)

	forbidden := []struct{ needle, why string }{
		{"http://127.0.0.1", "a loopback URL"},
		{"https://127.0.0.1", "a loopback URL"},
		{"http://localhost", "a loopback URL"},
		{"/Users/", "an absolute filesystem path"},
		{"/home/", "an absolute filesystem path"},
		{"C:\\", "an absolute filesystem path"},
		{"Bearer eyJ", "something resembling a token"},
		{"example-token", "something resembling a credential example"},
	}
	for _, f := range forbidden {
		if strings.Contains(text, f.needle) {
			return fmt.Errorf("the document contains %q, which is %s; no example may carry a local path, a real host or anything resembling a credential", f.needle, f.why)
		}
	}
	return nil
}
