// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

// mtlsReloadFixture runs a real Server with a require-mode client-auth TLS
// listener whose CA and CRL files the test rewrites in place.
type mtlsReloadFixture struct {
	t       *testing.T
	addr    string
	cfg     *config.Config
	src     *stubSource
	reload  chan ReloadRequest
	crlPath string
	ca      *caFixture
	mu      sync.Mutex
	crlNext map[string]time.Time
}

func newMTLSReloadFixture(t *testing.T, revoked ...int64) *mtlsReloadFixture {
	return newMTLSReloadFixtureWithHandler(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte("ok")) }), revoked...)
}

func newMTLSReloadFixtureWithHandler(t *testing.T, handler http.Handler, revoked ...int64) *mtlsReloadFixture {
	t.Helper()
	dir := t.TempDir()
	ca := newCA(t)
	caFile := writePEM(t, dir, "ca.pem", ca.pem)
	f := &mtlsReloadFixture{t: t, ca: ca, addr: freePort(t), crlPath: ca.writeCRL(t, dir, "clients.crl", revoked...)}
	cert, key := writeSelfSigned(t, dir, "srv", "a.example.com")
	f.cfg = tlsCfgFor(f.addr, cert, key, "a.example.com")
	f.cfg.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "require", CAFile: caFile, CRLFile: f.crlPath}

	f.src = &stubSource{}
	f.src.set(f.snapshot(), nil)
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		return map[string]http.Handler{f.addr: handler}, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(f.snapshot(), nil, lifecycle.Fingerprint{}, quietLogger(), factory, f.src, func(context.Context, *config.Config) error { return nil })
	srv.CRLNextUpdateHook = func(m map[string]time.Time) {
		f.mu.Lock()
		f.crlNext = m
		f.mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.reload = make(chan ReloadRequest, 1)
	go func() { _ = srv.Run(ctx, f.reload, redact.EmptyState()) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		ready := f.crlNext != nil
		f.mu.Unlock()
		if c, err := net.DialTimeout("tcp", f.addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			if ready {
				return f
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("TLS listener never became reachable")
	return nil
}

func TestLongLivedMTLSStreamSurvivesClientRevocation(t *testing.T) {
	for _, protocol := range []string{"http1", "http2"} {
		t.Run(protocol, func(t *testing.T) {
			release := make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, "data: before\n\n")
				_ = http.NewResponseController(writer).Flush()
				select {
				case <-release:
				case <-request.Context().Done():
					return
				}
				_, _ = io.WriteString(writer, "data: after revocation\n\n")
				_ = http.NewResponseController(writer).Flush()
			})
			fixture := newMTLSReloadFixtureWithHandler(t, handler)
			_, victim := fixture.ca.clientCert(t, "long-lived", 19, nil, nil)
			transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "a.example.com", Certificates: []tls.Certificate{victim}}, ForceAttemptHTTP2: protocol == "http2"}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			response, err := client.Get("https://" + fixture.addr + "/events")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			wantMajor := 1
			if protocol == "http2" {
				wantMajor = 2
			}
			if response.ProtoMajor != wantMajor {
				t.Fatalf("protocol=%s, want HTTP/%d", response.Proto, wantMajor)
			}
			reader := bufio.NewReader(response.Body)
			if line, err := reader.ReadString('\n'); err != nil || line != "data: before\n" {
				t.Fatalf("initial frame=%q err=%v", line, err)
			}
			fixture.ca.writeCRL(t, "", fixture.crlPath, 19)
			if reload := fixture.doReload("revoke-live-stream"); reload.Outcome != ReloadAppliedLive {
				t.Fatalf("CRL reload=%+v", reload)
			}
			if _, err := fixture.get(victim, nil); err == nil {
				t.Fatal("new handshake accepted revoked certificate")
			}
			releaseOnce.Do(func() { close(release) })
			if _, err := reader.ReadString('\n'); err != nil {
				t.Fatal(err)
			}
			if line, err := reader.ReadString('\n'); err != nil || line != "data: after revocation\n" {
				t.Fatalf("established stream frame=%q err=%v", line, err)
			}
		})
	}
}

// snapshot copies the parts of f.cfg a test mutates, so the running server
// never shares them with the test goroutine.
func (f *mtlsReloadFixture) snapshot() *config.Config {
	c := *f.cfg
	c.Servers = append([]config.ServerConfig(nil), f.cfg.Servers...)
	tlsCopy := *c.Servers[0].TLS
	if tlsCopy.ClientAuth != nil {
		ca := *tlsCopy.ClientAuth
		tlsCopy.ClientAuth = &ca
	}
	c.Servers[0].TLS = &tlsCopy
	return &c
}

func (f *mtlsReloadFixture) doReload(id string) ReloadResult {
	f.t.Helper()
	f.src.set(f.snapshot(), nil)
	res := make(chan ReloadResult, 1)
	f.reload <- ReloadRequest{ID: id, Source: ReloadSourceFileWatch, Result: res}
	select {
	case r := <-res:
		return r
	case <-time.After(10 * time.Second):
		f.t.Fatal("reload did not finish")
		return ReloadResult{}
	}
}

// get performs one HTTPS request presenting cert, reusing cache so a second call
// can resume. TLS 1.3 reports a rejected client certificate on the first read.
func (f *mtlsReloadFixture) get(cert tls.Certificate, cache tls.ClientSessionCache) (resumed bool, err error) {
	cfg := &tls.Config{InsecureSkipVerify: true, ServerName: "a.example.com", ClientSessionCache: cache}
	if cert.Certificate != nil {
		cfg.Certificates = []tls.Certificate{cert}
	}
	conn, err := tls.Dial("tcp", f.addr, cfg)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: a.example.com\r\nConnection: close\r\n\r\n"); err != nil {
		return false, err
	}
	body, err := io.ReadAll(conn)
	if err != nil && !errors.Is(err, io.EOF) {
		return conn.ConnectionState().DidResume, err
	}
	if len(body) == 0 {
		return conn.ConnectionState().DidResume, errors.New("empty response")
	}
	return conn.ConnectionState().DidResume, nil
}

// TestReloadRefreshesClientCRLWithoutRestart is #486's end-to-end proof: an
// in-place CRL rewrite plus an ordinary reload revokes a client certificate
// for new and resumed handshakes, a broken CRL is refused while the previous
// policy keeps serving, and lifting the revocation takes effect the same way.
func TestReloadRefreshesClientCRLWithoutRestart(t *testing.T) {
	f := newMTLSReloadFixture(t)
	_, victim := f.ca.clientCert(t, "victim", 5, nil, nil)
	_, other := f.ca.clientCert(t, "other", 6, nil, nil)

	var next time.Time
	var ok bool
	for deadline := time.Now().Add(3 * time.Second); !ok && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		f.mu.Lock()
		next, ok = f.crlNext[f.addr]
		f.mu.Unlock()
	}
	if !ok || !next.After(time.Now()) {
		t.Fatalf("startup CRL next_update = %v (reported %v), want a future time", next, ok)
	}

	cache := tls.NewLRUClientSessionCache(4)
	if _, err := f.get(victim, cache); err != nil {
		t.Fatalf("victim before revocation: %v", err)
	}
	if resumed, err := f.get(victim, cache); err != nil || !resumed {
		t.Fatalf("victim resumption before revocation: resumed=%v err=%v", resumed, err)
	}

	f.ca.writeCRL(t, "", f.crlPath, 5)
	if r := f.doReload("revoke"); r.Outcome != ReloadAppliedLive {
		t.Fatalf("revoking reload = %+v, want applied_live (no restart)", r)
	}
	if _, err := f.get(victim, nil); err == nil {
		t.Fatal("revoked certificate still accepted on a full handshake")
	}
	if _, err := f.get(victim, cache); err == nil {
		t.Fatal("revoked certificate still accepted by resuming a pre-revocation ticket")
	}
	if _, err := f.get(other, nil); err != nil {
		t.Fatalf("unrevoked certificate rejected: %v", err)
	}

	if err := os.WriteFile(f.crlPath, []byte("not a crl"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := f.doReload("broken"); r.Outcome != ReloadNotApplied {
		t.Fatalf("broken-CRL reload = %+v, want not_applied", r)
	}
	if _, err := f.get(victim, nil); err == nil {
		t.Fatal("a rejected reload dropped the live revocation")
	}
	if _, err := f.get(other, nil); err != nil {
		t.Fatalf("a rejected reload broke the live policy: %v", err)
	}

	f.ca.writeCRL(t, "", f.crlPath)
	if r := f.doReload("lift"); r.Outcome != ReloadAppliedLive {
		t.Fatalf("lifting reload = %+v", r)
	}
	if _, err := f.get(victim, nil); err != nil {
		t.Fatalf("certificate still rejected after its revocation was lifted: %v", err)
	}
	if r := f.doReload("same"); r.Outcome != ReloadNoChange {
		t.Fatalf("reload with unchanged CRL content = %s, want no_change", r.Outcome)
	}
}

// TestReloadEnablesClientAuthWithoutRestart: switching a live TLS listener from
// no client auth to require is a hot change.
func TestReloadEnablesClientAuthWithoutRestart(t *testing.T) {
	f := newMTLSReloadFixture(t)
	ca := f.cfg.Servers[0].TLS.ClientAuth
	f.cfg.Servers[0].TLS.ClientAuth = nil
	if r := f.doReload("off"); r.Outcome != ReloadAppliedLive {
		t.Fatalf("disabling reload = %+v", r)
	}
	if _, err := f.get(tls.Certificate{}, nil); err != nil {
		t.Fatalf("anonymous client rejected with client auth off: %v", err)
	}
	f.cfg.Servers[0].TLS.ClientAuth = ca
	if r := f.doReload("on"); r.Outcome != ReloadAppliedLive {
		t.Fatalf("enabling reload = %+v", r)
	}
	if _, err := f.get(tls.Certificate{}, nil); err == nil {
		t.Fatal("anonymous client accepted after require was enabled")
	}
}

// TestReloadRotatesClientCAWithoutRestart follows the documented CA rotation
// with in-place rewrites of ca_file (and the CRL the new CA signs): bundle old
// and new CA, then drop the old one, each applied by an ordinary reload.
func TestReloadRotatesClientCAWithoutRestart(t *testing.T) {
	f := newMTLSReloadFixture(t)
	caFile := f.cfg.Servers[0].TLS.ClientAuth.CAFile
	next := newNamedCA(t, "Next CA")
	_, oldClient := f.ca.clientCert(t, "old", 7, nil, nil)
	_, newClient := next.clientCert(t, "new", 8, nil, nil)

	if _, err := f.get(newClient, nil); err == nil {
		t.Fatal("a certificate from a CA that is not configured yet was accepted")
	}

	writePEM(t, "", caFile, append(append([]byte(nil), f.ca.pem...), next.pem...))
	if r := f.doReload("bundle"); r.Outcome != ReloadAppliedLive {
		t.Fatalf("bundling reload = %+v, want applied_live", r)
	}
	for name, cert := range map[string]tls.Certificate{"old": oldClient, "new": newClient} {
		if _, err := f.get(cert, nil); err != nil {
			t.Fatalf("%s-CA client rejected while both CAs are bundled: %v", name, err)
		}
	}

	writePEM(t, "", caFile, next.pem)
	next.writeCRL(t, "", f.crlPath)
	if r := f.doReload("drop-old"); r.Outcome != ReloadAppliedLive {
		t.Fatalf("dropping reload = %+v, want applied_live", r)
	}
	if _, err := f.get(oldClient, nil); err == nil {
		t.Fatal("old-CA client still accepted after the old CA was removed")
	}
	if _, err := f.get(newClient, nil); err != nil {
		t.Fatalf("new-CA client rejected after rotation: %v", err)
	}
}
