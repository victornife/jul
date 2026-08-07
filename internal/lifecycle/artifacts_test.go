// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderIsDeterministic proves repeated generation produces byte-identical
// output, which is what makes check mode a reliable drift gate.
func TestRenderIsDeterministic(t *testing.T) {
	first, err := RenderArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		next, err := RenderArtifacts()
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

// TestGeneratedArtifactsCarryNoEnvironmentState proves the output cannot depend
// on where or when it was generated.
func TestGeneratedArtifactsCarryNoEnvironmentState(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := RenderArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range ArtifactPaths {
		body := string(artifacts[rel])
		if strings.Contains(body, wd) {
			t.Errorf("%s embeds the absolute checkout path", rel)
		}
		if strings.Contains(body, "20") && strings.Contains(body, "T00:00:00Z") {
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

// TestGeneratedArtifactsCarryNoSecrets proves the mirrors expose only canonical
// paths, closed class names, bounded subsystems and fixed reasons.
func TestGeneratedArtifactsCarryNoSecrets(t *testing.T) {
	artifacts, err := RenderArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range ArtifactPaths {
		body := string(artifacts[rel])
		for _, forbidden := range []string{"BEGIN CERTIFICATE", "BEGIN PRIVATE KEY", "sha256:", "Bearer "} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s contains %q", rel, forbidden)
			}
		}
	}
}

// TestJSONArtifactMatchesRegistry proves the machine mirror is complete and
// agrees with the Go authority path for path.
func TestJSONArtifactMatchesRegistry(t *testing.T) {
	raw, err := RenderJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc Metadata
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("generated JSON does not parse: %v", err)
	}
	if len(doc.Fields) != len(Registry) {
		t.Fatalf("JSON has %d fields, registry has %d", len(doc.Fields), len(Registry))
	}
	for i, f := range doc.Fields {
		e := Registry[i]
		if f.Path != e.Path || f.Class != e.Class.String() || f.Subsystem != string(e.Subsystem) {
			t.Fatalf("JSON field %d = %+v, registry = %+v", i, f, e)
		}
	}
	if doc.Counts.RegistryEntries != len(Registry) {
		t.Errorf("counts.registry_entries = %d, want %d", doc.Counts.RegistryEntries, len(Registry))
	}
	if doc.Counts.SchemaLeaves != doc.Counts.RegistryEntries+len(SchemaExemptions) {
		t.Errorf("closed world broken: %d leaves, %d entries, %d exemptions",
			doc.Counts.SchemaLeaves, doc.Counts.RegistryEntries, len(SchemaExemptions))
	}
}

// TestYAMLAndMarkdownMirrorEveryPath proves the three artifacts stay in parity.
func TestYAMLAndMarkdownMirrorEveryPath(t *testing.T) {
	artifacts, err := RenderArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	yamlBody := string(artifacts[ArtifactPaths[0]])
	mdBody := string(artifacts[ArtifactPaths[1]])
	for _, e := range Registry {
		if !strings.Contains(yamlBody, `path: "`+e.Path+`"`) {
			t.Errorf("YAML is missing %s", e.Path)
		}
		if !strings.Contains(mdBody, "| `"+e.Path+"` |") {
			t.Errorf("Markdown is missing %s", e.Path)
		}
	}
}

// TestCheckModeDetectsStaleAndWritesNothing exercises the generator command's
// two modes against a temporary tree.
func TestCheckModeDetectsStaleAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	artifacts, err := RenderArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range ArtifactPaths {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, artifacts[rel], 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stale := filepath.Join(dir, filepath.FromSlash(ArtifactPaths[1]))
	before, err := os.ReadFile(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, append(before, []byte("\nhand edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	// Check mode must report the stale file, name the remedy, and change nothing.
	err = checkArtifacts(dir)
	if err == nil {
		t.Fatal("check mode did not detect the hand-edited artifact")
	}
	if !strings.Contains(err.Error(), ArtifactPaths[1]) {
		t.Errorf("failure does not name the stale file: %v", err)
	}
	if !strings.Contains(err.Error(), RegenerateCommand) {
		t.Errorf("failure does not name the regeneration command: %v", err)
	}
	after, err := os.ReadFile(stale)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before)+"\nhand edit\n" {
		t.Fatal("check mode modified the working tree")
	}
}

// checkArtifacts mirrors the generator's check mode so the behavior is tested
// without shelling out.
func checkArtifacts(dir string) error {
	artifacts, err := RenderArtifacts()
	if err != nil {
		return err
	}
	var stale []string
	for _, rel := range ArtifactPaths {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || string(got) != string(artifacts[rel]) {
			stale = append(stale, rel)
		}
	}
	if len(stale) > 0 {
		return &staleError{files: stale}
	}
	return nil
}

type staleError struct{ files []string }

func (e *staleError) Error() string {
	return "generated lifecycle artifacts are stale: " + strings.Join(e.files, ", ") + "; run: " + RegenerateCommand
}
