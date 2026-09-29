// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"jul/internal/config"
)

func TestReadBoundedBodyRejectsValidPrefixWithOversizedSuffix(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/config/adopt-external", strings.NewReader(`{"base_version":"v1"}`+strings.Repeat(" ", 1<<16)))
	if _, err := readBoundedBody(r, 1<<16); err == nil {
		t.Fatal("oversized valid JSON prefix accepted")
	}
}

func TestRouteTestRejectsOversizedValidPrefix(t *testing.T) {
	s := &Server{deps: Deps{LoadConfig: func() (*config.Config, error) { return routeTestCfg(), nil }}}
	r := httptest.NewRequest(http.MethodPost, "/api/routes/test", strings.NewReader(`{"path":"/"}`+strings.Repeat(" ", 1<<16)))
	w := httptest.NewRecorder()
	s.handleRouteTest(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized dry-run input accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestBoundedPatchJSONRejectsOversizedValidPrefix(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", strings.NewReader(`{"ops":[]}`+strings.Repeat(" ", 1<<16)))
	_, err := decodePatchBatch(r)
	if err == nil {
		t.Fatal("oversized patch accepted")
	}
}

func TestReadBoundedBodyPropagatesReaderFailure(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/config/preview", nil)
	want := errors.New("body read failed")
	r.Body = io.NopCloser(iotest.ErrReader(want))
	if _, err := readBoundedBody(r, 1<<20); !errors.Is(err, want) {
		t.Fatalf("readBoundedBody error = %v, want %v", err, want)
	}
}

func TestRawPreviewRejectsOversizedCandidateBeforeStateRead(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/config/preview", strings.NewReader("[cache]\n"+strings.Repeat(" ", 1<<20)))
	r.Header.Set(rawPreviewBaseVersionHeader, "pinned-version")
	w := httptest.NewRecorder()
	(&Server{}).handleConfigPreview(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Could not read the candidate") {
		t.Fatalf("raw preview oversized body: status=%d body=%s", w.Code, w.Body.String())
	}
}
