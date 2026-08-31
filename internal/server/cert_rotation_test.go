// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

// tlsCfgFor builds a minimal TLS-enabled *config.Config for addr using the
// given cert/key pair and server names.
func tlsCfgFor(addr, cert, key string, names ...string) *config.Config {
	return &config.Config{
		Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
		Servers: []config.ServerConfig{{
			Listen:      addr,
			ServerNames: names,
			TLS:         &config.TLSConfig{Enabled: true, Cert: cert, Key: key},
			Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
		}},
	}
}

// TestTLSIdentityFingerprintDetectsContentRotation (#100) proves the
// mechanism prepareCertRotation relies on: rotating a cert/key pair's file
// content in place, with the configured path unchanged, changes the identity
// fingerprint. This is the same-path detection listenerBindFingerprint used
// to provide before #100; it now lives here instead.
func TestTLSIdentityFingerprintDetectsContentRotation(t *testing.T) {
	dir := t.TempDir()
	cert1, key1 := writeSelfSigned(t, dir, "cert1", "a.example.com")
	cert2, key2 := writeSelfSigned(t, dir, "cert2", "a.example.com")

	bindings1, _, ok := tlsBindingsForAddr(tlsCfgFor(":8443", cert1, key1, "a.example.com").Servers, ":8443")
	if !ok {
		t.Fatal("expected TLS bindings")
	}
	bindings2, _, _ := tlsBindingsForAddr(tlsCfgFor(":8443", cert2, key2, "a.example.com").Servers, ":8443")

	if tlsIdentityFingerprint(bindings1) == tlsIdentityFingerprint(bindings2) {
		t.Fatal("rotating cert/key content did not change the identity fingerprint")
	}

	bindings1b, _, _ := tlsBindingsForAddr(tlsCfgFor(":8443", cert1, key1, "a.example.com").Servers, ":8443")
	if tlsIdentityFingerprint(bindings1) != tlsIdentityFingerprint(bindings1b) {
		t.Fatal("identical cert/key content produced different identity fingerprints")
	}
}

// boundTLSEntry builds a real listenerEntry for addr, as buildListenerEntry
// would, without starting to serve — enough to exercise prepareCertRotation
// and certRotationComponent against a realistic entry.provider.
func boundTLSEntry(t *testing.T, s *Server, cfg *config.Config, addr string) *listenerEntry {
	t.Helper()
	entry, err := s.buildListenerEntry(addr, cfg)
	if err != nil {
		t.Fatalf("buildListenerEntry: %v", err)
	}
	t.Cleanup(func() { _ = entry.ln.Close() })
	return entry
}

func TestPrepareCertRotationSkipsUnchangedIdentity(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeSelfSigned(t, dir, "cert", "a.example.com")
	addr := freePort(t)
	cfg := tlsCfgFor(addr, cert, key, "a.example.com")

	s := &Server{cfg: cfg, log: quietLogger(), listeners: map[string]*listenerEntry{}}
	s.listeners[addr] = boundTLSEntry(t, s, cfg, addr)

	comp, err := s.prepareCertRotation(cfg)
	if err != nil {
		t.Fatalf("prepareCertRotation: %v", err)
	}
	if comp != nil {
		t.Fatal("expected no candidate component when the identity is unchanged")
	}
}

func TestPrepareCertRotationSkipsACMEAddress(t *testing.T) {
	addr := freePort(t)
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: addr,
			TLS: &config.TLSConfig{
				Enabled: true,
				ACME:    &config.ACMEConfig{Enabled: true, Email: "ops@example.com", Domains: []string{"a.example.com"}},
			},
		}},
	}
	s := &Server{cfg: cfg, log: quietLogger(), listeners: map[string]*listenerEntry{
		addr: {addr: addr, provider: &dynamicCertProvider{}},
	}}

	comp, err := s.prepareCertRotation(cfg)
	if err != nil {
		t.Fatalf("prepareCertRotation: %v", err)
	}
	if comp != nil {
		t.Fatal("expected ACME addresses to be skipped; they obtain certificates at handshake time")
	}
}

func TestPrepareCertRotationSkipsNewlyAddedAddress(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeSelfSigned(t, dir, "cert", "a.example.com")
	addr := freePort(t)
	cfg := tlsCfgFor(addr, cert, key, "a.example.com")

	// No entry for addr yet: buildListenerEntry will bind it fresh.
	s := &Server{cfg: cfg, log: quietLogger(), listeners: map[string]*listenerEntry{}}

	comp, err := s.prepareCertRotation(cfg)
	if err != nil {
		t.Fatalf("prepareCertRotation: %v", err)
	}
	if comp != nil {
		t.Fatal("expected a newly added address to be skipped; it binds fresh")
	}
}

func TestPrepareCertRotationRejectsMalformedCandidateBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	cert1, key1 := writeSelfSigned(t, dir, "cert1", "a.example.com")
	addr := freePort(t)
	base := tlsCfgFor(addr, cert1, key1, "a.example.com")

	s := &Server{cfg: base, log: quietLogger(), listeners: map[string]*listenerEntry{}}
	entry := boundTLSEntry(t, s, base, addr)
	s.listeners[addr] = entry
	originalFP := entry.certFingerprint

	broken := tlsCfgFor(addr, dir+"/does-not-exist.pem", dir+"/does-not-exist-key.pem", "a.example.com")
	comp, err := s.prepareCertRotation(broken)
	if err == nil {
		t.Fatal("expected an error for an unloadable candidate cert/key pair")
	}
	if comp != nil {
		t.Fatal("expected no component when the candidate fails to load")
	}
	if entry.certFingerprint != originalFP {
		t.Fatal("a malformed candidate must never mutate the live entry's fingerprint")
	}
	// The live provider must still answer with the original certificate.
	got, err := entry.provider.GetCertificate(&tls.ClientHelloInfo{ServerName: "a.example.com"})
	if err != nil {
		t.Fatalf("live provider broken after a rejected candidate: %v", err)
	}
	if got == nil {
		t.Fatal("expected the live provider to still return the original certificate")
	}
}

func TestCertRotationComponentCommitSwapsProviderAndFingerprint(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := writeSelfSigned(t, dir, "a", "a.example.com")
	certB, keyB := writeSelfSigned(t, dir, "b", "a.example.com")

	addr := freePort(t)
	s := &Server{cfg: tlsCfgFor(addr, certA, keyA, "a.example.com"), log: quietLogger(), listeners: map[string]*listenerEntry{}}
	entry := boundTLSEntry(t, s, s.cfg, addr)
	oldFP := entry.certFingerprint

	bindingsB, _, _ := tlsBindingsForAddr(tlsCfgFor(addr, certB, keyB, "a.example.com").Servers, addr)
	newProvider, err := newFileCertProvider(bindingsB)
	if err != nil {
		t.Fatalf("newFileCertProvider: %v", err)
	}
	newFP := tlsIdentityFingerprint(bindingsB)
	comp := &certRotationComponent{swaps: []certProviderSwap{{entry: entry, provider: newProvider, newFP: newFP}}}

	if got := comp.component(); got != ComponentStaticCertificates {
		t.Fatalf("component() = %v, want ComponentStaticCertificates", got)
	}
	if ret := comp.commit(); ret != nil {
		t.Fatal("expected no retirement: immutable certificate providers need no active close")
	}

	if entry.certFingerprint != newFP || entry.certFingerprint == oldFP {
		t.Fatalf("certFingerprint = %q, want the new identity %q", entry.certFingerprint, newFP)
	}
	cert, err := entry.provider.GetCertificate(&tls.ClientHelloInfo{ServerName: "a.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	wantLeaf, _ := x509.ParseCertificate(cert.Certificate[0])
	bCert, _, _ := loadLeafForTest(t, certB, keyB)
	if wantLeaf.SerialNumber.Cmp(bCert.SerialNumber) != 0 {
		t.Fatal("expected the live provider to serve certificate B after commit")
	}
}

func TestCertRotationComponentAbortDoesNotMutateLiveState(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := writeSelfSigned(t, dir, "a", "a.example.com")
	certB, keyB := writeSelfSigned(t, dir, "b", "a.example.com")

	addr := freePort(t)
	s := &Server{cfg: tlsCfgFor(addr, certA, keyA, "a.example.com"), log: quietLogger(), listeners: map[string]*listenerEntry{}}
	entry := boundTLSEntry(t, s, s.cfg, addr)
	oldFP := entry.certFingerprint

	bindingsB, _, _ := tlsBindingsForAddr(tlsCfgFor(addr, certB, keyB, "a.example.com").Servers, addr)
	newProvider, _ := newFileCertProvider(bindingsB)
	comp := &certRotationComponent{swaps: []certProviderSwap{{entry: entry, provider: newProvider, newFP: tlsIdentityFingerprint(bindingsB)}}}

	comp.abort()

	if entry.certFingerprint != oldFP {
		t.Fatal("abort must never change the live entry's fingerprint")
	}
	cert, err := entry.provider.GetCertificate(&tls.ClientHelloInfo{ServerName: "a.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	aCert, _, _ := loadLeafForTest(t, certA, keyA)
	if leaf.SerialNumber.Cmp(aCert.SerialNumber) != 0 {
		t.Fatal("abort must leave the original certificate (A) live")
	}
}

// loadLeafForTest parses the leaf certificate at certPath for a serial-number
// comparison in tests; keyPath is accepted for symmetry with writeSelfSigned
// call sites but unused.
func loadLeafForTest(t *testing.T, certPath, keyPath string) (*x509.Certificate, string, string) {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load %s/%s: %v", certPath, keyPath, err)
	}
	leaf := pair.Leaf
	if leaf == nil {
		leaf, err = x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			t.Fatalf("parse leaf: %v", err)
		}
	}
	return leaf, certPath, keyPath
}

// dialLeaf opens a fresh TLS connection to addr and returns the leaf
// certificate the server presented, closing the connection immediately.
func dialLeaf(t *testing.T, addr string) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, ServerName: "a.example.com"})
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("no peer certificate presented")
	}
	return state.PeerCertificates[0]
}

// httpOverConn issues one HTTP/1.1 GET over an already-established
// connection (TLS or plain) and returns the response body, proving the
// connection is still usable end to end.
func httpOverConn(t *testing.T, conn *tls.Conn, host string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://"+host+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Close = false
	if err := req.Write(conn); err != nil {
		t.Fatalf("write request over existing connection: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read response over existing connection: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body over existing connection: %v", err)
	}
	return string(body)
}

// TestReloadRotatesLiveCertificateWithoutRebind is the real end-to-end proof
// for #100's acceptance criteria: a cert/key change on a retained TLS
// address applies live, new connections see the candidate certificate, and a
// connection already established under the old certificate keeps working
// across the rotation.
func TestReloadRotatesLiveCertificateWithoutRebind(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := writeSelfSigned(t, dir, "a", "a.example.com")
	certB, keyB := writeSelfSigned(t, dir, "b", "a.example.com")
	addr := freePort(t)

	initial := tlsCfgFor(addr, certA, keyA, "a.example.com")
	src := &stubSource{}
	src.set(initial, nil)
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
		return map[string]http.Handler{addr: h}, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(initial, nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan ReloadRequest, 2)
	go func() { _ = srv.Run(ctx, reload, redact.EmptyState()) }()

	// Wait for the initial TLS listener to accept, then hold one connection
	// open across the rotation below.
	var held *tls.Conn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, ServerName: "a.example.com"})
		if err == nil {
			held = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if held == nil {
		t.Fatal("TLS listener never became reachable")
	}
	defer held.Close()

	leafBefore := held.ConnectionState().PeerCertificates[0]
	certAParsed, _, _ := loadLeafForTest(t, certA, keyA)
	if leafBefore.SerialNumber.Cmp(certAParsed.SerialNumber) != 0 {
		t.Fatal("initial connection did not observe certificate A")
	}
	if body := httpOverConn(t, held, addr); body != "ok" {
		t.Fatalf("held connection body before rotation = %q", body)
	}

	next := tlsCfgFor(addr, certB, keyB, "a.example.com")
	src.set(next, nil)
	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{ID: "rotate", Source: ReloadSourceFileWatch, Result: resultCh}
	result := <-resultCh
	if result.Outcome != ReloadAppliedLive {
		t.Fatalf("reload outcome = %+v", result)
	}

	// A fresh connection after Publish must observe certificate B.
	certBParsed, _, _ := loadLeafForTest(t, certB, keyB)
	leafAfter := dialLeaf(t, addr)
	if leafAfter.SerialNumber.Cmp(certBParsed.SerialNumber) != 0 {
		t.Fatal("new connection after rotation did not observe certificate B")
	}

	// The connection established before rotation must still be usable: no
	// certificate change is retroactive to an already-completed handshake,
	// and ordinary certificate rotation never drops existing connections.
	if body := httpOverConn(t, held, addr); body != "ok" {
		t.Fatalf("held connection body after rotation = %q", body)
	}
}

// startTestServerWithTLS builds and starts a real *Server bound to a TLS
// listener at addr, serving initial, and returns the server, its reload
// channel, and the stubSource backing it (so a caller can mutate it before
// sending a file-watch-style ReloadRequest to drive a real rotation). The
// server stops when the test ends (t.Cleanup cancels its context).
func startTestServerWithTLS(t *testing.T, addr string, initial *config.Config) (*Server, chan ReloadRequest, *stubSource) {
	t.Helper()
	src := &stubSource{}
	src.set(initial, nil)
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
		return map[string]http.Handler{addr: h}, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(initial, nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reload := make(chan ReloadRequest, 2)
	go func() { _ = srv.Run(ctx, reload, redact.EmptyState()) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, ServerName: "a.example.com"})
		if err == nil {
			_ = c.Close()
			return srv, reload, src
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("TLS listener never became reachable")
	return nil, nil, nil
}

// TestLiveCertSummariesDoesNotReReadDiskAfterPublish (#100 acceptance
// criterion 6) is the residual's exact scenario: a certificate file is
// rewritten out of band, at its already-configured path, with no
// configuration change and therefore no reload. LiveCertSummaries must keep
// reporting the certificate actually installed in the live provider, not
// whatever bytes now sit at that path.
func TestLiveCertSummariesDoesNotReReadDiskAfterPublish(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := writeSelfSigned(t, dir, "a", "a.example.com")
	certB, keyB := writeSelfSigned(t, dir, "b", "a.example.com")
	addr := freePort(t)

	srv, _, _ := startTestServerWithTLS(t, addr, tlsCfgFor(addr, certA, keyA, "a.example.com"))

	summaries := srv.LiveCertSummaries()
	if len(summaries) != 1 {
		t.Fatalf("summaries = %+v, want exactly 1", summaries)
	}
	certAParsed, _, _ := loadLeafForTest(t, certA, keyA)
	if summaries[0].Subject != certAParsed.Subject.CommonName {
		t.Fatalf("subject = %q, want %q (certificate A)", summaries[0].Subject, certAParsed.Subject.CommonName)
	}

	// Rewrite the SAME configured path out of band with certificate B's
	// bytes, without going through a reload at all.
	bBytes, err := os.ReadFile(certB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certA, bBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	kBytes, err := os.ReadFile(keyB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyA, kBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	after := srv.LiveCertSummaries()
	if len(after) != 1 {
		t.Fatalf("summaries after out-of-band rewrite = %+v, want exactly 1", after)
	}
	certBParsed, _, _ := loadLeafForTest(t, certB, keyB)
	if after[0].Subject == certBParsed.Subject.CommonName {
		t.Fatal("LiveCertSummaries reported the out-of-band on-disk bytes instead of the live installed certificate")
	}
	if after[0].Subject != certAParsed.Subject.CommonName {
		t.Fatalf("subject after out-of-band rewrite = %q, want the still-live certificate A (%q)", after[0].Subject, certAParsed.Subject.CommonName)
	}
}

// TestLiveCertSummariesReflectsAPublishedRotation proves the complementary
// half: once a rotation genuinely goes through Publish, LiveCertSummaries
// does advance to the new certificate.
func TestLiveCertSummariesReflectsAPublishedRotation(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := writeSelfSigned(t, dir, "a", "a.example.com")
	certB, keyB := writeSelfSigned(t, dir, "b", "a.example.com")
	addr := freePort(t)

	srv, reload, src := startTestServerWithTLS(t, addr, tlsCfgFor(addr, certA, keyA, "a.example.com"))

	certAParsed, _, _ := loadLeafForTest(t, certA, keyA)
	if got := srv.LiveCertSummaries(); len(got) != 1 || got[0].Subject != certAParsed.Subject.CommonName {
		t.Fatalf("summaries before rotation = %+v, want certificate A", got)
	}

	src.set(tlsCfgFor(addr, certB, keyB, "a.example.com"), nil)
	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{ID: "rotate", Source: ReloadSourceFileWatch, Result: resultCh}
	if result := <-resultCh; result.Outcome != ReloadAppliedLive {
		t.Fatalf("reload outcome = %+v", result)
	}

	certBParsed, _, _ := loadLeafForTest(t, certB, keyB)
	got := srv.LiveCertSummaries()
	if len(got) != 1 || got[0].Subject != certBParsed.Subject.CommonName {
		t.Fatalf("summaries after rotation = %+v, want certificate B", got)
	}
}

// TestLiveCertSummariesReportsACME proves the ACME branch never consults a
// listener's provider (which has no Summaries method): it is answered
// entirely from configuration, exactly like InspectCerts' ACME marker.
func TestLiveCertSummariesReportsACME(t *testing.T) {
	addr := freePort(t)
	cfg := &config.Config{
		Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
		Servers: []config.ServerConfig{{
			Listen:      addr,
			ServerNames: []string{"acme.example.com"},
			TLS: &config.TLSConfig{
				Enabled: true,
				ACME:    &config.ACMEConfig{Enabled: true, Email: "ops@example.com", Domains: []string{"acme.example.com"}},
			},
		}},
	}
	s := &Server{log: quietLogger(), listeners: map[string]*listenerEntry{}}
	s.runtimeState.Store(&runtimeState{EffectiveConfig: cfg})

	got := s.LiveCertSummaries()
	if len(got) != 1 || got[0].Source != "acme" {
		t.Fatalf("summaries = %+v, want one ACME marker", got)
	}
	if len(got[0].ServerNames) != 1 || got[0].ServerNames[0] != "acme.example.com" {
		t.Fatalf("server names = %v, want [acme.example.com]", got[0].ServerNames)
	}
}

// TestLiveCertSummariesNilEffectiveConfig covers a Server with no published
// runtime state at all (never New()'d / never reloaded): LiveSnapshot's
// EffectiveConfig is nil, and LiveCertSummaries must return nil rather than
// panic.
func TestLiveCertSummariesNilEffectiveConfig(t *testing.T) {
	var s Server
	if got := s.LiveCertSummaries(); got != nil {
		t.Fatalf("summaries = %+v, want nil", got)
	}
}

// TestLiveCertSummariesSkipsNonTLSAndUnboundAddresses covers the two
// defensive skips between the ACME check and the live-provider read: a
// configured address with no TLS at all, and a TLS address the config
// describes but that has no corresponding bound listenerEntry yet (a
// narrow, otherwise-unreachable state in production, since a live TLS
// address always has an entry with a provider).
func TestLiveCertSummariesSkipsNonTLSAndUnboundAddresses(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := writeSelfSigned(t, dir, "a", "a.example.com")
	plainAddr := freePort(t)
	tlsAddr := freePort(t)
	unboundAddr := freePort(t)

	cfg := &config.Config{
		Servers: []config.ServerConfig{
			{Listen: plainAddr},
			{Listen: tlsAddr, ServerNames: []string{"a.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: certA, Key: keyA}},
			{Listen: unboundAddr, ServerNames: []string{"b.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: certA, Key: keyA}},
		},
	}
	s := &Server{log: quietLogger(), listeners: map[string]*listenerEntry{}}
	s.runtimeState.Store(&runtimeState{EffectiveConfig: cfg})
	s.listeners[tlsAddr] = boundTLSEntry(t, s, cfg, tlsAddr)
	// unboundAddr deliberately has no entry in s.listeners.

	got := s.LiveCertSummaries()
	if len(got) != 1 {
		t.Fatalf("summaries = %+v, want exactly 1 (only the bound TLS address)", got)
	}
	if got[0].Subject != "a" {
		t.Fatalf("subject = %q, want a", got[0].Subject)
	}
}

// TestLiveCertSummariesSkipsANilHolder covers a bound TLS address whose
// dynamicCertProvider was constructed but never had a provider installed —
// a state buildListenerEntry never leaves behind in production, but one
// LiveCertSummaries must still not panic against.
func TestLiveCertSummariesSkipsANilHolder(t *testing.T) {
	addr := freePort(t)
	cfg := &config.Config{
		Servers: []config.ServerConfig{{Listen: addr, TLS: &config.TLSConfig{Enabled: true, Cert: "unused", Key: "unused"}}},
	}
	s := &Server{log: quietLogger(), listeners: map[string]*listenerEntry{
		addr: {addr: addr, provider: &dynamicCertProvider{}},
	}}
	s.runtimeState.Store(&runtimeState{EffectiveConfig: cfg})

	if got := s.LiveCertSummaries(); len(got) != 0 {
		t.Fatalf("summaries = %+v, want none (no provider installed yet)", got)
	}
}

// TestAcmeCertSummariesForAddrSkipsNonMatchingServers proves the skip branch
// in the loop: a server on a different address, and a non-ACME server on the
// same address, are both excluded.
func TestAcmeCertSummariesForAddrSkipsNonMatchingServers(t *testing.T) {
	addr := freePort(t)
	other := freePort(t)
	servers := []config.ServerConfig{
		{Listen: other, TLS: &config.TLSConfig{Enabled: true, ACME: &config.ACMEConfig{Enabled: true}}},
		{Listen: addr, TLS: &config.TLSConfig{Enabled: true}}, // no ACME on this one.
		{Listen: addr, ServerNames: []string{"acme.example.com"}, TLS: &config.TLSConfig{
			Enabled: true, ACME: &config.ACMEConfig{Enabled: true},
		}},
	}
	got := acmeCertSummariesForAddr(servers, addr)
	if len(got) != 1 || got[0].ServerNames[0] != "acme.example.com" {
		t.Fatalf("summaries = %+v, want exactly the one matching ACME server on addr", got)
	}
}
