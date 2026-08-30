// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package corpus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDecodeManifestStrictAndCompleteContract(t *testing.T) {
	valid := validManifest()
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeManifest(strings.NewReader(string(data))); err != nil || got.ID != valid.ID {
		t.Fatalf("valid decode = %#v, %v", got, err)
	}
	for name, raw := range map[string]string{
		"malformed":        `{`,
		"multiple values":  string(data) + ` {}`,
		"trailing broken":  string(data) + ` {`,
		"invalid contract": `{"schema_version":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeManifest(strings.NewReader(raw)); err == nil {
				t.Fatal("invalid manifest unexpectedly decoded")
			}
		})
	}
}

func TestManifestValidationAggregatesContractFailures(t *testing.T) {
	m := validManifest()
	m.SchemaVersion = 0
	m.ID = "Bad ID"
	m.Tier = "unknown"
	m.Description = ""
	m.Origin = Origin{Kind: "unknown"}
	m.Categories = []string{"z", "a", "a"}
	m.BuildTags = []string{"z", "a"}
	m.Root = "../escape.conf"
	m.Assessment = ExpectedAssessment{
		Status:    "unknown",
		Ready:     true,
		FilesRead: 0,
		Sources:   []string{"z.conf", "../escape.conf", "z.conf"},
		Results: []ExpectedResult{
			{Source: "../escape.conf", Count: -1},
			{Source: "../escape.conf", Count: 1},
		},
	}
	m.Candidate = CandidateGolden{Required: true}
	m.Scenarios = append(m.Scenarios, m.Scenarios[0])
	if err := m.Validate(); err == nil {
		t.Fatal("invalid aggregate unexpectedly validated")
	}

	m = validManifest()
	m.Candidate.Required = false
	m.Candidate.Contains = nil
	m.Assessment.Ready = false
	m.Assessment.Status = "manual_action_required"
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "runtime scenarios require") {
		t.Fatalf("scenario/candidate gate error = %v", err)
	}

	m = validManifest()
	m.Scenarios = nil
	m.Assessment.Ready = false
	m.Assessment.Status = "manual_action_required"
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "required generated candidate") {
		t.Fatalf("candidate/readiness gate error = %v", err)
	}
}

func TestOriginAssessmentAndCandidateBoundaries(t *testing.T) {
	if err := validateOrigin(Origin{Kind: OriginPublicDerived, License: "MIT", Source: "https://example.test/sample"}); err != nil {
		t.Fatalf("valid public origin: %v", err)
	}
	if err := validateOrigin(Origin{Kind: OriginPublicDerived, License: "", Source: "http://example.test"}); err == nil {
		t.Fatal("unsafe public origin unexpectedly accepted")
	}

	ready := validManifest().Assessment
	ready.Status = "manual_action_required"
	if err := validateAssessment(ready); err == nil {
		t.Fatal("ready/status mismatch unexpectedly accepted")
	}
	notReady := validManifest().Assessment
	notReady.Ready = false
	if err := validateAssessment(notReady); err == nil {
		t.Fatal("not-ready ready_for_review unexpectedly accepted")
	}
	duplicate := validManifest().Assessment
	duplicate.Results = append(duplicate.Results, duplicate.Results[0])
	if err := validateAssessment(duplicate); err == nil {
		t.Fatal("duplicate expected result unexpectedly accepted")
	}

	for _, candidate := range []CandidateGolden{
		{Required: false, Contains: []string{"x"}},
		{Required: true},
		{Required: true, Contains: []string{"z", "a", "a"}},
	} {
		if err := validateCandidate(candidate); err == nil {
			t.Fatalf("invalid candidate unexpectedly accepted: %+v", candidate)
		}
	}
}

func TestScenarioValidationSafeReplayMatrix(t *testing.T) {
	base := validManifest().Scenarios[0]
	validPost := base
	validPost.ID = "safe-post"
	validPost.Request.Method = http.MethodPost
	validPost.Request.Body = "fixture-only"
	validPost.SideEffectFree = true
	if err := validPost.Validate(); err != nil {
		t.Fatalf("safe POST rejected: %v", err)
	}

	mutations := map[string]func(*Scenario){
		"bad id":             func(s *Scenario) { s.ID = "Bad ID" },
		"unsafe":             func(s *Scenario) { s.Safe = false },
		"post side effect":   func(s *Scenario) { s.Request.Method = http.MethodPost },
		"lowercase method":   func(s *Scenario) { s.Request.Method = "get" },
		"unsupported method": func(s *Scenario) { s.Request.Method = http.MethodDelete },
		"invalid path":       func(s *Scenario) { s.Request.Path = "https://example.test/" },
		"host scheme":        func(s *Scenario) { s.Request.Host = "https://fixture.test" },
		"host control":       func(s *Scenario) { s.Request.Host = "fixture\n.test" },
		"sensitive header":   func(s *Scenario) { s.Request.Headers = map[string][]string{"authorization": {"secret"}} },
		"body on GET":        func(s *Scenario) { s.Request.Body = "x" },
		"body too large": func(s *Scenario) {
			s.Request.Method = http.MethodPost
			s.SideEffectFree = true
			s.Request.Body = strings.Repeat("x", maxRequestBodyBytes+1)
		},
		"no assertions":       func(s *Scenario) { s.Assert = nil },
		"unknown dimension":   func(s *Scenario) { s.Assert = []Dimension{"unknown"} },
		"duplicate dimension": func(s *Scenario) { s.Assert = []Dimension{DimensionStatus, DimensionStatus} },
		"body and hash": func(s *Scenario) {
			s.Assert = []Dimension{DimensionBody, DimensionBodySHA256}
			s.Reference.BodySHA256 = "sha256:" + strings.Repeat("0", 64)
		},
		"headers without names": func(s *Scenario) {
			s.Assert = []Dimension{DimensionHeaders}
			s.Reference.Headers = map[string][]string{}
		},
		"names without headers": func(s *Scenario) { s.AssertHeaders = []string{"x-test"} },
		"assert ignored overlap": func(s *Scenario) {
			s.Assert = []Dimension{DimensionHeaders}
			s.AssertHeaders = []string{"x-test"}
			s.IgnoreHeaders = []string{"x-test"}
			s.Reference.Headers = map[string][]string{"x-test": {"ok"}}
		},
		"invalid status": func(s *Scenario) { s.Reference.Status = 99 },
		"missing asserted header": func(s *Scenario) {
			s.Assert = []Dimension{DimensionHeaders}
			s.AssertHeaders = []string{"x-test"}
			s.Reference.Headers = map[string][]string{}
		},
		"missing redirect": func(s *Scenario) { s.Assert = []Dimension{DimensionRedirectTarget}; s.Reference.Headers = nil },
		"bad digest":       func(s *Scenario) { s.Assert = []Dimension{DimensionBodySHA256}; s.Reference.BodySHA256 = "bad" },
		"digest with body": func(s *Scenario) {
			s.Assert = []Dimension{DimensionBodySHA256}
			s.Reference.BodySHA256 = "sha256:" + strings.Repeat("0", 64)
			s.Reference.Body = "x"
		},
		"body with digest": func(s *Scenario) {
			s.Assert = []Dimension{DimensionBody}
			s.Reference.BodySHA256 = "sha256:" + strings.Repeat("0", 64)
		},
		"equivalent differs":         func(s *Scenario) { s.Jul = &ObservationSpec{Status: 404} },
		"equivalent code":            func(s *Scenario) { s.ExpectedDifferenceCode = "NOT_ALLOWED" },
		"expected diff without diff": func(s *Scenario) { s.ExpectedVerdict = VerdictExpectedDifference; s.ExpectedDifferenceCode = "DIFF" },
		"expected diff without code": func(s *Scenario) {
			s.ExpectedVerdict = VerdictExpectedDifference
			s.Jul = &ObservationSpec{Status: 404}
		},
		"not executed without reason": func(s *Scenario) { s.ExpectedVerdict = VerdictNotExecuted },
		"blocking without reason":     func(s *Scenario) { s.ExpectedVerdict = VerdictBlockingSource },
		"unexpected as expectation":   func(s *Scenario) { s.ExpectedVerdict = VerdictUnexpected },
		"unknown verdict":             func(s *Scenario) { s.ExpectedVerdict = "unknown" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := base
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("invalid scenario unexpectedly validated")
			}
		})
	}

	for _, verdict := range []Verdict{VerdictNotExecuted, VerdictBlockingSource} {
		s := base
		s.ExpectedVerdict = verdict
		s.NotExecutedReason = "explicitly gated"
		if err := s.Validate(); err != nil {
			t.Fatalf("%s scenario rejected: %v", verdict, err)
		}
	}
}

func TestHeaderPathAndDigestHelpers(t *testing.T) {
	if err := validateHeaderMap("headers", map[string][]string{
		"Bad Header": nil,
		"x-token":    {"secret"},
		"x-empty":    {""},
	}, true); err == nil {
		t.Fatal("invalid header map unexpectedly accepted")
	}
	if err := validateHeaderNames("headers", []string{"z", "authorization", "z"}, true); err == nil {
		t.Fatal("invalid header names unexpectedly accepted")
	}
	if err := validateRequestTarget("//authority/path"); err == nil {
		t.Fatal("authority-form target unexpectedly accepted")
	}
	if err := validateRequestTarget("/bad\npath"); err == nil {
		t.Fatal("control-bearing target unexpectedly accepted")
	}
	if err := validateRelativePath("path", "a/../b"); err == nil {
		t.Fatal("unclean path unexpectedly accepted")
	}
	if err := validateBoundedText("text", strings.Repeat("x", 4), 3, false); err == nil {
		t.Fatal("oversized text unexpectedly accepted")
	}
	if err := validateBoundedText("text", "", 3, false); err == nil {
		t.Fatal("empty text unexpectedly accepted")
	}
	if !validSHA256("sha256:"+strings.Repeat("a", 64)) || validSHA256("sha256:"+strings.Repeat("A", 64)) || validSHA256("short") {
		t.Fatal("SHA-256 grammar mismatch")
	}
}

func TestRequestObservationAndComparisonBranches(t *testing.T) {
	for name, baseURL := range map[string]string{
		"malformed":    "://",
		"scheme":       "ftp://127.0.0.1",
		"bad hostport": "http://127.0.0.1:bad",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRequest(context.Background(), baseURL, RequestSpec{Method: http.MethodGet, Path: "/"}); err == nil {
				t.Fatal("invalid base URL unexpectedly accepted")
			}
		})
	}
	if _, err := NewRequest(context.Background(), "http://[::1]:8080", RequestSpec{Method: http.MethodGet, Path: "//bad"}); err == nil {
		t.Fatal("invalid target unexpectedly accepted")
	}
	req, err := NewRequest(context.Background(), "http://localhost:8080/base", RequestSpec{
		Method: http.MethodPost, Path: "/submit?q=1", Host: "fixture.test",
		Headers: map[string][]string{"x-test": {"one", "two"}}, Body: "body",
	})
	if err != nil || req.Host != "fixture.test" || req.Header.Values("X-Test")[1] != "two" {
		t.Fatalf("request = %#v, %v", req, err)
	}

	if _, err := ObserveResponse(nil, 1); err == nil {
		t.Fatal("nil response unexpectedly accepted")
	}
	badBody := &http.Response{Body: io.NopCloser(errorReader{}), Header: make(http.Header)}
	if _, err := ObserveResponse(badBody, 1); err == nil {
		t.Fatal("body read failure unexpectedly accepted")
	}
	response := &http.Response{StatusCode: 201, Header: http.Header{"X-Test": {"one"}}, Body: io.NopCloser(strings.NewReader("ok"))}
	observation, err := ObserveResponse(response, 0)
	if err != nil || observation.Status != 201 || string(observation.Body) != "ok" {
		t.Fatalf("default observation = %+v, %v", observation, err)
	}

	hash := digest([]byte("body"))
	scenario := Scenario{
		Assert:          []Dimension{DimensionStatus, DimensionHeaders, DimensionBodySHA256, DimensionRedirectTarget},
		AssertHeaders:   []string{"x-test"},
		Reference:       ObservationSpec{Status: 302, Headers: map[string][]string{"location": {"/next"}, "x-test": {"a", "b"}}, BodySHA256: hash},
		ExpectedVerdict: VerdictEquivalent,
	}
	actual := Observation{Status: 302, Headers: http.Header{"Location": {"/next"}, "X-Test": {" b ", "a"}}, Body: []byte("body")}
	if result := Evaluate(scenario, actual); result.Verdict != VerdictEquivalent {
		t.Fatalf("hash/redirect comparison = %+v", result)
	}
	if result := EvaluateReference(scenario, actual); result.Verdict != VerdictEquivalent {
		t.Fatalf("reference comparison = %+v", result)
	}
	actual.Status = 500
	actual.Headers.Set("Location", "/wrong")
	actual.Headers.Set("X-Test", "wrong")
	actual.Body = []byte("wrong")
	if result := Evaluate(scenario, actual); result.Verdict != VerdictUnexpected || len(result.Differences) != 4 {
		t.Fatalf("multi-difference result = %+v", result)
	}
	if result := EvaluateReference(scenario, actual); result.Verdict != VerdictUnexpected || len(result.Differences) != 4 {
		t.Fatalf("reference multi-difference result = %+v", result)
	}

	for _, verdict := range []Verdict{VerdictNotExecuted, VerdictBlockingSource} {
		scenario.ExpectedVerdict = verdict
		if got := Evaluate(scenario, Observation{}).Verdict; got != verdict {
			t.Fatalf("Evaluate(%s) = %s", verdict, got)
		}
	}
	scenario.ExpectedVerdict = VerdictEquivalent
	scenario.Jul = &ObservationSpec{Status: 404, Headers: scenario.Reference.Headers, BodySHA256: hash}
	if got := Evaluate(scenario, Observation{Status: 404, Headers: actual.Headers, Body: []byte("body")}).Verdict; got != VerdictUnexpected {
		t.Fatalf("undeclared difference verdict = %s", got)
	}

	if !loopbackHost("127.0.0.1") || !loopbackHost("[::1]") || loopbackHost("127.0.0.1:bad") || loopbackHost("example.test") {
		t.Fatal("loopback host classification mismatch")
	}
}

func TestFixtureLoadingDiscoveryAndLayoutFailures(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing manifest unexpectedly loaded")
	}
	invalidDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(invalidDir, manifestName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(invalidDir); err == nil {
		t.Fatal("invalid manifest unexpectedly loaded")
	}
	if _, err := Discover(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing corpus root unexpectedly discovered")
	}
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "note.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(empty); err == nil {
		t.Fatal("empty corpus unexpectedly discovered")
	}

	root := t.TempDir()
	writeValidFixture(t, root, "different-name")
	if _, err := Discover(root); err == nil || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("directory/id mismatch error = %v", err)
	}

	if runtime.GOOS != "windows" {
		symlinkRoot := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(symlinkRoot, "linked")); err == nil {
			if _, err := Discover(symlinkRoot); err == nil {
				t.Fatal("symlink corpus entry unexpectedly discovered")
			}
		}
	}
}

func TestFixtureLayoutRejectsUnsafeFiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, dir string, fixture *Fixture)
	}{
		{"missing nginx", func(t *testing.T, dir string, fixture *Fixture) { _ = os.RemoveAll(filepath.Join(dir, "nginx")) }},
		{"root directory", func(t *testing.T, dir string, fixture *Fixture) {
			_ = os.Remove(fixture.RootPath())
			_ = os.Mkdir(fixture.RootPath(), 0o700)
		}},
		{"empty readme", func(t *testing.T, dir string, fixture *Fixture) {
			_ = os.WriteFile(filepath.Join(dir, fixtureReadmeName), nil, 0o600)
		}},
		{"private key", func(t *testing.T, dir string, fixture *Fixture) {
			_ = os.WriteFile(filepath.Join(dir, "secret.pem"), []byte("-----BEGIN PRIVATE KEY-----"), 0o600)
		}},
		{"oversized", func(t *testing.T, dir string, fixture *Fixture) {
			path := filepath.Join(dir, "large.bin")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(maxFixtureFileSize + 1); err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dir := writeValidFixture(t, parent, "fixture-one")
			fixture := Fixture{Dir: dir, Manifest: validManifest()}
			tc.mutate(t, dir, &fixture)
			if err := fixture.ValidateLayout(); err == nil {
				t.Fatal("unsafe layout unexpectedly accepted")
			}
		})
	}

	if runtime.GOOS != "windows" {
		parent := t.TempDir()
		dir := writeValidFixture(t, parent, "fixture-one")
		if err := os.Symlink(filepath.Join(dir, "README.md"), filepath.Join(dir, "linked")); err == nil {
			fixture := Fixture{Dir: dir, Manifest: validManifest()}
			if err := fixture.ValidateLayout(); err == nil {
				t.Fatal("symlink fixture file unexpectedly accepted")
			}
		}
	}

	for _, marker := range []string{
		"-----BEGIN PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
	} {
		if !containsPrivateKeyMaterial([]byte(marker)) {
			t.Fatalf("marker %q not detected", marker)
		}
	}
	if containsPrivateKeyMaterial([]byte("public fixture")) {
		t.Fatal("benign fixture flagged as private key")
	}
	root := t.TempDir()
	child := filepath.Join(root, "child")
	outside := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-outside")
	if !pathInside(root, child) || pathInside(root, outside) {
		t.Fatal("pathInside boundary mismatch")
	}
	if relativeDisplay(root, filepath.Join(root, "a", "b")) != "a/b" {
		t.Fatal("relative display mismatch")
	}
}

func writeValidFixture(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(dir, "nginx"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := validManifest()
	manifest.ID = "fixture-one"
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		filepath.Join(dir, manifestName):          data,
		filepath.Join(dir, fixtureReadmeName):     []byte("fixture\n"),
		filepath.Join(dir, "nginx", "nginx.conf"): []byte("http {}\n"),
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failure") }
