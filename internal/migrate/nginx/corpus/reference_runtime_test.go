// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package corpus

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

const (
	referenceFixtureEnv = "NGINX_CORPUS_FIXTURE_DIR"
	referenceBaseURLEnv = "NGINX_CORPUS_BASE_URL"
)

func TestNGINXCorpusReferenceRuntime(t *testing.T) {
	fixtureDir := os.Getenv(referenceFixtureEnv)
	baseURL := os.Getenv(referenceBaseURLEnv)
	if fixtureDir == "" || baseURL == "" {
		t.Skipf("set %s and %s to execute the pinned NGINX reference lane", referenceFixtureEnv, referenceBaseURLEnv)
	}

	fixture, err := Load(fixtureDir)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if len(fixture.Manifest.Scenarios) == 0 {
		t.Fatal("reference runtime fixture has no scenarios")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := &http.Client{
		Timeout: 3 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	waitForReferenceRuntime(t, ctx, client, baseURL, fixture.Manifest.Scenarios[0])

	for _, scenario := range fixture.Manifest.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			req, err := NewRequest(ctx, baseURL, scenario.Request)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			response, err := client.Do(req)
			if err != nil {
				t.Fatalf("execute request: %v", err)
			}
			observation, observeErr := ObserveResponse(response, DefaultMaxObservedBody)
			closeErr := response.Body.Close()
			if observeErr != nil {
				t.Fatalf("observe response: %v", observeErr)
			}
			if closeErr != nil {
				t.Fatalf("close response: %v", closeErr)
			}
			result := EvaluateReference(scenario, observation)
			if result.Verdict == VerdictUnexpected {
				t.Fatalf("NGINX reference mismatch: %s", formatDifferences(result.Differences))
			}
		})
	}
}

func waitForReferenceRuntime(t *testing.T, ctx context.Context, client *http.Client, baseURL string, scenario Scenario) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, err := NewRequest(ctx, baseURL, scenario.Request)
		if err != nil {
			t.Fatalf("build readiness request: %v", err)
		}
		response, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("NGINX reference runtime did not become reachable: %v", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("NGINX reference runtime did not become reachable before the startup deadline")
}

func formatDifferences(differences []Difference) string {
	if len(differences) == 0 {
		return "no differences"
	}
	out := ""
	for i, difference := range differences {
		if i > 0 {
			out += "; "
		}
		out += fmt.Sprintf("%s/%s want %q got %q", difference.Dimension, difference.Field, difference.Want, difference.Got)
	}
	return out
}
