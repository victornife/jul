// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build consul && kubernetes

package upstream

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

type closeTrackingTransport struct{ calls atomic.Int64 }

func (*closeTrackingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}
func (t *closeTrackingTransport) CloseIdleConnections() { t.calls.Add(1) }

func TestHTTPDiscovererCloseRetiresOwnedPools(t *testing.T) {
	for name, closeFn := range map[string]func(*http.Client) error{
		"consul": func(c *http.Client) error { return (&consulDiscoverer{client: c}).Close() },
		"kubernetes": func(c *http.Client) error { return (&k8sDiscoverer{client: c}).Close() },
	} {
		t.Run(name+" nil", func(t *testing.T) {
			if err := closeFn(nil); err != nil {
				t.Fatalf("Close(nil client): %v", err)
			}
		})
		t.Run(name+" client", func(t *testing.T) {
			transport := &closeTrackingTransport{}
			if err := closeFn(&http.Client{Transport: transport}); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if transport.calls.Load() != 1 {
				t.Fatalf("CloseIdleConnections calls = %d, want 1", transport.calls.Load())
			}
		})
	}
}
