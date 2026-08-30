// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/app"
	"jul/internal/config"
	"jul/internal/migrate/nginx"
	"jul/internal/migrate/nginx/corpus"
)

func TestNGINXCorpusAssessmentCandidateAndRealJul(t *testing.T) {
	fixtures, err := corpus.Discover(repositoryCorpusRoot(t))
	if err != nil {
		t.Fatalf("discover corpus: %v", err)
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Manifest.ID, func(t *testing.T) {
			cfg, report, err := nginx.ImportFileWithImportOptions(fixture.RootPath(), nginx.ImportOptions{
				Assessment:     nginx.AssessmentOptions{PathStyle: nginx.AssessmentPathRelative},
				FollowIncludes: fixture.Manifest.FollowIncludes,
				IncludeRoot:    fixture.IncludeRoot(),
			})
			if err != nil {
				t.Fatalf("import fixture: %v", err)
			}
			if report == nil || report.Assessment == nil {
				t.Fatal("importer returned no assessment")
			}
			assertCorpusAssessment(t, fixture.Manifest, report.Assessment)

			if !fixture.Manifest.Candidate.Required {
				if !report.Assessment.HasBlocking() {
					t.Fatal("fixture forbids generation but assessment has no blocker")
				}
				return
			}
			if report.Assessment.HasBlocking() {
				t.Fatalf("required candidate has blocking findings: %+v", report.Assessment.Results)
			}
			toml, err := config.Marshal(cfg)
			if err != nil {
				t.Fatalf("marshal candidate: %v", err)
			}
			loaded, err := config.Parse(toml)
			if err != nil {
				t.Fatalf("parse canonical candidate: %v\n%s", err, toml)
			}
			if err := config.Validate(loaded); err != nil {
				t.Fatalf("validate canonical candidate: %v\n%s", err, toml)
			}
			for _, expected := range fixture.Manifest.Candidate.Contains {
				if !bytes.Contains(toml, []byte(expected)) {
					t.Errorf("candidate missing semantic golden %q:\n%s", expected, toml)
				}
			}
			if len(fixture.Manifest.Scenarios) > 0 {
				runRealJulCorpusScenarios(t, fixture.Manifest, loaded)
			}
		})
	}
}

func repositoryCorpusRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "testdata", "nginx-corpus"))
}

func assertCorpusAssessment(t *testing.T, manifest corpus.Manifest, assessment *nginx.Assessment) {
	t.Helper()
	expected := manifest.Assessment
	if assessment.SchemaVersion != nginx.AssessmentSchemaVersion {
		t.Errorf("schema version = %d, want %d", assessment.SchemaVersion, nginx.AssessmentSchemaVersion)
	}
	if assessment.Status != expected.Status || assessment.Summary.Ready != expected.Ready {
		t.Errorf("status/ready = %q/%v, want %q/%v", assessment.Status, assessment.Summary.Ready, expected.Status, expected.Ready)
	}
	policy := assessment.SourcePolicy
	if policy.Complete != expected.Complete || policy.FilesRead != expected.FilesRead || policy.FollowInclude != manifest.FollowIncludes {
		t.Errorf("source policy = %+v, want complete=%v files=%d follow=%v", policy, expected.Complete, expected.FilesRead, manifest.FollowIncludes)
	}

	var sources []string
	for _, source := range assessment.Sources {
		sources = append(sources, source.DisplayPath)
	}
	sort.Strings(sources)
	if strings.Join(sources, "\x00") != strings.Join(expected.Sources, "\x00") {
		t.Errorf("sources = %#v, want %#v", sources, expected.Sources)
	}

	gotResults := map[string]int{}
	for i, result := range assessment.Results {
		if result.Provenance == nil {
			t.Errorf("results[%d] %s has no provenance", i, result.Code)
			continue
		}
		if result.Provenance.SourceID == "" || result.Provenance.DisplayPath == "" || result.Provenance.Start.Line < 1 || result.Provenance.ContextPath == "" {
			t.Errorf("results[%d] %s has incomplete provenance: %+v", i, result.Code, result.Provenance)
		}
		key := corpusResultKey(
			result.Provenance.DisplayPath,
			result.Code,
			string(result.Class),
			string(result.Risk),
			string(result.Context),
			result.Directive,
		)
		gotResults[key]++
	}
	expectedResults := map[string]int{}
	for _, result := range expected.Results {
		count := result.Count
		if count == 0 {
			count = 1
		}
		expectedResults[corpusResultKey(result.Source, result.Code, result.Class, result.Risk, result.Context, result.Directive)] = count
	}
	if diff := diffResultCounts(expectedResults, gotResults); diff != "" {
		t.Errorf("assessment result golden mismatch:\n%s", diff)
	}
}

func corpusResultKey(source, code, class, risk, context, directive string) string {
	return strings.Join([]string{source, code, class, risk, context, directive}, "\x00")
}

func diffResultCounts(want, got map[string]int) string {
	keys := make(map[string]struct{}, len(want)+len(got))
	for key := range want {
		keys[key] = struct{}{}
	}
	for key := range got {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	var b strings.Builder
	for _, key := range ordered {
		if want[key] == got[key] {
			continue
		}
		parts := strings.Split(key, "\x00")
		fmt.Fprintf(&b, "  %s | %s | %s | %s | %s | %s: want %d, got %d\n",
			parts[0], parts[1], parts[2], parts[3], parts[4], parts[5], want[key], got[key])
	}
	return b.String()
}

func runRealJulCorpusScenarios(t *testing.T, manifest corpus.Manifest, cfg *config.Config) {
	t.Helper()
	if len(cfg.Servers) != 1 {
		t.Fatalf("core runtime tranche requires exactly one server, got %d", len(cfg.Servers))
	}
	address := reserveLoopbackAddress(t)
	cfg.Servers[0].Listen = address
	if err := app.ValidateRuntimeConfig(context.Background(), cfg); err != nil {
		t.Fatalf("runtime preflight: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan struct{})
	var logs corpusLogBuffer
	done := make(chan int, 1)
	go func() {
		done <- app.Serve(ctx, reload, memorySource{name: "<nginx-corpus:" + manifest.ID + ">", cfg: cfg}, cfg, productName, version, app.WithLogOutput(&logs))
	}()

	defer func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("Jul exit code = %d\nlogs:\n%s", code, logs.String())
			}
		case <-time.After(5 * time.Second):
			t.Errorf("Jul did not shut down\nlogs:\n%s", logs.String())
		}
	}()

	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	baseURL := "http://" + address
	waitForCorpusServer(t, ctx, client, baseURL, manifest.Scenarios[0], done, &logs)

	for _, scenario := range manifest.Scenarios {
		scenario := scenario
		t.Run("runtime/"+scenario.ID, func(t *testing.T) {
			req, err := corpus.NewRequest(ctx, baseURL, scenario.Request)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			response, err := client.Do(req)
			if err != nil {
				t.Fatalf("execute request: %v\nlogs:\n%s", err, logs.String())
			}
			observation, observeErr := corpus.ObserveResponse(response, corpus.DefaultMaxObservedBody)
			closeErr := response.Body.Close()
			if observeErr != nil {
				t.Fatalf("observe response: %v", observeErr)
			}
			if closeErr != nil {
				t.Fatalf("close response: %v", closeErr)
			}
			result := corpus.Evaluate(scenario, observation)
			if result.Verdict != scenario.ExpectedVerdict || result.DifferenceCode != scenario.ExpectedDifferenceCode {
				t.Fatalf("verdict = %+v, want %q/%q", result, scenario.ExpectedVerdict, scenario.ExpectedDifferenceCode)
			}
			if result.Verdict == corpus.VerdictUnexpected {
				t.Fatalf("unexpected difference: %+v", result.Differences)
			}
		})
	}

}

func reserveLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback address: %v", err)
	}
	return address
}

func waitForCorpusServer(t *testing.T, ctx context.Context, client *http.Client, baseURL string, scenario corpus.Scenario, done chan int, logs *corpusLogBuffer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case code := <-done:
			done <- code
			t.Fatalf("Jul exited during startup with code %d\nlogs:\n%s", code, logs.String())
		default:
		}
		req, err := corpus.NewRequest(ctx, baseURL, scenario.Request)
		if err != nil {
			t.Fatalf("build readiness request: %v", err)
		}
		response, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Jul did not become reachable before the startup deadline\nlogs:\n%s", logs.String())
}

type corpusLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *corpusLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *corpusLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
