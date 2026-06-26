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
// returns their file paths. It takes testing.TB so both tests and benchmarks
// can use it.
func writeSelfSigned(tb testing.TB, dir, name string, dnsNames ...string) (certPath, keyPath string) {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
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
		tb.Fatal(err)
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

// TestPreflightTLS proves the apply preflight rejects broken file-based TLS
// certificates up front, while skipping plaintext and ACME-served addresses
// (whose certificates are obtained at handshake time).
func TestPreflightTLS(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeSelfSigned(t, dir, "good", "example.com")

	t.Run("valid file pair passes", func(t *testing.T) {
		servers := []config.ServerConfig{{
			Listen: ":443",
			TLS:    &config.TLSConfig{Enabled: true, Cert: cert, Key: key},
		}}
		if err := PreflightTLS(servers); err != nil {
			t.Fatalf("PreflightTLS: %v", err)
		}
	})

	t.Run("missing cert file fails", func(t *testing.T) {
		servers := []config.ServerConfig{{
			Listen: ":443",
			TLS:    &config.TLSConfig{Enabled: true, Cert: filepath.Join(dir, "nope.crt"), Key: key},
		}}
		if err := PreflightTLS(servers); err == nil {
			t.Fatal("expected an error for a missing certificate file")
		}
	})

	t.Run("cert/key mismatch fails", func(t *testing.T) {
		_, otherKey := writeSelfSigned(t, dir, "other", "other.com")
		servers := []config.ServerConfig{{
			Listen: ":443",
			TLS:    &config.TLSConfig{Enabled: true, Cert: cert, Key: otherKey},
		}}
		if err := PreflightTLS(servers); err == nil {
			t.Fatal("expected an error for a cert/key mismatch")
		}
	})

	t.Run("plaintext server skipped", func(t *testing.T) {
		servers := []config.ServerConfig{{Listen: ":80"}}
		if err := PreflightTLS(servers); err != nil {
			t.Fatalf("plaintext server should pass: %v", err)
		}
	})

	t.Run("acme address skips file validation", func(t *testing.T) {
		// No cert/key files are set, but ACME is enabled for the address, so the
		// certificate is obtained at handshake time — preflight must not fail.
		servers := []config.ServerConfig{{
			Listen: ":443",
			TLS: &config.TLSConfig{
				Enabled: true,
				ACME:    &config.ACMEConfig{Enabled: true, Domains: []string{"example.com"}},
			},
		}}
		if err := PreflightTLS(servers); err != nil {
			t.Fatalf("acme-enabled address should skip file validation: %v", err)
		}
	})
}

// TestACMERestartRequired pins the hot-apply policy for ACME changes: the
// autocert manager freezes its issued-domain set and issuer at startup, so
// introducing ACME or changing those parameters requires a restart, while an
// unchanged set or removing ACME entirely can hot-apply on the next reload.
func TestACMERestartRequired(t *testing.T) {
	acme := func(domains ...string) []config.ServerConfig {
		return []config.ServerConfig{{
			Listen: ":443",
			TLS: &config.TLSConfig{
				Enabled: true,
				ACME:    &config.ACMEConfig{Enabled: true, Email: "ops@example.com", Domains: domains},
			},
		}}
	}
	plain := func() []config.ServerConfig {
		return []config.ServerConfig{{Listen: ":80"}}
	}

	t.Run("introducing acme requires restart", func(t *testing.T) {
		reason, need := ACMERestartRequired(plain(), acme("example.com"))
		if !need {
			t.Fatal("introducing ACME must require a restart")
		}
		if reason == "" {
			t.Fatal("a non-empty reason is expected")
		}
	})

	t.Run("adding a domain requires restart", func(t *testing.T) {
		if _, need := ACMERestartRequired(acme("a.example.com"), acme("a.example.com", "b.example.com")); !need {
			t.Fatal("growing the ACME domain set must require a restart")
		}
	})

	t.Run("removing a domain requires restart", func(t *testing.T) {
		if _, need := ACMERestartRequired(acme("a.example.com", "b.example.com"), acme("a.example.com")); !need {
			t.Fatal("shrinking the ACME domain set must require a restart")
		}
	})

	t.Run("changing the issuer email requires restart", func(t *testing.T) {
		next := acme("example.com")
		next[0].TLS.ACME.Email = "new@example.com"
		if _, need := ACMERestartRequired(acme("example.com"), next); !need {
			t.Fatal("changing the ACME issuer email must require a restart")
		}
	})

	t.Run("unchanged set does not require restart", func(t *testing.T) {
		if _, need := ACMERestartRequired(acme("b.example.com", "a.example.com"), acme("a.example.com", "b.example.com")); need {
			t.Fatal("an unchanged ACME set (order-insensitive) must not require a restart")
		}
	})

	t.Run("removing acme does not require restart", func(t *testing.T) {
		if _, need := ACMERestartRequired(acme("example.com"), plain()); need {
			t.Fatal("removing ACME hot-applies via provider swap; no restart required")
		}
	})
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
