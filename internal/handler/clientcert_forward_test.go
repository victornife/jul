// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"

	"jul/internal/middleware"
)

// certRequest builds a proxy request pair carrying the given inbound headers and
// optional certificate identity, and returns the outbound headers Jul would send.
func certRequest(t *testing.T, id *middleware.PeerCertIdentity, inbound http.Header) http.Header {
	t.Helper()
	in := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	in.RemoteAddr = "203.0.113.7:5555"
	for k, vs := range inbound {
		for _, v := range vs {
			in.Header.Add(k, v)
		}
	}
	if id != nil {
		in = in.WithContext(middleware.WithPeerCertIdentity(in.Context(), id))
	}
	out := in.Clone(in.Context())
	setCanonicalXForwarded(&httputil.ProxyRequest{In: in, Out: out})
	return out.Header
}

// TestInboundCertificateAssertionsAreAlwaysStripped is the guarantee that makes
// certificate identity structural rather than a matter of operator diligence:
// the channel is sanitized on every proxied request, including ones where no
// client certificate was negotiated (RFC 9440 §2.4).
func TestInboundCertificateAssertionsAreAlwaysStripped(t *testing.T) {
	spoofed := http.Header{
		"Client-Cert":             []string{":c3Bvb2Y=:"},
		"Client-Cert-Chain":       []string{":c3Bvb2Y=:"},
		"X-Forwarded-Client-Cert": []string{"By=spoof;Hash=deadbeef"},
	}

	t.Run("no client certificate on the connection", func(t *testing.T) {
		got := certRequest(t, nil, spoofed)
		for h := range spoofed {
			if v := got.Get(h); v != "" {
				t.Errorf("%s = %q, want it stripped", h, v)
			}
		}
	})

	t.Run("a verified certificate that is not forwarded", func(t *testing.T) {
		// Raw is empty because the listener did not ask for forwarding, so Jul
		// asserts nothing and the client's assertion must still not survive.
		got := certRequest(t, &middleware.PeerCertIdentity{Verified: true, CN: "client"}, spoofed)
		for h := range spoofed {
			if v := got.Get(h); v != "" {
				t.Errorf("%s = %q, want it stripped", h, v)
			}
		}
	})
}

func TestForwardedCertificateUsesRFC9440Encoding(t *testing.T) {
	leaf := []byte{0x30, 0x82, 0x01, 0x02}
	inter := []byte{0x30, 0x82, 0x03, 0x04}

	t.Run("leaf only", func(t *testing.T) {
		got := certRequest(t, &middleware.PeerCertIdentity{Verified: true, Raw: leaf}, nil)
		want := ":" + base64.StdEncoding.EncodeToString(leaf) + ":"
		if got.Get("Client-Cert") != want {
			t.Errorf("Client-Cert = %q, want %q", got.Get("Client-Cert"), want)
		}
		if v := got.Get("Client-Cert-Chain"); v != "" {
			t.Errorf("Client-Cert-Chain = %q, want it absent without chain forwarding", v)
		}
	})

	t.Run("chain", func(t *testing.T) {
		got := certRequest(t, &middleware.PeerCertIdentity{Verified: true, Raw: leaf, Chain: [][]byte{inter}}, nil)
		if got.Get("Client-Cert") == "" {
			t.Fatal("Client-Cert is absent")
		}
		want := ":" + base64.StdEncoding.EncodeToString(inter) + ":"
		if got.Get("Client-Cert-Chain") != want {
			t.Errorf("Client-Cert-Chain = %q, want %q", got.Get("Client-Cert-Chain"), want)
		}
	})

	t.Run("a client assertion never survives alongside Jul's", func(t *testing.T) {
		got := certRequest(t, &middleware.PeerCertIdentity{Verified: true, Raw: leaf},
			http.Header{"Client-Cert": []string{":c3Bvb2Y=:"}})
		if v := got.Values("Client-Cert"); len(v) != 1 {
			t.Fatalf("Client-Cert has %d values, want exactly 1", len(v))
		}
		if strings.Contains(got.Get("Client-Cert"), "c3Bvb2Y") {
			t.Errorf("Client-Cert = %q, want the client's value replaced", got.Get("Client-Cert"))
		}
	})
}

// TestClientCertRetainsDEROnlyWhenForwarding pins that a deployment which does
// not forward certificates keeps none of the DER on the request.
func TestClientCertRetainsDEROnlyWhenForwarding(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte{0x30, 0x01}, SerialNumber: big.NewInt(1)}
	for _, tt := range []struct {
		forward   string
		wantRaw   bool
		wantChain bool
	}{
		{forward: middleware.ForwardCertNone},
		{forward: "", wantRaw: false},
		{forward: middleware.ForwardCertLeaf, wantRaw: true},
		{forward: middleware.ForwardCertChain, wantRaw: true, wantChain: true},
	} {
		t.Run(tt.forward, func(t *testing.T) {
			var got *middleware.PeerCertIdentity
			h := middleware.ClientCert(false, tt.forward)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = middleware.PeerCertIdentityFrom(r.Context())
			}))
			req := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert, cert}}
			h.ServeHTTP(httptest.NewRecorder(), req)

			if got == nil {
				t.Fatal("no identity was installed")
			}
			if (len(got.Raw) > 0) != tt.wantRaw {
				t.Errorf("Raw retained = %v, want %v", len(got.Raw) > 0, tt.wantRaw)
			}
			if (len(got.Chain) > 0) != tt.wantChain {
				t.Errorf("Chain retained = %v, want %v", len(got.Chain) > 0, tt.wantChain)
			}
		})
	}
}
