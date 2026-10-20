// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
)

func TestHTTP3RequiresConfiguredClientCertificate(t *testing.T) {
	dir := t.TempDir()
	serverCertPath, serverKeyPath := writeSelfSigned(t, dir, "h3-mtls-server", "localhost")
	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatalf("load server certificate: %v", err)
	}

	clientCA := newCA(t)
	_, validClient := clientCA.clientCert(t, "valid-client", 10, []string{"client.example"}, nil)
	otherCA := newCA(t)
	_, untrustedClient := otherCA.clientCert(t, "untrusted-client", 11, []string{"client.example"}, nil)

	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(clientCA.cert)
	serverTLS := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return &serverCert, nil
		},
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  clientRoots,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "verified client certificate missing", http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Client-CN", r.TLS.PeerCertificates[0].Subject.CommonName)
		w.WriteHeader(http.StatusNoContent)
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	h3, err := newStagedHTTP3WithTLS("127.0.0.1:0", serverTLS, handler, nil, logger)
	if err != nil {
		t.Fatalf("newStagedHTTP3WithTLS: %v", err)
	}
	if err := h3.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	defer func() { _ = h3.Close(context.Background()) }()
	addr := h3.(*h3Conn).ln.Addr().String()

	serverRoots := certificatePool(t, serverCertPath)
	tests := []struct {
		name         string
		certificates []tls.Certificate
		wantErr      bool
	}{
		{name: "missing certificate", wantErr: true},
		{name: "valid certificate", certificates: []tls.Certificate{validClient}},
		{name: "untrusted certificate", certificates: []tls.Certificate{untrustedClient}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &http3.Transport{TLSClientConfig: &tls.Config{
				RootCAs:      serverRoots,
				ServerName:   "localhost",
				Certificates: tt.certificates,
			}}
			defer func() { _ = tr.Close() }()
			client := &http.Client{Transport: tr, Timeout: 3 * time.Second}

			resp, err := client.Get("https://" + addr + "/")
			if tt.wantErr {
				if err == nil {
					_ = resp.Body.Close()
					t.Fatal("expected QUIC TLS client-auth failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("GET with valid client certificate: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
			}
			if resp.ProtoMajor != 3 {
				t.Fatalf("protocol = %q, want HTTP/3", resp.Proto)
			}
			if got := resp.Header.Get("X-Client-CN"); got != "valid-client" {
				t.Fatalf("client identity = %q, want valid-client", got)
			}
		})
	}
}

func TestHTTP3RequestClientCertificateAllowsAnonymousButRejectsUntrusted(t *testing.T) {
	dir := t.TempDir()
	serverCertPath, serverKeyPath := writeSelfSigned(t, dir, "h3-mtls-request", "localhost")
	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatalf("load server certificate: %v", err)
	}
	clientCA := newCA(t)
	otherCA := newCA(t)
	_, untrustedClient := otherCA.clientCert(t, "untrusted-client", 20, nil, nil)

	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(clientCA.cert)
	h3, err := newStagedHTTP3WithTLS("127.0.0.1:0", &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return &serverCert, nil
		},
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  clientRoots,
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newStagedHTTP3WithTLS: %v", err)
	}
	if err := h3.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	defer func() { _ = h3.Close(context.Background()) }()
	addr := h3.(*h3Conn).ln.Addr().String()

	serverRoots := certificatePool(t, serverCertPath)
	anonymous := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: serverRoots, ServerName: "localhost"}}
	client := &http.Client{Transport: anonymous, Timeout: 3 * time.Second}
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("anonymous request mode GET: %v", err)
	}
	_ = resp.Body.Close()
	_ = anonymous.Close()

	untrusted := &http3.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      serverRoots,
		ServerName:   "localhost",
		Certificates: []tls.Certificate{untrustedClient},
	}}
	defer func() { _ = untrusted.Close() }()
	_, err = (&http.Client{Transport: untrusted, Timeout: 3 * time.Second}).Get("https://" + addr + "/")
	if err == nil {
		t.Fatal("expected an untrusted presented certificate to fail in request mode")
	}
}

func TestHTTP3PreservesAdditionalClientCertificateVerifier(t *testing.T) {
	dir := t.TempDir()
	serverCertPath, serverKeyPath := writeSelfSigned(t, dir, "h3-mtls-verifier", "localhost")
	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	ca := newCA(t)
	_, allowed := ca.clientCert(t, "allowed", 30, []string{"allowed.example"}, nil)
	_, denied := ca.clientCert(t, "denied", 31, []string{"denied.example"}, nil)
	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(ca.cert)

	var verified atomic.Int64
	serverTLS := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &serverCert, nil },
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      clientRoots,
		VerifyPeerCertificate: func(_ [][]byte, chains [][]*x509.Certificate) error {
			if len(chains) == 0 || len(chains[0]) == 0 {
				return errors.New("verified client chain missing")
			}
			if err := chains[0][0].VerifyHostname("allowed.example"); err != nil {
				return err
			}
			verified.Add(1)
			return nil
		},
	}

	h3, err := newStagedHTTP3WithTLS("127.0.0.1:0", serverTLS, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := h3.Activate(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h3.Close(context.Background()) }()
	addr := h3.(*h3Conn).ln.Addr().String()
	serverRoots := certificatePool(t, serverCertPath)

	request := func(cert tls.Certificate) error {
		tr := &http3.Transport{TLSClientConfig: &tls.Config{
			RootCAs:      serverRoots,
			ServerName:   "localhost",
			Certificates: []tls.Certificate{cert},
		}}
		defer func() { _ = tr.Close() }()
		resp, err := (&http.Client{Transport: tr, Timeout: 3 * time.Second}).Get("https://" + addr + "/")
		if resp != nil {
			_ = resp.Body.Close()
		}
		return err
	}

	if err := request(allowed); err != nil {
		t.Fatalf("allowed certificate failed: %v", err)
	}
	if err := request(denied); err == nil {
		t.Fatal("expected additional peer verifier to reject denied SAN")
	}
	if verified.Load() != 1 {
		t.Fatalf("successful verifier calls = %d, want 1", verified.Load())
	}
}

func TestNewStagedHTTP3WithTLSRejectsInvalidTemplates(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := newStagedHTTP3WithTLS("127.0.0.1:0", nil, http.NotFoundHandler(), nil, logger); err == nil {
		t.Fatal("expected nil TLS template to fail")
	}
	if _, err := newStagedHTTP3WithTLS("127.0.0.1:0", &tls.Config{MaxVersion: tls.VersionTLS12}, http.NotFoundHandler(), nil, logger); err == nil {
		t.Fatal("expected a TLS 1.2 maximum to fail for HTTP/3")
	}
}

func certificatePool(t *testing.T, certPath string) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("append server certificate")
	}
	return pool
}

func TestHTTP3SharedConnStreamRetirementAndPerRequestCertRecheck(t *testing.T) {
	dir := t.TempDir()
	ca := newCA(t)
	caFile := writePEM(t, dir, "ca.pem", ca.pem)
	crlFile := ca.writeCRL(t, dir, "clients.crl")
	srvCert, srvKey := writeSelfSigned(t, dir, "h3-srv", "localhost", "portal.example.com", "dash.example.com")
	_, clientCert := ca.clientCert(t, "alice-h3", 201, []string{"alice.example"}, nil)

	addr := freePort(t)
	strPtr := func(s string) *string { return &s }

	baseCfg := func() *config.Config {
		return &config.Config{
			Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
			Servers: []config.ServerConfig{
				{
					Name:        "portal",
					Listen:      addr,
					ServerNames: []string{"portal.example.com", "localhost"},
					HTTP3:       &config.HTTP3Config{Enabled: true},
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
					HTTP3:       &config.HTTP3Config{Enabled: true},
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
		t.Fatal("HTTP/3 server did not start")
	}

	serverRoots := certificatePool(t, srvCert)
	h3Tr := &http3.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:      serverRoots,
			ServerName:   "localhost",
			Certificates: []tls.Certificate{clientCert},
		},
	}
	defer func() { _ = h3Tr.Close() }()
	h3Client := &http.Client{Transport: h3Tr, Timeout: 5 * time.Second}

	doH3Req := func(host, pathQuery string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+addr+pathQuery, nil)
		if err != nil {
			return nil, err
		}
		req.Host = host
		return h3Client.Do(req)
	}

	// 1. Open both a portal stream and a dashboard stream on the SAME QUIC HTTP/3 connection.
	portalResp, err := doH3Req("portal.example.com", "/?stream=1")
	if err != nil {
		t.Fatalf("H3 portal GET: %v", err)
	}
	defer portalResp.Body.Close()
	if portalResp.ProtoMajor != 3 {
		t.Fatalf("expected HTTP/3 on portal, got %s", portalResp.Proto)
	}
	portalBR := bufio.NewReader(portalResp.Body)
	if line, rErr := portalBR.ReadString('\n'); rErr != nil || !strings.Contains(line, "start-portal") {
		t.Fatalf("H3 portal initial frame = %q, err = %v", line, rErr)
	}

	dashResp, err := doH3Req("dash.example.com", "/?stream=1")
	if err != nil {
		t.Fatalf("H3 dashboard GET: %v", err)
	}
	defer dashResp.Body.Close()
	dashBR := bufio.NewReader(dashResp.Body)
	if line, rErr := dashBR.ReadString('\n'); rErr != nil || !strings.Contains(line, "start-dashboard") {
		t.Fatalf("H3 dashboard initial frame = %q, err = %v", line, rErr)
	}

	// 2. Withdraw dashboard. Only the dashboard H3 stream must reset; portal H3 stream completes!
	withdrawnCfg := baseCfg()
	withdrawnCfg.Servers[1].Locations[0].Withdrawn = true
	candWithdrawn, _ := config.NewCandidate(withdrawnCfg)
	resCh := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "h3-withdraw-dash", Source: ReloadSourceAdmin, Candidate: candWithdrawn, Result: resCh}
	relRes := <-resCh
	if relRes.Outcome != ReloadAppliedLive {
		t.Fatalf("H3 withdraw reload = %+v", relRes)
	}
	if len(relRes.EntryRetirements) != 1 || relRes.EntryRetirements[0].EndedExchanges != 1 || relRes.EntryRetirements[0].CollateralCount != 0 {
		t.Fatalf("expected 1 retired H3 stream with 0 collateral, got %+v", relRes.EntryRetirements)
	}

	if _, dErr := io.ReadAll(dashBR); dErr == nil {
		t.Fatal("expected withdrawn dashboard H3 stream to be reset")
	}

	close(portalRelease)
	var portalRest strings.Builder
	for {
		line, pErr := portalBR.ReadString('\n')
		portalRest.WriteString(line)
		if strings.Contains(portalRest.String(), "end-portal") {
			break
		}
		if pErr != nil {
			t.Fatalf("H3 portal stream interrupted by sibling retirement: %v", pErr)
		}
	}

	// 3. Restore dashboard and revoke clientCert (serial 201) in CRL.
	//    Next request on the SAME open H3 QUIC connection to dashboard must get 403 Forbidden,
	//    while portal on the same open H3 QUIC connection gets 200 OK!
	ca.writeCRL(t, "", crlFile, 201)
	restoredCfg := baseCfg()
	candRestored, _ := config.NewCandidate(restoredCfg)
	resCh2 := make(chan ReloadResult, 1)
	reloadCh <- ReloadRequest{ID: "h3-crl-revoke", Source: ReloadSourceAdmin, Candidate: candRestored, Result: resCh2}
	if r := <-resCh2; r.Outcome != ReloadAppliedLive {
		t.Fatalf("H3 crl-revoke reload = %+v", r)
	}

	dashAfterCRL, err := doH3Req("dash.example.com", "/")
	if err != nil {
		t.Fatalf("H3 dashboard GET after CRL reload: %v", err)
	}
	_ = dashAfterCRL.Body.Close()
	if dashAfterCRL.StatusCode != http.StatusForbidden {
		t.Fatalf("H3 dashboard status on open QUIC conn after CRL revocation = %d, want 403", dashAfterCRL.StatusCode)
	}

	portalAfterCRL, err := doH3Req("portal.example.com", "/")
	if err != nil {
		t.Fatalf("H3 portal GET after CRL reload: %v", err)
	}
	_ = portalAfterCRL.Body.Close()
	if portalAfterCRL.StatusCode != http.StatusOK {
		t.Fatalf("H3 portal status on open QUIC conn after CRL revocation = %d, want 200", portalAfterCRL.StatusCode)
	}
}
