// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package corpus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const DefaultMaxObservedBody = 1 << 20

// Observation is one captured real-server response.
type Observation struct {
	Status  int
	Headers http.Header
	Body    []byte
}

// Difference is one asserted semantic mismatch.
type Difference struct {
	Dimension Dimension `json:"dimension"`
	Field     string    `json:"field,omitempty"`
	Want      string    `json:"want"`
	Got       string    `json:"got"`
}

// Result is the bounded outcome for one scenario.
type Result struct {
	Verdict        Verdict      `json:"verdict"`
	DifferenceCode string       `json:"difference_code,omitempty"`
	Differences    []Difference `json:"differences,omitempty"`
}

// NewRequest builds only loopback HTTP(S) requests from the safe scenario
// grammar. It never resolves or dials an external host.
func NewRequest(ctx context.Context, baseURL string, spec RequestSpec) (*http.Request, error) {
	if err := validateRequestTarget(spec.Path); err != nil {
		return nil, fmt.Errorf("request path must be a valid origin-form target: %w", err)
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("base URL scheme must be http or https")
	}
	if !loopbackHost(base.Host) {
		return nil, fmt.Errorf("base URL must target loopback")
	}
	target, err := url.ParseRequestURI(spec.Path)
	if err != nil || target.IsAbs() || target.Host != "" {
		return nil, fmt.Errorf("request path must be a valid origin-form target")
	}
	base.Path, base.RawPath, base.RawQuery = "", "", ""
	base.Fragment = ""
	base = base.ResolveReference(target)
	req, err := http.NewRequestWithContext(ctx, spec.Method, base.String(), strings.NewReader(spec.Body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if spec.Host != "" {
		req.Host = spec.Host
	}
	for name, values := range spec.Headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	return req, nil
}

// ObserveResponse captures a bounded response body and normalized header copy.
func ObserveResponse(response *http.Response, maxBody int64) (Observation, error) {
	if response == nil {
		return Observation{}, fmt.Errorf("nil response")
	}
	if maxBody <= 0 {
		maxBody = DefaultMaxObservedBody
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return Observation{}, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(data)) > maxBody {
		return Observation{}, fmt.Errorf("response body exceeds %d bytes", maxBody)
	}
	return Observation{
		Status:  response.StatusCode,
		Headers: response.Header.Clone(),
		Body:    data,
	}, nil
}

// EvaluateReference verifies that a pinned NGINX runtime still matches the
// reviewed reference side of one scenario. It does not make a Jul-equivalence
// claim; cross-runtime classification remains Evaluate's responsibility.
func EvaluateReference(scenario Scenario, actual Observation) Result {
	if scenario.ExpectedVerdict == VerdictNotExecuted {
		return Result{Verdict: VerdictNotExecuted}
	}
	if scenario.ExpectedVerdict == VerdictBlockingSource {
		return Result{Verdict: VerdictBlockingSource}
	}
	if differences := compareActual(scenario, scenario.Reference, actual); len(differences) > 0 {
		return Result{Verdict: VerdictUnexpected, Differences: differences}
	}
	return Result{Verdict: VerdictEquivalent}
}

// Evaluate verifies the real Jul observation against its explicit expectation,
// then classifies the approved reference-vs-Jul relationship.
func Evaluate(scenario Scenario, actual Observation) Result {
	if scenario.ExpectedVerdict == VerdictNotExecuted {
		return Result{Verdict: VerdictNotExecuted}
	}
	if scenario.ExpectedVerdict == VerdictBlockingSource {
		return Result{Verdict: VerdictBlockingSource}
	}
	expectedJul := scenario.Reference
	if scenario.Jul != nil {
		expectedJul = *scenario.Jul
	}
	actualDiffs := compareActual(scenario, expectedJul, actual)
	if len(actualDiffs) > 0 {
		return Result{Verdict: VerdictUnexpected, Differences: actualDiffs}
	}
	referenceDiffs := compareObservationSpecs(scenario, scenario.Reference, expectedJul)
	if len(referenceDiffs) == 0 {
		return Result{Verdict: VerdictEquivalent}
	}
	if scenario.ExpectedVerdict == VerdictExpectedDifference && scenario.ExpectedDifferenceCode != "" {
		return Result{
			Verdict:        VerdictExpectedDifference,
			DifferenceCode: scenario.ExpectedDifferenceCode,
			Differences:    referenceDiffs,
		}
	}
	return Result{Verdict: VerdictUnexpected, Differences: referenceDiffs}
}

func compareActual(scenario Scenario, want ObservationSpec, got Observation) []Difference {
	var diffs []Difference
	for _, dimension := range scenario.Assert {
		switch dimension {
		case DimensionStatus:
			if want.Status != got.Status {
				diffs = append(diffs, difference(dimension, "status", fmt.Sprint(want.Status), fmt.Sprint(got.Status)))
			}
		case DimensionHeaders:
			for _, name := range scenario.AssertHeaders {
				wantValues := normalizeValues(want.Headers[name])
				gotValues := normalizeValues(got.Headers.Values(name))
				if !equalStrings(wantValues, gotValues) {
					diffs = append(diffs, difference(dimension, name, strings.Join(wantValues, "\n"), strings.Join(gotValues, "\n")))
				}
			}
		case DimensionBody:
			if want.Body != string(got.Body) {
				diffs = append(diffs, difference(dimension, "body", want.Body, string(got.Body)))
			}
		case DimensionBodySHA256:
			gotHash := digest(got.Body)
			if want.BodySHA256 != gotHash {
				diffs = append(diffs, difference(dimension, "body_sha256", want.BodySHA256, gotHash))
			}
		case DimensionRedirectTarget:
			wantLocation := firstNormalized(want.Headers["location"])
			gotLocation := firstNormalized(got.Headers.Values("Location"))
			if wantLocation != gotLocation {
				diffs = append(diffs, difference(dimension, "location", wantLocation, gotLocation))
			}
		}
	}
	return diffs
}

func compareObservationSpecs(scenario Scenario, left, right ObservationSpec) []Difference {
	actual := Observation{
		Status:  right.Status,
		Headers: make(http.Header, len(right.Headers)),
		Body:    []byte(right.Body),
	}
	for name, values := range right.Headers {
		actual.Headers[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
	}
	if hasDimension(scenario.Assert, DimensionBodySHA256) {
		// compareActual hashes bytes, while a manifest stores only the expected
		// digest. Compare the two declared digests directly instead.
		diffs := compareActualWithoutHash(scenario, left, actual)
		if left.BodySHA256 != right.BodySHA256 {
			diffs = append(diffs, difference(DimensionBodySHA256, "body_sha256", left.BodySHA256, right.BodySHA256))
		}
		return diffs
	}
	return compareActual(scenario, left, actual)
}

func compareActualWithoutHash(scenario Scenario, want ObservationSpec, got Observation) []Difference {
	copyScenario := scenario
	copyScenario.Assert = make([]Dimension, 0, len(scenario.Assert))
	for _, dimension := range scenario.Assert {
		if dimension != DimensionBodySHA256 {
			copyScenario.Assert = append(copyScenario.Assert, dimension)
		}
	}
	return compareActual(copyScenario, want, got)
}

func difference(dimension Dimension, field, want, got string) Difference {
	return Difference{Dimension: dimension, Field: field, Want: want, Got: got}
}

func normalizeValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.TrimSpace(value))
	}
	sort.Strings(out)
	return out
}

func firstNormalized(values []string) string {
	normalized := normalizeValues(values)
	if len(normalized) == 0 {
		return ""
	}
	return normalized[0]
}

func equalStrings(left, right []string) bool {
	return len(left) == len(right) && bytes.Equal([]byte(strings.Join(left, "\x00")), []byte(strings.Join(right, "\x00")))
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hasDimension(dimensions []Dimension, target Dimension) bool {
	for _, dimension := range dimensions {
		if dimension == target {
			return true
		}
	}
	return false
}

func loopbackHost(hostport string) bool {
	host := hostport
	if splitHost, port, err := net.SplitHostPort(hostport); err == nil {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return false
		}
		host = splitHost
	} else if strings.Contains(hostport, ":") && !strings.HasPrefix(hostport, "[") {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
