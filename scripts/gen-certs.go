//go:build ignore

// Generate fresh TLS certificates for burn-in testing.
// Creates: CA cert+key, server cert+key (localhost), client cert+key.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

func main() {
	tlsDir := "testdata/tls"
	os.MkdirAll(tlsDir, 0755)

	// Generate CA key pair
	caPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	caTemplate := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "burn-in-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caPriv.PublicKey, caPriv)
	if err != nil {
		panic(err)
	}

	// Write CA cert (overwrites clients-ca.pem)
	caPath := filepath.Join(tlsDir, "clients-ca.pem")
	f, _ := os.Create(caPath)
	pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	f.Close()
	fmt.Println("Wrote CA cert:", caPath)

	// Generate server key pair
	serverPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	serverTemplate := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  nil, // no IP SAN needed if client uses InsecureSkipVerify
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, &serverTemplate, &caTemplate, &serverPriv.PublicKey, caPriv)
	if err != nil {
		panic(err)
	}

	// Write server cert
	certPath := filepath.Join(tlsDir, "localhost.crt")
	f, _ = os.Create(certPath)
	pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	f.Close()

	// Write server key
	keyPath := filepath.Join(tlsDir, "localhost.key")
	f, _ = os.Create(keyPath)
	keyDER, _ := x509.MarshalECPrivateKey(serverPriv)
	pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	f.Close()
	fmt.Println("Wrote server cert:", certPath)
	fmt.Println("Wrote server key :", keyPath)

	// Generate client cert for mTLS
	clientPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	clientTemplate := x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "burn-in-client"},
		DNSNames:     []string{"burn-in-client"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, &clientTemplate, &caTemplate, &clientPriv.PublicKey, caPriv)
	if err != nil {
		panic(err)
	}

	// Write client cert
	clientCertPath := filepath.Join(tlsDir, "client.crt")
	f, _ = os.Create(clientCertPath)
	pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
	f.Close()

	// Write client key
	clientKeyPath := filepath.Join(tlsDir, "client.key")
	f, _ = os.Create(clientKeyPath)
	keyDER, _ = x509.MarshalECPrivateKey(clientPriv)
	pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	f.Close()
	fmt.Println("Wrote client cert:", clientCertPath)
	fmt.Println("Wrote client key :", clientKeyPath)

	fmt.Println("All certificates generated successfully (valid 1 year)")
}
