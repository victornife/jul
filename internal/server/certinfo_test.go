// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/config"
)

func generateTestCert(t *testing.T, dir string, names []string) (certPath, keyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certFile, err := os.Create(certPath)
	if err != nil {
		t.Fatal(err)
	}
	pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certFile.Close()

	keyFile, err := os.Create(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyFile.Close()
	return certPath, keyPath
}

func TestInspectCertsACME(t *testing.T) {
	cs := InspectCerts([]config.ServerConfig{{
		ServerNames: []string{"a.com"},
		TLS: &config.TLSConfig{
			Enabled: true,
			ACME:    &config.ACMEConfig{Enabled: true},
		},
	}})
	if len(cs) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(cs))
	}
	if cs[0].Source != "acme" {
		t.Errorf("source = %q, want acme", cs[0].Source)
	}
	if cs[0].ServerNames[0] != "a.com" {
		t.Errorf("server names = %v", cs[0].ServerNames)
	}
}

func TestInspectCertsNoTLS(t *testing.T) {
	cs := InspectCerts([]config.ServerConfig{{ServerNames: []string{"a.com"}}})
	if len(cs) != 0 {
		t.Errorf("expected 0 summaries, got %d", len(cs))
	}
}

func TestInspectCertsFile(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateTestCert(t, dir, []string{"example.com", "www.example.com"})

	cs := InspectCerts([]config.ServerConfig{{
		ServerNames: []string{"example.com"},
		TLS: &config.TLSConfig{
			Enabled: true,
			Cert:    certPath,
			Key:     keyPath,
		},
	}})
	if len(cs) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(cs))
	}
	c := cs[0]
	if c.Source != "file" {
		t.Errorf("source = %q, want file", c.Source)
	}
	if c.Subject != "example.com" {
		t.Errorf("subject = %q", c.Subject)
	}
	if len(c.DNSNames) != 2 {
		t.Errorf("dns names = %v", c.DNSNames)
	}
}

func TestInspectCertsFileBadPair(t *testing.T) {
	dir := t.TempDir()
	badCert := filepath.Join(dir, "bad.pem")
	os.WriteFile(badCert, []byte("not a cert"), 0644)

	cs := InspectCerts([]config.ServerConfig{{
		ServerNames: []string{"a.com"},
		TLS: &config.TLSConfig{
			Enabled: true,
			Cert:    badCert,
			Key:     badCert,
		},
	}})
	if len(cs) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(cs))
	}
	if cs[0].Error == "" {
		t.Error("expected error for bad cert")
	}
}

func TestInspectCertsMultipleServers(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateTestCert(t, dir, []string{"b.com"})

	cs := InspectCerts([]config.ServerConfig{
		{ServerNames: []string{"a.com"}, TLS: &config.TLSConfig{Enabled: true}},
		{ServerNames: []string{"b.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: certPath, Key: keyPath}},
	})
	if len(cs) != 2 {
		t.Errorf("expected 2 summaries, got %d", len(cs))
	}
}
