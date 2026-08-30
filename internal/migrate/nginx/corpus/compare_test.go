// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package corpus

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEvaluateEquivalentSelectedDimensions(t *testing.T) {
	scenario := Scenario{
		ID:              "health",
		Safe:            true,
		Request:         RequestSpec{Method: "GET", Path: "/health"},
		Assert:          []Dimension{DimensionStatus, DimensionHeaders, DimensionBody},
		AssertHeaders:   []string{"x-corpus"},
		Reference:       ObservationSpec{Status: 200, Headers: map[string][]string{"x-corpus": {"ok"}}, Body: "healthy"},
		ExpectedVerdict: VerdictEquivalent,
	}
	if err := scenario.Validate(); err != nil {
		t.Fatalf("scenario validation: %v", err)
	}
	actual := Observation{Status: 200, Headers: http.Header{"X-Corpus": {"ok"}}, Body: []byte("healthy")}
	result := Evaluate(scenario, actual)
	if result.Verdict != VerdictEquivalent || len(result.Differences) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestEvaluateExpectedAndUnexpectedDifference(t *testing.T) {
	scenario := Scenario{
		ID:                     "method-denial",
		Safe:                   true,
		Request:                RequestSpec{Method: "GET", Path: "/resource"},
		Assert:                 []Dimension{DimensionStatus},
		Reference:              ObservationSpec{Status: 403},
		Jul:                    &ObservationSpec{Status: 404},
		ExpectedVerdict:        VerdictExpectedDifference,
		ExpectedDifferenceCode: "NGX_LOCATION_LIMIT_EXCEPT",
	}
	if err := scenario.Validate(); err != nil {
		t.Fatalf("scenario validation: %v", err)
	}
	result := Evaluate(scenario, Observation{Status: 404})
	if result.Verdict != VerdictExpectedDifference || result.DifferenceCode != "NGX_LOCATION_LIMIT_EXCEPT" {
		t.Fatalf("result = %+v", result)
	}
	result = Evaluate(scenario, Observation{Status: 200})
	if result.Verdict != VerdictUnexpected || len(result.Differences) != 1 {
		t.Fatalf("unexpected runtime mismatch result = %+v", result)
	}
}

func TestNewRequestRejectsExternalNetwork(t *testing.T) {
	_, err := NewRequest(context.Background(), "https://example.com", RequestSpec{Method: "GET", Path: "/"})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error = %v, want loopback rejection", err)
	}
	req, err := NewRequest(context.Background(), "http://127.0.0.1:8080", RequestSpec{Method: "GET", Path: "/x?q=1", Host: "fixture.test"})
	if err != nil {
		t.Fatalf("loopback request: %v", err)
	}
	if req.Host != "fixture.test" || req.URL.RequestURI() != "/x?q=1" {
		t.Fatalf("request = host %q uri %q", req.Host, req.URL.RequestURI())
	}
}

func TestObserveResponseBoundsBody(t *testing.T) {
	response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("abcdef"))}
	if _, err := ObserveResponse(response, 3); err == nil {
		t.Fatal("oversized body unexpectedly accepted")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Corpus", "ok")
		_, _ = w.Write([]byte("body"))
	}))
	defer server.Close()
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	observation, err := ObserveResponse(resp, 16)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Status != 200 || string(observation.Body) != "body" || observation.Headers.Get("X-Corpus") != "ok" {
		t.Fatalf("observation = %+v", observation)
	}
}
