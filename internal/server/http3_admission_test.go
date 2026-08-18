// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"jul/internal/resilience"
	"jul/internal/upstream"
)

// TestHTTP3AdmissionCountsPerStream pins the HTTP/3 row of ADR 0017's accounting
// matrix against a real QUIC stack.
//
// Jul's HTTP/3 support is inbound only — there is no HTTP/3 backend transport —
// so h3 requests are served by the same handler tree as everything else and
// admission needs no protocol-specific code. That is a claim worth testing
// rather than asserting: this drives real QUIC streams through an admission-
// bounded handler and checks that each stream takes exactly one slot, that
// concurrent streams on one QUIC connection each take their own, and that the
// connection itself is never what is counted.
func TestHTTP3AdmissionCountsPerStream(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "h3-admission", "localhost")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load cert: %v", err)
	}
	getCert := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }

	policy, err := resilience.Resolve(resilience.Options{MaxActiveRequests: 2})
	if err != nil {
		t.Fatalf("resolve policy: %v", err)
	}
	adm := upstream.NewAdmission(policy)

	hold := make(chan struct{})
	var holdOnce sync.Once
	defer holdOnce.Do(func() { close(hold) })

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release, err := adm.Admit(r.Context(), nil)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		defer release()
		if r.URL.Path == "/hold" {
			<-hold
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, r.Proto)
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h3, err := startHTTP3("127.0.0.1:0", getCert, handler, func(int64) {}, logger)
	if err != nil {
		t.Fatalf("startHTTP3: %v", err)
	}
	defer func() { _ = h3.Close(context.Background()) }()
	addr := h3.(*h3Conn).ln.Addr().String()

	pool := x509.NewCertPool()
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("failed to add self-signed cert to pool")
	}
	tr := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}}
	defer func() { _ = tr.Close() }()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	// One QUIC connection is established here and reused for every request
	// below, which is the point: the connection is never what is counted.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get("https://" + addr + "/")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.ProtoMajor != 3 {
				t.Fatalf("proto = %q, want HTTP/3", resp.Proto)
			}
			if string(body) != "HTTP/3.0" {
				t.Fatalf("handler saw proto %q", body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP/3 GET never succeeded: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := adm.Active(); got != 0 {
		t.Fatalf("active after one completed h3 request = %d, want 0", got)
	}

	// Two concurrent streams over the same QUIC connection: each takes its own
	// slot, so the limit of 2 is exactly consumed.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get("https://" + addr + "/hold")
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	waitAdmissionActive(t, adm, 2)

	// A third concurrent stream on that same connection exceeds the request
	// limit, proving the count is per stream and not per connection.
	resp, err := client.Get("https://" + addr + "/third")
	if err != nil {
		t.Fatalf("third request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("third concurrent h3 stream: status = %d, want 503", resp.StatusCode)
	}

	holdOnce.Do(func() { close(hold) })
	wg.Wait()

	deadline = time.Now().Add(5 * time.Second)
	for adm.Active() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("active at quiesce = %d, want 0", adm.Active())
		}
		time.Sleep(time.Millisecond)
	}
}

func waitAdmissionActive(t *testing.T, a *upstream.Admission, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for a.Active() != n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for active=%d (have %d)", n, a.Active())
		}
		time.Sleep(time.Millisecond)
	}
}
