// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package server

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

// TestH3ConnExitAfterCloseDoesNotFireOnExit proves an intentional Close never
// triggers onExit: acceptLoop's resulting Accept error is expected, not a
// live failure (#161).
func TestH3ConnExitAfterCloseDoesNotFireOnExit(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "h3-altsvc-exit", "localhost")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load cert: %v", err)
	}
	tlsConf := &tls.Config{Certificates: []tls.Certificate{cert}}

	h3, err := newStagedHTTP3WithTLS("127.0.0.1:0", tlsConf, http.NotFoundHandler(), nil, discardLogger())
	if err != nil {
		t.Fatalf("newStagedHTTP3WithTLS: %v", err)
	}
	var fired atomicFlag
	h3.SetOnExit(func(error) { fired.set() })
	if err := h3.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h3.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Give acceptLoop's goroutine a moment to observe the close and return.
	time.Sleep(50 * time.Millisecond)
	if fired.get() {
		t.Fatal("onExit fired after an intentional Close")
	}
}
