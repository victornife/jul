// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDNS01OfflineCommandsAreSecretSafeAndDoNotIssue(t *testing.T) {
	const secret = "c3ludGhldGljLXRzaWcta2V5LWZpeHR1cmU="
	t.Setenv("JUL_DNS_CLI_TSIG", secret)
	var caRequests, dnsConnections atomic.Int32
	ca := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		caRequests.Add(1)
		http.Error(w, "unexpected CA request", http.StatusInternalServerError)
	}))
	defer ca.Close()
	dns, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := dns.Accept()
			if err != nil {
				return
			}
			dnsConnections.Add(1)
			_ = conn.Close()
		}
	}()
	defer func() { _ = dns.Close(); <-done }()

	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	raw := fmt.Sprintf(`
[[servers]]
listen = "127.0.0.1:8443"
server_names = ["example.test", "*.example.test"]
  [servers.tls]
  enabled = true
    [servers.tls.acme]
    enabled = true
    email = "ops@example.test"
    ca = %q
    challenge = "dns-01"
    dns_provider = "rfc2136"
      [servers.tls.acme.rfc2136]
      server = %q
      observers = [%q]
      zone = "example.test"
      tsig_key_name = "jul.example.test"
      tsig_secret = "${env:JUL_DNS_CLI_TSIG}"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = %q
`, ca.URL+"/directory", dns.Addr().String(), dns.Addr().String(), dir)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "support.tar.gz")
	commands := []struct {
		name string
		run  func() int
	}{
		{"check", func() int { return cmdCheck([]string{"-config", path}) }},
		{"lint", func() int { return cmdLint([]string{"-config", path, "-json"}) }},
		{"doctor", func() int { return cmdDoctor([]string{"-config", path, "-json"}) }},
		{"support-bundle", func() int {
			return cmdSupportBundle([]string{"-config", path, "-output", archivePath, "-json"})
		}},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			withCLIOutput(t, func(out, errOut *bytes.Buffer) {
				if code := command.run(); code != 0 {
					t.Fatalf("exit %d: %s %s", code, out, errOut)
				}
				if strings.Contains(out.String(), secret) || strings.Contains(errOut.String(), secret) {
					t.Fatal("TSIG secret escaped in command output")
				}
			})
		})
	}
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), secret) {
			t.Fatalf("TSIG secret escaped in bundle entry %s", header.Name)
		}
	}
	if caRequests.Load() != 0 || dnsConnections.Load() != 0 {
		t.Fatalf("offline commands made %d CA requests and %d DNS connections", caRequests.Load(), dnsConnections.Load())
	}
}
