// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme && http3 && linux

package server

import (
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"jul/internal/config"
)

const pebbleImage = "ghcr.io/letsencrypt/pebble@sha256:0c81f05db371f436c97fb894bd6d2ab8fc9c8e2ce40f8bef8a8e15941bcd75ef"

func TestDNS01PebbleEndToEnd(t *testing.T) {
	if os.Getenv("JUL_PEBBLE_E2E") != "1" {
		t.Skip("set JUL_PEBBLE_E2E=1 to run the pinned Docker/Pebble integration test")
	}
	dnsServer, dnsProvider := startRFC2136Server(t)
	packet, err := net.ListenPacket("udp", dnsProvider.server)
	if err != nil {
		t.Fatal(err)
	}
	udp := &dns.Server{PacketConn: packet, Handler: dnsServer, MsgAcceptFunc: func(dns.Header) dns.MsgAcceptAction { return dns.MsgAccept }}
	udpDone := make(chan error, 1)
	go func() { udpDone <- udp.ActivateAndServe() }()
	t.Cleanup(func() {
		if err := udp.Shutdown(); err != nil {
			t.Errorf("shutdown DNS UDP server: %v", err)
		}
		if err := <-udpDone; err != nil {
			t.Errorf("DNS UDP server: %v", err)
		}
	})
	dnsServer.mu.Lock()
	dnsServer.records[testOwner] = map[string]bool{"unrelated": true}
	dnsServer.mu.Unlock()

	pebble := startDNS01Pebble(t, dnsProvider.server)
	dir, acmeHTTP, serverRoots, containerID := pebble.dir, pebble.client, pebble.roots, pebble.containerID

	cfg := &config.ACMEConfig{
		Email: "ops@example.test", CA: pebble.directory,
		CacheDir: filepath.Join(dir, "cache"), DNSProvider: "rfc2136", Challenge: "dns-01",
		RFC2136: &config.RFC2136Config{
			Server: dnsProvider.server, Observers: append([]string(nil), dnsProvider.observers...), Zone: "example.test", TSIGKeyName: testTSIGName,
			TSIGSecret: testTSIGSecret, PropagationTimeout: config.Duration(5 * time.Second),
			PollInterval: config.Duration(20 * time.Millisecond),
		},
	}
	manager, err := newDNSACMEManager(cfg, nil, acmeHTTP, nil, ACMEOptions{DNSDialContext: (&net.Dialer{}).DialContext})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	names := []string{"*.example.test", "example.test"}
	provider := manager.ProviderForListener(names, names)
	first, err := provider.GetCertificate(dnsTestHello("www.example.test"))
	if err != nil {
		logs, _ := exec.Command("docker", "logs", containerID).CombinedOutput()
		t.Fatalf("Pebble DNS-01 issuance: %v\n%s", err, logs)
	}
	if !equalDNSNames(canonicalDNSNames(first.Leaf.DNSNames), canonicalDNSNames(names)) {
		t.Fatalf("issued SANs = %v", first.Leaf.DNSNames)
	}
	firstRSA, err := provider.GetCertificate(dnsTestRSAHello("www.example.test"))
	if err != nil {
		t.Fatalf("Pebble RSA DNS-01 issuance: %v", err)
	}
	if _, ok := firstRSA.Leaf.PublicKey.(*rsa.PublicKey); !ok {
		t.Fatal("RSA-only client did not receive an RSA certificate")
	}
	assertDNSClean(t, dnsServer)
	runDNS01TLSMatrix(t, manager, cfg, serverRoots, first.Leaf.SerialNumber.String(), firstRSA.Leaf.SerialNumber.String())
	mtls := startDNSRenewalMTLS(t, manager, cfg, serverRoots)

	dynamic := &DynamicCertProvider{}
	dynamic.Set(provider)
	tlsServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-SNI", r.TLS.ServerName)
		w.WriteHeader(http.StatusNoContent)
	}))
	tlsServer.EnableHTTP2 = true
	tlsServer.TLS = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: dynamic.GetCertificate}
	tlsServer.StartTLS()
	t.Cleanup(tlsServer.Close)
	for _, sni := range []string{"example.test", "www.example.test"} {
		if _, err := verifiedPebbleRequest(tlsServer.URL, serverRoots, sni); err != nil {
			t.Fatalf("verified TLS for %s: %v", sni, err)
		}
	}
	for _, sni := range []string{"deep.www.example.test", "outside.test"} {
		if _, err := verifiedPebbleRequest(tlsServer.URL, serverRoots, sni); err == nil {
			t.Fatalf("out-of-scope SNI %s unexpectedly succeeded", sni)
		}
	}

	deadline := time.Now().Add(60 * time.Second)
	var renewed, renewedRSA *tls.Certificate
	for time.Now().Before(deadline) {
		renewed, err = provider.GetCertificate(dnsTestHello("www.example.test"))
		if err == nil {
			renewedRSA, err = provider.GetCertificate(dnsTestRSAHello("www.example.test"))
		}
		if err == nil && renewed.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) != 0 &&
			renewedRSA.Leaf.SerialNumber.Cmp(firstRSA.Leaf.SerialNumber) != 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || renewed == nil || renewedRSA == nil ||
		renewed.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) == 0 ||
		renewedRSA.Leaf.SerialNumber.Cmp(firstRSA.Leaf.SerialNumber) == 0 {
		t.Fatalf("short-lived certificate variants were not renewed: %v", err)
	}
	serial, err := verifiedPebbleRequest(tlsServer.URL, serverRoots, "www.example.test")
	if err != nil || serial != renewed.Leaf.SerialNumber.String() {
		t.Fatalf("renewed certificate not served by a fresh TLS connection: %v", err)
	}
	assertDNSClean(t, dnsServer)
	runDNS01TLSMatrix(t, manager, cfg, serverRoots, renewed.Leaf.SerialNumber.String(), renewedRSA.Leaf.SerialNumber.String())
	mtls.verifyAfterRenewal(t, serverRoots, renewed.Leaf.SerialNumber.String())

	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newDNSACMEManager(cfg, nil, acmeHTTP, nil, ACMEOptions{DNSDialContext: (&net.Dialer{}).DialContext})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restartedProvider := restarted.ProviderForListener(names, names)
	for _, hello := range []*tls.ClientHelloInfo{dnsTestHello("www.example.test"), dnsTestRSAHello("www.example.test")} {
		if _, err := restartedProvider.GetCertificate(hello); err != nil {
			t.Fatalf("load cached certificate after restart: %v", err)
		}
	}
	dynamic.Set(restartedProvider)
	serial, err = verifiedPebbleRequest(tlsServer.URL, serverRoots, "www.example.test")
	if err != nil || serial != renewed.Leaf.SerialNumber.String() {
		t.Fatalf("restart did not serve cached ECDSA certificate: got %s, want %s (RSA %s): %v",
			serial, renewed.Leaf.SerialNumber, renewedRSA.Leaf.SerialNumber, err)
	}
	rsaSerial, err := verifiedPebbleRSARequest(tlsServer.URL, serverRoots, "www.example.test")
	if err != nil || rsaSerial != renewedRSA.Leaf.SerialNumber.String() {
		t.Fatalf("restart did not serve cached RSA certificate: %v", err)
	}
	assertDNSClean(t, dnsServer)
}

type testDNSPebble struct {
	dir         string
	directory   string
	containerID string
	client      *http.Client
	roots       *x509.CertPool
}

func startDNS01Pebble(t *testing.T, dnsServer string) *testDNSPebble {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "pebble", "localhost")
	httpsPort, managementPort := freeTCPPort(t), freeTCPPort(t)
	for managementPort == httpsPort {
		managementPort = freeTCPPort(t)
	}
	pebbleConfig := map[string]any{"pebble": map[string]any{
		"listenAddress":           fmt.Sprintf("127.0.0.1:%d", httpsPort),
		"managementListenAddress": fmt.Sprintf("127.0.0.1:%d", managementPort),
		"certificate":             "/work/" + filepath.Base(certPath),
		"privateKey":              "/work/" + filepath.Base(keyPath),
		"httpPort":                5002,
		"tlsPort":                 5001,
		"keyAlgorithm":            "ecdsa",
		"retryAfter":              map[string]int{"authz": 1, "order": 1},
		"profiles": map[string]any{"default": map[string]any{
			"description": "short-lived integration certificate", "validityPeriod": 45,
		}},
	}}
	rawConfig, err := json.Marshal(pebbleConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pebble.json"), rawConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("jul-dns01-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	authzReuse := os.Getenv("JUL_PEBBLE_AUTHZREUSE")
	if authzReuse == "" {
		authzReuse = "0"
	}
	if authzReuse != "0" && authzReuse != "100" {
		t.Fatal("JUL_PEBBLE_AUTHZREUSE must be 0 or 100")
	}
	cmd := exec.Command("docker", "run", "-d", "--rm", "--name", name, "--network", "host",
		"-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0", "-e", "PEBBLE_AUTHZREUSE="+authzReuse,
		"-v", dir+":/work:ro", pebbleImage, "-config", "/work/pebble.json", "-dnsserver", dnsServer)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start pinned Pebble: %v: %s", err, output)
	}
	containerID := strings.TrimSpace(string(output))
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "stop", "-t", "3", containerID).CombinedOutput(); err != nil {
			t.Errorf("stop Pebble container: %v: %s", err, out)
		}
	})
	caRoots := x509.NewCertPool()
	caServerPEM, err := os.ReadFile(certPath)
	if err != nil || !caRoots.AppendCertsFromPEM(caServerPEM) {
		t.Fatalf("load Pebble HTTPS certificate: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: caRoots, ServerName: "localhost"},
	}}
	rootURL := fmt.Sprintf("https://127.0.0.1:%d/roots/0", managementPort)
	var rootPEM []byte
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, getErr := client.Get(rootURL)
		if getErr == nil {
			rootPEM, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if err == nil && resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		logs, _ := exec.Command("docker", "logs", containerID).CombinedOutput()
		t.Fatalf("Pebble did not become ready: %s", logs)
	}
	return &testDNSPebble{
		dir: dir, directory: fmt.Sprintf("https://127.0.0.1:%d/dir", httpsPort),
		containerID: containerID, client: client, roots: roots,
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func verifiedPebbleRequest(url string, roots *x509.CertPool, sni string) (string, error) {
	return verifiedPebbleRequestWithTLS(url, &tls.Config{RootCAs: roots, ServerName: sni})
}

func verifiedPebbleRSARequest(url string, roots *x509.CertPool, sni string) (string, error) {
	return verifiedPebbleRequestWithTLS(url, &tls.Config{
		RootCAs: roots, ServerName: sni, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
	})
}

func verifiedPebbleRequestWithTLS(url string, config *tls.Config) (string, error) {
	transport := &http.Transport{TLSClientConfig: config}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("X-SNI") != config.ServerName || len(resp.TLS.VerifiedChains) == 0 {
		return "", fmt.Errorf("unexpected response or unverified TLS chain")
	}
	return resp.TLS.PeerCertificates[0].SerialNumber.String(), nil
}

func assertDNSClean(t *testing.T, server *testRFC2136Server) {
	t.Helper()
	waitForDNSACMETest(t, "Pebble challenge cleanup", func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return len(server.records[testOwner]) == 1 && server.records[testOwner]["unrelated"]
	})
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.records[testOwner]) != 1 || !server.records[testOwner]["unrelated"] {
		t.Fatalf("challenge cleanup did not preserve unrelated TXT: %v", server.records[testOwner])
	}
}
