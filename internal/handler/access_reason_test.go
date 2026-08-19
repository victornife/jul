// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/upstream"
)

// TestAccessLogReasonIsBoundedToTheEnum pins the access-log field to the closed
// taxonomy.
//
// internal/middleware takes the reason as a plain string to keep the dependency
// pointing one way, which means nothing there stops an unbounded value being
// written. This is the lowest package that can see both, so the check lives
// here.
func TestAccessLogReasonIsBoundedToTheEnum(t *testing.T) {
	for _, r := range upstream.Reasons() {
		if !r.Valid() {
			t.Fatalf("Reasons() yielded %q, which is not a member of its own set", r)
		}
	}

	var got []string
	sink := sinkFunc(func(rec middleware.AccessRecord) {
		if rec.UpstreamReason != "" {
			got = append(got, rec.UpstreamReason)
		}
	})

	// Drive one request per reason through the middleware, publishing that
	// reason from a handler standing in for the proxy.
	for _, want := range upstream.Reasons() {
		h := middleware.AccessLog(sink)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			middleware.SetUpstreamReason(r.Context(), string(want))
			w.WriteHeader(http.StatusBadGateway)
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	}

	want := make([]string, 0, len(upstream.Reasons()))
	for _, r := range upstream.Reasons() {
		want = append(want, string(r))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("logged reasons = %v, want exactly the enum %v", got, want)
	}
}

// TestAccessLogOmitsTheReasonOnSuccess pins that a served request does not carry
// an empty field on every line, following the same rule as trace_id and peer_ip.
func TestAccessLogOmitsTheReasonOnSuccess(t *testing.T) {
	var records []middleware.AccessRecord
	sink := sinkFunc(func(rec middleware.AccessRecord) { records = append(records, rec) })

	h := middleware.AccessLog(sink)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/", nil))

	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if records[0].UpstreamReason != "" {
		t.Fatalf("upstream_reason = %q on a successful request, want empty", records[0].UpstreamReason)
	}
}

// TestProxyFailurePublishesTheReason is the end-to-end check: the reason has to
// travel from the proxy, which knows it, up to the access-log middleware, which
// wraps it. A field nobody fills is worse than no field.
func TestProxyFailurePublishesTheReason(t *testing.T) {
	var records []middleware.AccessRecord
	sink := sinkFunc(func(rec middleware.AccessRecord) { records = append(records, rec) })

	// A backend that is not listening: every dial fails.
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://127.0.0.1:1"}, nil)
	wrapped := middleware.AccessLog(sink)(h)
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/", nil))

	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	got := records[0].UpstreamReason
	if got == "" {
		t.Fatal("a failed upstream call logged no reason")
	}
	if !upstream.Reason(got).Valid() {
		t.Fatalf("logged reason %q is not a member of the closed set", got)
	}
	if got != string(upstream.ReasonUpstreamConnectFailed) {
		t.Fatalf("reason = %q, want %q for a refused dial", got, upstream.ReasonUpstreamConnectFailed)
	}
}

type sinkFunc func(middleware.AccessRecord)

func (f sinkFunc) Log(rec middleware.AccessRecord) { f(rec) }
