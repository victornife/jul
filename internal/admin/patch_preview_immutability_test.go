// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

// TestPatchPreviewDoesNotMutateSharedLoaderPointer verifies AC-07: the preview
// endpoints apply patch operations to an independent clone, never to the object
// returned by LoadConfig. A loader may legally return a cached/shared pointer,
// so a preview that mutated it would corrupt shared state even though it claims
// no change was made.
func TestPatchPreviewDoesNotMutateSharedLoaderPointer(t *testing.T) {
	shared := patchProxyConfig()
	deps := Deps{
		// Adversarial loader: always returns the exact same pointer.
		LoadConfig: func() (*config.Config, error) { return shared, nil },
	}
	s := newTestServer(t, config.AdminConfig{}, deps)

	before := len(shared.Servers)

	// A batch that would add a whole new server if applied to the shared object.
	req := patchApplyRequest{
		Ops: []patchRequest{
			{Op: "server_add", Listen: ":9090"},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/preview", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if got := len(shared.Servers); got != before {
		t.Fatalf("preview mutated the shared loader pointer: servers = %d, want %d", got, before)
	}
}

// TestPatchPreviewFailedOpLeavesSharedPointerUnchanged verifies AC-07 for the
// partial-application case: op 1 succeeds against the clone, op 2 fails, and the
// shared loader pointer is still untouched — the successful op must not have
// leaked into shared state.
func TestPatchPreviewFailedOpLeavesSharedPointerUnchanged(t *testing.T) {
	shared := patchProxyConfig()
	deps := Deps{
		LoadConfig: func() (*config.Config, error) { return shared, nil },
	}
	s := newTestServer(t, config.AdminConfig{}, deps)

	before := len(shared.Servers)

	req := patchApplyRequest{
		Ops: []patchRequest{
			{Op: "server_add", Listen: ":9090"},
			// Second op targets a non-existent server, so the batch aborts.
			{Op: "server_remove", Listen: ":does-not-exist"},
		},
	}
	body, _ := json.Marshal(req)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/preview", bytes.NewReader(body)))
	// The batch fails (op 2), but importantly the shared pointer is unchanged.
	if got := len(shared.Servers); got != before {
		t.Fatalf("failed batch mutated the shared loader pointer: servers = %d, want %d", got, before)
	}
}
