// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package egress

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

func TestManagerPrepareIsolatedUntilPublish(t *testing.T) {
	m, err := NewManager(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	original := m.Current()
	if original == nil || !original.Enabled() {
		t.Fatal("startup generation should be enabled")
	}

	same, changed, err := m.Prepare(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1", "127.0.0.1"}})
	if err != nil {
		t.Fatalf("Prepare same: %v", err)
	}
	if changed || same != original {
		t.Fatalf("semantically equal policy = (%p,%v), want current %p,false", same, changed, original)
	}

	candidate, changed, err := m.Prepare(config.EgressConfig{Enabled: true, Allow: []string{"192.0.2.1"}})
	if err != nil {
		t.Fatalf("Prepare changed: %v", err)
	}
	if !changed || candidate == original {
		t.Fatal("changed policy must prepare a distinct generation")
	}
	if candidate.ID() <= original.ID() {
		t.Fatalf("candidate ID = %d, original = %d", candidate.ID(), original.ID())
	}
	if got := m.Current(); got != original {
		t.Fatalf("Prepare mutated live generation: got %p want %p", got, original)
	}

	retired := m.Publish(candidate)
	if retired != original || m.Current() != candidate {
		t.Fatal("Publish did not atomically swap candidate/current")
	}
}

func TestManagerPrepareRejectsMalformedPolicyWithoutPublish(t *testing.T) {
	m, err := NewManager(config.EgressConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	original := m.Current()
	if _, _, err := m.Prepare(config.EgressConfig{Enabled: true}); err == nil {
		t.Fatal("enabled policy without allow entries should fail")
	}
	if m.Current() != original {
		t.Fatal("failed Prepare changed live generation")
	}
}

func TestDynamicClientTighteningCannotReuseOldKeepAlive(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Length", "2")
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	m, err := NewManager(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	client := m.Client(SubsystemACME, 0)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("policy A request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if requests.Load() != 1 {
		t.Fatalf("server requests = %d, want 1", requests.Load())
	}

	candidate, changed, err := m.Prepare(config.EgressConfig{Enabled: true, Allow: []string{"192.0.2.1"}})
	if err != nil || !changed {
		t.Fatalf("Prepare B = changed %v err %v", changed, err)
	}
	old := m.Publish(candidate)
	defer old.CloseIdleConnections()

	resp, err = client.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("policy B request err = %v, want ErrBlocked", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("blocked request reached old keep-alive connection: server requests = %d", requests.Load())
	}
}

func TestDynamicClientRedirectHopUsesPublishedGeneration(t *testing.T) {
	var targetRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		_, _ = io.WriteString(w, "target")
	}))
	defer target.Close()

	m, err := NewManager(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	candidate, changed, err := m.Prepare(config.EgressConfig{Enabled: true, Allow: []string{"192.0.2.1"}})
	if err != nil || !changed {
		t.Fatalf("Prepare B = changed %v err %v", changed, err)
	}

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.Publish(candidate)
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	client := m.Client(SubsystemACME, 0)
	resp, err := client.Get(redirector.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("redirect after Publish err = %v, want ErrBlocked", err)
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect reused old-policy transport; target requests = %d", targetRequests.Load())
	}
}

func TestGenerationDisabledPreservesProxyAndEnabledPinsItOff(t *testing.T) {
	disabled, err := NewManager(config.EgressConfig{})
	if err != nil {
		t.Fatalf("disabled manager: %v", err)
	}
	disabled.Current().roundTripper(SubsystemACME)
	disabled.Current().mu.Lock()
	disabledProxy := disabled.Current().http[SubsystemACME].transport.Proxy
	disabled.Current().mu.Unlock()
	if disabledProxy == nil {
		t.Fatal("disabled generation should preserve default proxy behavior")
	}

	enabled, err := NewManager(config.EgressConfig{Enabled: true, Allow: []string{"example.com"}})
	if err != nil {
		t.Fatalf("enabled manager: %v", err)
	}
	enabled.Current().roundTripper(SubsystemACME)
	enabled.Current().mu.Lock()
	enabledProxy := enabled.Current().http[SubsystemACME].transport.Proxy
	enabled.Current().mu.Unlock()
	if enabledProxy != nil {
		t.Fatal("enabled generation must pin Proxy=nil")
	}
}

type idleCloserRoundTripper struct {
	closed atomic.Int64
}

func (*idleCloserRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

func (r *idleCloserRoundTripper) CloseIdleConnections() { r.closed.Add(1) }

func TestGuardedRoundTripperForwardsCloseIdleConnections(t *testing.T) {
	p, err := New(config.EgressConfig{Enabled: true, Allow: []string{"example.com"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	next := &idleCloserRoundTripper{}
	rt := &guardedRoundTripper{subsystem: SubsystemAuth, policy: p, next: next}
	rt.CloseIdleConnections()
	if next.closed.Load() != 1 {
		t.Fatalf("close calls = %d, want 1", next.closed.Load())
	}
}
