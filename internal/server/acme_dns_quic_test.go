// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme && http3

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"jul/internal/config"
)

func TestDNS01QUICResumptionEnforcesLiveSNIScopeWithMTLS(t *testing.T) {
	m := testDNSManager(t, t.TempDir(), "https://ca.example.test/directory")
	names := []string{"*.example.test"}
	issuer := newOCSPTestCA(t)
	now := time.Now()
	cert := dnsTestCertificate(t, issuer, names, 33, now.Add(-time.Hour), now.Add(12*time.Hour))
	m.issue = dnsECDSAIssue(func(context.Context, []string) (*tls.Certificate, error) { return cert, nil })
	dynamic := &DynamicCertProvider{}
	dynamic.Set(m.ProviderForListener(names, []string{"first.example.test"}))
	clientCA := newCA(t)
	caFile := writePEM(t, t.TempDir(), "client-ca.pem", clientCA.pem)
	bundle, err := clientAuthForAddr([]config.ServerConfig{{
		Listen: ":443", TLS: &config.TLSConfig{Enabled: true,
			ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: caFile}},
	}}, ":443", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, clientCert := clientCA.clientCert(t, "client", 1, nil, nil)
	auth := &dynamicClientAuth{}
	auth.set(bundle)
	base := &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: dynamic.GetCertificate}
	base.GetConfigForClient = dynamic.configForClient(auth.configForClient(base))
	h3, err := newStagedHTTP3WithTLS("127.0.0.1:0", base, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), nil, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := h3.Activate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h3.Close(context.Background()) })
	addr := h3.(*h3Conn).ln.Addr().String()
	roots := x509.NewCertPool()
	roots.AddCert(issuer.issuer)
	cache := &sharedTLSSessionCache{}
	request := func(name string, certs []tls.Certificate, sessions tls.ClientSessionCache) (bool, error) {
		transport := &http3.Transport{TLSClientConfig: &tls.Config{
			RootCAs: roots, ServerName: name, Certificates: certs, ClientSessionCache: sessions,
		}}
		defer transport.Close()
		resp, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get("https://" + addr + "/")
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		_, err = io.Copy(io.Discard, resp.Body)
		if err != nil {
			return false, err
		}
		return resp.TLS.DidResume, nil
	}
	certs := []tls.Certificate{clientCert}
	if resumed, err := request("first.example.test", certs, cache); err != nil || resumed {
		t.Fatalf("initial QUIC handshake: resumed=%v err=%v", resumed, err)
	}
	if resumed, err := request("first.example.test", certs, cache); err != nil || !resumed {
		t.Fatalf("allowed QUIC session did not resume: resumed=%v err=%v", resumed, err)
	}
	if _, err := request("second.example.test", certs, cache); err == nil {
		t.Fatal("resumed QUIC handshake bypassed the listener SNI scope")
	}
	dynamic.Set(m.ProviderForListener(names, []string{"second.example.test"}))
	if _, err := request("first.example.test", certs, cache); err == nil {
		t.Fatal("removed SNI resumed over QUIC after the policy swap")
	}
	if _, err := request("second.example.test", certs, cache); err != nil {
		t.Fatalf("new QUIC listener SNI rejected: %v", err)
	}
	if _, err := request("second.example.test", nil, nil); err == nil {
		t.Fatal("QUIC listener accepted a client without the required certificate")
	}
}
