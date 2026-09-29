// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatusRecorderUsesFinalStatusAfterEarlyHints(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	s.WriteHeader(http.StatusEarlyHints)
	s.WriteHeader(http.StatusServiceUnavailable)
	if s.status != http.StatusServiceUnavailable {
		t.Fatalf("observed %d; want final status 503", s.status)
	}
}

func TestStatusRecorderFlushCommitsSuccess(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	s.Flush()
	s.WriteHeader(http.StatusServiceUnavailable)
	if s.status != http.StatusOK || rec.Code != http.StatusOK {
		t.Fatalf("observed %d, wire %d; want 200", s.status, rec.Code)
	}
}
