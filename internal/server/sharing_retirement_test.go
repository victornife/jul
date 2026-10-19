// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/middleware"
	"jul/internal/redact"
	"jul/internal/respwriter"
	"jul/internal/router"
	"jul/internal/upstream"
)

func buildTestRouterFactory(t *testing.T, streamHold <-chan struct{}, portalRelease <-chan struct{}) HandlerFactory {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		builders := map[string]router.Builder{
			router.ActionProxy: func(srv config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
				rid := ""
				if loc.RouteID != nil {
					rid = *loc.RouteID
				}
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, "data: start-"+rid+"\n\n")
					_ = http.NewResponseController(w).Flush()
					if r.URL.Query().Get("stream") == "1" {
						if rid == "portal" && portalRelease != nil {
							select {
							case <-portalRelease:
								_, _ = io.WriteString(w, "data: end-portal\n\n")
								_ = http.NewResponseController(w).Flush()
								return
							case <-r.Context().Done():
								return
							}
						}
						select {
						case <-streamHold:
							_, _ = io.WriteString(w, "data: end-"+rid+"\n\n")
							_ = http.NewResponseController(w).Flush()
						case <-r.Context().Done():
							return
						}
					}
				}), nil
			},
		}
		locMod := func(srv config.ServerConfig, loc config.LocationConfig) middleware.Middleware {
			reqCert := loc.RequireClientCert
			for _, s := range c.Servers {
				if s.Listen == srv.Listen && s.TLS != nil && s.TLS.Enabled && s.TLS.ClientAuth != nil {
					if strings.EqualFold(strings.TrimSpace(s.TLS.ClientAuth.Mode), "require") {
						reqCert = true
					}
				}
			}
			verifier, _ := BuildClientCertVerifier(c.Servers, srv.Listen)
			return middleware.ClientCertWithVerifier(reqCert, middleware.ForwardCertNone, verifier)
		}
		rtr, err := router.New(c, builders, nil, locMod, log)
		if err != nil {
			return nil, 0, nil, nil, err
		}
		out := make(map[string]http.Handler)
		for _, addr := range uniqueListenAddrs(c.Servers) {
			out[addr] = rtr.For(addr)
		}
		genID := uint64(time.Now().UnixNano())
		return out, genID, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
}

func TestSharedH2StreamRetirementAndPerRequestCertRecheck(t *testing.T) {
	dir := t.TempDir()
	ca1 := newCA(t)
	ca2 := newNamedCA(t, "Second CA")
	caFile := writePEM(t, dir, "ca.pem", ca1.pem)
	crlFile := ca1.writeCRL(t, dir, "clients.crl")
	srvCert, srvKey := writeSelfSigned(t, dir, "srv", "portal.example.com", "dash.example.com", "other.example.com")

	_, clientCert1 := ca1.clientCert(t, "alice", 101, []string{"alice.example"}, nil)
	addr := freePort(t)
	strPtr := func(s string) *string { return &s }

	baseCfg := func() *config.Config {
		return &config.Config{
			Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
			Servers: []config.ServerConfig{
				{
					Name:        "portal",
					Listen:      addr,
					ServerNames: []string{"portal.example.com"},
					TLS: &config.TLSConfig{
						Enabled: true,
						Cert:    srvCert,
						Key:     srvKey,
						ClientAuth: &config.ClientAuthConfig{
							Mode:    "request",
							CAFile:  caFile,
							CRLFile: crlFile,
						},
					},
					Locations: []config.LocationConfig{{
						RouteID:   strPtr("portal"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/"},
						ProxyPass: "http://127.0.0.1:9001",
						Sharing:   &config.SharingConfig{Mode: "public"},
					}},
				},
				{
					Name:        "dashboard",
					Listen:      addr,
					ServerNames: []string{"dash.example.com"},
					TLS: &config.TLSConfig{
						Enabled: true,
						Cert:    srvCert,
						Key:     srvKey,
					},
					Locations: []config.LocationConfig{{
						RouteID:           strPtr("dashboard"),
						Match:             config.MatchConfig{Type: "prefix", Path: "/"},
						ProxyPass:         "http://127.0.0.1:9002",
						RequireClientCert: true,
						Sharing:           &config.SharingConfig{Mode: "restricted"},
					}},
				},
			},
		}
	}

	streamHold := make(chan struct{})
	defer close(streamHold)
	portalRelease := make(chan struct{})

	initCfg := baseCfg()
	src := &stubSource{}
	src.set(initCfg, nil)
	factory := buildTestRouterFactory(t, streamHold, portalRelease)
	srv := New(initCfg, initCfg, lifecycle.ComputeFingerprint(initCfg), quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })
	srv.exchanges.Grace = 150 * time.Millisecond
	srv.exchanges.Bound = 400 * time.Millisecond

	ready := make(chan struct{})
	srv.OnInitialGenerationReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloadCh := make(chan ReloadRequest, 1)
	go func() { _ = srv.Run(ctx, reloadCh, redact.EmptyState()) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}

	// Build a single shared HTTP/2 transport that dials addr once and multiplexes
	// requests for both portal.example.com and dash.example.com over that single
	// TCP/TLS/HTTP2 connection (using one URL authority + per-request Host header).
	var dialCount atomic.Int32
	h2Tr := &http.Transport{
		ForceAttemptHTTP2: true,
		DialTLSContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialCount.Add(1)
			d := &tls.Dialer{
				Config: &tls.Config{
					InsecureSkipVerify: true,
					ServerName:         "portal.example.com",
					NextProtos:         []string{"h2"},
					Certificates:       []tls.Certificate{clientCert1},
				},
			}
			return d.DialContext(ctx, network, addr)
		},
	}
	defer h2Tr.CloseIdleConnections()
	h2Client := &http.Client{Transport: h2Tr, Timeout: 5 * time.Second}
	doH2Get := func(host, path string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+path, nil)
		if err != nil {
			return nil, err
		}
		req.Host = host
		return h2Client.Do(req)
	}

	// 1. Open both a portal stream and a dashboard stream on the SAME shared HTTP/2 connection.
	portalResp, err := doH2Get("portal.example.com", "/?stream=1")
	if err != nil {
		t.Fatalf("portal GET: %v", err)
	}
	defer portalResp.Body.Close()
	if portalResp.ProtoMajor != 2 {
		t.Fatalf("expected HTTP/2 on portal, got %s", portalResp.Proto)
	}
	portalBR := bufio.NewReader(portalResp.Body)
	if line, rErr := portalBR.ReadString('\n'); rErr != nil || !strings.Contains(line, "start-portal") {
		t.Fatalf("portal initial frame = %q, err = %v", line, rErr)
	}

	dashResp, err := doH2Get("dash.example.com", "/?stream=1")
	if err != nil {
		t.Fatalf("dashboard GET: %v", err)
	}
	defer dashResp.Body.Close()
	dashBR := bufio.NewReader(dashResp.Body)
	if line, rErr := dashBR.ReadString('\n'); rErr != nil || !strings.Contains(line, "start-dashboard") {
		t.Fatalf("dashboard initial frame = %q, err = %v", line, rErr)
	}
	if gotDials := dialCount.Load(); gotDials != 1 {
		t.Fatalf("shared-H2 fixture must use exactly 1 TCP/TLS connection, got %d", gotDials)
	}

	// 2. Withdraw dashboard (Withdrawn = true). Only the dashboard stream must end;
	//    the portal stream on the same H2 connection must continue and complete!
	withdrawnCfg := baseCfg()
	withdrawnCfg.Servers[1].Locations[0].Withdrawn = true
	candWithdrawn, _ := config.NewCandidate(withdrawnCfg)
	resCh := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "withdraw-dash", Source: ReloadSourceAdmin, Candidate: candWithdrawn, Result: resCh}
	relRes := <-resCh
	if relRes.Outcome != ReloadAppliedLive {
		t.Fatalf("withdraw reload = %+v", relRes)
	}
	if len(relRes.EntryRetirements) != 1 || relRes.EntryRetirements[0].EndedExchanges != 1 || relRes.EntryRetirements[0].CollateralCount != 0 || !relRes.EntryRetirements[0].CompletedWithinBound {
		t.Fatalf("expected 1 retired exchange with 0 collateral, got %+v", relRes.EntryRetirements)
	}

	// Dashboard stream must now return an error (RST_STREAM) when read past buffered bytes.
	if _, dErr := io.ReadAll(dashBR); dErr == nil {
		t.Fatal("expected withdrawn dashboard H2 stream to be reset")
	}

	// Portal stream on the same H2 connection must still complete cleanly!
	close(portalRelease)
	var portalRest strings.Builder
	for {
		line, pErr := portalBR.ReadString('\n')
		portalRest.WriteString(line)
		if strings.Contains(portalRest.String(), "end-portal") {
			break
		}
		if pErr != nil {
			t.Fatalf("portal stream interrupted by sibling retirement: %v (read %q)", pErr, portalRest.String())
		}
	}

	// 3. Restore dashboard (Withdrawn = false), then revoke clientCert1 (serial 101) in CRL and reload.
	//    The next request to dashboard on the ALREADY-OPEN H2 connection must be denied 403,
	//    while a request to portal on the same H2 connection still succeeds 200!
	ca1.writeCRL(t, "", crlFile, 101)
	restoredCfg := baseCfg()
	candRestored, _ := config.NewCandidate(restoredCfg)
	resCh2 := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "restore-and-revoke", Source: ReloadSourceAdmin, Candidate: candRestored, Result: resCh2}
	if r := <-resCh2; r.Outcome != ReloadAppliedLive {
		t.Fatalf("restore-and-revoke reload = %+v", r)
	}

	dashAfterCRL, err := doH2Get("dash.example.com", "/")
	if err != nil {
		t.Fatalf("dashboard GET on open H2 conn after CRL reload failed at transport instead of HTTP 403: %v", err)
	}
	_ = dashAfterCRL.Body.Close()
	if dashAfterCRL.StatusCode != http.StatusForbidden {
		t.Fatalf("dashboard status on open H2 conn with revoked cert = %d, want 403", dashAfterCRL.StatusCode)
	}

	portalAfterCRL, err := doH2Get("portal.example.com", "/")
	if err != nil {
		t.Fatalf("portal GET on open H2 conn after CRL reload: %v", err)
	}
	_ = portalAfterCRL.Body.Close()
	if portalAfterCRL.StatusCode != http.StatusOK {
		t.Fatalf("portal status on open H2 conn = %d, want 200", portalAfterCRL.StatusCode)
	}

	// 4. Lift CRL revocation, but restrict VerifySAN to ["other.example"] -> next request on open H2 conn is 403.
	ca1.writeCRL(t, "", crlFile)
	sanCfg := baseCfg()
	sanCfg.Servers[0].TLS.ClientAuth.VerifySAN = []string{"other.example"}
	candSAN, _ := config.NewCandidate(sanCfg)
	resCh3 := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "san-restrict", Source: ReloadSourceAdmin, Candidate: candSAN, Result: resCh3}
	if r := <-resCh3; r.Outcome != ReloadAppliedLive {
		t.Fatalf("san-restrict reload = %+v", r)
	}
	dashAfterSAN, err := doH2Get("dash.example.com", "/")
	if err != nil {
		t.Fatalf("dashboard GET after SAN reload: %v", err)
	}
	_ = dashAfterSAN.Body.Close()
	if dashAfterSAN.StatusCode != http.StatusForbidden {
		t.Fatalf("dashboard status on open H2 conn with disallowed SAN = %d, want 403", dashAfterSAN.StatusCode)
	}

	// 5. Rotate CA bundle to ca2 (removing ca1) -> next request on open H2 conn is 403.
	writePEM(t, "", caFile, ca2.pem)
	ca2.writeCRL(t, "", crlFile)
	caCfg := baseCfg()
	candCA, _ := config.NewCandidate(caCfg)
	resCh4 := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "ca-rotate", Source: ReloadSourceAdmin, Candidate: candCA, Result: resCh4}
	if r := <-resCh4; r.Outcome != ReloadAppliedLive {
		t.Fatalf("ca-rotate reload = %+v", r)
	}
	dashAfterCA, err := doH2Get("dash.example.com", "/")
	if err != nil {
		t.Fatalf("dashboard GET after CA rotation: %v", err)
	}
	_ = dashAfterCA.Body.Close()
	if dashAfterCA.StatusCode != http.StatusForbidden {
		t.Fatalf("dashboard status on open H2 conn after CA rotation = %d, want 403", dashAfterCA.StatusCode)
	}
	if gotDials := dialCount.Load(); gotDials != 1 {
		t.Fatalf("expected all requests to share the single H2 connection, got %d dials", gotDials)
	}
}

func TestPublishTimeFileContentClassificationAndKeepOpenOverride(t *testing.T) {
	dir := t.TempDir()
	htFile := filepath.Join(dir, "users.htpasswd")
	if err := os.WriteFile(htFile, []byte("alice:$2y$05$hash1\nbob:$2y$05$hash2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wafRuleFile := filepath.Join(dir, "rules.conf")
	if err := os.WriteFile(wafRuleFile, []byte("SecRuleEngine On\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	strPtr := func(s string) *string { return &s }
	makeCfg := func() *config.Config {
		return &config.Config{
			WAF: config.WAFConfig{
				Enabled:         true,
				Mode:            "block",
				DirectivesFiles: []string{wafRuleFile},
			},
			Servers: []config.ServerConfig{{
				Listen:      "127.0.0.1:8080",
				ServerNames: []string{"app.example.com"},
				Locations: []config.LocationConfig{{
					RouteID:   strPtr("app"),
					Match:     config.MatchConfig{Type: "prefix", Path: "/"},
					ProxyPass: "http://127.0.0.1:9001",
					Auth: &config.AuthConfig{
						Basic: &config.BasicAuthConfig{File: htFile, Realm: "App"},
					},
				}},
			}},
		}
	}

	cfg1 := makeCfg()
	files1 := captureLoadedAccessFiles(cfg1)

	// 1. Unchanged files -> Retire=false.
	trSame := ComputePublishEntryTransitions(cfg1, cfg1, files1, cfg1, cfg1, files1, nil, nil)
	if len(trSame) != 1 || trSame[0].Retire {
		t.Fatalf("unchanged files must not trigger retirement, got %+v", trSame)
	}

	// 2. Remove bob from htpasswd (remaining entry alice byte-identical) -> narrowed (mandatory tightening).
	//    Even if keep_open_entries=["app"] is passed AND proxy_pass also changes (mixed change),
	//    mandatory tightening MUST override keep_open_entries and set Retire=true (Point 1)!
	if err := os.WriteFile(htFile, []byte("alice:$2y$05$hash1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2 := makeCfg()
	cfg2.Servers[0].Locations[0].ProxyPass = "http://127.0.0.1:9002"
	files2 := captureLoadedAccessFiles(cfg2)
	trHtRemoved := ComputePublishEntryTransitions(cfg1, cfg1, files1, cfg2, cfg2, files2, nil, []string{"app"})
	if len(trHtRemoved) != 1 || !trHtRemoved[0].Retire || trHtRemoved[0].KeepOpen {
		t.Fatalf("Point 1: htpasswd user removal + target change must override keep_open_entries and retire, got %+v", trHtRemoved)
	}

	// 3. Mixed written change: adding a CIDR restriction + changing proxy_pass must also override keep_open_entries.
	cfg3 := makeCfg()
	cfg3.Servers[0].Locations[0].ProxyPass = "http://127.0.0.1:9003"
	cfg3.Servers[0].Locations[0].Auth.Allow = []string{"10.0.0.0/8"}
	trMixedWritten := ComputePublishEntryTransitions(cfg2, cfg2, files2, cfg3, cfg3, files2, nil, []string{"app"})
	if len(trMixedWritten) != 1 || !trMixedWritten[0].Retire || trMixedWritten[0].KeepOpen {
		t.Fatalf("Point 1: CIDR narrowing + target change must override keep_open_entries, got %+v", trMixedWritten)
	}

	// 4. Pure uncertain change (WAF rule file content changed or target changed without narrowing)
	//    honours keep_open_entries=["app"] (KeepOpen=true, Retire=false), and retires when keep_open_entries is empty.
	if err := os.WriteFile(wafRuleFile, []byte("SecRuleEngine On\n# updated rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files3 := captureLoadedAccessFiles(cfg3)
	trWAFKeepOpen := ComputePublishEntryTransitions(cfg3, cfg3, files2, cfg3, cfg3, files3, nil, []string{"app"})
	if len(trWAFKeepOpen) != 1 || trWAFKeepOpen[0].Retire || !trWAFKeepOpen[0].KeepOpen {
		t.Fatalf("expected uncertain WAF file edit to honour keep_open_entries, got %+v", trWAFKeepOpen)
	}
	trWAFRetire := ComputePublishEntryTransitions(cfg3, cfg3, files2, cfg3, cfg3, files3, nil, nil)
	if len(trWAFRetire) != 1 || !trWAFRetire[0].Retire || trWAFRetire[0].KeepOpen {
		t.Fatalf("expected uncertain WAF file edit without keep_open_entries to retire, got %+v", trWAFRetire)
	}

	// 5. CRL content classification unit checks: serial added (narrowed), serial removed (widened), issuer changed (uncertain).
	crlBase := []crlSet{{issuer: []byte("ca1"), revoked: map[string]bool{"0a": true}}}
	crlAdded := []crlSet{{issuer: []byte("ca1"), revoked: map[string]bool{"0a": true, "0b": true}}}
	crlRemoved := []crlSet{{issuer: []byte("ca1"), revoked: map[string]bool{}}}
	crlDiffIssuer := []crlSet{{issuer: []byte("ca2"), revoked: map[string]bool{"0a": true}}}
	if got := classifyCRLContentChange(crlBase, crlAdded); got != "narrowed" {
		t.Fatalf("CRL serial added = %q, want narrowed", got)
	}
	if got := classifyCRLContentChange(crlAdded, crlRemoved); got != "widened" {
		t.Fatalf("CRL serial removed = %q, want widened", got)
	}
	if got := classifyCRLContentChange(crlBase, crlDiffIssuer); got != "uncertain" {
		t.Fatalf("CRL issuer changed = %q, want uncertain", got)
	}
	if got := classifyCRLContentChange(nil, nil); got != "unchanged" {
		t.Fatalf("empty CRL = %q, want unchanged", got)
	}
	if got := classifyCRLContentChange(crlBase, []crlSet{{issuer: []byte("ca1"), revoked: map[string]bool{"0b": true}}}); got != "uncertain" {
		t.Fatalf("CRL added+removed = %q, want uncertain", got)
	}
	if got := classifyCRLContentChange(crlBase, append([]crlSet(nil), crlBase[0], crlDiffIssuer[0])); got != "uncertain" {
		t.Fatalf("CRL different issuer count = %q, want uncertain", got)
	}
	_ = sync.Mutex{}
	_ = x509.Certificate{}
}

func TestPublishClassificationAllSubsystems(t *testing.T) {
	dir := t.TempDir()

	// 1. htpasswd edge cases: comments, blank lines, missing colon, duplicate user, added user, modified hash.
	htInvalid := filepath.Join(dir, "invalid.htpasswd")
	_ = os.WriteFile(htInvalid, []byte("# comment\n\ninvalid_line_no_colon\n"), 0o600)
	snapInvalid := parseHtpasswdSnapshot(htInvalid)
	if snapInvalid.valid {
		t.Fatal("expected invalid snapshot for line without colon")
	}
	htDup := filepath.Join(dir, "dup.htpasswd")
	_ = os.WriteFile(htDup, []byte("alice:h1\nalice:h2\n"), 0o600)
	snapDup := parseHtpasswdSnapshot(htDup)
	if snapDup.valid {
		t.Fatal("expected invalid snapshot for duplicate user")
	}
	if got := classifyHtpasswdContentChange(snapInvalid, snapDup); got != "uncertain" {
		t.Fatalf("invalid snapshots with different digests = %q, want uncertain", got)
	}
	if got := classifyHtpasswdContentChange(snapInvalid, snapInvalid); got != "unchanged" {
		t.Fatalf("same invalid snapshot = %q, want unchanged", got)
	}
	snapMissing := parseHtpasswdSnapshot(filepath.Join(dir, "nonexistent"))
	if snapMissing.rawDigest != "" {
		t.Fatal("expected empty digest for missing file")
	}
	snapU1 := htpasswdSnapshot{rawDigest: "d1", users: map[string]string{"a": "a:1"}, valid: true}
	snapU1Mod := htpasswdSnapshot{rawDigest: "d2", users: map[string]string{"a": "a:2"}, valid: true}
	snapU1Add := htpasswdSnapshot{rawDigest: "d3", users: map[string]string{"a": "a:1", "b": "b:1"}, valid: true}
	if got := classifyHtpasswdContentChange(snapU1, snapU1Mod); got != "uncertain" {
		t.Fatalf("modified hash = %q, want uncertain", got)
	}
	if got := classifyHtpasswdContentChange(snapU1, snapU1Add); got != "uncertain" {
		t.Fatalf("added user = %q, want uncertain", got)
	}

	// 2. Bind address classification (IPv4/IPv6 wildcard <-> specific, port change, invalid).
	bindCases := []struct {
		before, after, want string
	}{
		{"127.0.0.1:8080", "127.0.0.1:8080", "unchanged"},
		{"0.0.0.0:8080", "127.0.0.1:8080", "narrowed"},
		{"127.0.0.1:8080", "0.0.0.0:8080", "widened"},
		{"[::]:8080", "[::1]:8080", "narrowed"},
		{"[::1]:8080", "[::]:8080", "widened"},
		{"127.0.0.1:8080", "127.0.0.2:8080", "uncertain"},
		{"127.0.0.1:8080", "127.0.0.1:9090", "uncertain"},
		{"invalid", "127.0.0.1:8080", "uncertain"},
		{"localhost:8080", "127.0.0.1:8080", "uncertain"},
	}
	for _, tc := range bindCases {
		if got := classifyPublishBindChange(tc.before, tc.after); got != tc.want {
			t.Fatalf("classifyPublishBindChange(%q, %q) = %q, want %q", tc.before, tc.after, got, tc.want)
		}
	}

	// 3. CORS classification.
	corsOff := &config.CORSConfig{Enabled: false}
	corsA := &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example", "https://b.example"}, AllowedMethods: []string{"GET"}, AllowCredentials: true}
	corsNarrowOrigin := &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example"}, AllowedMethods: []string{"GET"}, AllowCredentials: true}
	corsNarrowCreds := &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example", "https://b.example"}, AllowedMethods: []string{"GET"}, AllowCredentials: false}
	corsWidenCreds := &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example", "https://b.example"}, AllowedMethods: []string{"GET"}, AllowCredentials: true}
	corsMixed := &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example", "https://c.example"}, AllowedMethods: []string{"GET"}, AllowCredentials: true}
	corsMethods := &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://a.example"}, AllowedMethods: []string{"GET", "POST"}}
	if got := classifyPublishCORS(corsOff, corsOff); got != "unchanged" {
		t.Fatalf("cors off->off = %q", got)
	}
	if got := classifyPublishCORS(corsOff, corsA); got != "widened" {
		t.Fatalf("cors off->on = %q", got)
	}
	if got := classifyPublishCORS(corsA, corsOff); got != "narrowed" {
		t.Fatalf("cors on->off = %q", got)
	}
	if got := classifyPublishCORS(corsA, corsNarrowOrigin); got != "narrowed" {
		t.Fatalf("cors origin removed = %q", got)
	}
	if got := classifyPublishCORS(corsNarrowOrigin, corsA); got != "widened" {
		t.Fatalf("cors origin added = %q", got)
	}
	if got := classifyPublishCORS(corsA, corsNarrowCreds); got != "narrowed" {
		t.Fatalf("cors creds disabled = %q", got)
	}
	if got := classifyPublishCORS(corsNarrowCreds, corsWidenCreds); got != "widened" {
		t.Fatalf("cors creds enabled = %q", got)
	}
	if got := classifyPublishCORS(corsA, corsMixed); got != "uncertain" {
		t.Fatalf("cors mixed origins = %q", got)
	}
	if got := classifyPublishCORS(corsA, corsMethods); got != "uncertain" {
		t.Fatalf("cors methods changed = %q", got)
	}

	// 4. CIDR v4/v6 set algebra (allow, deny subtraction, subset, superset, disjoint, invalid, overlapping/sorted intervals).
	if got := classifyPublishCIDR(nil, nil, nil, nil); got != "unchanged" {
		t.Fatalf("empty CIDR = %q", got)
	}
	if got := classifyPublishCIDR([]string{"192.168.1.0/24", "10.0.0.0/24", "10.0.0.128/25", "10.0.1.0/24", "2001:db8::/32"}, nil, []string{"10.0.0.0/24"}, nil); got != "narrowed" {
		t.Fatalf("CIDR subset = %q, want narrowed", got)
	}
	if got := classifyPublishCIDR([]string{"10.1.0.0/16"}, nil, []string{"10.0.0.0/8"}, nil); got != "widened" {
		t.Fatalf("CIDR superset = %q, want widened", got)
	}
	if got := classifyPublishCIDR([]string{"10.0.0.0/8"}, nil, []string{"10.0.0.0/8"}, []string{"10.1.0.1", "2001:db8::1"}); got != "narrowed" {
		t.Fatalf("CIDR deny added = %q, want narrowed", got)
	}
	if got := classifyPublishCIDR([]string{"10.1.0.0/16"}, nil, []string{"192.168.0.0/16"}, nil); got != "uncertain" {
		t.Fatalf("CIDR disjoint = %q, want uncertain", got)
	}
	if got := classifyPublishCIDR([]string{"not-an-ip"}, nil, []string{"10.0.0.0/8"}, nil); got != "uncertain" {
		t.Fatalf("CIDR invalid = %q, want uncertain", got)
	}
	if got := classifyPublishCIDR([]string{""}, nil, []string{"10.0.0.0/8"}, nil); got != "uncertain" {
		t.Fatalf("CIDR blank = %q, want uncertain", got)
	}
	if got := classifyPublishCIDR([]string{"10.0.0.1/99"}, nil, []string{"10.0.0.0/8"}, nil); got != "uncertain" {
		t.Fatalf("CIDR bad prefix = %q, want uncertain", got)
	}
	if got := classifyPublishCIDR([]string{"10.0.0.0/8"}, []string{"bad-deny"}, []string{"10.0.0.0/8"}, nil); got != "uncertain" {
		t.Fatalf("CIDR bad deny = %q, want uncertain", got)
	}

	// 5. Auth credentials transitions (none <-> JWT, JWT -> ForwardAuth, JWT modified, ForwardAuth modified).
	emptyFiles := captureLoadedAccessFiles(nil)
	authNone := &config.AuthConfig{}
	authJWT1 := &config.AuthConfig{JWT: &config.JWTAuthConfig{Issuer: "iss1"}}
	authJWT2 := &config.AuthConfig{JWT: &config.JWTAuthConfig{Issuer: "iss2"}}
	authFwd1 := &config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{URL: "http://auth1"}}
	authFwd2 := &config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{URL: "http://auth2"}}
	if got := classifyPublishAuth(emptyFiles, emptyFiles, authNone, authJWT1); !slices.Contains(got, "narrowed") {
		t.Fatalf("auth none->jwt = %v, want narrowed", got)
	}
	if got := classifyPublishAuth(emptyFiles, emptyFiles, authJWT1, authNone); !slices.Contains(got, "widened") {
		t.Fatalf("auth jwt->none = %v, want widened", got)
	}
	if got := classifyPublishAuth(emptyFiles, emptyFiles, authJWT1, authFwd1); !slices.Contains(got, "uncertain") {
		t.Fatalf("auth jwt->fwd = %v, want uncertain", got)
	}
	if got := classifyPublishAuth(emptyFiles, emptyFiles, authJWT1, authJWT2); !slices.Contains(got, "uncertain") {
		t.Fatalf("auth jwt1->jwt2 = %v, want uncertain", got)
	}
	if got := classifyPublishAuth(emptyFiles, emptyFiles, authFwd1, authFwd2); !slices.Contains(got, "uncertain") {
		t.Fatalf("auth fwd1->fwd2 = %v, want uncertain", got)
	}

	// 6. WAF transitions (disabled <-> detect <-> block, with and without engine control).
	effWAFDisabled := &config.Config{WAF: config.WAFConfig{Enabled: false}}
	effWAFDetect := &config.Config{WAF: config.WAFConfig{Enabled: true, Mode: "detect"}}
	effWAFBlock := &config.Config{WAF: config.WAFConfig{Enabled: true, Mode: "block"}}
	effWAFDetectCtl := &config.Config{WAF: config.WAFConfig{Enabled: true, Mode: "detect", InlineRules: "SecAction \"id:1,ctl:ruleEngine=On\""}}
	if got := classifyPublishWAF(effWAFDisabled, effWAFBlock, emptyFiles, emptyFiles, 0, 0, 0, 0, nil, nil); got != "narrowed" {
		t.Fatalf("waf disabled->block = %q", got)
	}
	if got := classifyPublishWAF(effWAFBlock, effWAFDisabled, emptyFiles, emptyFiles, 0, 0, 0, 0, nil, nil); got != "widened" {
		t.Fatalf("waf block->disabled = %q", got)
	}
	if got := classifyPublishWAF(effWAFDisabled, effWAFDetect, emptyFiles, emptyFiles, 0, 0, 0, 0, nil, nil); got != "unchanged" {
		t.Fatalf("waf disabled->detect (no ctl) = %q", got)
	}
	if got := classifyPublishWAF(effWAFDetect, effWAFBlock, emptyFiles, emptyFiles, 0, 0, 0, 0, nil, nil); got != "narrowed" {
		t.Fatalf("waf detect->block = %q", got)
	}
	if got := classifyPublishWAF(effWAFBlock, effWAFDetect, emptyFiles, emptyFiles, 0, 0, 0, 0, nil, nil); got != "widened" {
		t.Fatalf("waf block->detect = %q", got)
	}
	if got := classifyPublishWAF(effWAFDisabled, effWAFDetectCtl, emptyFiles, emptyFiles, 0, 0, 0, 0, nil, nil); got != "narrowed" {
		t.Fatalf("waf disabled->detect (with ctl) = %q", got)
	}
	if got := classifyPublishWAF(effWAFDetectCtl, effWAFDisabled, emptyFiles, emptyFiles, 0, 0, 0, 0, nil, nil); got != "widened" {
		t.Fatalf("waf detect (with ctl)->disabled = %q", got)
	}

	// 7. Server block correlation, host dispatch reordering, removed entry, and client_auth transitions.
	r1 := "r1"
	r2 := "r2"
	prevMulti := &config.Config{
		Upstreams: []config.UpstreamConfig{{Name: "up1", Servers: []config.UpstreamServer{{Address: "127.0.0.1:9001"}}}},
		Servers: []config.ServerConfig{
			{
				Name:        "srv-a",
				Listen:      "127.0.0.1:8080",
				ServerNames: []string{"a.example.com", "a2.example.com"},
				TLS:         &config.TLSConfig{Enabled: true, ClientAuth: &config.ClientAuthConfig{Mode: "none", CAFile: "ca.pem"}},
				Locations: []config.LocationConfig{
					{RouteID: &r1, Match: config.MatchConfig{Type: "prefix", Path: "/a"}, ProxyPass: "http://up1"},
					{RouteID: &r2, Match: config.MatchConfig{Type: "prefix", Path: "/removed"}, Return: 200},
				},
			},
			{
				Name:        "srv-b",
				Listen:      "127.0.0.1:8080",
				ServerNames: []string{"*.example.com"},
				Locations: []config.LocationConfig{
					{Match: config.MatchConfig{Type: "prefix", Path: "/b"}, Deny: true},
				},
			},
		},
	}
	nextMulti := &config.Config{
		Upstreams: []config.UpstreamConfig{{Name: "up1", Servers: []config.UpstreamServer{{Address: "127.0.0.1:9002"}}}},
		Servers: []config.ServerConfig{
			{
				Name:        "srv-b",
				Listen:      "127.0.0.1:8080",
				ServerNames: []string{"*.example.com"},
				Locations: []config.LocationConfig{
					{Match: config.MatchConfig{Type: "prefix", Path: "/b"}, Deny: false, Return: 200},
				},
			},
			{
				Name:        "srv-a",
				Listen:      "127.0.0.1:8080",
				ServerNames: []string{"a.example.com"},
				TLS:         &config.TLSConfig{Enabled: true, ClientAuth: &config.ClientAuthConfig{Mode: "request", CAFile: "ca.pem"}},
				Locations: []config.LocationConfig{
					{RouteID: &r1, Match: config.MatchConfig{Type: "prefix", Path: "/a"}, RequireClientCert: true, ProxyPass: "http://up1"},
				},
			},
		},
	}
	trs := ComputePublishEntryTransitions(prevMulti, prevMulti, nil, nextMulti, nextMulti, nil, nil, []string{"servers[1].locations[0]"})
	if len(trs) != 3 {
		t.Fatalf("expected 3 transitions, got %+v", trs)
	}
	if ComputePublishEntryTransitions(nil, nil, nil, nil, nil, nil, nil, nil) != nil {
		t.Fatal("expected nil on nil configs")
	}
	_ = ComputePublishEntryTransitions(nil, prevMulti, nil, nil, nextMulti, nil, nil, nil)

	// Unnamed blocks correlating by shared route_id (pass 3) and single-block-on-listener (pass 4).
	bPass34 := []config.ServerConfig{
		{Listen: "127.0.0.1:8001", ServerNames: []string{"old1.example"}, Locations: []config.LocationConfig{{RouteID: &r1, Match: config.MatchConfig{Path: "/"}}}},
		{Listen: "127.0.0.1:8002", ServerNames: []string{"old2.example"}, Locations: []config.LocationConfig{{Match: config.MatchConfig{Path: "/"}}}},
	}
	aPass34 := []config.ServerConfig{
		{Listen: "127.0.0.1:9001", ServerNames: []string{"new1.example"}, Locations: []config.LocationConfig{{RouteID: &r1, Match: config.MatchConfig{Path: "/"}}}},
		{Listen: "127.0.0.1:8002", ServerNames: []string{"new2.example"}, Locations: []config.LocationConfig{{Match: config.MatchConfig{Path: "/"}}}},
	}
	if corr := correlatePublishServerBlocks(bPass34, aPass34); corr[0] != 0 || corr[1] != 1 {
		t.Fatalf("unexpected pass 3/4 block correlation: %v", corr)
	}

	// BuildClientCertVerifier error branch on unreadable CA file.
	badCASrvs := []config.ServerConfig{{
		Listen: "127.0.0.1:8443",
		TLS:    &config.TLSConfig{Enabled: true, ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: filepath.Join(dir, "missing-ca.pem")}},
	}}
	if _, err := BuildClientCertVerifier(badCASrvs, "127.0.0.1:8443"); err == nil {
		t.Fatal("expected error from BuildClientCertVerifier on missing CA file")
	}

	// 8. BuildClientCertVerifier, verifyPeerChain, verifyRequest, bareClientAuthWriter, plugins, and location WAF.
	v, err := BuildClientCertVerifier(nil, "127.0.0.1:8080")
	if err != nil || v == nil {
		t.Fatalf("expected non-nil verifier, got err=%v", err)
	}
	if ok, err := v(nil, false); ok || err != nil {
		t.Fatalf("v(nil, false) = %v, %v", ok, err)
	}
	if ok, err := v(nil, true); ok || err == nil {
		t.Fatalf("v(nil, true) = %v, %v; want error", ok, err)
	}

	caTest := newCA(t)
	caFile := writePEM(t, dir, "unit-ca.pem", caTest.pem)
	otherCA := newNamedCA(t, "OtherCA")
	_, untrustedCert := otherCA.clientCert(t, "untrusted", 77, []string{"untrusted.example"}, nil)
	parsedUntrusted, _ := x509.ParseCertificate(untrustedCert.Certificate[0])

	srvsRequire := []config.ServerConfig{{
		Listen: "127.0.0.1:8443",
		TLS:    &config.TLSConfig{Enabled: true, ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: caFile}},
	}}
	srvsRequest := []config.ServerConfig{{
		Listen: "127.0.0.1:8443",
		TLS:    &config.TLSConfig{Enabled: true, ClientAuth: &config.ClientAuthConfig{Mode: "request", CAFile: caFile}},
	}}
	bReq, err := clientAuthForAddr(srvsRequire, "127.0.0.1:8443", nil)
	if err != nil || bReq == nil {
		t.Fatalf("clientAuthForAddr require: %v", err)
	}
	bOpt, err := clientAuthForAddr(srvsRequest, "127.0.0.1:8443", nil)
	if err != nil || bOpt == nil {
		t.Fatalf("clientAuthForAddr request: %v", err)
	}
	if err := bReq.verifyPeerChain(nil); err == nil {
		t.Fatal("expected verifyPeerChain(nil) to fail when mode=require")
	}
	if err := bOpt.verifyPeerChain(nil); err != nil {
		t.Fatalf("expected verifyPeerChain(nil) to pass when mode=request, got %v", err)
	}
	badCS := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{parsedUntrusted, caTest.cert}}
	if ok, err := bOpt.verifyRequest(badCS, false); ok || err != nil {
		t.Fatalf("verifyRequest(untrusted, locRequire=false) on mode=request = %v, %v; want false, nil", ok, err)
	}
	if ok, err := bOpt.verifyRequest(badCS, true); ok || err == nil {
		t.Fatalf("verifyRequest(untrusted, locRequire=true) on mode=request = %v, %v; want false, err", ok, err)
	}

	// Exercise bareClientAuthWriter when a handler does not invoke ClientCert middleware.
	holder := &dynamicClientAuth{}
	holder.current.Store(bReq)
	checked := false
	bw := &bareClientAuthWriter{
		ResponseWriter: httptest.NewRecorder(),
		req:            httptest.NewRequest(http.MethodGet, "https://example.com/404", nil),
		caHolder:       holder,
		checked:        &checked,
	}
	bw.WriteHeader(http.StatusNotFound)
	_, _ = bw.Write([]byte("not found"))
	if !bw.rejected {
		t.Fatal("expected bareClientAuthWriter to reject unauthenticated request when mode=require")
	}

	// Exercise plugin file digests, location WAF override, and client_auth mode transitions in ComputePublishEntryTransitions.
	plugPath := filepath.Join(dir, "plug.wasm")
	_ = os.WriteFile(plugPath, []byte("wasm-v1"), 0o600)
	locWAF1 := &config.WAFConfig{Enabled: true, Mode: "block", Paranoia: 1, DirectivesFiles: []string{"", filepath.Join(dir, "missing-waf.conf")}}
	locWAF2 := &config.WAFConfig{Enabled: true, Mode: "block", Paranoia: 2, DirectivesFiles: []string{"", filepath.Join(dir, "missing-waf.conf")}}
	cfgPlug1 := &config.Config{
		Plugins: map[string]config.PluginConfig{"p1": {Path: plugPath}},
		Servers: []config.ServerConfig{{
			Listen:        "127.0.0.1:8443",
			RedirectHTTPS: 0,
			ProxyProtocol: "",
			TLS:           &config.TLSConfig{Enabled: true, ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: caFile}},
			Plugins:       []string{"p1"},
			Locations: []config.LocationConfig{{
				Match:     config.MatchConfig{Type: "prefix", Path: "/p"},
				WAF:       locWAF1,
				Withdrawn: true,
				Rewrites:  []config.RewriteConfig{{Pattern: "^/old", Replacement: "/new"}},
				Return:    200,
			}},
		}},
	}
	filesPlug1 := captureLoadedAccessFiles(cfgPlug1)
	_ = os.WriteFile(plugPath, []byte("wasm-v2"), 0o600)
	cfgPlug2, _ := cfgPlug1.Clone()
	cfgPlug2.Servers[0].RedirectHTTPS = 301
	cfgPlug2.Servers[0].ProxyProtocol = "v2"
	cfgPlug2.Servers[0].TLS.ClientAuth.Mode = "request"
	cfgPlug2.Servers[0].Locations[0].WAF = locWAF2
	cfgPlug2.Servers[0].Locations[0].Withdrawn = false
	cfgPlug2.Servers[0].Locations[0].Rewrites = nil
	filesPlug2 := captureLoadedAccessFiles(cfgPlug2)
	trsPlug := ComputePublishEntryTransitions(cfgPlug1, cfgPlug1, filesPlug1, cfgPlug2, cfgPlug2, filesPlug2, []PluginModuleChange{{Name: "p1", Before: "a", After: "b"}}, nil)
	if len(trsPlug) != 1 || !trsPlug[0].Retire {
		t.Fatalf("expected plugin/listener/WAF/rewrite transition to retire, got %+v", trsPlug)
	}
}

func audit653Config() *config.Config {
	id := "portal"
	return &config.Config{
		Servers: []config.ServerConfig{{
			Listen: "127.0.0.1:8080",
			Locations: []config.LocationConfig{{
				RouteID:   &id,
				Match:     config.MatchConfig{Type: "prefix", Path: "/portal"},
				ProxyPass: "http://127.0.0.1:9001",
			}},
		}},
	}
}

func TestAudit653H2FixtureActuallySharesConnection(t *testing.T) {
	TestSharedH2StreamRetirementAndPerRequestCertRecheck(t)
}

func TestAudit653WAFIncludeContentChangeRetires(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child.conf")
	dataFile := filepath.Join(dir, "words.data")
	main := filepath.Join(dir, "main.conf")
	if err := os.WriteFile(dataFile, []byte("badword1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, []byte("# comment\nInclude @owasp_crs/ignore.conf\nInclude \"*.conf\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("SecRule REQUEST_URI \"@pmFromFile words.data\" \\\n\"id:1001,phase:1,deny\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := audit653Config()
	cfg.WAF = config.WAFConfig{Enabled: true, Mode: "block", DirectivesFiles: []string{main}, InlineRules: "IncludeOptional " + child}
	beforeFiles := captureLoadedAccessFiles(cfg)
	if err := os.WriteFile(child, []byte(`SecRule REQUEST_URI "@contains new" "id:1002,phase:1,deny"`), 0o600); err != nil {
		t.Fatal(err)
	}
	afterFiles := captureLoadedAccessFiles(cfg)
	trs := ComputePublishEntryTransitions(cfg, cfg, beforeFiles, cfg, cfg, afterFiles, nil, nil)
	if len(trs) != 1 || !trs[0].Retire {
		t.Fatalf("loaded WAF include changed but no retirement: %+v", trs)
	}
}

func TestAudit653HandlerPanicReleasesGeneration(t *testing.T) {
	s := &Server{baseCtx: context.Background(), exchanges: respwriter.NewExchangeRegistry(), listeners: make(map[string]*listenerEntry)}
	g := newHandlerGen(context.Background(), time.Second, map[string]http.Handler{"addr": http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })}, nil, 1)
	s.handlers.Store(g)
	func() {
		defer func() { _ = recover() }()
		s.dynamicHandler("addr").ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/", nil))
	}()
	if got := g.inflight.Load(); got != 0 {
		t.Fatalf("generation inflight=%d after aborted request, want 0", got)
	}
}

func TestAudit653RealListenerRemoveAndReadd(t *testing.T) {
	addr1, addr2 := freePort(t), freePort(t)
	base := audit653Config()
	base.Servers[0].Listen = addr1
	otherID := "other"
	other := base.Servers[0]
	other.Listen = addr2
	other.Locations = []config.LocationConfig{{RouteID: &otherID, Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: "http://127.0.0.1:9002"}}
	base.Servers = append(base.Servers, other)

	src := &stubSource{}
	src.set(base, nil)
	s := New(base, base, lifecycle.ComputeFingerprint(base), quietLogger(), buildTestRouterFactory(t, nil, nil), src, func(context.Context, *config.Config) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	s.OnInitialGenerationReady = func() { close(ready) }
	reload := make(chan ReloadRequest)
	go func() { done <- s.Run(ctx, reload, redact.EmptyState()) }()
	defer func() { cancel(); <-done }()
	<-ready

	tr := &http.Transport{DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: time.Second}
	get := func() error {
		r, err := client.Get("http://" + addr1 + "/portal")
		if err != nil {
			return err
		}
		_ = r.Body.Close()
		return nil
	}
	if err := get(); err != nil {
		t.Fatalf("before remove: %v", err)
	}
	apply := func(c *config.Config) {
		cand, err := config.NewCandidate(c)
		if err != nil {
			t.Fatal(err)
		}
		res := make(chan ReloadResult, 1)
		reload <- ReloadRequest{Source: ReloadSourceAdmin, Candidate: cand, Result: res}
		if r := <-res; r.Outcome != ReloadAppliedLive {
			t.Fatalf("reload=%+v", r)
		}
	}
	removed, _ := base.Clone()
	removed.Servers = removed.Servers[1:]
	apply(removed)
	apply(base)
	if err := get(); err != nil {
		t.Fatalf("fresh request after remove/re-add fails despite applied_live: %v", err)
	}
}

func TestAudit653MappedIPv4CIDRDoesNotPanicAtPublish(t *testing.T) {
	cfg := audit653Config()
	cfg.Servers[0].Locations[0].Auth = &config.AuthConfig{Allow: []string{"::ffff:192.0.2.0/120"}}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("fixture must pass config validation: %v", err)
	}
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("validated mapped IPv4 CIDR panics during publication: %v", p)
		}
	}()
	ComputePublishEntryTransitions(cfg, cfg, nil, cfg, cfg, nil, nil, nil)
}

func TestRealH1StreamRetirementAndPerRequestCertRecheck(t *testing.T) {
	dir := t.TempDir()
	ca1 := newCA(t)
	caFile := writePEM(t, dir, "ca.pem", ca1.pem)
	crlFile := ca1.writeCRL(t, dir, "clients.crl")
	srvCert, srvKey := writeSelfSigned(t, dir, "srv", "dash.example.com")
	_, clientCert1 := ca1.clientCert(t, "alice", 202, []string{"alice.example"}, nil)
	addr := freePort(t)
	strPtr := func(s string) *string { return &s }

	baseCfg := func() *config.Config {
		return &config.Config{
			Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
			Servers: []config.ServerConfig{{
				Name:        "dash",
				Listen:      addr,
				ServerNames: []string{"dash.example.com"},
				TLS: &config.TLSConfig{
					Enabled: true,
					Cert:    srvCert,
					Key:     srvKey,
					ClientAuth: &config.ClientAuthConfig{
						Mode:    "request",
						CAFile:  caFile,
						CRLFile: crlFile,
					},
				},
				Locations: []config.LocationConfig{{
					RouteID:           strPtr("dashboard"),
					Match:             config.MatchConfig{Type: "prefix", Path: "/"},
					ProxyPass:         "http://127.0.0.1:9002",
					RequireClientCert: true,
					Sharing:           &config.SharingConfig{Mode: "restricted"},
				}},
			}},
		}
	}

	streamHold := make(chan struct{})
	defer close(streamHold)
	initCfg := baseCfg()
	src := &stubSource{}
	src.set(initCfg, nil)
	srv := New(initCfg, initCfg, lifecycle.ComputeFingerprint(initCfg), quietLogger(), buildTestRouterFactory(t, streamHold, nil), src, func(context.Context, *config.Config) error { return nil })
	srv.exchanges.Grace = 120 * time.Millisecond
	srv.exchanges.Bound = 350 * time.Millisecond

	ready := make(chan struct{})
	srv.OnInitialGenerationReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloadCh := make(chan ReloadRequest, 1)
	go func() { _ = srv.Run(ctx, reloadCh, redact.EmptyState()) }()
	<-ready

	var h1Dials atomic.Int32
	h1Tr := &http.Transport{
		ForceAttemptHTTP2: false,
		TLSNextProto:      make(map[string]func(string, *tls.Conn) http.RoundTripper),
		DialTLSContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			h1Dials.Add(1)
			d := &tls.Dialer{
				Config: &tls.Config{
					InsecureSkipVerify: true,
					ServerName:         "dash.example.com",
					NextProtos:         []string{"http/1.1"},
					Certificates:       []tls.Certificate{clientCert1},
				},
			}
			return d.DialContext(ctx, network, addr)
		},
	}
	defer h1Tr.CloseIdleConnections()
	h1Client := &http.Client{Transport: h1Tr, Timeout: 5 * time.Second}

	// 1. Unary request on HTTP/1.1 keep-alive connection succeeds, then CRL revocation denies next request on SAME H1 connection (h1Dials == 1).
	req1, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/", nil)
	req1.Host = "dash.example.com"
	resp1, err := h1Client.Do(req1)
	if err != nil || resp1.ProtoMajor != 1 || resp1.StatusCode != http.StatusOK {
		t.Fatalf("initial H1 GET: resp=%+v err=%v", resp1, err)
	}
	_, _ = io.Copy(io.Discard, resp1.Body)
	_ = resp1.Body.Close()

	ca1.writeCRL(t, "", crlFile, 202)
	candCRL, _ := config.NewCandidate(baseCfg())
	resCh := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "h1-crl", Source: ReloadSourceAdmin, Candidate: candCRL, Result: resCh}
	if r := <-resCh; r.Outcome != ReloadAppliedLive {
		t.Fatalf("h1-crl reload = %+v", r)
	}

	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/", nil)
	req2.Host = "dash.example.com"
	resp2, err := h1Client.Do(req2)
	if err != nil {
		t.Fatalf("second H1 GET on reused keep-alive conn failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden || h1Dials.Load() != 1 {
		t.Fatalf("expected 403 on reused H1 keep-alive conn (dials=1), got status=%d dials=%d", resp2.StatusCode, h1Dials.Load())
	}

	// 2. Lift CRL revocation, open an active H1 stream, then withdraw route -> stream ends within bound.
	ca1.writeCRL(t, "", crlFile)
	candRestore, _ := config.NewCandidate(baseCfg())
	resCh2 := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "h1-restore", Source: ReloadSourceAdmin, Candidate: candRestore, Result: resCh2}
	<-resCh2

	reqStream, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/?stream=1", nil)
	reqStream.Host = "dash.example.com"
	respStream, err := h1Client.Do(reqStream)
	if err != nil {
		t.Fatalf("H1 stream GET: %v", err)
	}
	defer respStream.Body.Close()
	br := bufio.NewReader(respStream.Body)
	if line, rErr := br.ReadString('\n'); rErr != nil || !strings.Contains(line, "start-dashboard") {
		t.Fatalf("H1 stream initial line = %q, err = %v", line, rErr)
	}

	withdrawnCfg := baseCfg()
	withdrawnCfg.Servers[0].Locations[0].Withdrawn = true
	candWithdrawn, _ := config.NewCandidate(withdrawnCfg)
	resCh3 := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "h1-withdraw", Source: ReloadSourceAdmin, Candidate: candWithdrawn, Result: resCh3}
	rel3 := <-resCh3
	if len(rel3.EntryRetirements) != 1 || rel3.EntryRetirements[0].EndedExchanges != 1 || !rel3.EntryRetirements[0].CompletedWithinBound {
		t.Fatalf("expected H1 stream retirement within bound, got %+v", rel3.EntryRetirements)
	}
}

func TestRealGRPCStreamRetirementOnSharedH2Conn(t *testing.T) {
	dir := t.TempDir()
	srvCert, srvKey := writeSelfSigned(t, dir, "srv", "portal.example.com", "grpc.example.com")
	addr := freePort(t)
	strPtr := func(s string) *string { return &s }

	baseCfg := func() *config.Config {
		return &config.Config{
			Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
			Servers: []config.ServerConfig{
				{
					Name:        "portal",
					Listen:      addr,
					ServerNames: []string{"portal.example.com"},
					TLS:         &config.TLSConfig{Enabled: true, Cert: srvCert, Key: srvKey},
					Locations: []config.LocationConfig{{
						RouteID:   strPtr("portal"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/"},
						ProxyPass: "http://127.0.0.1:9001",
					}},
				},
				{
					Name:        "grpc-svc",
					Listen:      addr,
					ServerNames: []string{"grpc.example.com"},
					TLS:         &config.TLSConfig{Enabled: true, Cert: srvCert, Key: srvKey},
					Locations: []config.LocationConfig{{
						RouteID:   strPtr("grpc-route"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/pkg.Svc/Stream"},
						ProxyPass: "http://127.0.0.1:9002",
					}},
				},
			},
		}
	}

	streamHold := make(chan struct{})
	defer close(streamHold)
	portalRelease := make(chan struct{})
	initCfg := baseCfg()
	src := &stubSource{}
	src.set(initCfg, nil)
	srv := New(initCfg, initCfg, lifecycle.ComputeFingerprint(initCfg), quietLogger(), buildTestRouterFactory(t, streamHold, portalRelease), src, func(context.Context, *config.Config) error { return nil })
	srv.exchanges.Grace = 120 * time.Millisecond
	srv.exchanges.Bound = 350 * time.Millisecond

	ready := make(chan struct{})
	srv.OnInitialGenerationReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloadCh := make(chan ReloadRequest, 1)
	go func() { _ = srv.Run(ctx, reloadCh, redact.EmptyState()) }()
	<-ready

	var dials atomic.Int32
	h2Tr := &http.Transport{
		ForceAttemptHTTP2: true,
		DialTLSContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dials.Add(1)
			d := &tls.Dialer{
				Config: &tls.Config{
					InsecureSkipVerify: true,
					ServerName:         "portal.example.com",
					NextProtos:         []string{"h2"},
				},
			}
			return d.DialContext(ctx, network, addr)
		},
	}
	defer h2Tr.CloseIdleConnections()
	h2Client := &http.Client{Transport: h2Tr, Timeout: 5 * time.Second}

	portalReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/?stream=1", nil)
	portalReq.Host = "portal.example.com"
	portalResp, err := h2Client.Do(portalReq)
	if err != nil {
		t.Fatalf("portal stream: %v", err)
	}
	defer portalResp.Body.Close()
	portalBR := bufio.NewReader(portalResp.Body)
	_, _ = portalBR.ReadString('\n')

	grpcReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+"/pkg.Svc/Stream?stream=1", strings.NewReader("\x00\x00\x00\x00\x00"))
	grpcReq.Host = "grpc.example.com"
	grpcReq.Header.Set("Content-Type", "application/grpc")
	grpcReq.Header.Set("TE", "trailers")
	grpcResp, err := h2Client.Do(grpcReq)
	if err != nil {
		t.Fatalf("gRPC stream: %v", err)
	}
	defer grpcResp.Body.Close()
	grpcBR := bufio.NewReader(grpcResp.Body)
	_, _ = grpcBR.ReadString('\n')

	if dials.Load() != 1 {
		t.Fatalf("expected 1 shared H2 connection for gRPC + portal, got %d", dials.Load())
	}

	// Narrow grpc-route by adding CIDR restriction -> gRPC stream resets while portal completes.
	narrowCfg := baseCfg()
	narrowCfg.Servers[1].Locations[0].Auth = &config.AuthConfig{Allow: []string{"10.254.0.0/16"}}
	candNarrow, _ := config.NewCandidate(narrowCfg)
	resCh := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "narrow-grpc", Source: ReloadSourceAdmin, Candidate: candNarrow, Result: resCh}
	rel := <-resCh
	if len(rel.EntryRetirements) != 1 || rel.EntryRetirements[0].EndedExchanges != 1 || rel.EntryRetirements[0].CollateralCount != 0 || !rel.EntryRetirements[0].CompletedWithinBound {
		t.Fatalf("expected gRPC stream retirement with 0 collateral, got %+v", rel.EntryRetirements)
	}
	if _, gErr := io.ReadAll(grpcBR); gErr == nil {
		t.Fatal("expected narrowed gRPC H2 stream to be reset")
	}
	close(portalRelease)
	rest, pErr := io.ReadAll(portalBR)
	if pErr != nil || !strings.Contains(string(rest), "end-portal") {
		t.Fatalf("sibling portal stream failed: err=%v body=%q", pErr, string(rest))
	}
}
