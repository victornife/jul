package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
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

// writeSelfSigned creates a self-signed cert/key for the given DNS names and
// returns their file paths.
func writeSelfSigned(t *testing.T, dir, name string, dnsNames ...string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")

	certOut, _ := os.Create(certPath)
	_ = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	certOut.Close()

	keyBytes, _ := x509.MarshalECPrivateKey(key)
	keyOut, _ := os.Create(keyPath)
	_ = pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	keyOut.Close()
	return certPath, keyPath
}

func TestMinTLSVersion(t *testing.T) {
	if minTLSVersion("1.3") != tls.VersionTLS13 {
		t.Error("1.3 should map to TLS 1.3")
	}
	if minTLSVersion("") != tls.VersionTLS12 {
		t.Error("empty should default to TLS 1.2")
	}
	if minTLSVersion("1.2") != tls.VersionTLS12 {
		t.Error("1.2 should map to TLS 1.2")
	}
}

func TestSNICertSelection(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := writeSelfSigned(t, dir, "a", "a.example.com")
	certB, keyB := writeSelfSigned(t, dir, "wild", "*.svc.example.com")

	servers := []config.ServerConfig{
		{Listen: ":443", ServerNames: []string{"a.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: certA, Key: keyA}},
		{Listen: ":443", ServerNames: []string{"*.svc.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: certB, Key: keyB}},
	}
	conf, err := tlsConfigForAddr(servers, ":443")
	if err != nil {
		t.Fatal(err)
	}
	if conf == nil {
		t.Fatal("expected TLS config")
	}

	pick := func(sni string) *x509.Certificate {
		cert, err := conf.GetCertificate(&tls.ClientHelloInfo{ServerName: sni})
		if err != nil {
			t.Fatalf("GetCertificate(%q): %v", sni, err)
		}
		leaf, _ := x509.ParseCertificate(cert.Certificate[0])
		return leaf
	}

	if cn := pick("a.example.com").Subject.CommonName; cn != "a" {
		t.Errorf("exact SNI -> CN %q, want a", cn)
	}
	if cn := pick("api.svc.example.com").Subject.CommonName; cn != "wild" {
		t.Errorf("wildcard SNI -> CN %q, want wild", cn)
	}
	// Unknown SNI falls back to the first loaded cert.
	if cn := pick("unknown.test").Subject.CommonName; cn != "a" {
		t.Errorf("fallback -> CN %q, want a", cn)
	}
}

func TestTLSConfigForAddrPlainHTTP(t *testing.T) {
	servers := []config.ServerConfig{{Listen: ":80", ServerNames: []string{"x"}}}
	conf, err := tlsConfigForAddr(servers, ":80")
	if err != nil {
		t.Fatal(err)
	}
	if conf != nil {
		t.Fatal("expected nil TLS config for plain HTTP listener")
	}
}

func TestFileCertProviderMissingFile(t *testing.T) {
	servers := []config.ServerConfig{
		{Listen: ":443", TLS: &config.TLSConfig{Enabled: true, Cert: "/nope.crt", Key: "/nope.key"}},
	}
	if _, err := tlsConfigForAddr(servers, ":443"); err == nil {
		t.Fatal("expected error for missing cert files")
	}
}
