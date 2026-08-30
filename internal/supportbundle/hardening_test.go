// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package supportbundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"jul/internal/diagnostics"
)

func TestCollectorErrorsRedactPathsURLsAndSecrets(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	secret := "collector-fixture-secret"
	collector := CollectorFunc{ID: "failure", Fn: func(context.Context, Snapshot) ([]Artifact, error) {
		return nil, fmt.Errorf("open %s/private.log: GET https://user:pass@example.test/path?token=%s password=%s", directory, secret, secret)
	}}
	bundle, err := NewGenerator([]Collector{collector}, DefaultLimits(), 1).Build(context.Background(), Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	got := bundle.Manifest.Collectors[0].Error
	if bytes.Contains([]byte(got), []byte(directory)) || bytes.Contains([]byte(got), []byte(secret)) || bytes.Contains([]byte(got), []byte("user:pass")) {
		t.Fatalf("collector error leaked sensitive data: %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("[PATH REDACTED]")) || !bytes.Contains([]byte(got), []byte("[URL REDACTED]")) {
		t.Fatalf("collector error did not record redaction: %q", got)
	}
}

func TestFinalExtractedArchiveSecretScan(t *testing.T) {
	t.Parallel()
	secret := "archive-fixture-secret"
	collector := CollectorFunc{ID: "fixtures", Fn: func(context.Context, Snapshot) ([]Artifact, error) {
		return []Artifact{
			{Path: "fixtures/report.json", ContentType: "application/json", Data: []byte(`{"token":"` + secret + `","safe":"` + secret + `"}`)},
			{Path: "fixtures/report.txt", ContentType: "text/plain", Data: []byte("Authorization: Bearer " + secret + "\nCookie: session=" + secret)},
		}, nil
	}}
	bundle, err := NewGenerator([]Collector{collector}, DefaultLimits(), 1).Build(context.Background(), Snapshot{RedactValues: []string{secret}})
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := WriteArchive(context.Background(), &archive, bundle); err != nil {
		t.Fatal(err)
	}
	for name, data := range extractArchive(t, archive.Bytes()) {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("fixture secret survived extracted entry %s: %s", name, data)
		}
	}
}

func TestDoctorCollectorCarriesBuildIdentity(t *testing.T) {
	t.Parallel()
	artifacts, err := collectDoctor(context.Background(), Snapshot{Product: "Jul.IA", Version: "test", Commit: "abcdef", BuildProfile: "full"})
	if err != nil {
		t.Fatal(err)
	}
	var report diagnostics.Report
	if err := json.Unmarshal(artifacts[0].Data, &report); err != nil {
		t.Fatal(err)
	}
	for _, result := range report.Checks {
		if result.Code != "SYSTEM_RUNTIME" {
			continue
		}
		if result.Evidence["commit"] != "abcdef" || result.Evidence["build_profile"] != "full" {
			t.Fatalf("doctor build identity = %#v", result.Evidence)
		}
		return
	}
	t.Fatal(errors.New("SYSTEM_RUNTIME result missing"))
}
