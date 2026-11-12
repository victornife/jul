// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"jul/internal/config"
	"jul/internal/inspection"
)

// fetchPlugin is a bare plugin whose fetch client dials addr directly, so
// these tests target charging rather than the SSRF guard.
func fetchPlugin(addr string, maxResp int) *plugin {
	return &plugin{
		capFetch: true, allowedHosts: []string{"origin.test"}, fetchTimeout: 2 * time.Second,
		maxFetchResp: maxResp, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		client: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			},
		}},
	}
}

// rawOrigin answers every request with resp verbatim and closes.
func rawOrigin(t *testing.T, resp string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = http.ReadRequest(bufio.NewReader(c))
				_, _ = io.WriteString(c, resp)
			}()
		}
	}()
	return ln.Addr().String()
}

// TestFetchResponseChargeBySize: the response buffer is charged before it
// is read, at the declared length plus the detection byte when that is
// within max_fetch_response, else at the whole cap; it stays charged while
// fetch_read can reach it and is refunded by the next fetch or the call end.
func TestFetchResponseChargeBySize(t *testing.T) {
	const maxResp = 1000
	cases := []struct {
		name      string
		resp      string
		wantErr   bool
		wantLen   int
		charged   uint64
		truncated bool
	}{
		{"declared", "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n" + strings.Repeat("a", 100), false, 100, 101, false},
		{"unknown", "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n" + strings.Repeat("b", 300), false, 300, maxResp + 1, false},
		{"declared over cap", "HTTP/1.1 200 OK\r\nContent-Length: 5000\r\n\r\n" + strings.Repeat("c", 5000), false, maxResp, maxResp + 1, true},
		{"unknown over cap", "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n" + strings.Repeat("d", 5000), false, maxResp, maxResp + 1, true},
		// Declared 100, connection closed after 50: never a complete body.
		{"cut short", "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n" + strings.Repeat("e", 50), true, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := testGovernor(t)
			lease, ctx, _, _ := g.AcquireLease(t.Context())
			defer lease.Close()
			inv := &invocation{lease: lease}
			p := fetchPlugin(rawOrigin(t, c.resp), maxResp)
			base := g.Snapshot().Usage[inspection.BufferBytes]
			_, data, err := p.doFetch(ctx, inv, "GET", "http://origin.test/", nil)
			if c.wantErr {
				if err == nil {
					t.Fatalf("accepted %d bytes of a cut-short response", len(data))
				}
				if inv.lastFetch != nil || g.Snapshot().Usage[inspection.BufferBytes] != base {
					t.Fatal("failed fetch left a charged or visible response")
				}
				return
			}
			if err != nil || len(data) != c.wantLen || inv.lastFetchTruncated != c.truncated {
				t.Fatalf("err=%v len=%d truncated=%v", err, len(data), inv.lastFetchTruncated)
			}
			if got := g.Snapshot().Usage[inspection.BufferBytes] - base; got != c.charged {
				t.Fatalf("charged %d, want %d", got, c.charged)
			}
			inv.endCall()
			if g.Snapshot().Usage[inspection.BufferBytes] != base || inv.lastFetch != nil {
				t.Fatal("call end did not refund the fetch response")
			}
		})
	}
}

// TestRepeatedFetchRefundsPrevious: each fetch refunds the previous response
// before charging its own, so repeated fetches hold one response, and the
// request-body copy is charged while the origin reads it and refunded after.
func TestRepeatedFetchRefundsPrevious(t *testing.T) {
	var during []uint64
	g := testGovernor(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		during = append(during, g.Snapshot().Usage[inspection.BufferBytes])
		_, _ = io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, strings.Repeat("r", 200))
	}))
	defer srv.Close()
	lease, ctx, _, _ := g.AcquireLease(t.Context())
	defer lease.Close()
	inv := &invocation{lease: lease}
	p := fetchPlugin(srv.Listener.Addr().String(), 1000)
	for i := range 3 {
		body, err := copyGuest(inv.chargeFn(), []byte(strings.Repeat("q", 64)))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.doFetch(ctx, inv, "POST", "http://origin.test/", body); err != nil {
			t.Fatal(err)
		}
		// One 201-byte response (CL 200 + detection byte), nothing else.
		if got := g.Snapshot().Usage[inspection.BufferBytes]; got != 201 {
			t.Fatalf("fetch %d: live buffer %d, want 201", i, got)
		}
	}
	// While the origin read the body: the 64-byte copy plus, after the first
	// call, the previous response is already refunded.
	for i, d := range during {
		if d != 64 {
			t.Fatalf("fetch %d: origin saw %d charged, want the 64-byte request copy only", i, d)
		}
	}
	inv.endCall()
	if got := g.Snapshot().Usage[inspection.BufferBytes]; got != 0 {
		t.Fatalf("leaked %d", got)
	}
}

// TestFetchResponseRefusedKeepsFailure: a response that cannot be charged
// fails the fetch and the invocation as overload, so a guest that ignores
// the error code still ends in 503 rather than being served.
func TestFetchResponseRefusedKeepsFailure(t *testing.T) {
	g, err := inspection.New(limitsWith(100), inspection.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	lease, ctx, _, _ := g.AcquireLease(t.Context())
	defer lease.Close()
	inv := &invocation{lease: lease}
	p := fetchPlugin(rawOrigin(t, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nxyz"), 1000)
	if _, _, err := p.doFetch(ctx, inv, "GET", "http://origin.test/", nil); !inspection.IsOverload(err) {
		t.Fatalf("err = %v, want overload", err)
	}
	if !inspection.IsOverload(inv.err) {
		t.Fatalf("invocation error = %v, want the overload retained", inv.err)
	}
	if got := g.Snapshot().Usage[inspection.BufferBytes]; got != 0 {
		t.Fatalf("leaked %d", got)
	}
}

// TestGuestIgnoringFetchRefusalGets503 runs a real guest that calls fetch,
// ignores its refusal and continues: the request is answered 503, the
// origin is not reached and no panic is recorded.
func TestGuestIgnoringFetchRefusalGets503(t *testing.T) {
	for _, abi := range bothABIs {
		t.Run(abi, func(t *testing.T) {
			url := []byte("http://origin.test/")
			// fetch(method_ptr, method_len, url_ptr, url_len, body_ptr, body_len, hdr_ptr, hdr_len)
			g := hostGuest{abi: abi, fns: []hostFn{hfFetch}, data: map[int32][]byte{64: []byte("GET"), 128: url},
				body: seq(call(0, 64, 3, 128, int32(len(url)), 0, 0, 0, 0), []byte{opDrop}, i32c(1))}
			m, hooks, gov, _ := govManager(t, limitsWith(100))
			s := buildSet(t, m, map[string]config.PluginConfig{"f": g.config(func(pc *config.PluginConfig) {
				pc.Fetch, pc.AllowedHosts, pc.MaxFetchResponse = true, []string{"origin.test"}, 1000
			})})
			s.plugins["f"].client = fetchPlugin(rawOrigin(t, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nxyz"), 1000).client
			var hits atomic.Int64
			got := serve(s.Middleware("f")(echoNext(&hits)), httptest.NewRequest(http.MethodGet, "/", nil))
			if got.Code != http.StatusServiceUnavailable || got.Header().Get("Retry-After") != "1" {
				t.Fatalf("status %d", got.Code)
			}
			if _, _, _, panics, _ := hooks.snapshot(); panics != 0 || hits.Load() != 0 {
				t.Fatalf("panics=%d origin=%d", panics, hits.Load())
			}
			assertDrained(t, gov)
		})
	}
}

// holdingTransport answers at once but keeps the request body open until
// release is closed, as a transport that closes request bodies late does.
type holdingTransport struct {
	release chan struct{}
	done    chan struct{}
	held    chan io.ReadCloser
}

func newHoldingTransport() *holdingTransport {
	return &holdingTransport{release: make(chan struct{}), done: make(chan struct{}), held: make(chan io.ReadCloser, 1)}
}

func (h *holdingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	h.held <- r.Body
	go func() {
		<-h.release
		_ = r.Body.Close()
		close(h.done)
	}()
	return &http.Response{StatusCode: 200, ContentLength: 2, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
}

// assertRevoked checks that a request-body reader the transport still holds
// can no longer reach the refunded copy.
func assertRevoked(t *testing.T, body io.ReadCloser) {
	t.Helper()
	if n, err := body.Read(make([]byte, 8)); n != 0 || err == nil {
		t.Fatalf("held reader read %d bytes, err %v, after the copy was refunded", n, err)
	}
	r := body.(*fetchBodyReader)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.r != nil || (r.fb != nil && r.fb.data != nil) {
		t.Fatal("held reader still references the refunded copy")
	}
}

// TestFetchBodyHeldPastGraceIsRevokedBeforeRefund: a request copy the
// transport still holds after the grace is revoked, so no reader can reach
// it, and only then refunded, before the host call returns. A late close
// afterwards is harmless.
func TestFetchBodyHeldPastGraceIsRevokedBeforeRefund(t *testing.T) {
	old := fetchBodyCloseGrace
	fetchBodyCloseGrace = 10 * time.Millisecond
	t.Cleanup(func() { fetchBodyCloseGrace = old })
	g := testGovernor(t)
	lease, ctx, _, _ := g.AcquireLease(t.Context())
	defer lease.Close()
	inv := &invocation{lease: lease}
	ht := newHoldingTransport()
	p := fetchPlugin("unused", 1000)
	p.client = &http.Client{Transport: ht}
	body, err := copyGuest(inv.chargeFn(), []byte(strings.Repeat("q", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.doFetch(ctx, inv, "POST", "http://origin.test/", body); err != nil {
		t.Fatal(err)
	}
	// Only the 3-byte response remains; the held copy is revoked and refunded.
	if got := g.Snapshot().Usage[inspection.BufferBytes]; got != 3 {
		t.Fatalf("live %d after the call, want only the response", got)
	}
	assertRevoked(t, <-ht.held)
	close(ht.release)
	<-ht.done
	inv.endCall()
	if got := g.Snapshot().Usage[inspection.BufferBytes]; got != 0 {
		t.Fatalf("leaked %d", got)
	}
}

// TestFetchBodyHeldAtCancelIsRevoked: cancelling the request while the
// transport holds the copy ends the wait at once; the copy is revoked and
// refunded before the call returns, so the lease close that follows finds
// nothing reachable still charged.
func TestFetchBodyHeldAtCancelIsRevoked(t *testing.T) {
	old := fetchBodyCloseGrace
	fetchBodyCloseGrace = time.Hour
	t.Cleanup(func() { fetchBodyCloseGrace = old })
	g := testGovernor(t)
	lease, leaseCtx, _, _ := g.AcquireLease(t.Context())
	ctx, cancel := context.WithCancel(leaseCtx)
	inv := &invocation{lease: lease}
	ht := newHoldingTransport()
	defer close(ht.release)
	p := fetchPlugin("unused", 1000)
	p.client = &http.Client{Transport: ht}
	body, err := copyGuest(inv.chargeFn(), []byte(strings.Repeat("q", 64)))
	if err != nil {
		t.Fatal(err)
	}
	type result struct{ err error }
	res := make(chan result, 1)
	go func() {
		_, _, err := p.doFetch(ctx, inv, "POST", "http://origin.test/", body)
		res <- result{err}
	}()
	held := <-ht.held
	cancel()
	select {
	case r := <-res:
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			t.Fatalf("doFetch: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not end the wait for the held copy")
	}
	inv.endCall()
	if got := g.Snapshot().Usage[inspection.BufferBytes]; got != 0 {
		t.Fatalf("live %d after the call: the held copy must be revoked and refunded", got)
	}
	assertRevoked(t, held)
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if got := g.Snapshot().Usage[inspection.BufferBytes]; got != 0 {
		t.Fatalf("lease close left %d", got)
	}
}

// retainingTransport reads the request body to EOF and closes it, as a
// well-behaved transport does, but keeps the request itself: its closed body
// reader and its GetBody closure outlive the call.
type retainingTransport struct{ kept chan *http.Request }

func (rt *retainingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	rt.kept <- r
	return &http.Response{StatusCode: 200, ContentLength: 2, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
}

// TestFetchBodyClosedNormallyIsDetachedBeforeRefund: when every reader
// closes in time the copy is still detached before it is refunded, so a
// transport retaining the closed reader, the request or its GetBody closure
// cannot keep the refunded array reachable.
func TestFetchBodyClosedNormallyIsDetachedBeforeRefund(t *testing.T) {
	g := testGovernor(t)
	lease, ctx, _, _ := g.AcquireLease(t.Context())
	defer lease.Close()
	inv := &invocation{lease: lease}
	rt := &retainingTransport{kept: make(chan *http.Request, 1)}
	p := fetchPlugin("unused", 1000)
	p.client = &http.Client{Transport: rt}
	body, err := copyGuest(inv.chargeFn(), []byte(strings.Repeat("q", 64)))
	if err != nil {
		t.Fatal(err)
	}
	copyArray := weak.Make(&body.b[0])
	if _, _, err := p.doFetch(ctx, inv, "POST", "http://origin.test/", body); err != nil {
		t.Fatal(err)
	}
	if got := g.Snapshot().Usage[inspection.BufferBytes]; got != 3 {
		t.Fatalf("live %d after the call, want only the response", got)
	}
	kept := <-rt.kept
	closedReader := kept.Body.(*fetchBodyReader)
	closedReader.mu.Lock()
	if closedReader.r != nil || closedReader.fb != nil {
		closedReader.mu.Unlock()
		t.Fatal("closed reader still references the copy")
	}
	closedReader.mu.Unlock()
	if _, err := kept.GetBody(); err == nil {
		t.Fatal("GetBody replayed a refunded copy")
	}
	for range 5 {
		runtime.GC()
		if copyArray.Value() == nil {
			break
		}
	}
	if copyArray.Value() != nil {
		t.Fatal("refunded request copy is still reachable from the retained request")
	}
	runtime.KeepAlive(kept)
	runtime.KeepAlive(closedReader)
	inv.endCall()
	if got := g.Snapshot().Usage[inspection.BufferBytes]; got != 0 {
		t.Fatalf("leaked %d", got)
	}
}

// TestFetchBodyRefusesReplayAfterSeal: GetBody cannot hand out a new reader
// once the call has sealed the copy, and a closed reader cannot be read.
func TestFetchBodyRefusesReplayAfterSeal(t *testing.T) {
	fb := &fetchBody{data: []byte("abc"), idle: make(chan struct{})}
	r := fb.open()
	_ = r.Close()
	_ = r.Close()
	if _, err := r.Read(make([]byte, 1)); err == nil {
		t.Fatal("read from a closed reader")
	}
	if !fb.waitClosed(time.Second, nil) {
		t.Fatal("no open readers, yet not idle")
	}
	if _, err := fb.reopen(); err == nil {
		t.Fatal("replay after seal")
	}
	// A revoked copy serves neither its open readers nor a replay.
	fb2 := &fetchBody{data: []byte("abc"), idle: make(chan struct{})}
	held := fb2.open()
	fb2.revoke()
	assertRevoked(t, held)
	if _, err := fb2.reopen(); err == nil {
		t.Fatal("replay after revoke")
	}
	_ = held.Close()
	if !fb2.waitClosed(time.Second, nil) {
		t.Fatal("closing the revoked reader did not leave the body idle")
	}
}
