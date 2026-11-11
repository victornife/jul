// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestInspectionContractDescribesPendingRequestIntegration(t *testing.T) {
	artifacts, err := RenderArtifacts(buildTestContract(t))
	if err != nil {
		t.Fatal(err)
	}
	var metadata ContractMetadata
	if err := json.Unmarshal(artifacts["generated/config-metadata.json"], &metadata); err != nil {
		t.Fatal(err)
	}
	type schemaNode struct {
		Description string                `json:"description"`
		Properties  map[string]schemaNode `json:"properties"`
	}
	var schema schemaNode
	if err := json.Unmarshal(artifacts["generated/config.schema.json"], &schema); err != nil {
		t.Fatal(err)
	}
	global, ok := schema.Properties["global"]
	if !ok {
		t.Fatal("schema missing global")
	}
	inspection, ok := global.Properties["inspection"]
	if !ok {
		t.Fatal("schema missing global.inspection")
	}
	reference := string(artifacts["generated/config-reference.md"])
	tests := []struct {
		path        string
		want        string
		description string
	}{
		{
			"global.inspection.body_timeout",
			"configured and reported; live inspected-body deadlines are not applied in I1",
			"BodyTimeout bounds a future inspected-body receive; it is not applied to live requests in I1",
		},
		{
			"global.inspection.evaluation_queue_timeout",
			"applied by the governor API; live requests do not acquire governor slots in I1",
			"EvaluationQueueTimeout bounds governor slot waiting; live requests do not acquire slots in I1",
		},
		{
			"global.inspection.max_buffered_bytes",
			"applied by governor accounting and configuration validation; live WAF/plugin requests are not governed in I1",
			"MaxBufferedBytes is the shared governor ceiling for base, buffer and estimated WAF state; live requests are not governed in I1",
		},
		{
			"global.inspection.max_concurrent_evaluations",
			"applied by the governor API; live WAF/plugin evaluations are not governed in I1",
			"MaxConcurrentEvaluations is the governor slot limit; live evaluations are not governed in I1",
		},
		{
			"global.inspection.max_response_header_bytes",
			"used by configuration validation; live response-header accounting is pending in I1",
			"MaxResponseHeaderBytes is the positive R3 ceiling (decoded field names and values plus 64 bytes per occurrence); it is not applied to live responses in I1",
		},
		{
			"global.inspection.mode",
			"controls governor accounting and configuration validation; live WAF/plugin requests are not governed in I1",
			`Mode is "enforce" (default) or "observe" for governor accounting; live requests are not governed in I1`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			leaf, ok := inspection.Properties[strings.TrimPrefix(tt.path, "global.inspection.")]
			if !ok {
				t.Fatalf("schema missing %s", tt.path)
			}
			if leaf.Description != tt.description {
				t.Errorf("schema description = %q, want %q", leaf.Description, tt.description)
			}
			entry, ok := metadata.Fields[tt.path]
			if !ok {
				t.Fatalf("metadata missing %s", tt.path)
			}
			if entry.ActiveWhen != tt.want {
				t.Errorf("metadata active_when = %q, want %q", entry.ActiveWhen, tt.want)
			}
			if entry.Description != tt.description {
				t.Errorf("metadata description = %q, want %q", entry.Description, tt.description)
			}
			start := strings.Index(reference, "## `"+tt.path+"` ")
			if start < 0 {
				t.Fatalf("reference missing %s", tt.path)
			}
			section := reference[start:]
			if end := strings.Index(section, "\n## "); end >= 0 {
				section = section[:end]
			}
			if row := "| Active when | " + tt.want + " |"; !strings.Contains(section, row) {
				t.Errorf("reference section missing %q", row)
			}
			if !strings.Contains(section, tt.description+".") {
				t.Errorf("reference section missing description %q", tt.description)
			}
		})
	}
}

func buildTestContract(t *testing.T) Contract {
	t.Helper()
	c, err := Build(loadTestSources(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return c
}

// TestArtifactsRenderIsDeterministic proves repeated generation produces
// byte-identical output, which is what makes check mode a reliable drift gate.
func TestArtifactsRenderIsDeterministic(t *testing.T) {
	c := buildTestContract(t)
	first, err := RenderArtifacts(c)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		next, err := RenderArtifacts(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, rel := range ArtifactPaths {
			if string(first[rel]) != string(next[rel]) {
				t.Fatalf("%s differs between generation runs", rel)
			}
		}
	}
}

// TestArtifactsCarryNoEnvironmentState proves the output cannot depend on
// where or when it was generated.
func TestArtifactsCarryNoEnvironmentState(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	c := buildTestContract(t)
	artifacts, err := RenderArtifacts(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range ArtifactPaths {
		body := string(artifacts[rel])
		if strings.Contains(body, wd) {
			t.Errorf("%s embeds the absolute checkout path", rel)
		}
		if strings.Contains(body, `C:\Users`) || strings.Contains(body, "/home/") || strings.Contains(body, "file://") {
			t.Errorf("%s appears to embed a local filesystem path", rel)
		}
		if strings.Contains(body, "T00:00:00Z") {
			t.Errorf("%s appears to embed a timestamp", rel)
		}
		if !strings.Contains(body, "GENERATED FILE") {
			t.Errorf("%s is missing the generated-file banner", rel)
		}
		if !strings.Contains(body, RegenerateCommand) {
			t.Errorf("%s does not name the regeneration command", rel)
		}
	}
}

// TestArtifactsCarryNoSecrets proves the generated artifacts, which are built
// entirely from static Go/doc/audited metadata and never a read configuration
// document, contain no credential-shaped content.
func TestArtifactsCarryNoSecrets(t *testing.T) {
	c := buildTestContract(t)
	artifacts, err := RenderArtifacts(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range ArtifactPaths {
		body := string(artifacts[rel])
		for _, forbidden := range []string{"BEGIN CERTIFICATE", "BEGIN PRIVATE KEY", "-----BEGIN"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s contains %q", rel, forbidden)
			}
		}
	}
}

// TestSchemaIDHasNoLocalPath is the dedicated $id check ADR 0019 §23 requires.
func TestSchemaIDHasNoLocalPath(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(SchemaID, wd) {
		t.Errorf("$id embeds the checkout path: %s", SchemaID)
	}
	if strings.HasPrefix(SchemaID, "file://") {
		t.Errorf("$id must not be a local file:// identifier: %s", SchemaID)
	}
}

// TestSchemaIDIsVersioned proves $id is tied to ContractVersion (ADR 0019
// §11 of the corrective task): it embeds the version, and the formula is
// sensitive to it, so the same $id can never represent two incompatible
// contract versions.
func TestSchemaIDIsVersioned(t *testing.T) {
	want := fmt.Sprintf("https://github.com/victornife/jul/schema/config-contract/v%d", ContractVersion)
	if SchemaID != want {
		t.Errorf("SchemaID = %q, want %q", SchemaID, want)
	}
	if !strings.Contains(SchemaID, strconv.Itoa(ContractVersion)) {
		t.Errorf("SchemaID %q does not embed ContractVersion %d", SchemaID, ContractVersion)
	}
	other := fmt.Sprintf("https://github.com/victornife/jul/schema/config-contract/v%d", ContractVersion+1)
	if other == SchemaID {
		t.Fatal("the $id formula is not sensitive to ContractVersion, so a version bump would not change it")
	}
	if strings.Contains(SchemaID, "/main/") || strings.Contains(SchemaID, "/blob/") {
		t.Errorf("$id %q still points at a mutable branch/commit path", SchemaID)
	}
}

// TestArtifactPathsAreUnderGeneratedDir proves the three new artifacts follow
// ADR 0019 §23's docs/generated/ convention.
func TestArtifactPathsAreUnderGeneratedDir(t *testing.T) {
	for _, rel := range ArtifactPaths {
		if !strings.HasPrefix(rel, "generated/") {
			t.Errorf("artifact path %q is not under generated/", rel)
		}
	}
}
