// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assessFileWithOptions(t *testing.T, source string, options AssessmentOptions) *Assessment {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	tree, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	_, report := Translate(tree, path)
	assessment := BuildAssessmentWithOptions(tree, path, report, options)
	if assessment == nil {
		t.Fatal("assessment is nil")
	}
	return assessment
}

func TestAssessmentSchemaV2IncludesRootSourceProvenance(t *testing.T) {
	assessment := assessFileWithOptions(t, `
http {
  server {
    listen 8080;
    server_name example.test;
    location /api {
      proxy_pass http://backend;
    }
  }
}
`, AssessmentOptions{})
	if assessment.SchemaVersion != 2 {
		t.Fatalf("schema version = %d, want 2", assessment.SchemaVersion)
	}
	if assessment.SourcePolicy.PathStyle != AssessmentPathRelative || assessment.SourcePolicy.Root != "." || assessment.SourcePolicy.FollowInclude {
		t.Fatalf("unexpected source policy: %+v", assessment.SourcePolicy)
	}
	if len(assessment.Sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(assessment.Sources))
	}
	source := assessment.Sources[0]
	if source.ID != "source-0001" || source.DisplayPath != "nginx.conf" || !strings.HasPrefix(source.Digest, "sha256:") {
		t.Fatalf("unexpected source: %+v", source)
	}
	for _, result := range assessment.Results {
		if result.Synthetic {
			continue
		}
		if !strings.HasPrefix(result.ID, "result-source-0001-") {
			t.Errorf("unstable parsed result ID: %q", result.ID)
		}
		if result.Provenance == nil {
			t.Errorf("result lacks provenance: %+v", result)
			continue
		}
		if result.Provenance.SourceID != source.ID || result.Provenance.DisplayPath != source.DisplayPath {
			t.Errorf("wrong source reference: %+v", result.Provenance)
		}
		if result.Provenance.Start.Line < 1 || result.Provenance.ContextPath == "" || result.Provenance.Summary == "" {
			t.Errorf("incomplete provenance: %+v", result.Provenance)
		}
	}

	proxy := assessmentResultByCode(assessment, "NGX_LOCATION_PROXY_PASS")
	if proxy == nil || proxy.Provenance == nil {
		t.Fatalf("proxy_pass result missing provenance: %+v", assessment.Results)
	}
	if !strings.Contains(proxy.Provenance.ContextPath, "http > server[") || !strings.Contains(proxy.Provenance.ContextPath, "location[/api]") {
		t.Fatalf("unexpected context path: %q", proxy.Provenance.ContextPath)
	}
	if proxy.Provenance.Start.Column < 1 || proxy.Provenance.End.Line < proxy.Provenance.Start.Line {
		t.Fatalf("unexpected proxy span: %+v", proxy.Provenance)
	}
}

func TestAssessmentSchemaV2IsDeterministicAcrossDirectories(t *testing.T) {
	source := `http { server { listen 8080; server_name example.test; location / { return 204; } } }`
	first := assessFileWithOptions(t, source, AssessmentOptions{})
	second := assessFileWithOptions(t, source, AssessmentOptions{})
	firstJSON, err := first.JSON()
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := second.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("relative schema v2 report depends on temp directory:\n%s\n---\n%s", firstJSON, secondJSON)
	}
}

func TestAssessmentSchemaV2TargetMappingsAndGuidance(t *testing.T) {
	assessment := assessFileWithOptions(t, `
include conf.d/*.conf;
http {
  server {
    listen 8443 ssl;
    location /api {
      alias /srv/api;
      if ($request_method = POST) { return 403; }
    }
  }
}
`, AssessmentOptions{})
	listen := assessmentResultByCode(assessment, "NGX_SERVER_LISTEN")
	if listen == nil || len(listen.TargetMappings) != 1 || len(listen.TargetMappings[0].Paths) != 2 {
		t.Fatalf("listen mapping missing: %+v", listen)
	}
	include := assessmentResultByCode(assessment, "NGX_MAIN_INCLUDE")
	if include == nil || !containsString(include.GuidanceCodes, "GUIDE_INCLUDE_ENABLE") {
		t.Fatalf("include guidance missing: %+v", include)
	}
	for _, result := range assessment.Results {
		if result.Class != AssessmentBlocking && result.Class != AssessmentApproximated {
			continue
		}
		if len(result.GuidanceCodes) == 0 {
			t.Errorf("actionable result has no guidance: %+v", result)
		}
		for _, code := range result.GuidanceCodes {
			if _, ok := lookupAssessmentGuidance(code); !ok {
				t.Errorf("result references unknown guidance %q", code)
			}
		}
	}
	if len(assessment.Guidance) == 0 {
		t.Fatal("deduplicated guidance catalogue is empty")
	}
}

func TestAssessmentSchemaV2AbsolutePathsAreExplicit(t *testing.T) {
	assessment := assessFileWithOptions(t, `http { server { listen 8080; location / { return 204; } } }`, AssessmentOptions{PathStyle: AssessmentPathAbsolute})
	if assessment.SourcePolicy.PathStyle != AssessmentPathAbsolute || !filepath.IsAbs(filepath.FromSlash(assessment.SourcePolicy.Root)) {
		t.Fatalf("unexpected absolute policy: %+v", assessment.SourcePolicy)
	}
	if !filepath.IsAbs(filepath.FromSlash(assessment.Source)) {
		t.Fatalf("source is not absolute: %q", assessment.Source)
	}
	for _, result := range assessment.Results {
		if !result.Synthetic && (result.Provenance == nil || !filepath.IsAbs(filepath.FromSlash(result.Provenance.DisplayPath))) {
			t.Fatalf("result path is not absolute: %+v", result)
		}
	}
}

func TestAssessmentSchemaV2SourceOrderHumanOutput(t *testing.T) {
	assessment := assessFileWithOptions(t, `
http {
  server {
    listen 8080;
    location / {
      alias /srv;
      if ($x) { return 403; }
    }
  }
}
`, AssessmentOptions{})
	assessment.SetSourceOrder(true)
	human := assessment.Human()
	if !strings.Contains(human, "SOURCE ORDER:") || !strings.Contains(human, "nginx.conf:") {
		t.Fatalf("source-order navigation missing:\n%s", human)
	}
	listen := strings.Index(human, " listen (")
	conditional := strings.Index(human, " if (")
	if listen < 0 || conditional < 0 || listen > conditional {
		t.Fatalf("human output is not in source order:\n%s", human)
	}
}

func TestAssessmentSchemaV2RedactsSourceSummary(t *testing.T) {
	const secret = "VERY-SECRET-PROVENANCE-TOKEN"
	assessment := assessFileWithOptions(t, `
http {
  server {
    listen 8080;
    location / {
      proxy_set_header Authorization "Bearer VERY-SECRET-PROVENANCE-TOKEN";
      proxy_pass https://alice:VERY-SECRET-PROVENANCE-TOKEN@backend.example;
    }
  }
}
`, AssessmentOptions{})
	jsonOutput, err := assessment.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(jsonOutput), secret) || strings.Contains(assessment.Human(), secret) {
		t.Fatalf("assessment leaked fixture secret:\n%s", jsonOutput)
	}
}

func TestAssessmentSchemaV2SyntheticIDsRemainStable(t *testing.T) {
	assessment := assessFileWithOptions(t, `http { server { listen 8080; location / { return 204; } } }`, AssessmentOptions{})
	before := make(map[string]string)
	for _, result := range assessment.Results {
		if !result.Synthetic {
			before[result.Code] = result.ID
		}
	}
	assessment.SetValidation([]error{errors.New("fixture validation failure")}, nil)
	validation := assessmentResultByCode(assessment, "JUL_CANDIDATE_VALIDATION")
	if validation == nil || !strings.HasPrefix(validation.ID, "result-synthetic-") || validation.Scope != "global" {
		t.Fatalf("validation result is not a stable global synthetic result: %+v", validation)
	}
	if !containsString(validation.GuidanceCodes, "GUIDE_CANDIDATE_VALIDATION") {
		t.Fatalf("validation guidance missing: %+v", validation)
	}
	for _, result := range assessment.Results {
		if result.Synthetic {
			continue
		}
		if want := before[result.Code]; want != "" && result.ID != want {
			t.Errorf("parsed result ID changed after validation: %q -> %q", want, result.ID)
		}
	}
}

func TestFailureAssessmentSchemaV2UsesSafeSourcePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.conf")
	assessment := FailureAssessmentWithOptions(path, AssessmentParseError, "NGX_PARSE_ERROR", "could not parse", AssessmentOptions{})
	if assessment.SchemaVersion != 2 || assessment.Source != "broken.conf" || assessment.SourcePolicy.Root != "." {
		t.Fatalf("unexpected failure assessment source metadata: %+v", assessment)
	}
	if len(assessment.Results) != 1 || assessment.Results[0].Provenance == nil || assessment.Results[0].Scope != "global" {
		t.Fatalf("failure result lacks explicit global provenance: %+v", assessment.Results)
	}
}

func assessmentResultByCode(assessment *Assessment, code string) *AssessmentResult {
	if assessment == nil {
		return nil
	}
	for i := range assessment.Results {
		if assessment.Results[i].Code == code {
			return &assessment.Results[i]
		}
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
