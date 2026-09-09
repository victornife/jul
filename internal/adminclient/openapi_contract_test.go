// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/adminapi"
)

type openAPIDoc struct {
	Paths map[string]map[string]struct {
		OperationID string `json:"operationId"`
		Parameters  []struct {
			Name     string `json:"name"`
			In       string `json:"in"`
			Required bool   `json:"required"`
		} `json:"parameters"`
		RequestBody *struct {
			Content map[string]json.RawMessage `json:"content"`
		} `json:"requestBody"`
	} `json:"paths"`
}

func TestClientOperationsExistInGeneratedOpenAPI(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "generated", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPIDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	seenIDs := make(map[string]bool)
	for _, op := range Operations() {
		if !strings.HasPrefix(op.Path, "/api/v1/") {
			t.Fatalf("client references non-v1/private path %q", op.Path)
		}
		pathItem, ok := doc.Paths[op.Path]
		if !ok {
			t.Fatalf("client path %q absent from generated OpenAPI", op.Path)
		}
		operation, ok := pathItem[strings.ToLower(op.Method)]
		if !ok {
			t.Fatalf("client method %s absent for %s", op.Method, op.Path)
		}
		if operation.OperationID != op.ID {
			t.Fatalf("%s %s operationId=%q, client=%q", op.Method, op.Path, operation.OperationID, op.ID)
		}
		if seenIDs[op.ID] {
			t.Fatalf("duplicate client operationId %q", op.ID)
		}
		seenIDs[op.ID] = true
		if op.ContentType != "" {
			if operation.RequestBody == nil {
				t.Fatalf("%s has no OpenAPI requestBody", op.ID)
			}
			if _, ok := operation.RequestBody.Content[op.ContentType]; !ok {
				t.Fatalf("%s content type %q absent from OpenAPI", op.ID, op.ContentType)
			}
		}
	}
}

func TestMutationRequiredParametersStillMatchContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "generated", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPIDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	apply := doc.Paths["/api/v1/config/apply"]["post"]
	params := make(map[string]struct {
		in       string
		required bool
	})
	for _, p := range apply.Parameters {
		params[p.Name] = struct {
			in       string
			required bool
		}{p.In, p.Required}
	}
	if p, ok := params["base_version"]; !ok || p.in != "query" || !p.required {
		t.Fatalf("apply base_version contract changed: %+v", params)
	}
	if p, ok := params["Idempotency-Key"]; !ok || p.in != "header" || p.required {
		t.Fatalf("apply idempotency contract changed: %+v", params)
	}
	if p, ok := params["mode"]; !ok || p.in != "query" {
		t.Fatalf("apply mode contract changed: %+v", params)
	}
}

func TestEveryStableAPIErrorHasCLIHandling(t *testing.T) {
	for _, code := range adminapi.Codes() {
		if _, ok := ErrorExit(code); !ok {
			t.Fatalf("stable external error %q has no CLI handling", code)
		}
	}
}
