package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadBoundedBodyRejectsValidPrefixWithOversizedSuffix(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/config/adopt-external", strings.NewReader(`{"base_version":"v1"}`+strings.Repeat(" ", 1<<16)))
	if _, err := readBoundedBody(r, 1<<16); err == nil {
		t.Fatal("oversized valid JSON prefix accepted")
	}
}

func TestBoundedPatchJSONRejectsOversizedValidPrefix(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", strings.NewReader(`{"ops":[]}`+strings.Repeat(" ", 1<<16)))
	_, err := decodePatchBatch(r)
	if err == nil {
		t.Fatal("oversized patch accepted")
	}
}
