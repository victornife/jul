// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package backendtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pki is a throwaway certificate authority for the tests.
type pki struct {
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	pemDER []byte
}

func newPKI(t *testing.T, name string) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &pki{cert: cert, key: key, pemDER: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue signs a leaf certificate with the given SANs and returns PEM bytes.
func (p *pki) issue(t *testing.T, cn string, dnsNames []string, uris []string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var parsedURIs []*url.URL
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		parsedURIs = append(parsedURIs, u)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     dnsNames,
		URIs:         parsedURIs,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.cert, &key.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string // "" means accepted
	}{
		{name: "empty block", opts: Options{}},
		{name: "system default", opts: Options{MinVersion: "1.3"}},
		{name: "file only", opts: Options{CAMode: CAModeFileOnly, CAFile: "/tmp/ca.pem"}},
		{name: "system and file", opts: Options{CAMode: CAModeSystemAndFile, CAFile: "/tmp/ca.pem"}},
		{name: "client pair", opts: Options{ClientCert: "/tmp/c.pem", ClientKey: "/tmp/c.key"}},
		{name: "peer identities", opts: Options{PeerIdentities: []string{"dns:a.example", "uri:spiffe://x/y"}}},
		{name: "insecure alone", opts: Options{InsecureSkipVerify: true}},

		{name: "unknown ca mode", opts: Options{CAMode: "trust_everything"}, want: "ca_mode"},
		{name: "file mode without file", opts: Options{CAMode: CAModeFileOnly}, want: "ca_file: required"},
		{name: "ca file ignored under system", opts: Options{CAFile: "/tmp/ca.pem"}, want: "set ca_mode"},
		{name: "cert without key", opts: Options{ClientCert: "/tmp/c.pem"}, want: "both are required"},
		{name: "key without cert", opts: Options{ClientKey: "/tmp/c.key"}, want: "both are required"},
		{name: "server name with port", opts: Options{ServerName: "svc.internal:8443"}, want: "must not carry a port"},
		{name: "server name wildcard", opts: Options{ServerName: "*.internal"}, want: "concrete name"},
		{name: "server name url", opts: Options{ServerName: "https://svc.internal"}, want: "not a URL"},
		{name: "bad min version", opts: Options{MinVersion: "1.1"}, want: "min_version"},
		{name: "unprefixed identity", opts: Options{PeerIdentities: []string{"svc.internal"}}, want: "must start with"},
		{name: "empty dns identity", opts: Options{PeerIdentities: []string{"dns:"}}, want: "empty DNS name"},
		{name: "duplicate identity", opts: Options{PeerIdentities: []string{"dns:a.example", "dns:A.example."}}, want: "duplicate identity"},
		{name: "insecure with identities", opts: Options{InsecureSkipVerify: true, PeerIdentities: []string{"dns:a.example"}}, want: "cannot be combined with peer_identities"},
		{name: "insecure with ca mode", opts: Options{InsecureSkipVerify: true, CAMode: CAModeFileOnly, CAFile: "/tmp/ca.pem"}, want: "cannot be combined with ca_mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(tt.opts)
			if tt.want == "" {
				if len(errs) > 0 {
					t.Fatalf("Validate rejected a valid block: %v", errs)
				}
				return
			}
			var joined string
			for _, e := range errs {
				joined += e.Error() + "\n"
			}
			if !strings.Contains(joined, tt.want) {
				t.Fatalf("errors = %q, want one containing %q", joined, tt.want)
			}
		})
	}
}

func TestResolveTrustRoots(t *testing.T) {
	dir := t.TempDir()
	ca := newPKI(t, "backend-ca")
	caPath := writeFile(t, dir, "ca.pem", ca.pemDER)
	emptyPath := writeFile(t, dir, "empty.pem", []byte("not a certificate"))

	t.Run("system default has no explicit pool", func(t *testing.T) {
		p, err := Resolve(Options{MinVersion: MinVersion13}, "svc.internal")
		if err != nil {
			t.Fatal(err)
		}
		if p.ClientConfig().RootCAs != nil {
			t.Error("the system mode must leave RootCAs nil so Go uses the platform pool")
		}
		if p.Metadata().CAMode != CAModeSystem {
			t.Errorf("ca_mode = %q", p.Metadata().CAMode)
		}
	})

	t.Run("file only", func(t *testing.T) {
		p, err := Resolve(Options{CAMode: CAModeFileOnly, CAFile: caPath}, "svc.internal")
		if err != nil {
			t.Fatal(err)
		}
		if p.ClientConfig().RootCAs == nil {
			t.Fatal("file_only must install a pool")
		}
	})

	t.Run("system and file", func(t *testing.T) {
		p, err := Resolve(Options{CAMode: CAModeSystemAndFile, CAFile: caPath}, "svc.internal")
		if err != nil {
			t.Fatal(err)
		}
		if p.ClientConfig().RootCAs == nil {
			t.Fatal("system_and_file must install a pool")
		}
	})

	for _, tt := range []struct {
		name, path, want string
	}{
		{name: "missing file", path: filepath.Join(dir, "nope.pem"), want: "not readable"},
		{name: "no usable certificate", path: emptyPath, want: "no usable PEM certificate"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve(Options{CAMode: CAModeFileOnly, CAFile: tt.path}, "svc.internal")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestResolveClientCertificate(t *testing.T) {
	dir := t.TempDir()
	ca := newPKI(t, "client-ca")
	certPEM, keyPEM := ca.issue(t, "jul-client", []string{"jul.client"}, nil)
	certPath := writeFile(t, dir, "client.pem", certPEM)
	keyPath := writeFile(t, dir, "client.key", keyPEM)

	p, err := Resolve(Options{ClientCert: certPath, ClientKey: keyPath}, "svc.internal")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg := p.ClientConfig()
	if len(cfg.Certificates) != 1 {
		t.Fatalf("client certificate not installed: %+v", cfg.Certificates)
	}
	meta := p.Metadata()
	if !strings.Contains(meta.ClientCertSubject, "jul-client") {
		t.Errorf("subject = %q, want the leaf CN", meta.ClientCertSubject)
	}
	if meta.ClientCertNotAfter == "" {
		t.Error("expiry metadata is missing")
	}

	// A mismatched pair must fail at resolution, not at the first handshake.
	otherCert, _ := newPKI(t, "other-ca").issue(t, "other", []string{"other"}, nil)
	mismatchPath := writeFile(t, dir, "other.pem", otherCert)
	if _, err := Resolve(Options{ClientCert: mismatchPath, ClientKey: keyPath}, "svc.internal"); err == nil {
		t.Fatal("a mismatched certificate/key pair was accepted")
	}
}

func TestResolveServerName(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		logical string
		want    string
	}{
		{name: "explicit override wins", opts: Options{ServerName: "inventory.internal"}, logical: "10.0.0.5:8443", want: "inventory.internal"},
		{name: "derived from the logical host", opts: Options{MinVersion: MinVersion12}, logical: "inventory.internal", want: "inventory.internal"},
		{name: "port stripped", opts: Options{MinVersion: MinVersion12}, logical: "inventory.internal:8443", want: "inventory.internal"},
		{name: "ipv6 brackets stripped", opts: Options{MinVersion: MinVersion12}, logical: "[2001:db8::1]:8443", want: "2001:db8::1"},
		{name: "no logical host", opts: Options{MinVersion: MinVersion12}, logical: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Resolve(tt.opts, tt.logical)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.ServerName(); got != tt.want {
				t.Fatalf("server name = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveMinVersion(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want uint16
	}{
		{in: "", want: tls.VersionTLS12},
		{in: MinVersion12, want: tls.VersionTLS12},
		{in: MinVersion13, want: tls.VersionTLS13},
	} {
		p, err := Resolve(Options{MinVersion: tt.in, ServerName: "svc.internal"}, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := p.ClientConfig().MinVersion; got != tt.want {
			t.Errorf("min_version %q = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// TestPolicyVerifiesRealHandshake drives an actual TLS handshake, because the
// only convincing proof that a policy verifies is a connection that succeeds
// against the right peer and fails against the wrong one.
func TestPolicyVerifiesRealHandshake(t *testing.T) {
	dir := t.TempDir()
	ca := newPKI(t, "backend-ca")
	caPath := writeFile(t, dir, "ca.pem", ca.pemDER)
	serverCert, serverKey := ca.issue(t, "backend", []string{"inventory.internal"}, []string{"spiffe://example/inventory"})
	pair, err := tls.X509KeyPair(serverCert, serverKey)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = conn.(*tls.Conn).Handshake()
				_ = conn.Close()
			}()
		}
	}()

	dial := func(p *Policy) error {
		conn, err := tls.Dial("tcp", ln.Addr().String(), p.ClientConfig())
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		return conn.Handshake()
	}

	tests := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{
			name: "private CA with the right name",
			opts: Options{CAMode: CAModeFileOnly, CAFile: caPath, ServerName: "inventory.internal"},
		},
		{
			name:    "private CA with the wrong name",
			opts:    Options{CAMode: CAModeFileOnly, CAFile: caPath, ServerName: "wrong.internal"},
			wantErr: "certificate is valid for",
		},
		{
			name:    "system roots do not trust a private CA",
			opts:    Options{ServerName: "inventory.internal", MinVersion: MinVersion12},
			wantErr: "certificate",
		},
		{
			name: "matching dns peer identity",
			opts: Options{CAMode: CAModeFileOnly, CAFile: caPath, ServerName: "inventory.internal", PeerIdentities: []string{"dns:inventory.internal"}},
		},
		{
			name: "matching uri peer identity",
			opts: Options{CAMode: CAModeFileOnly, CAFile: caPath, ServerName: "inventory.internal", PeerIdentities: []string{"uri:spiffe://example/inventory"}},
		},
		{
			name: "one matching identity out of several is enough",
			opts: Options{CAMode: CAModeFileOnly, CAFile: caPath, ServerName: "inventory.internal", PeerIdentities: []string{"dns:other.internal", "uri:spiffe://example/inventory"}},
		},
		{
			name:    "no matching peer identity",
			opts:    Options{CAMode: CAModeFileOnly, CAFile: caPath, ServerName: "inventory.internal", PeerIdentities: []string{"dns:other.internal"}},
			wantErr: "matches none of the",
		},
		{
			name:    "peer identity does not substitute for chain verification",
			opts:    Options{ServerName: "inventory.internal", PeerIdentities: []string{"dns:inventory.internal"}},
			wantErr: "certificate",
		},
		{
			name: "insecure bypass reaches the peer",
			opts: Options{InsecureSkipVerify: true, ServerName: "anything.invalid"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Resolve(tt.opts, "inventory.internal")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			err = dial(p)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("handshake failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("handshake succeeded, want a verification failure")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestFingerprintTracksContentNotPaths(t *testing.T) {
	dir := t.TempDir()
	ca := newPKI(t, "rotating-ca")
	caPath := writeFile(t, dir, "ca.pem", ca.pemDER)
	opts := Options{CAMode: CAModeFileOnly, CAFile: caPath, ServerName: "svc.internal"}

	first, err := Resolve(opts, "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := Resolve(opts, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() != again.Fingerprint() {
		t.Fatal("the same configuration and material must fingerprint identically")
	}

	// Rotate the file in place, leaving the configured path untouched.
	rotated := newPKI(t, "rotating-ca-2")
	if err := os.WriteFile(caPath, rotated.pemDER, 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := Resolve(opts, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Fingerprint() == first.Fingerprint() {
		t.Fatal("an in-place rotation must change the fingerprint")
	}

	// A policy difference that changes behaviour must change it too.
	changed := opts
	changed.MinVersion = MinVersion13
	other, err := Resolve(changed, "")
	if err != nil {
		t.Fatal(err)
	}
	if other.Fingerprint() == after.Fingerprint() {
		t.Fatal("a different minimum version must fingerprint differently")
	}
}

// TestClientConfigIsIndependentPerConsumer proves the sharing contract: one
// consumer mutating its own *tls.Config cannot affect another's.
func TestClientConfigIsIndependentPerConsumer(t *testing.T) {
	p, err := Resolve(Options{ServerName: "svc.internal", MinVersion: MinVersion13}, "")
	if err != nil {
		t.Fatal(err)
	}
	a := p.ClientConfig()
	a.NextProtos = []string{"h2"}
	a.ServerName = "mutated.invalid"
	a.InsecureSkipVerify = true

	b := p.ClientConfig()
	if len(b.NextProtos) != 0 || b.ServerName != "svc.internal" || b.InsecureSkipVerify {
		t.Fatalf("a second consumer observed another's mutation: %+v", b)
	}
	if p.ServerName() != "svc.internal" {
		t.Fatal("the policy itself was mutated")
	}
}

func TestNilPolicyIsUsable(t *testing.T) {
	var p *Policy
	cfg := p.ClientConfig()
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("nil policy min version = %d, want TLS 1.2", cfg.MinVersion)
	}
	if p.InsecureSkipVerify() || p.Fingerprint() != "" || p.ServerName() != "" {
		t.Error("a nil policy must read as no policy")
	}
	if meta := p.Metadata(); meta.CAMode != CAModeSystem || meta.MinVersion != MinVersion12 {
		t.Errorf("nil metadata = %+v", meta)
	}
}

func TestResolveEmptyOptionsYieldsNoPolicy(t *testing.T) {
	p, err := Resolve(Options{}, "svc.internal")
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("an empty block must resolve to no policy, got %+v", p)
	}
}

func TestMetadataCarriesNoSecret(t *testing.T) {
	dir := t.TempDir()
	ca := newPKI(t, "meta-ca")
	certPEM, keyPEM := ca.issue(t, "jul-client", []string{"jul.client"}, nil)
	opts := Options{
		CAMode:         CAModeFileOnly,
		CAFile:         writeFile(t, dir, "ca.pem", ca.pemDER),
		ClientCert:     writeFile(t, dir, "client.pem", certPEM),
		ClientKey:      writeFile(t, dir, "client.key", keyPEM),
		ServerName:     "svc.internal",
		PeerIdentities: []string{"dns:svc.internal"},
	}
	p, err := Resolve(opts, "")
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.Join([]string{
		p.Metadata().CAMode, p.Metadata().ServerName, p.Metadata().MinVersion,
		p.Metadata().ClientCertSubject, p.Metadata().ClientCertNotAfter,
		p.Metadata().Fingerprint, strings.Join(p.Metadata().PeerIdentities, ","),
	}, "|")

	for _, secret := range []string{
		"PRIVATE KEY", "BEGIN CERTIFICATE", string(keyPEM), string(certPEM), string(ca.pemDER),
		opts.CAFile, opts.ClientCert, opts.ClientKey,
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("metadata leaked %q", secret[:min(len(secret), 32)])
		}
	}
}

func TestParseIdentity(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "dns:svc.internal", want: "dns:svc.internal"},
		{in: "dns:SVC.Internal.", want: "dns:svc.internal"},
		{in: " dns:svc.internal ", want: "dns:svc.internal"},
		{in: "uri:spiffe://example/workload", want: "uri:spiffe://example/workload"},
		{in: "svc.internal", wantErr: true},
		{in: "dns:", wantErr: true},
		{in: "uri:", wantErr: true},
		{in: "spiffe://example/workload", wantErr: true},
		{in: "", wantErr: true},
		{in: "dns:a b", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseIdentity(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseIdentity(%q) = %v, want an error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseIdentity(%q): %v", tt.in, err)
			continue
		}
		if got.String() != tt.want {
			t.Errorf("ParseIdentity(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
